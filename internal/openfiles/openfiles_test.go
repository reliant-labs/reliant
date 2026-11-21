package openfiles

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeLsof puts an executable named `lsof` first on PATH that prints records
// and then runs tail, which decides how it ends.
func fakeLsof(t *testing.T, records, tail string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake lsof is a POSIX shell script")
	}
	dir := t.TempDir()
	data := filepath.Join(dir, "records")
	if err := os.WriteFile(data, []byte(records), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat '" + data + "'\n" + tail + "\n"
	if err := os.WriteFile(filepath.Join(dir, "lsof"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FORGE_OPENFILES_SOURCE", "lsof") // Linux reads /proc unless told otherwise
}

// TestHoldsComparesCanonicalPaths is the macOS defect in miniature: the caller
// names a path through a symlinked directory (/var → /private/var), lsof
// reports the resolved one, and the two must still match — in both directions.
func TestHoldsComparesCanonicalPaths(t *testing.T) {
	realRoot := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Fatal(err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(realRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(realRoot, "entry", "deep"), 0o700); err != nil {
		t.Fatal(err)
	}

	byResolved := FromPaths([]string{filepath.Join(resolvedRoot, "entry", "deep", "open.log")})
	for _, p := range []string{
		filepath.Join(link, "entry"),
		filepath.Join(link, "entry", "deep"),
		filepath.Join(resolvedRoot, "entry"),
	} {
		if !byResolved.Holds(p) {
			t.Errorf("Holds(%q) = false; a file under it is open (reported resolved)", p)
		}
	}

	byLink := FromPaths([]string{filepath.Join(link, "entry", "deep", "open.log")})
	if !byLink.Holds(filepath.Join(resolvedRoot, "entry")) {
		t.Error("a path reported through a symlink did not match its resolved spelling")
	}

	if byResolved.Holds(filepath.Join(link, "entry-sibling")) {
		t.Error("a sibling sharing a name prefix was reported in use")
	}
	if (Snapshot{}).Holds(link) {
		t.Error("the zero snapshot holds something")
	}
}

func TestTakeParsesACompleteListing(t *testing.T) {
	dir := t.TempDir()
	fakeLsof(t, "p1\nfcwd\nn"+dir+"\nf3\nn->0xdeadbeef\nf4\nn*:8080\n", "exit 0")
	snap, err := takeLsof(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Holds(dir) {
		t.Fatal("a process's working directory is not in the snapshot")
	}
}

// TestTakeAcceptsLsofsRoutineExitOne: lsof exits 1 whenever some process's
// files were inaccessible, which on a developer machine is normal.
func TestTakeAcceptsLsofsRoutineExitOne(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake lsof through shell subprocesses; runs in task test")
	}
	dir := t.TempDir()
	fakeLsof(t, "p1\nf3\nn"+dir+"/open\n", "exit 1")
	snap, err := takeLsof(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Holds(dir) {
		t.Fatal("exit 1 listing was not used")
	}
}

// TestTakeFailsClosed: every way lsof can end without a complete listing is an
// error, never a smaller snapshot. A partial listing says nothing about the
// processes lsof never reached.
func TestTakeFailsClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake lsof through shell subprocesses; runs in task test")
	}
	partial := "p1\nf3\nn/nonexistent/forge-openfiles-test/open\n"
	for _, tc := range []struct{ name, records, tail string }{
		{"killed mid-listing", partial, "kill -9 $$"},
		{"unexpected exit status", partial, "exit 2"},
		{"empty listing", "", "exit 0"},
		{"exit 1 with no listing", "", "exit 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeLsof(t, tc.records, tc.tail)
			if _, err := takeLsof(context.Background()); err == nil {
				t.Fatal("an incomplete lsof listing was accepted as a snapshot")
			}
		})
	}
}

func TestTakeFailsClosedOnDeadline(t *testing.T) {
	fakeLsof(t, "p1\nf3\nn/nonexistent/forge-openfiles-test/open\n", "exec sleep 30")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := takeLsof(ctx)
	if err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("a timed-out lsof must be an error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("Take outlived its deadline by %s", elapsed)
	}
}

func TestTakeLsofWithoutLsof(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows takes its snapshot through the Restart Manager and process table, not lsof (openfiles_windows.go)")
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := takeLsof(context.Background()); err == nil {
		t.Fatal("a machine with no lsof produced a snapshot")
	}
}
