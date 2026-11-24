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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// A chat member's open stream follows their grant (control-plane
// docs/design/chat-collaboration.md §11.1): a grant is re-read on every
// re-authorization tick, uncached, so a revoke ends the stream within one
// interval; a demotion ends it too, so the client reopens into the poorer
// shape; a promotion does not.

// switchableGrants holds userID's level on every chat until changed,
// counting the asks.
type switchableGrants struct {
	userID string
	role   atomic.Value // core.ChatMemberRole
	asked  atomic.Int64
}

func newSwitchableGrants(userID string, role core.ChatMemberRole) *switchableGrants {
	g := &switchableGrants{userID: userID}
	g.role.Store(role)
	return g
}

func (g *switchableGrants) ChatMemberRole(_ context.Context, _, userID string) (core.ChatMemberRole, error) {
	g.asked.Add(1)
	if userID != g.userID {
		return "", nil
	}
	r, _ := g.role.Load().(core.ChatMemberRole)
	return r, nil
}

// memberStreams is sharedStreams with the chat grants g on every request,
// as the API listener's process carries them.
func memberStreams(t *testing.T, svc *Service, m authz.Membership, g authz.Grants) reliantv1connect.StreamingServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := reliantv1connect.NewStreamingServiceHandler(svc, connect.WithInterceptors(svcerr.Interceptor()))
	mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), auth.UserIDContextKey, r.Header.Get(testUserHeader))
		ctx = auth.WithCredential(authz.WithGrants(authz.WithMembership(ctx, m), g), auth.CredentialSession)
		handler.ServeHTTP(w, r.WithContext(ctx))
	}))
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	srv.EnableHTTP2 = true
	srv.Start()
	t.Cleanup(srv.Close)
	return reliantv1connect.NewStreamingServiceClient(srv.Client(), srv.URL)
}

// makePrivate unshares the fixture's chat, so only a grant reads it.
func makePrivate(t *testing.T, f *viewertest.Fixture) {
	t.Helper()
	changed, err := applyShareChange(context.Background(), f.Repo, core.ShareChange{
		Kind: core.ShareKindChat, ResourceID: f.ChatID, OrgID: viewertest.OrgID, OwnerUserID: f.OwnerID,
		ActorUserID: f.OwnerID, Action: core.ShareActionUnshare, At: time.Now(),
	})
	require.NoError(t, err)
	require.True(t, changed)
}

// requireStaysOpen waits for grants to be asked three more times (three
// re-authorization ticks) and requires the stream sent nothing but
// heartbeats and did not end.
func requireStaysOpen(t *testing.T, grants *switchableGrants, events <-chan *reliantv1.ChatStreamEvent, why string) {
	t.Helper()
	asked := grants.asked.Load()
	require.Eventually(t, func() bool { return grants.asked.Load() >= asked+3 }, 10*time.Second, time.Millisecond,
		"the open stream never re-read the grant")
	select {
	case ev, open := <-events:
		if !open {
			t.Fatalf("the stream ended after %s", why)
		}
		assert.NotNil(t, ev.GetHeartbeat(), "only heartbeats after %s", why)
	default:
	}
}

func TestStreamChatUpdates_RevokeEndsAMembersStream(t *testing.T) {
	f, svc := newSharedChatService(t)
	makePrivate(t, f)
	grants := newSwitchableGrants(f.ViewerID, core.ChatMemberReply)
	client := memberStreams(t, svc, f.Membership, grants)
	_, events, stream := openChatStream(t, client, f.ViewerID, &reliantv1.StreamChatUpdatesRequest{ChatId: f.ChatID})

	grants.role.Store(core.ChatMemberRole(""))
	requireEndsNotFound(t, events, stream, "the revoke")
}

func TestStreamChatUpdates_DemotionEndsAMembersStream(t *testing.T) {
	for _, tc := range []struct {
		name     string
		private  bool
		from, to core.ChatMemberRole
	}{
		{"manage to view, private", true, core.ChatMemberManage, core.ChatMemberView},
		{"reply to view, private", true, core.ChatMemberReply, core.ChatMemberView},
		{"manage to reply, private", true, core.ChatMemberManage, core.ChatMemberReply},
		// The org can still view the chat: revoking the grant leaves an
		// org-wide viewer, which is a demotion from reply.
		{"reply to an org-wide viewer, shared", false, core.ChatMemberReply, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, svc := newSharedChatService(t)
			if tc.private {
				makePrivate(t, f)
			}
			grants := newSwitchableGrants(f.ViewerID, tc.from)
			client := memberStreams(t, svc, f.Membership, grants)
			_, events, stream := openChatStream(t, client, f.ViewerID, &reliantv1.StreamChatUpdatesRequest{ChatId: f.ChatID})

			grants.role.Store(tc.to)
			requireEndsNotFound(t, events, stream, "the demotion")
		})
	}
}

func TestStreamChatUpdates_PromotionKeepsAMembersStream(t *testing.T) {
	f, svc := newSharedChatService(t)
	makePrivate(t, f)
	grants := newSwitchableGrants(f.ViewerID, core.ChatMemberView)
	client := memberStreams(t, svc, f.Membership, grants)
	_, events, _ := openChatStream(t, client, f.ViewerID, &reliantv1.StreamChatUpdatesRequest{ChatId: f.ChatID})

	grants.role.Store(core.ChatMemberManage)
	requireStaysOpen(t, grants, events, "the promotion")
}

// A reply or manage member's frames are a viewer's, except a question, whose
// card they answer from.
func TestMemberChatEvent(t *testing.T) {
	question := `{"id":"q","status":"pending","metadata":"{\"type\":\"ask_user\"}","response_data":"x"}`
	message := `{"message":{"id":"m","content_blocks":[{"type":3,"tool_call_id":"1","content":"SECRET=hunter2"}]}}`
	frame := func() *reliantv1.ChatStreamEvent {
		return &reliantv1.ChatStreamEvent{Event: &reliantv1.ChatStreamEvent_Updates{Updates: &reliantv1.ChatUpdateBatch{
			Updates: []*reliantv1.ChatUpdateData{
				{UpdateType: reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION, DataJson: question},
				{UpdateType: reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE, DataJson: message},
			},
		}}}
	}

	var got []*reliantv1.ChatStreamEvent
	sender := viewerChatSender{out: recordingSender(func(ev *reliantv1.ChatStreamEvent) { got = append(got, ev) }), member: true}
	require.NoError(t, sender.Send(frame()))
	updates := got[0].GetUpdates().GetUpdates()
	assert.JSONEq(t, question, updates[0].GetDataJson(), "a member gets the whole question")
	assert.NotContains(t, updates[1].GetDataJson(), "SECRET", "and no tool result")

	viewed := viewerChatEvent(frame()).GetUpdates().GetUpdates()
	assert.NotContains(t, viewed[0].GetDataJson(), "metadata", "a viewer gets a workflow question without its metadata")
}

// recordingSender is a chatSender that hands each frame to fn.
type recordingSender func(*reliantv1.ChatStreamEvent)

func (r recordingSender) Send(ev *reliantv1.ChatStreamEvent) error {
	r(ev)
	return nil
}

func TestAccessAtLeast(t *testing.T) {
	order := []authz.Access{authz.AccessNone, authz.AccessViewer, authz.AccessReply, authz.AccessManage, authz.AccessOwner}
	for i, a := range order {
		for j, b := range order {
			assert.Equal(t, i >= j, a.AtLeast(b), "%v.AtLeast(%v)", a, b)
		}
	}
}
