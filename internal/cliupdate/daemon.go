// Copyright (c) 2026 Reliant Labs
package cliupdate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/reliant-labs/forge/pkg/selfupdate"
)

// The daemon's half: a daemon on a person's machine started with
// --auto-update (owner decision D7: opt-in) stages each newer release in the
// background and installs it at a quiet moment daemonruntime chooses
// (upgrade_self.go); a RestartRequested can only make that sooner. The
// swap itself is forge's: the runtime drains and tears down, then `daemon
// start` execs the new binary as its last act (ExecPending), which confirms
// itself once the gateway accepts it (Confirm) or is put back.

// DefaultStageEvery is how often an auto-updating daemon looks for a newer
// release. The manifest is one small CDN object, so the cost is nothing; the
// interval only bounds how stale a staged release can be.
const DefaultStageEvery = 6 * time.Hour

// ErrNoUpdate is Stage's answer when the channel has nothing newer than the
// running build (or only a release this machine already reverted).
var ErrNoUpdate = errors.New("no newer release")

// ErrAutoUpdateOff is Stage's and Apply's refusal on a daemon that may not
// replace itself: not opted in, an install something else owns, or a
// platform with no swap yet.
var ErrAutoUpdateOff = errors.New("this daemon does not update itself")

// Boot is the first thing `daemon start` does on a person's machine: after a
// swap it arms selfupdate's revert watchdog, and it reverts at once a release
// that already failed to start twice. It runs whether or not --auto-update is
// set, since `reliant update` swaps the same binary. stateDir "" is the
// default.
func Boot(stateDir string, log *slog.Logger) *selfupdate.Pending {
	if stateDir == "" {
		dir, err := selfupdate.DefaultStateDir(Binary)
		if err != nil {
			return &selfupdate.Pending{}
		}
		stateDir = dir
	}
	return selfupdate.Boot(selfupdate.BootOptions{Binary: Binary, StateDir: stateDir, Logger: log})
}

// DaemonOptions configures a daemon's updater.
type DaemonOptions struct {
	Options
	// AutoUpdate is the daemon's opt-in (--auto-update, DaemonConfig.auto_update).
	AutoUpdate bool
	// Pending is what Boot found at this start; nil is none.
	Pending *selfupdate.Pending
	// GOOS replaces runtime.GOOS (tests).
	GOOS string
	// Detect replaces selfupdate.Detect of the running binary (tests).
	Detect func(managed func() (string, bool)) (selfupdate.Install, error)
}

// ApplyRequest is one apply: how urgent, the drain's deadline, how the
// runtime drains (an error declines the upgrade with nothing swapped), and how
// it tears down for the swap.
type ApplyRequest struct {
	Urgency  selfupdate.Urgency
	Deadline time.Time
	Drain    func(ctx context.Context, deadline time.Time) (interrupted int, err error)
	Shutdown func()
}

// Daemon is a daemon's self-updater.
type Daemon struct {
	updater *selfupdate.Updater
	install selfupdate.Install
	auto    bool
	goos    string
	pending *selfupdate.Pending
	log     *slog.Logger
	channel string

	confirm sync.Once

	mu sync.Mutex
	// failed is why the last install failed after the runtime stopped for
	// it; set, the daemon installs nothing more until it restarts.
	failed error
	staged *selfupdate.Staged
	// apply is the request in progress; the Drainer and Shutdown forge
	// calls read it.
	apply *ApplyRequest
}

// NewDaemon builds a daemon's updater. A daemon that may not update itself
// still gets one: it confirms a swap `reliant update` made and names how its
// owner updates it (Advice).
func NewDaemon(o DaemonOptions) (*Daemon, error) {
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	o.Logger = log
	detect := o.Detect
	if detect == nil {
		detect = Detect
	}
	install, err := detect(o.Managed)
	if err != nil {
		return nil, err
	}
	goos := o.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	d := &Daemon{install: install, auto: o.AutoUpdate, goos: goos, pending: o.Pending, log: log}
	if o.Pending == nil {
		d.pending = &selfupdate.Pending{}
	}
	cfg, err := Config(o.Options)
	if err != nil {
		if d.auto {
			return nil, err
		}
		return d, nil // nothing to stage; Confirm and Advice still work
	}
	d.channel = cfg.Channel
	cfg.Drainer = drainerFunc(d.drain)
	cfg.Shutdown = d.shutdown
	updater, err := selfupdate.New(cfg)
	if err != nil {
		return nil, err
	}
	d.updater = updater
	return d, nil
}

// AutoUpdate reports whether this daemon applies releases itself: opted in,
// on an install nothing else owns, on a platform that can swap a running
// binary (not Windows yet).
func (d *Daemon) AutoUpdate() bool {
	d.mu.Lock()
	failed := d.failed
	d.mu.Unlock()
	return d.auto && d.updater != nil && d.install.SelfOwned() && d.goos != "windows" && failed == nil
}

// Disable stops installing for the rest of this process: the install err
// failed after the runtime had stopped for it, so `daemon start` runs the
// runtime again on the binary it has instead of trying again at every quiet
// moment.
func (d *Daemon) Disable(err error) {
	d.mu.Lock()
	d.failed = err
	d.mu.Unlock()
}

// Install is what Detect found about the running binary.
func (d *Daemon) Install() selfupdate.Install { return d.install }

// SelfOwned reports whether `reliant update` could replace this install.
func (d *Daemon) SelfOwned() bool { return d.install.SelfOwned() }

// Advice is how this daemon's owner updates it by hand.
func (d *Daemon) Advice() string { return Advice(d.install) }

// WhyNotAuto says why AutoUpdate is false, for the log; "" when it is true.
func (d *Daemon) WhyNotAuto() string {
	d.mu.Lock()
	failed := d.failed
	d.mu.Unlock()
	switch {
	case failed != nil:
		return "the last install failed (" + failed.Error() + "); restart the daemon to try again"
	case !d.auto:
		return "auto-update is off (start the daemon with --auto-update to turn it on)"
	case d.updater == nil:
		return "this build cannot be ordered against a release"
	case !d.install.SelfOwned():
		return fmt.Sprintf("this reliant (%s) is %s: %s", d.install.Path, d.install.Kind, d.install.Reason)
	case d.goos == "windows":
		return "replacing a running reliant.exe is not supported on Windows yet"
	}
	return ""
}

// Stage finds the channel's newest release and downloads, verifies and probes
// it, unless it is staged already. It returns the staged version, or
// ErrNoUpdate.
func (d *Daemon) Stage(ctx context.Context) (string, error) {
	if !d.AutoUpdate() {
		return "", ErrAutoUpdateOff
	}
	rel, decision, err := d.updater.Check(ctx)
	if err != nil {
		return "", err
	}
	if decision.Action == selfupdate.Skip {
		return "", fmt.Errorf("%w on %s: %s", ErrNoUpdate, d.channel, decision.Reason)
	}
	d.mu.Lock()
	staged := d.staged
	d.mu.Unlock()
	if staged != nil && staged.Release.Tag == rel.Tag {
		if _, err := os.Stat(staged.Path); err == nil {
			return rel.Version, nil
		}
	}
	s, err := d.updater.Prepare(ctx, rel)
	if err != nil {
		return "", err
	}
	d.mu.Lock()
	d.staged = &s
	d.mu.Unlock()
	d.log.InfoContext(ctx, "Auto-update staged a release; the daemon installs it at its next quiet moment",
		"version", rel.Version, "channel", d.channel)
	return rel.Version, nil
}

// Apply drains through req.Drain and asks the runtime to tear down for the
// swap of the staged release (req.Shutdown). After the runtime returns, the
// caller must call ExecPending. A drain that declines aborts it with nothing
// swapped.
func (d *Daemon) Apply(ctx context.Context, req ApplyRequest) error {
	if !d.AutoUpdate() {
		return ErrAutoUpdateOff
	}
	d.mu.Lock()
	staged := d.staged
	if staged == nil {
		d.mu.Unlock()
		return errors.New("no release is staged")
	}
	d.apply = &req
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.apply = nil
		d.mu.Unlock()
	}()
	return d.updater.Request(ctx, *staged, selfupdate.Decision{
		Action: selfupdate.Apply, Urgency: req.Urgency, Deadline: req.Deadline,
		Reason: "auto-update at a quiet moment",
	})
}

// Armed reports whether Apply armed a swap for ExecPending.
func (d *Daemon) Armed() bool { return d.updater != nil && d.updater.Armed() }

// ExecPending performs the armed swap and execs the new binary; it returns nil
// at once when none is armed, and otherwise only on failure, with the old
// binary in place.
func (d *Daemon) ExecPending() error {
	if d.updater == nil {
		return nil
	}
	return d.updater.ExecPending()
}

// Confirm marks a just-swapped binary healthy. Called when the gateway
// accepts the daemon; only the first call does anything.
func (d *Daemon) Confirm() {
	d.confirm.Do(func() {
		if !d.pending.Swapped {
			return
		}
		if err := d.pending.Confirm(); err != nil {
			d.log.Warn("Could not record that the updated daemon started; it is put back unless a later start confirms it", "error", err)
			return
		}
		d.log.Info("Updated daemon confirmed", "from", d.pending.From, "to", d.pending.To)
	})
}

// Reverted names a release this machine put back after it failed to start,
// and why; empty when the last swap did not fail.
func (d *Daemon) Reverted() (version, reason string) {
	return d.pending.Reverted, d.pending.RevertReason
}

func (d *Daemon) drain(ctx context.Context, deadline time.Time) (selfupdate.DrainReport, error) {
	d.mu.Lock()
	req := d.apply
	d.mu.Unlock()
	if req == nil || req.Drain == nil {
		return selfupdate.DrainReport{}, nil
	}
	n, err := req.Drain(ctx, deadline)
	return selfupdate.DrainReport{Interrupted: n}, err
}

func (d *Daemon) shutdown() {
	d.mu.Lock()
	req := d.apply
	d.mu.Unlock()
	if req != nil && req.Shutdown != nil {
		req.Shutdown()
	}
}

type drainerFunc func(ctx context.Context, deadline time.Time) (selfupdate.DrainReport, error)

func (f drainerFunc) Drain(ctx context.Context, deadline time.Time) (selfupdate.DrainReport, error) {
	return f(ctx, deadline)
}
