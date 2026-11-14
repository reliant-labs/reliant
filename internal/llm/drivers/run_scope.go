// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"sync"

	"github.com/reliant-labs/reliant/internal/authz"
)

// A tool that spends a provider (generate_image, generate_video, and
// load_tool's check of them) is called with the person's id alone: the tools
// package cannot name a Scope, because this package imports it. So the scope
// of the run the call belongs to rides on its context. The activity that
// executes the call sets it once (WithRunScope), from the chat row and who is
// behind it, as call_llm scopes the model's own calls; media resolution reads
// it back for the same person (runScopeFor). Nothing else sets it: never a
// client, a workflow input or a daemon (control-plane
// docs/design/org-provider-accounts.md §4.1, org-policies.md §4.2).

type runScopeKey struct{}

// runScope resolves the run's scope at most once, and only when a call asks:
// deciding attendance reads the database, and most tool calls spend nothing.
type runScope struct {
	once    sync.Once
	resolve func() Scope
	scope   Scope
}

// WithRunScope returns ctx carrying the scope of the run its tool calls
// belong to; resolve is called at most once, the first time one asks.
func WithRunScope(ctx context.Context, resolve func() Scope) context.Context {
	return context.WithValue(ctx, runScopeKey{}, &runScope{resolve: resolve})
}

// runScopeFor is the scope a call for userID made under ctx resolves in: the
// run's, when the activity set one for that same person, else the person's
// own accounts (a call that belongs to no run).
func runScopeFor(ctx context.Context, userID string) Scope {
	if rs, ok := ctx.Value(runScopeKey{}).(*runScope); ok && rs != nil && rs.resolve != nil {
		rs.once.Do(func() { rs.scope = rs.resolve() })
		// The run's scope spends its person's accounts: only that person's
		// call may resolve in it.
		if authz.CanAccessPersonal(userID, rs.scope.UserID) {
			return rs.scope
		}
	}
	return PersonalScope(userID)
}
