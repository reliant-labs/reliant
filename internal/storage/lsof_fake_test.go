package storage

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fakeLsof puts an executable named `lsof` first on PATH. It prints records
// (lsof -F output, one field per line) and then runs tail, a shell statement
// that decides how the process ends: "exit 0", "exit 1", "kill -9 $$" for an
// lsof killed mid-listing, "sleep 30" for one that hangs.
//
// The real lsof costs ~6s on a busy Mac and reports whatever the machine
// happens to have open, so a test that relied on it would be both slow and
// unable to say what it proved.
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

// resolved is path with every symlink resolved, which is the form lsof
// reports: on macOS t.TempDir() is under /var/folders, and lsof prints
// /private/var/folders.
func resolved(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// unrelatedOpenFile is a well-formed lsof record for a path no test owns, so a
// fake snapshot is non-empty without holding anything the test cares about.
const unrelatedOpenFile = "p1\nf3\nn/nonexistent/forge-storage-test/unrelated\n"
