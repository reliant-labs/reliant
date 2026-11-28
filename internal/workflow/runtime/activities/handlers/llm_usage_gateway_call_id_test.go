// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/drivers/openai"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/llmusage/llmusagetest"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/observability/temporalobs"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/threads"
)

// llm_call_usage.gateway_call_id is the LLM gateway's id for a call
// (x-litellm-call-id), the id control-plane's wallet ledger keys the call's
// cost by (agent-analytics.md §3.2). The recorder takes it from where the
// gateway's driver records it on the call's span (llm.RecordGatewayCallID).

// gatewayCallIDs is each ledger row of chatID's gateway_call_id by the row's
// request: "" for the request that ended the call, "/request/N" for the Nth
// one the driver failed and retried. nil is NULL.
func gatewayCallIDs(t *testing.T, h *IdempotencyTestHelper, chatID string) map[string]*string {
	t.Helper()
	rows, err := h.DB().Query(`SELECT id, gateway_call_id FROM llm_call_usage WHERE chat_id = $1`, chatID)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]*string{}
	for rows.Next() {
		var id string
		var gw *string
		require.NoError(t, rows.Scan(&id, &gw))
		request := ""
		if i := strings.LastIndex(id, "/request/"); i >= 0 {
			request = id[i:]
		}
		out[request] = gw
	}
	require.NoError(t, rows.Err())
	return out
}

// A call through the gateway stores the gateway's id, the one its chat span
// carries as reliant.gateway.call_id, and not the upstream provider's request
// id: through the real Reliant driver behind the base driver that opens the
// span, answering the way the gateway does. Not parallel: it swaps the global
// tracer provider.
func TestUsageLedger_GatewayCallStoresTheSpansGatewayCallID(t *testing.T) {
	rec := recordHandlerSpans(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-oai-request-id", "req_upstream")
		w.Header().Set("x-litellm-call-id", "call-litellm-1")
		fmt.Fprintf(w, "data: "+terminalChunk+"\n\n", `"choices":[{"index":0,"delta":{"content":"summary"},"finish_reason":"stop"}]`)
		fmt.Fprintf(w, "data: "+terminalChunk+"\n\n", `"choices":[],"usage":{"prompt_tokens":40,"completion_tokens":7,"total_tokens":47,"cost":0.0042}`)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-gw-call", "user-gw-call")
	chat := h.CreateTestChat(ctx, "chat-gw-call", project.ID, project.UserID)

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

	var spanID string
	require.Eventually(t, func() bool {
		for _, s := range rec.Ended() {
			if s.Name() == "chat m" {
				spanID = attrsOf(s)[telemetry.AttrGatewayCallID]
				return true
			}
		}
		return false
	}, 5*time.Second, 5*time.Millisecond, "the call's gen_ai span")

	ids := gatewayCallIDs(t, h, chat.ID)
	require.Len(t, ids, 1)
	require.NotNil(t, ids[""])
	assert.Equal(t, "call-litellm-1", *ids[""])
	assert.Equal(t, spanID, *ids[""], "the ledger row and the span name the same gateway call")
	var providerRequestID string
	require.NoError(t, h.DB().QueryRow(`SELECT provider_request_id FROM llm_call_usage WHERE chat_id = $1`, chat.ID).Scan(&providerRequestID))
	assert.Equal(t, "req_upstream", providerRequestID, "provider_request_id stays the upstream provider's id")
}

// A call that does not go through the gateway stores NULL, even when its
// endpoint is a LiteLLM of the user's own that sends x-litellm-call-id: only
// Reliant's gateway driver records the id.
func TestUsageLedger_NonGatewayCallHasNoGatewayCallID(t *testing.T) {
	t.Parallel()
	const chunk = `{"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-4o",%s}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-litellm-call-id", "someone-elses-litellm")
		fmt.Fprintf(w, "data: "+chunk+"\n\n", `"choices":[{"index":0,"delta":{"content":"summary"},"finish_reason":"stop"}]`)
		fmt.Fprintf(w, "data: "+chunk+"\n\n", `"choices":[],"usage":{"prompt_tokens":40,"completion_tokens":7,"total_tokens":47}`)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-gw-none", "user-gw-none")
	chat := h.CreateTestChat(ctx, "chat-gw-none", project.ID, project.UserID)

	driver := openai.NewClient(llm.DriverOptions{Model: models.Model{ID: "gpt-4o", APIModel: "gpt-4o"}, ApiKey: "k", BaseURL: srv.URL, MaxTokens: 100})
	meterOnce(t, h, driver, project.UserID, chat.ID)

	ids := gatewayCallIDs(t, h, chat.ID)
	require.Len(t, ids, 1)
	require.Contains(t, ids, "")
	assert.Nil(t, ids[""], "a call outside the gateway has no gateway call id: NULL, not \"\"")
}

// gatewayLadderDriver answers the way the Reliant driver does behind the
// gateway when its first attempt is refused: every response, the refusal
// included, carries the gateway's id for that attempt (llm.RecordGatewayCallID),
// the refused attempt is announced as a retry, and the second attempt answers.
type gatewayLadderDriver struct{ pathDriver }

func (d *gatewayLadderDriver) StreamResponse(ctx context.Context, _ []string, _ []message.Message, _ []tools.Tool) <-chan llm.DriverEvent {
	ch := make(chan llm.DriverEvent, 3)
	llm.RecordGatewayCallID(ctx, "call-refused")
	ch <- llm.DriverEvent{Type: llm.EventRetryWait, Retry: &llm.RetryWait{Attempt: 1, StatusCode: 503, Reason: "http_503"}}
	llm.RecordGatewayCallID(ctx, "call-answered")
	ch <- llm.DriverEvent{Type: llm.EventContentDelta, Content: "summary"}
	ch <- llm.DriverEvent{Type: llm.EventComplete, Response: &llm.DriverResponse{Content: "summary", Usage: answeredUsage, FinishReason: message.FinishReasonEndTurn}}
	close(ch)
	return ch
}

// requireFinalAttemptsGatewayCallID asserts a retried call's rows: the request
// that ended it holds the id of the attempt that answered, the refused one
// none (it answered nothing, as its provider_request_id says).
func requireFinalAttemptsGatewayCallID(t *testing.T, ids map[string]*string) {
	t.Helper()
	require.Len(t, ids, 2, "the refused request and the answer are two rows")
	require.NotNil(t, ids[""])
	assert.Equal(t, "call-answered", *ids[""], "the call's row names its final attempt, as its span does")
	require.Contains(t, ids, "/request/1")
	assert.Nil(t, ids["/request/1"], "the refused request's row holds no gateway id")
}

// A call the driver retried stores the gateway's id for its final attempt,
// for a metered request (compaction, titles, drafts) ...
func TestUsageMeter_RetriedCallStoresItsFinalAttemptsGatewayCallID(t *testing.T) {
	t.Parallel()
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-gw-retry", "user-gw-retry")
	chat := h.CreateTestChat(ctx, "chat-gw-retry", project.ID, project.UserID)

	meterOnce(t, h, &gatewayLadderDriver{pathDriver{name: "reliant"}}, project.UserID, chat.ID)

	requireFinalAttemptsGatewayCallID(t, gatewayCallIDs(t, h, chat.ID))
}

// ... and for a chat turn (CallLLM).
func TestCallLLM_RetriedTurnStoresItsFinalAttemptsGatewayCallID(t *testing.T) {
	t.Parallel()
	d := &gatewayLadderDriver{pathDriver{name: "reliant"}}
	resolver := drivers.DriverResolver(func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
		return d, nil
	})
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	h.env.SetWorkerOptions(temporaltest.WorkerOptions(worker.Options{
		Interceptors: []interceptor.WorkerInterceptor{temporalobs.NewRunScopeWorkerInterceptor(nil)},
	}))
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-gw-turn", "user-gw-turn")
	chat := h.CreateTestChat(ctx, "chat-gw-turn", project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)
	callLLM := NewCallLLMActivity(CallLLMDeps{ToolResults: toolCallsOf(h.Repo()), Store: tools.WithChats(h.Repo()), Mailbox: store.NewAgentMessageExtStore(h.Repo().ORM()), Threads: threads.New(threads.Deps{Repo: h.Repo(), Messages: store.NewMessageExtStore(h.Repo().ORM()), Senders: store.NewThreadExtStore(h.Repo().ORM())}), Spawns: store.NewToolCallExtStore(h.Repo().ORM()), Hub: &captureHub{}, ConfigProvider: &staticConfigProvider{}, DriverResolver: resolver}).
		WithUsageRecorder(llmusagetest.New(t, h.Repo()))

	h.env.RegisterActivity(callLLM.Execute)
	_, err := h.env.ExecuteActivity(callLLM.Execute, callLLMInput(chat.ID, chat.ID, "mock-model"))
	require.NoError(t, err)

	requireFinalAttemptsGatewayCallID(t, gatewayCallIDs(t, h, chat.ID))
}

// The gateway's id is the last answered attempt's, read safely while the
// driver's goroutine records it; a call made without asking for it (no
// GatewayCall on its ctx) records nothing anywhere.
func TestDriverRetries_GatewayCallID(t *testing.T) {
	t.Parallel()
	var none *driverRetries
	assert.Empty(t, none.gatewayCallID())

	r := newDriverRetriesAt(func() time.Time { return time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC) })
	assert.Empty(t, r.gatewayCallID(), "nothing answered yet")
	ctx := r.watch(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		llm.RecordGatewayCallID(ctx, "call-1")
		llm.RecordGatewayCallID(ctx, "") // a response with no header keeps the last id
		llm.RecordGatewayCallID(ctx, "call-2")
	}()
	<-done
	assert.Equal(t, "call-2", r.gatewayCallID())

	llm.RecordGatewayCallID(context.Background(), "call-unwatched")
	assert.Equal(t, "call-2", r.gatewayCallID(), "another call's id is not this call's")
}
