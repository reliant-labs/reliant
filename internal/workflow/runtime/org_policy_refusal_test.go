// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/orgpolicy"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// orgPolicyRefused is a call an org policy refused, as the activity wrapper
// hands it to Temporal (classifyError over the driver's orgpolicy.Refusal).
func orgPolicyRefused(unattended bool) error {
	return classifyError(fmt.Errorf("stream llm response: %w",
		&orgpolicy.Refusal{Reason: orgpolicy.ReasonModelNotAllowed, Detail: "claude-opus-5 is not allowed in Acme", Unattended: unattended}))
}

// A refusal is never retried, and its type says whether a person is behind
// the run; the marker survives as text.
func TestClassifyError_OrgPolicyRefusal(t *testing.T) {
	t.Parallel()
	for unattended, want := range map[bool]string{false: types.OrgPolicyRefusedErrType, true: types.OrgPolicyRefusedUnattendedErrType} {
		var app *temporal.ApplicationError
		err := orgPolicyRefused(unattended)
		if !errors.As(err, &app) || app.Type() != want || !app.NonRetryable() {
			t.Fatalf("unattended=%v: %v, want non-retryable %s", unattended, err, want)
		}
		assert.True(t, isTerminal(err))
		assert.True(t, orgpolicy.IsRefusal(err), "the marker survives classification")
	}
	// A refusal recovered from text alone (after Temporal stringified it) is
	// still a verdict, read as attended.
	text := errors.New((&orgpolicy.Refusal{Reason: orgpolicy.ReasonOrgSpendCapReached}).Error())
	var app *temporal.ApplicationError
	if !errors.As(classifyError(text), &app) || app.Type() != types.OrgPolicyRefusedErrType {
		t.Fatalf("marker-only refusal = %v", classifyError(text))
	}
	assert.True(t, isTerminal(text))
}

// A paused refusal is what a capacity wake releases (a model switch, Resume,
// or the policy changing); an unattended one ends its run instead of pausing.
func TestOrgPolicyRefusal_PauseOrEnd(t *testing.T) {
	t.Parallel()
	assert.True(t, providerCapacityError(orgPolicyRefused(false)))

	var pc PauseController
	assert.Nil(t, pc.endsRunInsteadOfPausing("call_llm", orgPolicyRefused(false)), "an attended refusal pauses")
	end := pc.endsRunInsteadOfPausing("call_llm", orgPolicyRefused(true))
	var app *temporal.ApplicationError
	if !errors.As(end, &app) || app.Type() != types.OrgPolicyRefusedUnattendedErrType || !orgpolicy.IsRefusal(end) {
		t.Fatalf("an unattended refusal ends the run with %v", end)
	}
}

// The turn PAUSES on a policy refusal (it does not fail), and a model switch
// (the capacity wake the chat's model change sends) resumes it: the
// conversation continues where it stopped.
func (s *CapacityWakeSuite) TestOrgPolicyRefusal_PausesThenAModelSwitchResumes() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	input := spawnE2EWorkflowInput("chat-org-policy-pause")
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)
	var allowed capacity
	e.parentTurn = func(int) ([]map[string]interface{}, error) {
		if !allowed.ok() {
			return nil, orgPolicyRefused(false)
		}
		return nil, nil
	}
	env.RegisterDelayedCallback(func() {
		allowed.restore() // the person picked an allowed model
		env.SignalWorkflow(CapacityWakeSignalName, nil)
	}, 10*time.Minute)
	// The same wake again (the org's policy watcher retrying a delivery it
	// could not confirm): idempotent, it resumes nothing twice.
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(CapacityWakeSignalName, nil) }, 10*time.Minute+time.Second)

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError(), "a policy refusal pauses the turn; it never fails the run")
	at, ok := e.callAt("parent", 2)
	s.Require().True(ok, "the turn went on after the model switch")
	s.GreaterOrEqual(at.Sub(startOf(e)), 10*time.Minute, "the turn waited, paused, for the switch")
	s.Equal(2, e.turns(input.ExecContext.Thread), "a second wake re-ran nothing: one refused turn, one resumed turn")
}

// An unattended run ends with the reason on a policy refusal instead of
// self-resuming into the same verdict.
func (s *CapacityWakeSuite) TestOrgPolicyRefusal_EndsAnUnattendedRun() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	input := spawnE2EWorkflowInput("chat-org-policy-unattended")
	input.Inputs[InputKeyUnattended] = true
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)
	e.parentTurn = func(int) ([]map[string]interface{}, error) { return nil, orgPolicyRefused(true) }

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	err := env.GetWorkflowError()
	s.Require().Error(err, "the unattended run ended")
	s.True(orgpolicy.IsRefusal(err), "it ended with the policy's reason: %v", err)
	s.Equal(1, e.turns(input.ExecContext.Thread), "no self-resume met the verdict again")
}
