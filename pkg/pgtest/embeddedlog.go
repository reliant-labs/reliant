package pgtest

import (
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// embedded-postgres leaks one temp log file per start, and this is where forge
// reclaims it.
//
// github.com/fergusstrange/embedded-postgres@v1.34.0 logging.go:18 does
// os.CreateTemp("", "embedded_postgres_log") on every Start and never removes
// the file — not in Stop, not on a failed start. There is no way to prevent
// it: the directory is hard-coded to "" (os.TempDir), the path is not
// configurable, and Config.Logger only sets where the contents are FLUSHED to,
// not whether the file is created. So the file always appears, and the only
// question is who deletes it. One host had 197 of them.
//
// Both of forge's callers — pkg/pgtest's bootEmbedded and
// internal/hostinfra's Start — go through StartEmbedded, so removing it there
// covers every start forge performs, including the ones that fail.
//
// Unlinking is safe even though postgres is still writing to the file: on Unix
// the open descriptor keeps the inode alive, so the writer is unaffected and
// the space is reclaimed when the server exits. That is what makes
// deterministic removal possible at all here — the alternative would be
// waiting for a Stop that, for hostinfra, happens in a different process
// entirely (its server outlives `forge env up` by design).
const embeddedLogPrefix = "embedded_postgres_log"

// staleEmbeddedLogAge is how old a leftover log file must be before the
// backstop sweep removes it. Generous: a live boot's file is seconds old.
const staleEmbeddedLogAge = time.Hour

// embeddedLogSnapshot records the leaked log files that exist right now, so
// the ones a start creates can be told apart from everyone else's.
func embeddedLogSnapshot(dir string) map[string]bool {
	existing := map[string]bool{}
	for _, p := range embeddedLogFiles(dir) {
		existing[p] = true
	}
	return existing
}

// removeNewEmbeddedLogs deletes the log files that appeared since the
// snapshot, returning how many it removed.
//
// A concurrent start in another process could have created a file in the same
// window, and this cannot distinguish it from ours. Removing it anyway is
// harmless for the reason above — that process keeps its descriptor and keeps
// writing — and the alternative (leave anything ambiguous) is how the leak got
// to 197 files.
func removeNewEmbeddedLogs(dir string, before map[string]bool) int {
	if runtime.GOOS == "windows" {
		// Windows refuses to unlink a file another process holds open, so the
		// age-based sweep below is the only option there.
		return 0
	}
	removed := 0
	for _, p := range embeddedLogFiles(dir) {
		if before[p] {
			continue
		}
		if err := os.Remove(p); err == nil {
			removed++
		}
	}
	return removed
}

// sweepStaleEmbeddedLogs is the backstop for files this process did not
// create: historical leftovers, files from a process that was SIGKILLed
// between CreateTemp and the removal above, and every file on Windows. Age is
// the only gate it needs — a file still being written to by a live boot is
// seconds old, never an hour.
func sweepStaleEmbeddedLogs(dir string, now time.Time, maxAge time.Duration) int {
	removed := 0
	for _, p := range embeddedLogFiles(dir) {
		info, err := os.Stat(p)
		if err != nil || now.Sub(info.ModTime()) < maxAge {
			continue
		}
		if err := os.Remove(p); err == nil {
			removed++
		}
	}
	return removed
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
