package services

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

// TestStreamUserUpdates_BinaryConnectWire exercises the production stream
// implementation through the generated Connect handler and client over real
// HTTP/2. The browser's dedicated stream transport uses application/proto, so
// this catches codec changes that fake streams cannot see and proves snapshot
// and heartbeat frames decode.
func TestStreamUserUpdates_BinaryConnectWire(t *testing.T) {
	contentTypes := make(chan string, 1)
	chatID := raceTestChatID
	path, handler := reliantv1connect.NewStreamingServiceHandler(
		NewStreamingService(newCatchupChatRepo(42), noopHub{}, nil, nil),
	)
	mux := http.NewServeMux()
	mux.Handle(path, authInjector(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		contentTypes <- request.Header.Get("Content-Type")
		handler.ServeHTTP(writer, request)
	})))

	server := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	server.EnableHTTP2 = true
	server.Start()
	t.Cleanup(server.Close)

	if testing.Short() {
		t.Skip("waits for the production heartbeat interval; skipped under -short")
	}
	ctx, cancel := context.WithTimeout(context.Background(), heartbeatInterval+10*time.Second)
	t.Cleanup(cancel)
	stream, err := reliantv1connect.NewStreamingServiceClient(server.Client(), server.URL).
		StreamUserUpdates(ctx, connect.NewRequest(&reliantv1.StreamUserUpdatesRequest{
			SubscribeChatId: &chatID,
		}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })

	require.Equal(t, "application/proto", <-contentTypes)

	receivedSnapshot := false
	for stream.Receive() {
		event := stream.Msg()
		if snapshot := event.GetChatSyncSnapshot(); snapshot != nil {
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
