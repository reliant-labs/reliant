// Copyright (c) 2025 Reliant Labs
package osutil

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestFileHolders covers the answers that need no probe at all. The
// tri-state exists because a boolean would force these cases to read as "not
// held", and a wrong "not held" is what deletes a live lock.
func TestFileHolders(t *testing.T) {
	tests := []struct {
		name string
		path func(t *testing.T) string
		want FileHoldState
		why  string
	}{
		{
			name: "missing file is Unknown, not NotHeld",
			path: func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent") },
			want: FileHoldUnknown,
			why:  "absence is not evidence about holders; lsof reports it as exit 1 like 'no holders'",
		},
		{
			name: "empty path is Unknown",
			path: func(t *testing.T) string { return "" },
			want: FileHoldUnknown,
			why:  "no question was asked",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FileHolders(context.Background(), tc.path(t)); got != tc.want {
				t.Fatalf("FileHolders = %v, want %v (%s)", got, tc.want, tc.why)
			}
		})
	}
}

// TestFileHolders_RealLsof is the one test that runs the real lsof, with the
// production timeout. The verdict logic is pinned against scripted results in
// file_holders_unix_test.go; what only the real tool can show is that the
// flags are accepted, that "nobody holds it" really is exit 1 with a clean
// stderr, and that a descriptor held by ANOTHER process — the production case,
// since git is always a separate process — is reported.
//
// It fails if lsof cannot answer within holderProbeTimeout on this host. That
// is deliberate: it is the signal that the timeout no longer fits the machines
// this runs on, which is exactly what made recovery silently inert before.
func TestFileHolders_RealLsof(t *testing.T) {
	if testing.Short() {
		t.Skip("runs real lsof, which can take up to holderProbeTimeout (10s) on a loaded host; runs in make test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("no holder probe on Windows; fileHolders always reports Unknown by design")
	}
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}

	t.Run("file with no holder is NotHeld", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "unheld")
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := FileHolders(context.Background(), path); got != FileHoldNotHeld {
			t.Fatalf("FileHolders = %v, want NotHeld for a file nothing has open", got)
		}
	})

	t.Run("file held by another process is Held", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "crossproc")
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}

		// A child that opens the file on fd 3, says so, then waits. Reading
		// its "ready" line is what guarantees the descriptor is open before
		// the single probe below, so no polling against lsof is needed.
		cmd := exec.Command("sh", "-c", `exec 3>>"$1"; echo ready; exec sleep 60`, "sh", path)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
		if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
			t.Fatalf("holder process did not report ready: %q, %v", line, err)
		}

		if got := FileHolders(context.Background(), path); got != FileHoldHeld {
			t.Fatalf("FileHolders = %v, want Held for a file another process has open", got)
		}
	})
}

// TestFileHolders_CancelledContextIsUnknown: a probe that could not run must
// never come back as NotHeld.
func TestFileHolders_CancelledContextIsUnknown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no holder probe on Windows")
	}
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if got := FileHolders(ctx, path); got == FileHoldNotHeld {
		t.Fatalf("FileHolders with a cancelled context = NotHeld; an unanswerable probe must not license removal")
	}
}

func TestFileHoldState_String(t *testing.T) {
	for state, want := range map[FileHoldState]string{
		FileHoldHeld:    "held",
		FileHoldNotHeld: "not-held",
		FileHoldUnknown: "unknown",
	} {
		if got := state.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
}
