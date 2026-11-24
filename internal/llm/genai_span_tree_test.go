// Copyright (c) 2025 Reliant Labs
package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	forgegenai "github.com/reliant-labs/forge/pkg/observe/genai"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/telemetry"
)

// updateGolden rewrites testdata/genai_span_tree.golden.json from the current
// code: `go test ./internal/llm -run TestGenAISpanTreeGolden -update`. The span
// and attribute names are a public contract (dashboards, and tenant backends,
// read them), so a diff to the golden file is a contract change to review,
// never a file to regenerate to make the test pass.
var updateGolden = flag.Bool("update", false, "rewrite the golden span tree")

// goldenSpan is what the contract fixes about one span: its name, kind,
// parent, attribute keys (with their types), events, links and status. Values
// are left out: they are the run's, not the contract's.
type goldenSpan struct {
	Name   string        `json:"name"`
	Kind   string        `json:"kind"`
	Parent string        `json:"parent"`
	Attrs  []string      `json:"attrs"`
	Events []goldenEvent `json:"events,omitempty"`
	Links  int           `json:"links,omitempty"`
	Status string        `json:"status"`
}

type goldenEvent struct {
	Name  string   `json:"name"`
	Attrs []string `json:"attrs,omitempty"`
}

// attrKeys is nil for no attributes, as an event without any reads back from
// the golden file.
func attrKeys(kvs []attribute.KeyValue) []string {
	var out []string
	for _, kv := range kvs {
		out = append(out, string(kv.Key)+":"+kv.Value.Type().String())
	}
	sort.Strings(out)
	return out
}

func attrMap(s sdktrace.ReadOnlySpan) map[attribute.Key]attribute.Value {
	out := map[attribute.Key]attribute.Value{}
	for _, kv := range s.Attributes() {
		out[kv.Key] = kv.Value
	}
	return out
}

// scriptedRunScope is a spawned agent's run, in a loop, in an org: every
// field of the scope set, so every attribute a span can carry is on it.
func scriptedRunScope() telemetry.RunScope {
	return telemetry.RunScope{
		UserID: "user-1", ChatID: "chat-1", ThreadID: "thread-1", WorkflowID: "wf-1", OrgID: "org-1",
		WorkflowName: "agent", WorkflowRef: "builtin://agent", Trigger: "spawn",
		RunID: "run-2", ParentRunID: "run-1", RootRunID: "run-1", Preset: "general",
	}
}

// recordScriptedTurn records the span tree of one scripted agent turn the way
// a worker emits it: the call_llm node's span, the turn under it, a streamed
// model call that retried twice (an HTTP status, then a reset connection with
// none) and reported its cost, a model call that
// failed, a turn the user cancelled, then the execute_tools node's span with
// a tool call that reported an error and one that failed to run. Both nodes
// sit under one stand-in for the Temporal workflow span, as on a real run.
func recordScriptedTurn(t *testing.T) []sdktrace.ReadOnlySpan {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	ctx, run := otel.Tracer("test").Start(context.Background(), "RunWorkflow:DynamicWorkflow")
	ctx = telemetry.WithRunScope(ctx, scriptedRunScope())

	llmCtx := telemetry.WithRunScope(ctx, telemetry.RunScope{NodeID: "call_llm", NodeType: "call_llm", NodeIteration: 3, InLoop: true})
	nodeCtx, node := telemetry.StartNodeSpan(llmCtx)
	turnCtx, turn := telemetry.StartAgentTurn(nodeCtx)

	_, call := llm.StartGenAISpan(turnCtx, "anthropic", models.Model{ID: "claude-x", APIModel: "claude-x-api"}, true)
	call.Observe(llm.DriverEvent{Type: llm.EventRetryWait, Retry: &llm.RetryWait{Attempt: 1, MaxAttempts: 8, Delay: 2 * time.Second, StatusCode: 529, Reason: "overloaded"}})
	call.Observe(llm.DriverEvent{Type: llm.EventRetryWait, Retry: &llm.RetryWait{Attempt: 2, MaxAttempts: 8, Delay: 4 * time.Second, Reason: "connection_error"}})
	call.Observe(llm.DriverEvent{Type: llm.EventContentDelta, Content: "hi"})
	call.Observe(llm.DriverEvent{Type: llm.EventContentDelta, Content: " there"})
	call.Observe(llm.DriverEvent{Type: llm.EventComplete, Model: models.Model{APIModel: "claude-x-served"}, Response: &llm.DriverResponse{
		Usage: llm.TokenUsage{
			InputTokens: 1200, OutputTokens: 300, ReasoningTokens: 40,
			CacheReadInputTokens: 1000, CacheCreationInputTokens: 100, Cost: 0.25,
		},
		FinishReason:      message.FinishReasonToolUse,
		UpstreamRequestID: "msg_123",
		ToolCalls:         []message.ToolCall{{ID: "call-1", Name: "grep"}},
	}})
	call.End()

	_, failed := llm.StartGenAISpan(turnCtx, "openai", models.Model{ID: "gpt-x"}, false)
	failed.Observe(llm.DriverEvent{Type: llm.EventError, Error: errors.New("provider said no")})
	failed.End()

	turnTraceParent := turn.TraceParent()
	turn.End(nil)
	telemetry.EndAISpan(node, nil)

	_, canceled := telemetry.StartAgentTurn(nodeCtx)
	canceled.End(context.Canceled)

	toolsCtx := telemetry.WithRunScope(ctx, telemetry.RunScope{NodeID: "execute_tools", NodeType: "execute_tools"})
	toolsCtx, toolsNode := telemetry.StartNodeSpan(toolsCtx)
	_, grep := telemetry.StartToolCall(toolsCtx, "grep", "call-1", telemetry.ToolLocationDaemon, turnTraceParent)
	grep.End(true, nil)
	_, shell := telemetry.StartToolCall(toolsCtx, "shell", "call-2", telemetry.ToolLocationLocal, "")
	shell.End(false, errors.New("daemon unreachable"))
	telemetry.EndAISpan(toolsNode, nil)

	run.End()
	return rec.Ended()
}

func goldenTree(spans []sdktrace.ReadOnlySpan) []goldenSpan {
	names := map[trace.SpanID]string{}
	for _, s := range spans {
		names[s.SpanContext().SpanID()] = s.Name()
	}
	out := make([]goldenSpan, 0, len(spans))
	for _, s := range spans {
		g := goldenSpan{
			Name:   s.Name(),
			Kind:   s.SpanKind().String(),
			Parent: names[s.Parent().SpanID()],
			Attrs:  append([]string{}, attrKeys(s.Attributes())...),
			Links:  len(s.Links()),
			Status: fmt.Sprintf("%s %s", s.Status().Code, s.Status().Description),
		}
		for _, ev := range s.Events() {
			g.Events = append(g.Events, goldenEvent{Name: ev.Name, Attrs: attrKeys(ev.Attributes)})
		}
		out = append(out, g)
	}
	return out
}

// knownDrops are the attribute keys the golden file has that the span tree no
// longer carries, each a deliberate, reviewed change: span name, event index
// (-1 for the span's own attributes) and key.
//
//   - A retry with no HTTP status (a reset connection) said
//     http.response.status_code=0; forge's genai.RetryWait leaves it out. Every
//     reader treats a missing key and "0" alike (the Backoffs panel of
//     deploy/observability/spec/project_gen.json: NOT IN (”, '0')).
var knownDrops = []struct {
	span  string
	event int
	key   string
}{
	{"chat claude-x-api", 1, "http.response.status_code:INT64"},
}

func withoutKnownDrops(spans []goldenSpan) []goldenSpan {
	drop := func(keys []string, key string) []string {
		out := keys[:0]
		for _, k := range keys {
			if k != key {
				out = append(out, k)
			}
		}
		return out
	}
	for _, d := range knownDrops {
		for i := range spans {
			if spans[i].Name != d.span {
				continue
			}
			if d.event < 0 {
				spans[i].Attrs = drop(spans[i].Attrs, d.key)
			} else if d.event < len(spans[i].Events) {
				spans[i].Events[d.event].Attrs = drop(spans[i].Events[d.event].Attrs, d.key)
			}
		}
	}
	return spans
}

// The span tree of a scripted turn is the one recorded before reliant adopted
// forge's pkg/observe/genai (testdata/genai_span_tree.golden.json): the same
// span names, kinds, parents, attribute keys, events, links and statuses, but
// for knownDrops. A key lost is a dashboard that stops drawing.
func TestGenAISpanTreeGolden(t *testing.T) {
	got := goldenTree(recordScriptedTurn(t))

	path := filepath.Join("testdata", "genai_span_tree.golden.json")
	if *updateGolden {
		out, err := json.MarshalIndent(got, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, append(out, '\n'), 0o644))
		return
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var want []goldenSpan
	require.NoError(t, json.Unmarshal(raw, &want))
	assert.Equal(t, withoutKnownDrops(want), got)
}

// Every AI span — the node, the agent turn, the model call and the tool call,
// failed and cancelled ones too — carries the org it was for: tenant export
// routes a span by org.id and drops one without it.
func TestGenAISpanTree_EveryAISpanCarriesOrgID(t *testing.T) {
	kinds := map[string]bool{}
	for _, s := range recordScriptedTurn(t) {
		if !forgegenai.IsAISpan(s.Attributes()) {
			continue
		}
		attrs := attrMap(s)
		assert.Equal(t, "org-1", attrs[telemetry.AttrOrgID].AsString(), "span %q has no org.id", s.Name())
		kind := attrs[telemetry.AttrGenAIOperationName].AsString()
		if kind == "" {
			kind = telemetry.SpanNameNode
		}
		kinds[kind] = true
	}
	assert.Equal(t, map[string]bool{
		telemetry.SpanNameNode:       true,
		telemetry.GenAIOpInvokeAgent: true,
		telemetry.GenAIOpChat:        true,
		telemetry.GenAIOpExecuteTool: true,
	}, kinds, "the scripted turn covers every kind of AI span a worker opens")
}

// A reported cost is on the model call's span under forge's name and under
// reliant.cost_usd, the name reliant reported it under first, with the same
// value; a call with no reported cost has neither.
func TestGenAISpanTree_CostUnderBothNames(t *testing.T) {
	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range recordScriptedTurn(t) {
		byName[s.Name()] = s
	}
	priced := attrMap(byName["chat claude-x-api"])
	assert.InDelta(t, 0.25, priced[telemetry.AttrAICostUSD].AsFloat64(), 1e-9)
	assert.InDelta(t, 0.25, priced[telemetry.AttrReliantCostUSD].AsFloat64(), 1e-9)

	unpriced := attrMap(byName["chat gpt-x"])
	assert.NotContains(t, unpriced, attribute.Key(telemetry.AttrAICostUSD))
	assert.NotContains(t, unpriced, attribute.Key(telemetry.AttrReliantCostUSD))
}
