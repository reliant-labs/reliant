// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/llmusage/llmusagetest"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/observability/temporalobs"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/threads"
	activitytypes "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// scopeDriver answers every request with one scripted reply: optionally a
// first output token, then the final event carrying the provider's request id
// and the cost the gateway reported.
type scopeDriver struct {
	name      string
	token     bool
	requestID string
	cost      float64
}

func (d *scopeDriver) Name() string                          { return d.name }
func (d *scopeDriver) Model() models.Model                   { return models.Model{ID: "mock-model"} }
func (d *scopeDriver) ValidateKey(ctx context.Context) error { return nil }
func (d *scopeDriver) SendMessages(context.Context, []string, []message.Message, []tools.Tool) (*llm.DriverResponse, error) {
	return nil, fmt.Errorf("not used")
}

func (d *scopeDriver) StreamResponse(context.Context, []string, []message.Message, []tools.Tool) <-chan llm.DriverEvent {
	ch := make(chan llm.DriverEvent, 3)
	defer close(ch)
	if d.token {
		ch <- llm.DriverEvent{Type: llm.EventContentDelta, Content: "Summary"}
	}
	ch <- llm.DriverEvent{Type: llm.EventComplete, Response: &llm.DriverResponse{
		Content: "Summary", FinishReason: message.FinishReasonEndTurn, UpstreamRequestID: d.requestID,
		Usage: llm.TokenUsage{InputTokens: 40, OutputTokens: 7, Cost: d.cost},
	}}
	return ch
}

// scopedNodeInput is a workflow node's activity input as the runtime
// dispatches it, the shape the worker's run-scope interceptor reads its run
// from (temporalobs.RunScopeOfActivityArgs).
type scopedNodeInput struct {
	Runtime struct {
		ChatID     string                      `json:"chat_id"`
		Thread     string                      `json:"thread"`
		WorkflowID string                      `json:"workflow_id"`
		StepID     string                      `json:"step_id"`
		Telemetry  *activitytypes.RunTelemetry `json:"telemetry"`
	} `json:"runtime"`
	Node struct {
		Type string `json:"type"`
	} `json:"node"`
}

// runScopedActivity runs fn as an activity on a worker that has the
// production run-scope interceptor, so the ctx fn sees carries the run exactly
// as a worker's would.
func runScopedActivity(t *testing.T, in scopedNodeInput, fn func(ctx context.Context) error) {
	t.Helper()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.SetWorkerOptions(temporaltest.WorkerOptions(worker.Options{
		Interceptors: []interceptor.WorkerInterceptor{temporalobs.NewRunScopeWorkerInterceptor(nil)},
	}))
	exec := func(ctx context.Context, _ scopedNodeInput) error { return fn(ctx) }
	env.RegisterActivityWithOptions(exec, activity.RegisterOptions{Name: "ScopedLedgerNode"})
	_, err := env.ExecuteActivity("ScopedLedgerNode", in)
	require.NoError(t, err)
}

// scopeRow is a ledger row's run-scope columns; nil is NULL.
type scopeRow struct {
	Provider, AuthMode                                 string
	WorkflowID, ThreadID, WorkflowName, NodeID, Preset *string
	ProjectID, AIRunID, AIRootRunID, ProviderRequestID *string
	CostBasis                                          *string
	TTFTMs, CostUSDNanos                               *int64
}

func scopeRows(t *testing.T, db *sql.DB, chatID string) []scopeRow {
	t.Helper()
	rows, err := db.Query(`SELECT provider, auth_mode, workflow_id, thread_id, workflow_name, node_id, preset, project_id,
		ai_run_id, ai_root_run_id, provider_request_id, cost_basis, ttft_ms, cost_usd_nanos
		FROM llm_call_usage WHERE chat_id = $1 ORDER BY id`, chatID)
	require.NoError(t, err)
	defer rows.Close()
	var out []scopeRow
	for rows.Next() {
		var r scopeRow
		require.NoError(t, rows.Scan(&r.Provider, &r.AuthMode, &r.WorkflowID, &r.ThreadID, &r.WorkflowName, &r.NodeID,
			&r.Preset, &r.ProjectID, &r.AIRunID, &r.AIRootRunID, &r.ProviderRequestID, &r.CostBasis, &r.TTFTMs, &r.CostUSDNanos))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

func strOrNil(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// A model call's ledger row names the run it was part of, from the scope the
// worker puts on the activity (agent-analytics.md §3.2), and what it cost
// (§4). Three requests of one sub-agent's node: a managed call the gateway
// priced, a subscription call (no per-call price) and a call on the person's
// own key that nothing priced. One subtest per column.
func TestUsageLedger_RecordsTheRunScopeOfEachCall(t *testing.T) {
	t.Parallel()
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-run-scope", "user-run-scope")
	chat := h.CreateTestChat(ctx, "chat-run-scope", project.ID, project.UserID)

	var in scopedNodeInput
	in.Runtime.ChatID, in.Runtime.Thread, in.Runtime.WorkflowID, in.Runtime.StepID = chat.ID, "thread-sub", "wf-sub", "summarize"
	in.Runtime.Telemetry = &activitytypes.RunTelemetry{
		WorkflowName: "research", RunID: "run-sub", ParentRunID: "run-parent", RootRunID: "run-root", Preset: "researcher",
	}
	in.Node.Type = "call_llm"

	managed := &scopeDriver{name: "reliant", token: true, requestID: "req_managed", cost: 0.0123}
	subscription := &scopeDriver{name: "claude", token: true, requestID: "req_sub"}
	byok := &scopeDriver{name: "anthropic"}
	runScopedActivity(t, in, func(ctx context.Context) error {
		meter := newUsageMeter(llmusagetest.New(t, h.Repo()), project.UserID, chat.ID, store.LLMPurposeCompaction).inProject(project.ID)
		for _, d := range []*scopeDriver{managed, subscription, byok} {
			if _, err := meter.streamAndAccumulate(ctx, &resolvedLLMCall{Driver: d, Model: d.Model()}, nil, nil, nil); err != nil {
				return err
			}
		}
		return nil
	})

	rows := scopeRows(t, h.DB(), chat.ID)
	require.Len(t, rows, 3, "one row per request, ordinals 1..3")
	require.Equal(t, []string{"reliant", "claude", "anthropic"}, []string{rows[0].Provider, rows[1].Provider, rows[2].Provider})
	each := func(t *testing.T, col func(scopeRow) *string, want string) {
		t.Helper()
		for i, r := range rows {
			assert.Equal(t, want, strOrNil(col(r)), "row %d (%s)", i, r.Provider)
		}
	}

	t.Run("workflow_id", func(t *testing.T) { each(t, func(r scopeRow) *string { return r.WorkflowID }, "wf-sub") })
	t.Run("thread_id", func(t *testing.T) { each(t, func(r scopeRow) *string { return r.ThreadID }, "thread-sub") })
	t.Run("workflow_name", func(t *testing.T) { each(t, func(r scopeRow) *string { return r.WorkflowName }, "research") })
	t.Run("node_id", func(t *testing.T) { each(t, func(r scopeRow) *string { return r.NodeID }, "summarize") })
	t.Run("preset", func(t *testing.T) { each(t, func(r scopeRow) *string { return r.Preset }, "researcher") })
	t.Run("project_id", func(t *testing.T) { each(t, func(r scopeRow) *string { return r.ProjectID }, project.ID) })
	t.Run("ai_run_id", func(t *testing.T) {
		// forge.ai.run.id, the sub-agent's own run, not Temporal's run id.
		each(t, func(r scopeRow) *string { return r.AIRunID }, "run-sub")
	})
	t.Run("ai_root_run_id", func(t *testing.T) {
		// The top of the spawn chain: what step_executions.run_id holds.
		each(t, func(r scopeRow) *string { return r.AIRootRunID }, "run-root")
	})
	t.Run("provider_request_id", func(t *testing.T) {
		assert.Equal(t, "req_managed", strOrNil(rows[0].ProviderRequestID))
		assert.Equal(t, "req_sub", strOrNil(rows[1].ProviderRequestID))
		assert.Nil(t, rows[2].ProviderRequestID, "a provider that sends no id records NULL, not \"\"")
	})
	t.Run("ttft_ms", func(t *testing.T) {
		require.NotNil(t, rows[0].TTFTMs, "a request that streamed a token has a time to it")
		assert.GreaterOrEqual(t, *rows[0].TTFTMs, int64(0))
		require.NotNil(t, rows[1].TTFTMs)
		assert.Nil(t, rows[2].TTFTMs, "a request that streamed no token has no time to first token, not 0 ms")
	})
	t.Run("cost_usd_nanos", func(t *testing.T) {
		require.NotNil(t, rows[0].CostUSDNanos)
		assert.EqualValues(t, 12_300_000, *rows[0].CostUSDNanos, "$0.0123 reported by the gateway")
		assert.Nil(t, rows[1].CostUSDNanos, "a subscription call has no per-call price: NULL, never $0")
		assert.Nil(t, rows[2].CostUSDNanos, "a call nothing priced is unknown: NULL, never $0")
	})
	t.Run("cost_basis", func(t *testing.T) {
		assert.Equal(t, store.LLMCostBasisReported, strOrNil(rows[0].CostBasis))
		assert.Equal(t, store.LLMCostBasisUnpriced, strOrNil(rows[1].CostBasis))
		assert.Nil(t, rows[2].CostBasis, "not priced yet: no basis")
	})
}

// A chat turn (CallLLM) records the same run scope as the metered requests:
// its chat's project, the turn's preset, the run and node it ran in, the
// provider's request id and the time to the first token.
func TestCallLLM_LedgerRowCarriesTheTurnsRunScope(t *testing.T) {
	t.Parallel()
	d := &scopeDriver{name: "reliant", token: true, requestID: "req_turn", cost: 0.5}
	resolver := drivers.DriverResolver(func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
		return d, nil
	})
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	h.env.SetWorkerOptions(temporaltest.WorkerOptions(worker.Options{
		Interceptors: []interceptor.WorkerInterceptor{temporalobs.NewRunScopeWorkerInterceptor(nil)},
	}))
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-turn-scope", "user-turn-scope")
	chat := h.CreateTestChat(ctx, "chat-turn-scope", project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)
	callLLM := NewCallLLMActivity(CallLLMDeps{ToolResults: toolCallsOf(h.Repo()), Store: tools.WithChats(h.Repo()), Threads: threads.New(threads.Deps{Repo: h.Repo(), Messages: store.NewMessageExtStore(h.Repo().ORM()), Senders: store.NewThreadExtStore(h.Repo().ORM())}), Spawns: store.NewToolCallExtStore(h.Repo().ORM()), Mailbox: store.NewAgentMessageExtStore(h.Repo().ORM()), Hub: &captureHub{}, ConfigProvider: &staticConfigProvider{}, DriverResolver: resolver}).
		WithUsageRecorder(llmusagetest.New(t, h.Repo()))

	input := callLLMInput(chat.ID, chat.ID, "mock-model")
	input.Runtime.WorkflowID, input.Runtime.StepID = "wf-turn", "agent_turn"
	input.Runtime.Telemetry = &activitytypes.RunTelemetry{WorkflowName: "agent", RunID: "run-turn", Preset: "general"}
	h.env.RegisterActivity(callLLM.Execute)
	_, err := h.env.ExecuteActivity(callLLM.Execute, input)
	require.NoError(t, err)

	rows := scopeRows(t, h.DB(), chat.ID)
	require.Len(t, rows, 1)
	r := rows[0]
	assert.Equal(t, "wf-turn", strOrNil(r.WorkflowID))
	assert.Equal(t, chat.ID, strOrNil(r.ThreadID))
	assert.Equal(t, "agent", strOrNil(r.WorkflowName))
	assert.Equal(t, "agent_turn", strOrNil(r.NodeID))
	assert.Equal(t, "general", strOrNil(r.Preset))
	assert.Equal(t, project.ID, strOrNil(r.ProjectID))
	assert.Equal(t, "run-turn", strOrNil(r.AIRunID))
	assert.Equal(t, "run-turn", strOrNil(r.AIRootRunID), "a top-level run is its own root")
	assert.Equal(t, "req_turn", strOrNil(r.ProviderRequestID))
	assert.NotNil(t, r.TTFTMs)
	require.NotNil(t, r.CostUSDNanos)
	assert.EqualValues(t, 500_000_000, *r.CostUSDNanos)
	assert.Equal(t, store.LLMCostBasisReported, strOrNil(r.CostBasis))
}

// provider_request_id is the id the call's span carries as gen_ai.response.id,
// and the cost is the one the span carries as forge.ai.cost.usd: the row and
// the span are joined exactly (agent-analytics.md §3.2). Through the real
// Reliant driver behind the base driver that opens the span, answering the way
// the gateway does: the request id in x-oai-request-id, the cost in the stream's
// usage chunk. Not parallel: it swaps the global tracer provider.
func TestUsageLedger_ProviderRequestIDIsTheSpansResponseID(t *testing.T) {
	rec := recordHandlerSpans(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-oai-request-id", "req_7f3a")
		fmt.Fprintf(w, "data: "+terminalChunk+"\n\n", `"choices":[{"index":0,"delta":{"content":"summary"},"finish_reason":"stop"}]`)
		fmt.Fprintf(w, "data: "+terminalChunk+"\n\n", `"choices":[],"usage":{"prompt_tokens":40,"completion_tokens":7,"total_tokens":47,"cost":0.0042}`)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-span-join", "user-span-join")
	chat := h.CreateTestChat(ctx, "chat-span-join", project.ID, project.UserID)

	driver, err := drivers.GetDriverForModel(models.Model{ID: "claude-4.5-haiku", APIModel: "m"}, "reliant",
		llm.WithAPIKey("k"), llm.WithBaseURL(srv.URL), llm.WithMaxTokens(100))
	require.NoError(t, err)
	var in scopedNodeInput
	in.Runtime.ChatID, in.Runtime.Thread = chat.ID, chat.ID
	runScopedActivity(t, in, func(ctx context.Context) error {
		_, err := newUsageMeter(llmusagetest.New(t, h.Repo()), project.UserID, chat.ID, store.LLMPurposeTitle).
			streamAndAccumulate(ctx, &resolvedLLMCall{Driver: driver, Model: driver.Model()}, nil, nil, nil)
		return err
	})

	rows := scopeRows(t, h.DB(), chat.ID)
	require.Len(t, rows, 1)
	var call map[string]string
	require.Eventually(t, func() bool {
		for _, s := range rec.Ended() {
			if s.Name() == "chat m" {
				call = map[string]string{}
				for k, v := range attrsOf(s) {
					call[string(k)] = v
				}
				return true
			}
		}
		return false
	}, 5*time.Second, 5*time.Millisecond, "the call's gen_ai span")

	require.NotNil(t, rows[0].ProviderRequestID)
	assert.Equal(t, "req_7f3a", *rows[0].ProviderRequestID)
	assert.Equal(t, call["gen_ai.response.id"], *rows[0].ProviderRequestID,
		"the ledger row and the span name the same provider request")
	require.NotNil(t, rows[0].CostUSDNanos)
	assert.EqualValues(t, 4_200_000, *rows[0].CostUSDNanos)
	assert.Equal(t, "0.0042", call[telemetry.AttrAICostUSD], "the span's cost is the row's")
	assert.Equal(t, store.LLMCostBasisReported, strOrNil(rows[0].CostBasis))
}

// Time to first token is the answering request's: from its start, after the
// last retry's wait, to its first output delta. A token a failed request
// streamed before it was retried is not the answer's; no token is no time.
func TestDriverRetries_TimeToFirstToken(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	clock := t0
	now := func() time.Time { return clock }

	r := newDriverRetriesAt(now)
	assert.Nil(t, r.ttftMs(), "nothing streamed yet")

	clock = t0.Add(80 * time.Millisecond)
	r.observeEvent(llm.DriverEvent{Type: llm.EventContentDelta, Content: "par"}) // the failed request's token
	clock = t0.Add(100 * time.Millisecond)
	r.observeEvent(llm.DriverEvent{Type: llm.EventRetryWait, Retry: &llm.RetryWait{Attempt: 1, StatusCode: 529, Delay: 50 * time.Millisecond}})
	assert.Nil(t, r.ttftMs(), "the failed request's token is not the answer's")

	clock = t0.Add(300 * time.Millisecond)
	r.observeEvent(llm.DriverEvent{Type: llm.EventToolUseStart}) // framing, not an output delta
	r.observeEvent(llm.DriverEvent{Type: llm.EventContentDelta})
	r.observeEvent(llm.DriverEvent{Type: llm.EventThinkingDelta})
	assert.Nil(t, r.ttftMs(), "empty deltas before the real token are framing")
	clock = t0.Add(400 * time.Millisecond)
	r.observeEvent(llm.DriverEvent{Type: llm.EventThinkingDelta, Thinking: "hmm"})
	clock = t0.Add(900 * time.Millisecond)
	r.observeEvent(llm.DriverEvent{Type: llm.EventContentDelta, Content: "a later token"})

	got := r.ttftMs()
	require.NotNil(t, got)
	assert.EqualValues(t, 250, *got, "from 150ms (100ms + the 50ms wait) to the first delta at 400ms")
	require.Len(t, r.list, 1, "the retry is still its own provider request")

	emptyOnly := newDriverRetriesAt(now)
	clock = clock.Add(100 * time.Millisecond)
	emptyOnly.observeEvent(llm.DriverEvent{Type: llm.EventContentDelta})
	emptyOnly.observeEvent(llm.DriverEvent{Type: llm.EventThinkingDelta})
	emptyOnly.finish()
	assert.Nil(t, emptyOnly.ttftMs(), "an empty-only stream produced no output token")
}

// What the ledger records of a call's cost: the reported cost in nanos, else
// "unpriced" for a subscription or local model, else nothing. A reported 0 is
// the gateway not pricing the call, never a free one.
func TestLLMUsageCost(t *testing.T) {
	t.Parallel()
	nanos := func(v int64) *int64 { return &v }
	for _, tc := range []struct {
		name      string
		authMode  string
		cost      float64
		wantNanos *int64
		wantBasis string
	}{
		{"managed, priced", store.LLMAuthModeManaged, 0.0123, nanos(12_300_000), store.LLMCostBasisReported},
		{"rounded to the nano", store.LLMAuthModeManaged, 1.0000000004, nanos(1_000_000_000), store.LLMCostBasisReported},
		{"managed, not priced", store.LLMAuthModeManaged, 0, nil, ""},
		{"own key", store.LLMAuthModeBYOK, 0, nil, ""},
		{"subscription", store.LLMAuthModeSubscription, 0, nil, store.LLMCostBasisUnpriced},
		{"local", store.LLMAuthModeLocal, 0, nil, store.LLMCostBasisUnpriced},
		{"negative", store.LLMAuthModeManaged, -1, nil, ""},
		{"NaN", store.LLMAuthModeManaged, math.NaN(), nil, ""},
		{"overflow", store.LLMAuthModeManaged, math.Inf(1), nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotNanos, gotBasis := llmUsageCost(tc.authMode, tc.cost)
			assert.Equal(t, tc.wantNanos, gotNanos)
			assert.Equal(t, tc.wantBasis, gotBasis)
		})
	}
}
