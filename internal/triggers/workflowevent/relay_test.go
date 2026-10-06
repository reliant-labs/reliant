// Copyright (c) 2025 Reliant Labs
package workflowevent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/triggers/runevents"
)

// recordingStarter records dispatch starts, refusing ids already started the
// way Temporal does under REJECT_DUPLICATE.
type recordingStarter struct {
	mu      sync.Mutex
	started map[string]bool
	down    bool
}

func (s *recordingStarter) ExecuteWorkflow(_ context.Context, options client.StartWorkflowOptions, _ any, _ ...any) (client.WorkflowRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down {
		return nil, errors.New("temporal unavailable")
	}
	if s.started == nil {
		s.started = map[string]bool{}
	}
	if s.started[options.ID] {
		return nil, serviceerror.NewWorkflowExecutionAlreadyStarted("already started", "", "")
	}
	s.started[options.ID] = true
	return &stubRun{id: options.ID}, nil
}

func TestRelay_HandsEachEventToItsDispatchWorkflowOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.addTrigger(f.userID, f.projectID, "listener", "builtin://agent", Source{Workflows: []string{"code-review"}})
	chatID := f.humanRun("code-review")
	_, err := emitFinished(ctx, f, chatID)
	require.NoError(t, err)

	starter := &recordingStarter{}
	relay := NewRelay(f.repo, starter, "test-queue")

	handed, err := relay.RelayOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, handed)
	assert.Len(t, starter.started, 1)

	// Dispatched rows are not claimed again.
	handed, err = relay.RelayOnce(ctx)
	require.NoError(t, err)
	assert.Zero(t, handed)
}

func TestRelay_AFailedStartLeavesTheEventForALaterPass(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.addTrigger(f.userID, f.projectID, "listener", "builtin://agent", Source{Workflows: []string{"code-review"}})
	_, err := emitFinished(ctx, f, f.humanRun("code-review"))
	require.NoError(t, err)

	starter := &recordingStarter{down: true}
	relay := NewRelay(f.repo, starter, "test-queue")
	relay.Lease = time.Millisecond

	handed, err := relay.RelayOnce(ctx)
	require.Error(t, err)
	assert.Zero(t, handed)

	starter.down = false
	time.Sleep(5 * time.Millisecond) // let the lease lapse
	handed, err = relay.RelayOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, handed, "the event must survive a failed handoff")
}

// A crash between the start and the dispatched stamp re-starts the same
// workflow id; that must be success, not a stuck row.
func TestRelay_AlreadyStartedIsSuccess(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.addTrigger(f.userID, f.projectID, "listener", "builtin://agent", Source{Workflows: []string{"code-review"}})
	created, err := emitFinished(ctx, f, f.humanRun("code-review"))
	require.NoError(t, err)

	starter := &recordingStarter{started: map[string]bool{DispatchWorkflowID(created.ID): true}}
	relay := NewRelay(f.repo, starter, "test-queue")
	handed, err := relay.RelayOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, handed)
}

func TestDispatchWorkflowRunsTheActivityForItsEvent(t *testing.T) {
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(RunEventDispatchWorkflow, workflow.RegisterOptions{Name: DispatchWorkflowName})
	var got DispatchInput
	env.RegisterActivityWithOptions(func(_ context.Context, in DispatchInput) (*DispatchOutput, error) {
		got = in
		return &DispatchOutput{Results: []Result{{TriggerID: "t1", Outcome: core.TriggerEventLaunched}}}, nil
	}, activity.RegisterOptions{Name: DispatchActivityName})

	env.ExecuteWorkflow(RunEventDispatchWorkflow, DispatchInput{RunEventID: "ev-1"})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	assert.Equal(t, "ev-1", got.RunEventID)
}

// emitFinished writes chatID's finished run event, the way the WorkflowStatus
// activity does, and returns it without claiming it.
func emitFinished(ctx context.Context, f *fixture, chatID string) (*core.RunEvent, error) {
	f.t.Helper()
	runID := "run-" + chatID
	if _, err := runevents.EmitTerminal(ctx, f.repo, runevents.Terminal{
		ChatID: chatID, WorkflowID: chatID, WorkflowName: "code-review", RunID: runID, Outcome: core.RunEventFinished,
	}, time.Now().UTC()); err != nil {
		return nil, err
	}
	return f.repo.GetRunEventByDedupe(ctx, chatID+":"+runID+":finished")
}
