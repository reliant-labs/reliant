// Copyright (c) 2025 Reliant Labs
package analytics

import (
	"context"

	"github.com/reliant-labs/forge/pkg/observe"
)

// AuthProvider names Reliant's auth provider on the standard auth events
// (forge's auth_signed_up / auth_signed_in / auth_signed_out). It is
// self-reported on each event: nothing declares it anywhere else.
const AuthProvider = "supabase"

// Recording reports whether this Tracker records anything: false for the
// zero Tracker — no client installed, or a user who opted out of analytics or
// whose consent could not be read. The Supabase auth-event hook asks it per
// event, so a user's opt-out holds for the events Supabase reports too.
func (t Tracker) Recording() bool { return t.c != nil }

// TrackAuthSignedIn records the user's daily sign-in heartbeat as forge's
// standard auth event: auth_signed_in {kind: resumed, provider: supabase},
// which is what counts a user with a long-lived session as active. The
// caller sends it at most once per user per UTC day (the auth interceptor's
// heartbeat); the zero Tracker records nothing, so a user's analytics opt-out
// holds. It writes through observe.TrackAuth, forge's one shape for the
// event, on this client's logger.
func (t Tracker) TrackAuthSignedIn() {
	if t.c == nil || t.userID == "" {
		return
	}
	ctx := t.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	observe.TrackAuth(observe.WithLogger(ctx, t.c.log()), observe.AuthSignedIn, t.userID,
		observe.WithKind(observe.AuthKindResumed), observe.WithProvider(AuthProvider))
}
