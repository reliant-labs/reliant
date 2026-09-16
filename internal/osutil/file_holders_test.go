// Copyright (c) 2025 Reliant Labs
package osutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestFileHolders covers the probe's answers including, deliberately, the
// cases where the honest answer is "I cannot tell". The tri-state exists
// because a boolean would force those cases to read as "not held", and a
// wrong "not held" is what deletes a live lock.
func TestFileHolders(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no holder probe on Windows; fileHolders always reports Unknown by design")
	}
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}

	tests := []struct {
		name string
		// setup returns the path to probe and any handle to keep open.
		setup func(t *testing.T) (path string, keepOpen *os.File)
		want  FileHoldState
		why   string
	}{
		{
			name: "file held open by this process",
			setup: func(t *testing.T) (string, *os.File) {
				path := filepath.Join(t.TempDir(), "held")
				f, err := os.Create(path)
				if err != nil {
					t.Fatal(err)
				}
				return path, f
			},
			want: FileHoldHeld,
			why:  "an open descriptor is the signal the whole design rests on",
		},
		{
			name: "file exists with no holder",
			setup: func(t *testing.T) (string, *os.File) {
				path := filepath.Join(t.TempDir(), "unheld")
				if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
				return path, nil
			},
			want: FileHoldNotHeld,
			why:  "the recoverable case: a file nothing has open",
		},
		{
			name: "missing file is Unknown, not NotHeld",
			setup: func(t *testing.T) (string, *os.File) {
				return filepath.Join(t.TempDir(), "absent"), nil
			},
			want: FileHoldUnknown,
			why:  "absence is not evidence about holders; lsof reports it as exit 1 like 'no holders'",
		},
		{
			name: "empty path is Unknown",
			setup: func(t *testing.T) (string, *os.File) {
				return "", nil
			},
			want: FileHoldUnknown,
			why:  "no question was asked",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path, keepOpen := tc.setup(t)
			if keepOpen != nil {
				defer keepOpen.Close()
			}
			if got := FileHolders(context.Background(), path); got != tc.want {
				t.Fatalf("FileHolders = %v, want %v (%s)", got, tc.want, tc.why)
			}
		})
	}
}

// TestFileHolders_HeldByAnotherProcess uses a real second process, since the
// production case is always cross-process and a same-process descriptor could
// in principle be reported differently.
func TestFileHolders_HeldByAnotherProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no holder probe on Windows")
	}
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}

	path := filepath.Join(t.TempDir(), "crossproc")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	// A child that holds the file open on fd 3 and waits.
	cmd := exec.Command("bash", "-c", "exec 3>>"+path+"; sleep 10")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	// Poll: the child needs a moment to open the descriptor.
	var got FileHoldState
	for i := 0; i < 100; i++ {
		got = FileHolders(context.Background(), path)
		if got == FileHoldHeld {
			return
		}
		sleepShort()
	}
	t.Fatalf("FileHolders = %v, want Held for a file another process has open", got)
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

// sleepShort is the poll interval for tests that wait on another process to
// open a descriptor.
func sleepShort() { time.Sleep(20 * time.Millisecond) }
