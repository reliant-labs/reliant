// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"time"

	"github.com/reliant-labs/reliant/internal/llm"
)

// firstTokenGuard bounds how long CallLLM's stream may run before the
// provider's first output token. It is armed once per request and disarmed for
// good by the first token: after that the inter-event guards (byte idle,
// content stall, progress) own the stream.
//
// Only events that carry model output count. ThinkingStart, ContentStart and
// warnings are framing the provider can send while producing nothing, which is
// exactly the trickle that kept the other guards from firing.
type firstTokenGuard struct {
	timeout time.Duration
	timer   *time.Timer
	armed   bool
}

func newFirstTokenGuard(timeout time.Duration) *firstTokenGuard {
	return &firstTokenGuard{timeout: timeout, timer: time.NewTimer(timeout), armed: true}
}

// C is the channel to select on; nil (never ready) once disarmed.
func (g *firstTokenGuard) C() <-chan time.Time {
	if !g.armed {
		return nil
	}
	return g.timer.C
}

// observe updates the guard for one driver event.
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
		g.rearm(delay + g.timeout)
	case llm.EventContentDelta, llm.EventThinkingDelta,
		llm.EventToolUseStart, llm.EventToolUseDelta, llm.EventToolUseStop,
		// A completion or an error ends the stream either way; neither is a
		// stall for this guard to report.
		llm.EventComplete, llm.EventError:
		g.disarm()
	}
}

// expired is called when C fires and reports whether the stream must be cut.
//
// A provider that opened a block and keeps it alive with keepalives is
// generating (a redacted thinking block emits no token until it closes), so a
// keepalive read within the last timeout defers the deadline to one timeout
// after that keepalive. The guard stays armed: once the keepalives stop, it
// fires; while they continue, the transport's mid-block deadline bounds the
// open block. A liveness mark older than the timeout exempts nothing.
func (g *firstTokenGuard) expired(liveness *llm.StreamLiveness) bool {
	if sinceKeepalive, ok := liveness.Since(); ok && sinceKeepalive < g.timeout {
		g.rearm(g.timeout - sinceKeepalive)
		return false
	}
	return true
}

func (g *firstTokenGuard) rearm(d time.Duration) {
	if !g.timer.Stop() {
		select {
		case <-g.timer.C:
		default:
		}
	}
	g.timer.Reset(d)
}

func (g *firstTokenGuard) disarm() {
	g.armed = false
	g.timer.Stop()
}

func (g *firstTokenGuard) stop() { g.timer.Stop() }
