package pgtest

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// writeLog creates a leaked-log-shaped file with the given mtime.
func writeLog(t *testing.T, dir, name string, age time.Duration) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("LOG: database system is ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-age)
	if err := os.Chtimes(p, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestRemoveNewEmbeddedLogsRemovesOnlyWhatTheStartCreated pins the snapshot
// contract: the library creates one embedded_postgres_log file per Start and
// never removes it, so forge removes the ones that appear during a Start —
// and nothing that was already there, which belongs to a concurrent boot in
// another process whose own removal will cover it.
func TestRemoveNewEmbeddedLogsRemovesOnlyWhatTheStartCreated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("removal of an open file is refused on Windows; the age sweep covers it there")
	}
	dir := t.TempDir()
	preexisting := writeLog(t, dir, "embedded_postgres_log111", 0)

	before := embeddedLogSnapshot(dir)

	// What the library does inside Start.
	created := writeLog(t, dir, "embedded_postgres_log222", 0)

	if got := removeNewEmbeddedLogs(dir, before); got != 1 {
		t.Errorf("removeNewEmbeddedLogs removed %d files, want 1", got)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Errorf("the log this start created survived (%v) — this is the leak, one file per boot", err)
	}
	if _, err := os.Stat(preexisting); err != nil {
		t.Errorf("a pre-existing log was removed (%v); it may belong to a concurrent boot", err)
	}
}

// TestSweepStaleEmbeddedLogsIsTheBackstop covers what the snapshot cannot: the
// 197 historical files already on disk, and a process SIGKILLed between the
// library's CreateTemp and forge's removal. Age is the whole gate — a file a
// live boot is still writing to is seconds old, never an hour.
func TestSweepStaleEmbeddedLogsIsTheBackstop(t *testing.T) {
	dir := t.TempDir()
	stale := writeLog(t, dir, "embedded_postgres_log333", 2*time.Hour)
	fresh := writeLog(t, dir, "embedded_postgres_log444", 30*time.Second)
	// A directory sharing the prefix is not a leaked log file and not ours.
	otherDir := filepath.Join(dir, "embedded_postgres_log_dir")
	if err := os.MkdirAll(otherDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(otherDir, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	// An unrelated temp file must be invisible to the sweep.
	unrelated := writeLog(t, dir, "someone-elses-file", 48*time.Hour)

	if got := sweepStaleEmbeddedLogs(dir, time.Now(), staleEmbeddedLogAge); got != 1 {
		t.Errorf("sweepStaleEmbeddedLogs removed %d files, want 1", got)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale log survived the sweep (%v)", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("sweep removed a log a live boot may still be writing (%v)", err)
	}
	if _, err := os.Stat(otherDir); err != nil {
		t.Errorf("sweep removed a directory, not a log file (%v)", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Errorf("sweep removed an unrelated temp file (%v) — it matches no prefix of ours", err)
	}
}
