//go:build !windows

package pgtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A lease is live exactly while some open file holds its lock, whoever holds
// it: here two leases of this process (two open files, as two processes
// would have), one abandoned lease file (its holder died, so nothing holds
// it), and a lease of another scope.
func TestLeases_CountTheHeldAndPruneTheAbandoned(t *testing.T) {
	privatePool(t)
	if err := os.MkdirAll(poolRoot(), 0o755); err != nil {
		t.Fatal(err)
	}
	const scope = "aaaaaaaaaaaa"
	mine, err := takeLease(scope)
	if err != nil {
		t.Fatal(err)
	}
	defer mine.release()
	other, err := takeLease(scope)
	if err != nil {
		t.Fatal(err)
	}
	abandoned := filepath.Join(poolRoot(), leasePrefix+scope+"-1-dead")
	if err := os.WriteFile(abandoned, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	elsewhere, err := takeLease("bbbbbbbbbbbb")
	if err != nil {
		t.Fatal(err)
	}
	defer elsewhere.release()

	if n := liveLeases(scope, mine.path, false); n != 1 {
		t.Errorf("liveLeases without prune = %d, want 1 (the other user's)", n)
	}
	if _, err := os.Stat(abandoned); err != nil {
		t.Errorf("a count without prune removed the abandoned lease: %v", err)
	}
	if n := liveLeases(scope, mine.path, true); n != 1 {
		t.Errorf("liveLeases = %d, want 1 (the other user's)", n)
	}
	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Errorf("the abandoned lease survived a prune (stat err = %v)", err)
	}
	if n := liveLeases(scope, "", true); n != 2 {
		t.Errorf("liveLeases with no exception = %d, want 2", n)
	}

	other.release()
	if n := liveLeases(scope, mine.path, true); n != 0 {
		t.Errorf("liveLeases after the other user released = %d, want 0", n)
	}
}

// A user's lease is released by the kernel when its process dies, however it
// dies: a SIGKILLed user never pins the server open.
//
// flock(1) forks its command with a copy of the locked descriptor, and the
// child closes that copy (--close) only after the fork, so the lock can still
// be held for an instant after the flock process itself is reaped. The
// release is therefore awaited, bounded, rather than sampled once: the
// sampled version failed on CI (liveLeases = 1).
func TestLeases_AKilledHoldersLeaseIsReleased(t *testing.T) {
	privatePool(t)
	if err := os.MkdirAll(poolRoot(), 0o755); err != nil {
		t.Fatal(err)
	}
	flock, err := exec.LookPath("flock")
	if err != nil {
		t.Skip("needs util-linux flock(1) to hold a lease from another process")
	}
	const scope = "cccccccccccc"
	path := filepath.Join(poolRoot(), leasePrefix+scope+"-child")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	holder := exec.Command(flock, "--shared", "--close", path, "sleep", "60")
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Process.Kill(); _ = holder.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	for liveLeases(scope, "", false) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the child never took its lease")
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = holder.Process.Kill()
	_ = holder.Wait()
	deadline = time.Now().Add(10 * time.Second)
	for {
		n := liveLeases(scope, "", true)
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("liveLeases 10s after its holder was killed = %d, want 0", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The last-user decision is the leases', not the pids'. A user in another pid
// namespace holds a lease and records no pid this process could probe; this
// process detaching must leave the server to it. Once that user has gone too,
// the last detach tears the server down as before.
func TestDetach_AnotherUsersLeaseKeepsTheServer(t *testing.T) {
	privatePool(t)
	if err := os.MkdirAll(poolRoot(), 0o755); err != nil {
		t.Fatal(err)
	}
	port := silentPort(t)
	fakeServerDir(t, port)
	writePoolState(poolState{URL: "postgres://shared", Port: port})

	other, err := takeLease(poolScope())
	if err != nil {
		t.Fatal(err)
	}
	if poolLease, err = takeLease(poolScope()); err != nil {
		t.Fatal(err)
	}
	detachEmbedded(port)
	if _, err := os.Stat(filepath.Join(runtimeDir(port), "data")); err != nil {
		t.Fatalf("detach tore down a server another user still leases: %v", err)
	}
	if st := readPoolState(); st.Port != port {
		t.Errorf("pool state %+v after detach, want the server on %d kept", st, port)
	}

	other.release()
	if poolLease, err = takeLease(poolScope()); err != nil {
		t.Fatal(err)
	}
	detachEmbedded(port)
	if _, err := os.Stat(runtimeDir(port)); !os.IsNotExist(err) {
		t.Errorf("the last user's detach left the server's dir behind (stat err = %v)", err)
	}
	if st := readPoolState(); st.Port != 0 {
		t.Errorf("pool state %+v after the last detach, want none", st)
	}
}

// A runtime dir whose server is gone is still reclaimed.
func TestStopSharedByPort_RemovesTheDirOfAServerThatIsGone(t *testing.T) {
	privatePool(t)
	port := silentPort(t)
	fakeServerDir(t, port)
	if !stopSharedByPort(port) {
		t.Error("stopSharedByPort reported a server with no postmaster and no listener as still running")
	}
	if _, err := os.Stat(runtimeDir(port)); !os.IsNotExist(err) {
		t.Errorf("the dir of a gone server was left behind (stat err = %v)", err)
	}
}
