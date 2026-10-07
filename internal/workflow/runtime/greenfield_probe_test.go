// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"

	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// greenfieldProbeRun is one DynamicWorkflow execution with the probe activity
// stubbed, recording the order activities started in and what the probe was
// asked.
type greenfieldProbeRun struct {
	mu         sync.Mutex
	started    []string
	probeInput map[string]interface{}
}

func (r *greenfieldProbeRun) order() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.started...)
}

// runWithGreenfieldProbe drives the one-node ask_question workflow interactively
// — its node's first activity is QuestionCreate, standing in for the first LLM
// call — and answers the question so the run completes.
func runWithGreenfieldProbe(t *testing.T, probe bool, probeErr error) *greenfieldProbeRun {
	t.Helper()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	run := &greenfieldProbeRun{}

	env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, _ converter.EncodedValues) {
		run.mu.Lock()
		run.started = append(run.started, info.ActivityType.Name)
		run.mu.Unlock()
	})

	var questionID string
	registerUnattendedTestActivities(t, env, askQuestionWorkflowBytes(t), &questionID)
	env.RegisterActivityWithOptions(
		func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			run.mu.Lock()
			run.probeInput = input
			run.mu.Unlock()
			if probeErr != nil {
				return nil, probeErr
			}
			return map[string]interface{}{"outcome": "guidance_seeded"}, nil
		},
		activity.RegisterOptions{Name: GreenfieldProbeActivityName},
	)
	env.RegisterDelayedCallback(func() {
		require.NotEmpty(t, questionID)
		env.SignalWorkflow("signal.question."+questionID, map[string]interface{}{
			"action":        "reply",
			"response_data": `{"answers":[{"question":"Proceed?","selected":[],"freetext":"go"}]}`,
		})
	}, time.Second)

	env.ExecuteWorkflow(DynamicWorkflow, WorkflowInput{
		ChatID:       "chat-greenfield",
		WorkflowName: "unattended-ask-question",
		Inputs: map[string]interface{}{
			"project_path":      "/home/workspace/projects/my-project",
			"session_daemon_id": "daemon-session",
		},
		ExecContext: &ExecutionContext{
			WorkflowID:   "wf-greenfield",
			ChatID:       "chat-greenfield",
			Thread:       "thread-greenfield",
			ThreadMode:   model.ThreadModeNew,
			WorkflowName: "unattended-ask-question",
		},
		GreenfieldProbe: probe,
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError(), "no outcome of the probe may fail the run")
	return run
}

func indexOf(names []string, want string) int {
	for i, name := range names {
		if name == want {
			return i
		}
	}
	return -1
}

// The probe runs before the run's first node does, so the guidance it may seed
// is already in the thread when the first LLM call reads it — and it probes the
// machine the run's tools execute on, at the run's working directory.
func TestGreenfieldProbeRunsBeforeTheFirstNode(t *testing.T) {
	t.Parallel()
	run := runWithGreenfieldProbe(t, true, nil)

	order := run.order()
	probeAt, firstNodeAt := indexOf(order, GreenfieldProbeActivityName), indexOf(order, "QuestionCreate")
	require.NotEqual(t, -1, probeAt, "a first-turn run must probe; activities: %v", order)
	require.NotEqual(t, -1, firstNodeAt, "activities: %v", order)
	assert.Less(t, probeAt, firstNodeAt, "the probe must finish before the first node starts; activities: %v", order)

	run.mu.Lock()
	defer run.mu.Unlock()
	assert.Equal(t, "chat-greenfield", run.probeInput["chat_id"])
	assert.Equal(t, "thread-greenfield", run.probeInput["thread"])
	assert.Equal(t, "/home/workspace/projects/my-project", run.probeInput["project_path"])
	assert.Equal(t, "daemon-session", run.probeInput["daemon_id"],
		"the probe must ask the machine the run's tools execute on")
}

// Every run that is not a chat's first turn — and every history recorded
// before the field existed, which decodes it as false — schedules no probe.
// That is what keeps those histories' command sequence, and so their replay,
// unchanged.
func TestGreenfieldProbeIsNotScheduledWhenTheInputDoesNotAskForIt(t *testing.T) {
	t.Parallel()
	run := runWithGreenfieldProbe(t, false, nil)

	assert.Equal(t, -1, indexOf(run.order(), GreenfieldProbeActivityName),
		"activities: %v", run.order())
}

// A probe that fails or times out costs the run nothing but the guidance.
func TestGreenfieldProbeFailureDoesNotStopTheRun(t *testing.T) {
	t.Parallel()
	run := runWithGreenfieldProbe(t, true, errors.New("worker lost the activity"))

	order := run.order()
	assert.NotEqual(t, -1, indexOf(order, GreenfieldProbeActivityName))
	assert.NotEqual(t, -1, indexOf(order, "QuestionCreate"),
		"the run must carry on to its first node; activities: %v", order)
}
