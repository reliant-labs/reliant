package pgtest

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/openfiles"
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

// A Start owns the log files THIS process newly holds open, and nothing else:
// a file another process created is in the directory but never in our
// descriptor table, however it appeared.
func TestOwnEmbeddedLogs_OnlyWhatThisProcessHolds(t *testing.T) {
	dir := t.TempDir()
	mine := filepath.Join(dir, "embedded_postgres_log111")
	open := func() ([]string, error) {
		return []string{
			mine,
			filepath.Join(dir, "unrelated-file"),
			filepath.Join(t.TempDir(), "embedded_postgres_log222"), // another directory
			"/usr/lib/libc.so.6",
		}, nil
	}
	got, err := ownEmbeddedLogs(dir, open)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]bool{mine: true}; !reflect.DeepEqual(got, want) {
		t.Errorf("owned = %v, want %v", got, want)
	}
}

func TestOwnEmbeddedLogs_ErrorClaimsNothing(t *testing.T) {
	_, err := ownEmbeddedLogs(t.TempDir(), func() ([]string, error) { return nil, errors.New("no /proc") })
	if err == nil {
		t.Fatal("an unreadable descriptor table must be an error, so StartEmbedded claims nothing")
	}
}

func TestClaimStartedLogs(t *testing.T) {
	before := map[string]bool{"/t/embedded_postgres_log1": true}
	after := map[string]bool{"/t/embedded_postgres_log1": true, "/t/embedded_postgres_log3": true, "/t/embedded_postgres_log2": true}
	want := []string{"/t/embedded_postgres_log2", "/t/embedded_postgres_log3"}
	if got := claimStartedLogs(before, after); !reflect.DeepEqual(got, want) {
		t.Errorf("claimed %v, want %v", got, want)
	}
}

// ownOpenPaths must really see a file this process holds, or no Start ever
// claims its log and every one leaks to the sweep.
func TestOwnOpenPaths_SeesAnOpenFile(t *testing.T) {
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, embeddedLogPrefix)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	got, err := ownEmbeddedLogs(dir, ownOpenPaths)
	if err != nil {
		t.Skipf("this platform cannot list own open files: %v", err)
	}
	if !got[f.Name()] {
		t.Errorf("an open %s file is not reported as owned: %v", embeddedLogPrefix, got)
	}
}

// Detach removes exactly the files the handle owns.
func TestEmbeddedDetach_RemovesOnlyOwnedLogs(t *testing.T) {
	dir := t.TempDir()
	mine := writeLog(t, dir, "embedded_postgres_log111", 0)
	other := writeLog(t, dir, "embedded_postgres_log222", 0)
	e := &Embedded{logs: []string{mine}}
	e.Detach()
	if _, err := os.Stat(mine); !os.IsNotExist(err) {
		t.Errorf("the instance's own log survived Detach (%v)", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("Detach removed a log it does not own (%v)", err)
	}
	e.Detach() // idempotent
}

// The sweep removes a file only when it is old AND unheld. A long-running
// server's log is old and held; age alone must never delete it.
func TestSweepStaleEmbeddedLogs_RemovesOnlyOldUnheldFiles(t *testing.T) {
	dir := t.TempDir()
	staleFree := writeLog(t, dir, "embedded_postgres_log333", 2*time.Hour)
	staleHeld := writeLog(t, dir, "embedded_postgres_log444", 48*time.Hour)
	fresh := writeLog(t, dir, "embedded_postgres_log555", 30*time.Second)
	otherDir := filepath.Join(dir, "embedded_postgres_log_dir")
	if err := os.MkdirAll(otherDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(otherDir, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	unrelated := writeLog(t, dir, "someone-elses-file", 48*time.Hour)

	snap := func() (openfiles.Snapshot, error) { return openfiles.FromPaths([]string{staleHeld}), nil }
	if got := sweepStaleEmbeddedLogs(dir, time.Now(), staleEmbeddedLogAge, snap); got != 1 {
		t.Errorf("sweep removed %d files, want 1", got)
	}
	if _, err := os.Stat(staleFree); !os.IsNotExist(err) {
		t.Errorf("an old, unheld log survived the sweep (%v)", err)
	}
	for name, p := range map[string]string{
		"an old log a live process holds": staleHeld,
		"a young log":                     fresh,
		"a directory with the prefix":     otherDir,
		"an unrelated temp file":          unrelated,
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("sweep removed %s (%v)", name, err)
		}
	}
}

// If nobody can say what is held, nothing is removed: age is not evidence.
func TestSweepStaleEmbeddedLogs_FailsClosedWithoutASnapshot(t *testing.T) {
	dir := t.TempDir()
	old := writeLog(t, dir, "embedded_postgres_log666", 72*time.Hour)
	snap := func() (openfiles.Snapshot, error) { return openfiles.Snapshot{}, errors.New("no /proc, no lsof") }
	if got := sweepStaleEmbeddedLogs(dir, time.Now(), staleEmbeddedLogAge, snap); got != 0 {
		t.Errorf("sweep removed %d files without a snapshot", got)
	}
	if _, err := os.Stat(old); err != nil {
		t.Errorf("an old log was removed without evidence that nobody holds it (%v)", err)
	}
}
