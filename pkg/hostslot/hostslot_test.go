package hostslot

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Every test here drives the REAL lock path: slot files in a t.TempDir(),
// flock'd through separate open file descriptions. flock belongs to the open
// file description, so two goroutines contend exactly as two processes do;
// the properties that need a second process (crash release, an ancestor's
// slot) re-exec this test binary as TestSlotHelperProcess.

func testOpts(t *testing.T, slots int) Options {
	t.Helper()
	return Options{Dir: t.TempDir(), Slots: slots, Poll: 5 * time.Millisecond, Env: []string{}}
}

func acquire(t *testing.T, opt Options) *Slot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := Acquire(ctx, Heavy, opt)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	return s
}

func TestAcquire_NeverMoreThanNHolders(t *testing.T) {
	opt := testOpts(t, 3)
	var active, peak, done atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			s, err := Acquire(ctx, Heavy, opt)
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			n := active.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(15 * time.Millisecond)
			active.Add(-1)
			if err := s.Release(); err != nil {
				t.Errorf("Release: %v", err)
			}
			done.Add(1)
		}()
	}
	wg.Wait()
	if got := peak.Load(); got > 3 || got < 1 {
		t.Fatalf("peak concurrent holders = %d, want at most the 3 slots", got)
	}
	if done.Load() != 12 {
		t.Fatalf("only %d of 12 acquisitions completed", done.Load())
	}
}

func TestAcquire_WaitsAndHonorsContext(t *testing.T) {
	opt := testOpts(t, 1)
	held := acquire(t, opt)

	var notice bytes.Buffer
	waiter := opt
	waiter.Out = &notice
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := Acquire(ctx, Heavy, waiter); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire with every slot held = %v, want the context's deadline", err)
	}
	line := notice.String()
	if !strings.Contains(line, fmt.Sprintf("pid %d", os.Getpid())) || !strings.Contains(line, "waiting for a heavy slot") {
		t.Fatalf("waiting line does not name the holder:\n%s", line)
	}

	_ = held.Release()
	s := acquire(t, opt)
	if s.Index() != 0 {
		t.Fatalf("after the holder released, Index = %d, want slot 0", s.Index())
	}
	_ = s.Release()
}

func TestAcquire_QueueIsFIFO(t *testing.T) {
	opt := testOpts(t, 1)
	held := acquire(t, opt)
	qdir := filepath.Join(opt.Dir, Heavy, "queue")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type result struct {
		name string
		slot *Slot
		err  error
	}
	results := make(chan result, 2)
	start := func(name string, wantQueued int) {
		go func() {
			s, err := Acquire(ctx, Heavy, opt)
			results <- result{name, s, err}
		}()
		waitFor(t, func() bool { return queueLen(qdir) == wantQueued })
	}
	start("first", 1)
	start("second", 2)

	_ = held.Release()
	first := <-results
	if first.err != nil || first.name != "first" {
		t.Fatalf("the freed slot went to %q (err %v), want the waiter that queued first", first.name, first.err)
	}
	select {
	case r := <-results:
		t.Fatalf("%q acquired while the first waiter held the only slot", r.name)
	case <-time.After(50 * time.Millisecond):
	}
	_ = first.slot.Release()
	second := <-results
	if second.err != nil || second.name != "second" {
		t.Fatalf("second waiter: %q, %v", second.name, second.err)
	}
	_ = second.slot.Release()
}

func TestAcquire_ReentrantThroughEnv(t *testing.T) {
	opt := testOpts(t, 1)
	parent := acquire(t, opt)
	env := parent.Env()
	if len(env) != 1 || !strings.HasPrefix(env[0], EnvHeld+"=heavy:0:") {
		t.Fatalf("Env() = %q, want one %s entry for slot 0", env, EnvHeld)
	}

	child := opt
	child.Env = env
	s := acquire(t, child) // would block forever without re-entrancy: the only slot is held
	if s.Reentrant() == "" || s.Index() != -1 {
		t.Fatalf("child of a slot holder took slot %d (reentrant=%q), want it to take nothing", s.Index(), s.Reentrant())
	}
	_ = s.Release()

	// A token for a slot whose holder has gone is not trusted.
	_ = parent.Release()
	stale := acquire(t, child)
	if stale.Reentrant() != "" || stale.Index() != 0 {
		t.Fatalf("stale token: reentrant=%q index=%d, want a real acquisition of slot 0", stale.Reentrant(), stale.Index())
	}

	// A token whose nonce does not match the slot's record is not trusted
	// either: it waits like anyone else.
	forged := opt
	forged.Env = []string{EnvHeld + "=heavy:0:deadbeef"}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, err := Acquire(ctx, Heavy, forged); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("forged token: Acquire = %v, want it to wait for the held slot", err)
	}
	_ = stale.Release()
}

func TestSlotEnv_KeepsOtherClasses(t *testing.T) {
	opt := testOpts(t, 1)
	opt.Env = []string{EnvHeld + "=build:3:aaaa,heavy:9:bbbb"}
	s := acquire(t, opt)
	defer func() { _ = s.Release() }()
	got := strings.TrimPrefix(s.Env()[0], EnvHeld+"=")
	if !strings.HasPrefix(got, "heavy:0:") || !strings.Contains(got, ",build:3:aaaa") || strings.Contains(got, "heavy:9") {
		t.Fatalf("Env merged to %q: want this heavy slot, the inherited build slot, and not the old heavy entry", got)
	}
}

func TestAcquire_Unlimited(t *testing.T) {
	opt := testOpts(t, 0)
	opt.Env = []string{SlotsEnv(Heavy) + "=off"}
	s := acquire(t, opt)
	if !s.Unlimited() || s.Index() != -1 || s.Env() != nil {
		t.Fatalf("FORGE_HEAVY_SLOTS=off: Unlimited=%v Index=%d Env=%v, want a no-op slot", s.Unlimited(), s.Index(), s.Env())
	}
}

func TestQueue_ReapsDeadWaiters(t *testing.T) {
	opt := testOpts(t, 1)
	held := acquire(t, opt)
	defer func() { _ = held.Release() }()
	qdir := filepath.Join(opt.Dir, Heavy, "queue")
	// An unlocked ticket past the grace window: its waiter died.
	old := fmt.Sprintf("%019d-%d", time.Now().Add(-time.Minute).UnixNano(), 999999)
	if err := os.WriteFile(filepath.Join(qdir, old), []byte("pid=999999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// One inside the grace window is a waiter that has not locked it yet.
	fresh := fmt.Sprintf("%019d-%d", time.Now().UnixNano(), 999998)
	if err := os.WriteFile(filepath.Join(qdir, fresh), []byte("pid=999998\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	q := queue{dir: qdir}
	if n := q.ahead(""); n != 1 {
		t.Fatalf("ahead = %d, want only the fresh ticket counted", n)
	}
	if _, err := os.Stat(filepath.Join(qdir, old)); !os.IsNotExist(err) {
		t.Fatalf("dead waiter's ticket was not reaped: %v", err)
	}
}

func TestStatus_ListsHolders(t *testing.T) {
	opt := testOpts(t, 2)
	opt.Label = "go test ./..."
	s := acquire(t, opt)
	defer func() { _ = s.Release() }()
	rep, err := Status(Heavy, opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Holders) != 1 || rep.Holders[0].PID != os.Getpid() || rep.Holders[0].Command != "go test ./..." {
		t.Fatalf("Status holders = %+v, want this process with its label", rep.Holders)
	}
	_ = s.Release()
	rep, _ = Status(Heavy, opt)
	if len(rep.Holders) != 0 {
		t.Fatalf("after Release, Status still lists %+v", rep.Holders)
	}
}

func TestAcquire_RejectsBadClass(t *testing.T) {
	if _, err := Acquire(context.Background(), "Heavy Jobs", testOpts(t, 1)); err == nil {
		t.Fatal("Acquire accepted a class name with spaces and capitals")
	}
}

// ---- multi-process properties ------------------------------------------------

// TestSlotHelperProcess is not a test. Re-exec'd with `-- <mode> <dir>` after
// the test flags (flag.Args) it plays one process:
//
//	hold     take the slot, write "held", then sleep until killed
//	exit     take the slot, write "held", exit WITHOUT releasing
//	acquire  try to take a slot within 2s; exit 0 if Acquire returned
//	         re-entrant, 10 if it took a real slot, 11 if it timed out
func TestSlotHelperProcess(t *testing.T) {
	args := flag.Args()
	if len(args) != 2 {
		return
	}
	mode, dir := args[0], args[1]
	// A scrubbed environment: whatever this process knows about its
	// parent's slot, it does not know through FORGE_SLOT_HELD.
	opt := Options{Dir: dir, Slots: 1, Poll: 5 * time.Millisecond, Env: []string{}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s, err := Acquire(ctx, Heavy, opt)
	switch {
	case mode == "acquire" && errors.Is(err, context.DeadlineExceeded):
		os.Exit(11)
	case err != nil:
		fmt.Fprintln(os.Stderr, "acquire:", err)
		os.Exit(3)
	}
	switch mode {
	case "acquire":
		if s.Reentrant() != "" {
			os.Exit(0)
		}
		os.Exit(10)
	case "hold", "exit":
		if err := os.WriteFile(filepath.Join(dir, "held"), nil, 0o644); err != nil {
			os.Exit(4)
		}
		if mode == "exit" {
			os.Exit(0) // no Release: the OS must drop the lock
		}
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}

func helper(t *testing.T, mode, dir string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSlotHelperProcess$", "--", mode, dir)
	// An empty environment: whatever the child knows about its parent's
	// slot, it does not know through FORGE_SLOT_HELD.
	cmd.Env = []string{}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	return cmd
}

func TestAcquire_SlotFreedWhenHolderCrashes(t *testing.T) {
	opt := testOpts(t, 1)
	for _, mode := range []string{"exit", "hold"} {
		t.Run(mode, func(t *testing.T) {
			_ = os.Remove(filepath.Join(opt.Dir, "held"))
			cmd := helper(t, mode, opt.Dir)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waitFor(t, func() bool { _, err := os.Stat(filepath.Join(opt.Dir, "held")); return err == nil })
			if mode == "hold" {
				_ = cmd.Process.Kill() // SIGKILL: no deferred cleanup runs
			}
			_ = cmd.Wait()
			s := acquire(t, opt)
			if s.Index() != 0 {
				t.Fatalf("after the holder died, Index = %d, want its slot 0", s.Index())
			}
			_ = s.Release()
		})
	}
}

func TestAcquire_ReentrantForDescendantsOfSubtreeHolder(t *testing.T) {
	for _, tc := range []struct {
		subtree  bool
		wantExit int
	}{
		{subtree: true, wantExit: 0},   // child of a wrapper: inside its slot
		{subtree: false, wantExit: 11}, // child of an in-process holder: waits
	} {
		t.Run(fmt.Sprintf("subtree=%v", tc.subtree), func(t *testing.T) {
			if !tc.subtree && testing.Short() {
				t.Skip("waits out the helper's 2s acquire timeout; runs in task test")
			}
			opt := testOpts(t, 1)
			opt.Subtree = tc.subtree
			s := acquire(t, opt)
			defer func() { _ = s.Release() }()
			err := helper(t, "acquire", opt.Dir).Run()
			code := 0
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != tc.wantExit {
				t.Fatalf("child with a scrubbed env exited %d, want %d", code, tc.wantExit)
			}
		})
	}
}

// ---- capacity ------------------------------------------------------------------

func TestDefaultSlots(t *testing.T) {
	const g = 1 << 30
	for _, tc := range []struct {
		cpus float64
		mem  uint64
		want int
	}{
		{16, 32 * g, 4},                    // the workspace VM
		{16, 33_291_000_000, 4},            // "32 GiB" as MemTotal reports it, a little under
		{4, 16 * g, 2},                     // a CI runner
		{16, 128 * g, 8},                   // a workstation: CPU-bound, 2 per slot
		{8, 16 * g, 2},                     //
		{64, 32 * g, 4},                    // memory-bound
		{2, 4 * g, 1},                      // never zero
		{16, 0, 8},                         // memory unknown: CPUs alone
		{1.5, 0, 1},                        // a fractional quota
		{32, 31*g + 300*(1<<20), 4},        // rounds memory to nearest
		{32, uint64(11.5 * float64(g)), 1}, // 11.5/8 rounds to 1
	} {
		if got := DefaultSlots(Machine{CPUs: tc.cpus, MemBytes: tc.mem}); got != tc.want {
			t.Errorf("DefaultSlots(%v CPUs, %d bytes) = %d, want %d", tc.cpus, tc.mem, got, tc.want)
		}
	}
}

func TestCapacityFor_EnvOverride(t *testing.T) {
	for _, tc := range []struct {
		val       string
		want      int
		unlimited bool
		wantErr   bool
	}{
		{val: "6", want: 6},
		{val: "off", unlimited: true},
		{val: "Unlimited", unlimited: true},
		{val: "0", wantErr: true},
		{val: "-2", wantErr: true},
		{val: "lots", wantErr: true},
	} {
		c, err := CapacityFor(Heavy, []string{"FORGE_HEAVY_SLOTS=" + tc.val})
		if (err != nil) != tc.wantErr {
			t.Errorf("FORGE_HEAVY_SLOTS=%s: err = %v, wantErr %v", tc.val, err, tc.wantErr)
			continue
		}
		if err == nil && (c.Slots != tc.want || c.Unlimited != tc.unlimited) {
			t.Errorf("FORGE_HEAVY_SLOTS=%s: %+v", tc.val, c)
		}
	}
	if got := SlotsEnv("long-tests"); got != "FORGE_LONG_TESTS_SLOTS" {
		t.Errorf("SlotsEnv(long-tests) = %q", got)
	}
	c, err := CapacityFor(Heavy, []string{})
	if err != nil || c.Slots < 1 || !strings.Contains(c.Source, "CPUs") {
		t.Errorf("machine default: %+v, %v", c, err)
	}
}

func TestReadCgroupV2(t *testing.T) {
	root := t.TempDir()
	write := func(rel, data string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The kata shape: the sandbox limit on the root, "max" on the process's
	// own cgroup.
	write("cpu.max", "1600000 100000\n")
	write("memory.max", "34359738368\n")
	write("daemon/cpu.max", "max 100000\n")
	write("daemon/memory.max", "max\n")
	write("self", "0::/daemon\n")
	lim, ok := readCgroupV2(root, filepath.Join(root, "self"))
	if !ok || lim.cpus != 16 || lim.mem != 32<<30 {
		t.Fatalf("kata shape: %+v ok=%v, want 16 CPUs / 32 GiB from the root", lim, ok)
	}

	// A tighter limit lower down wins.
	write("daemon/memory.max", "8589934592\n")
	lim, _ = readCgroupV2(root, filepath.Join(root, "self"))
	if lim.mem != 8<<30 || lim.cpus != 16 {
		t.Fatalf("nested limit: %+v, want 8 GiB from the child and 16 CPUs from the root", lim)
	}

	// A cgroup namespace: the named path does not exist under the mount.
	write("self", "0::/elsewhere/deep\n")
	lim, ok = readCgroupV2(root, filepath.Join(root, "self"))
	if !ok || lim.cpus != 16 {
		t.Fatalf("namespaced: %+v ok=%v, want the mount root's limits", lim, ok)
	}

	// cgroup v1 has no unified line.
	write("self", "4:memory:/foo\n")
	if _, ok := readCgroupV2(root, filepath.Join(root, "self")); ok {
		t.Fatal("cgroup v1 read as v2")
	}

	m := cgroupLimit{cpus: 16, mem: 32 << 30}.apply(Machine{CPUs: 64, CPUSource: "nproc", MemBytes: 256 << 30, MemSource: "/proc/meminfo"})
	if m.CPUs != 16 || m.MemBytes != 32<<30 || m.CPUSource != "cgroup cpu.max" || m.MemSource != "cgroup memory.max" {
		t.Fatalf("cgroup limits did not win over the host totals: %+v", m)
	}
}

func TestMeminfoTotal(t *testing.T) {
	got := meminfoTotal([]byte("MemFree:  1 kB\nMemTotal:       35521440 kB\n"))
	if got != 35521440*1024 {
		t.Fatalf("meminfoTotal = %d", got)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached within 10s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func queueLen(dir string) int {
	entries, _ := os.ReadDir(dir)
	return len(entries)
}

func TestSetLabelAndInside(t *testing.T) {
	opt := testOpts(t, 1)
	s := acquire(t, opt)
	s.SetLabel("ci gate: golangci-lint")
	rep, err := Status(Heavy, opt)
	if err != nil || len(rep.Holders) != 1 || rep.Holders[0].Command != "ci gate: golangci-lint" {
		t.Fatalf("after SetLabel, Status = %+v, %v", rep.Holders, err)
	}
	if _, ok := Inside(Heavy, opt); ok {
		t.Fatal("Inside reports true for a process whose env carries no slot")
	}
	child := opt
	child.Env = s.Env()
	if why, ok := Inside(Heavy, child); !ok || !strings.Contains(why, EnvHeld) {
		t.Fatalf("Inside with the holder's env = %q, %v; want inside", why, ok)
	}
	_ = s.Release()
	if _, ok := Inside(Heavy, child); ok {
		t.Fatal("Inside trusts a token whose slot was released")
	}
}

// A waiter's new ticket is visible, unlocked, between its create and its
// lock, and another waiter's alive() probe can hold the lock right then: it
// takes it to test for a dead waiter, sees a ticket younger than ticketGrace,
// and lets go. Filing the ticket must ride that out — a single lock attempt
// failed the whole Acquire ("lock slot ticket …"), which is how
// TestAcquire_NeverMoreThanNHolders flaked on CI. Deterministic: the hook
// probes every ticket at exactly that moment.
func TestEnqueue_SurvivesAProbeOfTheFreshTicket(t *testing.T) {
	q := queue{dir: t.TempDir()}
	probed := make(chan struct{}, 1)
	t.Cleanup(func() { ticketFiled = nil })
	ticketFiled = func(path string) {
		if !q.alive(path, filepath.Base(path)) {
			t.Errorf("a fresh ticket was reaped")
		}
		// Hold the lock a while, as a probe on a busy host can.
		f, err := os.Open(path)
		if err != nil {
			t.Errorf("open ticket: %v", err)
			return
		}
		if ok, err := tryLock(f); err != nil || !ok {
			t.Errorf("the probe could not lock the fresh ticket (ok=%v err=%v): the race did not happen", ok, err)
			_ = f.Close()
			return
		}
		go func() {
			time.Sleep(20 * time.Millisecond)
			_ = unlock(f)
			_ = f.Close()
			probed <- struct{}{}
		}()
	}
	tk, err := q.enqueue(record{})
	if err != nil {
		t.Fatalf("enqueue failed while another waiter probed the new ticket: %v", err)
	}
	<-probed
	defer tk.remove()
	if q.alive(tk.path, tk.name) != true {
		t.Fatal("the filed ticket is not held by its waiter")
	}
	other, err := os.Open(tk.path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if ok, _ := tryLock(other); ok {
		t.Fatal("the filed ticket is not locked by its waiter")
	}
}
