// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// A chat whose provider ran out goes on as a whole, with one action, once there
// is capacity again (capacity_wake.go). These drive the real DynamicWorkflow
// wiring: a root and two sub-agents blocked on their provider, then ONE
// capacity wake — what adding an account, switching the chat's provider, or
// pressing Resume sends — and every thread continues with no message to any of
// them.

// creditExhausted is a provider refusing a request for spent credit, as the
// activity wrapper hands it to Temporal: non-retryable, so the step's ladder
// ends at once.
func creditExhausted() error {
	return temporal.NewNonRetryableApplicationError(
		`POST "https://api.openai.com/v1/responses": 429 Too Many Requests: You exceeded your current quota, please check your plan and billing details (insufficient_quota)`,
		"ProviderCreditExhaustion", nil)
}

type CapacityWakeSuite struct {
	suite.Suite
	temporaltest.WorkflowTestSuite
}

func TestCapacityWake(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(CapacityWakeSuite))
}

// capacity is the provider's state, flipped by the test when "an account is
// added": until then every call is refused with refusal.
type capacity struct {
	mu       sync.Mutex
	restored bool
}

func (c *capacity) restore() {
	c.mu.Lock()
	c.restored = true
	c.mu.Unlock()
}

func (c *capacity) ok() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.restored
}

// The root and two sub-agents are all parked on the same provider's usage
// limit, hours from its reset. One capacity wake — no message to any thread —
// retries all three at once, and the run completes.
func (s *CapacityWakeSuite) TestLiveWaits_OneWakeContinuesEveryThread() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	const chatID = "chat-capacity-wake-live"
	input := spawnE2EWorkflowInput(chatID)
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	start := env.Now()
	reset := start.Add(4 * time.Hour)
	var c capacity
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return []map[string]interface{}{
				spawnToolCall("tc-a", "implement the feature"),
				spawnToolCall("tc-b", "write the tests"),
			}, nil
		}
		if !c.ok() {
			return nil, usageLimitError(reset)
		}
		return nil, nil
	}
	e.childTurn = func(_ string, _ int) (map[string]interface{}, error) {
		if !c.ok() {
			return nil, usageLimitError(reset)
		}
		return map[string]interface{}{"response_text": "done", "tool_calls": nil}, nil
	}
	env.RegisterDelayedCallback(func() {
		c.restore()
		env.SignalWorkflow(CapacityWakeSignalName, nil)
	}, 10*time.Minute)

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())

	// Every thread waited, then retried at the wake — not at the reset.
	waited := map[string]bool{}
	for _, m := range e.markers() {
		if !m.Clear {
			waited[m.Thread] = true
		}
	}
	s.Len(waited, 3, "the root and both sub-agents parked on the limit")
	for _, who := range []string{"tc-a", "tc-b"} {
		retried, ok := e.callAt(who, 2)
		s.Require().True(ok, "%s retried", who)
		s.Less(retried.Sub(start), time.Hour, "%s retried at the wake, hours before the reset", who)
		s.GreaterOrEqual(retried.Sub(start), 10*time.Minute, "%s did not retry before the wake", who)
		report := e.reportFor(who)
		s.Require().NotNil(report, "%s reported", who)
		s.Equal(2, reportKind(report), "%s completed", who)
	}
	retried, ok := e.callAt("parent", 3)
	s.Require().True(ok, "the root retried")
	s.Less(retried.Sub(start), time.Hour, "the root retried at the wake")
	s.Empty(e.pausedStatuses(), "nothing paused")
}

// The stopped variant: both sub-agents stop on spent credit (a failure no wait
// fixes, so each ends and reports), and the root's own turn fails the same way
// and self-pauses the run. One capacity wake — no resume, no message — releases
// the root's pause and resumes both agents on their own threads; each reports
// a completion under a fresh slot and the run completes.
func (s *CapacityWakeSuite) TestStoppedThreads_OneWakeResumesTheWholeChat() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	const chatID = "chat-capacity-wake-stopped"
	input := spawnE2EWorkflowInput(chatID)
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	var c capacity
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return []map[string]interface{}{
				spawnToolCall("tc-a", "implement the feature"),
				spawnToolCall("tc-b", "write the tests"),
			}, nil
		}
		if !c.ok() {
			return nil, creditExhausted()
		}
		return nil, nil
	}
	e.childTurn = func(_ string, _ int) (map[string]interface{}, error) {
		if !c.ok() {
			return nil, creditExhausted()
		}
		return map[string]interface{}{"response_text": "done after the top-up", "tool_calls": nil}, nil
	}
	// The root's failing turn lands after both agents have stopped.
	e.hold = func(toolCallID string, turn int) string {
		if toolCallID == "" && turn == 2 {
			return "both-stopped"
		}
		return ""
	}
	go awaitReports(e, "both-stopped", "tc-a", "tc-b")
	env.RegisterDelayedCallback(func() {
		c.restore()
		env.SignalWorkflow(CapacityWakeSignalName, nil)
	}, 10*time.Minute)

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted(), "one wake must bring the whole chat back")
	s.Require().NoError(env.GetWorkflowError())

	paused := e.pausedStatuses()
	s.Require().Len(paused, 1, "the root's own failure paused the run")
	s.GreaterOrEqual(e.turns(e.parentThread), 3, "the root retried its step after the wake")
	for _, who := range []string{"tc-a", "tc-b"} {
		thread := e.threadOf(who)
		s.Require().NotEmpty(thread)
		s.Equal(2, e.turns(thread), "%s stopped once and was resumed once", who)
		resumed := e.reportFor(who + "#resume-1")
		s.Require().NotNil(resumed, "%s reported its resumed run under a fresh slot", who)
		s.Equal(2, reportKind(resumed), "%s completed", who)
	}
}

// What a wake must leave alone: a sub-agent that stopped for a reason capacity
// cannot fix stays stopped (its parent, or a message, decides), and a pause the
// user asked for stays paused.
func (s *CapacityWakeSuite) TestWakeLeavesOtherStopsAndUserPausesAlone() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	const chatID = "chat-capacity-wake-selective"
	input := spawnE2EWorkflowInput(chatID)
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	var c capacity
	var mu sync.Mutex
	userResumed := false
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return []map[string]interface{}{
				spawnToolCall("tc-credit", "implement the feature"),
				spawnToolCall("tc-broken", "write the tests"),
			}, nil
		}
		if !c.ok() {
			return nil, creditExhausted()
		}
		return nil, nil
	}
	e.childTurn = func(toolCallID string, _ int) (map[string]interface{}, error) {
		if toolCallID == "tc-broken" {
			return nil, providerBadRequest()
		}
		if !c.ok() {
			return nil, creditExhausted()
		}
		return map[string]interface{}{"response_text": "done", "tool_calls": nil}, nil
	}
	e.hold = func(toolCallID string, turn int) string {
		if toolCallID == "" && turn == 2 {
			return "both-stopped"
		}
		return ""
	}
	go awaitReports(e, "both-stopped", "tc-credit", "tc-broken")
	// The root's own turn self-pauses the run on spent credit; then the user
	// pauses it too, which makes the pause theirs. Capacity returns and the
	// wake lands: it must not undo the user's pause. The resumed agent waits
	// at the pause gate, and only the user's resume lets the run go on.
	env.RegisterDelayedCallback(func() { env.SignalWorkflow("signal.pause", nil) }, 5*time.Minute)
	env.RegisterDelayedCallback(func() {
		c.restore()
		env.SignalWorkflow(CapacityWakeSignalName, nil)
	}, 10*time.Minute)
	env.RegisterDelayedCallback(func() {
		mu.Lock()
		userResumed = true
		mu.Unlock()
		env.SignalWorkflow("signal.resume", nil)
	}, 30*time.Minute)

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())

	s.Require().NotNil(e.reportFor("tc-credit#resume-1"), "the agent stopped on spent credit was resumed")
	s.Nil(e.reportFor("tc-broken#resume-1"), "the agent stopped on a bad request was not")
	s.Equal(1, e.turns(e.threadOf("tc-broken")))

	mu.Lock()
	s.True(userResumed, "the user's resume happened")
	mu.Unlock()
	for _, who := range []string{"tc-credit", "parent"} {
		turn := 2
		if who == "parent" {
			turn = 3
		}
		at, ok := e.callAt(who, turn)
		s.Require().True(ok, "%s went on", who)
		s.GreaterOrEqual(at.Sub(startOf(e)), 30*time.Minute,
			"%s waited for the user's resume: the wake did not undo the user's pause", who)
	}
}

// The user's Resume (a wake with AllStopped) resumes every stopped sub-agent,
// whatever it stopped on: one that failed on a bad request goes on with the
// rest. The agent's own retry is what it gets — here the provider accepts it.
func (s *CapacityWakeSuite) TestResumeWake_ResumesEveryStoppedAgent() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	const chatID = "chat-capacity-wake-resume-all"
	input := spawnE2EWorkflowInput(chatID)
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	var c capacity
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return []map[string]interface{}{
				spawnToolCall("tc-credit", "implement the feature"),
				spawnToolCall("tc-broken", "write the tests"),
			}, nil
		}
		if !c.ok() {
			return nil, creditExhausted()
		}
		return nil, nil
	}
	e.childTurn = func(toolCallID string, _ int) (map[string]interface{}, error) {
		if !c.ok() {
			if toolCallID == "tc-broken" {
				return nil, providerBadRequest()
			}
			return nil, creditExhausted()
		}
		return map[string]interface{}{"response_text": "done", "tool_calls": nil}, nil
	}
	e.hold = func(toolCallID string, turn int) string {
		if toolCallID == "" && turn == 2 {
			return "both-stopped"
		}
		return ""
	}
	go awaitReports(e, "both-stopped", "tc-credit", "tc-broken")
	env.RegisterDelayedCallback(func() {
		c.restore()
		env.SignalWorkflow(CapacityWakeSignalName, CapacityWakeSignal{AllStopped: true})
	}, 10*time.Minute)

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	for _, who := range []string{"tc-credit", "tc-broken"} {
		resumed := e.reportFor(who + "#resume-1")
		s.Require().NotNil(resumed, "%s was resumed by the chat's Resume", who)
		s.Equal(2, reportKind(resumed), "%s completed", who)
	}
}

// startOf is the mock-clock time of the run's first call.
func startOf(e *subAgentFailureEnv) time.Time {
	at, _ := e.callAt("parent", 1)
	return at
}

// awaitReports reaches milestone once every one of toolCallIDs has reported.
func awaitReports(e *subAgentFailureEnv, milestone string, toolCallIDs ...string) {
	for {
		done := true
		for _, id := range toolCallIDs {
			if e.reportFor(id) == nil {
				done = false
			}
		}
		if done {
			e.reach(milestone)
			return
		}
		select {
		case <-e.testDone:
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestProviderCapacityError(t *testing.T) {
	t.Parallel()
	stopped := &subAgentFailedError{StepID: "call_llm", Reason: "AI provider quota or credits are exhausted", Err: creditExhausted()}
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"spent credit":                         {creditExhausted(), true},
		"a stopped agent's summarized credit":  {stopped, true},
		"usage limit":                          {usageLimitError(time.Now().Add(time.Hour)), true},
		"a rejected credential":                {errors.New(`POST "https://api.anthropic.com/v1/messages": 401 Unauthorized authentication_error`), true},
		"no provider can serve the model":      {errors.New("failed to resolve model: no connected provider serves gpt-5"), true},
		"a rate limit that outlived the retry": {errors.New("429 Too Many Requests: rate_limit_error"), true},
		"a bad request":                        {providerBadRequest(), false},
		"a tool failure":                       {fmt.Errorf("execute tools: %w", errors.New("exit status 1")), false},
		"nil":                                  {nil, false},
	} {
		assert.Equal(t, tc.want, providerCapacityError(tc.err), name)
	}
}

func TestSameProvider(t *testing.T) {
	t.Parallel()
	assert.True(t, sameProvider("gpt-5.6-sol@codex", "codex"))
	assert.True(t, sameProvider("claude-opus-5@Anthropic", "anthropic"))
	assert.False(t, sameProvider("gpt-5.6-sol@codex", "anthropic"))
	assert.False(t, sameProvider("gpt-5.6-sol@codex", ""), "an unknown waiting provider is not the same")
	assert.False(t, sameProvider("gpt-5.6-sol", "codex"), "a model with no provider is not the same")
}
