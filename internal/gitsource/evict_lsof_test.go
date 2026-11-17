package gitsource

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// fakeLsof puts an executable named `lsof` first on PATH that prints records
// and then runs tail, which decides how it ends ("exit 0", "kill -9 $$").
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

// staleSourceEntry writes a complete cache entry last used age ago.
func staleSourceEntry(t *testing.T, root, name string, age time.Duration) string {
	t.Helper()
	entry := filepath.Join(root, name)
	if err := os.MkdirAll(entry, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(entry, MetadataFile)
	if err := os.WriteFile(marker, []byte(`{"repo":"github.com/acme/app","ref":"v1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-age)
	if err := os.Chtimes(marker, at, at); err != nil {
		t.Fatal(err)
	}
	return entry
}

// TestEvictDefaultProbeFailsClosed is B1's source-cache half. The default
// in-use probe used to run lsof with no timeout (a wedged lsof hung `forge env
// up`), accepted a killed lsof's partial listing as complete, and on any other
// failure returned "cannot tell" and evicted on recency alone. Now every
// failure to get a complete snapshot retains every candidate.
func TestEvictDefaultProbeFailsClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake lsof through shell subprocesses; runs in task test")
	}
	for _, tc := range []struct{ name, tail string }{
		{"killed mid-listing", "kill -9 $$"},
		{"unexpected exit status", "exit 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			now := time.Now()
			staleSourceEntry(t, root, "app-aaaaaaaaaaaa", 0)
			doomed := staleSourceEntry(t, root, "app-bbbbbbbbbbbb", 90*24*time.Hour)
			fakeLsof(t, "p1\nf3\nn/nonexistent/forge-evict-test/open\n", tc.tail)
			got, err := Evict(root, now, EvictPolicy{KeepPerSlug: 1}, true, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, statErr := os.Stat(doomed); statErr != nil || len(got.Removed) != 0 {
				t.Fatalf("evicted without a complete in-use snapshot: removed=%v stat=%v", got.Removed, statErr)
			}
		})
	}
}

// TestEvictDefaultProbeMatchesResolvedPaths: lsof reports resolved paths, and
// the cache root may be reached through a symlink.
func TestEvictDefaultProbeMatchesResolvedPaths(t *testing.T) {
	target := t.TempDir()
	root := filepath.Join(t.TempDir(), "cache-link")
	if err := os.Symlink(target, root); err != nil {
		t.Fatal(err)
	}
	staleSourceEntry(t, root, "app-aaaaaaaaaaaa", 0)
	held := staleSourceEntry(t, root, "app-bbbbbbbbbbbb", 90*24*time.Hour)
	real, err := filepath.EvalSymlinks(held)
	if err != nil {
		t.Fatal(err)
	}
	fakeLsof(t, "p1\nfcwd\nn"+real+"/web\n", "exit 0")
	got, err := Evict(root, time.Now(), EvictPolicy{KeepPerSlug: 1}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(held); statErr != nil || len(got.Held) != 1 {
		t.Fatalf("evicted an entry a process is using: held=%v stat=%v", got.Held, statErr)
	}
}
