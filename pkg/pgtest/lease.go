package pgtest

// Who is using the shared server: leases, not pids.
//
// The pool used to record its users as bare pids in its state file and prune
// the ones kill(pid, 0) called dead. That is only meaningful between
// processes in ONE pid namespace, and the pool lives in os.TempDir(), which
// processes in different pid namespaces share: reliant runs credential-free
// gates (pr-fix, pr-regen) in a pid namespace of their own over the host's
// /tmp, beside host-namespace gates (pr-review's verify-generated). Neither
// side could see the other's pids, so each pruned the other's entry, believed
// itself the last user, and tore the server down under the other: `pg_ctl
// stop` could not signal the invisible postmaster, and the data directory was
// removed under it anyway — a postmaster answering pings and failing every
// CREATE DATABASE (control-plane rc.18/rc.19, forge #1906), and a `forge
// generate` that silently wrote no test factories and swept the committed
// ones as stale (forge bug df26c327).
//
// Two changes make the pool correct across pid namespaces:
//
//   - A user is a LEASE: a file of its own in the pool dir on which it holds a
//     shared flock for as long as it uses the server. A kernel file lock
//     belongs to the open file, not to a pid, so every process on the
//     filesystem sees it the same way, in any pid namespace; and the kernel
//     drops it when the holder dies, however it dies. "Am I the last user?"
//     is "can I lock every other lease exclusively?" — a lease nobody holds
//     is a dead process's, and is removed.
//
//   - The pool is SCOPED to a pid space (owner.go's pidSpaceID: the boot plus
//     the pid namespace). Each scope has its own shared server. Leases alone
//     would let two scopes share one server, but a server is only safe to
//     share inside the pid namespace its postmaster runs in: when a pid
//     namespace's init exits the kernel SIGKILLs every process in it, the
//     postmaster included, under any outside user; and only a process in that
//     namespace can signal the postmaster to stop it cleanly.
//
// Every scope's pool state is guarded by one lock file (pool-scopes.lock),
// which is never removed: removing a lock file others may be waiting on lets
// two holders in at once. One lock also lets an attaching process tidy the
// state another scope left behind (gcScopes).

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// poolScopesLockFile guards every scope's pool state and leases.
	poolScopesLockFile = "pool-scopes.lock"
	// poolScopeStatePrefix + scope + ".json" is one scope's pool state.
	poolScopeStatePrefix = "pool-"
	// leasePrefix + scope + "-" names one scope's leases.
	leasePrefix = "lease-"
)

// poolScope names the pid space this process's pool lives in.
var poolScope = sync.OnceValue(func() string {
	if id, ok := pidSpaceID(); ok {
		return shortHash(id, 12)
	}
	// No way to name the pid space (Windows, or an unreadable /proc): one
	// pool, as before scoping.
	return "local"
})

// lease is this process's claim on its scope's shared server: a file whose
// shared lock it holds until release.
type lease struct {
	f    *os.File
	path string
}

// takeLeaseFn is takeLease, a var so a test can make lease creation fail.
var takeLeaseFn = takeLease

// takeLease creates a lease in scope and takes its shared lock. Callers hold
// the pool lock: liveLeases prunes only under it, so a lease is never seen
// between its file appearing and its lock being taken.
func takeLease(scope string) (*lease, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	// The pid is for a person reading the directory; nothing parses it.
	name := fmt.Sprintf("%s%s-%d-%s", leasePrefix, scope, os.Getpid(), hex.EncodeToString(b[:]))
	path := filepath.Join(poolRoot(), name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := lockFileShared(f); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return &lease{f: f, path: path}, nil
}

// release drops the lease: its lock goes with the file, and the file is
// removed. A lease whose holder dies first loses its lock all the same, and
// the next liveLeases removes the file.
func (l *lease) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = l.f.Close()
	_ = os.Remove(l.path)
	l.f = nil
}

// liveLeases counts scope's leases, other than the one at except, that a live
// process holds. With prune, a lease nobody holds (its holder died: the
// kernel released the lock) is removed; prune only under the pool lock. A
// lease whose lock cannot be tested counts as live: a server is never torn
// down on doubt.
func liveLeases(scope, except string, prune bool) int {
	entries, err := os.ReadDir(poolRoot())
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), leasePrefix+scope+"-") {
			continue
		}
		path := filepath.Join(poolRoot(), e.Name())
		if path == except {
			continue
		}
		if leaseHeld(path, prune) {
			n++
		}
	}
	return n
}

// leaseHeld reports whether some process holds the lease at path.
func leaseHeld(path string, prune bool) bool {
	f, err := os.Open(path)
	if err != nil {
		return false // gone: released between the listing and now
	}
	held, err := tryLockFileExclusive(f)
	if err != nil || !held {
		_ = f.Close()
		return true
	}
	_ = f.Close()
	if prune {
		_ = os.Remove(path)
	}
	return false
}

// poolScopes lists the scopes that have pool state in the pool dir.
func poolScopes() []string {
	entries, _ := os.ReadDir(poolRoot())
	var scopes []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, poolScopeStatePrefix) || !strings.HasSuffix(name, ".json") {
			continue
		}
		scope := strings.TrimSuffix(strings.TrimPrefix(name, poolScopeStatePrefix), ".json")
		if scope == "" || strings.ContainsAny(scope, ".-") {
			continue
		}
		scopes = append(scopes, scope)
	}
	return scopes
}

// gcScopes removes the pool state of every OTHER scope that no live lease
// holds and whose server no longer runs: a pid namespace that has gone (a
// finished sandbox) leaves its state behind, and every sandbox run is a new
// scope. Runs under the pool lock. The server dirs themselves are
// reapStaleInstances's.
func gcScopes(mine string) {
	for _, scope := range poolScopes() {
		if scope == mine || liveLeases(scope, "", true) > 0 {
			continue
		}
		st := readPoolStateOf(scope)
		if st.Port != 0 && serverRunning(runtimeDir(st.Port)) {
			continue
		}
		_ = os.Remove(poolStatePathOf(scope))
	}
}

// serverRunning reports whether a postmaster still runs from the runtime dir
// dir, as far as anything can tell. Its pid file names a pid of the pid
// namespace the postmaster started in, so kill(pid, 0) from another namespace
// proves nothing either way: a live postmaster looks dead, or an unrelated
// process looks like it. So a postmaster this process can see and identify
// (isPostgresOf) is running, and otherwise the server's port decides — it
// does not depend on whose pid namespace the postmaster is in. A port another
// program now holds reads as running too: that leaks a dead server's dir,
// which is far cheaper than deleting a live one's.
func serverRunning(dir string) bool {
	pidFile := filepath.Join(dir, "data", "postmaster.pid")
	if pid, alive := postmaster(pidFile); alive && isPostgresOf(pid, dir) {
		return true
	}
	port := serverPort(dir)
	return port != 0 && portAnswers(port)
}

// serverPort is the port the server in dir listens on: the 4th line of its
// pid file, else the runtime dir's own name (runtimeDir is named by port).
func serverPort(dir string) uint32 {
	if b, err := os.ReadFile(filepath.Join(dir, "data", "postmaster.pid")); err == nil {
		lines := strings.Split(string(b), "\n")
		if len(lines) > 3 {
			if p, err := strconv.ParseUint(strings.TrimSpace(lines[3]), 10, 16); err == nil && p > 0 {
				return uint32(p)
			}
		}
	}
	if p, err := strconv.ParseUint(filepath.Base(dir), 10, 16); err == nil && p > 0 {
		return uint32(p)
	}
	return 0
}

// portAnswers reports whether something accepts TCP connections on port at
// the loopback addresses the embedded server listens on ("localhost").
func portAnswers(port uint32) bool {
	for _, host := range []string{"127.0.0.1", "::1"} {
		c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.FormatUint(uint64(port), 10)), time.Second)
		if err == nil {
			_ = c.Close()
			return true
		}
	}
	return false
}
