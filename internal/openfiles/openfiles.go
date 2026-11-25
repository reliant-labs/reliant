// Package openfiles answers the one question every reclaimer that deletes from
// a shared directory must ask first: is a live process using this path?
//
// Three reclaimers ask it — the temp sweep, source-cache eviction and worktree
// removal — and each had its own lsof parser, which went wrong in the same
// three ways:
//
//   - PATH SPELLING. lsof reports the path the kernel resolved, so on macOS a
//     file under os.TempDir() (/var/folders/…) is reported as
//     /private/var/folders/…. A string comparison against the unresolved form
//     matched 2 of 266 open files under $TMPDIR on one Mac: the in-use check
//     was off and nothing said so. Snapshot compares canonical paths on BOTH
//     sides.
//   - PARTIAL SNAPSHOTS. A timed-out or killed lsof has already written part of
//     its listing. Treating that prefix as the whole machine reports every
//     process it never reached as idle. Take accepts only a listing lsof
//     finished: exit 0, or lsof's routine exit 1 for an inaccessible process.
//   - NO TIMEOUT. One caller ran lsof unbounded, so a wedged lsof hung
//     `forge env up`. Take always runs under Timeout.
//
// The /proc source (openfiles_proc.go) avoids the same three by construction:
// it feeds FromPaths, scans every process before answering or fails, and runs
// under Timeout.
//
// Every failure is an error, and every caller treats an error as "everything
// is in use". Without the snapshot, age is the only evidence left, and age
// cannot tell abandoned from merely quiet.
//
//forge:exclude-contract: a value type and the function that takes it; consumers inject a func(string) bool predicate, so there is no seam for a contract to describe
package openfiles

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Timeout bounds one snapshot. lsof takes seconds on a busy developer machine
// (~6s measured across ~74k records); minutes means it is wedged on a hung
// mount, and an answer that never arrives is no better than a wrong one.
const Timeout = 2 * time.Minute

// waitDelay bounds how long Take waits for lsof's output pipe to close after
// the process is killed, so a child that inherited the pipe cannot extend the
// timeout indefinitely.
const waitDelay = 5 * time.Second

// SourceEnv names the environment variable that, set to "lsof", makes Take use
// lsof on Linux instead of /proc: an escape hatch for a host whose procfs is
// restricted in a way lsof is not (and the seam the tests use to drive a fake
// lsof).
const SourceEnv = "FORGE_OPENFILES_SOURCE"

// Snapshot is the set of paths live processes held at one instant: open files,
// working directories, mapped executables. The zero value holds nothing.
type Snapshot struct {
	// covered holds every reported path AND every ancestor directory of it,
	// in both the spelling lsof used and the canonical one. Holds is then a
	// lookup: a path is in use exactly when something at or under it is.
	covered map[string]bool

	// probe, when set, answers Holds on demand instead of from covered. A
	// platform with no machine-wide listing (Windows) can only ask about a
	// path at a time; probe must fail closed, answering true when it cannot
	// tell.
	probe func(path string) bool
}

// Take records one machine-wide snapshot: from /proc on Linux, with lsof on
// other Unixes, lazily on Windows.
//
// Linux reads /proc rather than running lsof because lsof is itself only a
// reader of /proc there. Going to the source needs no binary (minimal images,
// containers and sandboxes ship none, and every caller fails closed without a
// snapshot, so a missing lsof meant nothing was ever reclaimed), no subprocess
// whose output can be cut short, and no parsing of a second format. macOS has
// no /proc, so lsof stays the source there.
//
// One snapshot rather than a probe per candidate: a per-path `lsof +D` would
// race — a path could be opened between its own probe and its removal.
func Take(ctx context.Context) (Snapshot, error) {
	switch runtime.GOOS {
	case "windows":
		return takeLazy(ctx)
	case "linux":
		if os.Getenv(SourceEnv) == "lsof" {
			return takeLsof(ctx)
		}
		return takeProc(ctx, procRoot)
	}
	return takeLsof(ctx)
}

// takeLsof records one machine-wide snapshot with lsof.
func takeLsof(ctx context.Context) (Snapshot, error) {
	if _, err := exec.LookPath("lsof"); err != nil {
		return Snapshot{}, fmt.Errorf("lsof not found: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	// -Fn: one field per line, names prefixed 'n'. -w: no warning lines. The
	// default selection includes each process's cwd, root dir and text file.
	cmd := exec.CommandContext(ctx, "lsof", "-Fn", "-w")
	cmd.WaitDelay = waitDelay
	out, err := cmd.Output()
	if ctxErr := ctx.Err(); ctxErr != nil {
		// Whatever was read before the kill is a prefix of the listing.
		return Snapshot{}, fmt.Errorf("lsof did not finish: %w", ctxErr)
	}
	if err != nil {
		var exit *exec.ExitError
		// ExitCode is -1 for a process killed by a signal, which is a
		// partial listing for the same reason a timeout is. Exit 1 is lsof's
		// routine "some process's files could not be listed".
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return Snapshot{}, fmt.Errorf("lsof: %w", err)
		}
	}
	paths := parse(string(out))
	if len(paths) == 0 {
		return Snapshot{}, errors.New("lsof reported no open paths")
	}
	return FromPaths(paths), nil
}

// parse extracts the absolute names from `lsof -Fn` output. Sockets, pipes and
// anonymous descriptors carry names that are not paths and are skipped.
func parse(out string) []string {
	var paths []string
	scanner := bufio.NewScanner(strings.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) > 1 && line[0] == 'n' && line[1] == '/' {
			paths = append(paths, line[1:])
		}
	}
	return paths
}

// FromPaths builds a snapshot from paths already known to be open. Take uses
// it for lsof's output; tests use it to state exactly what is open.
func FromPaths(paths []string) Snapshot {
	s := Snapshot{covered: map[string]bool{}}
	// Directories repeat heavily across a listing (~3k distinct directories
	// for ~17k distinct paths measured), so each is resolved once.
	resolvedDirs := map[string]string{}
	for _, p := range paths {
		clean := filepath.Clean(p)
		s.cover(clean)
		dir := filepath.Dir(clean)
		real, seen := resolvedDirs[dir]
		if !seen {
			if r, err := filepath.EvalSymlinks(dir); err == nil {
				real = r
			}
			resolvedDirs[dir] = real
		}
		if real != "" {
			s.cover(filepath.Join(real, filepath.Base(clean)))
		}
	}
	return s
}

// cover marks path and every ancestor of it.
func (s Snapshot) cover(path string) {
	for {
		if s.covered[path] {
			return // its ancestors were marked when it was
		}
		s.covered[path] = true
		parent := filepath.Dir(path)
		if parent == path {
			return
		}
		path = parent
	}
}

// Holds reports whether a live process holds path itself or anything under it.
// path is compared in both its given and its canonical spelling, so it matches
// however the snapshot's paths and the caller's path were reached.
func (s Snapshot) Holds(path string) bool {
	if s.probe != nil {
		return s.probe(path)
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	path = filepath.Clean(path)
	if s.covered[path] {
		return true
	}
	if real, err := filepath.EvalSymlinks(path); err == nil && s.covered[real] {
		return true
	}
	// A path that no longer exists cannot be resolved itself, but its
	// directory usually can.
	if real, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		return s.covered[filepath.Join(real, filepath.Base(path))]
	}
	return false
}
