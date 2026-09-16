// Copyright (c) 2025 Reliant Labs
//go:build !windows

package osutil

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

// holderProbeTimeout bounds the external probe. lsof walks every process's
// descriptor table and can block on an unresponsive network mount; measured
// at ~0.2s locally, so two seconds is generous for the healthy case while
// keeping a wedged probe from stalling a git command.
const holderProbeTimeout = 2 * time.Second

// fileHolders answers the open-descriptor question via lsof.
//
// lsof rather than a hand-rolled scan because the portable alternative does
// not exist: /proc/<pid>/fd is Linux-only (this daemon also runs on macOS,
// where there is no /proc at all — verified on the dev host), and reading
// every process's descriptor table directly requires privileges lsof already
// encapsulates.
//
// Exit-status handling is the part that matters for safety. lsof exits 1 both
// when nothing holds the file AND when it failed to look properly, so exit
// status alone cannot be trusted to mean "not held". Two guards:
//
//   - The file must still exist. lsof reports "no such file" as exit 1, which
//     would otherwise read as a confident "not held".
//   - Anything on stderr, or a missing lsof binary, or a timeout, degrades to
//     Unknown rather than NotHeld.
//
// Every failure mode lands on Unknown, which callers treat as "leave it
// alone". The probe can refuse to answer; it must never answer wrongly in the
// direction that deletes a live lock.
func fileHolders(ctx context.Context, path string) FileHoldState {
	if _, err := os.Stat(path); err != nil {
		// Gone, or unreadable. Either way this is not a confident "not held".
		return FileHoldUnknown
	}

	probeCtx, cancel := context.WithTimeout(ctx, holderProbeTimeout)
	defer cancel()

	// -t: pids only, one per line. `--` guards a path that looks like a flag.
	cmd := exec.CommandContext(probeCtx, "lsof", "-t", "--", path)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()

	if len(strings.TrimSpace(string(out))) > 0 {
		return FileHoldHeld
	}

	if probeCtx.Err() != nil {
		return FileHoldUnknown // timed out: no answer, not "nobody"
	}

	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			// lsof missing or not executable: no probe available.
			return FileHoldUnknown
		}
		// lsof exits 1 for "no holders" AND for several look-up failures. A
		// clean stderr is what distinguishes them.
		if strings.TrimSpace(stderr.String()) != "" {
			return FileHoldUnknown
		}
		if exitErr.ExitCode() != 1 {
			return FileHoldUnknown
		}
	}

	// Re-confirm the file still exists: a file deleted while lsof ran also
	// produces empty output, and that is not evidence about holders.
	if _, statErr := os.Stat(path); statErr != nil {
		return FileHoldUnknown
	}

	return FileHoldNotHeld
}
