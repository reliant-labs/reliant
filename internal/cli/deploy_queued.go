package cli

// A QUEUED hosted deploy: accepted, recorded, and waiting on a person.
//
// When the control plane accepts a deploy that needs something only a human
// can provide — today, billing for the hosted workloads or database it runs —
// it no longer refuses it. The release, the bundle and the promotion are
// recorded exactly as for any deploy, the promotion carries a HOLD, and the
// platform applies it on its own the moment the hold clears (the billing
// webhook wakes it). Nobody re-runs anything.
//
// So forge's job is to SAY that, once, in one block a person or an agent can
// act on — what it waits on, why, the exact place to fix it, and that nothing
// needs re-running — and to exit 7, which no other outcome uses. The logic of
// what is held and why lives in the control plane: forge renders the hold's
// own reason, fix and link, and never decides any of them.

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/deploytarget"
)

// queuedHoldsReader is the one thing followPromote needs from a ledger: what
// the WRITE said a promotion is queued on. Declared here at the consumer;
// hostedStore satisfies it, a file ledger does not (nothing queues there).
type queuedHoldsReader interface {
	QueuedHolds(promotionID string) []deploytarget.HostedHold
}

// recordedHolds is what the promotion this command just recorded is waiting
// on, read off the Promote response. Nil when nothing holds it, when there was
// no write (a plan), or when the ledger cannot say.
func recordedHolds(ledger envLedger, plan promotePlan) []deploytarget.HostedHold {
	reader, ok := ledger.Bindings.(queuedHoldsReader)
	if !ok || plan.Recorded == nil {
		return nil
	}
	return reader.QueuedHolds(plan.Recorded.ID)
}

// deployQueuedError is a deploy (or a wait, or a status read) that found the
// env's promotion QUEUED. It is an error only so the process exits 7 and the
// --json envelope says so; its message is the block a person reads.
type deployQueuedError struct {
	Env         string
	Release     string
	PromotionID string
	Holds       []deploytarget.HostedHold
	// WaitedFor is set when a `--wait` ran out of budget while still
	// queued, so the block says the wait ended rather than that it never
	// started.
	WaitedFor time.Duration
}

func (e *deployQueuedError) ExitCode() int { return exitQueued }

func (e *deployQueuedError) Error() string {
	var b strings.Builder
	release := "the current release"
	if e.Release != "" {
		release = "release " + e.Release
	}
	if e.WaitedFor > 0 {
		fmt.Fprintf(&b, "deploy to %s is still QUEUED after %s: %s is recorded and waits on %s.",
			e.Env, e.WaitedFor, release, queuedLabel(e.Holds))
	} else {
		fmt.Fprintf(&b, "deploy to %s is QUEUED, not failed: %s is recorded and waits on %s.",
			e.Env, release, queuedLabel(e.Holds))
	}
	var links []string
	for _, h := range e.Holds {
		if h.Reason != "" {
			fmt.Fprintf(&b, "\n  why   %s", h.Reason)
		}
		if h.Fix != "" {
			fmt.Fprintf(&b, "\n  do    %s", h.Fix)
		}
		if h.ActionURL != "" && !listHas(links, h.ActionURL) {
			links = append(links, h.ActionURL)
		}
	}
	for _, l := range links {
		fmt.Fprintf(&b, "\n  open  %s", l)
	}
	fmt.Fprintf(&b, "\n  then  nothing to re-run: it deploys on its own once that is done. "+
		"To block until it is live: forge env status %s --wait", e.Env)
	b.WriteString("\n  (exit 7: queued on a person, not a failure)")
	return b.String()
}

// queuedLabel names what the holds wait on: "billing", or the kinds joined.
func queuedLabel(holds []deploytarget.HostedHold) string {
	var labels []string
	for _, h := range holds {
		if l := h.Label(); !listHas(labels, l) {
			labels = append(labels, l)
		}
	}
	if len(labels) == 0 {
		return "a human action"
	}
	return strings.Join(labels, " and ")
}

func listHas(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// queuedOf extracts a queued outcome from an error chain: the CLI's own, or
// the hosted provider's (a publish whose wait found the promotion HELD).
func queuedOf(err error) *deployQueuedError {
	var queued *deployQueuedError
	if errors.As(err, &queued) {
		return queued
	}
	var held *deploytarget.HeldError
	if errors.As(err, &held) {
		return &deployQueuedError{Env: held.Env, Release: held.Release, PromotionID: held.PromotionID, Holds: held.Holds}
	}
	return nil
}

// queuedHoldJSON is one hold in forge's --json documents. snake_case like
// every forge document, and every field the control plane sent — a pipeline
// that branches on exit 7 reads action_url from here rather than scraping the
// block.
type queuedHoldJSON struct {
	Kind             string     `json:"kind"`
	Reason           string     `json:"reason,omitempty"`
	Fix              string     `json:"fix,omitempty"`
	ActionURL        string     `json:"action_url,omitempty"`
	CallerCanResolve bool       `json:"caller_can_resolve"`
	PromotionID      string     `json:"promotion_id,omitempty"`
	HeldSince        *time.Time `json:"held_since,omitempty"`
}

// deployQueuedJSON is the `queued` object of the deploy and wait documents.
type deployQueuedJSON struct {
	Env         string           `json:"env"`
	Release     string           `json:"release,omitempty"`
	PromotionID string           `json:"promotion_id,omitempty"`
	WaitingOn   string           `json:"waiting_on"`
	Holds       []queuedHoldJSON `json:"holds"`
}

func queuedHoldsJSON(holds []deploytarget.HostedHold) []queuedHoldJSON {
	out := make([]queuedHoldJSON, 0, len(holds))
	for _, h := range holds {
		out = append(out, queuedHoldJSON{
			Kind: h.Label(), Reason: h.Reason, Fix: h.Fix, ActionURL: h.ActionURL,
			CallerCanResolve: h.CallerCanResolve, PromotionID: h.PromotionID, HeldSince: h.HeldSince,
		})
	}
	return out
}

func (e *deployQueuedError) document() *deployQueuedJSON {
	return &deployQueuedJSON{
		Env: e.Env, Release: e.Release, PromotionID: e.PromotionID,
		WaitingOn: queuedLabel(e.Holds), Holds: queuedHoldsJSON(e.Holds),
	}
}
