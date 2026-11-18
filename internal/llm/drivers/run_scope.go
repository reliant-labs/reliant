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

// RunFunding is what the person behind a run in an org has said about
// spending their own accounts there (org-provider-accounts.md §4.4, §10),
// read at the activity edge as the run's model calls read it. The zero value
// is the default: the org's accounts first, and the person's own only with
// their say. A generation has no turn to pause for the question, so without
// that say it is refused with how to give it, never spent.
type RunFunding struct {
	// OrgOnly: the person's own accounts never serve: an org_only order
	// (the person's for the org, the chat's or the run's), or consent that
	// could not be read.
	OrgOnly bool
	// PersonalFirst: the person chose their own accounts first in this org.
	PersonalFirst bool
	// PersonalWhenOrgCannot: the person chose to go on with their own
	// accounts when the org's cannot serve (on_org_limit use_personal), or
	// the run's automation is stamped to spend any source (§11.3).
	PersonalWhenOrgCannot bool
}

// runScope resolves the run's scope at most once, and only when a call asks:
// deciding attendance reads the database, and most tool calls spend nothing.
type runScope struct {
	actor   string
	once    sync.Once
	resolve func() (Scope, RunFunding)
	scope   Scope
	funding RunFunding
}

// WithRunScope returns ctx carrying the scope of the run its tool calls
// belong to, and what its person has said about spending their own accounts
// in its org, for calls executing as actor (the person the activity runs
// them as); resolve is called at most once, the first time one asks.
func WithRunScope(ctx context.Context, actor string, resolve func() (Scope, RunFunding)) context.Context {
	return context.WithValue(ctx, runScopeKey{}, &runScope{actor: actor, resolve: resolve})
}

// runScopeFor is the scope a call for userID made under ctx resolves in, and
// its funding: the run's, when userID is the actor the activity executes the
// call as and the run spends that same person's accounts; else the person's
// own accounts (a call that belongs to no run, or one made as anyone else). A
// run's org and attendance never reach a call made as another person.
func runScopeFor(ctx context.Context, userID string) (Scope, RunFunding) {
	if rs, ok := ctx.Value(runScopeKey{}).(*runScope); ok && rs != nil && rs.resolve != nil && authz.CanAccessPersonal(userID, rs.actor) {
		rs.once.Do(func() { rs.scope, rs.funding = rs.resolve() })
		if authz.CanAccessPersonal(userID, rs.scope.UserID) {
			return rs.scope, rs.funding
		}
	}
	return PersonalScope(userID), RunFunding{}
}
