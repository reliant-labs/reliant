// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/machinepool"
)

// persistDeclaredLabels merges the durable labels a daemon declared at
// registration (DaemonRegister.machine_labels: `reliant daemon start --label`)
// into its machine's labels, which decide machine-pool membership
// (dev-docs/design/machine-pools.md). It runs on every registration, so a
// box's bootstrap is its declaration: each declared key is set to its value,
// a label it does not declare is left alone, and a daemon that declares none
// writes nothing.
//
// It runs after UpsertDaemon accepted the registration, and the write itself
// carries the same guard (the caller's own row; a reserved row only for a
// bound credential), so it can never label a machine the credential could
// not connect as.
//
// Recovery first: an invalid set, or a write that fails, is logged and the
// connection carries on with the machine's labels as they were.
//
// A pool's member_idle_timeout is not applied here, as SetMachineLabels
// applies it: it is set on the control plane with the owner's signed-in
// session, which a daemon's connection does not have. It matters only for a
// managed machine, whose labels its owner sets, and the next pool or label
// write that touches the machine converges it.
func (s *Service) persistDeclaredLabels(ctx context.Context, userID, daemonID string, boundCredential bool, labels map[string]string) {
	if len(labels) == 0 {
		return
	}
	if err := machinepool.ValidateLabels(labels, "machine labels"); err != nil {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Ignored the machine labels a daemon declared: they are not valid labels",
			"error", err, "userID", userID, "daemonID", daemonID)
		return
	}
	wrote, err := s.deps.Repo.MergeDeclaredDaemonLabels(ctx, userID, daemonID, boundCredential, labels, machinepool.MaxMachineLabels)
	if err != nil {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Failed to persist the machine labels a daemon declared",
			"error", err, "userID", userID, "daemonID", daemonID)
		return
	}
	if wrote {
		logging.InfoContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Merged the machine labels a daemon declared",
			"userID", userID, "daemonID", daemonID, "labels", labels)
		return
	}
	// Nothing written: usually a reconnect whose labels already hold. Only
	// a read can say whether it was instead refused, which an owner needs to
	// hear about. The registration upsert just accepted this credential for
	// the row, so the guard is not what refused it: the label limit was.
	d, err := s.deps.Repo.GetDaemon(ctx, daemonID)
	if err != nil || d == nil {
		return
	}
	if !machinepool.Matches(d.Labels, labels) {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Did not persist the machine labels a daemon declared: the machine would carry more than the label limit",
			"userID", userID, "daemonID", daemonID, "limit", machinepool.MaxMachineLabels, "labels", labels)
	}
}
