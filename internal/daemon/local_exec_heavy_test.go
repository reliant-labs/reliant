// Copyright (c) 2025 Reliant Labs
//go:build !windows

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/hostslot"
	"github.com/reliant-labs/reliant/internal/heavyjob"
)

// These tests drive the daemon's own exec paths (RunCommand, StartBackground)
// against a private one-slot pool: the property that matters is that the
// agents' commands, not just forge's lanes, are held to the machine's heavy
// slots.

// heavyMarker is the pattern these tests' gate treats as heavy, so a command
// is classified by its words exactly as `go test` is in production, without
// running anything slow.
const heavyMarker = "true heavy-marker"

func oneSlotClient(t *testing.T) (*LocalClient, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	slots := t.TempDir()
	g, err := heavyjob.New(heavyjob.Config{
		Commands: []string{heavyMarker},
		Env:      []string{"FORGE_SLOTS_DIR=" + slots, "FORGE_HEAVY_SLOTS=1"},
		Self:     []string{exe, heavyjob.WrapperArg},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The test's own environment may carry a slot from an outer lane.
	t.Setenv(hostslot.EnvHeld, "")
	return NewLocalClient().WithHeavyGate(g), slots
}

// holderTimeout bounds the slot holder. It only has to outlive the test (the
// holder is released through release() or cleanup), and it is far longer than
// any wall-clock skew a test applies to time out a queued command, so that
// skew never times the holder out and frees the slot.
const holderTimeout = 24 * time.Hour

// holdSlot runs a heavy command that holds the only slot until release().
func holdSlot(t *testing.T, c *LocalClient) (release func()) {
	t.Helper()
	dir := t.TempDir()
	flag, started := filepath.Join(dir, "release"), filepath.Join(dir, "started")
	done := make(chan struct{})
	go func() {
		defer close(done)
		res, err := c.RunCommand(context.Background(), &RunCommandRequest{
			Command:   ": > " + started + "; while [ ! -f " + flag + " ]; do sleep 0.02; done",
			TimeoutMs: int(holderTimeout.Milliseconds()),
			Heavy:     true,
		})
		if err != nil || res.ExitCode != 0 {
			t.Errorf("holder: err %v, result %+v", err, res)
		}
	}()
	waitFor(t, "the holder to start", func() bool { _, err := os.Stat(started); return err == nil })
	var once bool
	release = func() {
		if !once {
			once = true
			_ = os.WriteFile(flag, nil, 0o644)
			<-done
		}
	}
	t.Cleanup(release)
	return release
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waiters(t *testing.T, slots string) int {
	t.Helper()
	rep, err := hostslot.Status(hostslot.Heavy, hostslot.Options{Dir: slots, Slots: 1})
	if err != nil {
		t.Fatal(err)
	}
	return len(rep.Waiting)
}

func TestRunCommand_SecondHeavyCommandWaitsForTheSlot(t *testing.T) {
	c, slots := oneSlotClient(t)
	release := holdSlot(t, c)

	type result struct {
		res *CommandResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		// Recognized by its words, not declared.
		res, err := c.RunCommand(context.Background(), &RunCommandRequest{
			Command:   heavyMarker + " && echo RAN-$((1+1))",
			TimeoutMs: 60_000,
		})
		done <- result{res, err}
	}()

	waitFor(t, "the second heavy command to queue", func() bool { return waiters(t, slots) == 1 })
	select {
	case r := <-done:
		t.Fatalf("the second heavy command finished while the only slot was held: %+v", r.res)
	case <-time.After(200 * time.Millisecond):
	}

	release()
	r := <-done
	if r.err != nil || r.res.ExitCode != 0 {
		t.Fatalf("second command: err %v, result %+v", r.err, r.res)
	}
	if !strings.Contains(r.res.Stdout, "RAN-2") {
		t.Fatalf("second command stdout %q", r.res.Stdout)
	}
	// The agent reads why its command took long, and who it waited for.
	if !heavyjob.WaitedFor(r.res.Stderr) || !strings.Contains(r.res.Stderr, "1 of 1 held") {
		t.Fatalf("second command stderr does not show the wait:\n%s", r.res.Stderr)
	}
}

// The queued command's timeout is fired by the test, once the command is
// provably waiting in the slot queue, through the exec wall clock — not by a
// short TimeoutMs. A fixed budget had to cover starting this (-race) test
// binary as the slot wrapper as well as the wait, and on a loaded machine the
// start alone used it up: the command timed out before the wrapper queued or
// could explain itself.
func TestRunCommand_TimeoutWhileQueuedSaysSo(t *testing.T) {
	c, slots := oneSlotClient(t)

	// The test owns the wall clock every command's deadline is checked on.
	// The skew below stays far inside the holder's timeout.
	var skew atomic.Int64
	prevClock, prevInterval := ExecWallClock, ExecWallClockCheckInterval
	ExecWallClock = func() time.Time { return time.Now().Round(0).Add(time.Duration(skew.Load())) }
	ExecWallClockCheckInterval = 20 * time.Millisecond
	t.Cleanup(func() { ExecWallClock, ExecWallClockCheckInterval = prevClock, prevInterval })
	holdSlot(t, c)

	// A timeout no machine load reaches: only the test ends the wait.
	const queuedTimeout = 10 * time.Minute
	type result struct {
		res *CommandResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := c.RunCommand(context.Background(), &RunCommandRequest{
			Command:   "echo RAN-$((1+1))",
			TimeoutMs: int(queuedTimeout.Milliseconds()),
			Heavy:     true,
		})
		done <- result{res, err}
	}()

	// Once the wrapper's ticket is in the queue its signal handler is
	// installed and it prints its waiting lines before it next looks at its
	// cancellation, so the timeout below cannot beat its explanation.
	for queued := false; !queued; {
		select {
		case r := <-done:
			t.Fatalf("the heavy command returned before it queued for the held slot: %+v (err %v)", r.res, r.err)
		case <-time.After(20 * time.Millisecond):
		}
		queued = waiters(t, slots) == 1
	}

	// Its timeout elapses now; the holder's does not.
	skew.Store(int64(queuedTimeout + time.Minute))
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	res := r.res
	if !res.TimedOut || strings.Contains(res.Stdout, "RAN-2") {
		t.Fatalf("want a timeout before the command ran, got %+v", res)
	}
	if !strings.Contains(res.Stderr, "never started") || !strings.Contains(res.Stderr, heavyjob.TimeoutNote) {
		t.Fatalf("stderr does not explain the queued timeout:\n%s", res.Stderr)
	}
}

func TestRunCommand_LightCommandNeverWaits(t *testing.T) {
	c, _ := oneSlotClient(t)
	holdSlot(t, c)

	start := time.Now()
	res, err := c.RunCommand(context.Background(), &RunCommandRequest{Command: "echo light", TimeoutMs: 10_000})
	if err != nil || res.ExitCode != 0 || strings.TrimSpace(res.Stdout) != "light" {
		t.Fatalf("light command: err %v, result %+v", err, res)
	}
	if heavyjob.WaitedFor(res.Stderr) || time.Since(start) > 5*time.Second {
		t.Fatalf("a light command waited behind the heavy slot (%s):\n%s", time.Since(start), res.Stderr)
	}
}

func TestStartBackground_HeavyCommandQueuesWithoutBlocking(t *testing.T) {
	c, slots := oneSlotClient(t)
	release := holdSlot(t, c)

	start := time.Now()
	id, err := c.StartBackground(context.Background(), &RunCommandRequest{
		Command: heavyMarker + " && echo RAN-$((1+1))",
	})
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("starting a queued background command blocked for %s", took)
	}
	output := func() string {
		out, err := c.GetProcessOutput(context.Background(), id, nil)
		if err != nil {
			t.Fatal(err)
		}
		return out.Output
	}
	waitFor(t, "the background command to queue", func() bool { return waiters(t, slots) == 1 && heavyjob.WaitedFor(output()) })
	if strings.Contains(output(), "RAN-2") {
		t.Fatalf("the background command ran while the only slot was held:\n%s", output())
	}

	release()
	waitFor(t, "the background command to run", func() bool { return strings.Contains(output(), "RAN-2") })
}

// refuser is an Admission that refuses every heavy command, as the disk guard
// does on a nearly full disk.
type refuser struct{ why string }

func (r *refuser) HeavyRefusal() string { return r.why }

func TestRunCommand_RefusedHeavyCommandNeverStartsAndSaysWhy(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	adm := &refuser{why: `Refused: this machine's "home" volume is 97% full.`}
	g, err := heavyjob.New(heavyjob.Config{
		Env:       []string{"FORGE_SLOTS_DIR=" + t.TempDir(), "FORGE_HEAVY_SLOTS=1"},
		Self:      []string{exe, heavyjob.WrapperArg},
		Admission: adm,
	})
	if err != nil {
		t.Fatal(err)
	}
	c := NewLocalClient().WithHeavyGate(g)
	marker := filepath.Join(t.TempDir(), "ran")

	res, err := c.RunCommand(context.Background(), &RunCommandRequest{Command: "touch " + marker, Heavy: true, TimeoutMs: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != heavyjob.RefusedExitCode || !strings.Contains(res.Stderr, "97% full") || !strings.Contains(res.Stderr, "was not started") {
		t.Fatalf("refused command result: %+v", res)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("a refused heavy command ran")
	}

	if _, err := c.StartBackground(context.Background(), &RunCommandRequest{Command: "touch " + marker, Heavy: true}); err == nil || !strings.Contains(err.Error(), "97% full") {
		t.Fatalf("refused background command: err %v", err)
	}

	// Light commands are never refused.
	res, err = c.RunCommand(context.Background(), &RunCommandRequest{Command: "touch " + marker, TimeoutMs: 10_000})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("light command: err %v, result %+v", err, res)
	}

	// Space returned: the same heavy command runs.
	adm.why = ""
	res, err = c.RunCommand(context.Background(), &RunCommandRequest{Command: "echo RAN-$((1+1))", Heavy: true, TimeoutMs: 10_000})
	if err != nil || res.ExitCode != 0 || !strings.Contains(res.Stdout, "RAN-2") {
		t.Fatalf("heavy command after recovery: err %v, result %+v", err, res)
	}
}
