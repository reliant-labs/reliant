// Copyright (c) 2025 Reliant Labs
package serverapi

import (
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// daemonRouterOptions is the api-server's daemon-router wiring.
//
// Daemon records are this process's own: the router resolves them — including
// the "provisioning" / "suspended" lifecycle that makes ErrDaemonPending — from
// repo, the same tables DaemonRegistryService serves
// (docs/design/one-daemon-list.md). It never asks the control plane for them;
// control-plane deleted its reliant.v1.DaemonRegistryService adapter so that
// exactly one host answers that path.
//
// The control plane is wired only for what it alone can do: resume a
// suspended managed machine, via controlplane.v1.DaemonService/ResumeDaemon,
// authenticated as the signed-in user. cpURL is empty when no control plane is
// configured, and then a suspended daemon is reported pending, never woken.
func daemonRouterOptions(repo db.Repository, cpURL string) []toolexec.NATSRouterOption {
	opts := []toolexec.NATSRouterOption{toolexec.WithDatabase(repo)}
	if cpURL != "" {
		opts = append(opts, toolexec.WithDaemonResumer(controlplane.NewDaemonClient(cpURL)))
	}
	return opts
}
