// Copyright (c) 2025 Reliant Labs
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/reliant-labs/reliant/internal/chatmarkers"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type snapshotRecorder struct {
	mu    sync.Mutex
	snaps []ratelimit.Snapshot
}

func (r *snapshotRecorder) ObserveRateLimits(_ context.Context, s ratelimit.Snapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snaps = append(r.snaps, s)
}

// retryTestClient is a CodexClient wired like production (SDK retries are off
// via requestOptions; rate-limit middleware installed) against srv, with the
// ladder's sleeper replaced so no test waits on a real timer.
func retryTestClient(srv *httptest.Server, sleeps *[]time.Duration) *CodexClient {
	return &CodexClient{
		options: llm.DriverOptions{
			Model:     models.Model{ID: models.GPT53Codex, APIModel: "gpt-5.3-codex"},
			MaxTokens: 1024,
		},
		sessionID: "test-session",
		client: llm.NewOpenAISDKClient(
			option.WithBaseURL(srv.URL),
			option.WithAPIKey("test-key"),
			option.WithMiddleware(ratelimit.Middleware("codex", ratelimit.ParseCodex)),
		),
		ladder: ratelimit.Retryer{
			Jitter: func(time.Duration) time.Duration { return 0 },
			Sleep: func(_ context.Context, d time.Duration) error {
				*sleeps = append(*sleeps, d)
				return nil
			},
		},
	}
}

func drain(t *testing.T, ctx context.Context, c *CodexClient) []llm.DriverEvent {
	t.Helper()
	msgs := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}}}
	var out []llm.DriverEvent
	timeout := time.After(10 * time.Second)
	ch := c.StreamResponse(ctx, []string{"be helpful"}, msgs, nil)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-timeout:
			t.Fatal("stream did not finish")
		}
	}
}

const codexOKStream = "event: response.created\n" +
	`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress"}}` + "\n\n" +
	"event: response.completed\n" +
	`data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp_1","status":"completed","output":[]}}` + "\n\n"

// A `usage_limit_reached` 429 used to be treated as a transient 429: five
// Temporal attempts burned and the reset time lost. Body shape verified against
// codex-rs api_bridge.rs (UsageErrorBody): resets_at is Unix seconds.
func TestStream_UsageLimitReachedBecomesTheMarkerWithTheResetTime(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "1") // a short one must not make this a transient retry
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_at":1790712000,"limit_window_minutes":300}}`))
	}))
	defer srv.Close()

	var sleeps []time.Duration
	events := drain(t, context.Background(), retryTestClient(srv, &sleeps))

	assert.Equal(t, int32(1), hits.Load(), "an exhausted window is not retried")
	assert.Empty(t, sleeps)
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	require.Equal(t, llm.EventError, last.Type)
	for _, ev := range events {
		assert.NotEqual(t, llm.EventRetryWait, ev.Type)
	}
	kind, payload, found := chatmarkers.Extract(last.Error.Error())
	require.True(t, found)
	assert.Equal(t, chatmarkers.KindProviderUsageLimit, kind)
	assert.Equal(t, "2026-09-29T20:00:00Z", payload)
	summary := chatmarkers.ProviderUsageLimitSummary(last.Error.Error())
	assert.Contains(t, summary, "Codex, 5-hour window")
	assert.Contains(t, summary, "resets Tue Sep 29 20:00 UTC")
}

func TestStream_UsageLimitWithoutAResetStillMarksIt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"usage_limit_reached"}}`))
	}))
	defer srv.Close()
	var sleeps []time.Duration
	events := drain(t, context.Background(), retryTestClient(srv, &sleeps))
	last := events[len(events)-1]
	require.Equal(t, llm.EventError, last.Type)
	_, payload, found := chatmarkers.Extract(last.Error.Error())
	require.True(t, found)
	assert.Empty(t, payload)
}

func TestStream_ResetComesFromTheExhaustedHeaderWindowWhenTheBodyHasNone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-codex-secondary-used-percent", "100")
		w.Header().Set("x-codex-secondary-window-minutes", "10080")
		w.Header().Set("x-codex-secondary-reset-at", "1790712000")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"usage_limit_reached"}}`))
	}))
	defer srv.Close()
	var sleeps []time.Duration
	events := drain(t, context.Background(), retryTestClient(srv, &sleeps))
	last := events[len(events)-1]
	_, payload, _ := chatmarkers.Extract(last.Error.Error())
	assert.Equal(t, "2026-09-29T20:00:00Z", payload)
	assert.Contains(t, chatmarkers.ProviderUsageLimitSummary(last.Error.Error()), "7-day window")
}

// The Codex ladder: SDK retries off, Retry-After honored, each wait published
// as EventRetryWait BEFORE it is slept so CallLLM can record provider_backoff.
func TestStream_TransientRateLimitPublishesRetryWaitAndRetries(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "3")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_exceeded","message":"slow down"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(codexOKStream))
	}))
	defer srv.Close()

	var sleeps []time.Duration
	events := drain(t, context.Background(), retryTestClient(srv, &sleeps))

	require.NotEmpty(t, events)
	require.Equal(t, llm.EventRetryWait, events[0].Type)
	assert.Equal(t, 429, events[0].Retry.StatusCode)
	assert.Equal(t, 3*time.Second, events[0].Retry.Delay)
	assert.Equal(t, []time.Duration{3 * time.Second}, sleeps)
	assert.Equal(t, llm.EventComplete, events[len(events)-1].Type)
	assert.Equal(t, int32(2), hits.Load(), "exactly one request per attempt: the SDK must not retry underneath")
}

func TestStream_RetryAfterPastTheCapIsAUsageLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7200")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_exceeded"}}`))
	}))
	defer srv.Close()
	var sleeps []time.Duration
	events := drain(t, context.Background(), retryTestClient(srv, &sleeps))
	last := events[len(events)-1]
	kind, payload, found := chatmarkers.Extract(last.Error.Error())
	require.True(t, found)
	assert.Equal(t, chatmarkers.KindProviderUsageLimit, kind)
	assert.NotEmpty(t, payload)
	assert.Empty(t, sleeps)
}

func TestStream_QuotaExhaustionIsNotRetriedAndNotAUsageWindow(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"insufficient_quota","message":"You exceeded your current quota"}}`))
	}))
	defer srv.Close()
	var sleeps []time.Duration
	events := drain(t, context.Background(), retryTestClient(srv, &sleeps))
	last := events[len(events)-1]
	require.Equal(t, llm.EventError, last.Type)
	_, _, found := chatmarkers.Extract(last.Error.Error())
	assert.False(t, found)
	assert.Equal(t, int32(1), hits.Load())
	assert.Empty(t, sleeps)
}

func TestStream_LadderGivesUpAfterMaxAttempts(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	var sleeps []time.Duration
	events := drain(t, context.Background(), retryTestClient(srv, &sleeps))
	assert.Equal(t, llm.EventError, events[len(events)-1].Type)
	assert.Equal(t, int32(models.MaxRetries+1), hits.Load())
	assert.Len(t, sleeps, models.MaxRetries)
}

// Every response reaches the observer — the 429 as well as the success — and
// the in-stream codex.rate_limits event does too.
func TestStream_ObserverSeesHeadersOfErrorAndSuccessAndTheStreamEvent(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.Header().Set("x-codex-primary-used-percent", "97")
			w.Header().Set("x-codex-primary-reset-at", "1790712000")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-codex-primary-used-percent", "12.5")
		w.Header().Set("x-codex-primary-reset-at", "1790712000")
		_, _ = w.Write([]byte("event: codex.rate_limits\n" +
			`data: {"type":"codex.rate_limits","plan_type":"pro","rate_limits":{"primary":{"used_percent":13,"window_minutes":300,"reset_at":1790712000}}}` + "\n\n" +
			codexOKStream))
	}))
	defer srv.Close()

	rec := &snapshotRecorder{}
	var sleeps []time.Duration
	events := drain(t, ratelimit.WithObserver(context.Background(), rec), retryTestClient(srv, &sleeps))
	require.Equal(t, llm.EventComplete, events[len(events)-1].Type)

	require.Len(t, rec.snaps, 3)
	assert.Equal(t, "codex", rec.snaps[0].Provider)
	assert.Equal(t, 0.97, *rec.snaps[0].Windows[0].UsedFraction)
	assert.Equal(t, ratelimit.StatusAllowed, rec.snaps[1].Status)
	assert.Equal(t, "pro", rec.snaps[2].PlanType, "the codex.rate_limits event")
	assert.Equal(t, 300*time.Minute, rec.snaps[2].Windows[0].Length)
}

const unverifiableReasoningBody = `{"error":{"message":"The encrypted content gAAAA... could not be verified. Reason: Encrypted content organization_id did not match the target organization.","type":"invalid_request_error","code":"invalid_encrypted_content"}}`

func reasoningHistory() []message.Message {
	return []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}},
	}
}

func TestWithoutReasoningItemsKeepsEverythingElse(t *testing.T) {
	params := responses.ResponseNewParams{Input: responses.ResponseNewParamsInputUnion{OfInputItemList: responses.ResponseInputParam{
		responses.ResponseInputItemParamOfMessage("hi", responses.EasyInputMessageRoleUser),
		{OfReasoning: &responses.ResponseReasoningItemParam{ID: "rs_1", EncryptedContent: openai.String("gAAAA")}},
		responses.ResponseInputItemParamOfFunctionCall("{}", "call_1", "tool"),
	}}}
	got, n := withoutReasoningItems(params)
	assert.Equal(t, 1, n)
	require.Len(t, got.Input.OfInputItemList, 2)
	assert.Nil(t, got.Input.OfInputItemList[0].OfReasoning)
	assert.NotNil(t, got.Input.OfInputItemList[1].OfFunctionCall)
	assert.Len(t, params.Input.OfInputItemList, 3, "input untouched")
}

func TestRecoverUnverifiableReasoningIsOneShot(t *testing.T) {
	c := &CodexClient{sessionID: "chat-1"}
	params := responses.ResponseNewParams{Input: responses.ResponseNewParamsInputUnion{OfInputItemList: responses.ResponseInputParam{
		{OfReasoning: &responses.ResponseReasoningItemParam{ID: "rs_1"}},
	}}}
	err := &openai.Error{StatusCode: 400, Response: &http.Response{StatusCode: 400}}
	require.NoError(t, json.Unmarshal([]byte(unverifiableReasoningBody), err))
	require.True(t, isUnverifiableReasoningError(err))

	done := false
	assert.True(t, c.recoverUnverifiableReasoning(err, &params, &done))
	assert.Empty(t, params.Input.OfInputItemList)
	assert.False(t, c.recoverUnverifiableReasoning(err, &params, &done), "at most one recovery")
}

func TestRecoverIgnoresUnrelatedErrors(t *testing.T) {
	c := &CodexClient{}
	params := responses.ResponseNewParams{Input: responses.ResponseNewParamsInputUnion{OfInputItemList: responses.ResponseInputParam{
		{OfReasoning: &responses.ResponseReasoningItemParam{ID: "rs_1"}},
	}}}
	other := &openai.Error{StatusCode: 400, Response: &http.Response{StatusCode: 400}}
	require.NoError(t, json.Unmarshal([]byte(`{"error":{"message":"bad schema"}}`), other))
	done := false
	assert.False(t, c.recoverUnverifiableReasoning(other, &params, &done))
	assert.False(t, c.recoverUnverifiableReasoning(errors.New("boom"), &params, &done))
	assert.Len(t, params.Input.OfInputItemList, 1)
}

// The request path: a 400 naming unverifiable encrypted content is retried
// exactly once, and the retry body carries no reasoning items.
func TestStreamRetriesOnceWithoutReasoningItems(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		n := len(bodies)
		mu.Unlock()
		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(unverifiableReasoningBody))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(codexOKStream))
	}))
	defer srv.Close()

	var sleeps []time.Duration
	c := retryTestClient(srv, &sleeps)
	c.finalizeParams = func(p *responses.ResponseNewParams) {
		p.Input.OfInputItemList = append(responses.ResponseInputParam{
			{OfReasoning: &responses.ResponseReasoningItemParam{ID: "rs_1", EncryptedContent: openai.String("gAAAA")}},
		}, p.Input.OfInputItemList...)
	}
	events := drain(t, context.Background(), c)

	require.Len(t, bodies, 2, "one original request and exactly one recovery")
	assert.Contains(t, bodies[0], `"type":"reasoning"`)
	assert.NotContains(t, bodies[1], `"type":"reasoning"`)
	assert.Contains(t, bodies[1], "hello", "the rest of the history is kept")
	assert.Empty(t, sleeps, "recovery is not a ladder wait")
	for _, ev := range events {
		assert.NotEqual(t, llm.EventError, ev.Type, "%v", ev.Error)
	}
}

func TestStreamUnverifiableReasoningRetryIsBounded(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(unverifiableReasoningBody))
	}))
	defer srv.Close()

	var sleeps []time.Duration
	c := retryTestClient(srv, &sleeps)
	c.finalizeParams = func(p *responses.ResponseNewParams) {
		p.Input.OfInputItemList = append(p.Input.OfInputItemList,
			responses.ResponseInputItemUnionParam{OfReasoning: &responses.ResponseReasoningItemParam{ID: "rs_1"}})
	}
	events := drain(t, context.Background(), c)

	assert.Equal(t, int32(2), hits.Load())
	var sawErr bool
	for _, ev := range events {
		sawErr = sawErr || ev.Type == llm.EventError
	}
	assert.True(t, sawErr)
}

func TestSendMessagesRetriesOnceWithoutReasoningItems(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(unverifiableReasoningBody))
			return
		}
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[]}`))
	}))
	defer srv.Close()

	var sleeps []time.Duration
	c := retryTestClient(srv, &sleeps)
	c.finalizeParams = func(p *responses.ResponseNewParams) {
		p.Input.OfInputItemList = append(p.Input.OfInputItemList,
			responses.ResponseInputItemUnionParam{OfReasoning: &responses.ResponseReasoningItemParam{ID: "rs_1"}})
	}
	_, err := c.SendMessages(context.Background(), []string{"be helpful"}, reasoningHistory(), nil)
	require.NoError(t, err)
	assert.Equal(t, int32(2), hits.Load())
}
