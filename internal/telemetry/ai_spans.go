// Copyright (c) 2025 Reliant Labs
package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// The spans below the workflow's own that make a run readable as an AI run:
//
//	RunWorkflow:DynamicWorkflow        gen_ai.operation.name=invoke_workflow
//	└ workflow.node <node>             one execution of a node (StartNodeSpan)
//	  └ invoke_agent <agent>           one agent turn (StartAgentTurn)
//	    ├ chat <model>                 one provider call (llm.StartGenAISpan)
//	    └ execute_tool <tool>          one tool call the turn asked for (StartToolCall)
//
// A turn's tool calls run in the next activity (execute_tools), after the turn
// itself has ended, on whichever worker takes it. The turn's span context
// travels there in the workflow's own data (TraceParent), so the calls are
// children of the turn that asked for them rather than of the execute_tools
// node; each keeps a link to that node's span.
//
// Every one of these spans is opened worker-side, in an activity, never in
// workflow code: workflow code is replayed, and a span opened there would be
// emitted again, or lost, on every replay.

// Resolved per span, not cached: a package-level tracer taken before the
// process installs its provider would stay a no-op.
func aiTracer() trace.Tracer {
	return otel.Tracer("github.com/reliant-labs/reliant/internal/telemetry")
}

// StartNodeSpan opens the span for one execution of the workflow node ctx's
// run scope names (RunScope.NodeID): "workflow.node <id>". The returned ctx
// carries it; end it with EndAISpan.
//
// It carries the scope's SpanAttributes and the chat as gen_ai.conversation.id
// too, so a per-chat read keyed on the conversation finds the node itself, not
// only its children. Not GenAIAttributes: a node is not an agent, so it names
// no gen_ai.agent.*.
func StartNodeSpan(ctx context.Context) (context.Context, trace.Span) {
	scope := RunScopeFromContext(ctx)
	attrs := scope.SpanAttributes()
	if scope.ChatID != "" {
		attrs = append(attrs, attribute.String(AttrGenAIConversationID, scope.ChatID))
	}
	return aiTracer().Start(ctx, SpanNameNode+" "+scope.NodeID,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attrs...))
}

// AgentTurn is the span of one agent turn: "invoke_agent <agent>", the turn's
// LLM calls its children (they are opened under its ctx) and, through
// TraceParent, its tool calls.
type AgentTurn struct {
	span trace.Span
}

// StartAgentTurn opens the span for one turn of the agent ctx's run scope
// names. End it exactly once.
func StartAgentTurn(ctx context.Context) (context.Context, *AgentTurn) {
	scope := RunScopeFromContext(ctx)
	name := GenAIOpInvokeAgent
	if agent := scope.AgentName(); agent != "" {
		name += " " + agent
	}
	attrs := append([]attribute.KeyValue{attribute.String(AttrGenAIOperationName, GenAIOpInvokeAgent)}, scope.GenAIAttributes()...)
	ctx, span := aiTracer().Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attrs...))
	return ctx, &AgentTurn{span: span}
}

// TraceParent is the turn's span context as a W3C traceparent, for the tool
// calls the turn asks for to be opened under it (StartToolCall). "" when the
// span is not recording.
func (t *AgentTurn) TraceParent() string {
	if t == nil {
		return ""
	}
	return TraceParentOf(t.span.SpanContext())
}

// End records err's class, if any, and ends the span (EndAISpan).
func (t *AgentTurn) End(err error) {
	t.EndAs(err, "")
}

// EndAs is End with err's class named by the caller (a value of the
// error.type vocabulary, ai_error_type.go): the turn's provider failure, which
// internal/llm can classify and this package cannot. "" is ErrorTypeOf(err).
func (t *AgentTurn) EndAs(err error, errType string) {
	if t == nil {
		return
	}
	EndAISpanAs(t.span, err, errType)
}

// ToolCall is the span of one tool call: "execute_tool <tool>".
type ToolCall struct {
	span trace.Span
}

// StartToolCall opens the span for the tool call callID of tool name, run at
// location (ToolLocationDaemon or ToolLocationLocal). turnTraceParent is the
// traceparent of the agent turn that asked for it (AgentTurn.TraceParent);
// when it names a span of this trace the call is opened under that turn and
// links the span ctx carries (the execute_tools node), otherwise it is opened
// under ctx's span and links the turn. The returned ctx carries the call's
// span, so the gateway's round trip to the daemon is its child.
func StartToolCall(ctx context.Context, name, callID, location, turnTraceParent string) (context.Context, *ToolCall) {
	scope := RunScopeFromContext(ctx)
	attrs := append([]attribute.KeyValue{
		attribute.String(AttrGenAIOperationName, GenAIOpExecuteTool),
		attribute.String(AttrGenAIToolName, name),
		attribute.String(AttrGenAIToolType, GenAIToolTypeFunction),
	}, scope.GenAIAttributes()...)
	if callID != "" {
		attrs = append(attrs, attribute.String(AttrGenAIToolCallID, callID))
	}
	if location != "" {
		attrs = append(attrs, attribute.String(AttrAIToolLocation, location))
	}
	opts := []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindInternal), trace.WithAttributes(attrs...)}

	current := trace.SpanContextFromContext(ctx)
	if turn := SpanContextOf(turnTraceParent); turn.IsValid() {
		if !current.IsValid() || current.TraceID() == turn.TraceID() {
			if current.IsValid() {
				opts = append(opts, trace.WithLinks(trace.Link{SpanContext: current}))
			}
			ctx = trace.ContextWithRemoteSpanContext(ctx, turn)
		} else {
			opts = append(opts, trace.WithLinks(trace.Link{SpanContext: turn}))
		}
	}
	ctx, span := aiTracer().Start(ctx, GenAIOpExecuteTool+" "+name, opts...)
	return ctx, &ToolCall{span: span}
}

// End records how the call ended and ends the span. A tool that ran and
// reported an error is the tool's outcome, not a failure of the call: it is
// error.type=tool_error and leaves the status alone. err is a failure to run
// it at all.
func (c *ToolCall) End(toolError bool, err error) {
	if c == nil {
		return
	}
	if toolError && err == nil {
		c.span.SetAttributes(attribute.String(AttrErrorType, ErrorTypeToolError))
	}
	EndAISpan(c.span, err)
}

// EndAISpan records err's class on span, if any, and ends it: error.type and
// an error status described by the same code (SetAISpanError), never the
// error's text. A cancellation is the caller hanging up (the user pressed
// stop, a newer turn superseded this one), not a failure: it is
// error.type=canceled and sets no error status, as the gen_ai span does.
//
// The text is not lost: the code that returned err logs it (an activity's
// failure is logged by the Temporal worker with the run's ids), and the span's
// trace and run ids join the two.
func EndAISpan(span trace.Span, err error) {
	EndAISpanAs(span, err, "")
}

// EndAISpanAs is EndAISpan with err's class named by the caller; "" is
// ErrorTypeOf(err).
func EndAISpanAs(span trace.Span, err error, errType string) {
	SetAISpanError(span, err, errType)
	span.End()
}

// TraceParentOf is sc as a W3C traceparent header value, "" when sc is not
// valid.
func TraceParentOf(sc trace.SpanContext) string {
	if !sc.IsValid() {
		return ""
	}
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(trace.ContextWithSpanContext(context.Background(), sc), carrier)
	return carrier.Get("traceparent")
}

// SpanContextOf parses a W3C traceparent; the zero SpanContext when it is
// empty or malformed.
func SpanContextOf(traceParent string) trace.SpanContext {
	if traceParent == "" {
		return trace.SpanContext{}
	}
	ctx := propagation.TraceContext{}.Extract(context.Background(), propagation.MapCarrier{"traceparent": traceParent})
	return trace.SpanContextFromContext(ctx)
}
