// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"sync"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/lifecycle"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"go.temporal.io/sdk/activity"
)

// wrapperTestRepo is the minimum db.Repository the ActivityWrapper touches on
// its success path: the stopped-run check, the liveness write, the step row
// and the node event. Everything else panics (nil embedded interface), which
// is how a test learns it strayed off that path.
type wrapperTestRepo struct {
	db.Repository

	mu    sync.Mutex
	steps []*db.StepExecution
}

func (r *wrapperTestRepo) GetWorkflow(context.Context, string) (*db.Workflow, error) {
	return nil, nil
}

func (r *wrapperTestRepo) EnsureWorkflowRunning(context.Context, string, string) {}

func (r *wrapperTestRepo) CreateStepExecution(_ context.Context, exec *db.StepExecution) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, exec)
	return nil
}

func (r *wrapperTestRepo) EmitNodeExecutionEvent(context.Context, string, *db.NodeExecutionState) error {
	return nil
}

func (r *wrapperTestRepo) CreateChatUpdate(context.Context, string, reliantv1.ChatUpdateType, string, string) error {
	return nil
}

func (r *wrapperTestRepo) stepRows() []*db.StepExecution {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*db.StepExecution(nil), r.steps...)
}

// writtenMessage is one message the recording writer received.
type writtenMessage struct {
	Runtime        types.RuntimeContext
	Args           *reliantv1.SaveMessageNodeArgs
	IdempotencyKey string
	Attempt        int32
}

// recordingMessageWriter is a MessageWriter that records every write and can
// be told to fail the first N of them.
type recordingMessageWriter struct {
	mu       sync.Mutex
	writes   []writtenMessage
	failures int
	failErr  error
	calls    int
}

func (w *recordingMessageWriter) WriteMessage(
	_ context.Context,
	rtx types.RuntimeContext,
	args *reliantv1.SaveMessageNodeArgs,
	idempotencyKey string,
	attempt int32,
) (*reliantv1.SaveMessageOutput, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	if w.calls <= w.failures {
		return nil, w.failErr
	}
	w.writes = append(w.writes, writtenMessage{Runtime: rtx, Args: args, IdempotencyKey: idempotencyKey, Attempt: attempt})
	return &reliantv1.SaveMessageOutput{
		MessageId:        "msg-" + rtx.StepID,
		Thread:           rtx.Thread,
		ThreadTokenCount: 42,
		MessageCount:     1,
	}, nil
}

func (w *recordingMessageWriter) messages() []writtenMessage {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]writtenMessage(nil), w.writes...)
}

// registerWrapped registers fn under name with the Temporal test env wrapped
// exactly as production wraps a registered activity (wrapActivity) — the real
// ActivityWrapper runs around it, including the delegated save_message and
// message-only stripping. It deliberately skips RegisterActivity's global
// schema registration, which other tests in this package depend on being
// absent for these names.
func registerWrapped[I any, O any](
	env interface {
		RegisterActivityWithOptions(interface{}, activity.RegisterOptions)
	},
	registry *ActivityRegistry,
	name string,
	fn func(context.Context, I) (O, error),
) {
	env.RegisterActivityWithOptions(wrapActivity(registry, name, fn, lifecycle.AgentWork), activity.RegisterOptions{Name: name})
}

// messageWriterFunc adapts a function to MessageWriter.
type messageWriterFunc func(ctx context.Context, rtx types.RuntimeContext, args *reliantv1.SaveMessageNodeArgs, key string, attempt int32) (*reliantv1.SaveMessageOutput, error)

func (f messageWriterFunc) WriteMessage(ctx context.Context, rtx types.RuntimeContext, args *reliantv1.SaveMessageNodeArgs, key string, attempt int32) (*reliantv1.SaveMessageOutput, error) {
	return f(ctx, rtx, args, key, attempt)
}
