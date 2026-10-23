package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// generateLockRel is the per-project lock every forge run that rewrites the
// project's generated outputs holds: `forge generate` (and everything that
// runs its pipeline — build's auto-generate, rescaffold, project new) and
// every `forge scaffold` command, for its whole run.
const generateLockRel = ".forge/forge.lock"

// acquireGenerateLock takes the project's exclusive generate lock, BLOCKING
// while another forge run holds it, and returns the function that releases
// it.
//
// Parallel agents sharing one checkout run generate concurrently, and two
// pipelines in one tree interleave their writes and, worse, their
// revert-on-failure rollbacks: each run restores what IT saw before writing,
// which can be the other run's fresh output, so a failed run's code survives a
// "reverted byte for byte" report. Queueing is the right behavior for that
// collision — the second run's inputs are as valid as the first's, it just
// has to go after — so a contended lock waits rather than fails. The wait is
// announced in one line naming the holder, because a silently stuck command
// is indistinguishable from a hung one.
//
// The lock is an OS file lock (flock(2); LockFileEx on Windows), not the
// file's existence. The OS drops it when the holding process exits however it
// exits, so a killed or crashed run never strands it: there is no staleness
// window, no pid-liveness probe, and nothing for anyone to `rm`. The previous
// O_EXCL marker file got all three wrong — it failed every concurrent run
// outright, told the agent to delete the live lock, and its pid/age reclaim
// could hand the lock to two runs at once. For the same reason the file is
// never deleted on release: a waiter blocked on the old inode and a newcomer
// that created a fresh one would both "hold" the lock.
//
// After acquiring, the holder records its pid and command in the file; that
// record exists only to make the waiting line informative.
//
// The lock is not reentrant: a second acquisition from the same process
// blocks like any other (forge also runs embedded in long-lived hosts, where
// two goroutines must exclude each other). Code already inside a held lock —
// a scaffold command running the pipeline — must not acquire it again.
func acquireGenerateLock(projectDir string) (release func(), err error) {
	lockPath := filepath.Join(projectDir, generateLockRel)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, fmt.Errorf("create .forge directory: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open generate lock %s: %w", lockPath, err)
	}

	acquired, err := tryLockFile(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s: %w", lockPath, err)
	}
	if !acquired {
		fmt.Fprintln(os.Stderr, generateLockWaitNotice(lockPath))
		if err := lockFile(f); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("lock %s: %w", lockPath, err)
		}
	}

	recordGenerateLockHolder(f)
	return func() {
		_ = unlockFile(f)
		_ = f.Close()
	}, nil
}

// generateLockWaitNotice is the one line a waiting run prints. The holder
// details come from the record the holder wrote after acquiring; a holder
// that has not written it yet (a sub-millisecond window) is reported without
// them rather than delaying the notice.
func generateLockWaitNotice(lockPath string) string {
	holder := "another forge run"
	if data, err := os.ReadFile(lockPath); err == nil {
		rec := parseGenerateLockRecord(data)
		if rec.pid > 0 {
			holder = fmt.Sprintf("pid %d", rec.pid)
			if rec.cmd != "" {
				holder += fmt.Sprintf(" (%s)", rec.cmd)
			}
		}
	}
	return fmt.Sprintf("⏳ forge: waiting for %s, which holds this project's generate lock (%s); this run starts as soon as it finishes.", holder, generateLockRel)
}

// recordGenerateLockHolder overwrites the lock file's body with this
// process's pid and command. Best effort: the record only feeds the waiting
// notice, so a failed write never fails the run.
func recordGenerateLockHolder(f *os.File) {
	body := fmt.Sprintf("pid=%d\ncmd=%s\ntime=%s\n", os.Getpid(), generateLockCommandLine(), time.Now().Format(time.RFC3339))
	if err := f.Truncate(0); err != nil {
		return
	}
	_, _ = f.WriteAt([]byte(body), 0)
}

// generateLockCommandLine is a short, single-line rendering of how this
// process was invoked ("forge generate", "reliant forge scaffold rpc …").
func generateLockCommandLine() string {
	if len(os.Args) == 0 {
		return ""
	}
	parts := append([]string{filepath.Base(os.Args[0])}, os.Args[1:]...)
	line := strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
	const maxLen = 80
	if len(line) > maxLen {
		line = line[:maxLen] + "…"
	}
	return line
}

// generateLockRecord is the parsed holder record ("pid=<n>\ncmd=<...>\n…").
type generateLockRecord struct {
	pid int
	cmd string
}

func parseGenerateLockRecord(body []byte) generateLockRecord {
	var rec generateLockRecord
	for _, line := range strings.Split(string(body), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "pid":
			if pid, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && pid > 0 {
				rec.pid = pid
			}
		case "cmd":
			rec.cmd = strings.TrimSpace(value)
		}
	}
	return rec
}
