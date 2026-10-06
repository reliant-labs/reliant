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

// holderProbeTimeout bounds the external probe.
//
// lsof walks every process's descriptor table, so its cost scales with the
// number of processes on the host and how loaded it is, not with the file
// being asked about. Measured for this exact query on one macOS dev host with
// ~850–870 processes: 0.25s when quiet, up to 7.9s at load average ~45. The
// previous 2s limit therefore timed out on a busy machine, and a timeout
// answers Unknown — so a stranded index.lock was never recovered and every
// index write in that repository kept failing.
//
// Ten seconds is affordable because of where this runs. It is the recovery
// path only: gitutil stats the lock first and consults the probe only for a
// lock that exists and is past its age floor, so the healthy case never pays
// it, and the git command waiting on it would otherwise fail outright. It
// clears the slowest run observed, and stays under lsof's own 15s default for
// a blocking kernel call (lsof -S), so a probe wedged on an unresponsive mount
// is cut off here rather than outliving the command it is meant to unblock.
const holderProbeTimeout = 10 * time.Second

// lsofResult is one lsof run reduced to the facts the verdict depends on, so
// the interpretation can be tested against scripted results rather than the
// real lsof's timing.
type lsofResult struct {
	stdout string
	stderr string
	// exitCode is lsof's exit status, or -1 when it did not exit on its own:
	// it could not be started, or it was killed.
	exitCode int
}

// runLsof asks lsof which processes hold path open.
func runLsof(ctx context.Context, path string) lsofResult {
	// -t: pids only, one per line.
	// -n -P: never convert addresses to host names or ports to service
	//   names. -t prints neither, so this cannot change the answer; it
	//   guarantees lsof does not wait on DNS or the services database for
	//   the many sockets it walks past on the way to the file asked about.
	// -w: suppress warnings. -t already selects it (lsof(8)), so this changes
	//   nothing; it is spelled out because the stderr check in
	//   probeFileHolders relies on stderr carrying errors only. A warning about
	//   some unrelated mount lsof could not stat would otherwise turn every
	//   answer on that host into Unknown.
	// --: guards a path that looks like a flag.
	cmd := exec.CommandContext(ctx, "lsof", "-t", "-n", "-P", "-w", "--", path)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()

	result := lsofResult{stdout: string(out), stderr: stderr.String()}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		result.exitCode = 0
	case errors.As(err, &exitErr):
		result.exitCode = exitErr.ExitCode() // -1 when killed by a signal
	default:
		result.exitCode = -1 // lsof missing or not executable
	}
	return result
}

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
	return probeFileHolders(ctx, path, holderProbeTimeout, runLsof)
}

// probeFileHolders is fileHolders with the lsof run and its timeout passed
// in, so the verdict logic is testable without depending on how fast the real
// lsof is on the test host.
func probeFileHolders(ctx context.Context, path string, timeout time.Duration, run func(context.Context, string) lsofResult) FileHoldState {
	if _, err := os.Stat(path); err != nil {
		// Gone, or unreadable. Either way this is not a confident "not held".
		return FileHoldUnknown
	}

	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result := run(probeCtx, path)

	if len(strings.TrimSpace(result.stdout)) > 0 {
		return FileHoldHeld
	}

	if probeCtx.Err() != nil {
		return FileHoldUnknown // timed out: no answer, not "nobody"
	}

	if result.exitCode != 0 {
		// lsof exits 1 for "no holders" AND for several look-up failures. A
		// clean stderr is what distinguishes them. Any other status, including
		// -1 for a probe that never ran or was killed, is no answer at all.
		if result.exitCode != 1 || strings.TrimSpace(result.stderr) != "" {
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
