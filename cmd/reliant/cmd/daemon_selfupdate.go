// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"context"
	"errors"
	"log/slog"

	"github.com/reliant-labs/reliant/internal/cliupdate"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonruntime"
)

// daemonSelfUpdater is a person's daemon's updater (internal/cliupdate): it
// confirms or reverts a swap that `reliant update` or an auto-update made,
// and, with autoUpdate, installs new releases at quiet moments
// (daemonruntime/upgrade_self.go). Nil on a managed machine. Nothing here
// stops the daemon from starting: an updater that cannot be built is logged
// and the daemon runs without one.
func daemonSelfUpdater(ctx context.Context, managed, autoUpdate bool) *cliupdate.Daemon {
	if managed {
		return nil
	}
	log := slog.Default()
	pending := cliupdate.Boot("", log)
	if v, why := pending.Reverted, pending.RevertReason; v != "" {
		logging.WarnContext(ctx, "A self-update was put back: the new release did not start", "version", v, "reason", why)
	}
	opts := cliupdate.DaemonOptions{AutoUpdate: autoUpdate, Pending: pending}
	opts.Managed = cliupdate.ManagedHost(false)
	opts.Logger = log
	su, err := cliupdate.NewDaemon(opts)
	if err != nil && autoUpdate {
		logging.WarnContext(ctx, "Auto-update is off: it could not be set up", "error", err)
		opts.AutoUpdate = false
		su, err = cliupdate.NewDaemon(opts)
	}
	if err != nil {
		logging.WarnContext(ctx, "This daemon cannot tell who installed it, so it does not update itself", "error", err)
		return nil
	}
	switch {
	case su.AutoUpdate():
		logging.InfoContext(ctx, "Auto-update is on: this daemon installs new releases itself, at quiet moments", "path", su.Install().Path)
	case autoUpdate:
		logging.WarnContext(ctx, "Auto-update was asked for but is off", "reason", su.WhyNotAuto(), "update", su.Advice())
	}
	return su
}

// selfUpdateExec is the swap a stopped runtime hands to `daemon start`.
type selfUpdateExec interface {
	Armed() bool
	ExecPending() error
	Disable(error)
}

// runSelfUpdating runs start until it returns for good. When the runtime
// stopped for a self-update (ErrUpgradeExit with a swap armed), the swap is
// performed here, after the runtime's teardown: ExecPending replaces this
// process with the new binary and does not return. If it fails, the old
// binary is still in place, so the runtime is started again on it, with
// auto-update off for the rest of this process, rather than leaving a
// person's machine with no daemon (D6).
func runSelfUpdating(start func() error, su selfUpdateExec, beforeExec func()) error {
	for {
		err := start()
		if su == nil || !errors.Is(err, daemonruntime.ErrUpgradeExit) || !su.Armed() {
			return err
		}
		beforeExec()
		execErr := su.ExecPending()
		if execErr == nil {
			return err // only a stand-in exec returns
		}
		logging.Error("The update could not be installed; the daemon keeps running the version it has", "error", execErr)
		su.Disable(execErr)
	}
}

// killBackgroundForSwap stops the background processes before the swap's
// exec: their output pipes would close under them, and the new binary
// cannot adopt them yet (R5). At a quiet moment there are none; a forced
// update interrupts them, as any restart does.
func killBackgroundForSwap() { shell.GetBackgroundManager().KillAllRunning() }
