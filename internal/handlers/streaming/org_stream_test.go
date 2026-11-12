package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// openOrgFeed opens userID's org feed with msg and returns every update sent
// before caught_up, caught_up's sequence, and the channel of the rest.
func openOrgFeed(t *testing.T, client reliantv1connect.StreamingServiceClient, userID string, msg *reliantv1.StreamOrgUpdatesRequest) ([]*reliantv1.OrgUpdate, int64, <-chan *reliantv1.OrgStreamEvent) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	stream, err := client.StreamOrgUpdates(ctx, as(userID, msg))
	require.NoError(t, err)
	events := receiveAll(t, stream)
	var replayed []*reliantv1.OrgUpdate
	for {
		ev, open := <-events
		if !open {
			t.Fatalf("the org feed ended before caught_up: %v", stream.Err())
		}
		replayed = append(replayed, ev.GetUpdates().GetUpdates()...)
		if c := ev.GetCaughtUp(); c != nil {
			return replayed, c.GetLatestSequence(), events
		}
	}
}

// nextOrgUpdate is the next update the feed sends.
func nextOrgUpdate(t *testing.T, events <-chan *reliantv1.OrgStreamEvent, what string) *reliantv1.OrgUpdate {
	t.Helper()
	ev := nextEvent(t, events, what, func(ev *reliantv1.OrgStreamEvent) bool { return len(ev.GetUpdates().GetUpdates()) > 0 })
	require.Len(t, ev.GetUpdates().GetUpdates(), 1, "%s: one change, one row", what)
	return ev.GetUpdates().GetUpdates()[0]
}

func newOrgFeedService(t *testing.T) (*viewertest.Fixture, reliantv1connect.StreamingServiceClient) {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc := NewTestStreaming(t, WithStreamingDeps(Deps{Repo: repo, Deltas: noopHub{}, Orgs: f.Membership}))
	svc.heartbeatInterval = time.Hour
	svc.orgFeedPollInterval = 10 * time.Millisecond
	return f, sharedStreams(t, svc, f.Membership)
}

// The org feed (control-plane docs/design/org-sharing.md §5.2) follows what
// an org's members shared, coarsely: written by the database in the
// transaction of each change, one row per change, resumable from a cursor.
// A rename and an activity announcement are CHANGED rows carrying the title
// and the activity; an unshare and a deletion are bare markers; a message,
// or any other user update, writes nothing.
func TestStreamOrgUpdates_FollowsAndReplaysAnOrgsShares(t *testing.T) {
	f, client := newOrgFeedService(t)
	ctx := context.Background()

	// The fixture shared a project and a chat: two SHARED rows.
	replayed, start, events := openOrgFeed(t, client, f.ViewerID, &reliantv1.StreamOrgUpdatesRequest{})
	assert.Empty(t, replayed, "a fresh start replays nothing: the client lists what is shared now")
	require.EqualValues(t, 2, start)

	_, err := f.Repo.ORM().Bun().NewRaw(`UPDATE chats SET title = 'Renamed' WHERE id = ?`, f.ChatID).Exec(ctx)
	require.NoError(t, err)
	renamed := nextOrgUpdate(t, events, "the rename")
	assert.Equal(t, reliantv1.OrgUpdateType_ORG_UPDATE_TYPE_CHANGED, renamed.GetType())
	assert.Equal(t, f.ChatID, renamed.GetResourceId())
	assert.Equal(t, "chat", renamed.GetResourceKind())
	assert.Equal(t, f.OwnerID, renamed.GetOwnerUserId())
	assert.Equal(t, "Renamed", renamed.GetTitle())

	chatID, projectID := f.ChatID, f.ProjectID
	announce := func(kind reliantv1.UserUpdateType, data string) {
		require.NoError(t, f.Repo.CreateUserUpdate(ctx, &db.UserUpdate{
			UserID: f.OwnerID, ChatID: &chatID, ProjectID: &projectID, UpdateType: kind,
			EntityType: reliantv1.EntityType_ENTITY_TYPE_CHAT, EntityID: f.ChatID, Data: json.RawMessage(data),
		}))
	}
	// Neither a config change nor a message writes the feed.
	announce(reliantv1.UserUpdateType_USER_UPDATE_TYPE_CHAT_CONFIG_CHANGED, `{"model":"x"}`)
	announce(reliantv1.UserUpdateType_USER_UPDATE_TYPE_CHAT_ACTIVITY_CHANGED,
		`{"chat_id":"`+f.ChatID+`","activity":2,"working_threads":1}`)
	working := nextOrgUpdate(t, events, "the activity announcement")
	assert.Equal(t, reliantv1.OrgUpdateType_ORG_UPDATE_TYPE_CHANGED, working.GetType())
	require.NotNil(t, working.Activity)
	assert.EqualValues(t, 2, working.GetActivity())
	assert.Equal(t, renamed.GetSequenceNumber()+1, working.GetSequenceNumber(), "the config change wrote no row")

	changed, err := f.Repo.ApplyShareChange(ctx, core.ShareChange{
		Kind: core.ShareKindChat, ResourceID: f.ChatID, OrgID: viewertest.OrgID, OwnerUserID: f.OwnerID,
		ActorUserID: f.OwnerID, Action: core.ShareActionUnshare, At: time.Now(),
	})
	require.NoError(t, err)
	require.True(t, changed)
	unshared := nextOrgUpdate(t, events, "the unshare")
	assert.Equal(t, reliantv1.OrgUpdateType_ORG_UPDATE_TYPE_UNSHARED, unshared.GetType())
	assert.Equal(t, f.ChatID, unshared.GetResourceId())
	assert.Nil(t, unshared.Title, "an unshare is a bare marker")
	assert.Nil(t, unshared.Activity)

	// A private chat's changes are nobody else's.
	_, err = f.Repo.ORM().Bun().NewRaw(`UPDATE chats SET title = 'Private again' WHERE id = ?`, f.ChatID).Exec(ctx)
	require.NoError(t, err)
	announce(reliantv1.UserUpdateType_USER_UPDATE_TYPE_CHAT_ACTIVITY_CHANGED, `{"chat_id":"`+f.ChatID+`","activity":1}`)

	require.NoError(t, f.Repo.DeleteProject(ctx, f.ProjectID, f.OwnerID))
	deleted := nextOrgUpdate(t, events, "the deletion")
	assert.Equal(t, reliantv1.OrgUpdateType_ORG_UPDATE_TYPE_DELETED, deleted.GetType())
	assert.Equal(t, "project", deleted.GetResourceKind())
	assert.Nil(t, deleted.Title, "a deletion is a bare marker")
	assert.Equal(t, unshared.GetSequenceNumber()+1, deleted.GetSequenceNumber(), "the private chat's changes wrote no row")

	// A reconnect from a cursor replays exactly what came after it, in order.
	replayed, latest, _ := openOrgFeed(t, client, f.ViewerID, &reliantv1.StreamOrgUpdatesRequest{SinceSeq: start})
	var types []reliantv1.OrgUpdateType
	for _, u := range replayed {
		types = append(types, u.GetType())
	}
	assert.Equal(t, []reliantv1.OrgUpdateType{
		reliantv1.OrgUpdateType_ORG_UPDATE_TYPE_CHANGED, reliantv1.OrgUpdateType_ORG_UPDATE_TYPE_CHANGED,
		reliantv1.OrgUpdateType_ORG_UPDATE_TYPE_UNSHARED, reliantv1.OrgUpdateType_ORG_UPDATE_TYPE_DELETED,
	}, types)
	assert.Equal(t, deleted.GetSequenceNumber(), latest)
}

// The owner's feed leaves their own rows out (their own list has them), but
// its cursor still passes them, so a resume never re-reads them.
func TestStreamOrgUpdates_SkipsTheCallersOwnRows(t *testing.T) {
	f, client := newOrgFeedService(t)
	replayed, latest, _ := openOrgFeed(t, client, f.OwnerID, &reliantv1.StreamOrgUpdatesRequest{SinceSeq: 1})
	assert.Empty(t, replayed, "both SHARED rows are the owner's")
	assert.EqualValues(t, 2, latest)

	replayed, _, _ = openOrgFeed(t, client, f.ViewerID, &reliantv1.StreamOrgUpdatesRequest{SinceSeq: 1})
	require.Len(t, replayed, 1, "a member is sent the row after their cursor")
	assert.Equal(t, f.ChatID, replayed[0].GetResourceId())
	assert.Equal(t, reliantv1.OrgUpdateType_ORG_UPDATE_TYPE_SHARED, replayed[0].GetType())
}

// Only a person who is a live member follows an org's feed: an outsider
// naming it is refused, and so is an agent's CLI login.
func TestStreamOrgUpdates_RefusesNonMembersAndAgents(t *testing.T) {
	f, client := newOrgFeedService(t)
	org := viewertest.OrgID

	refused := func(req *connect.Request[reliantv1.StreamOrgUpdatesRequest]) connect.Code {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		t.Cleanup(cancel)
		stream, err := client.StreamOrgUpdates(ctx, req)
		require.NoError(t, err)
		for ev := range receiveAll(t, stream) {
			t.Fatalf("a refused caller was sent %v", ev)
		}
		return connect.CodeOf(stream.Err())
	}
	assert.Contains(t, []connect.Code{connect.CodeNotFound, connect.CodePermissionDenied},
		refused(as(f.OutsiderID, &reliantv1.StreamOrgUpdatesRequest{OrgId: &org})), "an outsider naming the org")

	agent := as(f.ViewerID, &reliantv1.StreamOrgUpdatesRequest{OrgId: &org})
	agent.Header().Set(testCredentialHeader, "access-token")
	assert.Equal(t, connect.CodePermissionDenied, refused(agent), "an agent's CLI login")
}

// flippableFeedMembership answers as the fixture's resolver until flipped,
// then with its after answer, counting the questions asked after the flip.
type flippableFeedMembership struct {
	*orgs.Resolver
	flipped      atomic.Bool
	afterMember  bool
	afterOK      bool
	afterErr     error
	askedFlipped atomic.Int32
}

func (m *flippableFeedMembership) IsMember(ctx context.Context, userID, orgID string) (bool, bool, error) {
	if !m.flipped.Load() {
		return m.Resolver.IsMember(ctx, userID, orgID)
	}
	m.askedFlipped.Add(1)
	return m.afterMember, m.afterOK, m.afterErr
}

// An open org feed ends the moment its caller is anything but a definite live
// member: removed, unknown (the resolver has no answer) or unreachable. The
// org-wide feed has no owner-only fallback. No row written after that is sent.
func TestStreamOrgUpdates_EndsWhenMembershipIsNotConfirmed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		member bool
		ok     bool
		err    error
		code   connect.Code
	}{
		{"removed", false, true, nil, connect.CodePermissionDenied},
		{"unknown", false, false, nil, connect.CodePermissionDenied},
		{"unknown though member", true, false, nil, connect.CodePermissionDenied},
		{"outage", false, false, errors.New("control plane down"), connect.CodeUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, cleanup := db.SetupTestDB(t)
			t.Cleanup(cleanup)
			f := viewertest.New(t, repo)
			m := &flippableFeedMembership{Resolver: f.Membership, afterMember: tc.member, afterOK: tc.ok, afterErr: tc.err}
			svc := NewTestStreaming(t, WithStreamingDeps(Deps{Repo: repo, Deltas: noopHub{}, Orgs: m}))
			svc.heartbeatInterval = time.Hour
			svc.orgFeedPollInterval = 10 * time.Millisecond
			svc.reauthorizeInterval = 10 * time.Millisecond
			client := sharedStreams(t, svc, f.Membership)

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			t.Cleanup(cancel)
			stream, err := client.StreamOrgUpdates(ctx, as(f.ViewerID, &reliantv1.StreamOrgUpdatesRequest{}))
			require.NoError(t, err)
			events := receiveAll(t, stream)
			nextEvent(t, events, "caught_up", func(ev *reliantv1.OrgStreamEvent) bool { return ev.GetCaughtUp() != nil })

			m.flipped.Store(true)
			require.Eventually(t, func() bool { return m.askedFlipped.Load() >= 1 }, 10*time.Second, time.Millisecond,
				"the open feed never re-asked the membership")
			// A change after the re-check that ended the feed.
			_, err = repo.ORM().Bun().NewRaw(`UPDATE chats SET title = 'After the end' WHERE id = ?`, f.ChatID).Exec(context.Background())
			require.NoError(t, err)

			for ev := range events {
				if len(ev.GetUpdates().GetUpdates()) > 0 {
					t.Fatalf("a row was sent after the membership was not confirmed: %v", ev)
				}
			}
			require.Equal(t, tc.code, connect.CodeOf(stream.Err()), "%v", stream.Err())
		})
	}
}

// The open itself refuses a caller whose membership is not confirmed.
func TestStreamOrgUpdates_OpenRefusesUnconfirmedMembership(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	m := &flippableFeedMembership{Resolver: f.Membership, afterMember: true, afterOK: false}
	m.flipped.Store(true)
	svc := NewTestStreaming(t, WithStreamingDeps(Deps{Repo: repo, Deltas: noopHub{}, Orgs: m}))
	client := sharedStreams(t, svc, f.Membership)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	stream, err := client.StreamOrgUpdates(ctx, as(f.ViewerID, &reliantv1.StreamOrgUpdatesRequest{}))
	require.NoError(t, err)
	for ev := range receiveAll(t, stream) {
		t.Fatalf("an unconfirmed member was sent %v", ev)
	}
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(stream.Err()))
}
