//go:build !windows

package pgtest

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// privatePool points this test's pool and runtime dirs (both under
// os.TempDir()) at a directory of its own, so stalling "the shared server"
// stalls nobody else's tests on the machine. Short, under /tmp: postgres
// keeps its socket in /tmp regardless, but a long TMPDIR has bitten macOS
// path limits before.
func privatePool(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pgt")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", dir)
	t.Cleanup(func() {
		if st := readPoolState(); st.Port != 0 {
			stopSharedByPort(st.Port)
		}
		entries, _ := os.ReadDir(poolRoot())
		for _, e := range entries {
			if e.IsDir() {
				if port, err := strconv.ParseUint(e.Name(), 10, 32); err == nil {
					stopSharedByPort(uint32(port))
				}
			}
		}
		_ = os.RemoveAll(dir)
	})
}

// sharedPostmaster boots the pool's shared server as a live user (this test
// process) and returns its port, URL and postmaster pid.
func sharedPostmaster(t *testing.T) (uint32, string, int) {
	t.Helper()
	url, port, mode, err := attachEmbedded()
	if err != nil {
		t.Fatalf("boot the shared server: %v", err)
	}
	if mode != poolModePooled {
		t.Fatalf("the first attach is not pooled (mode %d)", mode)
	}
	pid, alive := postmaster(filepath.Join(runtimeDir(port), "data", "postmaster.pid"))
	if !alive {
		t.Fatal("the shared server has no live postmaster")
	}
	return port, url, pid
}

// A shared server that is slow to answer is not a dead one. On a loaded or
// just-woken machine a postmaster can take seconds to accept a connection;
// the attaching process used to give it 2s to connect and 3s to ping, then
// `pg_ctl stop -m fast` it — under every other process's running tests, which
// then failed with 57P01 "terminating connection due to administrator
// command" and 57P03 "the database system is shutting down" (control-plane's
// integration lane, on a busy box, four runs in a row).
//
// The stall is simulated by SIGSTOPping the postmaster for longer than the
// old probe budget: it cannot fork a backend for a new connection, exactly as
// a starved or frozen one cannot, and resumes afterwards.
func TestAttach_AStalledSharedServerIsWaitedForNotStopped(t *testing.T) {
	if testing.Short() {
		t.Skip("boots an embedded postgres and stalls it for seconds; runs in task test")
	}
	privatePool(t)
	port, url, pid := sharedPostmaster(t)

	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	resumed := make(chan struct{})
	go func() {
		time.Sleep(8 * time.Second)
		_ = syscall.Kill(pid, syscall.SIGCONT)
		close(resumed)
	}()
	t.Cleanup(func() { <-resumed })

	gotURL, gotPort, mode, err := attachEmbedded()
	if err != nil {
		t.Fatalf("attach while the shared server stalled: %v", err)
	}
	<-resumed
	if _, alive := postmaster(filepath.Join(runtimeDir(port), "data", "postmaster.pid")); !alive || !processAlive(pid) {
		t.Fatalf("the stalled shared server (postmaster %d) was stopped by an attaching process", pid)
	}
	if mode != poolModePooled || gotPort != port || gotURL != url {
		t.Errorf("attach used port %d (mode %d), want the shared server on %d", gotPort, mode, port)
	}
	if !CanReach(url) {
		t.Error("the shared server does not answer after the stall")
	}
}

// A shared server that stays unresponsive past sharedPatience while other
// processes still use it is theirs: the attaching process runs on a server of
// its own instead of stopping it.
func TestAttach_ABusySharedServerIsNeverStopped(t *testing.T) {
	if testing.Short() {
		t.Skip("boots two embedded postgres servers; runs in task test")
	}
	privatePool(t)
	port, _, pid := sharedPostmaster(t)
	prev := sharedPatience
	sharedPatience = 2 * time.Second
	t.Cleanup(func() { sharedPatience = prev })

	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	stopped := true
	resume := func() {
		if stopped {
			_ = syscall.Kill(pid, syscall.SIGCONT)
			stopped = false
		}
	}
	t.Cleanup(resume)

	_, gotPort, mode, err := attachEmbedded()
	resume()
	if err != nil {
		t.Fatalf("attach beside a busy shared server: %v", err)
	}
	t.Cleanup(func() { stopSharedByPort(gotPort) })
	if !processAlive(pid) {
		t.Fatalf("the busy shared server (postmaster %d) was stopped by an attaching process", pid)
	}
	if mode != poolModeStandalone || gotPort == port {
		t.Errorf("attach returned port %d mode %d, want a standalone server beside %d", gotPort, mode, port)
	}
	if st := readPoolState(); st.Port != port {
		t.Errorf("the pool now records port %d, want the shared server's %d", st.Port, port)
	}
}

// Age is not abandonment. A shared server with a live user, and a standalone
// one whose owner is alive, survive reapStaleInstances however old their pid
// file looks — a long fan-out on a loaded box, or a sleep (which ages every
// file by the wall clock), used to get them SIGKILLed under running tests.
func TestReap_SparesOldServersStillInUse(t *testing.T) {
	if testing.Short() {
		t.Skip("boots two embedded postgres servers; runs in task test")
	}
	privatePool(t)
	port, _, pid := sharedPostmaster(t)
	_, standalonePort, _, err := bootStandalone()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopSharedByPort(standalonePort) })
	standalonePid, _ := postmaster(filepath.Join(runtimeDir(standalonePort), "data", "postmaster.pid"))

	// Both servers are now older than the stale age, as a fan-out's shared
	// server is after half an hour (or after a sleep: the age is wall-clock).
	prev := staleInstanceAge
	staleInstanceAge = time.Second
	t.Cleanup(func() { staleInstanceAge = prev })
	time.Sleep(2 * time.Second)

	reapStaleInstances()
	for _, s := range []struct {
		what string
		port uint32
		pid  int
	}{{"the shared server, which has a live user", port, pid}, {"a standalone server, whose owner is alive", standalonePort, standalonePid}} {
		if !processAlive(s.pid) {
			t.Errorf("reapStaleInstances killed %s", s.what)
		}
		if _, err := os.Stat(filepath.Join(runtimeDir(s.port), "data")); err != nil {
			t.Errorf("reapStaleInstances removed the data directory of %s: %v", s.what, err)
		}
	}
}
