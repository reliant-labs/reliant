// Copyright (c) 2025 Reliant Labs
//go:build !windows

package osutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// These tests pin how one lsof run becomes an answer, against scripted
// results. The real lsof's speed depends on how many processes the host is
// running, so a test that asserts a verdict through it is a test of the
// machine's load. TestFileHolders_RealLsof is the one that runs it for real.

// existingFile makes a file to ask about: the probe refuses to answer for a
// path that is not there.
func existingFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "probed")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// lsofReturns is a probe run that finishes immediately with result.
func lsofReturns(result lsofResult) func(context.Context, string) lsofResult {
	return func(context.Context, string) lsofResult { return result }
}

func TestProbeFileHolders_Verdicts(t *testing.T) {
	tests := []struct {
		name   string
		result lsofResult
		want   FileHoldState
		why    string
	}{
		{
			name:   "a pid on stdout is held",
			result: lsofResult{stdout: "4242\n", exitCode: 0},
			want:   FileHoldHeld,
			why:    "lsof named a live process with the file open",
		},
		{
			name:   "a pid is held even alongside an error",
			result: lsofResult{stdout: "4242\n", stderr: "lsof: something else failed\n", exitCode: 1},
			want:   FileHoldHeld,
			why:    "a named holder is evidence whatever else went wrong, and Held only ever keeps a file",
		},
		{
			name:   "exit 1 with nothing printed is not held",
			result: lsofResult{exitCode: 1},
			want:   FileHoldNotHeld,
			why:    "lsof's 'looked, found nobody': the recoverable case",
		},
		{
			name:   "exit 1 with stderr is unknown",
			result: lsofResult{stderr: "lsof: status error on /x: No such file or directory\n", exitCode: 1},
			want:   FileHoldUnknown,
			why:    "lsof also exits 1 when its look-up failed, and stderr is the only tell",
		},
		{
			name:   "any other exit status is unknown",
			result: lsofResult{exitCode: 2},
			want:   FileHoldUnknown,
			why:    "not lsof's 'found nobody'",
		},
		{
			name:   "lsof that never ran is unknown",
			result: lsofResult{exitCode: -1},
			want:   FileHoldUnknown,
			why:    "a missing binary or a killed probe answered nothing",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := probeFileHolders(context.Background(), existingFile(t), time.Minute, lsofReturns(tc.result))
			if got != tc.want {
				t.Fatalf("probeFileHolders = %v, want %v (%s)", got, tc.want, tc.why)
			}
		})
	}
}

// TestProbeFileHolders_TimeoutIsUnknown is the case that made recovery inert
// on a loaded host: lsof outlived the timeout. The scripted run reports a
// clean "found nobody" AFTER the deadline, so it is the deadline itself — not
// the exit status — that must produce Unknown.
func TestProbeFileHolders_TimeoutIsUnknown(t *testing.T) {
	slowLsof := func(ctx context.Context, _ string) lsofResult {
		<-ctx.Done()
		return lsofResult{exitCode: 1}
	}
	if got := probeFileHolders(context.Background(), existingFile(t), 10*time.Millisecond, slowLsof); got != FileHoldUnknown {
		t.Fatalf("probeFileHolders after a timeout = %v, want Unknown: an unfinished probe is not evidence nobody holds the file", got)
	}
}

// TestProbeFileHolders_FileGoneIsUnknown: lsof answers "found nobody" for a
// file that does not exist, so absence must never read as NotHeld — whether
// the file was missing up front or vanished while lsof ran.
func TestProbeFileHolders_FileGoneIsUnknown(t *testing.T) {
	t.Run("missing before the probe", func(t *testing.T) {
		ran := false
		run := func(context.Context, string) lsofResult {
			ran = true
			return lsofResult{exitCode: 1}
		}
		if got := probeFileHolders(context.Background(), filepath.Join(t.TempDir(), "absent"), time.Minute, run); got != FileHoldUnknown {
			t.Fatalf("probeFileHolders on a missing file = %v, want Unknown", got)
		}
		if ran {
			t.Fatal("lsof was run for a file that does not exist")
		}
	})

	t.Run("deleted while lsof ran", func(t *testing.T) {
		path := existingFile(t)
		run := func(context.Context, string) lsofResult {
			if err := os.Remove(path); err != nil {
				t.Error(err)
			}
			return lsofResult{exitCode: 1}
		}
		if got := probeFileHolders(context.Background(), path, time.Minute, run); got != FileHoldUnknown {
			t.Fatalf("probeFileHolders on a file deleted mid-probe = %v, want Unknown", got)
		}
	})
}
