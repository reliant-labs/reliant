// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"time"

	"github.com/reliant-labs/reliant/internal/llm"
)

// keepaliveReport is what the first-token guard needs from the transport's
// llm.StreamLiveness: when it last read a keepalive inside an open content
// block.
type keepaliveReport interface {
	Last() (time.Time, bool)
}

// firstTokenGuard bounds how long CallLLM's stream may run before the
// provider's first output token: a text, thinking or tool-call token. It is
// armed once per request and disarmed for good by the first token; after that
// the inter-event guards (byte idle, content stall, progress) own the stream.
//
// ThinkingStart, ContentStart and warnings are framing the provider can send
// while producing nothing, which is exactly the trickle that kept the other
// guards from firing, so they never count as output.
//
// The rule, in full:
//
//   - The deadline is fixed when the request is sent, one timeout later.
//     Nothing the stream sends moves it, keepalives included. The one
//     exception is a driver RetryWait, which announces a NEW request: the
//     deadline restarts for that request, after the backoff.
//   - Output before the deadline disarms the guard.
//   - At the deadline the stream is cut, unless it is held: the transport read
//     a keepalive inside an open content block within the last hold window.
//     That is a provider generating a block it does not stream (a redacted
//     thinking block emits pings and no token until it closes; prod turns
//     spent 230-301s there, see llm.DefaultStreamMidBlockStallTimeout), and
//     cutting it would only repeat the same thinking on every retry.
//   - A held stream stays held only while keepalives keep coming with no gap
//     of a hold window or more; the first such gap cuts it.
//   - The hold has an overall cap (llm.StreamFirstTokenHoldCap, 10 minutes)
//     counted from when the request was sent: at the cap the stream is cut
//     however fresh its keepalives are. The cap restarts with the deadline on
//     a RetryWait.
//   - Output after the deadline is accepted only while the stream is held:
//     it is the output of the block the hold was for. On a stream that is not
//     held, or past the cap, it is late, and cuts the stream even if it was
//     already queued.
//
// Why a hold rather than a hard deadline (owner decision 2026-11-04, reliant
// #2145): Claude's redacted thinking sends 230-301s of keepalives before its
// first token, so a hard three-minute cut would kill real calls and repeat
// the same thinking on every retry.
//
// The hold window is the byte-idle timeout: the idle reader already cuts a
// stream silent that long, so a live block cannot go longer between pings.
type firstTokenGuard struct {
	timeout time.Duration
	hold    time.Duration
	holdCap time.Duration
	now     func() time.Time
	// timer wakes CallLLM's select at the deadline, and while held at the
	// moment the last keepalive goes stale or the cap is reached. Every
	// decision is made against deadline, capAt and now, never against
	// whether the timer has delivered yet.
	timer    *time.Timer
	deadline time.Time
	// capAt ends any hold: holdCap after the request was sent.
	capAt time.Time
	// held is set when the deadline passed with a fresh keepalive.
	held  bool
	armed bool
}

// newFirstTokenGuard arms a guard for a request sent now. A nil now is
// time.Now. A holdCap shorter than timeout is raised to it.
func newFirstTokenGuard(timeout, hold, holdCap time.Duration, now func() time.Time) *firstTokenGuard {
	if now == nil {
		now = time.Now
	}
	holdCap = max(holdCap, timeout)
	sent := now()
	return &firstTokenGuard{
		timeout:  timeout,
		hold:     hold,
		holdCap:  holdCap,
		now:      now,
		timer:    time.NewTimer(timeout),
		deadline: sent.Add(timeout),
		capAt:    sent.Add(holdCap),
		armed:    true,
	}
}

// C is the channel to select on; nil (never ready) once disarmed.
func (g *firstTokenGuard) C() <-chan time.Time {
	if !g.armed {
		return nil
	}
	return g.timer.C
}

// admit is called for every event CallLLM receives, before it processes the
// event, and reports whether the stream must be cut instead. The deadline is
// decided first, so an output token queued behind it cannot disarm the guard
// and pass as timely; and because it runs per event, a channel that is always
// ready cannot starve the select case on C.
func (g *firstTokenGuard) admit(event llm.DriverEvent, keepalives keepaliveReport) bool {
	if g.cut(keepalives) {
		return true
	}
	g.observe(event)
	return false
}

// fire is called when C delivers, and reports whether the stream must be cut.
func (g *firstTokenGuard) fire(keepalives keepaliveReport) bool {
	if g.cut(keepalives) {
		return true
	}
	if g.armed && !g.held {
		// Woke before the deadline (it restarted for a retried request).
		g.wakeAt(g.deadline)
	}
	return false
}

// cut applies the rule above at this instant. While held it schedules the
// next wake for when the last keepalive goes stale.
func (g *firstTokenGuard) cut(keepalives keepaliveReport) bool {
	if !g.armed {
		return false
	}
	now := g.now()
	if now.Before(g.deadline) {
		return false
	}
	if !now.Before(g.capAt) {
		return true
	}
	last, ok := keepalives.Last()
	if !ok || now.Sub(last) >= g.hold {
		return true
	}
	g.held = true
	wake := last.Add(g.hold)
	if g.capAt.Before(wake) {
		wake = g.capAt
	}
	g.wakeAt(wake)
	return false
}

// observe updates the guard for one driver event that admit let through.
func (g *firstTokenGuard) observe(event llm.DriverEvent) {
	if !g.armed {
		return
	}
	switch event.Type {
	case llm.EventRetryWait:
		// The driver is about to sleep in its provider retry ladder and then
		// send a fresh request. The deadline belongs to that request, so it
		// restarts after the wait rather than charging the backoff to it.
		delay := time.Duration(0)
		if event.Retry != nil {
			delay = event.Retry.Delay
		}
		sent := g.now().Add(delay)
		g.deadline = sent.Add(g.timeout)
		g.capAt = sent.Add(g.holdCap)
		g.held = false
		g.wakeAt(g.deadline)
	case llm.EventContentDelta, llm.EventThinkingDelta,
		llm.EventToolUseStart, llm.EventToolUseDelta, llm.EventToolUseStop,
		// A completion or an error ends the stream either way; neither is a
		// stall for this guard to report.
		llm.EventComplete, llm.EventError:
		g.armed = false
		g.timer.Stop()
	}
}

func (g *firstTokenGuard) wakeAt(t time.Time) {
	if !g.timer.Stop() {
		select {
		case <-g.timer.C:
		default:
		}
	}
	g.timer.Reset(max(t.Sub(g.now()), 0))
}

func (g *firstTokenGuard) stop() { g.timer.Stop() }
