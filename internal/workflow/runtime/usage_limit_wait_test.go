// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/chatmarkers"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// A provider usage limit (5-hour / weekly window) clears in hours. Retrying
// seconds apart only burns the ladder, and pausing the run makes the user come
// back and type something. The run instead parks on a DURABLE WORKFLOW TIMER
// until the reset, with a marker written first so a supervisor can tell waiting
// from wedged, then retries the step by itself. These drive the real
// DynamicWorkflow wiring with synthetic errors built the way a driver builds
// them: chatmarkers.Wrap(KindProviderUsageLimit, "<rfc3339>", ...).

type usageWaitCall struct {
	Thread   string
	Clear    bool
	Provider string
	Probing  bool
	ResumeAt time.Time
	At       time.Time
}

type callTime struct {
	Who  string
	Turn int
	At   time.Time
}

// usageLimitError is what a driver returns for a spent window, as the activity
// wrapper hands it to Temporal: non-retryable, marker intact.
func usageLimitError(resetAt time.Time) error {
	payload := ""
	if !resetAt.IsZero() {
		payload = resetAt.UTC().Format(time.RFC3339)
	}
	msg := chatmarkers.Wrap(chatmarkers.KindProviderUsageLimit, payload,
		chatmarkers.ProviderUsageLimitLead+" (Anthropic, 5-hour window) — resets soon")
	return temporal.NewNonRetryableApplicationError(msg, "ProviderUsageLimit", nil)
}

func (e *subAgentFailureEnv) markers() []usageWaitCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]usageWaitCall(nil), e.usageWaits...)
}

func (e *subAgentFailureEnv) callAt(who string, turn int) (time.Time, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, c := range e.callTimes {
		if c.Who == who && c.Turn == turn {
			return c.At, true
		}
	}
	return time.Time{}, false
}

func (e *subAgentFailureEnv) eventLog() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.events...)
}

type UsageLimitWaitSuite struct {
	suite.Suite
	temporaltest.WorkflowTestSuite
}

func TestUsageLimitWait(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(UsageLimitWaitSuite))
}

// The reset is known: the step is held on a timer until reset + margin (and
// not before), the marker is written BEFORE the park and cleared AFTER, and the
// retried step succeeds. Nothing pauses and no error card is written.
func (s *UsageLimitWaitSuite) TestKnownReset_HoldsUntilResetThenRetries() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	const chatID = "chat-limit-known-reset"
	input := spawnE2EWorkflowInput(chatID)
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	start := env.Now()
	resetAt := start.Add(75 * time.Minute)
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return nil, usageLimitError(resetAt)
		}
		return nil, nil
	}

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Equal(2, e.turns(e.parentThread), "the step is retried once, after the wait")

	retriedAt, ok := e.callAt("parent", 2)
	s.Require().True(ok)
	s.False(retriedAt.Before(resetAt.Add(usageLimitMargin)),
		"the retry must not land before the reset plus its margin (got %s, reset %s)", retriedAt, resetAt)
	s.False(retriedAt.After(resetAt.Add(usageLimitMargin+usageLimitJitterSpan+time.Minute)),
		"and not long after it either (got %s)", retriedAt)

	s.Empty(e.pausedStatuses(), "a usage limit is waited out, not paused")
	e.mu.Lock()
	s.Empty(e.workflowErrors, "no error card: the run is waiting, not failing")
	e.mu.Unlock()

	// Marker: set before the park, cleared after it, around the retried call.
	marks := e.markers()
	s.Require().Len(marks, 2)
	s.False(marks[0].Clear)
	s.Equal("Anthropic", marks[0].Provider)
	s.False(marks[0].Probing, "a known reset is a promise, not a probe")
	s.True(marks[0].ResumeAt.After(resetAt), "resume_at is the reset plus a margin")
	s.True(marks[1].Clear)
	s.Equal(e.parentThread, marks[0].Thread)
	s.Equal([]string{
		"callllm:parent#1",
		"marker:set:" + e.parentThread,
		"marker:clear:" + e.parentThread,
		"callllm:parent#2",
	}, e.eventLog(), "the marker is written before the park and cleared before the retry")
	s.False(marks[0].At.After(resetAt), "the marker is written when the wait begins, not at the reset")
}

// Unknown reset (empty payload): a probe ladder — 5m, 15m, 30m, then hourly,
// never capped. Each failed probe climbs a rung; the run keeps waiting.
func (s *UsageLimitWaitSuite) TestUnknownReset_ProbeLadderHasNoCap() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	const chatID = "chat-limit-unknown-reset"
	input := spawnE2EWorkflowInput(chatID)
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	const failures = 6
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn <= failures {
			return nil, usageLimitError(time.Time{})
		}
		return nil, nil
	}

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Equal(failures+1, e.turns(e.parentThread), "six refusals, then the step that finally succeeds")

	want := []time.Duration{5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour, time.Hour, time.Hour}
	for i, rung := range want {
		prev, ok := e.callAt("parent", i+1)
		s.Require().True(ok)
		next, ok := e.callAt("parent", i+2)
		s.Require().True(ok)
		gap := next.Sub(prev)
		s.GreaterOrEqual(gap, rung, "rung %d waits at least %s (waited %s)", i+1, rung, gap)
		s.Less(gap, rung+usageLimitJitterSpan+time.Minute, "rung %d is not much longer than %s (waited %s)", i+1, rung, gap)
	}
	for _, m := range e.markers() {
		if !m.Clear {
			s.True(m.Probing, "an unknown reset is marked as a probe, not a promise")
		}
	}
	s.Empty(e.pausedStatuses())
}

// "Retry now" is a signal that wakes the timer at once, long before the reset.
func (s *UsageLimitWaitSuite) TestRetryNow_WakesTheTimerImmediately() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	const chatID = "chat-limit-retry-now"
	input := spawnE2EWorkflowInput(chatID)
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	start := env.Now()
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return nil, usageLimitError(start.Add(4 * time.Hour))
		}
		return nil, nil
	}
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(CapacityWakeSignalName, nil)
	}, 10*time.Minute)

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	retriedAt, ok := e.callAt("parent", 2)
	s.Require().True(ok)
	s.Less(retriedAt.Sub(start), 30*time.Minute, "retried when the signal landed, hours before the reset")
	s.GreaterOrEqual(retriedAt.Sub(start), 10*time.Minute, "and not before the signal")
	s.Len(e.markers(), 2, "the marker is still cleared on an early wake")
	s.True(e.markers()[1].Clear)
}

// A signal sent while nothing is waiting is a click on a stale button. It must
// not wake a LATER wait that has no connection to it.
func (s *UsageLimitWaitSuite) TestRetryNow_SignalWhileNotWaitingIsDropped() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	const chatID = "chat-limit-stale-signal"
	input := spawnE2EWorkflowInput(chatID)
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	start := env.Now()
	resetAt := start.Add(2 * time.Hour)
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return nil, usageLimitError(resetAt)
		}
		return nil, nil
	}
	// Delivered at t=0, before the first wait exists: it sits in the channel.
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(CapacityWakeSignalName, nil)
	}, 0)

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	retriedAt, ok := e.callAt("parent", 2)
	s.Require().True(ok)
	s.False(retriedAt.Before(resetAt), "a click before the wait existed must not cut it short")
}

// Stop while waiting ends the run cleanly: the wait is cancelled, the marker is
// released, the step is never retried, and the run is not left parked.
func (s *UsageLimitWaitSuite) TestStopWhileWaiting_CancelsCleanly() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	const chatID = "chat-limit-stop"
	input := spawnE2EWorkflowInput(chatID)
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		return nil, usageLimitError(env.Now().Add(6 * time.Hour))
	}
	env.RegisterDelayedCallback(func() { env.CancelWorkflow() }, 20*time.Minute)

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	err := env.GetWorkflowError()
	s.Require().Error(err)
	s.True(temporal.IsCanceledError(err) || errors.Is(err, errCancelledOK(err)),
		"stopping a waiting run ends it as a cancellation, got %v", err)
	s.Equal(1, e.turns(e.parentThread), "the step is not retried once the run is stopped")
	marks := e.markers()
	s.Require().NotEmpty(marks)
	s.True(marks[len(marks)-1].Clear, "the marker is released when the run is stopped mid-wait")
}

func errCancelledOK(err error) error { return err }

// A message sent while the run waits is never lost and never ignored: it lands
// in the thread's mailbox (SendMessage rings the thread_wake doorbell), the
// doorbell ends the wait, and the retried step is the turn that reads it. This is
// also how switching model or provider gets a run out of a limit without waiting
// for the reset.
func (s *UsageLimitWaitSuite) TestMessageWhileWaiting_WakesTheRunAndIsReadByTheRetry() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	const chatID = "chat-limit-message"
	input := spawnE2EWorkflowInput(chatID)
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	start := env.Now()
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return nil, usageLimitError(start.Add(5 * time.Hour))
		}
		return nil, nil
	}
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(ThreadWakeSignalName, ThreadWakeSignal{Thread: e.parentThread, Reason: "mailbox"})
	}, 15*time.Minute)

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	retriedAt, ok := e.callAt("parent", 2)
	s.Require().True(ok)
	s.GreaterOrEqual(retriedAt.Sub(start), 15*time.Minute, "not before the message")
	s.Less(retriedAt.Sub(start), time.Hour, "the message woke the wait; the run did not sit until the reset")
	s.Equal(2, e.turns(e.parentThread), "exactly one retry: the message is read by it, not owed a further turn")
	s.Empty(e.pausedStatuses())
}

// A user pause that lands mid-wait ends the wait; the retry is then held until a
// resume, exactly as any step boundary would hold it.
func (s *UsageLimitWaitSuite) TestPauseWhileWaiting_HoldsRetryUntilResume() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	const chatID = "chat-limit-pause"
	input := spawnE2EWorkflowInput(chatID)
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	start := env.Now()
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return nil, usageLimitError(start.Add(5 * time.Hour))
		}
		return nil, nil
	}
	env.RegisterDelayedCallback(func() { env.SignalWorkflow("signal.pause", nil) }, 10*time.Minute)
	env.RegisterDelayedCallback(func() { env.SignalWorkflow("signal.resume", nil) }, 40*time.Minute)

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	retriedAt, ok := e.callAt("parent", 2)
	s.Require().True(ok)
	s.GreaterOrEqual(retriedAt.Sub(start), 40*time.Minute, "the retry waited for the resume")
	s.Less(retriedAt.Sub(start), time.Hour, "and ran right after it, not at the reset")
}

// A sub-agent waits IN PLACE: the parent sees it as running (not failed, not
// reported as failed), keeps its own work going, and receives the child's
// completion after the child's wait is over.
func (s *UsageLimitWaitSuite) TestSubAgentWaitsInPlace_ParentContinues() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	const chatID = "chat-limit-subagent"
	input := spawnE2EWorkflowInput(chatID)
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	start := env.Now()
	resetAt := start.Add(90 * time.Minute)
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return []map[string]interface{}{spawnToolCall("tc-limited", "implement the feature")}, nil
		}
		return nil, nil
	}
	e.childTurn = func(toolCallID string, turn int) (map[string]interface{}, error) {
		if turn == 1 {
			return nil, usageLimitError(resetAt)
		}
		return map[string]interface{}{"response_text": "implemented", "tool_calls": nil}, nil
	}

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Empty(e.pausedStatuses(), "a sub-agent's usage limit pauses nothing")

	childThread := e.threadOf("tc-limited")
	s.Require().NotEmpty(childThread)
	s.Equal(2, e.turns(childThread), "the child retried its own step after waiting")
	retriedAt, ok := e.callAt("tc-limited", 2)
	s.Require().True(ok)
	s.False(retriedAt.Before(resetAt), "the child held until the reset")

	// The marker belongs to the CHILD's thread, so the chat itself is not shown
	// as waiting while the parent is free to work.
	var sawChildMarker bool
	for _, m := range e.markers() {
		s.Equal(childThread, m.Thread, "only the child waited")
		sawChildMarker = sawChildMarker || !m.Clear
	}
	s.True(sawChildMarker)

	report := e.reportFor("tc-limited")
	s.Require().NotNil(report, "the parent hears from the child once it is done")
	s.Equal(2, reportKind(report), "the report is a completion — the child never failed")
	s.NotEqual(4, reportKind(report))
	s.Equal([]string{"backgrounded", "completed"}, e.toolStatusesFor("tc-limited"),
		"the parent's tool call never goes through 'failed'")
}

// A BACKGROUND spawn (every spawn is dispatched detached) that hits the limit is
// reported as WAITING while it waits — never as failed: sampled mid-wait, the
// child's marker is set with its reset time and provider (what spawn_status
// renders), its workflow row and the parent's tool call are still running, and
// no failure report or error card exists. After the reset the child completes
// and the parent gets its normal result.
func (s *UsageLimitWaitSuite) TestBackgroundSpawn_ReportsWaitingNeverFailed() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	const chatID = "chat-limit-background"
	input := spawnE2EWorkflowInput(chatID)
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	start := env.Now()
	resetAt := start.Add(3 * time.Hour)
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return []map[string]interface{}{spawnToolCall("tc-bg", "long job")}, nil
		}
		return nil, nil
	}
	e.childTurn = func(_ string, turn int) (map[string]interface{}, error) {
		if turn == 1 {
			return nil, usageLimitError(resetAt)
		}
		return map[string]interface{}{"response_text": "done", "tool_calls": nil}, nil
	}

	var midWait struct {
		markers  []usageWaitCall
		reports  int
		errors   int
		statuses []string
		tool     []string
		checked  bool
	}
	env.RegisterDelayedCallback(func() {
		midWait.checked = true
		midWait.markers = e.markers()
		e.mu.Lock()
		midWait.reports = len(e.reports)
		midWait.errors = len(e.workflowErrors)
		for _, st := range e.statuses {
			if st["spawned_by_tool_call_id"] == "tc-bg" {
				if v, ok := st["status"].(string); ok {
					midWait.statuses = append(midWait.statuses, v)
				}
			}
		}
		e.mu.Unlock()
		midWait.tool = e.toolStatusesFor("tc-bg")
	}, time.Hour)

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Require().True(midWait.checked)

	child := e.threadOf("tc-bg")
	s.Require().Len(midWait.markers, 1, "mid-wait the child is parked on a marker")
	s.Equal(child, midWait.markers[0].Thread)
	s.False(midWait.markers[0].Clear)
	s.Equal("Anthropic", midWait.markers[0].Provider)
	s.True(midWait.markers[0].ResumeAt.After(resetAt), "with its reset time (plus margin), not a failure")
	s.Zero(midWait.reports, "no report reaches the parent while the child waits")
	s.Zero(midWait.errors, "no error card while the child waits")
	s.NotContains(midWait.statuses, "failed")
	s.NotContains(midWait.statuses, "paused")
	s.Equal([]string{"backgrounded"}, midWait.tool, "the parent's spawn call is still running, not failed")

	s.Equal(2, reportKind(e.reportFor("tc-bg")), "the parent's normal result: a completion")
	s.Equal([]string{"backgrounded", "completed"}, e.toolStatusesFor("tc-bg"))
}

// A usage limit is only waited out when the error says so. Any other terminal
// failure keeps the old shape: the root pauses and waits for the user.
func (s *UsageLimitWaitSuite) TestOtherFailures_StillPause() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	const chatID = "chat-limit-not-a-limit"
	input := spawnE2EWorkflowInput(chatID)
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	var mu sync.Mutex
	resumed := false
	e.parentTurn = func(int) ([]map[string]interface{}, error) {
		mu.Lock()
		defer mu.Unlock()
		if !resumed {
			return nil, providerBadRequest()
		}
		return nil, nil
	}
	env.RegisterDelayedCallback(func() {
		mu.Lock()
		resumed = true
		mu.Unlock()
		env.SignalWorkflow("signal.resume", nil)
	}, 10*time.Minute)

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Len(e.pausedStatuses(), 1)
	s.Empty(e.markers(), "no wait marker for a failure that is not a usage limit")
}

// --- the decision function ---

func TestDecideOnUsageLimit(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

	t.Run("known reset waits until reset plus margin and jitter", func(t *testing.T) {
		d := decideOnUsageLimit(usageLimit{Provider: "Anthropic", ResetAt: now.Add(time.Hour)}, types.UsageLimitFacts{}, usageLimitState{}, now, 7*time.Second)
		require.Equal(t, usageLimitWaitUntil, d.Action)
		require.False(t, d.Probing)
		require.Equal(t, now.Add(time.Hour+usageLimitMargin+7*time.Second), d.Until)
	})

	t.Run("unknown reset climbs the ladder and then holds at an hour", func(t *testing.T) {
		want := []time.Duration{5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour, time.Hour, time.Hour}
		for probes, rung := range want {
			d := decideOnUsageLimit(usageLimit{}, types.UsageLimitFacts{}, usageLimitState{Probes: probes}, now, 0)
			require.True(t, d.Probing)
			require.Equal(t, now.Add(rung), d.Until, "probe %d", probes)
		}
	})

	t.Run("a reset already in the past still probes", func(t *testing.T) {
		d := decideOnUsageLimit(usageLimit{ResetAt: now.Add(-time.Minute)}, types.UsageLimitFacts{}, usageLimitState{}, now, 0)
		require.True(t, d.Probing, "the provider still refuses: our clock and its disagree, so probe rather than spin")
		require.Equal(t, now.Add(5*time.Minute), d.Until)
	})
}

func TestParseUsageLimit(t *testing.T) {
	t.Parallel()
	reset := time.Date(2026, 10, 18, 15, 42, 0, 0, time.UTC)

	got, ok := parseUsageLimit(fmt.Errorf("activity error: %w", usageLimitError(reset)))
	require.True(t, ok, "the marker survives every wrap")
	require.True(t, got.ResetAt.Equal(reset))
	require.Equal(t, "Anthropic", got.Provider)

	got, ok = parseUsageLimit(usageLimitError(time.Time{}))
	require.True(t, ok)
	require.True(t, got.ResetAt.IsZero(), "an empty payload is an unknown reset")

	_, ok = parseUsageLimit(errors.New("429 too many requests"))
	require.False(t, ok, "a transient 429 is not a usage window")
	_, ok = parseUsageLimit(nil)
	require.False(t, ok)
}

const parallelFanoutYAML = `
name: par
entry: [fanout]
inputs:
  items:
    type: array
nodes:
  - id: fanout
    type: loop
    parallel: true
    items: "{{inputs.items}}"
    key: "{{iter.key}}"
    ref: builtin://leaf
`

// leafYAML's call_llm is a DIRECT step of each parallel iteration (builtin://agent
// wraps its call in a nested loop, which is a different retry-exhaustion site).
const leafYAML = `
name: leaf
entry: [call_llm]
nodes:
  - id: call_llm
    type: call_llm
`

// A child of a PARALLEL LOOP that hits the limit waits in place too: its sibling
// iteration is untouched, the loop does not fail or pause, and after the reset
// the waiting iteration retries and the loop completes with both iterations.
func (s *UsageLimitWaitSuite) TestParallelLoopChild_WaitsInPlaceAndTheLoopCompletes() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	const chatID = "chat-limit-parallel"
	input := spawnE2EWorkflowInput(chatID)
	input.WorkflowName = "par"
	input.ExecContext.WorkflowName = "par"
	input.Inputs = map[string]interface{}{"items": []interface{}{"alpha", "beta"}}
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)
	e.workflowYAMLs = map[string]string{"par": parallelFanoutYAML, "builtin://leaf": leafYAML}

	start := env.Now()
	resetAt := start.Add(2 * time.Hour)
	var limited sync.Once
	var limitedThread string
	var mu sync.Mutex
	e.childTurn = func(_ string, turn int) (map[string]interface{}, error) {
		return map[string]interface{}{"response_text": "done", "tool_calls": nil}, nil
	}
	// Exactly one iteration is refused, once.
	e.parentTurn = func(int) ([]map[string]interface{}, error) { return nil, nil }
	e.failFirstCallOf = func(thread string) error {
		var err error
		limited.Do(func() {
			mu.Lock()
			limitedThread = thread
			mu.Unlock()
			err = usageLimitError(resetAt)
		})
		return err
	}

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError(), "the loop must not fail because one iteration hit a usage limit")
	s.Empty(e.pausedStatuses())
	e.mu.Lock()
	s.Empty(e.workflowErrors, "no error card for a waiting iteration")
	e.mu.Unlock()

	mu.Lock()
	defer mu.Unlock()
	s.Require().NotEmpty(limitedThread)
	marks := e.markers()
	s.Require().Len(marks, 2, "the limited iteration set and cleared its marker")
	s.Equal(limitedThread, marks[0].Thread)
	s.True(marks[1].Clear)
	s.GreaterOrEqual(marks[1].At.Sub(start), 2*time.Hour, "it held until the reset")
	s.Equal(2, e.turns(limitedThread), "it retried its own step after the wait")
}
