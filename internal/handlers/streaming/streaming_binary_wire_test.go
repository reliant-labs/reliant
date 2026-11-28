package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db"
)

// TestStreamChatUpdates_BinaryConnectWire exercises the production chat stream
// implementation through the generated Connect handler and client over real
// HTTP/2. The browser's dedicated stream transport uses binary framing (application/connect+proto), so
// this catches codec changes that fake streams cannot see and proves snapshot
// and heartbeat frames decode.
func TestStreamChatUpdates_BinaryConnectWire(t *testing.T) {
	contentTypes := make(chan string, 1)
	svc := NewTestStreaming(t, WithStreamingDeps(withRoles(Deps{TriggerReads: &MockTriggerReads{}, Repo: newCatchupChatRepo(42), Deltas: noopHub{}})))
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

type wireSizedRepo struct {
	catchupChatRepo
	payload string
}

func (r wireSizedRepo) SnapshotUpdateHeads(context.Context, string) ([]db.ChatUpdate, string, error) {
	return []db.ChatUpdate{{UpdateType: reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WORKFLOW_STATUS, Data: []byte(r.payload)}}, "fixture", nil
}

// A quiet stream must deliver both initial frames without relying on a later
// heartbeat or EOF to flush them. This tests direct HTTP/2, not a browser or
// an edge proxy. The deadline is a failure bound, not a latency assertion.
func TestStreamChatUpdates_BinaryConnectQuietInitialSync(t *testing.T) {
	// Synthetic update data approximates the observed uncompressed sizes; it
	// does not recreate their contents or browser-side decoding cost.
	repo := wireSizedRepo{catchupChatRepo: newCatchupChatRepo(42), payload: `{"data":"` + strings.Repeat("x", 650000) + `"}`}
	svc := NewTestStreaming(t, WithStreamingDeps(withRoles(Deps{Repo: repo, Deltas: noopHub{}})))
	svc.heartbeatInterval = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	stream, err := streamingTestClient(t, svc).StreamChatUpdates(ctx, connect.NewRequest(&reliantv1.StreamChatUpdatesRequest{
		ChatId: raceTestChatID,
	}))
	require.NoError(t, err)
	defer stream.Close()

	require.True(t, stream.Receive(), "snapshot not readable on open quiet stream: %v", stream.Err())
	snapshot := stream.Msg()
	require.NotNil(t, snapshot.GetSnapshot())
	require.Equal(t, int64(42), snapshot.GetSnapshot().GetLatestSequence())
	t.Logf("snapshot decoded after %s, protobuf bytes=%d", time.Since(started), proto.Size(snapshot))

	require.True(t, stream.Receive(), "caught_up not readable on open quiet stream: %v", stream.Err())
	caughtUp := stream.Msg().GetCaughtUp()
	require.NotNil(t, caughtUp)
	require.Equal(t, int64(42), caughtUp.GetLatestSequence())
	t.Logf("caught_up decoded after %s; heartbeat interval=%s", time.Since(started), svc.heartbeatInterval)
}
