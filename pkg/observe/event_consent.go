package observe

import (
	"context"
	"log/slog"
	"sync/atomic"
)

// ConsentSource is the app's server-side consent lookup for product events:
// the one place a subject's opt-out is honoured before an event leaves the
// process. Register it once at startup with SetConsentSource. Without one,
// every event is recorded (the browser's own tiers still apply to browser
// events).
//
// It fails closed: an error, or a panic, drops the event. It is called for
// every Track and TrackAuth, so it should answer from memory or a cache.
type ConsentSource interface {
	// ProductEventsAllowed reports whether events about a may be recorded.
	// a is the event's actor (ActorFrom): any of its ids may be empty.
	ProductEventsAllowed(ctx context.Context, a Actor) (bool, error)
}

// ConsentFunc adapts a function to ConsentSource.
type ConsentFunc func(ctx context.Context, a Actor) (bool, error)

// ProductEventsAllowed calls f.
func (f ConsentFunc) ProductEventsAllowed(ctx context.Context, a Actor) (bool, error) {
	return f(ctx, a)
}

type consentHolder struct{ src ConsentSource }

var consentSource atomic.Pointer[consentHolder]

// SetConsentSource registers the process's consent lookup; nil removes it.
//
//	observe.SetConsentSource(observe.ConsentFunc(func(ctx context.Context, a observe.Actor) (bool, error) {
//		return !optOuts.Has(a.UserID), nil
//	}))
func SetConsentSource(s ConsentSource) {
	if s == nil {
		consentSource.Store(nil)
		return
	}
	consentSource.Store(&consentHolder{src: s})
}

// consentRefused is set once the first failed consent lookup has been
// warned about.
var consentRefused atomic.Bool

// consentAllows asks the registered source, failing closed.
func consentAllows(ctx context.Context, name string, a Actor) (allowed bool) {
	h := consentSource.Load()
	if h == nil {
		return true
	}
	defer func() {
		if r := recover(); r != nil {
			allowed = false
			warnConsentFailed(ctx, name, consentFailurePanic)
		}
	}()
	ok, err := h.src.ProductEventsAllowed(ctx, a)
	if err != nil {
		warnConsentFailed(ctx, name, consentFailureError)
		return false
	}
	return ok
}

// How a consent lookup failed, as its WARN says it. Only the class: the
// source's error text or panic value can hold whatever it was looking up (a
// user id, an email, a credential), and this warning is telemetry too.
const (
	consentFailureError = "error"
	consentFailurePanic = "panic"
)

func warnConsentFailed(ctx context.Context, name, failure string) {
	if consentRefused.CompareAndSwap(false, true) {
		FromContext(ctx).WarnContext(ctx, "product event dropped: the consent lookup failed, and consent fails closed",
			slog.String("event", name), slog.String("failure", failure))
	}
}
