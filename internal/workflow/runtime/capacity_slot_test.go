// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// A run waiting on a provider is not running, so it must not hold a slot of
// its workflow's concurrency.max_running (control-plane#2509: six
// rate-limited reviews held every slot of pr-review for hours). These drive
// the real DynamicWorkflow wiring with a stubbed TriggerCapacitySlot activity.

type CapacitySlotSuite struct {
	suite.Suite
	temporaltest.WorkflowTestSuite
}

func TestCapacitySlot(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(CapacitySlotSuite))
}

// slotStub answers TriggerCapacitySlot: park reports held, resume reports
// granted from resumeGrants in order (true once they run out).
type slotStub struct {
	e            *subAgentFailureEnv
	held         bool
	resumeGrants []bool
	calls        []map[string]interface{}
	callTimes    []time.Time
}

func registerSlotStub(e *subAgentFailureEnv, held bool, resumeGrants ...bool) *slotStub {
	s := &slotStub{e: e, held: held, resumeGrants: resumeGrants}
	e.env.RegisterActivityWithOptions(func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		s.calls = append(s.calls, input)
		s.callTimes = append(s.callTimes, e.env.Now())
		action, _ := input["action"].(string)
		e.events = append(e.events, "slot:"+action)
		if action == "park" {
			return map[string]interface{}{"held": s.held}, nil
		}
		granted := true
		if len(s.resumeGrants) > 0 {
			granted, s.resumeGrants = s.resumeGrants[0], s.resumeGrants[1:]
		}
		return map[string]interface{}{"held": s.held, "granted": granted}, nil
	}, activity.RegisterOptions{Name: "TriggerCapacitySlot"})
	return s
}

func triggeredInput(chatID string) WorkflowInput {
	input := spawnE2EWorkflowInput(chatID)
	input.Trigger = &TriggerInfo{Kind: "schedule", TriggerID: "trg-1", EventID: "evt-" + chatID}
	return input
}

// The run parks its slot when it starts waiting out a usage limit, and takes
// it back before the retried step dispatches — never before the wait is over.
func (s *CapacitySlotSuite) TestUsageLimitWait_ParksSlotAndResumesBeforeRetry() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	const chatID = "chat-slot-park"
	input := triggeredInput(chatID)
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)
	stub := registerSlotStub(e, true)

	resetAt := env.Now().Add(75 * time.Minute)
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return nil, usageLimitError(resetAt)
		}
		return nil, nil
	}

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Equal([]string{
		"callllm:parent#1",
		"marker:set:" + e.parentThread,
		"slot:park",
		"marker:clear:" + e.parentThread,
		"slot:resume",
		"callllm:parent#2",
	}, e.eventLog(), "the slot is given back for the wait and taken back before the retry")

	s.Require().Len(stub.calls, 2)
	s.Equal("evt-"+chatID, stub.calls[0]["event_id"], "the slot is named by the run's trigger event")
	s.Equal(stub.calls[0]["workflow_id"], stub.calls[1]["workflow_id"])
	s.NotEmpty(stub.calls[0]["workflow_id"])
	s.NotEmpty(stub.calls[0]["first_run_id"], "and by its admitted chain")
	s.False(stub.callTimes[0].After(resetAt), "parked when the wait begins, not at the reset")
}

// While the scope is full the parked run asks again on a backoff, and runs
// nothing until it is granted: max_running stays an upper bound.
func (s *CapacitySlotSuite) TestScopeFull_RetryWaitsForTheSlot() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	const chatID = "chat-slot-full"
	input := triggeredInput(chatID)
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)
	stub := registerSlotStub(e, true, false, false)

	resetAt := env.Now().Add(10 * time.Minute)
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return nil, usageLimitError(resetAt)
		}
		return nil, nil
	}

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Require().Len(stub.calls, 4, "one park, two refused resumes, one granted")
	for i, action := range []string{"park", "resume", "resume", "resume"} {
		s.Equal(action, stub.calls[i]["action"])
	}
	s.Equal(capacityResumeBackoff[0], stub.callTimes[2].Sub(stub.callTimes[1]), "asks back off")
	s.Equal(capacityResumeBackoff[1], stub.callTimes[3].Sub(stub.callTimes[2]))
	retriedAt, ok := e.callAt("parent", 2)
	s.Require().True(ok)
	s.False(retriedAt.Before(stub.callTimes[3]), "the retry waits for the granted resume")
}

// A run that holds no slot (not capacity-admitted, or already released) is
// told so once and stops asking: no resume, no second park.
func (s *CapacitySlotSuite) TestNoSlotHeld_StopsAsking() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	const chatID = "chat-slot-none"
	input := triggeredInput(chatID)
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)
	stub := registerSlotStub(e, false)

	start := env.Now()
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn <= 2 {
			return nil, usageLimitError(start.Add(time.Duration(turn) * time.Hour))
		}
		return nil, nil
	}

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Require().Len(stub.calls, 1, "one park attempt; the run holds no slot, so nothing more")
	s.Equal("park", stub.calls[0]["action"])
}

// A run nobody triggered holds no slot and never asks.
func (s *CapacitySlotSuite) TestChatStartedRun_NeverAsks() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	const chatID = "chat-slot-chat-start"
	input := spawnE2EWorkflowInput(chatID)
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)
	stub := registerSlotStub(e, true)

	resetAt := env.Now().Add(30 * time.Minute)
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return nil, usageLimitError(resetAt)
		}
		return nil, nil
	}

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Empty(stub.calls)
}

// A provider wait does not park while a step executes elsewhere in the run:
// that run is still running. A step whose future is ready is not executing,
// even if no executor ever handled it.
func TestCapacitySlotExecutingCountsOnlyUnfinishedSteps(t *testing.T) {
	var ts temporaltest.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		slot := newCapacitySlot(ctx, &TriggerInfo{EventID: "evt"}, false)
		if slot.executing() {
			return fmt.Errorf("an idle run is not executing")
		}
		future, settable := workflow.NewFuture(ctx)
		running := &RunningStep{StepID: "call_llm", Future: future}
		slot.trackStep(running)
		if !slot.executing() {
			return fmt.Errorf("a dispatched step is executing")
		}
		settable.Set(nil, nil)
		if slot.executing() {
			return fmt.Errorf("a finished but unhandled step is not executing")
		}
		slot.trackStep(running)
		slot.untrackStep(running)
		if slot.executing() {
			return fmt.Errorf("a handled step is not executing")
		}
		if newCapacitySlot(ctx, nil, false) != nil || newCapacitySlot(ctx, &TriggerInfo{}, false) != nil {
			return fmt.Errorf("a run with no trigger event holds no slot")
		}
		if !newCapacitySlot(ctx, &TriggerInfo{EventID: "evt"}, true).isParked() {
			return fmt.Errorf("a continue-as-new successor keeps a parked slot parked")
		}
		return nil
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
}
