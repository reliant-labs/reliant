package genai

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// recorder installs a global provider that samples per sampler and records
// every ended span.
func recorder(t *testing.T, sampler sdktrace.Sampler) *tracetest.SpanRecorder {
	t.Helper()
	prev := otel.GetTracerProvider()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec), sdktrace.WithSampler(sampler))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev); _ = tp.Shutdown(context.Background()) })
	return rec
}

func attrs(s sdktrace.ReadOnlySpan) map[string]attribute.Value {
	m := map[string]attribute.Value{}
	for _, kv := range s.Attributes() {
		m[string(kv.Key)] = kv.Value
	}
	return m
}

func byName(t *testing.T, rec *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, s := range rec.Ended() {
		if s.Name() == name {
			return s
		}
	}
	t.Fatalf("no span %q", name)
	return nil
}

// The tree a workflow run makes: every span under the workflow carries the
// workflow, run and node, and the chat and tool spans the agent's name.
func TestTheSpanTreeCarriesItsScope(t *testing.T) {
	rec := recorder(t, sdktrace.AlwaysSample())
	ctx := WithScope(context.Background(), attribute.String(AttrUserID, "u1"), attribute.String(AttrOrgID, "o1"))
	ctx, wf := StartWorkflow(ctx, Workflow{Name: "triage", RunID: "run-1", Trigger: "schedule"})
	ctx, node := StartNode(ctx, Node{ID: "classify", Type: "agent", Iteration: 2})
	ctx, agent := StartAgent(ctx, Agent{Name: "triager", ID: "thread-1", ConversationID: "chat-1"})
	chatCtx, chat := StartChat(ctx, Chat{Provider: "anthropic", Model: "claude-sonnet-5", Stream: true})
	cost := 0.0123
	FirstToken(chat)
	SetResponse(chat, Response{Model: "claude-sonnet-5-20261001", ID: "msg_1", FinishReasons: []string{"end_turn"},
		Usage: Usage{InputTokens: 1200, OutputTokens: 300, CacheReadInputTokens: 1000}, CostUSD: &cost, Retries: 1})
	End(chat, nil)
	_ = chatCtx
	_, tool := StartTool(ctx, Tool{Name: "grep", CallID: "call_1", Type: "function", Location: "daemon"})
	End(tool, errors.New("boom"))
	End(agent, nil)
	End(node, nil)
	End(wf, nil)

	c := attrs(byName(t, rec, "chat claude-sonnet-5"))
	for k, want := range map[string]string{
		AttrOperationName: OpChat, AttrProviderName: "anthropic", AttrRequestModel: "claude-sonnet-5",
		AttrResponseModel: "claude-sonnet-5-20261001", AttrWorkflowName: "triage", AttrRunID: "run-1", AttrRootRunID: "run-1",
		AttrNodeID: "classify", AttrAgentName: "triager", AttrConversationID: "chat-1", AttrUserID: "u1", AttrOrgID: "o1",
	} {
		if got := c[k].Emit(); got != want {
			t.Errorf("chat %s = %q, want %q", k, got, want)
		}
	}
	for k, want := range map[string]int64{AttrUsageInputTokens: 1200, AttrUsageOutputTokens: 300, AttrUsageCacheReadInputTokens: 1000, AttrRetries: 1} {
		if got := c[k].AsInt64(); got != want {
			t.Errorf("chat %s = %d, want %d", k, got, want)
		}
	}
	if c[AttrCostUSD].AsFloat64() != cost {
		t.Errorf("cost = %v", c[AttrCostUSD])
	}
	if _, ok := c[AttrUsageReasoningOutputTokens]; ok {
		t.Error("a zero reasoning count was recorded")
	}
	if _, ok := c[AttrAgentID]; ok {
		t.Error("the agent id belongs on the agent span alone")
	}

	tl := byName(t, rec, "execute_tool grep")
	ta := attrs(tl)
	if ta[AttrToolLocation].Emit() != "daemon" || ta[AttrWorkflowName].Emit() != "triage" || ta[AttrErrorType].Emit() != "errors.errorString" {
		t.Errorf("tool attrs = %v", ta)
	}
	if tl.Status().Code != codes.Error {
		t.Error("a failed tool call is not an error span")
	}
	wa := attrs(byName(t, rec, "invoke_workflow triage"))
	if wa[AttrOperationName].Emit() != OpInvokeWorkflow || wa[AttrWorkflowTrigger].Emit() != "schedule" {
		t.Errorf("workflow attrs = %v", wa)
	}
	na := attrs(byName(t, rec, "workflow.node classify"))
	if na[AttrNodeIteration].AsInt64() != 2 || na[AttrNodeType].Emit() != "agent" {
		t.Errorf("node attrs = %v", na)
	}
}

// A cancelled call records its class and stays a non-error span.
func TestEndTreatsACancellationAsNoError(t *testing.T) {
	rec := recorder(t, sdktrace.AlwaysSample())
	_, span := StartChat(context.Background(), Chat{Provider: "openai", Model: "gpt-5"})
	End(span, context.Canceled)
	s := rec.Ended()[0]
	if s.Status().Code == codes.Error {
		t.Error("a cancellation was recorded as an error")
	}
	if attrs(s)[AttrErrorType].Emit() != ErrorTypeCanceled {
		t.Errorf("error.type = %v", attrs(s)[AttrErrorType])
	}
}

func TestErrorType(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, ""},
		{context.Canceled, ErrorTypeCanceled},
		{context.DeadlineExceeded, "timeout"},
		{errors.New("x"), "errors.errorString"},
	} {
		if got := ErrorType(tc.err); got != tc.want {
			t.Errorf("ErrorType(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// Under a sampler that keeps nothing, KeepSampler still keeps every AI span
// (a workflow node included) and nothing else.
func TestKeepSamplerKeepsAISpansOnly(t *testing.T) {
	rec := recorder(t, KeepSampler(sdktrace.NeverSample()))
	ctx := context.Background()
	_, plain := otel.Tracer("t").Start(ctx, "GET /x")
	plain.End()
	ctx, wf := StartWorkflow(ctx, Workflow{Name: "w", RunID: "r"})
	_, node := StartNode(ctx, Node{ID: "n"})
	_, chat := StartChat(ctx, Chat{Provider: "openai", Model: "gpt-5"})
	End(chat, nil)
	End(node, nil)
	End(wf, nil)
	var names []string
	for _, s := range rec.Ended() {
		names = append(names, s.Name())
	}
	if len(names) != 3 {
		t.Fatalf("kept %v, want the workflow, node and chat spans", names)
	}
	for _, n := range names {
		if n == "GET /x" {
			t.Error("a plain span escaped the base sampler")
		}
	}
	if d := KeepSampler(sdktrace.NeverSample()).Description(); d != "KeepGenAI{AlwaysOffSampler}" {
		t.Errorf("description = %q", d)
	}
}

func TestWithScopeReplacesAKey(t *testing.T) {
	ctx := WithScope(context.Background(), attribute.String(AttrUserID, "a"), attribute.String(AttrOrgID, "o"))
	ctx = WithScope(ctx, attribute.String(AttrUserID, "b"))
	got := map[attribute.Key]string{}
	for _, kv := range Scope(ctx) {
		got[kv.Key] = kv.Value.Emit()
	}
	if len(Scope(ctx)) != 2 || got[AttrUserID] != "b" || got[AttrOrgID] != "o" {
		t.Errorf("scope = %v", Scope(ctx))
	}
}
