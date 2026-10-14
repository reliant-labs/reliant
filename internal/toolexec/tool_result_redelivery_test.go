// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A result's request id comes back from the daemon and becomes a subject
// token, so the publisher refuses one that would address a different subject.
func TestToolResultPublisherOnlyPublishesToTheRequestsOwnSubject(t *testing.T) {
	nc := startBridgeTestNATS(t)
	pub := NewToolResultPublisher(nc)

	for _, bad := range []string{"", "a.b", "a*", ">", "req 1", "tools.result.u.d.x"} {
		err := pub.PublishToolResult(context.Background(), "user-1", "daemon-1", &ToolExecutionResponse{RequestID: bad})
		require.Error(t, err, "request id %q must be refused", bad)
	}

	requestID := uuid.NewString()
	inbox, err := openReplyInbox(nc, toolResultSubject("user-1", "daemon-1", requestID))
	require.NoError(t, err)
	defer inbox.close()
	require.NoError(t, pub.PublishToolResult(context.Background(), "user-1", "daemon-1",
		&ToolExecutionResponse{RequestID: requestID, Success: true, Content: "done"}))

	msg, err := inbox.next(context.Background(), time.Now().Add(5*time.Second))
	require.NoError(t, err)
	resp, err := decodeToolReply(msg)
	require.NoError(t, err)
	assert.Equal(t, "done", resp.Content)
}

// A result past the chunked-reply cap is replaced by the structured
// too-large error, on the same subject, so the waiter is answered.
func TestToolResultPublisherReplacesAnOversizeResult(t *testing.T) {
	nc := startBridgeTestNATS(t)
	requestID := uuid.NewString()
	inbox, err := openReplyInbox(nc, toolResultSubject("user-1", "daemon-1", requestID))
	require.NoError(t, err)
	defer inbox.close()

	huge := string(make([]byte, maxChunkedReplyBytes+1))
	require.NoError(t, NewToolResultPublisher(nc).PublishToolResult(context.Background(), "user-1", "daemon-1",
		&ToolExecutionResponse{RequestID: requestID, Content: huge}))

	msg, err := inbox.next(context.Background(), time.Now().Add(5*time.Second))
	require.NoError(t, err)
	resp, err := decodeToolReply(msg)
	require.NoError(t, err)
	assert.Equal(t, "RESPONSE_TOO_LARGE", resp.ErrorCode)
}

// cancelRecordingMgr records the cancels the bridge forwards.
type cancelRecordingMgr struct {
	recordingDaemonMgr
	mu      sync.Mutex
	cancels []string
}

func (m *cancelRecordingMgr) SendToolExecutionCancel(_ context.Context, _, _, requestID, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancels = append(m.cancels, requestID)
	return nil
}

// A draining replica stops answering for the daemon's new work — so the next
// request finds the replica the daemon moved to, never both — but still
// forwards cancels, which reach executions already running on its stream.
func TestBridgeDrainStopsNewRequestsButStillForwardsCancels(t *testing.T) {
	nc := startBridgeTestNATS(t)
	mgr := &cancelRecordingMgr{}
	bridge := NewNATSToolBridge(nc, nil, mgr)
	t.Cleanup(func() { _ = bridge.Close() })
	bridge.OnDaemonConnected("user-1", "daemon-1")
	require.NoError(t, nc.Flush())

	online, err := nc.Request(daemonSubject(toolOnlineSubject, "user-1", "daemon-1"), nil, 2*time.Second)
	require.NoError(t, err)
	require.Equal(t, "true", string(online.Data))

	bridge.OnDaemonDraining("user-1", "daemon-1")

	_, err = nc.Request(daemonSubject(toolOnlineSubject, "user-1", "daemon-1"), nil, 2*time.Second)
	require.ErrorIs(t, err, nats.ErrNoResponders, "a draining replica must not answer for the daemon")
	_, err = nc.Request(daemonSubject(toolRequestSyncSubject, "user-1", "daemon-1"), []byte(`{}`), 2*time.Second)
	require.ErrorIs(t, err, nats.ErrNoResponders, "nor take its tool requests")

	cancel, err := json.Marshal(map[string]string{"request_id": "req-running", "reason": "user stopped it"})
	require.NoError(t, err)
	require.NoError(t, nc.Publish(daemonSubject(toolCancelSubject, "user-1", "daemon-1"), cancel))
	require.NoError(t, nc.Flush())
	require.Eventually(t, func() bool {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		return len(mgr.cancels) == 1 && mgr.cancels[0] == "req-running"
	}, 5*time.Second, 10*time.Millisecond, "a cancel for a running call must still reach it")
}

// The requester tells the gateway failing a round trip from the daemon
// refusing the command: the redelivery wait reads the first as "ask again",
// the second as "this daemon keeps no results".
func TestDaemonCommandFailuresSayWhoFailed(t *testing.T) {
	for name, tc := range map[string]struct {
		reply          string
		daemonRefusing bool
	}{
		"daemon refused":     {`{"success":false,"error_message":"unknown command type: tool.status"}`, true},
		"gateway round trip": {`{"success":false,"error_message":"daemon command timed out","error_code":"` + ErrorCodeDaemonRoundTrip + `"}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			nc := startBridgeTestNATS(t)
			sub, err := nc.Subscribe(daemonSubject(daemonCommandSubject, "user-1", "daemon-1"), func(m *nats.Msg) {
				_ = m.Respond([]byte(tc.reply))
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = sub.Unsubscribe() })
			require.NoError(t, nc.Flush())

			_, err = NewNATSDaemonRouter(nc).toolRequestState(context.Background(), "user-1", "daemon-1", "req-1")
			require.Error(t, err)
			require.Equal(t, tc.daemonRefusing, errors.Is(err, errDaemonReportedFailure), "%v", err)
		})
	}
}
