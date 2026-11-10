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
