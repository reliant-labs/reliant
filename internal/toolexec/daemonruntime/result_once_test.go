// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// toolRequestMessage is a tool request as a gateway forwards it.
func toolRequestMessage(requestID string) *reliantv1.ServerMessage {
	return &reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_ToolRequest{ToolRequest: &reliantv1.ToolRequest{
		RequestId: requestID, ToolName: "no_such_tool", ContextJson: `{"run_step":true}`,
	}}}
}

// A request id the daemon is running is not started a second time: nothing
// runs, nothing is answered, and the one execution's result is what goes out.
func TestRepeatedRequestWhileRunningIsNotRunAgain(t *testing.T) {
	var o resultOutbox
	_, fresh := o.begin("run-1")
	require.True(t, fresh)
	known, fresh := o.begin("run-1")
	assert.False(t, fresh, "a request id runs once")
	assert.Equal(t, toolexec.ToolRequestRunning, known)

	d := newTestDaemonClient("d-once", "u-once")
	s := useTestSession(d, 16)
	d.results.begin("run-2")
	require.NoError(t, d.handleServerMessage(context.Background(), s, toolRequestMessage("run-2")))
	noToolResponse(t, s, "a repeat of a running request runs nothing and answers nothing")
	assert.Zero(t, s.inFlight.Load(), "a repeat holds no stream open")
}

// A request id whose result is held, or retained after its delivery, gets
// that result again, and nothing runs.
func TestRepeatedRequestGetsTheFirstExecutionsResult(t *testing.T) {
	d := newTestDaemonClient("d-once", "u-once")
	s := useTestSession(d, 16)

	d.results.begin("held-1")
	d.results.hold("held-1", d.toolResponse("held-1", finished("first run's output")), nil)
	require.NoError(t, d.handleServerMessage(context.Background(), s, toolRequestMessage("held-1")))
	got := sentToolResponse(t, s)
	assert.Equal(t, "held-1", got.RequestId)
	assert.Equal(t, "first run's output", got.Content)

	d.results.markRetain("kept-1")
	d.results.begin("kept-1")
	d.results.hold("kept-1", d.toolResponse("kept-1", finished("kept output")), nil)
	require.NotNil(t, d.results.ack("kept-1"))
	require.NoError(t, d.handleServerMessage(context.Background(), s, toolRequestMessage("kept-1")))
	got = sentToolResponse(t, s)
	assert.Equal(t, "kept output", got.Content, "the retained result, byte for byte")
	noToolResponse(t, s, "and nothing ran")
}

// A request id that ran, whose result was delivered and is no longer held,
// gets the daemon's own "it already ran; outcome unknown" answer: never a
// second run, and never a verdict on the command.
func TestRepeatedRequestWhoseResultIsGoneSaysItAlreadyRan(t *testing.T) {
	d := newTestDaemonClient("d-once", "u-once")
	s := useTestSession(d, 16)

	d.results.begin("tool-1")
	d.results.hold("tool-1", d.toolResponse("tool-1", finished("out")), nil)
	require.NotNil(t, d.results.ack("tool-1")) // not retained: a model's call
	require.NoError(t, d.handleServerMessage(context.Background(), s, toolRequestMessage("tool-1")))
	got := sentToolResponse(t, s)
	assert.Equal(t, "tool-1", got.RequestId)
	assert.True(t, got.IsError)
	assert.Equal(t, toolexec.ErrorCodeDaemonRestarted, got.ErrorCode)
	assert.True(t, toolexec.IsTransportErrorCode(got.ErrorCode), "outcome unknown, not a failed command")
	assert.Contains(t, got.Content, "already ran on this machine")
	noToolResponse(t, s, "and nothing ran")
}

// A fresh request id runs as it always did.
func TestFreshRequestRuns(t *testing.T) {
	d := newTestDaemonClient("d-once", "u-once")
	s := useTestSession(d, 16)
	require.NoError(t, d.handleServerMessage(context.Background(), s, toolRequestMessage("new-1")))
	require.Eventually(t, func() bool {
		return d.results.state("new-1") == toolexec.ToolRequestCompleted
	}, 5*time.Second, 5*time.Millisecond, "the request ran and its result is held")
	assert.Equal(t, "new-1", sentToolResponse(t, s).RequestId)
}

// A run step's result, delivered to a worker that was going away, outlives a
// daemon restart for its retention window: the retry that reattaches after
// the restart is answered with it, not refused.
func TestRetainedResultSurvivesADaemonRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox")
	first, _ := newStoredOutbox(t, dir)
	first.markRetain("run-1")
	startRunning(first, "run-1")
	first.hold("run-1", resultMsg("run-1", "deployed"), nil)
	require.NotNil(t, first.ack("run-1"))
	require.Equal(t, []string{"run-1.ran", "run-1.retained"}, outboxFiles(t, dir), "the acknowledged run step's result is kept")

	second, report := newStoredOutbox(t, dir) // the restart
	assert.Equal(t, 1, report.Retained)
	assert.Zero(t, report.Restored, "it is not held again: it was delivered")
	assert.Equal(t, toolexec.ToolRequestDelivered, second.state("run-1"))
	kept := second.retainedResult("run-1")
	require.NotNil(t, kept)
	assert.Equal(t, "deployed", kept.msg.GetToolResponse().GetContent())
	known, fresh := second.begin("run-1")
	assert.False(t, fresh, "and a restarted daemon still never runs it again")
	assert.Equal(t, toolexec.ToolRequestDelivered, known)
}

// The window and the budget hold on disk: a retained result past its window
// is gone after a restart, and one dropped from memory leaves no file.
func TestRetainedResultFilesFollowTheirBounds(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox")
	o, _ := newStoredOutbox(t, dir)
	for _, id := range []string{"old", "new"} {
		o.markRetain(id)
		startRunning(o, id)
		o.hold(id, resultMsg(id, id), nil)
		require.NotNil(t, o.ack(id))
	}
	past := time.Now().Add(-resultRetainTTL - time.Minute)
	o.mu.Lock()
	o.retained["old"].heldAt = past
	o.mu.Unlock()
	assert.Nil(t, o.retainedResult("old"))
	assert.Equal(t, []string{"new.ran", "new.retained", "old.ran"}, outboxFiles(t, dir), "an expired retained result leaves no file, only its tombstone")

	// A file whose window ended while the daemon was down is not reloaded.
	require.NoError(t, os.Chtimes(filepath.Join(dir, "new.retained"), past, past))
	again, report := newStoredOutbox(t, dir)
	assert.Zero(t, report.Retained)
	assert.Equal(t, 1, report.Expired)
	assert.Equal(t, toolexec.ToolRequestUnknown, again.state("new"))
	assert.Equal(t, []string{"new.ran", "old.ran"}, outboxFiles(t, dir), "only the tombstones are left")
}

// A run step's id never runs again for as long as its activity can be
// retried, whatever became of its result: not after the result's delivery
// aged out of the outbox, not after an unacknowledged result was dropped over
// budget, and not after a restart on top of either. Only the tombstone's own
// window ends it.
func TestRunStepIDNeverRunsAgainAfterItsResultIsGone(t *testing.T) {
	// Delivered, retained, and both past their windows.
	d := newTestDaemonClient("d-once", "u-once")
	s := useTestSession(d, 16)
	d.results.markRetain("run-aged")
	d.results.begin("run-aged")
	d.results.hold("run-aged", d.toolResponse("run-aged", finished("deployed")), nil)
	require.NotNil(t, d.results.ack("run-aged"))
	past := time.Now().Add(-resultHoldTTL - time.Hour)
	d.results.mu.Lock()
	d.results.delivered["run-aged"] = past
	d.results.retained["run-aged"].heldAt = past
	d.results.mu.Unlock()
	assert.Nil(t, d.results.retainedResult("run-aged"), "the result is gone")
	assert.Equal(t, toolexec.ToolRequestUnknown, d.results.state("run-aged"), "and so is its delivery")
	require.NoError(t, d.handleServerMessage(context.Background(), s, toolRequestMessage("run-aged")))
	got := sentToolResponse(t, s)
	assert.Equal(t, toolexec.ErrorCodeDaemonRestarted, got.ErrorCode)
	assert.Contains(t, got.Content, "already ran on this machine")
	noToolResponse(t, s, "and nothing ran")

	// Finished, never acknowledged, and dropped over the hold budget; then a
	// restart.
	dir := filepath.Join(t.TempDir(), "outbox")
	o, _ := newStoredOutbox(t, dir)
	o.budget = 128
	o.markRetain("run-big")
	startRunning(o, "run-big")
	o.hold("run-big", resultMsg("run-big", string(make([]byte, 1024))), nil)
	assert.Equal(t, toolexec.ToolRequestUnknown, o.state("run-big"), "the result was dropped over budget")
	known, fresh := o.begin("run-big")
	assert.False(t, fresh, "a budget never makes a run step's id fresh again")
	assert.Equal(t, toolexec.ToolRequestDelivered, known)
	require.Equal(t, []string{"run-big.ran"}, outboxFiles(t, dir), "only the tombstone is on disk")

	again, report := newStoredOutbox(t, dir) // the restart
	assert.Equal(t, 1, report.Ran)
	_, fresh = again.begin("run-big")
	assert.False(t, fresh, "nor does a restart")
}

// A tombstone lasts runStepRanTTL, on disk and in memory, and no longer.
func TestRunStepTombstoneEndsWithItsWindow(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox")
	o, _ := newStoredOutbox(t, dir)
	o.markRetain("run-old")
	past := time.Now().Add(-runStepRanTTL - time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(dir, "run-old.ran"), past, past))

	// One whose window ended while the daemon was down is not reloaded.
	again, report := newStoredOutbox(t, dir)
	assert.Zero(t, report.Ran)
	assert.Equal(t, 1, report.Expired)
	assert.Empty(t, outboxFiles(t, dir))
	_, fresh := again.begin("run-old")
	assert.True(t, fresh)

	// And one whose window ends while it is up is swept.
	o.mu.Lock()
	o.ran["run-old"] = past
	o.ranSweptAt = time.Time{}
	o.mu.Unlock()
	_, fresh = o.begin("run-old")
	assert.True(t, fresh, "past its window, the activity can no longer be retried")
}
