// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/projectaccess"

	"github.com/reliant-labs/reliant/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/baggage"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.temporal.io/sdk/client"
	temporalotel "go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/threads"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// tracingStarter opens the StartWorkflow span the production Temporal client's
// tracing interceptor opens, with the same tracer and the same parent rule
// (the span ctx holds, or a new root when it holds none), then records the
// start like fakeStarter. Which trace a run lands in is decided right there.
type tracingStarter struct {
	fakeStarter
	tracer interceptor.Tracer

	mu     sync.Mutex
	starts map[string]trace.SpanContext // workflow id → its StartWorkflow span
	parent map[string]trace.SpanContext // workflow id → that span's parent
}

func newTracingStarter(t *testing.T, tp trace.TracerProvider) *tracingStarter {
	t.Helper()
	tracer, err := temporalotel.NewTracer(temporalotel.TracerOptions{Tracer: tp.Tracer("temporal-client"), AllowInvalidParentSpans: true})
	require.NoError(t, err)
	return &tracingStarter{tracer: tracer, starts: map[string]trace.SpanContext{}, parent: map[string]trace.SpanContext{}}
}

func (s *tracingStarter) ExecuteWorkflow(
	ctx context.Context, options client.StartWorkflowOptions, wf interface{}, args ...interface{},
) (client.WorkflowRun, error) {
	span, err := s.tracer.StartSpan(&interceptor.TracerStartSpanOptions{
		Parent: s.tracer.SpanFromContext(ctx), Operation: "StartWorkflow", Name: options.ID, Time: time.Now(), ToHeader: true,
	})
	if err != nil {
		return nil, err
	}
	defer span.Finish(&interceptor.TracerFinishSpanOptions{})
	if _, isRoot := args[0].(v2.WorkflowInput); isRoot {
		ro := span.(interface{ SpanContext() trace.SpanContext })
		s.mu.Lock()
		s.starts[options.ID] = ro.SpanContext()
		if parent := s.tracer.SpanFromContext(ctx); parent != nil {
			s.parent[options.ID] = parent.(interface{ SpanContext() trace.SpanContext }).SpanContext()
		}
		s.mu.Unlock()
	}
	return s.fakeStarter.ExecuteWorkflow(ctx, options, wf, args...)
}

// One integration delivery fans out to two orgs' triggers. Each run is the
// root of a trace of its own — never a child of the firing span, which would
// put both orgs' runs in one trace — and carries the firing span for the run
// to link back to.
func TestLaunch_TriggerFanOutStartsATraceOfItsOwnPerRun(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(tracetest.NewSpanRecorder()))
	starter := newTracingStarter(t, tp)
	launcher := New(Deps{Store: repo, Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()), ProjectRows: store.NewProjectExtStore(repo.ORM()), Projects: projectaccess.NewFromORM(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Threads: threads.New(threads.Deps{Repo: repo, Messages: store.NewMessageExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM())}), Temporal: starter, Runs: &recordingRunRecorder{repo: repo}, TaskQueue: "test-task-queue"})

	fireCtx, firing := tp.Tracer("test").Start(ctx, "trigger.fire integration")
	defer firing.End()
	fireCtx = withBaggage(t, fireCtx, "delivery", "d-1")
	wantLink := telemetry.TraceParentOf(firing.SpanContext())

	runs := map[string]string{} // org → workflow id
	for _, org := range []string{"org-a", "org-b"} {
		res, err := launcher.Launch(fireCtx, Event{
			Kind: core.TriggerEventKindIntegration, DedupeKey: "trigger-" + org + ":delivery-1", OccurredAt: time.Now().UTC(),
		}, Spec{
			OwnerUserID: launchTestUserID, ProjectID: projectID, DefaultOrgID: org, Workflow: "builtin://agent",
			Params: mockModelParams(t), Messages: userSeed("go"),
		})
		require.NoError(t, err)
		require.NotNil(t, res.Chat.OrgID)
		require.Equal(t, org, *res.Chat.OrgID)
		runs[org] = res.WorkflowID
	}

	a, b := starter.starts[runs["org-a"]], starter.starts[runs["org-b"]]
	require.True(t, a.IsValid() && b.IsValid(), "both runs were started")
	assert.NotEqual(t, a.TraceID(), b.TraceID(), "two orgs' runs are two traces")
	for org, id := range runs {
		start := starter.starts[id]
		assert.NotEqual(t, firing.SpanContext().TraceID(), start.TraceID(), "%s's run is not in the firing trace", org)
		assert.False(t, starter.parent[id].IsValid(), "%s's run start is a root span", org)
	}
	for _, call := range starter.calls {
		if input, ok := call.args[0].(v2.WorkflowInput); ok {
			require.NotNil(t, input.Trigger)
			assert.Equal(t, wantLink, input.Trigger.TraceParent, "the run carries its firing span, to link to")
		}
	}
}

// A person's own start is unchanged: its run stays in the trace of the
// request that started it, and carries no firing span.
func TestLaunch_AttendedStartStaysInItsCallersTrace(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(tracetest.NewSpanRecorder()))
	starter := newTracingStarter(t, tp)
	launcher := New(Deps{Store: repo, Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()), ProjectRows: store.NewProjectExtStore(repo.ORM()), Projects: projectaccess.NewFromORM(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Threads: threads.New(threads.Deps{Repo: repo, Messages: store.NewMessageExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM())}), Temporal: starter, Runs: &recordingRunRecorder{repo: repo}, TaskQueue: "test-task-queue"})

	reqCtx, request := tp.Tracer("test").Start(ctx, "ChatService/CreateChat")
	defer request.End()
	res, err := launcher.Launch(reqCtx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent",
		Params: mockModelParams(t), Messages: userSeed("hi"),
	})
	require.NoError(t, err)

	start := starter.starts[res.WorkflowID]
	require.True(t, start.IsValid())
	assert.Equal(t, request.SpanContext().TraceID(), start.TraceID(), "the run is in the request's trace")
	assert.True(t, starter.parent[res.WorkflowID].IsValid(), "under the request, not a root")
	_, input := starter.rootRun(t)
	assert.Empty(t, input.Trigger.TraceParent)
}

func withBaggage(t *testing.T, ctx context.Context, key, value string) context.Context {
	t.Helper()
	member, err := baggage.NewMember(key, value)
	require.NoError(t, err)
	bag, err := baggage.New(member)
	require.NoError(t, err)
	return baggage.ContextWithBaggage(ctx, bag)
}

// runTraceContext detaches exactly the kinds a stored trigger fires, keeps
// the caller's deadline and values, and never edits the caller's trigger.
func TestRunTraceContext(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	type key struct{}
	base, cancel := context.WithTimeout(context.WithValue(context.Background(), key{}, "v"), time.Minute)
	defer cancel()
	caller, span := tp.Tracer("test").Start(withBaggage(t, base, "k", "v"), "caller")
	defer span.End()
	firing := telemetry.TraceParentOf(span.SpanContext())

	for kind, unattended := range map[core.TriggerEventKind]bool{
		core.TriggerEventKindSchedule: true, core.TriggerEventKindWebhook: true,
		core.TriggerEventKindIntegration: true, core.TriggerEventKindWorkflowEvent: true,
		core.TriggerEventKindChatStart: false, core.TriggerEventKindManualRun: false,
		core.TriggerEventKindAgentStartRun: false, core.TriggerEventKindCLIRun: false,
		core.TriggerEventKindBuilderTest: false,
	} {
		t.Run(string(kind), func(t *testing.T) {
			in := &v2.TriggerInfo{Kind: string(kind), EventID: "e"}
			ctx, out := runTraceContext(caller, in, firing)
			assert.Empty(t, in.TraceParent, "the caller's trigger is not edited")
			assert.Equal(t, "v", ctx.Value(key{}))
			_, hasDeadline := ctx.Deadline()
			assert.True(t, hasDeadline)
			if !unattended {
				assert.Equal(t, caller, ctx)
				assert.Same(t, in, out)
				return
			}
			assert.False(t, trace.SpanContextFromContext(ctx).IsValid(), "a new root")
			assert.Zero(t, baggage.FromContext(ctx).Len(), "the firing trace's baggage stays there")
			assert.Equal(t, firing, out.TraceParent)
			assert.Equal(t, "e", out.EventID)
		})
	}

	_, out := runTraceContext(caller, &v2.TriggerInfo{Kind: string(core.TriggerEventKindSchedule)}, "not-a-traceparent")
	assert.Empty(t, out.TraceParent, "a malformed firing span is no link")
	ctx, out := runTraceContext(caller, nil, firing)
	assert.Nil(t, out)
	assert.Equal(t, caller, ctx)
}
