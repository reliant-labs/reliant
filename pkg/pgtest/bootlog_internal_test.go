package pgtest

import (
	"os"
	"path/filepath"
	"testing"
)

// The server's transcript lands in its runtime directory, appended across
// the library's flushes (end of Start, then Stop).
func TestBootLogAppendsToTheRuntimeDirectory(t *testing.T) {
	dir := t.TempDir()
	l := bootLog{path: bootLogPath(dir)}
	for _, s := range []string{"initdb ... ok\n", "LOG:  could not bind IPv6 address \"::1\"\n"} {
		if n, err := l.Write([]byte(s)); err != nil || n != len(s) {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, bootLogFile))
	if err != nil {
		t.Fatal(err)
	}
	if want := "initdb ... ok\nLOG:  could not bind IPv6 address \"::1\"\n"; string(got) != want {
		t.Fatalf("boot log = %q, want %q", got, want)
	}
}

// A transcript with nowhere to go is dropped, never an error: the library
// fails Start or Stop on a Logger error.
func TestBootLogNeverFails(t *testing.T) {
	l := bootLog{path: filepath.Join(t.TempDir(), "gone", bootLogFile)}
	if n, err := l.Write([]byte("x")); err != nil || n != 1 {
		t.Fatalf("Write into a missing directory = %d, %v; want 1, nil", n, err)
	}
}
