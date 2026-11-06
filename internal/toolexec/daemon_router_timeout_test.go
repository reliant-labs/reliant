// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/nats-io/nats.go"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startSilentDaemon subscribes to user-1/daemon-1's command subject and never
// answers: a machine that is connected (NATS has a responder, so this is not
// "no daemon connected") but too slow to reply — the saturated cloud machine
// behind the 2026-10-10 GetFileTree page.
func startSilentDaemon(t *testing.T) *NATSDaemonRouter {
	t.Helper()
	router, _ := startRecordingSilentDaemon(t)
	return router
}

// startRecordingSilentDaemon is startSilentDaemon that also hands back every
// command envelope it received.
func startRecordingSilentDaemon(t *testing.T) (*NATSDaemonRouter, <-chan []byte) {
	t.Helper()
	nc := startPayloadTestNATS(t)
	envelopes := make(chan []byte, 8)
	sub, err := nc.Subscribe(daemonSubject(daemonCommandSubject, "user-1", "daemon-1"), func(msg *nats.Msg) {
		envelopes <- msg.Data
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	require.NoError(t, nc.Flush())
	return NewNATSDaemonRouter(nc), envelopes
}

func envelopeTimeoutMs(t *testing.T, envelopes <-chan []byte) int32 {
	t.Helper()
	select {
	case data := <-envelopes:
		var env struct {
			TimeoutMs int32 `json:"timeout_ms"`
		}
		require.NoError(t, json.Unmarshal(data, &env))
		return env.TimeoutMs
	case <-time.After(2 * time.Second):
		t.Fatal("the daemon received no command")
		return 0
	}
}

// The budget the daemon and gateway receive is cut to the caller's deadline:
// they bound their own work by it, so a 30s budget under a 10s rpc deadline
// kept a saturated machine walking a file tree, and the gateway waiting, for
// 20s after the rpc had failed (2026-10-10).
func TestSendDaemonCommand_EnvelopeBudgetIsBoundedByTheCallersDeadline(t *testing.T) {
	router, envelopes := startRecordingSilentDaemon(t)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _ = router.SendDaemonCommandToDaemon(ctx, "user-1", "daemon-1", "fs.get_tree", []byte(`{}`), 30000)

	got := envelopeTimeoutMs(t, envelopes)
	assert.Greater(t, got, int32(0), "never 0, which reads as no budget")
	assert.LessOrEqual(t, got, int32(200))
}

// A caller with no deadline, or one later than its budget, sends the budget
// it named, unchanged — including none at all.
func TestSendDaemonCommand_EnvelopeBudgetIsUnchangedWithoutASoonerDeadline(t *testing.T) {
	router, envelopes := startRecordingSilentDaemon(t)

	_, _ = router.SendDaemonCommandToDaemon(context.Background(), "user-1", "daemon-1", "fs.get_tree", []byte(`{}`), 50)
	assert.Equal(t, int32(50), envelopeTimeoutMs(t, envelopes))

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	go func() {
		_, _ = router.SendDaemonCommandToDaemon(ctx, "user-1", "daemon-1", "fs.get_tree", []byte(`{}`), 50)
	}()
	assert.Equal(t, int32(50), envelopeTimeoutMs(t, envelopes))

	assert.Equal(t, int32(0), callerBoundedTimeoutMs(context.Background(), 0))
}

// The caller's deadline passing — the rpc's 10s budget, which bounds the NATS
// request too — is a daemon timeout whichever side of the race notices first,
// and the error names the command and the daemon it was addressed to. It used
// to read "daemon command via NATS failed: context deadline exceeded", which
// said neither, and every proxy surfaced it as Internal.
func TestSendDaemonCommand_DeadlineIsADaemonTimeout(t *testing.T) {
	router := startSilentDaemon(t)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := router.SendDaemonCommandToDaemon(ctx, "user-1", "daemon-1", "fs.get_tree", []byte(`{}`), 30000)

	require.Error(t, err)
	assert.True(t, IsDaemonTimeout(err), "error: %v", err)
	assert.Equal(t, connect.CodeDeadlineExceeded, svcerr.Code(err))
	assert.Equal(t, svcerr.ClassServer, svcerr.Classify(err), "a slow machine stays visible")
	assert.Contains(t, err.Error(), "fs.get_tree")
	assert.Contains(t, err.Error(), daemonSubject(daemonCommandSubject, "user-1", "daemon-1"))
}

// The command's own budget running out is the same timeout.
func TestSendDaemonCommand_CommandBudgetIsADaemonTimeout(t *testing.T) {
	router := startSilentDaemon(t)

	_, err := router.SendDaemonCommandToDaemon(context.Background(), "user-1", "daemon-1", "fs.get_tree", []byte(`{}`), 50)

	require.Error(t, err)
	assert.True(t, IsDaemonTimeout(err), "error: %v", err)
	assert.Equal(t, connect.CodeDeadlineExceeded, svcerr.Code(err))
}

// The caller going away ends the send at once, well inside the command's
// budget, and tells the daemon to drop the command it was sent: the worktree
// reads cancel their directory probe once the read has answered
// (gitReadOnDisk), and rely on this so the probe never outlives the rpc.
func TestSendDaemonCommand_CancelReturnsAndCancelsOnTheDaemon(t *testing.T) {
	nc := startPayloadTestNATS(t)
	commands := make(chan []byte, 1)
	cmdSub, err := nc.Subscribe(daemonSubject(daemonCommandSubject, "user-1", "daemon-1"), func(msg *nats.Msg) {
		commands <- msg.Data // never answered
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cmdSub.Unsubscribe() })
	cancels := make(chan []byte, 1)
	cancelSub, err := nc.Subscribe(daemonSubject(toolCancelSubject, "user-1", "daemon-1"), func(msg *nats.Msg) {
		cancels <- msg.Data
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cancelSub.Unsubscribe() })
	require.NoError(t, nc.Flush())
	router := NewNATSDaemonRouter(nc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sent := make(chan error, 1)
	go func() {
		_, err := router.SendDaemonCommandToDaemon(ctx, "user-1", "daemon-1", "worktree.validate_path", []byte(`{"path":"/w"}`), 60000)
		sent <- err
	}()

	var envelope struct {
		RequestID string `json:"request_id"`
	}
	select {
	case data := <-commands:
		require.NoError(t, json.Unmarshal(data, &envelope))
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon received no command")
	}
	cancel()

	select {
	case err := <-sent:
		require.Error(t, err)
		assert.Equal(t, svcerr.ClassCanceled, svcerr.Classify(err))
	case <-time.After(10 * time.Second):
		t.Fatal("the send did not return when its context was cancelled")
	}
	select {
	case data := <-cancels:
		var c struct {
			RequestID string `json:"request_id"`
		}
		require.NoError(t, json.Unmarshal(data, &c))
		assert.Equal(t, envelope.RequestID, c.RequestID)
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon was not told to drop the command")
	}
}

// The caller going away is a cancellation, not a timeout.
func TestSendDaemonCommand_CancelIsNotADaemonTimeout(t *testing.T) {
	router := startSilentDaemon(t)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err := router.SendDaemonCommandToDaemon(ctx, "user-1", "daemon-1", "fs.get_tree", []byte(`{}`), 30000)

	require.Error(t, err)
	assert.False(t, IsDaemonTimeout(err), "error: %v", err)
	assert.Equal(t, svcerr.ClassCanceled, svcerr.Classify(err))
	assert.Contains(t, err.Error(), "fs.get_tree")
}
