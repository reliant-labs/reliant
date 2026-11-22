package pgtest

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// One sweep, against a real server, over a database in every state a name can
// record. Exactly the provably abandoned ones go.
func TestReapAbandonedScratch_DropsOnlyTheAbandoned(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres and spawns processes; skipped under -short")
	}
	me := selfOwner()
	if me.space == scratchUnknown || me.start == scratchUnknown {
		t.Skip("this platform records no owner; its scratch databases are never reaped by liveness")
	}
	s, err := boot()
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	base := s.baseDB

	// A real process that exited (its pid is dead) and a real one still
	// running (its pid is live, with a start token we can read).
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	live := exec.Command("sleep", "300")
	if err := live.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Process.Kill(); _ = live.Wait() })
	liveTok, ok := processStartToken(live.Process.Pid)
	if !ok {
		t.Fatal("cannot read a child's start token")
	}
	liveStart := shortHash(liveTok, 8)

	old := time.Now().Add(-time.Hour).Unix()
	young := time.Now().Unix()
	n := 0
	mk := func(space string, pid int, start string, created int64) string {
		n++
		return fmt.Sprintf("%s%s_%d_%s_%d_%d", scratchTestPrefix, space, pid, start, created, 900000+n)
	}
	want := map[string]bool{ // name -> survives the sweep
		mk(me.space, gone.Process.Pid, "22222222", old):           false, // owner exited
		mk(me.space, live.Process.Pid, "33333333", old):           false, // the pid was recycled by `sleep`
		mk(me.space, live.Process.Pid, liveStart, old):            true,  // the owner is alive
		mk(me.space, gone.Process.Pid, "22222222", young):         true,  // within the grace
		mk("bbbbbbbbbbbb", gone.Process.Pid, "22222222", old):     true,  // another machine or container
		mk(me.space, gone.Process.Pid, scratchUnknown, old):       true,  // owner unrecorded
		fmt.Sprintf("forge_test_%d_%d", gone.Process.Pid, 900099): true,  // an older pgtest's name
	}
	for name := range want {
		if _, err := base.Exec("CREATE DATABASE " + quoteIdent(name)); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		t.Cleanup(func() { _, _ = base.Exec("DROP DATABASE IF EXISTS " + quoteIdent(name)) })
	}

	dropped := reapAbandonedScratch(context.Background(), base, me, probePid, time.Now(), 10*time.Minute)
	for _, name := range dropped {
		if want[name] {
			t.Errorf("the sweep dropped %s, which it had no proof was abandoned", name)
		}
	}
	for name, survives := range want {
		var exists bool
		if err := base.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != survives {
			t.Errorf("%s: exists after the sweep = %v, want %v", name, exists, survives)
		}
	}
}

// helperEnv selects TestScratchReapHelper's role in a child process.
const helperEnv = "FORGE_PGTEST_SCRATCH_HELPER"

// TestScratchReapHelper is not a test: it is a child process of
// TestScratchReap_ConcurrentProcesses, one test binary per simulated `go test`
// process, in one of two roles.
//
//	hold   create a scratch database the way every caller does, print its
//	       name, and use it until stdin closes (or the parent kills us)
//	sweep  reach the server as a fresh process does — which sweeps it — with
//	       no grace, so only the owner check protects anyone
func TestScratchReapHelper(t *testing.T) {
	role := os.Getenv(helperEnv)
	if role == "" {
		t.Skip("helper process for TestScratchReap_ConcurrentProcesses")
	}
	switch role {
	case "hold":
		db, cleanup, err := New()
		if err != nil {
			fmt.Println("ERR", err)
			return
		}
		defer cleanup()
		var name string
		if err := db.QueryRow(`SELECT current_database()`).Scan(&name); err != nil {
			fmt.Println("ERR", err)
			return
		}
		fmt.Println("DB", name)
		_, _ = io.Copy(io.Discard, os.Stdin)
		var one int
		if err := db.QueryRow(`SELECT 1`).Scan(&one); err != nil {
			t.Fatalf("%s after the sweeps: %v", name, err)
		}
	case "sweep":
		scratchGrace = 0
		if _, err := boot(); err != nil {
			fmt.Println("ERR", err)
			return
		}
		fmt.Println("SWEPT")
	}
}

// Many test processes share one server, start and die at once, and every one
// of them sweeps it on arrival. The databases of the processes that crashed
// are collected; not one database of a process still running is touched, and
// each stays usable.
func TestScratchReap_ConcurrentProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres and spawns processes; skipped under -short")
	}
	if me := selfOwner(); me.space == scratchUnknown || me.start == scratchUnknown {
		t.Skip("this platform records no owner; its scratch databases are never reaped by liveness")
	}
	// Hold the shared server open for the children: each attaches to it, and
	// a crashed child's database must still be there to collect.
	s, err := boot()
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	base := s.baseDB

	type helper struct {
		cmd   *exec.Cmd
		stdin io.WriteCloser
		done  chan struct{} // closed once cmd.Wait returned; cmd.ProcessState is then set
		line  string        // what followed want on the line it printed
	}
	// start runs one helper in role and returns once it printed want (its
	// database's name, for a holder). Its stdout keeps draining afterwards, so
	// it never blocks on a full pipe.
	start := func(role, want string) (helper, error) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestScratchReapHelper$", "-test.count=1")
		cmd.Env = append(os.Environ(), helperEnv+"="+role)
		cmd.Stderr = os.Stderr
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return helper{}, err
		}
		pr, pw := io.Pipe()
		cmd.Stdout = pw
		if err := cmd.Start(); err != nil {
			return helper{}, err
		}
		h := helper{cmd: cmd, stdin: stdin, done: make(chan struct{})}
		sc := bufio.NewScanner(pr)
		got, found := "", false
		for !found && sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "ERR ") {
				break
			}
			got, found = strings.CutPrefix(line, want)
		}
		go func() {
			for sc.Scan() {
			}
		}()
		go func() { _ = cmd.Wait(); _ = pw.Close(); close(h.done) }()
		h.line = strings.TrimSpace(got)
		if !found {
			return h, fmt.Errorf("%s helper (pid %d) never printed %q", role, cmd.Process.Pid, want)
		}
		return h, nil
	}

	const holders = 8
	hs := make([]helper, holders)
	errs := make(chan error, holders+8)
	var wg sync.WaitGroup
	for i := range hs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := start("hold", "DB ")
			if err != nil {
				errs <- err
			}
			hs[i] = h
		}()
	}
	wg.Wait()
	t.Cleanup(func() {
		for _, h := range hs {
			if h.cmd != nil {
				_ = h.stdin.Close()
				_ = h.cmd.Process.Kill()
			}
		}
	})
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}

	// Half of them crash: SIGKILL, so no cleanup runs and the database stays.
	var crashed, running []helper
	for i, h := range hs {
		if i%2 == 0 {
			_ = h.cmd.Process.Kill()
			<-h.done
			crashed = append(crashed, h)
		} else {
			running = append(running, h)
		}
	}
	// The dead clients' backends exit when their sockets close; the sweep's
	// DROP is not forced, so wait for that rather than race it.
	deadline := time.Now().Add(30 * time.Second)
	for _, h := range crashed {
		for {
			var sessions int
			if err := base.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE datname = $1`, h.line).Scan(&sessions); err != nil {
				t.Fatal(err)
			}
			if sessions == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s still has %d sessions after its owner was killed", h.line, sessions)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Many fresh processes arrive at once, each sweeping on its way in.
	const sweepers = 6
	for i := 0; i < sweepers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := start("sweep", "SWEPT")
			if h.stdin != nil {
				_ = h.stdin.Close()
			}
			if err != nil {
				errs <- err
				return
			}
			<-h.done
			if !h.cmd.ProcessState.Success() {
				errs <- fmt.Errorf("sweeper pid %d: %v", h.cmd.Process.Pid, h.cmd.ProcessState)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	exists := func(name string) bool {
		t.Helper()
		var ok bool
		if err := base.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	for _, h := range crashed {
		if exists(h.line) {
			t.Errorf("%s survived %d sweeps although its owner (pid %d) was killed", h.line, sweepers, h.cmd.Process.Pid)
			_, _ = base.Exec("DROP DATABASE IF EXISTS " + quoteIdent(h.line))
		}
	}
	for _, h := range running {
		if !exists(h.line) {
			t.Errorf("a sweep dropped %s while its owner (pid %d) was still running", h.line, h.cmd.Process.Pid)
		}
	}
	// And the survivors are still usable: closing stdin makes each owner
	// query its database (exiting non-zero if it cannot), then run its own
	// cleanup, which drops it.
	for _, h := range running {
		_ = h.stdin.Close()
		<-h.done
		if !h.cmd.ProcessState.Success() {
			t.Errorf("holder pid %d could not use its database after the sweeps: %v", h.cmd.Process.Pid, h.cmd.ProcessState)
		}
		if exists(h.line) {
			t.Errorf("%s outlived its owner's own cleanup", h.line)
		}
	}
}
