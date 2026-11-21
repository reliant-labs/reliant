// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// contextOverflowShrink is how far each resend after a context-window refusal
// shrinks the request: the share of the refused request's size (by the trim
// backstop's own estimate, capped at the model's prompt ceiling) the next
// request is trimmed to. One entry per resend, so its length is the cap:
// three resends, four requests at most. The steps follow opencode, which
// shrinks to 0.85, 0.7 and then 0.5 of the window on successive refusals.
var contextOverflowShrink = []float64{0.85, 0.7, 0.5}

// overflowCountsMargin scales the shrink a refusal's own token counts call for
// (limit/actual): characters are only a proxy for the provider's tokens, and
// the counts are of a request we will change.
const overflowCountsMargin = 0.9

// contextOverflowRecovery resends a request the provider refused as larger
// than the model's context window, smaller each time, before giving up.
//
// Why here, inside the call: compaction is the primary context manager and
// runs between turns (the agent loop's execute_tools → compact edge), but a
// refusal arrives mid-turn, and the loop has no edge from call_llm to compact.
// Adding one is a workflow change every custom workflow would have to copy,
// and a turn whose call ended with no message would otherwise fall out of the
// loop. Retrying inside the activity needs no workflow change at all, and the
// mechanism it reuses is the trim backstop that already runs on every request
// (prepareHistoryForLLM), only with a smaller target. The backstop sits at 95%
// of the window by its own estimate; a refusal is proof the estimate was low
// for this request (characters are a proxy for tokens, a stored count came
// from another provider's tokenizer, or the catalog's window is wrong), so
// each resend trims further below what was refused.
//
// The trim rewrites only tool output and only in the request: the stored
// conversation is untouched. A recovered turn's provider-reported size is
// then near the window, which is what makes the loop's compaction edge fire
// after this turn's tools, so the next turns start from a compacted thread
// rather than paying for a refusal each time.
//
// A request is resent only when the refusal came before any output was
// streamed (a resend would duplicate what the user already watched stream),
// and only when the trim actually made it smaller; otherwise the turn fails
// with an explanation that wraps drivererrors.ErrContextOverflow, which the
// runtime classifies as terminal.
type contextOverflowRecovery struct {
	chatID        string
	provider      string
	model         string
	pristine      []message.Message
	systemPrompts []string
	tools         []tools.Tool
	ceiling       int64

	// prepared is the request as first sent, sent the last one sent, and
	// resends how many smaller requests followed it.
	prepared []message.Message
	sent     []message.Message
	resends  int
}

// newContextOverflowRecovery snapshots history BEFORE prepareHistoryForLLM
// trims it in place, so every resend trims the original rather than stacking
// trims on trims. Hand it the prepared request with sending.
func newContextOverflowRecovery(chatID, provider, model string, history []message.Message, systemPrompts []string, availableTools []tools.Tool, ceiling int64) *contextOverflowRecovery {
	return &contextOverflowRecovery{
		chatID:        chatID,
		provider:      provider,
		model:         model,
		pristine:      cloneMessagesForTrim(history),
		systemPrompts: systemPrompts,
		tools:         availableTools,
		ceiling:       ceiling,
	}
}

// cloneMessagesForTrim copies the messages and their Parts slices: trimming
// replaces parts in place, and the snapshot must not see it.
// sending records the request as first sent, the one a refusal is measured
// against.
func (r *contextOverflowRecovery) sending(prepared []message.Message) []message.Message {
	r.prepared = prepared
	return prepared
}

func cloneMessagesForTrim(history []message.Message) []message.Message {
	out := make([]message.Message, len(history))
	for i, m := range history {
		m.Parts = append([]message.ContentPart(nil), m.Parts...)
		out[i] = m
	}
	return out
}

// contextOverflowRetryReason is the RetryWait.Reason a refused request is
// recorded under in the usage ledger.
const contextOverflowRetryReason = "context_overflow"

// resend is called with the error that ended the stream refused. When err is
// a context-window refusal of a request that produced no output, and a
// smaller request can be made, it records the refused request as a retried one
// (the usage ledger's row for it), starts the smaller request and returns its
// stream. Otherwise it returns a nil stream and the error the turn fails with:
// err itself, or, for an overflow nothing more can be done about, an
// explanation wrapping it.
//
// It runs on CallLLM's stream loop, in place of the driver call, so the turn's
// events are read straight from the driver as before: there is no relay hop
// between them, which matters because CallLLM drains events already produced
// when the turn is cancelled.
func (r *contextOverflowRecovery) resend(ctx context.Context, driver llm.Driver, refused <-chan llm.DriverEvent, err error, state *streamProcessingState) (<-chan llm.DriverEvent, error) {
	if !drivererrors.IsContextOverflow(err) {
		return nil, err
	}
	refusal := markedOverflow(err)
	if producedOutput(state) {
		// The user watched that output stream in; a resend would repeat it.
		// The turn then fails as any provider error after output does: the
		// activity returns the error, nothing of the partial is saved (only
		// an interrupted turn persists its partial, persistInterruptedTurn),
		// so a retry starts from the history as it was. The refused stream is
		// drained to close here, keeping what the provider billed for it, so
		// the driver always finishes and releases its stream.
		if usage := drainRefused(refused); usage != nil {
			state.usage = *usage
		}
		return nil, refusal
	}
	if r.sent == nil {
		r.sent = r.prepared
	}
	next, targetTokens := r.shrink(r.resends, refusal, r.sent)
	if next == nil {
		return nil, r.exhausted(refusal, r.resends)
	}
	r.resends++

	var billed *llm.TokenUsage
	if usage := drainRefused(refused); usage != nil {
		billed = llm.FailedAttemptUsage(*usage)
	}
	if state.retries != nil {
		state.retries.observe(llm.RetryWait{
			Attempt:     r.resends,
			MaxAttempts: len(contextOverflowShrink) + 1,
			StatusCode:  drivererrors.StatusFromText(refusal.Error()),
			Reason:      contextOverflowRetryReason,
			Usage:       billed,
		})
	}
	logging.WarnContext(ctx, "[CallLLM] Provider refused the request as larger than the context window; resending with tool output trimmed further",
		"chatID", r.chatID,
		"provider", r.provider,
		"model", r.model,
		"resend", r.resends,
		"maxResends", len(contextOverflowShrink),
		"targetTokens", targetTokens,
		"error", refusal)
	r.sent = next
	return driver.StreamResponse(ctx, r.systemPrompts, next, r.tools), nil
}

// drainRefused reads a refused stream to its close and returns the usage its
// complete event reported, if any. A driver closes its stream right after its
// error, after reporting what the provider billed for the refused request, if
// anything (llm.EndStreamWithError).
func drainRefused(refused <-chan llm.DriverEvent) *llm.TokenUsage {
	var usage *llm.TokenUsage
	for ev := range refused {
		if ev.Type == llm.EventComplete && ev.Response != nil {
			u := ev.Response.Usage
			usage = &u
		}
	}
	return usage
}

// producedOutput reports whether the stream has delivered anything the user
// sees: a content, thinking or tool block, or text.
func producedOutput(state *streamProcessingState) bool {
	return (state.blockStates != nil && state.blockStates.position > 0) ||
		len(state.textParts) > 0 || len(state.thinkingParts) > 0 || len(state.toolCalls) > 0
}

// markedOverflow gives a refusal recognised by its text (one that did not pass
// a production driver's marking) the ErrContextOverflow identity, so the turn's
// error is classified by identity like every other.
func markedOverflow(err error) error {
	text := err.Error()
	return drivererrors.MarkContextOverflow(err, drivererrors.ProviderFailure{Status: drivererrors.StatusFromText(text), Message: text})
}

// shrink returns the request to resend after a refusal of prev, or nil when
// there is no resend left or trimming cannot make it smaller.
func (r *contextOverflowRecovery) shrink(resend int, refusal error, prev []message.Message) ([]message.Message, int64) {
	if resend >= len(contextOverflowShrink) {
		return nil, 0
	}
	// What the provider refused, by the backstop's measure: never more than
	// the ceiling, and no more than the request itself.
	base := r.ceiling
	if base <= 0 {
		// The window the legacy, model-unaware backstop assumes.
		base = message.MaxContextTokens
	}
	toolDefs := wrapToolsForEstimation(r.tools)
	if est := int64(message.EstimateRequestTokens(r.pristine, r.systemPrompts, toolDefs, r.ceiling)); est > 0 && est < base {
		base = est
	}
	fraction := contextOverflowShrink[resend]
	if actual, limit := refusalCounts(refusal); actual > limit && limit > 0 {
		fraction *= min(1, float64(limit)/float64(actual)*overflowCountsMargin/contextOverflowShrink[0])
	}
	target := int64(float64(base) * fraction)
	// prepareHistoryForLLM trims to TrimBackstopFraction of the window it is
	// given, so hand it the window whose backstop is the target.
	window := max(int64(float64(target)/message.TrimBackstopFraction), 1)
	next := prepareHistoryForLLM(r.chatID, cloneMessagesForTrim(r.pristine), r.systemPrompts, r.tools, window)
	if message.MessagesChars(next) >= message.MessagesChars(prev) {
		return nil, 0
	}
	return next, target
}

// refusalCounts reads the prompt size and window a refusal states.
func refusalCounts(err error) (actual, limit int) {
	var coe *drivererrors.ContextOverflowError
	if errors.As(err, &coe) && coe.Limit > 0 {
		return coe.Actual, coe.Limit
	}
	return drivererrors.OverflowTokenCounts(err.Error())
}

// exhausted is the turn's error when no resend fits: what happened, what was
// tried, and what the user can do, wrapping the provider's refusal (and with
// it drivererrors.ErrContextOverflow).
func (r *contextOverflowRecovery) exhausted(refusal error, resends int) error {
	tried := "and there is no tool output left in the request to trim"
	if resends > 0 {
		tried = fmt.Sprintf("and it still did not fit after resending it %d time(s) with tool output trimmed", resends)
	}
	return fmt.Errorf("the conversation is too long for %s: %s refused the request as larger than the model's context window, %s. "+
		"Start a new chat, or switch to a model with a larger context window: %w", modelLabel(r.model), providerLabel(r.provider), tried, refusal)
}

func modelLabel(model string) string {
	if model == "" {
		return "this model"
	}
	return model
}

func providerLabel(provider string) string {
	if provider == "" {
		return "the provider"
	}
	return provider
}
