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
// A daemon with no data directory trusts only what this process made. The
// cost of that, and of a second daemon on the same home: its first credentialed
// command replaces a scratch another daemon made, so a credentialed step of the
// other daemon running at that moment loses its scratch (and fails closed).

// credScratchRecordName, in the daemon's data directory, holds the identity of
// the credentialed scratch it made.
const credScratchRecordName = "cred-scratch.identity"

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

func ensureCredScratchAt(dir, record string) error {
	fi, err := os.Lstat(dir)
	switch {
	case err == nil && fi.IsDir() && credScratchTrusted(fi, record):
		return os.Chmod(dir, 0o700)
	case err == nil:
		aside := dir + ".untrusted-" + strconv.FormatInt(time.Now().UnixNano(), 10)
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

// identity is a directory's device and inode.
func identity(fi os.FileInfo) string {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return strconv.FormatUint(uint64(st.Dev), 10) + ":" + strconv.FormatUint(st.Ino, 10) //nolint:unconvert // Dev is not uint64 on every architecture
}
