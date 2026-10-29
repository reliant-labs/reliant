// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/queueddelivery"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// namedMachines reports online exactly the machines in up: a selector answers
// by id, else by name, else it is the user's default machine ("").
type namedMachines struct {
	up       map[string]bool
	attached map[string]time.Time
}

func (m namedMachines) key(s *toolexec.DaemonSelector) string {
	switch {
	case s == nil:
	case s.ID != "":
		return s.ID
	case s.Name != "":
		return "name:" + s.Name
	}
	return ""
}

func (m namedMachines) AttachedSince(_ context.Context, _ string, s *toolexec.DaemonSelector) (time.Time, bool, error) {
	k := m.key(s)
	return m.attached[k], m.up[k], nil
}

func (m namedMachines) IsDaemonOnline(_ context.Context, _ string, s *toolexec.DaemonSelector) (bool, error) {
	return m.up[m.key(s)], nil
}

// preflightFailsAgain is what the run delivery starts does when the machine its
// workflow pins is down: its preflight fails and queues the message for that
// machine again (PreflightDaemonCheckActivity.terminal), and the run ends
// failed.
func (f continueFixture) preflightFailsAgain(t *testing.T, target db.QueuedMachineTarget) {
	t.Helper()
	require.NoError(t, f.repo.UpdateWorkflowStatus(f.ctx, f.chatID, db.Failed()))
	f.temporal.mu.Lock()
	f.temporal.status = enums.WORKFLOW_EXECUTION_STATUS_FAILED
	f.temporal.mu.Unlock()
	_, err := f.repo.QueueChatForMachine(f.ctx, f.chatID, target, "the machine this request asked for is not connected")
	require.NoError(t, err)
}

func (f continueFixture) elapseBackoff(t *testing.T) {
	t.Helper()
	_, err := f.repo.ORM().Exec(f.ctx, `UPDATE chat_queued_deliveries SET next_attempt_at = NULL WHERE chat_id = $1`, f.chatID)
	require.NoError(t, err)
}

func (f continueFixture) stopNotices(t *testing.T) []string {
	t.Helper()
	updates, err := f.repo.GetUpdatesSince(f.ctx, f.chatID, 0, 1000)
	require.NoError(t, err)
	var out []string
	for _, u := range updates {
		if u.UpdateType != reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_ERROR {
			continue
		}
		var data map[string]any
		require.NoError(t, json.Unmarshal(u.Data, &data))
		if data["activity_type"] == queuedDeliveryStoppedErrorType {
			out = append(out, data["error_message"].(string))
		}
	}
	return out
}

// The 8,928-run loop, end to end on a real database. The chat's own machine
// ("default") is connected and the workflow pins another ("cicd"); the run
// delivery starts fails preflight on cicd and queues the message again, every
// time.
//
// While cicd is down delivery must start nothing: the message remembers it
// waits for cicd, so the chat's own machine being up is not news. If cicd
// reads as connected but the runs still cannot reach it (a flapping machine,
// a gateway that has not caught up), the attempt limit stops the loop after
// core.MaxQueuedDeliveryAttempts runs, leaves the chat in a state its owner
// can see and retry from, and starts again, once, when cicd connects.
func TestQueuedDeliveryLoop_StopsAfterNAttemptsAndResumesWhenThePinnedMachineConnects(t *testing.T) {
	t.Parallel()

	f := newContinueFixture(t, true)
	pinned := db.QueuedMachineTarget{DaemonName: "cicd"}
	_, err := f.repo.ORM().Exec(f.ctx, `UPDATE chats SET active_daemon_id = 'default' WHERE id = $1`, f.chatID)
	require.NoError(t, err)
	machines := namedMachines{up: map[string]bool{"default": true}, attached: map[string]time.Time{}}
	delivery := queueddelivery.New(queueddelivery.Deps{Store: f.repo, Machines: machines, Deliverer: f.svc, Signaler: f.temporal})

	// The first run's preflight queues the message for cicd.
	f.preflightFailsAgain(t, pinned)
	require.Equal(t, reliantv1.ChatActivity_CHAT_ACTIVITY_QUEUED_FOR_MACHINE, f.activity(t))

	// Hours of 30-second reconciler passes with default up and cicd down.
	for range 200 {
		n, err := delivery.Sweep(f.ctx)
		require.NoError(t, err)
		require.Zero(t, n)
	}
	assert.Zero(t, f.pause.resetCount(), "delivery waits for cicd, not for the chat's own machine")

	// cicd reads as connected, but every run still fails at preflight.
	machines.up["name:cicd"] = true
	runs := 0
	for pass := 0; pass < 100; pass++ {
		f.elapseBackoff(t)
		n, err := delivery.Sweep(f.ctx)
		require.NoError(t, err)
		if n > 0 {
			runs++
			f.preflightFailsAgain(t, pinned)
		}
	}
	assert.Equal(t, core.MaxQueuedDeliveryAttempts, runs, "bounded: N runs, however many passes")
	assert.Equal(t, core.MaxQueuedDeliveryAttempts, f.pause.resetCount())

	// A terminal state the owner can see: the chat is no longer "queued", and
	// it says which machine it waits for, since when, and how to carry on.
	assert.NotEqual(t, reliantv1.ChatActivity_CHAT_ACTIVITY_QUEUED_FOR_MACHINE, f.activity(t))
	notices := f.stopNotices(t)
	require.Len(t, notices, 1, "told once")
	assert.Contains(t, notices[0], "Machine `cicd` didn't accept the run")
	assert.Contains(t, notices[0], "didn't accept the run after 5 tries")
	assert.NotContains(t, notices[0], "offline", "delivery saw the machine connected; it does not claim otherwise")
	assert.Contains(t, notices[0], "Last error: the machine this request asked for is not connected")
	assert.Contains(t, strings.ToLower(notices[0]), "send a message to retry")

	// cicd connects afresh once the hold has passed: exactly one more attempt.
	f.expireGiveUpHold(t)
	machines.attached["name:cicd"] = time.Now().Add(time.Minute)
	require.Equal(t, 1, delivery.MachineConnected(f.ctx, "test-user"))
	assert.Equal(t, core.MaxQueuedDeliveryAttempts+1, f.pause.resetCount())
	f.preflightFailsAgain(t, pinned)
	f.elapseBackoff(t)
	n, err := delivery.Sweep(f.ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "that attempt used the connect's one try; the sweep does not take another")
}

// The user acting always works: a message sent to a chat delivery gave up on
// starts its run and forgets the attempts spent on it.
func TestQueuedDeliveryLoop_TheOwnersSendEndsTheStop(t *testing.T) {
	t.Parallel()

	f := newContinueFixture(t, true)
	pinned := db.QueuedMachineTarget{DaemonName: "cicd"}
	f.preflightFailsAgain(t, pinned)
	for range core.MaxQueuedDeliveryAttempts {
		require.NoError(t, f.repo.RecordQueuedDeliveryAttempt(f.ctx, f.chatID, time.Now()))
	}
	stopped, err := f.repo.GiveUpQueuedDelivery(f.ctx, f.chatID)
	require.NoError(t, err)
	require.True(t, stopped)
	require.Equal(t, 1, countRecords(t, f), "parked")

	settled, err := f.repo.SettleQueuedDelivery(f.ctx, f.chatID)
	require.NoError(t, err)
	assert.False(t, settled, "the marker was already cleared by giving up")
	var n int
	require.NoError(t, f.repo.DB.QueryRowContext(f.ctx, `SELECT count(*) FROM chat_queued_deliveries WHERE chat_id = $1`, f.chatID).Scan(&n))
	assert.Zero(t, n, "the attempts spent on the message are forgotten")
}

func (f continueFixture) expireGiveUpHold(t *testing.T) {
	t.Helper()
	_, err := f.repo.ORM().Exec(f.ctx, `UPDATE chat_queued_deliveries SET next_attempt_at = NOW() - interval '1 second' WHERE chat_id = $1`, f.chatID)
	require.NoError(t, err)
}

func countRecords(t *testing.T, f continueFixture) int {
	t.Helper()
	var n int
	require.NoError(t, f.repo.DB.QueryRowContext(f.ctx, `SELECT count(*) FROM chat_queued_deliveries WHERE chat_id = $1`, f.chatID).Scan(&n))
	return n
}

// A message queued before targets were recorded names no machine, and the
// chat's own machine is not necessarily the one that failed: the notice does
// not blame it.
func TestQueuedDeliveryStoppedMessage_DoesNotBlameTheChatsOwnMachineWhenNoTargetWasRecorded(t *testing.T) {
	t.Parallel()

	f := newContinueFixture(t, true)
	queued := db.QueuedForMachineChat{
		ChatID: f.chatID, UserID: "test-user", Attempts: 5, Since: time.Date(2026, 10, 27, 15, 35, 0, 0, time.UTC),
		ActiveDaemonID: ptrTo("2aab1465-default-machine"),
		LastFailure:    "the machine this request asked for is not connected",
	}

	msg := f.svc.queuedDeliveryStoppedMessage(f.ctx, queued)

	assert.NotContains(t, msg, "2aab1465", "the chat's own machine is not named as the culprit")
	assert.Contains(t, msg, "The machine didn't accept the run after 5 tries")
	assert.Contains(t, msg, "Send a message to retry")
}

func ptrTo(s string) *string { return &s }
