// Copyright (c) 2025 Reliant Labs
package heavyjob

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/hostslot"
)

// TestMain lets this test binary stand in for the reliant binary: run with
// WrapperArg first, it is the slot wrapper, exactly as cmd/reliant's main is.
func TestMain(m *testing.M) {
	Dispatch()
	os.Exit(m.Run())
}

// testSelf runs the wrapper as this test binary.
func testSelf(t *testing.T) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{exe, WrapperArg}
}

// oneSlotGate is a gate over a private one-slot pool.
func oneSlotGate(t *testing.T) (*Gate, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("drives sh")
	}
	dir := t.TempDir()
	g, err := New(Config{Env: []string{"FORGE_SLOTS_DIR=" + dir, "FORGE_HEAVY_SLOTS=1"}, Self: testSelf(t)})
	if err != nil {
		t.Fatal(err)
	}
	return g, dir
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type started struct {
	cmd    *exec.Cmd
	out    *syncBuf
	done   chan struct{}
	err    error
	heavy  bool
	cancel context.CancelFunc
}

// start runs line through g the way a daemon exec path does: build the shell
// command, let the gate rewrite it, and terminate (not kill) on cancel.
func start(t *testing.T, g *Gate, line string, declared bool) *started {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	c := exec.CommandContext(ctx, "sh", "-c", line)
	c.Cancel = func() error { return c.Process.Signal(syscall.SIGTERM) }
	c.WaitDelay = 5 * time.Second
	// The test's own environment may carry a slot from an outer lane.
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, hostslot.EnvHeld+"=") {
			c.Env = append(c.Env, kv)
		}
	}
	s := &started{cmd: c, out: &syncBuf{}, done: make(chan struct{}), cancel: cancel}
	c.Stdout, c.Stderr = s.out, s.out
	s.heavy = g.Prepare(c, Job{Command: line, Declared: declared})
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { s.err = c.Wait(); close(s.done) }()
	t.Cleanup(func() {
		cancel()
		<-s.done
	})
	return s
}

func (s *started) wait(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(within):
		t.Fatalf("command still running after %s; output:\n%s", within, s.out)
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// holder starts a heavy command that holds the only slot until release() is
// called.
func holder(t *testing.T, g *Gate) (release func(), s *started) {
	t.Helper()
	flag := filepath.Join(t.TempDir(), "release")
	s = start(t, g, fmt.Sprintf("echo holding; while [ ! -f %s ]; do sleep 0.02; done", flag), true)
	eventually(t, "the holder to start", func() bool { return strings.Contains(s.out.String(), "holding") })
	return func() { _ = os.WriteFile(flag, nil, 0o644) }, s
}

func TestSecondHeavyCommandWaitsForTheSlot(t *testing.T) {
	g, _ := oneSlotGate(t)
	release, first := holder(t, g)

	// The output (RAN-2) differs from the command line, which the waiting
	// lines quote.
	second := start(t, g, "echo RAN-$((1+1))", true)
	eventually(t, "the second command to report it is queued", func() bool { return WaitedFor(second.out.String()) })
	out := second.out.String()
	if strings.Contains(out, "RAN-2") {
		t.Fatalf("the second heavy command ran while the only slot was held:\n%s", out)
	}
	// The agent is told who holds the slot.
	if !strings.Contains(out, "waiting for a heavy slot") || !strings.Contains(out, "1 of 1 held") {
		t.Fatalf("waiting output does not name the holder:\n%s", out)
	}

	release()
	first.wait(t, 10*time.Second)
	second.wait(t, 10*time.Second)
	if second.err != nil {
		t.Fatalf("second command failed: %v\n%s", second.err, second.out)
	}
	out = second.out.String()
	if !strings.Contains(out, "RAN-2") || !strings.Contains(out, "▶ reliant: running") {
		t.Fatalf("second command output:\n%s", out)
	}
}

func TestCancelWhileWaitingLeavesNoTicket(t *testing.T) {
	g, dir := oneSlotGate(t)
	release, _ := holder(t, g)

	waiting := start(t, g, "echo RAN-$((1+1))", true)
	eventually(t, "the command to queue", func() bool { return WaitedFor(waiting.out.String()) })
	waiting.cancel()
	waiting.wait(t, 5*time.Second)
	out := waiting.out.String()
	if strings.Contains(out, "RAN-2") || !strings.Contains(out, "never started") {
		t.Fatalf("cancelled-while-waiting output:\n%s", out)
	}
	if code := waiting.cmd.ProcessState.ExitCode(); code != 128+int(syscall.SIGTERM) {
		t.Fatalf("exit code %d, want %d", code, 128+int(syscall.SIGTERM))
	}
	rep, err := hostslot.Status(hostslot.Heavy, hostslot.Options{Dir: dir, Slots: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Waiting) != 0 {
		t.Fatalf("a cancelled waiter is still queued: %+v", rep.Waiting)
	}

	// The slot frees for the next command as usual.
	release()
	next := start(t, g, "echo next ran", true)
	next.wait(t, 10*time.Second)
	if !strings.Contains(next.out.String(), "next ran") {
		t.Fatalf("next command output:\n%s", next.out)
	}
}

func TestNestedHeavyCommandRunsInsideItsParentsSlot(t *testing.T) {
	g, dir := oneSlotGate(t)
	self := testSelf(t)
	// The heavy command itself runs a heavy command through the wrapper, as
	// `make test` does through `forge slot run`. With one slot, a nested
	// waiter would wait on its own parent forever.
	inner := fmt.Sprintf("%s %s --dir %s --slots 1 --label inner -- sh -c 'echo inner ran'", self[0], self[1], dir)
	outer := start(t, g, inner, true)
	outer.wait(t, 10*time.Second)
	if outer.err != nil {
		t.Fatalf("nested command failed: %v\n%s", outer.err, outer.out)
	}
	out := outer.out.String()
	if !strings.Contains(out, "inner ran") || WaitedFor(out) {
		t.Fatalf("nested command waited or did not run:\n%s", out)
	}
}

func TestLightCommandNeverWaits(t *testing.T) {
	g, _ := oneSlotGate(t)
	release, _ := holder(t, g)
	defer release()

	light := start(t, g, "echo light ran", false)
	if light.heavy {
		t.Fatal("a light command was routed through the slot wrapper")
	}
	light.wait(t, 5*time.Second)
	if !strings.Contains(light.out.String(), "light ran") {
		t.Fatalf("light command output:\n%s", light.out)
	}
}

func TestWrapperPropagatesExitStatus(t *testing.T) {
	g, _ := oneSlotGate(t)
	s := start(t, g, "exit 3", true)
	s.wait(t, 10*time.Second)
	if code := s.cmd.ProcessState.ExitCode(); code != 3 {
		t.Fatalf("exit code %d, want 3; output:\n%s", code, s.out)
	}
}
