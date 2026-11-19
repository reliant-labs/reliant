// Copyright (c) 2026 Reliant Labs
//go:build linux

package credsandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/logging"
)

// The credentialed scratch (CredScratchDir) is trusted only when a daemon of
// sandbox version 2 made it. Under version 2 no credentials-none command can
// reach it; under an older daemon one could have made it, with whatever it
// liked inside, before the upgrade. Owner and mode prove nothing about that:
// the command ran as the daemon's user. So the daemon records, where no
// credentials-none command has ever been able to write (its data directory,
// hidden since the first sandbox; daemonpolicy.DaemonDataDir), the identity
// (device and inode) of the directory it made, and trusts only that one.
// Anything else at the path is moved aside, kept for inspection, and replaced.
//
// A process with no data directory — not a daemon: a test binary, or anything
// else that runs commands through this package — trusts only what it made
// itself, and replaces nothing. The scratch at the path is then almost always
// the daemon's, recorded where this process cannot look. Moving it aside made
// that daemon move this process's replacement aside in turn: one machine
// collected 669 cred-scratch.untrusted-* directories in four days of agents
// running tests (#2823). So such a process makes the scratch only where there
// is none; a directory it did not make it leaves where it is, untrusted, and
// its credentialed commands get no SignalEnv (they fail closed).
//
// A second daemon on the same home still replaces a scratch another daemon
// made, so a credentialed step of the other daemon running at that moment
// loses its scratch (and fails closed). What is moved aside is kept for
// inspection for movedAsideRetention, then pruned when a daemon starts
// (PruneMovedAside).

// credScratchRecordName, in the daemon's data directory, holds the identity of
// the credentialed scratch it made.
const credScratchRecordName = "cred-scratch.identity" //nolint:gosec // G101: a file name in the data directory, not a credential

var scratchState struct {
	sync.Mutex
	// made is the identity this process made or verified, when there is no
	// record to keep it in.
	made string
}

// ensureCredScratch makes CredScratchDir, mode 0700, unless the directory there
// is the one a version-2 daemon made (credScratchTrusted); anything else there
// is moved aside first, never followed or reused.
func ensureCredScratch() error {
	scratchState.Lock()
	defer scratchState.Unlock()
	dir := CredScratchDir()
	if dir == "" {
		return errors.New("no home directory")
	}
	return ensureCredScratchAt(dir, credScratchRecord())
}

func credScratchRecord() string {
	if d := daemonpolicy.DaemonDataDir(); d != "" {
		return filepath.Join(d, credScratchRecordName)
	}
	return ""
}

// movedAsideSuffix is what ensureCredScratchAt appends, with the UnixNano time
// of the move, to a directory it moves aside.
const movedAsideSuffix = ".untrusted-"

// movedAsideRetention is how long a moved-aside scratch is kept for inspection.
const movedAsideRetention = 24 * time.Hour

func ensureCredScratchAt(dir, record string) error {
	fi, err := os.Lstat(dir)
	switch {
	case err == nil && fi.IsDir() && credScratchTrusted(fi, record):
		return os.Chmod(dir, 0o700)
	case err == nil && record == "":
		return errScratchNotMade
	case err == nil:
		aside := dir + movedAsideSuffix + strconv.FormatInt(time.Now().UnixNano(), 10)
		if err := os.Rename(dir, aside); err != nil {
			return fmt.Errorf("moving aside a credentialed scratch directory no version-2 daemon made: %w", err)
		}
		logging.Warn("credsandbox: moved aside a credentialed scratch directory no version-2 daemon made", "path", dir, "moved_to", aside)
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if err := unix.Mkdir(dir, 0o700); err != nil {
		return fmt.Errorf("making the credentialed scratch directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	fi, err = os.Lstat(dir)
	if err != nil {
		return err
	}
	id := identity(fi)
	scratchState.made = id
	if record == "" {
		return nil
	}
	if err := os.WriteFile(record+".new", []byte(id+"\n"), 0o600); err != nil {
		return fmt.Errorf("recording the credentialed scratch directory: %w", err)
	}
	return os.Rename(record+".new", record)
}

// credScratchTrusted reports whether fi, a directory at CredScratchDir, is the
// one a version-2 daemon made: the identity recorded in the data directory, or
// without one, the identity this process made.
func credScratchTrusted(fi os.FileInfo, record string) bool {
	if !ownedByUs(fi) {
		return false
	}
	id := identity(fi)
	if id == "" {
		return false
	}
	if record == "" {
		return id == scratchState.made
	}
	raw, err := os.ReadFile(record)
	return err == nil && strings.TrimSpace(string(raw)) == id
}

// PruneMovedAside removes the credentialed scratch directories this package
// moved aside (CredScratchDir + ".untrusted-<ns>") more than a day ago, and
// returns how many it removed. The daemon calls it when it starts. Only an
// entry this user owns is removed, and only by the name the move gave it, so
// nothing else beside the scratch is touched; a planted link that was moved
// aside is unlinked, never followed.
func PruneMovedAside() (int, error) {
	dir := CredScratchDir()
	if dir == "" {
		return 0, nil
	}
	return pruneMovedAsideAt(dir, time.Now(), movedAsideRetention)
}

func pruneMovedAsideAt(dir string, now time.Time, retention time.Duration) (int, error) {
	parent, prefix := filepath.Dir(dir), filepath.Base(dir)+movedAsideSuffix
	entries, err := os.ReadDir(parent)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	removed := 0
	var errs []error
	for _, e := range entries {
		ns, ok := strings.CutPrefix(e.Name(), prefix)
		if !ok {
			continue
		}
		at, err := strconv.ParseInt(ns, 10, 64)
		if err != nil || now.Sub(time.Unix(0, at)) < retention {
			continue
		}
		path := filepath.Join(parent, e.Name())
		fi, err := os.Lstat(path)
		if err != nil || !ownedByUs(fi) {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

// identity is a directory's device and inode.
func identity(fi os.FileInfo) string {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return strconv.FormatUint(uint64(st.Dev), 10) + ":" + strconv.FormatUint(st.Ino, 10) //nolint:unconvert // Dev is not uint64 on every architecture
}
