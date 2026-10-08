// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

func makeOwnedPendingMsg(t *testing.T, requestID, userID string) *stubMsg {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"request_id":   requestID,
		"command_type": "git.clone",
		"payload":      json.RawMessage(`{"token":"secret"}`),
		"timeout_ms":   1000,
		"user_id":      userID,
	})
	require.NoError(t, err)
	return &stubMsg{data: data}
}

func drainOnce(t *testing.T, connUser string, msgs ...jetstream.Msg) *recordingDaemonMgr {
	t.Helper()
	mgr := &recordingDaemonMgr{}
	consumer := &stubConsumer{batches: []jetstream.MessageBatch{
		&stubMessageBatch{msgs: msgs, err: jetstream.ErrMsgIteratorClosed},
	}}
	bridge := newTestBridge(&stubJetStream{stream: &stubStream{consumer: consumer}}, mgr)
	defer bridge.cancel()
	bridge.drainPendingCommands(context.Background(), connUser, "daemon-1")
	return mgr
}

// A queued command stamped for user-A must never run on user-B's connection,
// even when B holds the daemon id's queue.
func TestDrainPendingCommands_TermsMessageQueuedForAnotherUser(t *testing.T) {
	foreign := makeOwnedPendingMsg(t, "req-foreign", "user-A")
	mine := makeOwnedPendingMsg(t, "req-mine", "user-B")

	mgr := drainOnce(t, "user-B", foreign, mine)

	require.True(t, foreign.wasTermed(), "mismatched owner must be Term()'d")
	require.False(t, foreign.wasAcked())
	require.False(t, foreign.wasNaked())
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	require.Len(t, mgr.commands, 1, "only the matching message is dispatched")
	require.Equal(t, "req-mine", mgr.commands[0].RequestId)
	require.True(t, mine.wasAcked())
	require.False(t, mine.wasTermed())
}

// A queued command with no user_id has no provable owner — the daemon id in
// its subject is client-chosen — so it is Term()'d, never dispatched.
func TestDrainPendingCommands_TermsMessageWithoutUserID(t *testing.T) {
	unowned := makeOwnedPendingMsg(t, "req-unowned", "")
	mine := makeOwnedPendingMsg(t, "req-mine", "user-B")

	mgr := drainOnce(t, "user-B", unowned, mine)

	require.True(t, unowned.wasTermed(), "a message without user_id must be Term()'d")
	require.False(t, unowned.wasAcked())
	require.False(t, unowned.wasNaked())
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	require.Len(t, mgr.commands, 1, "only the owned message is dispatched")
	require.Equal(t, "req-mine", mgr.commands[0].RequestId)
}

// An empty connection user must not "match" an envelope that also omits
// user_id: the comparison is never between two absent owners.
func TestPendingEnvelopeOwnerMatches(t *testing.T) {
	require.True(t, pendingEnvelopeOwnerMatches("user-A", "user-A"))
	require.False(t, pendingEnvelopeOwnerMatches("user-A", "user-B"))
	require.False(t, pendingEnvelopeOwnerMatches("", "user-A"))
	require.False(t, pendingEnvelopeOwnerMatches("", ""))
}

// One user's connect/disconnect for a daemon id must not unsubscribe or cancel
// another user's bridge state for the same id.
func TestBridgeBookkeepingIsScopedToUser(t *testing.T) {
	bridge := newTestBridge(nil, &recordingDaemonMgr{})
	defer bridge.cancel()

	cancelledA := false
	bridge.daemonCancels[daemonKey("user-A", "d-1")] = func() { cancelledA = true }
	bridge.daemonSubs[daemonKey("user-A", "d-1")] = []*nats.Subscription{}

	bridge.OnDaemonDisconnected("user-B", "d-1")

	require.False(t, cancelledA, "B's teardown must not cancel A's forwarders")
	_, ok := bridge.daemonCancels[daemonKey("user-A", "d-1")]
	require.True(t, ok)
	_, ok = bridge.daemonSubs[daemonKey("user-A", "d-1")]
	require.True(t, ok)

	bridge.OnDaemonDisconnected("user-A", "d-1")
	require.True(t, cancelledA)
	require.Empty(t, bridge.daemonCancels)
	require.Empty(t, bridge.daemonSubs)
}
