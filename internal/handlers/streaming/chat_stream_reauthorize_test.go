package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
)

const reauthorizeTestOrg = "org-acme"

// toggledMembership is a Membership whose answer the test flips, counting
// every question it is asked.
type toggledMembership struct {
	member atomic.Bool
	asked  atomic.Int64
}

func (m *toggledMembership) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	m.asked.Add(1)
	return userID == raceTestUserID && orgID == reauthorizeTestOrg && m.member.Load(), true, nil
}

// orgChatRepo serves catchupChatRepo's sync paths for a chat in an org.
type orgChatRepo struct {
	catchupChatRepo
}

func (orgChatRepo) GetChatRow(context.Context, string) (*db.ChatRow, error) {
	org := reauthorizeTestOrg
	return &db.ChatRow{ID: raceTestChatID, UserID: raceTestUserID, OrgID: &org}, nil
}

// An open chat stream asks authz again while it is open: a member removed
// from the chat's org stops receiving it, with the open's NotFound, within
// one reauthorizeInterval (plus orgs.CacheTTL in production, which this
// Membership does not cache).
func TestStreamChatUpdates_EndsWhenTheCallerLosesTheChat(t *testing.T) {
	membership := &toggledMembership{}
	membership.member.Store(true)

	svc := NewTestStreaming(t, WithStreamingDeps(Deps{Repo: orgChatRepo{newCatchupChatRepo(10)}, Deltas: noopHub{}}))
	svc.reauthorizeInterval = 10 * time.Millisecond

	mux := http.NewServeMux()
	path, handler := reliantv1connect.NewStreamingServiceHandler(svc)
	mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), auth.UserIDContextKey, raceTestUserID)
		handler.ServeHTTP(w, r.WithContext(authz.WithMembership(ctx, membership)))
	}))
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	srv.EnableHTTP2 = true
	srv.Start()
	t.Cleanup(srv.Close)
	client := reliantv1connect.NewStreamingServiceClient(srv.Client(), srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	stream, err := client.StreamChatUpdates(ctx, connect.NewRequest(&reliantv1.StreamChatUpdatesRequest{
		ChatId: raceTestChatID, SinceSeq: 10,
	}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })

	ended := make(chan error, 1)
	caughtUp := make(chan struct{})
	go func() {
		sawCaughtUp := false
		for stream.Receive() {
			if !sawCaughtUp && stream.Msg().GetCaughtUp() != nil {
				sawCaughtUp = true
				close(caughtUp)
			}
		}
		ended <- stream.Err()
	}()

	select {
	case <-caughtUp:
	case err := <-ended:
		t.Fatalf("the stream ended before caught_up while the caller was a member: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for caught_up")
	}

	// While still a member, the stream rechecks and stays open.
	asked := membership.asked.Load()
	require.Eventually(t, func() bool { return membership.asked.Load() >= asked+3 }, 10*time.Second, time.Millisecond,
		"the open stream never asked again whether its caller may read the chat")
	select {
	case err := <-ended:
		t.Fatalf("the stream ended while the caller was still a member: %v", err)
	default:
	}

	membership.member.Store(false)
	select {
	case err := <-ended:
		// This bare server has no error interceptor to put the code on the
		// wire (TestStreams_RepositoryFailureCodes pins that); the message is
		// the open's NotFound.
		require.Error(t, err, "a revoked chat ends the stream with an error")
		require.Contains(t, err.Error(), "chat not found", "a revoked chat ends with the open's NotFound")
	case <-time.After(10 * time.Second):
		t.Fatal("the stream kept running after the caller lost the chat's org")
	}
}
