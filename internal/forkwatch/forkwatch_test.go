// Copyright (c) 2026 Reliant Labs
//go:build unix

package forkwatch

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// eagain is what os/exec returns when the kernel refuses the fork.
var eagain = &os.PathError{Op: "fork/exec", Path: "/usr/bin/true", Err: syscall.EAGAIN}

// script is a probe whose answer the test sets.
type script struct {
	mu    sync.Mutex
	err   error
	calls int
}

func (s *script) set(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

func (s *script) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *script) probe(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.err
}

func changedNow(w *Watchdog) bool {
	select {
	case <-w.Changed():
		return true
	default:
		return false
	}
}

// A machine whose starts are refused with EAGAIN Threshold times in a row is
// degraded, with the process-table code and a reason that names the error and
// the largest trees; one start that succeeds makes it healthy again. Each
// transition signals Changed once.
func TestDegradedAfterSustainedRefusalAndHealthyOnRecovery(t *testing.T) {
	p := &script{}
	w := New(Config{Probe: p.probe, Threshold: 3, Trees: func(n int) string {
		return "5210 tasks: `bash scripts/ci-gate.sh all` (agent 2ee014da)"
	}})
	ctx := context.Background()

	if h := w.Health(); h != nil {
		t.Fatalf("health before any probe = %v, want nil (not reported)", h)
	}

	w.Check(ctx)
	if got := w.Health().GetState(); got != reliantv1.MachineHealthState_MACHINE_HEALTH_STATE_OK {
		t.Fatalf("health after a start = %v, want OK", got)
	}
	if changedNow(w) {
		t.Fatal("Changed fired for a machine that was never degraded")
	}

	p.set(eagain)
	w.Check(ctx)
	w.Check(ctx)
	if got := w.Health().GetState(); got != reliantv1.MachineHealthState_MACHINE_HEALTH_STATE_OK {
		t.Fatalf("health after 2 refusals (threshold 3) = %v, want OK", got)
	}
	if changedNow(w) {
		t.Fatal("Changed fired below the threshold")
	}

	w.Check(ctx)
	h := w.Health()
	if h.GetState() != reliantv1.MachineHealthState_MACHINE_HEALTH_STATE_DEGRADED {
		t.Fatalf("health after 3 refusals = %v, want DEGRADED", h.GetState())
	}
	if h.GetCode() != CodeProcessTableFull {
		t.Errorf("code = %q, want %q", h.GetCode(), CodeProcessTableFull)
	}
	for _, want := range []string{"process table is full", "last 3 attempts", "resource temporarily unavailable", "ci-gate.sh all", "shell_kill"} {
		if !strings.Contains(h.GetReason(), want) {
			t.Errorf("reason %q does not contain %q", h.GetReason(), want)
		}
	}
	if !changedNow(w) {
		t.Fatal("Changed did not fire when the machine became degraded")
	}

	// Still refused: still degraded, the count in the reason grows, no
	// second signal.
	w.Check(ctx)
	if !strings.Contains(w.Health().GetReason(), "last 4 attempts") {
		t.Errorf("reason after a 4th refusal = %q, want it to count 4", w.Health().GetReason())
	}
	if changedNow(w) {
		t.Fatal("Changed fired again while the machine stayed degraded")
	}

	p.set(nil)
	w.Check(ctx)
	h = w.Health()
	if h.GetState() != reliantv1.MachineHealthState_MACHINE_HEALTH_STATE_OK || h.GetCode() != "" || h.GetReason() != "" {
		t.Fatalf("health after a start = %v, want a bare OK", h)
	}
	if !changedNow(w) {
		t.Fatal("Changed did not fire when the machine recovered")
	}

	// A new run of refusals starts the count again.
	p.set(eagain)
	w.Check(ctx)
	if got := w.Health().GetState(); got != reliantv1.MachineHealthState_MACHINE_HEALTH_STATE_OK {
		t.Fatalf("health after 1 refusal of a new run = %v, want OK", got)
	}
}

// A machine whose probes keep starting stays healthy and never signals.
func TestHealthyMachineStaysHealthy(t *testing.T) {
	p := &script{}
	w := New(Config{Probe: p.probe})
	for range 10 {
		w.Check(context.Background())
	}
	if got := w.Health().GetState(); got != reliantv1.MachineHealthState_MACHINE_HEALTH_STATE_OK {
		t.Fatalf("health = %v, want OK", got)
	}
	if changedNow(w) {
		t.Fatal("Changed fired for a healthy machine")
	}
}

// A probe failure that is not a refused fork (no binary, a cgroup that could
// not be made) says nothing about the process table: it neither degrades the
// machine nor breaks a run of refusals.
func TestInconclusiveProbesDoNotDegrade(t *testing.T) {
	p := &script{}
	w := New(Config{Probe: p.probe, Threshold: 2})
	ctx := context.Background()

	p.set(&os.PathError{Op: "fork/exec", Path: "/usr/bin/true", Err: syscall.ENOENT})
	for range 5 {
		w.Check(ctx)
	}
	if h := w.Health(); h != nil {
		t.Fatalf("health after only inconclusive probes = %v, want nil (not reported)", h)
	}

	p.set(eagain)
	w.Check(ctx)
	p.set(errors.New("cgroup: no such file or directory"))
	w.Check(ctx)
	p.set(eagain)
	w.Check(ctx)
	if got := w.Health().GetState(); got != reliantv1.MachineHealthState_MACHINE_HEALTH_STATE_DEGRADED {
		t.Fatalf("health after refused, inconclusive, refused (threshold 2) = %v, want DEGRADED", got)
	}
}

// Run probes at once and then every Interval, and signals the moment the
// threshold is crossed — on fake time, so no test waits on a real clock.
func TestRunProbesOnItsIntervalAndSignals(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &script{err: eagain}
		w := New(Config{Probe: p.probe, Interval: 30 * time.Second, Threshold: 3})
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { w.Run(ctx); close(done) }()

		synctest.Wait()
		if n := p.count(); n != 1 {
			t.Fatalf("probes at start = %d, want 1", n)
		}
		time.Sleep(30 * time.Second) // fake time: advances the bubble's clock only
		synctest.Wait()
		if changedNow(w) {
			t.Fatal("degraded after 2 refusals (threshold 3)")
		}
		time.Sleep(30 * time.Second)
		synctest.Wait()
		if n := p.count(); n != 3 {
			t.Fatalf("probes after 60s = %d, want 3", n)
		}
		if !changedNow(w) {
			t.Fatal("Changed did not fire on the 3rd refusal")
		}
		if got := w.Health().GetState(); got != reliantv1.MachineHealthState_MACHINE_HEALTH_STATE_DEGRADED {
			t.Fatalf("health = %v, want DEGRADED", got)
		}

		cancel()
		<-done
	})
}

// The default probe really starts `true` here, so a working machine reads
// healthy through the same path the daemon uses.
func TestDefaultProbeStartsAProcess(t *testing.T) {
	w := New(Config{})
	if !w.Enabled() {
		t.Skip("no `true` on PATH: the default probe is off here")
	}
	w.Check(t.Context())
	if got := w.Health().GetState(); got != reliantv1.MachineHealthState_MACHINE_HEALTH_STATE_OK {
		t.Fatalf("health = %v, want OK", got)
	}
}

// A nil watchdog (the daemon without one) reports nothing and never signals.
func TestNilWatchdog(t *testing.T) {
	var w *Watchdog
	w.Run(t.Context())
	w.Check(t.Context())
	if w.Health() != nil || w.Changed() != nil || w.Enabled() {
		t.Fatal("a nil watchdog reported something")
	}
}
