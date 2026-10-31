// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/reliant"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

const terminalChunk = `{"id":"c1","object":"chat.completion.chunk","created":1,"model":"m",%s}`

// billedThenFailed is a Reliant proxy stream that reports usage and then fails
// mid-stream, which the Reliant driver does not retry: the request ends the
// call, billed.
func billedThenFailed(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: "+terminalChunk+"\n\n", `"choices":[{"index":0,"delta":{"content":"par"}}]`)
	fmt.Fprintf(w, "data: "+terminalChunk+"\n\n", `"choices":[],"usage":{"prompt_tokens":500,"completion_tokens":120,"total_tokens":620}`)
	fmt.Fprint(w, "data: {\"error\":{\"message\":\"upstream reset\",\"type\":\"server_error\"}}\n\n")
}

func answered(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: "+terminalChunk+"\n\n", `"choices":[{"index":0,"delta":{"content":"summary"},"finish_reason":"stop"}]`)
	fmt.Fprintf(w, "data: "+terminalChunk+"\n\n", `"choices":[],"usage":{"prompt_tokens":40,"completion_tokens":7,"total_tokens":47}`)
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func reliantTestDriver(url string) llm.Driver {
	return reliant.NewClient(llm.DriverOptions{ApiKey: "k", BaseURL: url, Model: models.Model{ID: "m", APIModel: "m"}, MaxTokens: 100})
}

// A metered request (compaction, title, automation draft) whose stream reports
// usage and then fails for good, through the real Reliant driver and its
// transport: the failed request's row holds what it was billed.
func TestUsageMeter_TerminalStreamFailureKeepsItsBilledTokens(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { billedThenFailed(w) }))
	defer srv.Close()

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-terminal-meter", "user-terminal-meter")
	chat := h.CreateTestChat(ctx, "chat-terminal-meter", project.ID, project.UserID)

	driver := reliantTestDriver(srv.URL)
	resolved := &resolvedLLMCall{Driver: driver, Model: driver.Model()}
	exec := func(ctx context.Context, _ string) (string, error) {
		_, err := newUsageMeter(h.Repo(), project.UserID, chat.ID, "compaction").streamAndAccumulate(ctx, resolved, nil, nil, nil)
		require.ErrorContains(t, err, "upstream reset")
		return "", nil
	}
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivityWithOptions(exec, activity.RegisterOptions{Name: "MeteredTerminal"})
	_, err := env.ExecuteActivity("MeteredTerminal", "x")
	require.NoError(t, err)

	rows := ledgerRows(t, h, "compaction")
	require.Len(t, rows, 1, "%+v", rows)
	assert.Equal(t, "error", rows[0].Outcome)
	assert.Equal(t, "managed", rows[0].AuthMode)
	assert.Equal(t, [2]int64{500, 120}, [2]int64{rows[0].In, rows[0].Out}, "the failed request's billed tokens are counted")
	calls, errCalls, in := dailyCalls(t, h)
	assert.Equal(t, [3]int64{1, 1, 500}, [3]int64{calls, errCalls, in})
}

// CallLLM's turn, through the real Reliant driver: attempt 1's stream reports
// usage and fails for good, Temporal retries and attempt 2 answers. Attempt 1's
// row holds what it was billed, and the rollup holds both attempts' tokens.
func TestCallLLM_TerminalStreamFailureKeepsItsBilledTokens(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			billedThenFailed(w)
			return
		}
		answered(w)
	}))
	defer srv.Close()

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-terminal-call", "user-terminal-call")
	chat := h.CreateTestChat(ctx, "chat-terminal-call", project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	runCallLLMWithRetries(t, h, reliantTestDriver(srv.URL), chat.ID)
	require.EqualValues(t, 2, hits.Load(), "the test must really run the activity twice")

	rows := readLedger(t, h)
	require.Len(t, rows, 2, "%+v", rows)
	byAttempt := map[int]ledgerRow{}
	for _, r := range rows {
		byAttempt[r.Attempt] = r
	}
	require.Contains(t, byAttempt, 1)
	require.Contains(t, byAttempt, 2)
	assert.Equal(t, "error", byAttempt[1].Outcome)
	assert.Equal(t, [2]int64{500, 120}, [2]int64{byAttempt[1].In, byAttempt[1].Out}, "the failed attempt keeps what it was billed")
	assert.Equal(t, "success", byAttempt[2].Outcome)
	assert.Equal(t, [2]int64{40, 7}, [2]int64{byAttempt[2].In, byAttempt[2].Out})
	calls, errCalls, in := dailyCalls(t, h)
	assert.Equal(t, [3]int64{2, 1, 540}, [3]int64{calls, errCalls, in})
}

// slowRecorder is a ledger whose every write takes delay.
type slowRecorder struct {
	delay time.Duration
	mu    sync.Mutex
	recs  []db.LLMCallUsage
}

func (r *slowRecorder) RecordLLMCallUsage(_ context.Context, rec db.LLMCallUsage) (bool, error) {
	time.Sleep(r.delay)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recs = append(r.recs, rec)
	return true, nil
}

// The request that ended a call is measured to when its stream ended. Writing
// the rows of the requests the driver retried happens after that, and a slow
// ledger must not add those writes to the final request's latency.
func TestUsageMeter_FinalLatencyExcludesTheRetryRowWrites(t *testing.T) {
	t.Parallel()
	const writeDelay = 300 * time.Millisecond
	recorder := &slowRecorder{delay: writeDelay}
	driver := &ladderDriver{pathDriver{name: "anthropic"}}
	resolved := &resolvedLLMCall{Driver: driver, Model: driver.Model()}
	exec := func(ctx context.Context, _ string) (string, error) {
		_, err := newUsageMeter(recorder, "u", "c", "compaction").streamAndAccumulate(ctx, resolved, nil, nil, nil)
		return "", err
	}
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivityWithOptions(exec, activity.RegisterOptions{Name: "MeteredSlowLedger"})
	_, err := env.ExecuteActivity("MeteredSlowLedger", "x")
	require.NoError(t, err)

	require.Len(t, recorder.recs, 3, "%+v", recorder.recs)
	var final *db.LLMCallUsage
	for i := range recorder.recs {
		if !strings.Contains(recorder.recs[i].ID, "/request/") {
			final = &recorder.recs[i]
		}
	}
	require.NotNil(t, final)
	assert.Less(t, final.LatencyMs, writeDelay.Milliseconds(),
		"two retry rows took %s each to write; none of it is the final request's latency", writeDelay)
}
