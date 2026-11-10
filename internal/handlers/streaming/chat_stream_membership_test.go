package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/streaming"
)

// orgChatRepo serves the stream's chat as one of org acme.
type orgChatRepo struct{ catchupChatRepo }

func (orgChatRepo) GetChatRow(context.Context, string) (*db.ChatRow, error) {
	acme := "acme"
	return &db.ChatRow{ID: raceTestChatID, UserID: raceTestUserID, OrgID: &acme}, nil
}

// switchableMembership is a live membership of acme until removed, counting
// how often it was asked.
type switchableMembership struct {
	removed atomic.Bool
	asked   atomic.Int64
}

func (m *switchableMembership) IsMember(context.Context, string, string) (bool, bool, error) {
	m.asked.Add(1)
	return !m.removed.Load(), true, nil
}

// A chat stream opened by a live member of the chat's org ends once they are
// removed from it: the open's decision is re-asked while the stream lives,
// not only when it opens.
func TestStreamChatUpdates_RemovedMemberStreamEnds(t *testing.T) {
	svc := NewTestStreaming(t, WithStreamingDeps(Deps{Repo: orgChatRepo{newCatchupChatRepo(42)}, Deltas: noopHub{}}))
	svc.heartbeatInterval = time.Hour // only the re-authorization ticks
	svc.reauthorizeInterval = 10 * time.Millisecond
	membership := &switchableMembership{}

	mux := http.NewServeMux()
	path, handler := reliantv1connect.NewStreamingServiceHandler(svc, connect.WithInterceptors(svcerr.Interceptor()))
	// A request through a listener carries the membership authz decides with.
	mux.Handle(path, authInjector(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(authz.WithMembership(r.Context(), membership)))
	})))
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	srv.EnableHTTP2 = true
	srv.Start()
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	stream, err := reliantv1connect.NewStreamingServiceClient(srv.Client(), srv.URL).
		StreamChatUpdates(ctx, connect.NewRequest(&reliantv1.StreamChatUpdatesRequest{ChatId: raceTestChatID}))
	require.NoError(t, err)
	events := receiveAll(t, stream)

	for caughtUp := false; !caughtUp; {
		event, open := <-events
		require.True(t, open, "the stream ended before its initial sync: %v", stream.Err())
		caughtUp = event.GetCaughtUp() != nil
	}
	// While a member, the stream is re-authorized and stays open.
	require.Eventually(t, func() bool { return membership.asked.Load() >= 3 }, 10*time.Second, time.Millisecond,
		"the open stream never re-asked the membership")
	select {
	case event, open := <-events:
		if !open {
			t.Fatalf("the stream ended while the caller was still a member: %v", stream.Err())
		}
		t.Fatalf("unexpected frame while idle: %v", event)
	default:
	}

	membership.removed.Store(true)
	for event := range events {
		t.Fatalf("a frame after the removal: %v", event)
	}
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(stream.Err()),
		"a removed member's stream ends as the open refuses it: %v", stream.Err())
}

// stampableChatRepo serves the stream's chat with no org until it is stamped
// with one, as the lazy backfill stamps a legacy chat.
type stampableChatRepo struct {
	catchupChatRepo
	org   atomic.Pointer[string]
	reads atomic.Int64
}

func (r *stampableChatRepo) GetChatRow(context.Context, string) (*db.ChatRow, error) {
	r.reads.Add(1)
	return &db.ChatRow{ID: raceTestChatID, UserID: raceTestUserID, OrgID: r.org.Load()}, nil
}

// A chat stream opened on a chat of no org yet ends once the chat is stamped
// with an org its caller may not read: a credential bound to the owner's
// default org, and a member who has left the stamped org. The open's
// identity is re-read while it has no org, not trusted for the stream's life.
func TestStreamChatUpdates_AnUnassignedChatStampedWhileOpenIsReauthorized(t *testing.T) {
	for _, tc := range []struct {
		name    string
		members map[string]bool
		bound   string
	}{
		{"a credential bound to the default org", map[string]bool{"acme": true, "home": true}, "home"},
		{"a member who has left the org", map[string]bool{"home": true}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &stampableChatRepo{catchupChatRepo: newCatchupChatRepo(42)}
			svc := NewTestStreaming(t, WithStreamingDeps(Deps{Repo: repo, Deltas: noopHub{}}))
			svc.heartbeatInterval = time.Hour // only the re-authorization ticks
			svc.reauthorizeInterval = 10 * time.Millisecond
			membership := &orgMembers{orgs: tc.members, def: "home"}

			mux := http.NewServeMux()
			path, handler := reliantv1connect.NewStreamingServiceHandler(svc, connect.WithInterceptors(svcerr.Interceptor()))
			mux.Handle(path, authInjector(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handler.ServeHTTP(w, r.WithContext(authz.WithCredentialOrg(authz.WithMembership(r.Context(), membership), tc.bound)))
			})))
			srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
			srv.EnableHTTP2 = true
			srv.Start()
			t.Cleanup(srv.Close)

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			t.Cleanup(cancel)
			stream, err := reliantv1connect.NewStreamingServiceClient(srv.Client(), srv.URL).
				StreamChatUpdates(ctx, connect.NewRequest(&reliantv1.StreamChatUpdatesRequest{ChatId: raceTestChatID}))
			require.NoError(t, err)
			events := receiveAll(t, stream)
			for caughtUp := false; !caughtUp; {
				event, open := <-events
				require.True(t, open, "the stream ended before its initial sync: %v", stream.Err())
				caughtUp = event.GetCaughtUp() != nil
			}
			// Unassigned, the chat is the default org's: it is re-read on
			// each re-authorization, which passes, and the stream stays open.
			require.Eventually(t, func() bool { return repo.reads.Load() >= 4 }, 10*time.Second, time.Millisecond,
				"the open stream never re-read the unassigned chat")
			select {
			case event, open := <-events:
				if !open {
					t.Fatalf("the stream ended while the chat was unassigned: %v", stream.Err())
				}
				t.Fatalf("unexpected frame while idle: %v", event)
			default:
			}

			acme := "acme"
			repo.org.Store(&acme)
			for event := range events {
				t.Fatalf("a frame after the stamp: %v", event)
			}
			require.Equal(t, connect.CodeNotFound, connect.CodeOf(stream.Err()),
				"stamped acme, the stream ends as an open would refuse it: %v", stream.Err())
		})
	}
}

// A chat stream forwards a started chat's user updates only as the user
// stream would deliver them (orgGate): none about an org the caller has left.
func TestStreamChatUpdates_ForwardsNoUpdateOfALeftOrg(t *testing.T) {
	const child = "chat-started-by-the-open-one"
	userHub := newChanUserHub()
	repo := focusRepo{catchupChatRepo: newCatchupChatRepo(10), parents: map[string]string{child: raceTestChatID}}
	svc := NewTestStreaming(t, WithStreamingDeps(Deps{Repo: repo, Deltas: noopHub{}, UserUpdates: userHub}))
	membership := &orgMembers{orgs: map[string]bool{"home": true}}

	mux := http.NewServeMux()
	path, handler := reliantv1connect.NewStreamingServiceHandler(svc, connect.WithInterceptors(svcerr.Interceptor()))
	mux.Handle(path, authInjector(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(authz.WithMembership(r.Context(), membership)))
	})))
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	srv.EnableHTTP2 = true
	srv.Start()
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	project := catchupProject
	stream, err := reliantv1connect.NewStreamingServiceClient(srv.Client(), srv.URL).
		StreamChatUpdates(ctx, connect.NewRequest(&reliantv1.StreamChatUpdatesRequest{ChatId: raceTestChatID, SinceSeq: 10, ProjectId: &project}))
	require.NoError(t, err)
	events := receiveAll(t, stream)
	nextEvent(t, events, "caught_up", func(ev *reliantv1.ChatStreamEvent) bool { return ev.GetCaughtUp() != nil })
	<-userHub.subscribed

	seq := int64(0)
	publish := func(entityID, org string) {
		seq++
		chatID, projectID := child, catchupOtherProject
		u := db.UserUpdate{
			ID: entityID, UserID: raceTestUserID, SequenceNumber: seq, ChatID: &chatID, ProjectID: &projectID, OrgID: &org,
			UpdateType: db.UserUpdateChatActivityChanged, EntityType: db.EntityTypeChat, EntityID: entityID, Data: []byte(`{}`),
		}
		userHub.events <- streaming.UpdateEvent[db.UserUpdate]{Key: raceTestUserID, SequenceNumber: seq, Payload: u}
	}
	publish("child-in-a-left-org", "acme")
	publish("last", "home")

	var forwarded []string
	for len(forwarded) == 0 || forwarded[len(forwarded)-1] != "last" {
		batch := nextEvent(t, events, "forwarded user updates", func(ev *reliantv1.ChatStreamEvent) bool {
			return ev.GetUserUpdates() != nil
		}).GetUserUpdates()
		for _, u := range batch.GetUpdates() {
			forwarded = append(forwarded, u.GetEntityId())
		}
	}
	require.Equal(t, []string{"last"}, forwarded, "nothing of acme, which the caller has left")
}
