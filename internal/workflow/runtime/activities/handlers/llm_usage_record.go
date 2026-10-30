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
	Recorder    LLMUsageRecorder
	UserID      string
	ChatID      string
	Purpose     string
	Ordinal     int
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
// activity makes, plus the Temporal attempt. Temporal re-running an activity
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
		ID:               llmUsageID(info.WorkflowExecution.ID, info.WorkflowExecution.RunID, info.ActivityID, c.Purpose, c.Ordinal),
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

func llmUsageID(workflowID, runID, activityID, purpose string, ordinal int) string {
	return workflowID + "/" + runID + "/" + activityID + "/" + purpose + "/" + strconv.Itoa(ordinal)
}

// recordLLMCallUsage is CallLLM's turn: the one streamed request it makes.
func (a *CallLLMActivity) recordLLMCallUsage(ctx context.Context, chat *db.Chat, provider, modelID string, latencyMs int64, usage llm.TokenUsage, callErr error, interrupted bool) {
	recordLLMUsage(ctx, llmUsageCall{
		Recorder: a.repo, UserID: chat.UserID, ChatID: chat.ID,
		Purpose: store.LLMPurposeChat, Ordinal: 1,
		Provider: provider, Model: modelID, LatencyMs: latencyMs,
		Usage: usage, Err: callErr, Interrupted: interrupted,
	})
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
func (m *usageMeter) streamAndAccumulate(ctx context.Context, resolved *resolvedLLMCall, prompts []string, messages []message.Message, available []tools.Tool) (*llm.DriverResponse, error) {
	m.n++
	start := time.Now()
	resp, err := accumulator.StreamAndAccumulate(ctx, resolved.Driver, prompts, messages, available)
	call := llmUsageCall{
		Recorder: m.recorder, UserID: m.userID, ChatID: m.chatID,
		Purpose: m.purpose, Ordinal: m.n,
		Provider: usageProvider(resolved, resolved.Driver), Model: resolvedUsageModel(resolved),
		LatencyMs: time.Since(start).Milliseconds(), Err: err,
	}
	if resp != nil {
		call.Usage = resp.Usage
	}
	recordLLMUsage(ctx, call)
	return resp, err
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
