// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/accumulator"
	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
	"github.com/reliant-labs/reliant/internal/llm/models"
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
	Request  int
	Provider string
	Model    string
	// AccountID is the provider account that served the request
	// (usageAccountID); "" when it was not routed to one.
	AccountID string
	// OrgID is the org whose work the call was; Funding is who paid for it
	// (usageFunding). An org-funded call is also counted in the org's rollup.
	OrgID   string
	Funding string
	// FundingOrgID is the org that paid (usageFundingOrg); "" when no org is
	// known to have.
	FundingOrgID string
	// ProjectID is the project of the chat the call belonged to; "" for a
	// call with no chat.
	ProjectID string
	LatencyMs int64
	// TTFTMs is the time from the request to its first output token
	// (driverRetries.ttftMs); nil when no token arrived.
	TTFTMs *int64
	// ProviderRequestID is the request id the provider answered with
	// (DriverResponse.UpstreamRequestID, the span's gen_ai.response.id); ""
	// when it sent none.
	ProviderRequestID string
	Usage             llm.TokenUsage
	Err               error
	Interrupted       bool
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
	// The run the call was part of (usageRunOf).
	run := usageRunOf(ctx)
	rec := db.LLMCallUsage{
		ID:               llmUsageID(info.WorkflowExecution.ID, info.WorkflowExecution.RunID, info.ActivityID, c.Purpose, c.Ordinal, c.Request),
		UserID:           c.UserID,
		ChatID:           c.ChatID,
		RunID:            info.WorkflowExecution.RunID,
		Provider:         c.Provider,
		Model:            c.Model,
		AccountID:        c.AccountID,
		OrgID:            c.OrgID,
		Funding:          c.Funding,
		FundingOrgID:     c.FundingOrgID,
		AuthMode:         llmUsageAuthMode(c.Provider),
		Purpose:          c.Purpose,
		InputTokens:      nonNegative(c.Usage.InputTokens),
		OutputTokens:     nonNegative(c.Usage.OutputTokens),
		CacheReadTokens:  nonNegative(firstNonZero(c.Usage.CacheReadInputTokens, c.Usage.CachedInputTokens)),
		CacheWriteTokens: nonNegative(c.Usage.CacheCreationInputTokens),
		LatencyMs:        nonNegative(c.LatencyMs),
		Attempt:          info.Attempt,
		CreatedAt:        time.Now(),

		WorkflowID:        run.WorkflowID,
		ThreadID:          run.ThreadID,
		WorkflowName:      run.WorkflowName,
		NodeID:            run.NodeID,
		Preset:            run.Preset,
		ProjectID:         c.ProjectID,
		AIRunID:           run.AIRunID,
		AIRootRunID:       run.AIRootRunID,
		ProviderRequestID: strings.TrimSpace(c.ProviderRequestID),
		TTFTMs:            c.TTFTMs,
	}
	rec.CostUSDNanos, rec.CostBasis = llmUsageCost(rec.AuthMode, c.Usage.Cost)
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

// nonNegative is a ledger count as llm_call_usage stores it: never below
// zero (llm_call_usage_counts_nonnegative). A negative count is a measurement
// artefact, not usage: a driver's retry that started before the wait it
// announced had elapsed, or a provider's usage split that does not add up.
// Writing it failed the whole row on the check constraint, so the request
// was not counted at all and the failed transaction was an ERROR issue (prod
// reliant-temporal-worker 53e2370c, 2026-11-20).
func nonNegative(v int64) int64 {
	return max(v, 0)
}

// maxReportedCostUSD bounds a reported cost to what nanos fit in an int64. A
// value past it is a broken report, not a price, and is left unpriced.
const maxReportedCostUSD = float64(math.MaxInt64) / 1e9

// llmUsageCost is what the ledger records of a call's cost (agent-analytics.md
// §4): the cost the gateway reported, in billionths of a dollar, else
// "unpriced" for a subscription or a local model, which have no per-call
// price, else nothing at all. A reported cost of 0 is the gateway not pricing
// the call, not a free one (llm.TokenUsage.Cost), so it records no cost: a
// NULL cost is unknown and is never summed as zero.
func llmUsageCost(authMode string, costUSD float64) (*int64, string) {
	if costUSD > 0 && costUSD < maxReportedCostUSD {
		nanos := int64(math.Round(costUSD * 1e9))
		return &nanos, store.LLMCostBasisReported
	}
	switch authMode {
	case store.LLMAuthModeSubscription, store.LLMAuthModeLocal:
		return nil, store.LLMCostBasisUnpriced
	}
	return nil, ""
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
	end time.Time
	// firstToken is when the request that ended the call produced its first
	// output token (observeEvent); zero until then.
	firstToken time.Time
	list       []driverRetry
	// now is the clock; newDriverRetries uses time.Now.
	now func() time.Time
}

type driverRetry struct {
	wait      llm.RetryWait
	latencyMs int64
}

func newDriverRetries() *driverRetries { return newDriverRetriesAt(time.Now) }

// newDriverRetriesAt is newDriverRetries on the clock now.
func newDriverRetriesAt(now func() time.Time) *driverRetries {
	return &driverRetries{start: now(), now: now}
}

// observe records one failed request. The next one starts once the driver has
// slept out the wait.
func (r *driverRetries) observe(wait llm.RetryWait) {
	now := r.now()
	// A driver may retry before the whole wait it announced has passed (a
	// shortened backoff), so the next request can start before r.start.
	r.list = append(r.list, driverRetry{wait: wait, latencyMs: max(now.Sub(r.start).Milliseconds(), 0)})
	r.start = now.Add(wait.Delay)
	// A token the failed request streamed is not the next request's.
	r.firstToken = time.Time{}
}

// observeEvent records what one streamed event says about the call's requests:
// a retry (observe), or the first output token of the current request. The
// first token is the first text or thinking delta, as the call's span marks
// it (gen_ai.first_token), so the row and the span time the same thing.
func (r *driverRetries) observeEvent(ev llm.DriverEvent) {
	switch ev.Type {
	case llm.EventRetryWait:
		if ev.Retry != nil {
			r.observe(*ev.Retry)
		}
	case llm.EventContentDelta, llm.EventThinkingDelta:
		// Empty framing deltas do not represent an output token.
		if (ev.Type == llm.EventContentDelta && ev.Content == "") ||
			(ev.Type == llm.EventThinkingDelta && ev.Thinking == "") {
			return
		}
		if r.firstToken.IsZero() {
			r.firstToken = r.now()
		}
	}
}

// ttftMs is the time to first token of the request that ended the call: from
// its start (after the last retry's wait) to its first output token. nil when
// it produced none.
func (r *driverRetries) ttftMs() *int64 {
	if r == nil || r.firstToken.IsZero() {
		return nil
	}
	ms := max(r.firstToken.Sub(r.start).Milliseconds(), 0)
	return &ms
}

// finish marks the call's stream as ended and returns when. Call it as soon as
// the stream ends: what runs after it, the ledger writes of the retried
// requests above all, is not part of any request's latency.
func (r *driverRetries) finish() time.Time {
	r.end = r.now()
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
		end = r.now()
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
	for i, r := range retries.list {
		c := call
		// Numbered by position in the call, not by the driver's attempt: a
		// request resent after a context-window refusal is a new stream whose
		// driver counts its own retries from 1 again. In a single stream the
		// two are the same number.
		c.Request = i + 1
		c.LatencyMs = r.latencyMs
		c.Usage = llm.TokenUsage{}
		if r.wait.Usage != nil {
			c.Usage = *r.wait.Usage
		}
		c.Err = retriedRequestError{wait: r.wait}
		c.Interrupted = false
		// A failed request answered nothing: no request id, no first token.
		c.ProviderRequestID = ""
		c.TTFTMs = nil
		recordLLMUsage(ctx, c)
	}
}

// recordLLMCallUsage is CallLLM's turn: the streamed request it makes, plus
// every request its driver failed and retried along the way.
func (a *CallLLMActivity) recordLLMCallUsage(ctx context.Context, chat *db.Chat, provider, modelID string, resolved *resolvedLLMCall, latencyMs int64, usage llm.TokenUsage, requestID string, callErr error, interrupted bool, retries *driverRetries) {
	call := llmUsageCall{
		Recorder: a.usage, UserID: chat.UserID, ChatID: chat.ID, ProjectID: chat.ProjectID,
		Purpose: store.LLMPurposeChat, Ordinal: 1,
		Provider: provider, Model: modelID, AccountID: usageAccountID(resolved), LatencyMs: latencyMs,
		OrgID: chatOrgID(chat), Funding: usageFunding(resolved, provider), FundingOrgID: usageFundingOrg(resolved),
		Usage: usage, ProviderRequestID: requestID, Err: callErr, Interrupted: interrupted,
	}
	if retries != nil {
		recordDriverRetries(ctx, call, retries)
		call.LatencyMs = retries.finalLatencyMs(latencyMs)
		call.TTFTMs = retries.ttftMs()
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
	recorder  LLMUsageRecorder
	userID    string
	chatID    string
	projectID string
	purpose   string
	n         int
}

// newUsageMeter meters one execution's requests for purpose. A nil recorder
// (an activity built without a store, in tests) still streams, unrecorded.
func newUsageMeter(recorder LLMUsageRecorder, userID, chatID, purpose string) *usageMeter {
	return &usageMeter{recorder: recorder, userID: userID, chatID: chatID, purpose: purpose}
}

// inProject records the meter's requests against projectID, the project of
// its chat.
func (m *usageMeter) inProject(projectID string) *usageMeter {
	m.projectID = projectID
	return m
}

// streamAndAccumulate sends one request to resolved's driver, accumulates the
// streamed reply and records the request's usage, whether it succeeded or not.
// A request that fails after the provider reported usage records that usage:
// those tokens were consumed.
func (m *usageMeter) streamAndAccumulate(ctx context.Context, resolved *resolvedLLMCall, prompts []string, messages []message.Message, available []tools.Tool) (*llm.DriverResponse, error) {
	m.n++
	retries := newDriverRetries()
	callStart := retries.start
	resp, err := accumulator.StreamAndAccumulateObserving(ctx, resolved.Driver, prompts, messages, available, retries.observeEvent)
	callLatencyMs := retries.finish().Sub(callStart).Milliseconds()
	call := llmUsageCall{
		Recorder: m.recorder, UserID: m.userID, ChatID: m.chatID, ProjectID: m.projectID,
		Purpose: m.purpose, Ordinal: m.n,
		Provider: usageProvider(resolved, resolved.Driver), Model: resolvedUsageModel(resolved),
		AccountID: usageAccountID(resolved),
		OrgID:     resolved.OrgID, Funding: usageFunding(resolved, usageProvider(resolved, resolved.Driver)),
		FundingOrgID: usageFundingOrg(resolved),
	}
	recordDriverRetries(ctx, call, retries)
	call.LatencyMs = retries.finalLatencyMs(callLatencyMs)
	call.TTFTMs = retries.ttftMs()
	call.Err = err
	if err != nil && ctx.Err() != nil {
		// A driver that stops because its context ended closes the stream
		// without a final event, which the accumulator reports as a stream that
		// ended early. The cause is the context: a cancel or a deadline.
		call.Err = ctx.Err()
	}
	if resp != nil {
		call.Usage = resp.Usage
		call.ProviderRequestID = resp.UpstreamRequestID
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
	case errors.As(err, &stall), errors.Is(err, llm.ErrStreamProgressTimeout), errors.Is(err, llm.ErrStreamFirstTokenTimeout):
		return "stalled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, drivererrors.ErrStreamEndedEarly):
		return "network"
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
// usageAccountID is the provider account that served resolved: the routed
// account, or "" when the call was not routed to one (a local model, a
// provider with no accounts, routing off).
func usageAccountID(resolved *resolvedLLMCall) string {
	if resolved == nil || resolved.Account == nil {
		return ""
	}
	return resolved.Account.Config.AccountID
}

// usageFunding is who paid for a call (llm_call_usage.funding): the routed
// account's funding class, "local" for a model on the person's own hardware,
// and "" when it is not known (an unrouted call of a provider with accounts).
func usageFunding(resolved *resolvedLLMCall, provider string) string {
	switch {
	case resolved != nil && resolved.Account != nil:
		return resolved.Account.Config.Funding
	case provider == "local":
		return models.FundingLocal
	}
	return ""
}

// usageFundingOrg is the org that paid for a call: the org owning the routed
// account, or the org it is attributed to (a seat, the managed key minted in
// that org). "" for a person's own account and for the org-less managed key,
// which bills the person's default org, not necessarily the chat's.
func usageFundingOrg(resolved *resolvedLLMCall) string {
	if resolved == nil || resolved.Account == nil {
		return ""
	}
	cfg := resolved.Account.Config
	if cfg.AccountOrgID != "" {
		return cfg.AccountOrgID
	}
	return cfg.AttributedOrgID
}

func usageProvider(resolved *resolvedLLMCall, driver llm.Driver) string {
	if resolved.ProviderDriver != "" {
		return resolved.ProviderDriver
	}
	return driver.Name()
}
