package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// BootOptions locates the state [Boot] reads. Binary and StateDir must match
// the [Config] the [Updater] uses.
type BootOptions struct {
	Binary   string
	StateDir string // default DefaultStateDir(Binary)
	Logger   *slog.Logger
}

// Pending is what [Boot] found. Its zero value (no swap in flight) is inert:
// Confirm does nothing.
type Pending struct {
	// Swapped is true when this process runs a newly swapped binary that has
	// not been confirmed yet: the app must call Confirm after its own health
	// check, or the watchdog reverts it.
	Swapped bool
	From    string
	To      string
	// Reverted names a version this machine put back after it failed to
	// confirm; RevertReason says why. Empty when the last swap did not fail.
	Reverted     string
	RevertReason string

	dir   string
	log   *slog.Logger
	env   bootEnv
	sha   string
	mu    sync.Mutex
	done  bool
	timer *time.Timer
	// deadline is when the watchdog reverts; a Confirm that fails re-arms
	// the watchdog for whatever is left of it.
	deadline time.Time
	// lockWait bounds how long Confirm waits for the state lock (default
	// 30s); a test seam.
	lockWait time.Duration
}

// bootEnv is Boot's seams: which binary is running, how to exec, whether a
// pid is alive.
type bootEnv struct {
	exe   string
	pid   int
	exec  func(path string, argv, env []string) error
	alive func(pid int) bool
}

// Boot is the first call in main. After a swap it starts the revert
// watchdog, and it reverts at once a new binary that already crashed twice
// without confirming (the boot-loop guard). Errors are logged, never fatal:
// a broken state directory must not stop the app from starting.
func Boot(opts BootOptions) *Pending {
	env := bootEnv{pid: os.Getpid(), exec: execBinary, alive: processAlive}
	if exe, err := os.Executable(); err == nil {
		env.exe = exe
	}
	return boot(opts, env)
}

func boot(opts BootOptions, env bootEnv) *Pending {
	p := &Pending{log: opts.Logger, env: env}
	if p.log == nil {
		p.log = slog.Default()
	}
	p.dir = opts.StateDir
	if p.dir == "" {
		dir, err := DefaultStateDir(opts.Binary)
		if err != nil {
			p.log.Warn("selfupdate: boot", "err", err)
			return p
		}
		p.dir = dir
	}
	if _, err := os.Stat(statePath(p.dir)); err != nil {
		return p // never swapped here
	}
	exe, err := filepath.EvalSymlinks(env.exe)
	if err != nil {
		p.log.Warn("selfupdate: boot: resolve the running binary", "err", err)
		return p
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var revertNow string
	var watch time.Duration
	err = withLock(ctx, p.dir, func() error {
		st, err := loadState(p.dir)
		if err != nil {
			return err
		}
		rec := st.Swap
		if rec == nil || rec.Path != exe {
			return nil // another binary's swap, or none
		}
		switch rec.Phase {
		case phaseReverted:
			p.Reverted, p.RevertReason = rec.ToVersion, rec.Reason
			return nil
		case phaseSwapped:
		default:
			return nil
		}
		sum, err := sha256File(exe)
		if err != nil {
			return err
		}
		if sum != rec.ToSHA256 {
			// Replaced by someone else since (a reinstall, a manual copy).
			rec.Phase, rec.Reason, rec.UpdatedAt = phaseSuperseded, "the binary changed after the swap", time.Now().UTC()
			return saveState(p.dir, st)
		}
		p.From, p.To, p.sha = rec.FromVersion, rec.ToVersion, sum
		crashed := 0
		seen := false
		for _, b := range rec.Boots {
			if b.PID == env.pid {
				seen = true
			} else if !env.alive(b.PID) {
				crashed++
			}
		}
		if crashed >= maxUnconfirmedCrashes {
			revertNow = fmt.Sprintf("%d starts of %s exited without confirming", crashed, rec.ToVersion)
			return nil
		}
		if !seen {
			rec.Boots = append(rec.Boots, bootRecord{PID: env.pid, At: time.Now().UTC()})
		}
		rec.UpdatedAt = time.Now().UTC()
		watch = time.Duration(rec.ConfirmWithinMS) * time.Millisecond
		if watch <= 0 {
			watch = DefaultConfirmWithin
		}
		return saveState(p.dir, st)
	})
	if err != nil {
		p.log.Warn("selfupdate: boot", "err", err)
		return p
	}
	if revertNow != "" {
		p.log.Error("selfupdate: boot-loop guard: reverting", "to", p.To, "reason", revertNow)
		p.done = true
		p.revert(revertNow)
		// revert returns only if it could not exec the previous binary; keep
		// running the new one rather than not running at all.
		return p
	}
	if watch > 0 {
		p.Swapped = true
		p.log.Info("selfupdate: running a new binary; waiting for confirmation", "from", p.From, "to", p.To, "within", watch)
		p.deadline = time.Now().Add(watch)
		p.timer = time.AfterFunc(watch, p.watchdog)
	}
	return p
}

// Confirm marks the swapped binary good and stops the watchdog. Call it once
// the app's own health check passes (for a daemon: registered and
// authenticated). It does nothing when no swap is in flight. When the
// confirmation cannot be recorded (the state lock is held, the state file
// cannot be written) it returns the error and leaves the swap pending: the
// watchdog is re-armed for the rest of its deadline, and Confirm may be
// called again.
func (p *Pending) Confirm() error {
	if p == nil || !p.Swapped {
		return nil
	}
	p.mu.Lock()
	if p.done {
		p.mu.Unlock()
		return errors.New("selfupdate: already confirmed or reverting")
	}
	p.done = true
	if p.timer != nil {
		p.timer.Stop()
	}
	p.mu.Unlock()

	if err := p.confirm(); err != nil {
		p.mu.Lock()
		p.done = false
		if p.timer != nil {
			// Reset re-arms a stopped timer, and one that already fired
			// while Confirm held done (its watchdog then did nothing).
			p.timer.Reset(max(time.Until(p.deadline), 0))
		}
		p.mu.Unlock()
		return err
	}
	return nil
}

func (p *Pending) confirm() error {
	wait := p.lockWait
	if wait <= 0 {
		wait = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	return withLock(ctx, p.dir, func() error {
		st, err := loadState(p.dir)
		if err != nil {
			return err
		}
		rec := st.Swap
		if rec == nil || rec.Phase != phaseSwapped || rec.ToSHA256 != p.sha {
			return nil // another instance confirmed or reverted it
		}
		rec.Phase, rec.Boots, rec.Retained, rec.UpdatedAt = phaseConfirmed, nil, nil, time.Now().UTC()
		if err := saveState(p.dir, st); err != nil {
			return err
		}
		// Proven good: only its own rollback image is worth keeping.
		pruneBackups(filepath.Join(p.dir, "previous"), map[string]bool{rec.Previous: true})
		if safeName(rec.ToVersion) {
			_ = os.RemoveAll(filepath.Join(p.dir, "staged", rec.ToVersion))
		}
		p.log.Info("selfupdate: confirmed", "version", rec.ToVersion)
		return nil
	})
}

func (p *Pending) watchdog() {
	p.mu.Lock()
	if p.done {
		p.mu.Unlock()
		return
	}
	p.done = true
	p.mu.Unlock()
	p.revert(fmt.Sprintf("%s did not confirm within its deadline", p.To))
}

// revert puts the previous binary back and execs it. It returns only when
// it could not (the previous binary is gone, or exec failed), or when
// another instance confirmed the swap meanwhile.
func (p *Pending) revert(reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var path string
	err := withLock(ctx, p.dir, func() error {
		st, err := loadState(p.dir)
		if err != nil {
			return err
		}
		rec := st.Swap
		if rec == nil {
			return errors.New("no swap recorded")
		}
		now := time.Now().UTC()
		switch rec.Phase {
		case phaseConfirmed:
			return errors.New("another instance confirmed " + rec.ToVersion)
		case phaseReverted:
			path = rec.Path // another instance already put it back
			return nil
		case phaseSwapped:
		default:
			return errors.New("swap is " + rec.Phase)
		}
		st.markFailed(rec.ToVersion, reason, now)
		if err := replaceWithCopy(rec.Previous, rec.Path); err != nil {
			rec.Phase, rec.Reason, rec.UpdatedAt = "revert_failed", reason+"; revert failed: "+err.Error(), now
			return errors.Join(err, saveState(p.dir, st))
		}
		rec.Phase, rec.Reason, rec.Boots, rec.UpdatedAt = phaseReverted, reason, nil, now
		path = rec.Path
		return saveState(p.dir, st)
	})
	if err != nil {
		p.log.Error("selfupdate: revert", "reason", reason, "err", err)
		return
	}
	p.log.Error("selfupdate: reverted; restarting the previous binary", "failed", p.To, "reason", reason)
	if err := p.env.exec(path, os.Args, processEnv()); err != nil {
		p.log.Error("selfupdate: exec the previous binary", "err", err)
	}
}
