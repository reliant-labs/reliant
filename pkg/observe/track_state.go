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
	Track(ctx, name, StateAttrs(attrs)...)
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
