// Copyright (c) 2025 Reliant Labs
package observability

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"

	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// A workflow node's activity runs under a workflow.node span carrying the
// node and the run; the activity's own work (here, one child span) is under
// it. An activity that is not a node's opens none. The test swaps the global
// tracer provider, so it does not run in parallel.
func TestRunScopeInterceptor_OpensANodeSpanForANodesActivity(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	owners := telemetry.NewChatOwnerOrgCache(func(context.Context, string) (telemetry.ChatOwner, error) {
		return telemetry.ChatOwner{UserID: "user-1", OrgID: "org-1"}, nil
	}, 0, 10)
	telemetryJSON := map[string]any{"workflow_name": "agent", "workflow_ref": "builtin://agent", "trigger": "manual", "run_id": "run-1"}

	for name, tc := range map[string]struct {
		input    map[string]any
		wantNode bool
		wantType string
		wantIter string
	}{
		"a v3 node in a loop": {
			input: map[string]any{
				"runtime": map[string]any{"chat_id": "chat-1", "thread": "thread-1", "workflow_id": "wf-1",
					"step_id": "call_llm", "loop_node_id": "agent_loop", "loop_iteration": 4, "telemetry": telemetryJSON},
				"node": map[string]any{"id": "call_llm", "type": "call_llm"},
			},
			wantNode: true, wantType: "call_llm", wantIter: "4",
		},
		"a run step": {
			input: map[string]any{"chat_id": "chat-1", "workflow_id": "wf-1", "step_id": "build", "node_type": "run",
				"telemetry": telemetryJSON},
			wantNode: true, wantType: "run", wantIter: "0",
		},
		"bookkeeping, not a node": {
			input: map[string]any{"chat_id": "chat-1", "workflow_id": "wf-1", "status": "started"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			rec.Reset()
			env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
			env.SetWorkerOptions(temporaltest.WorkerOptions(worker.Options{
				Interceptors: []interceptor.WorkerInterceptor{NewRunScopeWorkerInterceptor(owners)},
			}))
			work := func(ctx context.Context, _ map[string]any) error {
				_, span := otel.Tracer("test").Start(ctx, "work")
				span.End()
				return nil
			}
			env.RegisterActivity(work)
			_, err := env.ExecuteActivity(work, tc.input)
			require.NoError(t, err)

			byName := map[string]sdktrace.ReadOnlySpan{}
			for _, s := range rec.Ended() {
				byName[s.Name()] = s
			}
			workSpan := byName["work"]
			require.NotNil(t, workSpan)
			if !tc.wantNode {
				assert.Len(t, rec.Ended(), 1, "no node span")
				return
			}
			var node sdktrace.ReadOnlySpan
			for n, s := range byName {
				if n != "work" {
					node = s
				}
			}
			require.NotNil(t, node)
			assert.Equal(t, node.SpanContext().SpanID(), workSpan.Parent().SpanID(), "the node's work is under its span")
			assert.Equal(t, trace.SpanKindInternal, node.SpanKind())
			a := map[attribute.Key]string{}
			for _, kv := range node.Attributes() {
				a[kv.Key] = kv.Value.Emit()
			}
			stepID := tc.input["step_id"]
			if rt, ok := tc.input["runtime"].(map[string]any); ok {
				stepID = rt["step_id"]
			}
			assert.Equal(t, "workflow.node "+stepID.(string), node.Name())
			assert.Equal(t, stepID, a[telemetry.AttrAINodeID])
			assert.Equal(t, tc.wantType, a[telemetry.AttrAINodeType])
			assert.Equal(t, tc.wantIter, a[telemetry.AttrAINodeIteration])
			assert.Equal(t, "run-1", a[telemetry.AttrAIRunID])
			assert.Equal(t, "run-1", a[telemetry.AttrAIRootRunID])
			assert.Equal(t, "agent", a[telemetry.AttrAIWorkflowName])
			assert.Equal(t, "builtin://agent", a[telemetry.AttrAIWorkflowRef])
			assert.Equal(t, "manual", a[telemetry.AttrAIWorkflowTrigger])
			assert.Equal(t, "user-1", a[telemetry.AttrUserID])
			assert.Equal(t, "org-1", a[telemetry.AttrOrgID])
			assert.Equal(t, "chat-1", a[telemetry.AttrChatID])
		})
	}
}
