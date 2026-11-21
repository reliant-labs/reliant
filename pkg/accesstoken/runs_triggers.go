package accesstoken

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// ── Runs and triggers ─────────────────────────────────────────────────
//
// runs:start, runs:read, runs:control, triggers:fire and triggers:manage let
// an SDK caller hold a slice of a Reliant API instead of the person's whole
// API (reliant:api). This package knows no rpc and imports no Reliant code;
// the table below is the contract a Reliant API enforces with [RequireScope].
//
//	RunService
//	  StartRun                                         runs:start
//	  GetRun, ListRuns, LastRunPerWorkflow             runs:read
//	  SignalRun, PauseRun, ResumeRun, CancelRun,
//	    InterruptRun                                   runs:control
//
//	TriggerService
//	  FireTrigger                                      triggers:fire
//	  CreateTrigger, UpdateTrigger, DeleteTrigger,
//	    SetTriggerEnabled, RotateWebhookToken,
//	    EnableWorkflow, DisableWorkflow                triggers:manage
//	  GetTrigger, ListTriggers, ListTriggerEvents,
//	    GetLaunchEvent, ResolveTriggerSenders,
//	    ListReplayEvents, PreviewReplay                no narrow scope yet: reliant:api only
//
// Every other rpc of the API still needs reliant:api, so a narrow token that
// reaches an unlisted rpc is refused. The enforcement is the API's job.
//
// WHICH SCOPE SATISFIES WHICH ([Set.Permits]):
//
//   - reliant:api permits all five, so an existing token keeps every
//     permission it had. Its stored grant is not widened.
//   - runs:start and runs:control each imply runs:read, so a caller that
//     starts or steers a run can look at it. runs:start and runs:control
//     do not imply each other.
//   - triggers:fire and triggers:manage imply nothing: running a trigger now
//     and changing what runs unattended are different authorities, and
//     neither implies runs:*. A fire starts a run through the trigger's own
//     activation, not through the caller's runs:start.
//
// TRIGGER ACTIVATION IS GATED BY PERMISSION, NOT BY WHO HOLDS THE TOKEN.
// triggers:manage is the scope for the activation rpcs; a token without it
// (and without reliant:api) is refused. Approving an item or clearing a halt
// stays human-only and has no scope here.
//
// All five act as a person ([RequiresActingUser]) and cannot be bound to a
// resource.

// RunsAndTriggersScopes are the five scopes above, narrowest first.
var RunsAndTriggersScopes = []Scope{
	ScopeRunsRead,
	ScopeRunsStart,
	ScopeRunsControl,
	ScopeTriggersFire,
	ScopeTriggersManage,
}

func isRunsOrTriggersScope(s Scope) bool {
	for _, known := range RunsAndTriggersScopes {
		if known == s {
			return true
		}
	}
	return false
}

type principalKey struct{}

// WithPrincipal returns ctx carrying the authenticated principal, for
// [RequireScope] further down the call. The authentication interceptor calls
// it once per request.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal [WithPrincipal] stored, or nil.
func PrincipalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey{}).(*Principal)
	return p
}

// RequireScope returns nil when the principal on ctx may perform an action
// that needs scope (by [Set.Permits], so reliant:api passes for the runs and
// triggers scopes), and a svcerr error otherwise:
//
//   - no principal on ctx: Unauthenticated, so a handler wired outside the
//     authentication interceptor fails closed;
//   - scope missing: PermissionDenied with reason [ReasonScopeMissing] and
//     the scope named in [ScopesHeader].
//
// Call it first in an rpc, before reading the request.
func RequireScope(ctx context.Context, scope Scope) error {
	p := PrincipalFrom(ctx)
	if p == nil {
		return svcerr.Unauthenticated("no authenticated principal")
	}
	return p.Require(scope)
}

// Require is [RequireScope] on a principal already in hand. A nil principal
// is refused.
func (p *Principal) Require(scope Scope) error {
	if p == nil {
		return svcerr.Unauthenticated("no authenticated principal")
	}
	if p.Scopes.Permits(scope) {
		return nil
	}
	return svcerr.WithMetadata(
		svcerr.WithReason(svcerr.PermissionDenied("this credential lacks the "+string(scope)+" scope"), ReasonScopeMissing),
		ScopesHeader, ScopesHeaderValue([]Scope{scope}),
	)
}
