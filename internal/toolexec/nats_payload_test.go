// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Pure guard-helper tests
// ---------------------------------------------------------------------------

func TestExceedsNATSPayloadLimit(t *testing.T) {
	const mb = 1 << 20
	tests := []struct {
		name       string
		size       int
		maxPayload int64
		want       bool
	}{
		{"zero max payload disables preflight", 10 * mb, 0, false},
		{"negative max payload disables preflight", 10 * mb, -1, false},
		{"well under limit", 1024, mb, false},
		{"exactly at effective limit fits", mb - natsPayloadHeadroom, mb, false},
		{"one over effective limit exceeds", mb - natsPayloadHeadroom + 1, mb, true},
		{"production case: 5.4MB reply vs 1MB limit", 5464492, mb, true},
		// When max_payload is smaller than the headroom itself, fall back to
		// the raw limit instead of rejecting everything.
		{"tiny max payload: under raw limit fits", 100, 4096, false},
		{"tiny max payload: over raw limit exceeds", 4097, 4096, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, exceedsNATSPayloadLimit(tt.size, tt.maxPayload))
		})
	}
}

func TestFormatByteSize(t *testing.T) {
	assert.Equal(t, "512 B", formatByteSize(512))
	assert.Equal(t, "1.0 KB", formatByteSize(1024))
	assert.Equal(t, "1.0 MB", formatByteSize(1<<20))
	assert.Equal(t, "5.2 MB", formatByteSize(5464492))
	assert.Equal(t, "2.0 GB", formatByteSize(2<<30))
}

func TestOversizeNATSPayloadError_IsActionable(t *testing.T) {
	msg := oversizeNATSPayloadError("response", 80<<20, maxChunkedReplyBytes, oversizeReplyHint)
	assert.Contains(t, msg, "response too large")
	assert.Contains(t, msg, "80.0 MB")
	assert.Contains(t, msg, "64.0 MB")
	assert.Contains(t, msg, "narrow your search")
}

func TestCapLLMToolContent(t *testing.T) {
	t.Run("small content untouched", func(t *testing.T) {
		assert.Equal(t, "hello", capLLMToolContent("hello"))
	})
	t.Run("at cap untouched", func(t *testing.T) {
		s := strings.Repeat("x", maxLLMToolContentBytes)
		assert.Equal(t, s, capLLMToolContent(s))
	})
	t.Run("over cap truncated with guidance tail", func(t *testing.T) {
		s := strings.Repeat("x", 5<<20) // 5 MB
		got := capLLMToolContent(s)
		assert.Less(t, len(got), maxLLMToolContentBytes+200)
		assert.True(t, strings.HasPrefix(got, "xxxx"), "must keep the head of the content")
		assert.Contains(t, got, "[output truncated: 5.0 MB total — narrow your search or request less data]")
	})
	t.Run("does not split a UTF-8 rune", func(t *testing.T) {
		s := strings.Repeat("é", maxLLMToolContentBytes) // 2 bytes per rune
		got := capLLMToolContent(s)
		require.Contains(t, got, "[output truncated:")
		kept := got[:strings.Index(got, "\n…")]
		assert.LessOrEqual(t, len(kept), maxLLMToolContentBytes)
		for _, r := range kept {
			assert.Equal(t, 'é', r)
		}
	})
}

// ---------------------------------------------------------------------------
// Loopback tests: real embedded NATS server with a small max_payload,
// real NATSToolBridge subscriptions, real NATSDaemonRouter requester.
// ---------------------------------------------------------------------------

// testMaxPayload is deliberately tiny so tests can trip the limit without
// allocating gigabytes. With natsPayloadHeadroom = 8KB the effective
// single-message budget (and per-chunk payload) is 8KB.
const testMaxPayload = 16 * 1024

// startPayloadTestNATS runs an in-process NATS server with a small
// max_payload and returns a client connection. Mirrors the helper pattern in
// internal/daemonliveness's tests.
func startPayloadTestNATS(t *testing.T) *nats.Conn {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = -1 // random port
	opts.MaxPayload = testMaxPayload
	srv := natstest.RunServer(&opts)
	t.Cleanup(srv.Shutdown)

	if !srv.ReadyForConnections(2 * time.Second) {
		t.Fatal("test nats server failed to come up")
	}
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	require.Equal(t, int64(testMaxPayload), nc.MaxPayload(),
		"connection must report the server's max_payload")
	return nc
}

// payloadTestMgr reuses the recordingDaemonMgr stub (nats_bridge_drain_test.go)
// but returns canned daemon-command / tool-sync responses so tests can control
// reply sizes.
type payloadTestMgr struct {
	recordingDaemonMgr
	cmdResp  *reliantv1.DaemonCommandResponse
	toolResp *ToolExecutionResponse
}

func (m *payloadTestMgr) SendDaemonCommand(ctx context.Context, userID string, req *reliantv1.DaemonCommandRequest) (*reliantv1.DaemonCommandResponse, error) {
	if m.cmdResp != nil {
		return m.cmdResp, nil
	}
	return m.recordingDaemonMgr.SendDaemonCommand(ctx, userID, req)
}

func (m *payloadTestMgr) SendToolRequestSync(context.Context, string, *ToolExecutionRequest) (*ToolExecutionResponse, error) {
	return m.toolResp, nil
}

// staticResolver resolves a fixed daemon set for any user.
type staticResolver struct{ daemons []DaemonInfo }

func (r staticResolver) ResolveDaemons(context.Context, string, *DaemonSelector) ([]DaemonInfo, error) {
	return r.daemons, nil
}

// startBridgeAndRouter wires a real bridge (subscriber/replier side) and a
// real router (requester side) on the same embedded server.
func startBridgeAndRouter(t *testing.T, mgr DaemonConnectionManager) (*nats.Conn, *NATSDaemonRouter) {
	t.Helper()
	nc := startPayloadTestNATS(t)

	bridge := NewNATSToolBridge(nc, nil, mgr)
	bridge.OnDaemonConnected("user-1", "daemon-1")
	t.Cleanup(func() { _ = bridge.Close() })
	require.NoError(t, nc.Flush(), "flush subscriptions before issuing requests")

	router := NewNATSDaemonRouter(nc, WithResolver(staticResolver{
		daemons: []DaemonInfo{{DaemonID: "daemon-1", Type: "local", Status: "connected"}},
	}))
	return nc, router
}

// A multi-MB daemon.command reply must round-trip byte-identical through the
// transparent chunking (the production failure mode: a 5.4MB fs.search reply
// silently failed to publish and the requester waited out its full timeout —
// user RPCs have no way to "narrow the search", so the transport must make
// large replies just work).
func TestChunkedReply_MultiMBDaemonCommandRoundTrip(t *testing.T) {
	// 2.5MB of varied bytes (~3.4MB envelope after base64) across a 16KB
	// max_payload → hundreds of chunks. Varied content catches any
	// reorder/misalignment bug that uniform bytes would mask.
	want := make([]byte, 2500*1024)
	for i := range want {
		want[i] = byte(i % 251)
	}
	mgr := &payloadTestMgr{
		cmdResp: &reliantv1.DaemonCommandResponse{Success: true, Payload: want},
	}
	_, router := startBridgeAndRouter(t, mgr)

	start := time.Now()
	got, err := router.SendDaemonCommand(context.Background(), "user-1", "fs.search", []byte(`{"pattern":"x"}`), 10000)
	require.NoError(t, err, "oversize reply must round-trip, not error or time out")
	assert.True(t, bytes.Equal(want, got), "reassembled payload must be byte-identical")
	assert.Less(t, time.Since(start), 5*time.Second)
}

// A reply within the limit stays a single plain message on the wire —
// byte-identical to today's msg.Respond fast path (no chunk headers).
func TestChunkedReply_SmallReplySingleMessageOnWire(t *testing.T) {
	want := []byte(`{"matches":[{"file":"a.go","line":1}]}`)
	mgr := &payloadTestMgr{
		cmdResp: &reliantv1.DaemonCommandResponse{Success: true, Payload: want},
	}
	nc, router := startBridgeAndRouter(t, mgr)

	// Raw single-shot request straight at the subject: if the bridge chunked,
	// we'd see the first chunk with its header envelope here.
	reply, err := nc.Request(daemonSubject(daemonCommandSubject, "user-1", "daemon-1"),
		[]byte(`{"request_id":"r1","command_type":"fs.search","payload":{},"timeout_ms":5000}`), 2*time.Second)
	require.NoError(t, err)
	assert.Empty(t, reply.Header.Get(chunkHeaderID), "small reply must not carry chunk headers")
	assert.Empty(t, reply.Header, "small reply must be a plain message, byte-identical to msg.Respond")
	assert.Contains(t, string(reply.Data), `"success":true`)

	// And through the router the payload comes back intact.
	got, err := router.SendDaemonCommand(context.Background(), "user-1", "fs.search", []byte(`{"pattern":"x"}`), 5000)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// Beyond the hard absolute cap chunking gives up: the caller gets the
// structured, actionable error — fast, not a timeout.
func TestChunkedReply_OverAbsoluteCapReturnsStructuredError(t *testing.T) {
	// 49MB raw base64-encodes to ~65.3MB inside the JSON envelope — over the
	// 64MB absolute cap.
	mgr := &payloadTestMgr{
		cmdResp: &reliantv1.DaemonCommandResponse{
			Success: true,
			Payload: bytes.Repeat([]byte{0xAB}, 49<<20),
		},
	}
	_, router := startBridgeAndRouter(t, mgr)

	start := time.Now()
	payload, err := router.SendDaemonCommand(context.Background(), "user-1", "fs.search", []byte(`{"pattern":"x"}`), 10000)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Nil(t, payload)
	assert.Contains(t, err.Error(), "response too large", "error must say the reply was oversize")
	assert.Contains(t, err.Error(), "64.0 MB", "error must name the cap")
	assert.Contains(t, err.Error(), "narrow your search", "error must be actionable")
	assert.Contains(t, err.Error(), `daemon command "fs.search" failed`, "error must name the command")
	assert.NotContains(t, err.Error(), "nats: timeout", "must fail fast, not time out")
	assert.Less(t, elapsed, 5*time.Second)
}

// An oversize tools.request.sync result transits the chunked transport intact
// and is then truncated for the LLM at the executor layer — the model gets
// partial results plus guidance, not an error.
func TestExecutor_OversizeToolSyncContentTruncatedForLLM(t *testing.T) {
	bigContent := strings.Repeat("x", 200*1024) // ~200KB, far over both max_payload and the LLM cap
	mgr := &payloadTestMgr{
		toolResp: &ToolExecutionResponse{RequestID: "req-1", Success: true, Content: bigContent},
	}
	_, router := startBridgeAndRouter(t, mgr)

	executor := NewRemoteExecutor(router)
	result, err := executor.executeOnDaemon(context.Background(), &ToolRequest{
		ToolName:  "grep",
		ToolInput: `{"pattern":"x"}`,
		UserID:    "user-1",
		ChatID:    "chat-1",
		ProjectID: "proj-1",
	}, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.True(t, result.Success, "oversize content is not an error for the tool call")
	assert.False(t, result.IsError)
	assert.True(t, strings.HasPrefix(result.Content, "xxxx"), "model must get the head of the real output")
	assert.Contains(t, result.Content, "[output truncated: 200.0 KB total — narrow your search or request less data]")
	assert.Less(t, len(result.Content), maxLLMToolContentBytes+200,
		"content surfaced to the LLM must be capped")
}

// A tool-sync reply within all limits passes through unchanged.
func TestNATSBridge_ToolSyncReplyWithinLimit_PassesThrough(t *testing.T) {
	mgr := &payloadTestMgr{
		toolResp: &ToolExecutionResponse{RequestID: "req-1", Success: true, Content: "42 matches"},
	}
	_, router := startBridgeAndRouter(t, mgr)

	resp, err := router.SendToolRequestSync(context.Background(), "user-1", &ToolExecutionRequest{
		RequestID: "req-1",
		ToolName:  "grep",
		TimeoutMs: 5000,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.True(t, resp.Success)
	assert.Equal(t, "42 matches", resp.Content)
}

// The request direction is still guarded by a preflight: requests are
// client-constructed, so failing fast with an actionable error (before daemon
// resolution and before any NATS round trip) is correct there.
func TestNATSDaemonRouter_OversizeRequest_FailsFast(t *testing.T) {
	nc := startPayloadTestNATS(t)
	// No resolver on purpose: the preflight must reject before resolution.
	router := NewNATSDaemonRouter(nc)

	// Valid JSON: the daemon-command payload travels as json.RawMessage.
	bigPayload := []byte(`{"content":"` + string(bytes.Repeat([]byte("x"), 32*1024)) + `"}`)

	_, err := router.SendDaemonCommand(context.Background(), "user-1", "fs.write", bigPayload, 5000)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "request too large")
	assert.Contains(t, err.Error(), "smaller chunks")

	_, err = router.SendToolRequestSync(context.Background(), "user-1", &ToolExecutionRequest{
		RequestID: "req-1",
		ToolName:  "write",
		ToolInput: string(bigPayload),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "request too large")

	_, err = router.SendToolRequestSyncWithSelector(context.Background(), "user-1", &ToolExecutionRequest{
		RequestID: "req-1",
		ToolName:  "write",
		ToolInput: string(bigPayload),
	}, &DaemonSelector{Type: "any"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "request too large")
}

// A request to a daemon nobody is serving must keep today's ErrNoResponders
// semantics through the explicit-inbox request path (daemonRequestError maps
// it to CodeUnavailable "no daemon connected").
func TestChunkedRequest_NoRespondersSemanticsPreserved(t *testing.T) {
	nc := startPayloadTestNATS(t)
	// Resolver resolves a daemon, but no bridge is subscribed for it.
	router := NewNATSDaemonRouter(nc, WithResolver(staticResolver{
		daemons: []DaemonInfo{{DaemonID: "daemon-ghost", Type: "local"}},
	}))

	_, err := router.SendDaemonCommand(context.Background(), "user-1", "fs.stat", []byte(`{}`), 2000)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no daemon connected", "ErrNoResponders must map to the friendly unavailable error")
}
