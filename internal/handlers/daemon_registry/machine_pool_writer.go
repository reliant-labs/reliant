package daemon_registry

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/auth"
)

// requirePoolWriter is the gate on every write that decides where pool-bound
// runs execute: a machine's labels and the pools' definitions
// (dev-docs/design/machine-pools.md). Owning the machine or pool is not
// enough. Every credential of the owner can do that, a machine's own daemon
// credential included, so a compromised box could pull pool work toward itself
// or strip other machines out of their pools. The write is admitted from:
//
//   - someone signed in to the app (auth.RequirePerson: an app session, or
//     a self-host deployment's single shared key);
//   - an agent's manage_machine / manage_machine_pool call a person
//     approved on its card, which the tool marks once it has checked the
//     approved hash covers exactly this change (auth.WithApprovedCard);
//   - Deprecated: the user's own access token, the CLI login or an API token
//     (personAccessToken). `reliant machine label` and `reliant machine pool
//     put|rm` shipped in v1.23.0 writing with the CLI login, so v1 keeps that
//     path working; each use is logged as deprecated. Use the app, or an
//     agent's approved card, instead.
//
// A daemon's credential (any machine-bound token), OAuth clients and an
// unclassified context are refused with reason person_required, fail-closed,
// and the refusal names the paths that do work (poolWriterRefusal). A
// daemon's own connect-time labels (`reliant daemon start --label`) are
// written by the registration (tools_daemon.persistDeclaredLabels), on the
// machine that connected only, and never come through here.
func (s *Service) requirePoolWriter(ctx context.Context, action string) error {
	if auth.RequirePersonOrApprovedCard(ctx, action) == nil {
		return nil
	}
	if personAccessToken(ctx) {
		s.deps.Logger.WarnContext(ctx, "deprecated: machine label or pool write with an access token; "+
			"use the Reliant app or an agent's approved card",
			"action", action, "credential", auth.CredentialAccessToken.String())
		return nil
	}
	return poolWriterRefusal(ctx, action)
}

// personAccessToken reports whether ctx was authenticated by one of the
// user's own access tokens that no machine holds: the CLI login or an API
// token (`reliant auth token create`). A machine-bound token (a daemon's
// credential, or its registration token) is not one, and an access token with
// no principal on ctx fails closed.
//
// Deprecated: kept only so the released CLI write path keeps working
// (requirePoolWriter); it is removed in the next major.
func personAccessToken(ctx context.Context) bool {
	if auth.CredentialFromContext(ctx) != auth.CredentialAccessToken {
		return false
	}
	p, ok := auth.MachineTokenFromContext(ctx)
	return ok && !auth.MachineBound(p)
}

// poolWriterRefusal is requirePoolWriter's PermissionDenied: reason
// person_required, like every person-only refusal, and a message that says
// how the change can be made instead. The app has no label or pool screen, so
// "open it in the app" (auth.RequirePerson's advice) would be a dead end.
func poolWriterRefusal(ctx context.Context, action string) error {
	return svcerr.WithReason(svcerr.PermissionDenied(
		"Only a person can "+action+": machine labels and pools decide where pool-bound runs execute. "+
			"This request was made with an "+auth.CredentialFromContext(ctx).String()+
			" that a machine or other software holds (such as a machine's own credential). "+
			"Ask an agent in a Reliant chat to make the change and approve its card, "+
			"or declare a machine's own labels when it starts: reliant daemon start --label key=value."),
		auth.ReasonPersonRequired)
}
