package streaming

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// The test caller's identity travels in these headers: sharedStreams'
// listener installs it as the API listener's edge does (the user, the org
// membership, the credential).
const (
	testUserHeader       = "X-Test-User"
	testCredentialHeader = "X-Test-Credential"
)

// sharedStreams serves svc over h2c with the edge of the API listener for a
// viewertest fixture: each request is the user its header names, through a
// person's session unless its credential header says "access-token" (an
// agent's CLI login), asking
// membership m.
func sharedStreams(t *testing.T, svc *Service, m authz.Membership) reliantv1connect.StreamingServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := reliantv1connect.NewStreamingServiceHandler(svc, connect.WithInterceptors(svcerr.Interceptor()))
	mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cred := auth.CredentialSession
		if r.Header.Get(testCredentialHeader) == "access-token" {
			cred = auth.CredentialAccessToken
		}
		ctx := context.WithValue(r.Context(), auth.UserIDContextKey, r.Header.Get(testUserHeader))
		ctx = auth.WithCredential(authz.WithMembership(ctx, m), cred)
		handler.ServeHTTP(w, r.WithContext(ctx))
	}))
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	srv.EnableHTTP2 = true
	srv.Start()
	t.Cleanup(srv.Close)
	return reliantv1connect.NewStreamingServiceClient(srv.Client(), srv.URL)
}

// as is req sent by userID.
func as[T any](userID string, msg *T) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set(testUserHeader, userID)
	return req
}

// openChatStream opens userID's stream of chatID and waits for its initial
// sync to end, returning the frames up to and including caught_up and the
// channel of the rest.
func openChatStream(t *testing.T, client reliantv1connect.StreamingServiceClient, userID string, msg *reliantv1.StreamChatUpdatesRequest) ([]*reliantv1.ChatStreamEvent, <-chan *reliantv1.ChatStreamEvent, *connect.ServerStreamForClient[reliantv1.ChatStreamEvent]) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	stream, err := client.StreamChatUpdates(ctx, as(userID, msg))
	require.NoError(t, err)
	events := receiveAll(t, stream)
	var sync []*reliantv1.ChatStreamEvent
	for {
		ev, open := <-events
		if !open { // stream.Err() only once receiveAll has stopped: it races Receive
			t.Fatalf("the stream ended before its initial sync: %v", stream.Err())
		}
		sync = append(sync, ev)
		if ev.GetCaughtUp() != nil {
			return sync, events, stream
		}
	}
}

// requireEndsNotFound waits for the stream to end with no further frame (a
// heartbeat aside) and requires it ended as an open would refuse it.
func requireEndsNotFound(t *testing.T, events <-chan *reliantv1.ChatStreamEvent, stream *connect.ServerStreamForClient[reliantv1.ChatStreamEvent], why string) {
	t.Helper()
	for ev := range events {
		if ev.GetHeartbeat() == nil {
			t.Fatalf("a frame after %s: %v", why, ev)
		}
	}
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(stream.Err()), "%s: %v", why, stream.Err())
}

func newSharedChatService(t *testing.T) (*viewertest.Fixture, *Service) {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc := NewTestStreaming(t, WithStreamingDeps(Deps{Repo: repo, Deltas: noopHub{}, Orgs: f.Membership}))
	svc.heartbeatInterval = time.Hour // only the re-authorization ticks
	svc.reauthorizeInterval = 10 * time.Millisecond
	return f, svc
}

// A viewer of a shared chat follows it read-only (control-plane
// docs/design/org-sharing.md §5.1): its snapshot carries every tool input
// and no tool result, the same rule as the viewer's ListMessages. The owner's
// own stream of the same chat carries it.
func TestStreamChatUpdates_AViewersSnapshotCarriesNoToolResult(t *testing.T) {
	f, svc := newSharedChatService(t)
	client := sharedStreams(t, svc, f.Membership)
	full := reliantv1.ToolBodyMode_TOOL_BODY_MODE_FULL

	resultOf := func(userID string) *reliantv1.ContentBlock {
		sync, _, _ := openChatStream(t, client, userID, &reliantv1.StreamChatUpdatesRequest{ChatId: f.ChatID, ToolBodies: &full})
		require.NotNil(t, sync[0].GetSnapshot(), "a fresh open starts with a snapshot")
		for _, m := range sync[0].GetSnapshot().GetMessages() {
			for _, b := range m.GetContentBlocks() {
				if b.GetId() == f.Transcript.ResultBlockID {
					return b
				}
			}
		}
		t.Fatalf("%s's snapshot has no result block", userID)
		return nil
	}

	viewed := resultOf(f.ViewerID)
	assert.Nil(t, viewed.Content, "a viewer's snapshot never carries a tool result")
	assert.True(t, viewed.GetBodyElided())
	assert.Equal(t, int64(len(f.Transcript.Result)), viewed.GetBodySize())

	owned := resultOf(f.OwnerID)
	assert.False(t, owned.GetBodyElided())
	require.NotEmpty(t, owned.GetContent(), "the owner's stream carries the result (a snapshot sends a long one's preview)")
	assert.Contains(t, f.Transcript.Result, owned.GetContent()[:64])

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	stream, err := client.StreamChatUpdates(ctx, as(f.OutsiderID, &reliantv1.StreamChatUpdatesRequest{ChatId: f.ChatID}))
	require.NoError(t, err)
	for range receiveAll(t, stream) {
		t.Fatal("an outsider is sent nothing of a chat shared with another org")
	}
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(stream.Err()))
}

// Its owner making a shared chat private ends a viewer's open stream within
// one re-authorization interval: the viewer's row is re-read on every tick,
// not trusted from the open.
func TestStreamChatUpdates_UnshareEndsAViewersStream(t *testing.T) {
	f, svc := newSharedChatService(t)
	client := sharedStreams(t, svc, f.Membership)
	_, events, stream := openChatStream(t, client, f.ViewerID, &reliantv1.StreamChatUpdatesRequest{ChatId: f.ChatID})

	changed, err := f.Repo.ApplyShareChange(context.Background(), core.ShareChange{
		Kind: core.ShareKindChat, ResourceID: f.ChatID, OrgID: viewertest.OrgID, OwnerUserID: f.OwnerID,
		ActorUserID: f.OwnerID, Action: core.ShareActionUnshare, At: time.Now(),
	})
	require.NoError(t, err)
	require.True(t, changed)

	requireEndsNotFound(t, events, stream, "the unshare")
}

// A viewer removed from the org loses an open stream within one
// re-authorization interval of the resolver's answer changing (in
// production orgs.CacheTTL bounds how stale that answer may be).
func TestStreamChatUpdates_RemovalEndsAViewersStream(t *testing.T) {
	f, svc := newSharedChatService(t)
	membership := &switchableMembership{}
	client := sharedStreams(t, svc, membership)
	_, events, stream := openChatStream(t, client, f.ViewerID, &reliantv1.StreamChatUpdatesRequest{ChatId: f.ChatID})

	asked := membership.asked.Load()
	require.Eventually(t, func() bool { return membership.asked.Load() >= asked+3 }, 10*time.Second, time.Millisecond,
		"the open stream never re-asked the membership")
	membership.removed.Store(true)

	requireEndsNotFound(t, events, stream, "the removal")
}

// Every frame a viewer is sent after the snapshot is shaped by the same rule
// (viewerChatEvent): a message's tool results, a run step's output and a
// node's output are left out, and the owner's user-level updates are not
// theirs.
func TestViewerChatEvent(t *testing.T) {
	message := `{"message":{"id":"m","content_blocks":[` +
		`{"type":2,"tool_call_id":"1","tool_name":"bash","input":"{\"command\":\"cat .env\"}"},` +
		`{"type":3,"tool_call_id":"1","content":"SECRET=hunter2"}]}}`
	runOutput := `{"step_id":"s","command":"make test","exit_code":1,"stdout":"SECRET=hunter2","stderr":"boom","working_directory":"/home/owner"}`
	node := `{"node_id":"n","status":"failed","output":{"token":"SECRET=hunter2"},"error_message":"SECRET=hunter2"}`
	ev := viewerChatEvent(&reliantv1.ChatStreamEvent{Event: &reliantv1.ChatStreamEvent_Updates{Updates: &reliantv1.ChatUpdateBatch{
		Updates: []*reliantv1.ChatUpdateData{
			{UpdateType: reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE, DataJson: message},
			{UpdateType: reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_RUN_OUTPUT, DataJson: runOutput},
			{UpdateType: reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_NODE_EXECUTION, DataJson: node},
		},
	}}})
	for _, u := range ev.GetUpdates().GetUpdates() {
		assert.NotContains(t, u.GetDataJson(), "SECRET", "%v reaches a viewer", u.GetUpdateType())
	}
	var run map[string]any
	require.NoError(t, json.Unmarshal([]byte(ev.GetUpdates().GetUpdates()[1].GetDataJson()), &run))
	assert.Equal(t, "make test", run["command"], "a step's command and how it ended stay")
	assert.Equal(t, true, run["body_elided"])
	assert.NotContains(t, run, "working_directory")

	user := viewerChatEvent(&reliantv1.ChatStreamEvent{Event: &reliantv1.ChatStreamEvent_UserUpdates{UserUpdates: &reliantv1.ChatUserUpdates{
		Updates: []*reliantv1.UserUpdateData{{EntityId: "the-owners"}},
	}}})
	assert.Empty(t, user.GetUserUpdates().GetUpdates(), "the owner's user-level updates are theirs")
}
