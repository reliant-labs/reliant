// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/accumulator"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/store"
)

// LLMUsageRecorder is what an activity needs of the usage ledger: one
// idempotent write per model request.
type LLMUsageRecorder interface {
	RecordLLMCallUsage(ctx context.Context, rec db.LLMCallUsage) (bool, error)
}

// llmUsageWriteTimeout bounds the ledger write. It is a service record, so a
// slow database must not hold up the turn it describes.
const llmUsageWriteTimeout = 5 * time.Second

// llmUsageCall is one model request, as the ledger sees it. Counts and
// metadata only: no prompt, reply or error text can be put in it.
type llmUsageCall struct {
	Recorder LLMUsageRecorder
	UserID   string
	ChatID   string
	Purpose  string
	Ordinal  int
	// Request is the driver-level request number of a request the driver
	// failed and retried inside the call (RetryWait.Attempt); 0 is the request
	// that ended the call.
	Request     int
	Provider    string
	Model       string
	LatencyMs   int64
	Usage       llm.TokenUsage
	Err         error
	Interrupted bool
}

// recordLLMUsage writes the count-only service record of one model request. It
// is the ledger's only write path: CallLLM's turn and every other model request
// (through usageMeter) end here.
//
// The record is keyed on the logical request, the activity (workflow id, run
// id, activity id), what it was for, and its ordinal among the requests that
// activity makes, plus the Temporal attempt. A driver that fails and retries
// a request inside one call (its own backoff ladder) made several provider
// requests for that logical one: each failed one is its own row, keyed by its
// driver request number (recordDriverRetries). Temporal re-running an activity
// makes the same logical requests again, and each attempt is a real request to
// the provider that may have used tokens (a stream can fail after reporting
// usage), so every attempt is its own row and is counted. Only a repeated write
// of the same attempt is ignored. A different purpose or ordinal is a different
// request and never collides.
//
// A failed write is logged and dropped; usage accounting never fails the work
// it describes.
func recordLLMUsage(ctx context.Context, c llmUsageCall) {
	if c.Recorder == nil || !activity.IsActivity(ctx) {
		return
	}
	info := activity.GetInfo(ctx)
	rec := db.LLMCallUsage{
		ID:               llmUsageID(info.WorkflowExecution.ID, info.WorkflowExecution.RunID, info.ActivityID, c.Purpose, c.Ordinal, c.Request),
		UserID:           c.UserID,
		ChatID:           c.ChatID,
		RunID:            info.WorkflowExecution.RunID,
		Provider:         c.Provider,
		Model:            c.Model,
		AuthMode:         llmUsageAuthMode(c.Provider),
		Purpose:          c.Purpose,
		InputTokens:      c.Usage.InputTokens,
		OutputTokens:     c.Usage.OutputTokens,
		CacheReadTokens:  firstNonZero(c.Usage.CacheReadInputTokens, c.Usage.CachedInputTokens),
		CacheWriteTokens: c.Usage.CacheCreationInputTokens,
		LatencyMs:        c.LatencyMs,
		Attempt:          info.Attempt,
		CreatedAt:        time.Now(),
	}
	switch {
	case c.Interrupted || errors.Is(c.Err, context.Canceled):
		rec.Outcome = store.LLMOutcomeCancelled
	case c.Err != nil:
		rec.Outcome = store.LLMOutcomeError
		rec.ErrorClass = llmUsageErrorClass(c.Err)
	default:
		rec.Outcome = store.LLMOutcomeSuccess
	}

	// Detached: a user interrupt cancels ctx, and the interrupted request is
	// still one that used tokens.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), llmUsageWriteTimeout)
	defer cancel()
	if _, err := c.Recorder.RecordLLMCallUsage(wctx, rec); err != nil {
		logging.WarnContext(ctx, "could not record LLM usage", "chatID", c.ChatID, "purpose", c.Purpose, "error", err)
	}
}

func llmUsageID(workflowID, runID, activityID, purpose string, ordinal, request int) string {
	id := workflowID + "/" + runID + "/" + activityID + "/" + purpose + "/" + strconv.Itoa(ordinal)
	if request > 0 {
		id += "/request/" + strconv.Itoa(request)
	}
	return id
}

// driverRetries collects the provider requests a driver failed and retried
// inside one call, from its EventRetryWait events, each with how long it ran.
type driverRetries struct {
	start time.Time
	// end is when the call's stream ended (finish); zero until then.
	end  time.Time
	list []driverRetry
}

type driverRetry struct {
	wait      llm.RetryWait
	latencyMs int64
}

func newDriverRetries() *driverRetries { return &driverRetries{start: time.Now()} }

// observe records one failed request. The next one starts once the driver has
// slept out the wait.
func (r *driverRetries) observe(wait llm.RetryWait) {
	now := time.Now()
	r.list = append(r.list, driverRetry{wait: wait, latencyMs: now.Sub(r.start).Milliseconds()})
	r.start = now.Add(wait.Delay)
}

// finish marks the call's stream as ended and returns when. Call it as soon as
// the stream ends: what runs after it, the ledger writes of the retried
// requests above all, is not part of any request's latency.
func (r *driverRetries) finish() time.Time {
	r.end = time.Now()
	return r.end
}

// finalLatencyMs is how long the request that ended the call ran: the whole
// call when the driver retried nothing, else from its last wait to finish.
func (r *driverRetries) finalLatencyMs(callLatencyMs int64) int64 {
	if len(r.list) == 0 {
		return callLatencyMs
	}
	end := r.end
	if end.IsZero() {
		end = time.Now()
	}
	return max(end.Sub(r.start).Milliseconds(), 0)
}

// retriedRequestError is a failed request a driver retried, as the ledger
// classifies it: from the status and reason its wait names. The ledger keeps
// only the class.
type retriedRequestError struct{ wait llm.RetryWait }

func (e retriedRequestError) Error() string {
	return "http " + strconv.Itoa(e.wait.StatusCode) + ": " + e.wait.Reason
}

// recordDriverRetries writes one row per provider request the driver failed
// and retried within call: the same logical request under its own driver
// request number, as an error of the class its wait names, holding whatever
// usage it reported before failing.
func recordDriverRetries(ctx context.Context, call llmUsageCall, retries *driverRetries) {
	for _, r := range retries.list {
		c := call
		c.Request = r.wait.Attempt
		c.LatencyMs = r.latencyMs
		c.Usage = llm.TokenUsage{}
		if r.wait.Usage != nil {
			c.Usage = *r.wait.Usage
		}
		c.Err = retriedRequestError{wait: r.wait}
		c.Interrupted = false
		recordLLMUsage(ctx, c)
	}
}

// recordLLMCallUsage is CallLLM's turn: the streamed request it makes, plus
// every request its driver failed and retried along the way.
func (a *CallLLMActivity) recordLLMCallUsage(ctx context.Context, chat *db.Chat, provider, modelID string, latencyMs int64, usage llm.TokenUsage, callErr error, interrupted bool, retries *driverRetries) {
	call := llmUsageCall{
		Recorder: a.repo, UserID: chat.UserID, ChatID: chat.ID,
		Purpose: store.LLMPurposeChat, Ordinal: 1,
		Provider: provider, Model: modelID, LatencyMs: latencyMs,
		Usage: usage, Err: callErr, Interrupted: interrupted,
	}
	if retries != nil {
		recordDriverRetries(ctx, call, retries)
		call.LatencyMs = retries.finalLatencyMs(latencyMs)
	}
	recordLLMUsage(ctx, call)
}

// usageMeter is how every model request other than CallLLM's turn is made:
// compaction summaries, chat titles and automation drafts. It streams the
// request and records it, so an activity cannot make a request the ledger
// misses. One meter per activity execution: its ordinal counts the requests
// made so far, which is what keeps a retried execution's requests on the same
// ids, which the attempt number then tells apart.
//
// TestModelRequestsGoThroughTheUsageLedger fails when a new call site streams a
// request any other way.
type usageMeter struct {
	recorder LLMUsageRecorder
	userID   string
	chatID   string
	purpose  string
	n        int
}

// newUsageMeter meters one execution's requests for purpose. A nil recorder
// (an activity built without a store, in tests) still streams, unrecorded.
func newUsageMeter(recorder LLMUsageRecorder, userID, chatID, purpose string) *usageMeter {
	return &usageMeter{recorder: recorder, userID: userID, chatID: chatID, purpose: purpose}
}

// streamAndAccumulate sends one request to resolved's driver, accumulates the
// streamed reply and records the request's usage, whether it succeeded or not.
// A request that fails after the provider reported usage records that usage:
// those tokens were consumed.
func (m *usageMeter) streamAndAccumulate(ctx context.Context, resolved *resolvedLLMCall, prompts []string, messages []message.Message, available []tools.Tool) (*llm.DriverResponse, error) {
	m.n++
	retries := newDriverRetries()
	callStart := retries.start
	resp, err := accumulator.StreamAndAccumulateObserving(ctx, resolved.Driver, prompts, messages, available, retries.observe)
	callLatencyMs := retries.finish().Sub(callStart).Milliseconds()
	call := llmUsageCall{
		Recorder: m.recorder, UserID: m.userID, ChatID: m.chatID,
		Purpose: m.purpose, Ordinal: m.n,
		Provider: usageProvider(resolved, resolved.Driver), Model: resolvedUsageModel(resolved),
	}
	recordDriverRetries(ctx, call, retries)
	call.LatencyMs = retries.finalLatencyMs(callLatencyMs)
	call.Err = err
	if err != nil && ctx.Err() != nil {
		// A driver that stops because its context ended closes the stream
		// without a final event, which the accumulator reports as a stream that
		// ended early. The cause is the context: a cancel or a deadline.
		call.Err = ctx.Err()
	}
	if resp != nil {
		call.Usage = resp.Usage
	}
	recordLLMUsage(ctx, call)
	if err != nil {
		// The accumulator hands back what a failed stream produced so its usage
		// can be recorded above; the callers of the meter get the old contract,
		// no response with an error.
		return nil, err
	}
	return resp, nil
}

// resolvedUsageModel is the concrete model that served the call: the registry
// definition after tag and selector resolution, else the driver's own.
func resolvedUsageModel(resolved *resolvedLLMCall) string {
	if resolved.Definition != nil {
		return resolved.Definition.ID
	}
	return string(resolved.Model.ID)
}

func firstNonZero(vs ...int64) int64 {
	for _, v := range vs {
		if v != 0 {
			return v
		}
	}
	return 0
}

// llmUsageAuthMode is how the call was paid for, from the provider that served
// it: the managed Reliant key, a local model, a subscription login, or a key
// the user pasted.
func llmUsageAuthMode(provider string) string {
	switch provider {
	case "reliant":
		return store.LLMAuthModeManaged
	case "local":
		return store.LLMAuthModeLocal
	case "claude", "codex", "copilot", "antigravity":
		return store.LLMAuthModeSubscription
	default:
		return store.LLMAuthModeBYOK
	}
}

// llmUsageErrorClass maps a failed call to the closed set the ledger's CHECK
// constraint allows. Only the class is kept; the error's text never is.
func llmUsageErrorClass(err error) string {
	var usageLimit *ratelimit.UsageLimitError
	var stall *llm.StreamStallError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &usageLimit):
		return "usage_limit"
	case errors.As(err, &stall), errors.Is(err, llm.ErrStreamProgressTimeout):
		return "stalled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "rate limit") || strings.Contains(msg, "429"):
		return "rate_limit"
	case strings.Contains(msg, "context length") || strings.Contains(msg, "too many tokens") || strings.Contains(msg, "maximum context"):
		return "context_length"
	case strings.Contains(msg, "401") || strings.Contains(msg, "403") || strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "authentication") || strings.Contains(msg, "invalid api key"):
		return "auth"
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline"):
		return "timeout"
	case strings.Contains(msg, "connection") || strings.Contains(msg, "network") || strings.Contains(msg, "eof"):
		return "network"
	default:
		return "unknown"
	}
}

// usageProvider is the provider that served the call: the registry's choice,
// or the driver's own name when an injected resolver supplied the model.
func usageProvider(resolved *resolvedLLMCall, driver llm.Driver) string {
	if resolved.ProviderDriver != "" {
		return resolved.ProviderDriver
	}
	return driver.Name()
}
