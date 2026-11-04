package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

// TestStreamChatUpdates_BinaryConnectWire exercises the production chat stream
// implementation through the generated Connect handler and client over real
// HTTP/2. The browser's dedicated stream transport uses binary framing (application/connect+proto), so
// this catches codec changes that fake streams cannot see and proves snapshot
// and heartbeat frames decode.
func TestStreamChatUpdates_BinaryConnectWire(t *testing.T) {
	contentTypes := make(chan string, 1)
	svc := NewTestStreaming(t, WithStreamingDeps(Deps{Repo: newCatchupChatRepo(42), Deltas: noopHub{}}))
	// The frame's encoding is what is under test, not the 30s production
	// cadence, so the heartbeat comes fast. It still comes from the real
	// ticker in the real stream loop, after the initial snapshot.
	svc.heartbeatInterval = 100 * time.Millisecond
	path, handler := reliantv1connect.NewStreamingServiceHandler(svc)
	mux := http.NewServeMux()
	mux.Handle(path, authInjector(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		contentTypes <- request.Header.Get("Content-Type")
		handler.ServeHTTP(writer, request)
	})))

	server := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	server.EnableHTTP2 = true
	server.Start()
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), svc.heartbeatInterval+10*time.Second)
	t.Cleanup(cancel)
	stream, err := reliantv1connect.NewStreamingServiceClient(server.Client(), server.URL).
		StreamChatUpdates(ctx, connect.NewRequest(&reliantv1.StreamChatUpdatesRequest{
			ChatId: raceTestChatID,
		}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })

	require.Equal(t, "application/connect+proto", <-contentTypes)

	receivedSnapshot := false
	for stream.Receive() {
		event := stream.Msg()
		if snapshot := event.GetSnapshot(); snapshot != nil {
			require.Equal(t, int64(42), snapshot.GetLatestSequence())
			receivedSnapshot = true
			continue
		}
		if heartbeat := event.GetHeartbeat(); heartbeat != nil {
			require.True(t, receivedSnapshot, "heartbeat must follow the initial snapshot")
			require.NotZero(t, heartbeat.GetTimestamp())
			return
		}
	}
	t.Fatalf("binary Connect stream ended before heartbeat: %v", stream.Err())
}
