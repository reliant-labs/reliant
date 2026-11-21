// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/openai"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// ladderDriver retries inside one StreamResponse the way every driver's
// backoff ladder does: a 429 with nothing streamed, then a stream that reported
// usage and dropped, then the answer. Each wait is announced before the retry.
type ladderDriver struct{ pathDriver }

var answeredUsage = llm.TokenUsage{InputTokens: 40, OutputTokens: 7}

func (d *ladderDriver) StreamResponse(context.Context, []string, []message.Message, []tools.Tool) <-chan llm.DriverEvent {
	ch := make(chan llm.DriverEvent, 4)
	billed := failedUsage
	ch <- llm.DriverEvent{Type: llm.EventRetryWait, Retry: &llm.RetryWait{Attempt: 1, StatusCode: 429, Reason: "http_429"}}
	ch <- llm.DriverEvent{Type: llm.EventRetryWait, Retry: &llm.RetryWait{Attempt: 2, Reason: "connection_error", Usage: &billed}}
	ch <- llm.DriverEvent{Type: llm.EventContentDelta, Content: "summary"}
	ch <- llm.DriverEvent{Type: llm.EventComplete, Response: &llm.DriverResponse{Content: "summary", Usage: answeredUsage, FinishReason: message.FinishReasonEndTurn}}
	close(ch)
	return ch
}

func meterOnce(t *testing.T, h *IdempotencyTestHelper, driver llm.Driver, userID, chatID string) {
	t.Helper()
	resolved := &resolvedLLMCall{Driver: driver, Model: driver.Model()}
	exec := func(ctx context.Context, _ string) (string, error) {
		_, err := newUsageMeter(h.Repo(), userID, chatID, "compaction").streamAndAccumulate(ctx, resolved, nil, nil, nil)
		return "", err
	}
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivityWithOptions(exec, activity.RegisterOptions{Name: "Metered"})
	_, err := env.ExecuteActivity("Metered", "x")
	require.NoError(t, err)
}

// The reviewer's case: a driver that retries inside one call made one provider
// request per rung of its ladder, and each is its own ledger row: a 429 as a
// rate_limit error, a dropped stream as a network error holding the tokens it
// was billed, and the answer. The rollup counts three calls, two of them
// errors, and every token.
func TestUsageMeter_DriverRetriesAreProviderRequestsOfTheirOwn(t *testing.T) {
	t.Parallel()
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-ladder", "user-ladder")
	chat := h.CreateTestChat(ctx, "chat-ladder", project.ID, project.UserID)

	meterOnce(t, h, &ladderDriver{pathDriver{name: "anthropic"}}, project.UserID, chat.ID)

	rows := ledgerRows(t, h, "compaction")
	require.Len(t, rows, 3, "%+v", rows)
	byID := map[string]ledgerPathRow{}
	for _, r := range rows {
		byID[r.ID[strings.LastIndex(r.ID, "/compaction/"):]] = r
	}
	limited, dropped, answered := byID["/compaction/1/request/1"], byID["/compaction/1/request/2"], byID["/compaction/1"]
	assert.Equal(t, [2]string{"error", "rate_limit"}, [2]string{limited.Outcome, limited.ErrorClass})
	assert.EqualValues(t, 0, limited.In)
	assert.Equal(t, [2]string{"error", "network"}, [2]string{dropped.Outcome, dropped.ErrorClass})
	assert.Equal(t, [4]int64{500, 120, 100, 20}, [4]int64{dropped.In, dropped.Out, dropped.CacheRead, dropped.CacheWrite},
		"the dropped stream's row holds the tokens it was billed")
	assert.Equal(t, [2]string{"success", ""}, [2]string{answered.Outcome, answered.ErrorClass})
	assert.Equal(t, [2]int64{40, 7}, [2]int64{answered.In, answered.Out})

	calls, errCalls, in := dailyCalls(t, h)
	assert.EqualValues(t, 3, calls)
	assert.EqualValues(t, 2, errCalls)
	assert.EqualValues(t, 540, in)
}

// After Chat Completions delivers a chunk, the driver must not replay its
// deltas. It reports the failure and the billed usage, and the activity retry
// owns the second provider request. The ledger records both attempts.
func TestUsageMeter_RealStreamFailureKeepsUsageAcrossActivityRetry(t *testing.T) {
	t.Parallel()
	const chunk = `{"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-4o",%s}`
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if hits.Add(1) == 1 {
			fmt.Fprintf(w, "data: "+chunk+"\n\n", `"choices":[{"index":0,"delta":{"content":"par"}}]`)
			fmt.Fprintf(w, "data: "+chunk+"\n\n", `"choices":[],"usage":{"prompt_tokens":500,"completion_tokens":120,"total_tokens":620}`)
			fmt.Fprint(w, "data: {\"error\":{\"message\":\"upstream reset\",\"type\":\"server_error\"}}\n\n")
			return
		}
		fmt.Fprintf(w, "data: "+chunk+"\n\n", `"choices":[{"index":0,"delta":{"content":"summary"},"finish_reason":"stop"}]`)
		fmt.Fprintf(w, "data: "+chunk+"\n\n", `"choices":[],"usage":{"prompt_tokens":40,"completion_tokens":7,"total_tokens":47}`)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-ladder-real", "user-ladder-real")
	chat := h.CreateTestChat(ctx, "chat-ladder-real", project.ID, project.UserID)

	driver := openai.NewClient(llm.DriverOptions{Model: models.Model{ID: "gpt-4o", APIModel: "gpt-4o"}, ApiKey: "k", BaseURL: srv.URL, MaxTokens: 100})
	resolved := &resolvedLLMCall{Driver: driver, Model: driver.Model()}
	runRetried(t, "MeteredStream", func(ctx context.Context, _ string) error {
		_, err := newUsageMeter(h.Repo(), project.UserID, chat.ID, "compaction").streamAndAccumulate(ctx, resolved, nil, nil, nil)
		return err
	}, "x")

	require.EqualValues(t, 2, hits.Load())
	rows := ledgerRows(t, h, "compaction")
	require.Len(t, rows, 2, "%+v", rows)
	byAttempt := map[int]ledgerPathRow{}
	for _, r := range rows {
		byAttempt[r.Attempt] = r
	}
	require.Contains(t, byAttempt, 1, "the first stream failed back to Temporal")
	require.Contains(t, byAttempt, 2, "the successful request was a new activity attempt, not a driver replay")
	failed, answered := byAttempt[1], byAttempt[2]
	assert.Equal(t, failed.ID, answered.ID, "the attempts are separate rows for one logical request")
	assert.Equal(t, "error", failed.Outcome)
	assert.Equal(t, [2]int64{500, 120}, [2]int64{failed.In, failed.Out}, "the failed stream's billed tokens are counted")
	assert.Equal(t, "success", answered.Outcome)
	assert.Equal(t, [2]int64{40, 7}, [2]int64{answered.In, answered.Out})
	calls, errCalls, in := dailyCalls(t, h)
	assert.Equal(t, [3]int64{2, 1, 540}, [3]int64{calls, errCalls, in})
}

// shortWaitDriver announces a long wait and then retries at once (a shortened
// backoff), and its answer reports a usage split that does not add up.
type shortWaitDriver struct{ pathDriver }

func (d *shortWaitDriver) StreamResponse(context.Context, []string, []message.Message, []tools.Tool) <-chan llm.DriverEvent {
	ch := make(chan llm.DriverEvent, 4)
	ch <- llm.DriverEvent{Type: llm.EventRetryWait, Retry: &llm.RetryWait{Attempt: 1, StatusCode: 429, Reason: "http_429", Delay: time.Hour}}
	ch <- llm.DriverEvent{Type: llm.EventRetryWait, Retry: &llm.RetryWait{Attempt: 2, StatusCode: 429, Reason: "http_429", Delay: time.Hour}}
	ch <- llm.DriverEvent{Type: llm.EventComplete, Response: &llm.DriverResponse{Content: "summary",
		Usage: llm.TokenUsage{InputTokens: -3, OutputTokens: 7}, FinishReason: message.FinishReasonEndTurn}}
	close(ch)
	return ch
}

// prod 53e2370c: a retry that started before its announced wait had passed
// measured a negative latency, the row failed llm_call_usage's
// counts_nonnegative check, and the request was not counted at all. Every
// request is still a row, its counts floored at zero.
func TestUsageMeter_NegativeMeasurementsAreFlooredNotDropped(t *testing.T) {
	t.Parallel()
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-negative", "user-negative")
	chat := h.CreateTestChat(ctx, "chat-negative", project.ID, project.UserID)

	meterOnce(t, h, &shortWaitDriver{pathDriver{name: "anthropic"}}, project.UserID, chat.ID)

	rows := ledgerRows(t, h, "compaction")
	require.Len(t, rows, 3, "every request is a row: %+v", rows)
	var latencies []int64
	rowsLat, err := h.DB().Query(`SELECT latency_ms FROM llm_call_usage WHERE purpose = 'compaction'`)
	require.NoError(t, err)
	defer rowsLat.Close()
	for rowsLat.Next() {
		var l int64
		require.NoError(t, rowsLat.Scan(&l))
		latencies = append(latencies, l)
	}
	require.NoError(t, rowsLat.Err())
	for _, l := range latencies {
		assert.GreaterOrEqual(t, l, int64(0))
	}
	for _, r := range rows {
		assert.GreaterOrEqual(t, r.In, int64(0), r.ID)
	}
}

func TestNonNegative(t *testing.T) {
	t.Parallel()
	assert.EqualValues(t, 0, nonNegative(-5))
	assert.EqualValues(t, 0, nonNegative(0))
	assert.EqualValues(t, 12, nonNegative(12))
}
