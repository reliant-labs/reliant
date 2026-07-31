// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// A self-pause — retry exhaustion, or the daemon-offline breaker — parks the
// workflow on workflow.Await with no timeout. The only thing that clears it is
// a human sending signal.resume. On an unattended run there is no human, so
// the run is dead where it stands until someone happens to look.
//
// These tests pin the ladder that fixes it, and the three things it must NOT
// do: resume an attended run, resume a run a person paused, or retry forever.
//
// The Temporal test environment auto-advances its clock whenever every
// coroutine is blocked, so the real 1m/2m/5m/10m/15m rungs cost no wall clock
// here. That is also why these tests can assert on the FULL ladder rather than
// a shortened stand-in: the durations under test are the production ones.

type SelfResumeSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
}

func TestSelfResume(t *testing.T) {
	suite.Run(t, new(SelfResumeSuite))
}

// selfPauseWorkflow arms ONE self-pause and blocks on it. It returns "resumed"
// only if something cleared the pause; a test-harness cancel unblocks the Await
// too, so that case is reported as an error to keep "still parked" distinct
// from "genuinely resumed".
func selfPauseWorkflow(ctx workflow.Context, unattended bool) (string, error) {
	pc := newPauseCoordinator(ctx, "wf-test", pauseOptions{HoldResume: true, Unattended: unattended})
	pc.RequestPause()
	pc.CheckPause(ctx)
	if ctx.Err() != nil {
		return "", temporal.NewCanceledError("still parked when cancelled")
	}
	return "resumed", nil
}

func (s *SelfResumeSuite) TestUnattendedSelfPause_ResumesItself() {
	env := s.NewTestWorkflowEnvironment()

	// Nothing signals this workflow. If it completes, it resumed itself.
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 30*time.Minute)

	env.ExecuteWorkflow(selfPauseWorkflow, true)

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError(),
		"an unattended run must resume itself from a self-pause — nobody is coming")
	var result string
	s.NoError(env.GetWorkflowResult(&result))
	s.Equal("resumed", result)
}

func (s *SelfResumeSuite) TestAttendedSelfPause_StaysParked() {
	// The counterpart, and the reason this is gated on `unattended` rather
	// than applied to every run: when a human is there, the pause is how the
	// workflow asks for them, and resuming underneath them hides the ask.
	env := s.NewTestWorkflowEnvironment()

	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 60*time.Minute)

	env.ExecuteWorkflow(selfPauseWorkflow, false)

	s.True(env.IsWorkflowCompleted())
	s.Error(env.GetWorkflowError(),
		"an attended run must stay parked: the human is the resume")
}

// userPauseWorkflow parks on a pause it did NOT arm itself.
func userPauseWorkflow(ctx workflow.Context) (string, error) {
	pc := newPauseCoordinator(ctx, "wf-test", pauseOptions{HoldResume: true, Unattended: true})
	// Let the signal be delivered before the check.
	_ = workflow.Sleep(ctx, 10*time.Millisecond)
	pc.CheckPause(ctx)
	if ctx.Err() != nil {
		return "", temporal.NewCanceledError("still parked when cancelled")
	}
	return "resumed", nil
}

func (s *SelfResumeSuite) TestUnattendedUserPause_StaysParked() {
	// "Unattended" means nobody is watching, not that nobody may instruct. A
	// pause someone sent by hand must survive, or the ladder would quietly
	// undo an explicit instruction — the single worst thing it could do.
	env := s.NewTestWorkflowEnvironment()

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.pause", nil)
	}, 1*time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 60*time.Minute)

	env.ExecuteWorkflow(userPauseWorkflow)

	s.True(env.IsWorkflowCompleted())
	s.Error(env.GetWorkflowError(),
		"a pause a person sent must stick, even with no person watching")
}

// selfPauseAfterUserPauseWorkflow arms a self-pause and then receives an
// explicit pause on top of it.
func selfPauseAfterUserPauseWorkflow(ctx workflow.Context) (string, error) {
	pc := newPauseCoordinator(ctx, "wf-test", pauseOptions{HoldResume: true, Unattended: true})
	pc.RequestPause()
	// The user pause lands while the self-pause is already armed.
	_ = workflow.Sleep(ctx, 10*time.Millisecond)
	pc.CheckPause(ctx)
	if ctx.Err() != nil {
		return "", temporal.NewCanceledError("still parked when cancelled")
	}
	return "resumed", nil
}

func (s *SelfResumeSuite) TestUserPauseOverSelfPause_StaysParked() {
	// The overlap case: the run self-paused, and THEN a person paused it. The
	// ladder must not treat the still-armed self-pause as its own to clear.
	env := s.NewTestWorkflowEnvironment()

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.pause", nil)
	}, 1*time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 60*time.Minute)

	env.ExecuteWorkflow(selfPauseAfterUserPauseWorkflow)

	s.True(env.IsWorkflowCompleted())
	s.Error(env.GetWorkflowError(),
		"a user pause landing on top of a self-pause must stop the ladder")
}

// selfResumeCounterStub records each time the ladder let the workflow through.
func selfResumeCounterStub(ctx context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
	return map[string]interface{}{"success": true}, nil
}

// repeatedSelfPauseWorkflow re-arms a self-pause every time it is resumed —
// the shape of a cause that does not clear (a sustained rate limit, a daemon
// that never comes back). It reports each pass through an activity so the test
// can count them without mutating state from workflow code.
func repeatedSelfPauseWorkflow(ctx workflow.Context) (string, error) {
	pc := newPauseCoordinator(ctx, "wf-test", pauseOptions{HoldResume: true, Unattended: true})
	ao := workflow.ActivityOptions{StartToCloseTimeout: 5 * time.Second}

	// More iterations than the ladder has rungs: the extra ones must never be
	// reached.
	for i := 0; i < len(selfPauseBackoff)+3; i++ {
		pc.RequestPause()
		pc.CheckPause(ctx)
		if ctx.Err() != nil {
			return "", temporal.NewCanceledError("still parked when cancelled")
		}
		_ = workflow.ExecuteActivity(
			workflow.WithActivityOptions(ctx, ao), selfResumeCounterStub,
			map[string]interface{}{"pass": i},
		).Get(ctx, nil)
	}
	return "never parked", nil
}

func (s *SelfResumeSuite) TestUnattendedSelfPause_LadderIsBounded() {
	// A cause that outlives every rung is not a rate limit — it needs a
	// person. The ladder must run out and leave the run parked where someone
	// can see it, rather than burning tokens against it forever.
	env := s.NewTestWorkflowEnvironment()

	resumes := 0
	env.OnActivity(selfResumeCounterStub, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			resumes++
			return map[string]interface{}{"success": true}, nil
		},
	)

	// Well past the ladder's total (1+2+5+10+15 = 33 minutes).
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 6*time.Hour)

	env.ExecuteWorkflow(repeatedSelfPauseWorkflow)

	s.True(env.IsWorkflowCompleted())
	s.Error(env.GetWorkflowError(),
		"the workflow must still be parked when the ladder runs out")
	s.Equal(len(selfPauseBackoff), resumes,
		"the ladder must grant exactly one resume per rung and then stop")
}
