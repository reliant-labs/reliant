// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/telemetry"
	activitytypes "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// These tests swap the global tracer provider, so neither runs in parallel.

func recordHandlerSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return rec
}

func attrsOf(s sdktrace.ReadOnlySpan) map[attribute.Key]string {
	out := map[attribute.Key]string{}
	for _, kv := range s.Attributes() {
		out[kv.Key] = kv.Value.Emit()
	}
	return out
}

// A CallLLM is one agent turn: an invoke_agent span named for the agent's
// preset, whose traceparent the output records for the execute_tools that
// runs the turn's tool calls.
func TestCallLLM_OpensTheAgentTurnAndRecordsItsTraceParent(t *testing.T) {
	rec := recordHandlerSpans(t)
	resolver := drivers.DriverResolver(func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
		return &mockLLMDriverForIdempotency{}, nil
	})
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)
	activityInstance := NewCallLLMActivity(h.Repo(), &captureHub{}, nil, &staticConfigProvider{}, resolver, nil)

	input := callLLMInput(chat.ID, chat.ID, "mock-model")
	input.Runtime.Telemetry = &activitytypes.RunTelemetry{WorkflowName: "agent", RunID: "run-1", Preset: "researcher"}
	run := func(activityCtx context.Context, in ActivityInput) (*CallLLMOutput, error) {
		// What the worker's run-scope interceptor puts on the ctx.
		activityCtx = telemetry.WithRunScope(activityCtx, telemetry.RunScope{
			ChatID: in.Runtime.ChatID, ThreadID: in.Runtime.Thread,
			WorkflowName: in.Runtime.Telemetry.WorkflowName, RunID: in.Runtime.Telemetry.RunID,
		})
		return activityInstance.Execute(activityCtx, in)
	}
	h.env.RegisterActivity(run)
	val, err := h.env.ExecuteActivity(run, input)
	require.NoError(t, err)
	var out CallLLMOutput
	require.NoError(t, val.Get(&out))

	var turn sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == "invoke_agent researcher" {
			turn = s
		}
	}
	require.NotNil(t, turn, "the turn has its span")
	a := attrsOf(turn)
	assert.Equal(t, telemetry.GenAIOpInvokeAgent, a[telemetry.AttrGenAIOperationName])
	assert.Equal(t, "researcher", a[telemetry.AttrGenAIAgentName])
	assert.Equal(t, chat.ID, a[telemetry.AttrGenAIAgentID])
	assert.Equal(t, chat.ID, a[telemetry.AttrGenAIConversationID])
	assert.Equal(t, "run-1", a[telemetry.AttrAIRunID])
	assert.Equal(t, "agent", a[telemetry.AttrAIWorkflowName])
	assert.Equal(t, telemetry.TraceParentOf(turn.SpanContext()), out.TraceParent,
		"the output names the turn, for its tool calls")
}

// Each call of a batch is an execute_tool span under the turn that asked for
// it, a refused one too. (Its link to the execute_tools node's span, which a
// worker's activity ctx carries, is pinned by the runtime's span-tree test.)
func TestExecuteTools_OpensEachCallUnderTheTurnThatAskedForIt(t *testing.T) {
	rec := recordHandlerSpans(t)
	h, chatID := newValidationChat(t)
	activity := NewExecuteToolsActivity(h.Repo(), newMockToolExecutor())

	// The asking turn, as CallLLM recorded it.
	_, turn := otel.Tracer("test").Start(context.Background(), "invoke_agent general")
	turn.End()
	viewID, refusedID := "toolu_"+uuid.NewString(), "toolu_"+uuid.NewString()

	var output ExecuteToolsOutput
	require.NoError(t, h.ExecuteActivity(activity.Execute, ExecuteToolsInput{
		ChatID: chatID,
		Thread: chatID,
		Capabilities: (&tools.Capabilities{
			Offered:    []string{tools.ToolView},
			Permission: tools.PermissionMutating,
		}).Proto(),
		ToolCalls: []message.ToolCall{
			{ID: viewID, Name: tools.ToolView, Input: `{"file_path":"/tmp/x"}`},
			{ID: refusedID, Name: "slack__message_post", Input: `{}`},
		},
		Telemetry: &activitytypes.RunTelemetry{RunID: "run-1", TurnTraceParent: telemetry.TraceParentOf(turn.SpanContext())},
	}, &output))
	require.Len(t, output.ToolResults, 2)

	calls := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range rec.Ended() {
		if a := attrsOf(s); a[telemetry.AttrGenAIOperationName] == telemetry.GenAIOpExecuteTool {
			calls[a[telemetry.AttrGenAIToolCallID]] = s
		}
	}
	require.Len(t, calls, 2, "one span per call, the refused one included")
	view := calls[viewID]
	require.NotNil(t, view)
	assert.Equal(t, "execute_tool "+tools.ToolView, view.Name())
	assert.Equal(t, turn.SpanContext().SpanID(), view.Parent().SpanID(), "the call is the asking turn's child")
	assert.Equal(t, turn.SpanContext().TraceID(), view.SpanContext().TraceID())
	va := attrsOf(view)
	assert.Equal(t, tools.ToolView, va[telemetry.AttrGenAIToolName])
	assert.Equal(t, telemetry.GenAIToolTypeFunction, va[telemetry.AttrGenAIToolType])
	assert.Equal(t, telemetry.ToolLocationDaemon, va[telemetry.AttrAIToolLocation])
	assert.NotContains(t, va, attribute.Key(telemetry.AttrErrorType))

	refused := calls[refusedID]
	require.NotNil(t, refused)
	assert.Equal(t, "tool_error", attrsOf(refused)[telemetry.AttrErrorType], "a refusal is the call's outcome, not a failure to run it")
}

func TestTurnPreset(t *testing.T) {
	t.Parallel()
	chat := &db.Chat{SelectedPresets: map[string]string{"agent": "general", "reviewer": "strict"}}
	assert.Equal(t, "spawned", turnPreset(RuntimeContext{Telemetry: &activitytypes.RunTelemetry{Preset: "spawned"}}, chat),
		"the runtime's preset wins")
	assert.Equal(t, "general,strict", turnPreset(RuntimeContext{}, chat), "the chat's own agent runs as its selection")
	assert.Empty(t, turnPreset(RuntimeContext{Telemetry: &activitytypes.RunTelemetry{ParentRunID: "p"}}, chat),
		"a sub-agent with no preset does not take the chat's")
	assert.Empty(t, turnPreset(RuntimeContext{}, &db.Chat{}))
}
