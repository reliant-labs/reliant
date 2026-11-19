package daemon_registry

import (
	"context"

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
// unclassified context are refused with reason person_required, fail-closed.
// A daemon's own connect-time labels (`reliant daemon start --label`) are
// written by the registration (tools_daemon.persistDeclaredLabels), on the
// machine that connected only, and never come through here.
func requirePoolWriter(ctx context.Context, action string) error {
	return auth.RequirePersonOrApprovedCard(ctx, action)
}
