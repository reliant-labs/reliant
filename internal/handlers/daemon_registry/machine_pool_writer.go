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
// or strip other machines out of their pools. The write needs a person:
//
//   - someone signed in to the app (auth.RequirePerson: an app session, or
//     a self-host deployment's single shared key);
//   - or an agent's manage_machine / manage_machine_pool call a person
//     approved on its card, which the tool marks once it has checked the
//     approved hash covers exactly this change (auth.WithApprovedCard).
//
// The CLI login, API tokens, a daemon's credential, OAuth clients and an
// unclassified context are refused with reason person_required, fail-closed,
// and the refusal names the paths that do work (poolWriterRefusal). A
// daemon's own connect-time labels (`reliant daemon start --label`) are
// written by the registration (tools_daemon.persistDeclaredLabels), on the
// machine that connected only, and never come through here.
//
// These rpcs and the CLI commands over them (#2963, #2964) were gated this way
// before their first release, so no released client ever wrote with a CLI
// login.
func requirePoolWriter(ctx context.Context, action string) error {
	if auth.RequirePersonOrApprovedCard(ctx, action) == nil {
		return nil
	}
	return poolWriterRefusal(ctx, action)
}

// poolWriterRefusal is requirePoolWriter's PermissionDenied: reason
// person_required, like every person-only refusal, and a message that says
// how the change can be made instead. The app has no label or pool screen, so
// "open it in the app" (auth.RequirePerson's advice) would be a dead end.
func poolWriterRefusal(ctx context.Context, action string) error {
	return svcerr.WithReason(svcerr.PermissionDenied(
		"Only a person can "+action+": machine labels and pools decide where pool-bound runs execute. "+
			"This request was made with an "+auth.CredentialFromContext(ctx).String()+
			" (such as the CLI login or a machine's own credential), which an agent or a script can hold. "+
			"Ask an agent in a Reliant chat to make the change and approve its card, "+
			"or declare a machine's own labels when it starts: reliant daemon start --label key=value."),
		auth.ReasonPersonRequired)
}
