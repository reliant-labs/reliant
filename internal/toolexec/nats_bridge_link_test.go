// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The pending-command poller is created with the daemon's subscriptions but
// starts only once the gateway says the daemon's link is ready. Started at
// registration, it fired every queued command into a laptop that had
// re-registered while still mostly asleep, where each one timed out and spent
// a delivery (prod 2026-10-30).
func TestPendingPoller_StartsOnlyWhenTheLinkIsReady(t *testing.T) {
	mgr := &recordingDaemonMgr{}
	msg := makePendingMsg(t, "req-1", "fs.mkdir", json.RawMessage(`{}`), 5000)
	consumer := &stubConsumer{batches: []jetstream.MessageBatch{
		&stubMessageBatch{msgs: []jetstream.Msg{msg}, err: jetstream.ErrMsgIteratorClosed},
	}}
	bridge := newTestBridge(&stubJetStream{stream: &stubStream{consumer: consumer}}, mgr)
	defer bridge.cancel()

	daemonCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bridge.finishDaemonConnected(daemonCtx, pendingDrainUser, "laptop", nil)

	// forge:lint-disable-next-line forge-test-fixed-sleep: asserting nothing happens — a poller correctly not started drains nothing, which leaves no event to wait for
	time.Sleep(50 * time.Millisecond)
	mgr.mu.Lock()
	require.Empty(t, mgr.commands, "nothing is drained into a link that has not proven itself")
	mgr.mu.Unlock()

	bridge.OnDaemonLinkReady(pendingDrainUser, "laptop", "", "", "")
	bridge.OnDaemonLinkReady(pendingDrainUser, "laptop", "", "", "") // once is enough
	require.Eventually(t, msg.wasAcked, 5*time.Second, time.Millisecond, "the queued command is delivered once the link is ready")
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	require.Len(t, mgr.commands, 1)
	require.Equal(t, []string{"laptop"}, mgr.targets)
}

// A link that drops under a queued command stops the drain there: the rest of
// the batch goes back on the queue unsent instead of into the dead link one
// timeout at a time. And the queued commands keep their delivery budget: a
// delivery the link swallowed is excused, so a command one strike from
// maxPendingCommandDeliveries is not dropped by the next real failure.
func TestDrainPendingCommands_LinkLossStopsThePassAndSparesTheBudget(t *testing.T) {
	interrupted := &recordingDaemonMgr{err: fmt.Errorf("%w while waiting for command %q response", ErrDaemonConnectionInterrupted, "git.clone")}
	first := makePendingMsg(t, "req-1", "git.clone", json.RawMessage(`{}`), 5000)
	first.numDelivered = maxPendingCommandDeliveries - 1 // one strike from being dropped
	second := makePendingMsg(t, "req-2", "fs.mkdir", json.RawMessage(`{}`), 5000)
	third := makePendingMsg(t, "req-3", "fs.mkdir", json.RawMessage(`{}`), 5000)
	bridge := newTestBridge(&stubJetStream{stream: &stubStream{consumer: &stubConsumer{batches: []jetstream.MessageBatch{
		&stubMessageBatch{msgs: []jetstream.Msg{first, second, third}, err: jetstream.ErrMsgIteratorClosed},
	}}}}, interrupted)
	defer bridge.cancel()

	bridge.drainPendingCommands(context.Background(), pendingDrainUser, "laptop")

	interrupted.mu.Lock()
	require.Len(t, interrupted.commands, 1, "the pass stops at the first command the link dropped")
	interrupted.mu.Unlock()
	for _, m := range []*stubMsg{first, second, third} {
		assert.False(t, m.wasAcked(), "nothing is acked (dropped) because the link went away")
		assert.True(t, m.wasNaked(), "everything goes back on the queue")
	}

	// Redelivered once more (its delivery count now at the bound), the same
	// command times out on a live link: that IS a strike, but the excused
	// delivery is not, so it is retried rather than dropped.
	first.mu.Lock()
	first.naked, first.numDelivered = false, maxPendingCommandDeliveries
	first.mu.Unlock()
	timedOut := &recordingDaemonMgr{err: fmt.Errorf("daemon command %q timed out after 5s", "git.clone")}
	bridge.mgr = timedOut
	bridge.js = &stubJetStream{stream: &stubStream{consumer: &stubConsumer{batches: []jetstream.MessageBatch{
		&stubMessageBatch{msgs: []jetstream.Msg{first}, err: jetstream.ErrMsgIteratorClosed},
	}}}}
	bridge.drainPendingCommands(context.Background(), pendingDrainUser, "laptop")
	assert.False(t, first.wasAcked(), "an excused delivery must not count toward dropping the command")
	assert.True(t, first.wasNaked())
}

// A timeout also stops the pass: whatever made the daemon miss one command's
// deadline, the next queued command would most likely meet it too.
func TestDrainPendingCommands_TimeoutStopsThePass(t *testing.T) {
	mgr := &recordingDaemonMgr{err: fmt.Errorf("daemon command %q timed out after 5s", "fs.mkdir")}
	first := makePendingMsg(t, "req-1", "fs.mkdir", json.RawMessage(`{}`), 5000)
	second := makePendingMsg(t, "req-2", "fs.mkdir", json.RawMessage(`{}`), 5000)
	bridge := newTestBridge(&stubJetStream{stream: &stubStream{consumer: &stubConsumer{batches: []jetstream.MessageBatch{
		&stubMessageBatch{msgs: []jetstream.Msg{first, second}, err: jetstream.ErrMsgIteratorClosed},
	}}}}, mgr)
	defer bridge.cancel()

	bridge.drainPendingCommands(context.Background(), pendingDrainUser, "laptop")

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	require.Len(t, mgr.commands, 1)
	assert.True(t, first.wasNaked())
	assert.True(t, second.wasNaked(), "the undispatched rest of the batch goes back on the queue")
	assert.False(t, second.wasAcked())
}

// The gateway's "held for a link that never answered" travels to the
// requester as its own code, and comes back as ErrDaemonReconnecting: a
// request that provably never ran.
func TestTransportErrorCode_Reconnecting(t *testing.T) {
	err := fmt.Errorf("daemon command %q held 15s: %w", "terminal.create", ErrDaemonReconnecting)
	require.Equal(t, ErrorCodeDaemonReconnecting, transportErrorCode(err))
	require.True(t, IsTransportErrorCode(ErrorCodeDaemonReconnecting))
}
