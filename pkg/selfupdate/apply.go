package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Request starts applying a staged release: it drains through the app's
// [Drainer] until d.Deadline (default now + DrainTimeout), arms the swap and
// calls [Config.Shutdown]. The app tears down as on any stop, then its main
// calls [Updater.ExecPending] as its last act. A drain error aborts the
// upgrade with nothing swapped.
func (u *Updater) Request(ctx context.Context, s Staged, d Decision) error {
	switch {
	case d.Action != Apply:
		return fmt.Errorf("selfupdate: Request needs an Apply decision, got %s", d.Action)
	case u.cfg.Shutdown == nil:
		return errors.New("selfupdate: Config.Shutdown is required to apply an upgrade")
	case !swapSupported():
		return ErrUnsupported
	}
	if _, err := u.requireSelfOwned(); err != nil {
		return err
	}
	// Refuse before disrupting anything if the staged binary moved.
	if err := checkStaged(s); err != nil {
		return err
	}
	deadline := d.Deadline
	if deadline.IsZero() {
		deadline = u.now().Add(u.cfg.DrainTimeout)
	}
	if u.cfg.Drainer != nil {
		report, err := u.cfg.Drainer.Drain(ctx, deadline)
		if err != nil {
			return fmt.Errorf("selfupdate: the drain declined the upgrade to %s: %w", s.Release.Version, err)
		}
		u.log.InfoContext(ctx, "selfupdate: drained", "binary", u.cfg.Binary, "interrupted", report.Interrupted, "detail", report.Detail)
	}
	u.mu.Lock()
	u.pending = &s
	u.mu.Unlock()
	u.log.InfoContext(ctx, "selfupdate: swap armed; shutting down", "binary", u.cfg.Binary, "from", u.cfg.Current.Version, "to", s.Release.Version, "urgency", d.Urgency.String())
	u.cfg.Shutdown()
	return nil
}

// Armed reports whether [Updater.Request] armed a swap in this process.
func (u *Updater) Armed() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.pending != nil
}

func checkStaged(s Staged) error {
	got, err := sha256File(s.Path)
	if err != nil {
		return fmt.Errorf("selfupdate: staged binary: %w", err)
	}
	if got != s.SHA256 {
		return fmt.Errorf("selfupdate: staged binary %s changed since it was verified", s.Path)
	}
	return nil
}

// ExecPending performs the swap [Updater.Request] armed, after the app's
// teardown: it saves the running binary under StateDir/previous/ (a new
// file each time; older ones are pruned only once this swap is recorded,
// and never while an unconfirmed swap still needs one), renames the
// staged one over it, records the swap and execs it with the same argv and
// environment ([ExitForSupervisor]: exits with [ExitCodeRestart] instead).
// On Windows the running .exe is renamed aside to <name>.old-<ns>.exe first
// (it can be renamed, not replaced), and "exec" starts the new binary as a
// new process with the same argv, environment, working directory and stdio,
// then exits this one: the pid changes. The next start removes the images
// set aside once nothing runs them.
// It returns nil at once when no swap is armed, and otherwise returns only on
// failure, with the old binary put back.
func (u *Updater) ExecPending() error {
	u.mu.Lock()
	s := u.pending
	u.pending = nil
	u.mu.Unlock()
	if s == nil {
		return nil
	}
	if !swapSupported() {
		return ErrUnsupported
	}
	in, err := u.requireSelfOwned()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	backups := filepath.Join(u.cfg.StateDir, "previous")
	fsys, journal := u.fs.orHost(), journalPath(u.cfg.StateDir)
	var prev string
	err = withLock(ctx, u.cfg.StateDir, func() error {
		if err := checkStaged(*s); err != nil {
			return err
		}
		st, err := loadState(u.cfg.StateDir)
		if err != nil {
			return err
		}
		now := u.now().UTC()
		// A new, uniquely named backup: every earlier rollback image stays
		// on disk until this swap is committed below.
		prev, err = u.backupFn(in.Path, backups, u.cfg.Current.Version, now)
		if err != nil {
			return fmt.Errorf("selfupdate: save the running binary: %w", err)
		}
		if err := fsys.replaceWithCopy(s.Path, in.Path, now, journal); err != nil {
			return fmt.Errorf("selfupdate: swap: %w", err)
		}
		// An earlier swap that was never confirmed still needs its rollback
		// image: the binary being replaced now is itself unproven.
		var retained []string
		if old := st.Swap; old != nil && old.Phase == phaseSwapped {
			retained = append(append(retained, old.Previous), old.Retained...)
		}
		st.Swap = &swapRecord{
			Phase:           phaseSwapped,
			Path:            in.Path,
			Previous:        prev,
			Retained:        retained,
			FromVersion:     u.cfg.Current.Version,
			ToVersion:       s.Release.label(),
			ToSHA256:        s.SHA256,
			SwappedAt:       now,
			ConfirmWithinMS: u.cfg.ConfirmWithin.Milliseconds(),
			UpdatedAt:       now,
		}
		if got, err := sha256File(in.Path); err != nil || got != s.SHA256 {
			err = errors.Join(err, fmt.Errorf("selfupdate: %s does not hold the staged binary after the swap", in.Path))
			return errors.Join(err, fsys.replaceWithCopy(prev, in.Path, now, journal))
		}
		if err := saveState(u.cfg.StateDir, st); err != nil {
			// Without the record no Boot could revert it: put the old one back.
			return errors.Join(err, fsys.replaceWithCopy(prev, in.Path, now, journal))
		}
		// Committed: only now may backups nothing refers to go.
		keep := map[string]bool{prev: true}
		for _, r := range retained {
			keep[r] = true
		}
		pruneBackups(backups, keep)
		return nil
	})
	if err != nil {
		return err
	}
	u.log.Info("selfupdate: swapped; restarting", "binary", u.cfg.Binary, "path", in.Path, "to", s.Release.Version, "mode", u.cfg.SwapMode)

	if u.cfg.SwapMode == ExitForSupervisor {
		u.exitFn(ExitCodeRestart)
		return nil
	}
	execErr := u.execFn(in.Path, os.Args, processEnv())
	if execErr == nil {
		return nil // only a stand-in exec returns without error
	}
	// exec failed: the process is still the old image, so put the old
	// binary back on disk too and never offer this version again.
	rollbackErr := withLock(ctx, u.cfg.StateDir, func() error {
		st, err := loadState(u.cfg.StateDir)
		if err != nil {
			return err
		}
		now := u.now().UTC()
		if err := fsys.replaceWithCopy(prev, in.Path, now, journal); err != nil {
			return err
		}
		reason := "exec failed: " + execErr.Error()
		if st.Swap != nil {
			st.Swap.Phase, st.Swap.Reason, st.Swap.UpdatedAt = phaseReverted, reason, now
		}
		st.markFailed(s.Release.label(), reason, now)
		return saveState(u.cfg.StateDir, st)
	})
	return errors.Join(fmt.Errorf("selfupdate: start %s: %w", s.Release.Version, execErr), rollbackErr)
}
