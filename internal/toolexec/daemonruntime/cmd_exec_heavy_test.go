// Copyright (c) 2025 Reliant Labs
//go:build !windows

package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/hostslot"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/heavyjob"
)

// exec.run queues heavy commands behind the gate the runtime sets, exactly as
// the LocalClient path does (internal/daemon/local_exec_heavy_test.go).
//
// The queued command's timeout is fired by the test, once the command is
// provably waiting in the slot queue, through the exec wall clock — not by a
// short TimeoutMs. A fixed budget had to cover starting this (-race) test
// binary as the slot wrapper as well as the wait, and on a loaded machine the
// start alone used it up: the command timed out before it ever queued, with
// nothing on stderr.
func TestHandleExecRun_HeavyCommandQueuesForTheSlot(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	slots := t.TempDir()
	g, err := heavyjob.New(heavyjob.Config{
		Env:  []string{"FORGE_SLOTS_DIR=" + slots, "FORGE_HEAVY_SLOTS=1"},
		Self: []string{exe, heavyjob.WrapperArg},
	})
	if err != nil {
		t.Fatal(err)
	}
	SetHeavyGate(g)
	t.Cleanup(func() { SetHeavyGate(nil) })
	t.Setenv(hostslot.EnvHeld, "")

	// The test owns the wall clock the command's deadline is checked on.
	var skew atomic.Int64
	prevClock, prevInterval := daemon.ExecWallClock, daemon.ExecWallClockCheckInterval
	daemon.ExecWallClock = func() time.Time { return time.Now().Round(0).Add(time.Duration(skew.Load())) }
	daemon.ExecWallClockCheckInterval = 20 * time.Millisecond
	t.Cleanup(func() {
		daemon.ExecWallClock, daemon.ExecWallClockCheckInterval = prevClock, prevInterval
	})

	// Another holder — a forge lane, say — has the only slot.
	slotOpts := hostslot.Options{Dir: slots, Slots: 1, Label: "forge generate"}
	held, err := hostslot.Acquire(context.Background(), hostslot.Heavy, slotOpts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Release() })

	// A timeout no machine load reaches: only the test ends the wait.
	const queuedTimeout = 10 * time.Minute
	type result struct {
		res daemon.CommandResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		payload, _ := json.Marshal(daemon.RunCommandRequest{
			Command: "echo RAN-$((1+1))", Heavy: true, TimeoutMs: int(queuedTimeout.Milliseconds()),
		})
		raw, err := handleExecRun(context.Background(), payload)
		var r result
		if r.err = err; err == nil {
			r.err = json.Unmarshal(raw, &r.res)
		}
		done <- r
	}()

	// Wait for the wrapper's ticket in the queue. Once it is there the wrapper
	// prints the line naming the holder before it next looks at its
	// cancellation, so the timeout below cannot beat that line.
	for queued := false; !queued; {
		select {
		case r := <-done:
			t.Fatalf("the heavy command returned before it queued for the held slot: %+v (err %v)", r.res, r.err)
		case <-time.After(20 * time.Millisecond):
		}
		rep, err := hostslot.Status(hostslot.Heavy, slotOpts)
		if err != nil {
			t.Fatal(err)
		}
		queued = len(rep.Waiting) > 0
	}

	// Its timeout elapses now.
	skew.Store(int64(2 * queuedTimeout))
	r := <-done
	if r.err != nil {
		t.Fatalf("handleExecRun: %v", r.err)
	}
	res := r.res
	if !res.TimedOut || strings.Contains(res.Stdout, "RAN-2") || !strings.Contains(res.Stderr, "forge generate") {
		t.Fatalf("want a queued command naming its holder to time out unstarted, got %+v", res)
	}
	if !strings.Contains(res.Stderr, heavyjob.TimeoutNote) {
		t.Errorf("a heavy command that timed out waiting must say its timeout counted the wait; stderr = %q", res.Stderr)
	}

	_ = held.Release()
	res = runExec(t, daemon.RunCommandRequest{Command: "echo RAN-$((1+1))", Heavy: true, TimeoutMs: 30_000})
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "RAN-2") || heavyjob.WaitedFor(res.Stderr) {
		t.Fatalf("with the slot free the command must run at once, got %+v", res)
	}
}
