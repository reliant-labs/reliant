package observe

import (
	"context"
	"log/slog"
)

// Product events that name a user without being something the user did. A
// daily billing snapshot, a staff marker or an account the platform created on
// a user's behalf is attributed to a user (it carries user.id, so a dashboard
// can join it), but it is not activity: counting it would make every account
// with a subscription "active" every day. Such an event carries the reserved
// prop PropKind = KindState, and the user-activity predicate
// (pkg/obsquery.UserActivityEvidence) leaves every row that carries it out of the
// active, new and returning counts: the Users dashboard, the Overview's Users
// card and `forge obs users` alike.
const (
	// PropKind is the reserved product prop (stored as Props['forge.kind'])
	// that says what kind of fact an event records. Unset is an action: the
	// user did something.
	PropKind = "forge.kind"
	// KindState marks an event as a fact about a user (a snapshot, a marker),
	// not something they did.
	KindState = "state"
)

// TrackState records a product event that is attributed to a user but is not
// activity: Track, with PropKind = KindState appended to its props. Use it for
// state snapshots ("billing_subscription_state"), markers ("internal_user")
// and records the platform writes on a user's behalf; use Track for anything
// the user did. A KindState event still counts as an event (Events by name,
// funnels, counts): only the user-activity counts skip it.
//
//	observe.TrackState(ctx, "billing_subscription_state", slog.String("plan", "pro"))
func TrackState(ctx context.Context, name string, attrs ...slog.Attr) {
	Track(WithStateEvents(ctx), name, attrs...)
}

// TrackStateAs is TrackState for a caller with no signed-in user to read: it
// names the user itself (a worker emitting a daily marker per staff member,
// a job writing a snapshot). "" attributes the event to no one.
//
//	observe.TrackStateAs(ctx, member.UserID, "internal_user", slog.String("reason", "email_domain"))
func TrackStateAs(ctx context.Context, userID, name string, attrs ...slog.Attr) {
	if ctx == nil {
		ctx = context.Background()
	}
	trackAs(WithStateEvents(ctx), name, Actor{UserID: userID}, AppClientBackend, attrs)
}

type stateEventsKey struct{}

// WithStateEvents marks every product event recorded under the returned
// context as a state event (PropKind = KindState): not the user's activity.
// It is how a caller marks events it does not emit itself, per call: Track
// and TrackAuth, and the ones forge records on the app's behalf (the login
// broker's sign-ins and sign-ups in pkg/devidp, the generated CRUD events).
// Use it for a synthetic monitor's sign-in, or a job that writes through the
// generated handlers on a user's behalf. The events are still recorded and
// counted as events; only the user counts skip them.
//
//	ctx = observe.WithStateEvents(ctx) // the identity canary's login
func WithStateEvents(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, stateEventsKey{}, true)
}

// StateEventsFrom reports whether ctx marks its product events as state
// events (WithStateEvents).
func StateEventsFrom(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	marked, _ := ctx.Value(stateEventsKey{}).(bool)
	return marked
}

// StateAttrs is attrs with PropKind = KindState in front, for an emitter that
// writes the record through its own wrapper (one that names the user itself)
// rather than through TrackState. The marker comes first, so the
// MaxProductProps bound never drops it, and a caller's own top-level
// forge.kind is dropped, so the record carries one.
func StateAttrs(attrs []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, 0, len(attrs)+1)
	out = append(out, slog.String(PropKind, KindState))
	for _, a := range attrs {
		if a.Key != PropKind {
			out = append(out, a)
		}
	}
	return out
}
