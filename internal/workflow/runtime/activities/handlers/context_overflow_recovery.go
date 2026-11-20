// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"fmt"
	"time"

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
// relayed (a resend would duplicate what the user already watched stream),
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
}

// newContextOverflowRecovery snapshots history BEFORE prepareHistoryForLLM
// trims it in place, so every resend trims the original rather than stacking
// trims on trims.
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
func cloneMessagesForTrim(history []message.Message) []message.Message {
	out := make([]message.Message, len(history))
	for i, m := range history {
		m.Parts = append([]message.ContentPart(nil), m.Parts...)
		out[i] = m
	}
	return out
}

// stream sends history (already prepared for the request) and relays the
// driver's events, resending smaller after a context-window refusal.
func (r *contextOverflowRecovery) stream(ctx context.Context, driver llm.Driver, history []message.Message) <-chan llm.DriverEvent {
	out := make(chan llm.DriverEvent)
	go func() {
		defer close(out)
		sent := history
		for resend := 0; ; resend++ {
			refusal, delivered := relayUntilOverflow(ctx, driver.StreamResponse(ctx, r.systemPrompts, sent, r.tools), out)
			if refusal == nil || !delivered {
				return
			}
			next, targetTokens := r.shrink(resend, refusal, sent)
			if next == nil {
				deliverRelayedEvent(ctx, out, llm.DriverEvent{Type: llm.EventError, Error: r.exhausted(refusal, resend)})
				return
			}
			logging.WarnContext(ctx, "[CallLLM] Provider refused the request as larger than the context window; resending with tool output trimmed further",
				"chatID", r.chatID,
				"provider", r.provider,
				"model", r.model,
				"resend", resend+1,
				"maxResends", len(contextOverflowShrink),
				"targetTokens", targetTokens,
				"error", refusal)
			sent = next
		}
	}()
	return out
}

// relayUntilOverflow forwards in's events to out until in closes. A
// context-window refusal that arrives before any output is not forwarded: it
// is returned, and the rest of in is discarded. delivered is false when the
// consumer stopped receiving.
func relayUntilOverflow(ctx context.Context, in <-chan llm.DriverEvent, out chan<- llm.DriverEvent) (refusal error, delivered bool) {
	defer func() {
		// Let the driver finish and release its stream whatever happens here.
		go func() {
			for range in { //nolint:revive // draining
			}
		}()
	}()
	produced := false
	for ev := range in {
		if ev.Type == llm.EventError && !produced && drivererrors.IsContextOverflow(ev.Error) {
			return markedOverflow(ev.Error), true
		}
		if ev.Type != llm.EventRetryWait && ev.Type != llm.EventWarning {
			produced = true
		}
		if !deliverRelayedEvent(ctx, out, ev) {
			return nil, false
		}
	}
	return nil, true
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

// relayConsumerGrace is how long a send waits for a receiver once ctx is
// done before the consumer is treated as gone. CallLLM keeps reading events
// already produced after it cancels, so a done ctx alone is not a gone
// consumer (the same rule as the drivers' own relay). A var only for tests.
var relayConsumerGrace = 5 * time.Second

func deliverRelayedEvent(ctx context.Context, out chan<- llm.DriverEvent, ev llm.DriverEvent) bool {
	select {
	case out <- ev:
		return true
	case <-ctx.Done():
	}
	grace := time.NewTimer(relayConsumerGrace)
	defer grace.Stop()
	select {
	case out <- ev:
		return true
	case <-grace.C:
		return false
	}
}
