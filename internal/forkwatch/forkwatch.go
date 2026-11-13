// Copyright (c) 2026 Reliant Labs

// Package forkwatch tells whether this machine can still start a process, and
// reports a machine that cannot as degraded (reliantv1.MachineHealth).
//
// Why. On 2026-10-30 a cloud machine's process table filled: from 17:56Z every
// fork failed with EAGAIN for 24 minutes, until its owner suspended and resumed
// it by hand (reliant#1919). Nothing noticed. The daemon stayed attached and
// answered, so the registry and the UI called the machine connected, and
// wait_for_machine told agents "connected again" 44 times while every command
// they retried failed the same way.
//
// What. Every Interval the watchdog starts `true` the way an agent's command
// starts: through proctree's default limiter, so in a command cgroup of its own
// under the cgroup all commands share. It therefore fails exactly when an
// agent's command would — that shared cgroup at its pids.max, the user's
// RLIMIT_NPROC, or the kernel's threads-max, which the exec liveness probe
// cannot see (when the table is full the probe's own process cannot start, and
// kubelet discards a probe that errored). Threshold refusals in a row make the
// machine degraded, with a reason naming the largest process trees; the next
// start that succeeds makes it healthy again. Only a start refused with EAGAIN
// counts: a slow start is a start, and any other failure (no `true` binary, a
// cgroup that could not be made) says nothing about the process table.
//
// The watchdog runs on an OS thread it locks at start and never gives back, so
// it never needs a new thread to probe, decide or log, the one thing a full
// table refuses.
//
// It reports and does nothing else. Freeing the table (killing the largest
// tool tree, then restarting the daemon) is reliant#1919's self-heal step,
// built on this signal later.
//
//forge:exclude-contract: one concrete policy over this machine's own process starts; its only consumer is the daemon heartbeat, and tests drive it through Config.Probe
package forkwatch

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
)

const (
	// DefaultInterval is how often the watchdog starts a process.
	DefaultInterval = 30 * time.Second
	// DefaultThreshold is how many refused starts in a row make the machine
	// degraded: about a minute of a full table, so one unlucky moment at the
	// limit is not reported.
	DefaultThreshold = 3
	// CodeProcessTableFull is MachineHealth.code while starts are refused.
	CodeProcessTableFull = "process_table_full"
	// probeTimeout bounds one probe. `true` exits at once; one that has not
	// exited by then has still started, which is all the probe asks.
	probeTimeout = 10 * time.Second
	// topTrees is how many process trees the reason names.
	topTrees = 3
)

// Config is the watchdog's policy. The zero value probes `true` every
// DefaultInterval and degrades after DefaultThreshold refusals.
type Config struct {
	// Interval is how often Run probes; 0 is DefaultInterval.
	Interval time.Duration
	// Threshold is how many refused probes in a row degrade the machine; 0
	// is DefaultThreshold.
	Threshold int
	// Probe starts one process and reports whether it started: nil when it
	// did, whatever it then did. Nil is the default probe (`true` through
	// proctree's default limiter); where there is none (no `true` binary, or
	// not a Unix) the watchdog is off.
	Probe func(ctx context.Context) error
	// Trees names the largest process trees on the machine, for the reason;
	// nil names none. It must not start a process.
	Trees func(n int) string
}

// Watchdog probes process starts and keeps the machine's health. A nil
// *Watchdog reports nothing.
type Watchdog struct {
	cfg     Config
	changed chan struct{}

	mu sync.Mutex
	// probed is whether any probe answered (started or was refused).
	probed bool
	// refused counts refused probes in a row; firstRefused is when the run
	// began.
	refused      int
	firstRefused time.Time
	degraded     bool
	reason       string
	// inconclusive is whether the last probe could not tell, logged once
	// per run of them.
	inconclusive bool
}

// New builds a watchdog. It does not probe until Check or Run.
func New(cfg Config) *Watchdog {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.Threshold <= 0 {
		cfg.Threshold = DefaultThreshold
	}
	if cfg.Probe == nil {
		cfg.Probe = defaultProbe()
	}
	return &Watchdog{cfg: cfg, changed: make(chan struct{}, 1)}
}

// Enabled reports whether the watchdog can probe here.
func (w *Watchdog) Enabled() bool { return w != nil && w.cfg.Probe != nil }

// Run probes at once and then every Interval until ctx ends. Where there is no
// probe it returns at once.
func (w *Watchdog) Run(ctx context.Context) {
	if !w.Enabled() {
		return
	}
	// Hold this goroutine's OS thread for good (the thread exits with it), so
	// the watchdog never needs a new one: on a full table that is the one
	// thing it could not get.
	runtime.LockOSThread()
	w.Check(ctx)
	t := time.NewTicker(w.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Check(ctx)
		}
	}
}

// Changed receives a token when the machine becomes degraded or healthy
// again, so the heartbeat can carry it at once. Nil-safe.
func (w *Watchdog) Changed() <-chan struct{} {
	if w == nil {
		return nil
	}
	return w.changed
}

// Check probes once and updates the health.
func (w *Watchdog) Check(ctx context.Context) {
	if !w.Enabled() {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	err := w.cfg.Probe(pctx)
	cancel()
	if err != nil && ctx.Err() != nil {
		// Shutting down: the probe was cut short, not refused.
		return
	}
	now := time.Now()

	w.mu.Lock()
	was := w.degraded
	switch {
	case err == nil:
		w.probed, w.refused, w.inconclusive = true, 0, false
		w.degraded, w.reason = false, ""
	case refused(err):
		w.probed, w.inconclusive = true, false
		if w.refused == 0 {
			w.firstRefused = now
		}
		w.refused++
		if w.refused >= w.cfg.Threshold {
			w.degraded = true
			// Rewritten on every refusal while degraded: the trees to
			// name change as the machine does.
			w.reason = w.describe(err)
		}
	default:
		if !w.inconclusive {
			logging.WarnContext(ctx, "forkwatch: a probe could not tell whether this machine can start processes; health unchanged",
				"error", err)
		}
		w.inconclusive = true
	}
	degraded, reason, refusedN := w.degraded, w.reason, w.refused
	w.mu.Unlock()

	if degraded == was {
		return
	}
	if degraded {
		logging.WarnContext(ctx, "forkwatch: this machine cannot start processes; reporting it degraded",
			"refused_in_a_row", refusedN, "error", err, "reason", reason)
	} else {
		logging.InfoContext(ctx, "forkwatch: this machine can start processes again; reporting it healthy")
	}
	select {
	case w.changed <- struct{}{}:
	default:
	}
}

// describe words the degraded reason. Called with w.mu held.
func (w *Watchdog) describe(err error) string {
	s := fmt.Sprintf("This machine cannot start new processes: its process table is full. "+
		"The last %d attempts to start one (every %s, since %s UTC) failed with %q, so every command an agent runs here fails the same way. "+
		"Retrying, or waiting for the machine, will not help until processes exit. "+
		"Stop what is not needed (shell_list, shell_kill), or restart the machine.",
		w.refused, w.cfg.Interval, w.firstRefused.UTC().Format(time.TimeOnly), errText(err))
	if w.cfg.Trees != nil {
		if trees := w.cfg.Trees(topTrees); trees != "" {
			s += " Largest process trees: " + trees + "."
		}
	}
	return s
}

// errText is the cause of err, "resource temporarily unavailable" rather than
// os/exec's whole "fork/exec /usr/bin/true: ...".
func errText(err error) string {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err.Error()
		}
		next := u.Unwrap()
		if next == nil {
			return err.Error()
		}
		err = next
	}
}

// Health is the machine's health for the heartbeat: nil before the first probe
// answers, or where there is no probe, which the gateway reads as healthy.
func (w *Watchdog) Health() *reliantv1.MachineHealth {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.probed {
		return nil
	}
	if !w.degraded {
		return &reliantv1.MachineHealth{State: reliantv1.MachineHealthState_MACHINE_HEALTH_STATE_OK}
	}
	return &reliantv1.MachineHealth{
		State:  reliantv1.MachineHealthState_MACHINE_HEALTH_STATE_DEGRADED,
		Code:   CodeProcessTableFull,
		Reason: w.reason,
	}
}
