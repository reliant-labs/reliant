// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/models"
	rtemporal "github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// The workflow holds a thread on a ProviderSwitchHold, so the hold must reach
// it as a top-level, non-retryable application error of that type after the
// activity has been through the REAL wrapper: ActivityWrapper.Execute, the
// error classifier and flattenForTemporal. A wrap layer, or a classifier that
// retries it, turns a visible per-thread hold into a retry loop.

// holdProbe is a registered activity whose body is a real model resolution
// that the bound provider cannot serve, wrapped the way call_llm wraps
// its errors.
type holdProbe struct {
	name     string
	attempts atomic.Int32
	run      func(ctx context.Context) error
}

func (p *holdProbe) Name() string { return p.name }

func (p *holdProbe) Execute(ctx context.Context, _ types.ActivityInput) (map[string]interface{}, error) {
	p.attempts.Add(1)
	if err := p.run(ctx); err != nil {
		return nil, err
	}
	return map[string]interface{}{"ok": true}, nil
}

// wrapperStore is the minimum runtime.ActivityStore the wrapper touches.
type wrapperStore struct {
	runtime.ActivityStore
}

func (wrapperStore) GetWorkflow(context.Context, string) (*db.Workflow, error) { return nil, nil }
func (wrapperStore) EnsureWorkflowRunning(context.Context, string, string)     {}
func (wrapperStore) CreateStepExecution(context.Context, *db.StepExecution) error {
	return nil
}
func (wrapperStore) EmitNodeExecutionEvent(context.Context, string, *db.NodeExecutionState) error {
	return nil
}
func (wrapperStore) CreateChatUpdate(context.Context, string, reliantv1.ChatUpdateType, string, string) error {
	return nil
}

func executeThroughRealWrapper(t *testing.T, probe *holdProbe) error {
	t.Helper()
	registry := runtime.NewActivityRegistry(wrapperStore{})
	runtime.RegisterActivity[types.ActivityInput, map[string]interface{}](registry, probe)
	fn, err := registry.Get(probe.name)
	require.NoError(t, err)

	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.SetDataConverter(rtemporal.NewFlexibleDataConverter())
	env.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: probe.name})

	_, err = env.ExecuteActivity(probe.name, types.ActivityInput{
		Runtime: types.RuntimeContext{ChatID: "chat-1", Thread: "thread-1", WorkflowID: "wf-1", StepID: "call_llm"},
		Node:    &reliantv1.Node{Id: "call_llm", Type: model.NodeTypeCallLLM},
	})
	return err
}

// rootCause is the application error Temporal hands the workflow: the first
// one in the activity error's chain.
func rootCause(t *testing.T, err error) *temporal.ApplicationError {
	t.Helper()
	require.Error(t, err)
	var app *temporal.ApplicationError
	require.True(t, errors.As(err, &app), "the workflow needs an ApplicationError, got %T: %v", err, err)
	return app
}

func TestProviderSwitchHold_SurvivesTheRealActivityWrapper(t *testing.T) {
	// codex is connected but serves nothing; anthropic and openai would serve.
	withAvailableDrivers(t, boundDrivers("codex", "anthropic", "codex", "openai"))

	resolveHeld := func(ctx context.Context) error {
		_, err := resolveLLMCall(ctx, nil, llmCallSpec{
			UserID:          "u",
			Selector:        models.ModelSelector{Tags: []string{models.TagFast}},
			ChatProvider:    "codex",
			Descendant:      true,
			PresetModelTags: []string{models.TagFast},
		})
		return err
	}

	t.Run("a bare hold", func(t *testing.T) {
		probe := &holdProbe{name: "HoldProbeBare", run: resolveHeld}
		app := rootCause(t, executeThroughRealWrapper(t, probe))
		assert.Equal(t, "ProviderSwitchHold", app.Type())
		assert.True(t, app.NonRetryable(), "a hold must not be retried by Temporal")
		assert.EqualValues(t, 1, probe.attempts.Load())
	})

	t.Run("a hold wrapped in several fmt.Errorf layers", func(t *testing.T) {
		probe := &holdProbe{name: "HoldProbeWrapped", run: func(ctx context.Context) error {
			err := resolveHeld(ctx)
			require.Error(t, err)
			return fmt.Errorf("call_llm: %w", fmt.Errorf("resolve model: %w", fmt.Errorf("prepare request: %w", err)))
		}}
		runErr := executeThroughRealWrapper(t, probe)
		app := rootCause(t, runErr)
		assert.Equal(t, "ProviderSwitchHold", app.Type(), "wrap layers must not hide the hold from the workflow")
		assert.True(t, app.NonRetryable())
		assert.EqualValues(t, 1, probe.attempts.Load(), "a hold is not retried")
		assert.Equal(t, 1, strings.Count(runErr.Error(), "This thread is waiting"), "the explanation is rendered once, not once per wrap layer: %v", runErr)
	})

	t.Run("a compaction hold goes through the same wrapper", func(t *testing.T) {
		probe := &holdProbe{name: "HoldProbeCompact", run: func(ctx context.Context) error {
			_, _, err := resolveCompactionModelForCall(ctx, "u",
				&reliantv1.ModelSelector{Tags: []string{models.TagFast}}, compactSpec("codex", true))
			return err
		}}
		app := rootCause(t, executeThroughRealWrapper(t, probe))
		assert.Equal(t, "ProviderSwitchHold", app.Type())
		assert.True(t, app.NonRetryable())
	})

	t.Run("control: an ordinary failure is not reported as a hold", func(t *testing.T) {
		probe := &holdProbe{name: "HoldProbeOrdinary", run: func(context.Context) error {
			return errors.New("connection reset by peer")
		}}
		app := rootCause(t, executeThroughRealWrapper(t, probe))
		assert.NotEqual(t, "ProviderSwitchHold", app.Type())
	})
}
