//go:build !windows

package pgtest

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// A standalone server's owner may run in another pid namespace, where its pid
// in forge-owner.pid names a dead process (or another one). Its owner lock is
// what every reaper reads: held, the server is in use and is never reaped,
// however old; released (its holder exited, however it exited), it is not.
func TestStandaloneOwnerLockKeepsTheReaperOffAcrossPidNamespaces(t *testing.T) {
	privatePool(t)
	port := silentPort(t)
	dir := runtimeDir(port)
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The owner's pid as this process sees one from another namespace: dead.
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ownerFile), []byte(strconv.Itoa(dead.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	name := strconv.FormatUint(uint64(port), 10)
	if portsInUse()[name] {
		t.Fatal("with no owner lock and a dead owner pid, the dir must not count as in use (the test would prove nothing)")
	}

	owner, err := holdOwnerLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !portsInUse()[name] {
		t.Fatal("a held owner lock must mark the standalone server in use, whatever its owner pid looks like from here")
	}
	// Old enough to reap, and still kept while its owner holds the lock.
	old := time.Now().Add(-2 * staleInstanceAge)
	_ = os.Chtimes(dir, old, old)
	reapStaleInstances()
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the reaper removed a standalone server its owner still holds: %v", err)
	}

	_ = owner.Close()
	if portsInUse()[name] {
		t.Fatal("a released owner lock (its holder gone) must not keep the dir in use")
	}
}

// Lease creation fails (a permission or filesystem error): the attach falls
// back to a standalone server, and that server's owner lock is held, so no
// other process's reaper can take it while this process uses it.
func TestAttachWithoutALeaseRunsAnOwnerLockedStandaloneServer(t *testing.T) {
	if testing.Short() {
		t.Skip("boots an embedded postgres; runs in task test (full mode)")
	}
	privatePool(t)
	// An earlier test of this process can leave its lease behind, and attach
	// takes a lease only when it holds none: clear it, or the injected
	// failure never runs and the attach pools.
	prev := poolLease
	poolLease = nil
	t.Cleanup(func() { poolLease = prev })
	takeLeaseFn = func(string) (*lease, error) { return nil, errors.New("injected: cannot create a lease") }
	t.Cleanup(func() { takeLeaseFn = takeLease })

	localMu.Lock()
	url, port, mode, err := attachEmbedded()
	localMu.Unlock()
	if err != nil {
		t.Fatalf("attach without a lease: %v", err)
	}
	t.Cleanup(func() {
		stopSharedByPort(port)
		if standaloneOwner != nil {
			_ = standaloneOwner.Close()
			standaloneOwner = nil
		}
	})
	if mode != poolModeStandalone || url == "" {
		t.Fatalf("mode = %d, url %q: want a standalone server", mode, url)
	}
	if !ownerLockHeld(runtimeDir(port)) || !portsInUse()[strconv.FormatUint(uint64(port), 10)] {
		t.Fatal("a standalone server booted without a lease must be owner-locked and in use")
	}
	if !CanReach(url) {
		t.Fatalf("the standalone server at %s does not answer", url)
	}
}
