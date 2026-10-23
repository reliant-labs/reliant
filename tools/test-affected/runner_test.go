package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The first two steps must overlap. A deadline is a failure bound, not a sleep
// or a benchmark threshold; the test completes as soon as the channels close.
func TestRunnerOverlapsIndependentLanesAndKeepsFailures(t *testing.T) {
	var out bytes.Buffer
	started := make(chan string, 3)
	release := make(chan struct{})
	var calls atomic.Int32
	var active atomic.Int32
	var exceeded atomic.Bool
	var exclusiveOverlap atomic.Bool
	r := &runner{root: t.TempDir(), out: &out, errOut: &out, jobs: 2,
		execute: func(ctx context.Context, s step, stdout, stderr io.Writer) error {
			calls.Add(1)
			n := active.Add(1)
			if n > 2 {
				exceeded.Store(true)
			}
			if s.exclusive && n != 1 {
				exclusiveOverlap.Store(true)
			}
			defer active.Add(-1)
			started <- s.name
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			io.WriteString(stdout, s.name+" output\n")
			if s.name == "electron" {
				return errors.New("fixture failure")
			}
			return nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- r.run(ctx, failSafe("", "", "fixture"), laneSet("go,web,electron")) }()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("independent lanes did not overlap")
		}
	}
	close(release)
	if code := <-done; code != 1 {
		t.Fatalf("exit=%d, want failure", code)
	}
	if calls.Load() != 3 {
		t.Fatalf("ran %d lanes, want all three despite failure", calls.Load())
	}
	if exceeded.Load() {
		t.Fatal("runner exceeded -jobs=2")
	}
	if exclusiveOverlap.Load() {
		t.Fatal("Electron overlapped repository readers")
	}
	if !strings.Contains(out.String(), "fixture failure") || !strings.Contains(out.String(), "web (full) output") {
		t.Fatalf("lost lane output: %s", &out)
	}
}

func TestRunnerGenerationFailureBlocksTests(t *testing.T) {
	var out bytes.Buffer
	var calls []string
	r := &runner{root: t.TempDir(), out: &out, errOut: &out, jobs: 2,
		execute: func(_ context.Context, s step, _, _ io.Writer) error {
			calls = append(calls, s.name)
			return errors.New("generation failed")
		},
	}
	if code := r.run(context.Background(), failSafe("", "", "fixture"), laneSet("forge-generate,go,web,electron")); code != 1 {
		t.Fatalf("exit=%d, want failure", code)
	}
	if len(calls) != 1 || calls[0] != "forge generate is a no-op" {
		t.Fatalf("ran tests on stale output: %v", calls)
	}
}
