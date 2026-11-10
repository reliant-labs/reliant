// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec"
	activitytypes "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
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
	got := awaitToolResponse(s) // the call's own result: it ran
	assert.Equal(t, "new-1", got.RequestId)
	assert.Equal(t, toolexec.ToolRequestCompleted, d.results.state("new-1"), "and its result is held")
}

// awaitToolResponse blocks until the next tool response is queued on s: the
// event itself, with no deadline of its own (the test binary's bounds it).
func awaitToolResponse(s *gatewaySession) *reliantv1.ToolResponse {
	for msg := range s.sendCh {
		if r := msg.GetToolResponse(); r != nil {
			return r
		}
	}
	return nil
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
	require.Equal(t, []string{"run-1.retained"}, outboxFiles(t, dir), "the acknowledged run step's result is kept")

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
	clock := newOutboxClock()
	o, _ := newClockedOutbox(t, dir, clock)
	o.markRetain("old")
	startRunning(o, "old")
	o.hold("old", resultMsg("old", "old"), nil)
	require.NotNil(t, o.ack("old"))
	clock.advance(resultRetainTTL / 2)
	o.markRetain("new")
	startRunning(o, "new")
	o.hold("new", resultMsg("new", "new"), nil)
	require.NotNil(t, o.ack("new"))

	clock.advance(resultRetainTTL/2 + time.Minute) // past "old"'s window, inside "new"'s
	assert.Nil(t, o.retainedResult("old"))
	assert.NotNil(t, o.retainedResult("new"))
	assert.Equal(t, []string{"new.retained"}, outboxFiles(t, dir), "an expired retained result leaves no file")

	// A file whose window ended while the daemon was down is not reloaded.
	clock.advance(resultRetainTTL)
	again, report := newClockedOutbox(t, dir, clock)
	assert.Zero(t, report.Retained)
	assert.Equal(t, 1, report.Expired)
	assert.Equal(t, toolexec.ToolRequestUnknown, again.state("new"))
	assert.Empty(t, outboxFiles(t, dir))
}

// outboxClock is the outbox's time in a test: it moves only when told to.
type outboxClock struct {
	mu sync.Mutex
	t  time.Time
}

func newOutboxClock() *outboxClock {
	return &outboxClock{t: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
}

func (c *outboxClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *outboxClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newClockedOutbox is newStoredOutbox on clock (dir "" keeps it in memory).
func newClockedOutbox(t *testing.T, dir string, clock *outboxClock) (*resultOutbox, outboxLoadReport) {
	t.Helper()
	o := &resultOutbox{clock: clock.now}
	if dir == "" {
		return o, outboxLoadReport{}
	}
	store, err := openOutboxStore(dir)
	require.NoError(t, err)
	return o, o.restore(store)
}

// runStepDelivered is a run step that ran to the end here: tombstoned before
// it ran, its result held, then delivered and retained.
func runStepDelivered(t *testing.T, o *resultOutbox, id string, size int) {
	t.Helper()
	_, fresh := o.begin(id)
	require.True(t, fresh)
	o.persistRunning(id)
	require.NoError(t, o.markRunStep(id, 5*time.Minute))
	o.hold(id, (&daemonClient{}).toolResponse(id, finished(string(make([]byte, size)))), nil)
	require.NotNil(t, o.ack(id))
}

// requireNeverRunsAgain: id is not fresh, and a repeat is answered as one
// that already ran.
func requireNeverRunsAgain(t *testing.T, o *resultOutbox, id string) {
	t.Helper()
	known, fresh := o.begin(id)
	assert.False(t, fresh, "a run step's request id never runs twice")
	assert.Equal(t, toolexec.ToolRequestDelivered, known)
}

// [B1] A run step's id stays "already ran" after everything that held its
// result is gone — its retention window, the retained-result budget, a
// daemon restart — for as long as any copy of it can still be sent
// (runStepSeenFor), and only then is forgotten.
func TestRunStepRequestIDOutlivesItsResult(t *testing.T) {
	t.Run("after its result's retention window", func(t *testing.T) {
		clock := newOutboxClock()
		o, _ := newClockedOutbox(t, "", clock)
		runStepDelivered(t, o, "run-1", 10)
		clock.advance(resultRetainTTL + time.Minute)
		require.Nil(t, o.retainedResult("run-1"), "the result is gone")
		requireNeverRunsAgain(t, o, "run-1")
	})

	t.Run("after the retained-result budget evicted its result", func(t *testing.T) {
		clock := newOutboxClock()
		o, _ := newClockedOutbox(t, "", clock)
		third := resultRetainBudget / 3
		for _, id := range []string{"a", "b", "c", "d"} {
			runStepDelivered(t, o, id, third)
			clock.advance(time.Second)
		}
		require.Nil(t, o.retainedResult("a"), "the oldest result was evicted for room")
		requireNeverRunsAgain(t, o, "a")
	})

	t.Run("after a daemon restart, with only the tombstone left", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "outbox")
		clock := newOutboxClock()
		first, _ := newClockedOutbox(t, dir, clock)
		runStepDelivered(t, first, "run-1", 10)
		clock.advance(resultRetainTTL + time.Minute)
		require.Nil(t, first.retainedResult("run-1"))
		require.Equal(t, []string{"run-1.seen"}, outboxFiles(t, dir), "only the tombstone outlives the result")

		second, report := newClockedOutbox(t, dir, clock)
		assert.Equal(t, 1, report.Seen)
		requireNeverRunsAgain(t, second, "run-1")
	})

	t.Run("and is forgotten once no copy can arrive", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "outbox")
		clock := newOutboxClock()
		o, _ := newClockedOutbox(t, dir, clock)
		runStepDelivered(t, o, "run-1", 10)
		clock.advance(runStepSeenFor(5*time.Minute) - time.Minute)
		requireNeverRunsAgain(t, o, "run-1")

		// Down past its expiry: a restart does not reload it.
		clock.advance(time.Minute)
		again, report := newClockedOutbox(t, dir, clock)
		assert.Zero(t, report.Seen)
		assert.Equal(t, 1, report.Expired)
		assert.Empty(t, outboxFiles(t, dir))
		_, fresh := again.begin("run-1")
		assert.True(t, fresh)

		// And a running daemon sweeps it too.
		clock.advance(runStepSeenSweep)
		_, fresh = o.begin("run-1")
		assert.True(t, fresh)
	})

	t.Run("a model's tool call keeps no tombstone", func(t *testing.T) {
		clock := newOutboxClock()
		o, _ := newClockedOutbox(t, "", clock)
		o.begin("tool-1")
		o.hold("tool-1", resultMsg("tool-1", "x"), nil)
		require.NotNil(t, o.ack("tool-1"))
		clock.advance(resultHoldTTL + time.Minute)
		_, fresh := o.begin("tool-1")
		assert.True(t, fresh, "unchanged: only a run step's id is remembered past its result")
	})
}

// The tombstone lasts as long as a copy of the id can still be sent: the
// command's timeout plus the attempt's machine wait and send overhead, and
// never less than the floor.
func TestRunStepSeenForFollowsTheSendContract(t *testing.T) {
	assert.Equal(t, runStepSeenFloor, runStepSeenFor(5*time.Minute))
	long := 48 * time.Hour
	assert.Equal(t, long+activitytypes.RunStepMachineWaitBudget+activitytypes.RunStepSendOverhead, runStepSeenFor(long))
}

// [B1] through the daemon: a run step that ran, whose result has since been
// evicted, arrives again; the daemon answers "already ran" and runs nothing.
func TestDaemonNeverRerunsARunStepWhoseResultIsGone(t *testing.T) {
	clock := newOutboxClock()
	d := newTestDaemonClient("d-once", "u-once")
	d.results.clock = clock.now
	s := useTestSession(d, 16)

	msg := toolRequestMessage("run-1")
	msg.GetToolRequest().TimeoutMs = int32((5 * time.Minute).Milliseconds())
	_, fresh := d.results.begin("run-1")
	require.True(t, fresh)
	d.executeTool(s, msg.GetToolRequest())
	require.Equal(t, "run-1", sentToolResponse(t, s).RequestId)
	d.handleToolResultAck("run-1")
	clock.advance(resultRetainTTL + time.Minute) // its result is gone

	inFlight := s.inFlight.Load()
	require.NoError(t, d.handleServerMessage(context.Background(), s, msg))
	got := sentToolResponse(t, s)
	assert.Equal(t, toolexec.ErrorCodeDaemonRestarted, got.ErrorCode)
	assert.Contains(t, got.Content, "already ran on this machine")
	assert.Equal(t, inFlight, s.inFlight.Load(), "nothing was started: a started call holds its stream")
	assert.Equal(t, toolexec.ToolRequestDelivered, d.results.state("run-1"))
}

// [R2.B1] A run step whose tombstone cannot be made durable is not run: a
// restart would forget it ran, so the daemon fails closed with its own
// outcome-unknown answer, and a repeat of the id is still refused.
func TestRunStepIsNotRunWhenItsTombstoneCannotBePersisted(t *testing.T) {
	d := newTestDaemonClient("d-once", "u-once")
	s := useTestSession(d, 16)
	dir := filepath.Join(t.TempDir(), "outbox")
	store, err := openOutboxStore(dir)
	require.NoError(t, err)
	d.results.restore(store)
	// The outbox directory is gone and a file stands in its place: nothing
	// can be written there, whoever the test runs as.
	require.NoError(t, os.RemoveAll(dir))
	require.NoError(t, os.WriteFile(dir, nil, 0o600))

	req := toolRequestMessage("run-1").GetToolRequest()
	_, fresh := d.results.begin("run-1")
	require.True(t, fresh)
	d.executeTool(s, req)

	got := sentToolResponse(t, s)
	assert.Equal(t, "run-1", got.RequestId)
	assert.Equal(t, toolexec.ErrorCodeDaemonRestarted, got.ErrorCode, "outcome unknown to the run, never a verdict")
	assert.True(t, toolexec.IsTransportErrorCode(got.ErrorCode))
	assert.Contains(t, got.Content, "was not run", "the tool never ran: it would have answered for itself")
	noToolResponse(t, s, "one answer, and nothing ran")

	_, fresh = d.results.begin("run-1")
	assert.False(t, fresh, "this process still never runs the id")
}

// markSeen says whether the tombstone is durable; with no store there is
// nothing to make durable.
func TestMarkSeenReportsWhetherTheTombstoneIsDurable(t *testing.T) {
	expires := newOutboxClock().now().Add(runStepSeenFloor)
	var none *outboxStore
	assert.NoError(t, none.markSeen("run-1", expires))

	dir := filepath.Join(t.TempDir(), "outbox")
	store, err := openOutboxStore(dir)
	require.NoError(t, err)
	require.NoError(t, store.markSeen("run-1", expires))
	info, err := os.Stat(filepath.Join(dir, "run-1"+seenFileExt))
	require.NoError(t, err)
	assert.True(t, info.ModTime().Equal(expires), "its expiry is its modification time")

	require.NoError(t, os.RemoveAll(dir))
	require.NoError(t, os.WriteFile(dir, nil, 0o600))
	assert.Error(t, store.markSeen("run-2", expires))
}
