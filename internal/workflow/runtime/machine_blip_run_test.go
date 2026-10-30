// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/lifecycle"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// cp-merge, 2026-10-30: the managed machine behind an automation's `gate` run
// node stopped answering while its VM restarted. The step failed, the run
// paused, the unattended self-resume ladder retried it five times over 33
// minutes, and then the run PARKED — an open Temporal execution, so it held
// its automation's only capacity slot for two hours and every later firing
// queued behind it, until a person cancelled it.
//
// These drive that run end to end through the real DynamicWorkflow, with the
// run step's activity standing in for the machine. Nothing here signals the
// workflow: an automation has nobody to.

const machineBlipRunYAML = `
name: agent
entry: [gate]
daemon:
  id: daemon-1
nodes:
  - id: gate
    type: run
    command: task ci
`

type MachineBlipRunSuite struct {
	suite.Suite
	temporaltest.WorkflowTestSuite
}

func TestMachineBlipRun(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(MachineBlipRunSuite))
}

// gateCalls counts the run step's dispatches.
type gateCalls struct {
	e *subAgentFailureEnv
	n int
}

func (g *gateCalls) count() int {
	g.e.mu.Lock()
	defer g.e.mu.Unlock()
	return g.n
}

// setup runs machineBlipRunYAML, answering the gate's Nth dispatch (1-based)
// with gate(n).
func (s *MachineBlipRunSuite) setup(chatID string, unattended bool, gate func(n int) (map[string]interface{}, error)) (*subAgentFailureEnv, *gateCalls, WorkflowInput) {
	return s.setupYAML(chatID, machineBlipRunYAML, unattended, func(_ context.Context, _ map[string]interface{}, n int) (map[string]interface{}, error) {
		return gate(n)
	})
}

// setupYAML is setup for any definition, with the gate's activity context and
// input in hand.
func (s *MachineBlipRunSuite) setupYAML(chatID, yaml string, unattended bool, gate func(ctx context.Context, in map[string]interface{}, n int) (map[string]interface{}, error)) (*subAgentFailureEnv, *gateCalls, WorkflowInput) {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)
	input := spawnE2EWorkflowInput(chatID)
	if unattended {
		input.Inputs[InputKeyUnattended] = true
	}
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)
	e.workflowYAMLs = map[string]string{"agent": yaml}
	env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"daemon_available": true}, nil
	}, activity.RegisterOptions{Name: "PreflightDaemonCheck"})
	env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"ready": true}, nil
	}, activity.RegisterOptions{Name: "MachineResumeWake"})

	calls := &gateCalls{e: e}
	env.RegisterActivityWithOptions(func(ctx context.Context, in map[string]interface{}) (map[string]interface{}, error) {
		e.mu.Lock()
		calls.n++
		n := calls.n
		e.mu.Unlock()
		return gate(ctx, in, n)
	}, activity.RegisterOptions{Name: "ExecuteRunStep"})
	return e, calls, input
}

// machineUnavailable is the run step's failure once its command never reached
// the machine within the step's own wait (handlers.executeUntilDelivered).
// Spelled as a literal: it is the wire contract between the activity and the
// workflow.
func machineUnavailable() error {
	return temporal.NewNonRetryableApplicationError(
		"failed to execute command: machine e164d47d unavailable for 30m; the command never ran (last error: no daemon connected for user)",
		"RunStepMachineUnavailable", nil)
}

// outcomeUnknown is the run step's failure when the command reached a machine
// that then stopped answering: it may have run, so it is never re-sent inside
// the step. Tonight's six failures were this one.
func outcomeUnknown() error {
	return temporal.NewNonRetryableApplicationError(
		`failed to execute command: daemon transport failure (DAEMON_ERROR): no result came back, the command may have run: tool request "258035a7" got no answer from the machine in 5m20s; it may have run`,
		"RunStepOutcomeUnknown", nil)
}

func gatePassed() map[string]interface{} {
	return map[string]interface{}{"stdout": "ok", "output": "ok", "exit_code": 0}
}

// The step already waited out the machine (30m, inside the activity, spending
// none of the node's retries). Pausing after that only holds the run — and an
// automation's capacity slot — for a resume nobody sends: the run ends, failed,
// naming the machine.
func (s *MachineBlipRunSuite) TestUnattended_MachineUnavailableEndsTheRun() {
	e, calls, input := s.setup("chat-blip-unavailable", true, func(int) (map[string]interface{}, error) {
		return nil, machineUnavailable()
	})

	e.env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(e.env.IsWorkflowCompleted(), "the run must end, not park holding its automation's slot")
	err := e.env.GetWorkflowError()
	s.Require().Error(err)
	s.Contains(err.Error(), "machine e164d47d unavailable for 30m")
	s.Contains(err.Error(), "the command never ran")
	s.Empty(e.pausedStatuses(), "the run never paused")
	s.Equal(1, calls.count(), "the wait already happened inside the step; nothing re-dispatches it")
}

// The same failure through the production activity boundary (wrapActivity's
// classifyError), carrying text the classifier's terminal rules match: a
// machine status "not found" and a transport's "permission denied". It must
// keep its type, so the run still ends rather than pausing.
func (s *MachineBlipRunSuite) TestUnattended_MachineUnavailableSurvivesTheActivityWrapper() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)
	input := spawnE2EWorkflowInput("chat-blip-wrapped")
	input.Inputs[InputKeyUnattended] = true
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)
	e.workflowYAMLs = map[string]string{"agent": machineBlipRunYAML}
	env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"daemon_available": true}, nil
	}, activity.RegisterOptions{Name: "PreflightDaemonCheck"})
	env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"ready": true}, nil
	}, activity.RegisterOptions{Name: "MachineResumeWake"})
	calls := 0
	gate := func(context.Context, map[string]interface{}) (map[string]interface{}, error) {
		calls++
		return nil, temporal.NewNonRetryableApplicationError(
			"failed to execute command: machine e164d47d no longer exists (daemon not found); the command never ran (last error: permission denied)",
			types.RunStepMachineUnavailableErrType, nil)
	}
	env.RegisterActivityWithOptions(wrapActivity(NewActivityRegistry(&wrapperTestRepo{}), "ExecuteRunStep", gate, lifecycle.AgentWork, false),
		activity.RegisterOptions{Name: "ExecuteRunStep"})

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted(), "the run must end, not park holding its automation's slot")
	err := env.GetWorkflowError()
	s.Require().Error(err)
	s.Contains(err.Error(), "machine e164d47d no longer exists")
	s.Empty(e.pausedStatuses(), "the run never paused")
	s.Equal(1, calls)
}

// The same for an attended chat: the wait was the retry, so the run fails
// clearly rather than sitting paused.
func (s *MachineBlipRunSuite) TestAttended_MachineUnavailableEndsTheRun() {
	e, calls, input := s.setup("chat-blip-unavailable-attended", false, func(int) (map[string]interface{}, error) {
		return nil, machineUnavailable()
	})

	e.env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(e.env.IsWorkflowCompleted())
	s.Require().Error(e.env.GetWorkflowError())
	s.Contains(e.env.GetWorkflowError().Error(), "machine e164d47d unavailable for 30m")
	s.Empty(e.pausedStatuses())
	s.Equal(1, calls.count())
}

// The incident's shape: a machine that takes commands and never answers them.
// Each failure pauses the run and the ladder retries it; when the ladder runs
// out (1+2+5+10+15 = 33 minutes) the run ends, failed with the cause, instead
// of parking. An ended run is a closed chain, which is what frees the
// automation's capacity slot (#1850).
func (s *MachineBlipRunSuite) TestUnattended_PauseThatNeverClearsEndsTheRun() {
	e, calls, input := s.setup("chat-blip-never-clears", true, func(int) (map[string]interface{}, error) {
		return nil, outcomeUnknown()
	})
	start := e.env.Now()

	e.env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(e.env.IsWorkflowCompleted(), "the run must end, not park holding its automation's slot")
	err := e.env.GetWorkflowError()
	s.Require().Error(err)
	var appErr *temporal.ApplicationError
	s.Require().ErrorAs(err, &appErr)
	s.Equal("UnattendedPauseGaveUp", appErr.Type())
	s.Contains(err.Error(), "may have run", "the failure names what kept pausing it")
	s.Equal(1+len(selfPauseBackoff), calls.count(), "the first attempt and one per rung, then no more")
	s.GreaterOrEqual(e.env.Now().Sub(start), 33*time.Minute, "it gave up only after the whole ladder")
	s.Less(e.env.Now().Sub(start), time.Hour, "and not long after it")
}

// A node's timeout bounds its COMMAND on a healthy machine: it reaches the run
// step as the command timeout the machine enforces. The machine-wait
// allowance goes on the activity alone, for a command that never reached its
// machine — never extra time for one that is running.
func (s *MachineBlipRunSuite) TestNodeTimeoutBoundsTheCommandAndTheWaitIsExtra() {
	const timedGateYAML = `
name: agent
entry: [gate]
daemon:
  id: daemon-1
nodes:
  - id: gate
    type: run
    command: task ci
    timeout: 30s
`
	var (
		mu             sync.Mutex
		commandTimeout interface{}
		activityBound  time.Duration
	)
	e, calls, input := s.setupYAML("chat-blip-timeout", timedGateYAML, true, func(ctx context.Context, in map[string]interface{}, _ int) (map[string]interface{}, error) {
		info := activity.GetInfo(ctx)
		mu.Lock()
		defer mu.Unlock()
		commandTimeout = in["timeout"]
		activityBound = info.Deadline.Sub(info.StartedTime)
		return gatePassed(), nil
	})

	e.env.ExecuteWorkflow(DynamicWorkflow, input)
	mu.Lock()
	defer mu.Unlock()

	s.Require().True(e.env.IsWorkflowCompleted())
	s.Require().NoError(e.env.GetWorkflowError())
	s.Equal(1, calls.count())
	s.EqualValues(30000, commandTimeout, "the node's 30s is the command's timeout, not the 5m default")
	s.Equal(30*time.Second+types.RunStepMachineWaitBudget+types.RunStepSendOverhead, activityBound.Round(time.Second),
		"the activity may also wait out an unreachable machine, plus one send's transport overhead")
}

// A blip that clears while the ladder is still climbing: the next rung's retry
// goes through and the run completes, with nothing failed and nobody involved.
func (s *MachineBlipRunSuite) TestUnattended_MachineBackDuringTheLadderCompletes() {
	e, calls, input := s.setup("chat-blip-clears", true, func(n int) (map[string]interface{}, error) {
		if n <= 2 {
			return nil, outcomeUnknown()
		}
		return gatePassed(), nil
	})

	e.env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(e.env.IsWorkflowCompleted())
	s.Require().NoError(e.env.GetWorkflowError())
	s.Equal(3, calls.count())
}
