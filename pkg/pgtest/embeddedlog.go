package pgtest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"

	"github.com/reliant-labs/forge/internal/openfiles"
)

// embedded-postgres leaks one temp log file per start, and this is where forge
// owns and reclaims it.
//
// github.com/fergusstrange/embedded-postgres@v1.34.0 logging.go does
// os.CreateTemp("", "embedded_postgres_log") on every Start and never removes
// the file. The directory is os.TempDir() (process-global, shared with every
// other process on the machine) and nothing about the path is configurable;
// Config.Logger only sets where the contents are FLUSHED to. So the file
// always appears in a shared directory, and the only question is who deletes
// it, and when.
//
// Two facts about the library decide the answer:
//
//   - flush() REOPENS the file by name (os.Open(s.file.Name())) whenever a
//     Logger is configured, and it runs at the end of Start and again inside
//     Stop. Unlinking the file early therefore breaks that instance's own
//     Start or Stop with "unable to process postgres logs: open …: no such
//     file or directory". The file must outlive the last flush.
//   - A file in the shared temp directory is not necessarily ours. An earlier
//     version deleted every embedded_postgres_log* file that APPEARED during
//     its own Start, reasoning that a writer keeps its descriptor. That is
//     false for a concurrent instance in another process (and in this one):
//     it broke their Start/Stop, which is how a sibling `go test` package and
//     forge's own hostinfra test failed.
//
// So each Start claims exactly the file IT created, identified by the one
// property no other process can share: this process holds it open. The
// library never closes its log, and CreateTemp returns the file already open,
// so "a log this process holds open that it did not hold before the Start" is
// that Start's file — provided the Start window is serialized within the
// process (startMu). Another process's log is never in our descriptor table.
//
// The claimed file is removed when the instance is done with it: in Stop,
// after the library's final flush, or at Detach for a caller whose server
// outlives the handle (hostinfra's postgres is stopped by a later process
// through pg_ctl, and the pgtest pool is stopped by whichever process
// detaches last; neither flushes again). Anything not claimed — historical
// leftovers, a process SIGKILLed between CreateTemp and its cleanup — is the
// stale sweep's, which removes a file only when no live process holds it.
const embeddedLogPrefix = "embedded_postgres_log"

// staleEmbeddedLogAge is the minimum age before the sweep even considers a
// file. Not the criterion (a live postgres's log is hours old and fine); a
// floor so a process in a PID namespace we cannot see into is not racing the
// sweep in the first seconds after its Start.
const staleEmbeddedLogAge = time.Hour

// startMu serializes StartEmbedded's Start window within this process, which
// is what makes "the log files this process newly holds" mean "this Start's".
// Starts were already serialized by the pool lock (pgtest) and are one-at-a-
// time in hostinfra; this makes it a property of the API.
var startMu sync.Mutex

// Embedded is a running embedded postgres plus the temp log file that belongs
// to it.
type Embedded struct {
	ep   *embeddedpostgres.EmbeddedPostgres
	logs []string
}

// Stop stops the server, then removes the log file this instance created. The
// library flushes that file once more inside Stop, so it is removed only
// afterwards. A failed Stop keeps the file: the instance may be stopped again,
// and that Stop needs it.
func (e *Embedded) Stop() error {
	err := e.ep.Stop()
	if err == nil || errors.Is(err, embeddedpostgres.ErrServerNotStarted) {
		e.Detach()
	}
	return err
}

// Detach removes the log file this instance created without stopping the
// server, for a caller that will never Stop through this handle because the
// server outlives it. The running postmaster keeps its descriptor, so it is
// unaffected. Calling Stop after Detach is an error in the library whenever a
// Logger is configured (its final flush reopens the removed file by name).
func (e *Embedded) Detach() {
	removeFiles(e.logs)
	e.logs = nil
}

// StartedLogs returns the log files this instance owns, for diagnostics.
func (e *Embedded) StartedLogs() []string { return append([]string(nil), e.logs...) }

// claimStartedLogs returns the logs held in after that were not held in
// before, sorted. Pure, so the ownership rule is testable without a server.
func claimStartedLogs(before, after map[string]bool) []string {
	var mine []string
	for p := range after {
		if !before[p] {
			mine = append(mine, p)
		}
	}
	sort.Strings(mine)
	return mine
}

// ownEmbeddedLogs lists the embedded_postgres_log* files under dir that this
// process holds open, spelled as dir spells them. open reports every path the
// process has a descriptor on; it is a parameter so tests need no real
// descriptors.
func ownEmbeddedLogs(dir string, open func() ([]string, error)) (map[string]bool, error) {
	paths, err := open()
	if err != nil {
		return nil, err
	}
	// The kernel reports resolved paths (macOS: /private/var/… for /var/…),
	// so compare directories resolved, but return dir's own spelling.
	realDir := dir
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		realDir = r
	}
	own := map[string]bool{}
	for _, p := range paths {
		base := filepath.Base(p)
		if !strings.HasPrefix(base, embeddedLogPrefix) {
			continue
		}
		if d := filepath.Dir(p); d != dir && d != realDir {
			continue
		}
		own[filepath.Join(dir, base)] = true
	}
	return own, nil
}

// removeFiles removes each path, ignoring failures: a log that is already
// gone, or (Windows) still open, is the sweep's problem, not an error.
func removeFiles(paths []string) {
	for _, p := range paths {
		_ = os.Remove(p)
	}
}

// sweepStaleEmbeddedLogs is the backstop for files nobody claimed: historical
// leftovers and files from a process killed before it cleaned up. A file goes
// only if it is at least maxAge old AND no live process holds it open — age
// alone cannot tell an abandoned file from a long-running server's log, whose
// Stop() reopens it by name days after Start. snapshot reports what is held;
// if it cannot answer, nothing is removed.
func sweepStaleEmbeddedLogs(dir string, now time.Time, maxAge time.Duration, snapshot func() (openfiles.Snapshot, error)) int {
	var candidates []string
	for _, p := range embeddedLogFiles(dir) {
		info, err := os.Stat(p)
		if err != nil || now.Sub(info.ModTime()) < maxAge {
			continue
		}
		candidates = append(candidates, p)
	}
	if len(candidates) == 0 {
		return 0
	}
	held, err := snapshot()
	if err != nil {
		return 0
	}
	removed := 0
	for _, p := range candidates {
		if held.Holds(p) {
			continue
		}
		if err := os.Remove(p); err == nil {
			removed++
		}
	}
	return removed
}

// machineOpenFiles is the sweep's default snapshot: every path any live
// process holds.
func machineOpenFiles() (openfiles.Snapshot, error) {
	return openfiles.Take(context.Background())
}

// embeddedLogFiles lists the leaked log files at the top level of dir.
// Regular files only: the name is a prefix match, and a directory someone
// else created with that prefix is not ours to delete.
func embeddedLogFiles(dir string) []string {
	matches, err := filepath.Glob(filepath.Join(dir, embeddedLogPrefix+"*"))
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range matches {
		if info, err := os.Lstat(p); err == nil && info.Mode().IsRegular() {
			out = append(out, p)
		}
	}
	return out
}
