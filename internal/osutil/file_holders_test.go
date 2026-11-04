// Copyright (c) 2025 Reliant Labs
package osutil

import (
	"context"
	"os"
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
