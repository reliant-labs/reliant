package streaming

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/streaming"
)

// orgUpdatesRepo serves one user's update log and the orgs of what it is
// about: chats, projects and saved workflows in acme, home and globex.
type orgUpdatesRepo struct {
	Repository
	log []db.UserUpdate
}

func orgRow(id string) *string {
	org := map[byte]string{'a': "acme", 'h': "home", 'g': "globex"}[id[0]]
	return &org
}

func (r orgUpdatesRepo) GetLatestUserUpdateSequence(context.Context, string) (int64, error) {
	return int64(len(r.log)), nil
}

func (r orgUpdatesRepo) GetUserUpdatesSince(_ context.Context, _ string, since int64, limit int) ([]db.UserUpdate, error) {
	var out []db.UserUpdate
	for _, u := range r.log {
		if u.SequenceNumber > since && len(out) < limit {
			out = append(out, u)
		}
	}
	return out, nil
}

func (orgUpdatesRepo) GetChatRow(_ context.Context, id string) (*db.ChatRow, error) {
	return &db.ChatRow{ID: id, UserID: raceTestUserID, OrgID: orgRow(id)}, nil
}

func (orgUpdatesRepo) GetProject(_ context.Context, id string) (*db.Project, error) {
	return &db.Project{ID: id, UserID: raceTestUserID, OrgID: orgRow(id)}, nil
}

func (orgUpdatesRepo) GetWorkflowDraft(_ context.Context, id string) (*db.WorkflowDraft, error) {
	if strings.HasPrefix(id, "gone") {
		return nil, svcerr.NotFound("workflow draft")
	}
	return &db.WorkflowDraft{ID: id, UserID: raceTestUserID, OrgID: orgRow(id)}, nil
}

// orgMembers is a live membership of the orgs it holds; an org can be
// removed while a stream is open.
type orgMembers struct {
	mu   sync.Mutex
	orgs map[string]bool
}

func (m *orgMembers) IsMember(_ context.Context, _, orgID string) (bool, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.orgs[orgID], true, nil
}

func (m *orgMembers) remove(org string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.orgs, org)
}

// update is one row of the log: about a chat ("c:<id>"), a project
// ("p:<id>"), a saved workflow ("d:<id>") or nothing of an org ("personal").
func update(seq int64, about string) db.UserUpdate {
	u := db.UserUpdate{ID: about, UserID: raceTestUserID, SequenceNumber: seq, EntityID: about}
	id := about[min(2, len(about)):]
	switch about[:min(2, len(about))] {
	case "c:":
		u.ChatID = &id
	case "p:":
		u.ProjectID = &id
	case "d:":
		u.EntityType = db.EntityTypeWorkflowDraft
		u.EntityID = id
	}
	return u
}

func orgUserStream(t *testing.T, repo Repository, hub streaming.UpdateHub[db.UserUpdate], m authz.Membership) <-chan *reliantv1.UserStreamEvent {
	t.Helper()
	svc := NewTestStreaming(t, WithStreamingDeps(Deps{Repo: repo, Deltas: noopHub{}, UserUpdates: hub}))
	mux := http.NewServeMux()
	path, handler := reliantv1connect.NewStreamingServiceHandler(svc, connect.WithInterceptors(svcerr.Interceptor()))
	// A request through a listener carries the membership authz decides with.
	mux.Handle(path, authInjector(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(authz.WithMembership(r.Context(), m)))
	})))
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	srv.EnableHTTP2 = true
	srv.Start()
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	stream, err := reliantv1connect.NewStreamingServiceClient(srv.Client(), srv.URL).
		StreamUserUpdates(ctx, connect.NewRequest(&reliantv1.StreamUserUpdatesRequest{}))
	require.NoError(t, err)
	return receiveAll(t, stream)
}

func nextBatch(t *testing.T, events <-chan *reliantv1.UserStreamEvent) (ids []string, latest int64) {
	t.Helper()
	batch := nextEvent(t, events, "an update batch", func(ev *reliantv1.UserStreamEvent) bool { return ev.GetUpdates() != nil }).GetUpdates()
	for _, u := range batch.GetUpdates() {
		ids = append(ids, u.GetId())
	}
	return ids, batch.GetLatestSequence()
}

// Replayed from the log, an update about a chat, project or saved workflow of
// an org the caller has left is withheld; personal and live-org updates
// arrive, and the batch's cursor is past every row, withheld or not.
func TestStreamUserUpdates_ReplayWithholdsALeftOrgsUpdates(t *testing.T) {
	repo := orgUpdatesRepo{log: []db.UserUpdate{
		update(1, "c:globex-chat"), update(2, "p:globex-project"), update(3, "d:globex-draft"),
		update(4, "personal"), update(5, "c:acme-chat"), update(6, "p:home-project"), update(7, "d:acme-draft"),
		update(8, "c:globex-chat-2"),
	}}
	events := orgUserStream(t, repo, nil, &orgMembers{orgs: map[string]bool{"acme": true, "home": true}})

	nextEvent(t, events, "sync", func(ev *reliantv1.UserStreamEvent) bool { return ev.GetSync() != nil })
	ids, latest := nextBatch(t, events)
	require.Equal(t, []string{"personal", "c:acme-chat", "p:home-project", "d:acme-draft"}, ids)
	require.Equal(t, int64(8), latest, "the cursor moves past the withheld rows")
}

// On an open stream, updates about an org stop the moment its member is
// removed; their personal updates and those of an org they are still in keep
// arriving, and each batch's cursor is past the withheld ones.
func TestStreamUserUpdates_RemovalWhileOpenWithholdsTheOrgsUpdates(t *testing.T) {
	members := &orgMembers{orgs: map[string]bool{"acme": true, "home": true}}
	hub := newChanUserHub()
	events := orgUserStream(t, orgUpdatesRepo{}, hub, members)
	nextEvent(t, events, "sync", func(ev *reliantv1.UserStreamEvent) bool { return ev.GetSync() != nil })
	<-hub.subscribed

	send := func(u db.UserUpdate) {
		hub.events <- streaming.UpdateEvent[db.UserUpdate]{SequenceNumber: u.SequenceNumber, Payload: u}
	}
	send(update(1, "c:acme-chat"))
	ids, latest := nextBatch(t, events)
	require.Equal(t, []string{"c:acme-chat"}, ids, "a member's org update arrives")
	require.Equal(t, int64(1), latest)

	members.remove("acme")
	send(update(2, "c:acme-chat"))
	send(update(3, "p:acme-project"))
	send(update(4, "personal"))
	ids, latest = nextBatch(t, events)
	require.Equal(t, []string{"personal"}, ids, "nothing of acme after the removal")
	require.Equal(t, int64(4), latest, "the cursor moved past the withheld updates")

	send(update(5, "d:home-draft"))
	ids, _ = nextBatch(t, events)
	require.Equal(t, []string{"d:home-draft"}, ids, "an org the caller is still in")
}

// A deleted saved workflow's history keeps the org it was stored with: a
// member who has left that org replays none of it, and at most the deletion,
// stripped to the draft's id. An update written before orgs about a resource
// that is gone is withheld too, unless it is a deletion.
func TestStreamUserUpdates_ReplayOfADeletedOrgDraft(t *testing.T) {
	acme := "acme"
	stamped := func(u db.UserUpdate, org *string, data string) db.UserUpdate {
		u.OrgID, u.Data = org, json.RawMessage(data)
		u.UpdateType = db.UserUpdateWorkflowDraftUpdated
		return u
	}
	repo := orgUpdatesRepo{log: []db.UserUpdate{
		stamped(update(1, "d:gone-draft"), &acme, `{"draft_id":"gone-draft","slug":"secret-flow","version":1}`),
		stamped(update(2, "d:gone-draft"), &acme, `{"draft_id":"gone-draft","slug":"secret-flow","version":2}`),
		// The deletion is written after the row is gone: no org to stamp.
		stamped(update(3, "d:gone-draft"), nil, `{"draft_id":"gone-draft","slug":"secret-flow","version":2,"deleted":true}`),
		// Written before orgs, about a draft that is gone: no org to decide on.
		stamped(update(4, "d:gone-legacy"), nil, `{"draft_id":"gone-legacy","slug":"old-flow","version":5}`),
		update(5, "personal"),
	}}
	events := orgUserStream(t, repo, nil, &orgMembers{orgs: map[string]bool{"home": true}})

	nextEvent(t, events, "sync", func(ev *reliantv1.UserStreamEvent) bool { return ev.GetSync() != nil })
	batch := nextEvent(t, events, "an update batch", func(ev *reliantv1.UserStreamEvent) bool { return ev.GetUpdates() != nil }).GetUpdates()
	require.Equal(t, int64(5), batch.GetLatestSequence())
	require.Len(t, batch.GetUpdates(), 2, "the deletion marker and the personal update only: %v", batch.GetUpdates())
	marker := batch.GetUpdates()[0]
	require.Equal(t, int64(3), marker.GetSequenceNumber())
	require.JSONEq(t, `{"draft_id":"gone-draft","deleted":true}`, marker.GetDataJson(), "the marker carries nothing of the draft")
	require.Equal(t, "personal", batch.GetUpdates()[1].GetId())
}
