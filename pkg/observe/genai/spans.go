package genai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// TracerName is the instrumentation scope of the spans this package starts.
const TracerName = "github.com/reliant-labs/forge/pkg/observe/genai"

// tracer is the global provider's tracer, read at span time so a span started
// before observe.Setup ran still reports once it has.
func tracer() trace.Tracer { return otel.Tracer(TracerName) }

// scopeKey keys the attributes every AI span started from a context carries.
type scopeKey struct{}

// WithScope returns ctx whose AI spans (every Start* from it, and from the
// contexts those return) carry attrs: the caller's identity (AttrUserID,
// AttrOrgID) or anything else a breakdown groups on. A later value for a key
// replaces an earlier one.
func WithScope(ctx context.Context, attrs ...attribute.KeyValue) context.Context {
	if len(attrs) == 0 {
		return ctx
	}
	prev := Scope(ctx)
	merged := make([]attribute.KeyValue, 0, len(prev)+len(attrs))
	for _, kv := range prev {
		if !hasKey(attrs, kv.Key) {
			merged = append(merged, kv)
		}
	}
	for i, kv := range attrs {
		if !hasKey(attrs[i+1:], kv.Key) {
			merged = append(merged, kv)
		}
	}
	return context.WithValue(ctx, scopeKey{}, merged)
}

// Scope is the attributes WithScope and the Start* helpers put in ctx.
func Scope(ctx context.Context) []attribute.KeyValue {
	s, _ := ctx.Value(scopeKey{}).([]attribute.KeyValue)
	return s
}

func hasKey(kvs []attribute.KeyValue, k attribute.Key) bool {
	for _, kv := range kvs {
		if kv.Key == k {
			return true
		}
	}
	return false
}

// nonEmpty appends key=v to attrs when v is set.
func nonEmpty(attrs []attribute.KeyValue, key, v string) []attribute.KeyValue {
	if v == "" {
		return attrs
	}
	return append(attrs, attribute.String(key, v))
}

// start opens a span named name under ctx with the scope, own and extra
// attributes, all passed at start so a sampler sees them, and returns the
// context with scoped added to its scope.
func start(ctx context.Context, name string, kind trace.SpanKind, own, scoped, extra []attribute.KeyValue) (context.Context, trace.Span) {
	ctx = WithScope(ctx, scoped...)
	attrs := append(append(append([]attribute.KeyValue{}, Scope(ctx)...), own...), extra...)
	return tracer().Start(ctx, name, trace.WithSpanKind(kind), trace.WithAttributes(attrs...))
}

// spanName is "<operation> <subject>", or the operation alone.
func spanName(op, subject string) string {
	if subject == "" {
		return op
	}
	return op + " " + subject
}

// Chat describes one model call.
type Chat struct {
	// Provider is gen_ai.provider.name: "anthropic", "openai", or the
	// gateway's own name. Required by the conventions.
	Provider string
	// Model is the model asked for (gen_ai.request.model).
	Model string
	// Operation is OpChat when empty; OpTextCompletion, OpGenerateContent
	// or OpEmbeddings for those calls.
	Operation string
	// Stream says the response streams.
	Stream bool
	// MaxTokens is the requested output limit; 0 is unset.
	MaxTokens int64
	// Temperature is the requested temperature; nil is unset.
	Temperature *float64
	// Attributes are extra attributes for this span alone.
	Attributes []attribute.KeyValue
}

// StartChat opens the CLIENT span "chat <model>" for one model call. Close it
// with End, after SetResponse.
func StartChat(ctx context.Context, c Chat) (context.Context, trace.Span) {
	op := c.Operation
	if op == "" {
		op = OpChat
	}
	own := []attribute.KeyValue{attribute.String(AttrOperationName, op)}
	own = nonEmpty(own, AttrProviderName, c.Provider)
	own = nonEmpty(own, AttrRequestModel, c.Model)
	if c.Stream {
		own = append(own, attribute.Bool(AttrRequestStream, true))
	}
	if c.MaxTokens > 0 {
		own = append(own, attribute.Int64(AttrRequestMaxTokens, c.MaxTokens))
	}
	if c.Temperature != nil {
		own = append(own, attribute.Float64(AttrRequestTemperature, *c.Temperature))
	}
	return start(ctx, spanName(op, c.Model), trace.SpanKindClient, own, nil, c.Attributes)
}

// Agent describes one agent turn.
type Agent struct {
	// Name is gen_ai.agent.name: what a "by agent" breakdown groups on.
	Name string
	// ID is gen_ai.agent.id.
	ID string
	// ConversationID is gen_ai.conversation.id, carried by every span
	// under the turn.
	ConversationID string
	// Attributes are extra attributes for this span alone.
	Attributes []attribute.KeyValue
}

// StartAgent opens the INTERNAL span "invoke_agent <name>" for one agent turn.
// The model and tool calls under the returned context carry the agent's name
// and the conversation.
func StartAgent(ctx context.Context, a Agent) (context.Context, trace.Span) {
	var scoped []attribute.KeyValue
	scoped = nonEmpty(scoped, AttrAgentName, a.Name)
	scoped = nonEmpty(scoped, AttrConversationID, a.ConversationID)
	own := []attribute.KeyValue{attribute.String(AttrOperationName, OpInvokeAgent)}
	own = nonEmpty(own, AttrAgentID, a.ID)
	return start(ctx, spanName(OpInvokeAgent, a.Name), trace.SpanKindInternal, own, scoped, a.Attributes)
}

// Tool describes one tool call.
type Tool struct {
	// Name is gen_ai.tool.name.
	Name string
	// CallID is the model's id for the call (gen_ai.tool.call.id).
	CallID string
	// Type is gen_ai.tool.type: "function", "extension" or "datastore".
	Type string
	// Location is forge.ai.tool.location: where the tool ran ("local",
	// "daemon", "remote").
	Location string
	// Attributes are extra attributes for this span alone.
	Attributes []attribute.KeyValue
}

// StartTool opens the INTERNAL span "execute_tool <name>" for one tool call.
// Close it with End: an error result is a failed tool call.
func StartTool(ctx context.Context, t Tool) (context.Context, trace.Span) {
	own := []attribute.KeyValue{attribute.String(AttrOperationName, OpExecuteTool)}
	own = nonEmpty(own, AttrToolName, t.Name)
	own = nonEmpty(own, AttrToolCallID, t.CallID)
	own = nonEmpty(own, AttrToolType, t.Type)
	own = nonEmpty(own, AttrToolLocation, t.Location)
	return start(ctx, spanName(OpExecuteTool, t.Name), trace.SpanKindInternal, own, nil, t.Attributes)
}

// Workflow describes one segment of a workflow run.
type Workflow struct {
	// Name is forge.ai.workflow.name.
	Name string
	// Ref is forge.ai.workflow.ref, the workflow's versioned reference.
	Ref string
	// Trigger is forge.ai.workflow.trigger: what started the run.
	Trigger string
	// RunID is forge.ai.run.id, stable across every segment of the run.
	RunID string
	// RootRunID is forge.ai.root_run_id; empty is RunID (a top-level run).
	RootRunID string
	// ParentRunID is forge.ai.parent_run_id; empty on a top-level run.
	ParentRunID string
	// Attributes are extra attributes for this span alone.
	Attributes []attribute.KeyValue
}

// StartWorkflow opens the INTERNAL span "invoke_workflow <name>" for one
// segment of a run. A run that continues in a new trace (continue-as-new, a
// resume) opens another segment with the same RunID; the dashboards group a
// run by RunID, never by TraceId. Every span under the returned context
// carries the workflow's name and the run's ids.
func StartWorkflow(ctx context.Context, w Workflow) (context.Context, trace.Span) {
	root := w.RootRunID
	if root == "" {
		root = w.RunID
	}
	var scoped []attribute.KeyValue
	scoped = nonEmpty(scoped, AttrWorkflowName, w.Name)
	scoped = nonEmpty(scoped, AttrRunID, w.RunID)
	scoped = nonEmpty(scoped, AttrRootRunID, root)
	own := []attribute.KeyValue{attribute.String(AttrOperationName, OpInvokeWorkflow)}
	own = nonEmpty(own, AttrWorkflowRef, w.Ref)
	own = nonEmpty(own, AttrWorkflowTrigger, w.Trigger)
	own = nonEmpty(own, AttrParentRunID, w.ParentRunID)
	return start(ctx, spanName(OpInvokeWorkflow, w.Name), trace.SpanKindInternal, own, scoped, w.Attributes)
}

// Node describes one execution of a workflow node.
type Node struct {
	// ID is forge.ai.node.id.
	ID string
	// Type is forge.ai.node.type.
	Type string
	// Iteration is forge.ai.node.iteration: the node's execution count in
	// the run, from 0.
	Iteration int
	// Attributes are extra attributes for this span alone.
	Attributes []attribute.KeyValue
}

// StartNode opens the INTERNAL span "workflow.node <id>" for one execution of
// a node. Every span under the returned context carries the node's id.
func StartNode(ctx context.Context, n Node) (context.Context, trace.Span) {
	scoped := nonEmpty(nil, AttrNodeID, n.ID)
	own := nonEmpty(nil, AttrNodeType, n.Type)
	own = append(own, attribute.Int(AttrNodeIteration, n.Iteration))
	return start(ctx, spanName("workflow.node", n.ID), trace.SpanKindInternal, own, scoped, n.Attributes)
}

// Usage is a call's token usage. A zero field is not recorded, except the
// input and output counts, which always are.
type Usage struct {
	// InputTokens counts every prompt token, cache reads and writes
	// included.
	InputTokens int64
	// OutputTokens counts every completion token, reasoning included.
	OutputTokens int64
	// CacheReadInputTokens is the part of InputTokens read from the cache.
	CacheReadInputTokens int64
	// CacheCreationInputTokens is the part of InputTokens written to it.
	CacheCreationInputTokens int64
	// ReasoningOutputTokens is the part of OutputTokens spent reasoning.
	ReasoningOutputTokens int64
}

// SetUsage records u on span.
func SetUsage(span trace.Span, u Usage) {
	attrs := []attribute.KeyValue{
		attribute.Int64(AttrUsageInputTokens, u.InputTokens),
		attribute.Int64(AttrUsageOutputTokens, u.OutputTokens),
	}
	if u.CacheReadInputTokens > 0 {
		attrs = append(attrs, attribute.Int64(AttrUsageCacheReadInputTokens, u.CacheReadInputTokens))
	}
	if u.CacheCreationInputTokens > 0 {
		attrs = append(attrs, attribute.Int64(AttrUsageCacheCreationInputTokens, u.CacheCreationInputTokens))
	}
	if u.ReasoningOutputTokens > 0 {
		attrs = append(attrs, attribute.Int64(AttrUsageReasoningOutputTokens, u.ReasoningOutputTokens))
	}
	span.SetAttributes(attrs...)
}

// Response is what a model call returned.
type Response struct {
	// Model is the model that served the call (gen_ai.response.model).
	Model string
	// ID is the provider's response id.
	ID string
	// FinishReasons are why the model stopped, one per choice.
	FinishReasons []string
	// Usage is the call's token usage.
	Usage Usage
	// CostUSD is the cost the provider or gateway REPORTED for the call;
	// nil when it reported none. Never an estimate.
	CostUSD *float64
	// Retries is how many times the call was retried before this response.
	Retries int
}

// SetResponse records r on a chat span.
func SetResponse(span trace.Span, r Response) {
	var attrs []attribute.KeyValue
	attrs = nonEmpty(attrs, AttrResponseModel, r.Model)
	attrs = nonEmpty(attrs, AttrResponseID, r.ID)
	if len(r.FinishReasons) > 0 {
		attrs = append(attrs, attribute.StringSlice(AttrResponseFinishReasons, r.FinishReasons))
	}
	if r.CostUSD != nil {
		attrs = append(attrs, attribute.Float64(AttrCostUSD, *r.CostUSD))
	}
	if r.Retries > 0 {
		attrs = append(attrs, attribute.Int(AttrRetries, r.Retries))
	}
	span.SetAttributes(attrs...)
	SetUsage(span, r.Usage)
}

// FirstToken records EventFirstToken on a streamed chat span: the time to
// first token is the span's start to it.
func FirstToken(span trace.Span) { span.AddEvent(EventFirstToken) }

// Retry describes one backoff before a retried call.
type Retry struct {
	// Attempt is the attempt about to be retried, from 1.
	Attempt int
	// StatusCode is the HTTP status that caused it; 0 when there was none.
	StatusCode int
	// Reason is the cause in words ("rate_limit", "overloaded").
	Reason string
	// Delay is the backoff.
	Delay time.Duration
}

// RetryWait records EventRetryWait on a chat span, once per backoff. Record
// the total with Response.Retries.
func RetryWait(span trace.Span, r Retry) {
	attrs := []attribute.KeyValue{
		attribute.Int(AttrRetryAttempt, r.Attempt),
		attribute.Int64(AttrRetryDelayMs, r.Delay.Milliseconds()),
	}
	if r.StatusCode != 0 {
		attrs = append(attrs, attribute.Int(AttrHTTPStatusCode, r.StatusCode))
	}
	attrs = nonEmpty(attrs, AttrRetryReason, r.Reason)
	span.AddEvent(EventRetryWait, trace.WithAttributes(attrs...))
}

// End closes span with err's outcome: nil is success; a cancellation
// (context.Canceled) records error.type=canceled and leaves the status unset,
// because a cancelled call did not fail; anything else records its
// ErrorType and an error status.
func End(span trace.Span, err error) {
	EndWithErrorType(span, err, "")
}

// EndWithErrorType is End with the error's class named by the caller (a
// provider's "rate_limit", "overloaded", or an HTTP status), for errors whose
// Go type says too little. An empty errorType is ErrorType(err).
func EndWithErrorType(span trace.Span, err error, errorType string) {
	defer span.End()
	if err == nil {
		return
	}
	if errorType == "" {
		errorType = ErrorType(err)
	}
	span.SetAttributes(attribute.String(AttrErrorType, errorType))
	if errorType == ErrorTypeCanceled {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

// ErrorType is err's error.type: ErrorTypeCanceled for context.Canceled,
// "timeout" for context.DeadlineExceeded, else err's Go type.
func ErrorType(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return ErrorTypeCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	return strings.TrimPrefix(fmt.Sprintf("%T", err), "*")
}
