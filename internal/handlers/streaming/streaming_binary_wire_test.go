package streaming

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
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
	client := serveStreaming(t, svc, func(next http.Handler) http.Handler {
		return authInjector(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			contentTypes <- request.Header.Get("Content-Type")
			next.ServeHTTP(writer, request)
		}))
	})

	ctx, cancel := context.WithTimeout(context.Background(), svc.heartbeatInterval+10*time.Second)
	t.Cleanup(cancel)
	stream, err := client.
		StreamChatUpdates(ctx, &reliantv1.StreamChatUpdatesRequest{
			ChatId: raceTestChatID,
		})
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })

	require.Equal(t, "application/connect+proto", <-contentTypes)

	receivedSnapshot := false
	for {
		event, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			t.Fatal("binary Connect stream ended before heartbeat")
		}
		require.NoError(t, err, "binary Connect stream ended before heartbeat")
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
}
