package pgtest

// This file implements the cross-process shared-embedded "pgpool".
//
// # Why a cross-process pool
//
// Booting embedded postgres is not just slow — each instance permanently
// claims scarce SysV IPC resources (one shared-memory interlock segment plus
// ~one semaphore set per 16 backends the server is sized for). Every `forge
// generate` and every per-package `go test` binary is a SEPARATE OS process,
// and under a parallel fan-out (build_mvp spawns N sub-agents each running
// generate and the suite at once)
// each process used to boot its OWN embedded postgres. N instances alive at
// once means N × (segments + semaphore sets), which on a resource-starved
// host exhausts the kernel tables (macOS default kern.sysv.shmmni is 32) and
// fails initdb with "could not create shared memory segment / semaphores: No
// space left on device", wedging the build.
//
// The pool makes the peak concurrent embedded-instance count 1 instead of N:
// the first process that needs a database boots the embedded server and
// records {URL, port} in a state file guarded by an advisory file lock
// (flock); every concurrent process reads that file, verifies the server is
// alive, and REUSES it — each creating its own scratch database on the one
// shared server (the NewAtURL pattern). Databases are cheap; the server boot
// (and its IPC footprint) is paid once. There is one pool per pid space
// (lease.go): a server is shared only inside the pid namespace its postmaster
// runs in.
//
// # Teardown strategy — reference counting by lease
//
// The shared server intentionally OUTLIVES a single invocation, so a bare
// defer-Stop in the process that booted it would break the siblings still
// using it. Teardown is therefore reference-counted across processes: each
// user holds a lease (a shared flock on a file of its own, lease.go) while it
// uses the server, and drops it on exit (pgtest.Shutdown, wired into the
// forge CLI). The process that finds no other live lease performs the actual
// teardown — a clean `pg_ctl stop` (which releases the semaphore sets and
// shared-memory segment), removal of the data directory, and deletion of the
// state file. A process that dies without detaching loses its lock with it,
// so a crashed user never pins the server open; and startup's
// reapStaleInstances remains as a belt-and-suspenders backstop for the case
// where the very last user also crashed (age-based, 30 min). The data
// directory of a postmaster that still runs is never removed (serverRunning).
//
// The deliberate trade-off: SEQUENTIAL invocations do not reuse across each
// other (the first tears the server down when it exits, the next boots
// fresh). That is correct for the goal — it is the CONCURRENT peak, not the
// total number of boots, that exhausts the IPC tables — and it is what
// guarantees ZERO leftover instances once a fan-out completes. Cross-
// sequential reuse would require leaving an idle server (and its dir)
// lingering, which an idle-timeout reaper only cleans much later.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// legacyPoolStateFile is the state file forge wrote before pools were scoped
// and users leased (lease.go): one pool for every pid namespace, its users as
// pids. A forge that old may still run beside this one, so its live server is
// still spared by reapStaleInstances (portsInUse). The pool's files are FILES
// (not dirs), so reapStaleInstances — which only considers directory entries
// — skips them.
const legacyPoolStateFile = "pool.json"

// legacyPoolState is the part of legacyPoolStateFile portsInUse reads.
type legacyPoolState struct {
	Port  uint32 `json:"port"`
	Users []int  `json:"users"`
}

// poolState is the on-disk registry of a scope's shared embedded server. Who
// uses it is not recorded here: that is the scope's leases (lease.go).
type poolState struct {
	URL  string `json:"url"`
	Port uint32 `json:"port"`
	// PID is the postmaster the booting process saw start, recorded so a
	// server whose data directory (pid file included) was deleted under it
	// can still be found and stopped (health.go). 0 in a state file an older
	// forge wrote.
	PID int `json:"pid,omitempty"`
}

// pool binding modes for the current process's single shared reference.
const (
	poolModeExternal   = iota // FORGE_TEST_POSTGRES_URL — not ours to tear down
	poolModePooled            // shared embedded, coordinated via the lockfile
	poolModeStandalone        // embedded we booted without the lock (degraded)
)

var (
	localMu   sync.Mutex
	localRefs int
	poolURL   string
	poolPort  uint32
	poolMode  int
	// retained keeps the *EmbeddedPostgres this process booted reachable for
	// the process lifetime so nothing finalizes its open log file while the
	// server runs. Teardown is by data dir (stopSharedByPort), not this
	// handle, because the LAST user may be a process that only REUSED the
	// server and never held a handle.
	retained []*Embedded
	// poolLease is this process's lease on its scope's shared server while
	// it is bound to it (poolModePooled).
	poolLease *lease
)

// AcquireShared returns the base maintenance DSN of the postgres server this
// process should use for scratch databases, plus a release that must be
// called when the process is done with it.
//
// Resolution: an explicit FORGE_TEST_POSTGRES_URL wins (external server, no
// teardown responsibility); otherwise the cross-process shared embedded
// instance is booted or reused. Multiple acquisitions in one process share a
// single underlying reference — the LAST release performs the cross-process
// detach (and teardown when this is the last live user across all processes).
//
// It is additive to the pgtest API. New/NewURL use it internally, which is
// what makes a plain `go test ./...` fan-out share ONE instance across its
// per-package binaries — no orchestrator required. (`forge test` used to call
// it directly to pre-resolve a server and export the DSN to its subprocesses;
// with that command gone the in-process path is the only caller, and it gives
// the same sharing.)
func AcquireShared() (baseURL string, release func(), err error) {
	localMu.Lock()
	defer localMu.Unlock()
	if localRefs == 0 {
		if base := strings.TrimSpace(os.Getenv(EnvBaseURL)); base != "" {
			poolURL, poolPort, poolMode = base, 0, poolModeExternal
		} else {
			u, p, mode, aerr := attachEmbedded()
			if aerr != nil {
				return "", nil, aerr
			}
			poolURL, poolPort, poolMode = u, p, mode
		}
	}
	localRefs++
	var once sync.Once
	return poolURL, func() { once.Do(releaseLocal) }, nil
}

// releaseLocal drops one local reference; when the last one goes it performs
// the cross-process detach (pooled) or a direct stop (standalone).
func releaseLocal() {
	localMu.Lock()
	defer localMu.Unlock()
	if localRefs == 0 {
		return
	}
	localRefs--
	if localRefs > 0 {
		return
	}
	switch poolMode {
	case poolModePooled:
		detachEmbedded(poolPort)
	case poolModeStandalone:
		stopSharedByPort(poolPort)
		if standaloneOwner != nil {
			_ = standaloneOwner.Close()
			standaloneOwner = nil
		}
	}
	poolURL, poolPort, poolMode, retained = "", 0, poolModeExternal, nil
}

// attachEmbedded boots or reuses the shared embedded server under the pool
// lock. Returns the maintenance DSN, its port, and the binding mode.
func attachEmbedded() (url string, port uint32, mode int, err error) {
	_ = os.MkdirAll(poolRoot(), 0o755)

	unlock, lerr := lockPool()
	if lerr != nil {
		// Can't coordinate (no lockfile). Boot our own, standalone: correct
		// but unshared. This is the rare degraded path; the common fan-out
		// always has a working lock.
		return bootStandalone()
	}
	defer unlock()

	scope := poolScope()
	gcScopes(scope)
	// The lease first: from here on, no other process of this scope can
	// find itself the last user and stop the server this attach is about to
	// reuse or boot. Without one this process cannot be counted, so it must
	// not share: it runs on a server of its own.
	if poolLease == nil {
		l, lerr := takeLeaseFn(scope)
		if lerr != nil {
			return bootStandalone()
		}
		poolLease = l
	}

	st := readPoolState()
	if st.URL != "" && st.Port != 0 {
		switch probeShared(st, liveLeases(scope, poolLease.path, true) > 0) {
		case sharedReady:
			// Reuse the live shared server.
			return st.URL, st.Port, poolModePooled, nil
		case sharedBusy:
			// Alive, in use, and still not answering: leave it to the
			// processes using it and run on a server of our own.
			poolLease.release()
			poolLease = nil
			return bootStandalone()
		case sharedBroken:
			// Alive but unable to create a database (health.go): it fails
			// every user, so it is replaced even while others hold it. They
			// re-attach on their next failed create (healShared).
			stopBroken(st)
			st.Port = 0
		case sharedGone:
		}
	}

	// Absent, dead, or wedged with nobody using it: clean any remnant, then
	// boot fresh.
	if st.Port != 0 {
		stopSharedByPort(st.Port)
	}
	u, p, ep, claim, berr := bootEmbedded()
	if berr != nil {
		_ = os.Remove(poolStatePath()) // let the next attach retry cleanly
		poolLease.release()
		poolLease = nil
		return "", 0, 0, berr
	}
	retainEmbedded(ep)
	pid, _ := postmaster(filepath.Join(runtimeDir(p), "data", "postmaster.pid"))
	if err := publishBootedPool(poolState{URL: u, Port: p, PID: pid}, claim, func() { stopSharedByPort(p) }); err != nil {
		poolLease.release()
		poolLease = nil
		return "", 0, 0, fmt.Errorf("pgtest: publish shared server ownership: %w", err)
	}
	// Recorded with our live lease, the server is in use (portsInUse):
	// only now may its boot claim go (publishBootedPool).
	return u, p, poolModePooled, nil
}

// detachEmbedded drops this process's lease on the shared server; the
// process that then finds no other live lease tears the server down.
func detachEmbedded(port uint32) {
	unlock, err := lockPool()
	if err != nil {
		// Can't coordinate; do NOT stop — other processes may still be using
		// the shared server. Leave it for reapStaleInstances.
		poolLease.release()
		poolLease = nil
		return
	}
	defer unlock()

	poolLease.release()
	poolLease = nil
	st := readPoolState()
	if st.Port != 0 && st.Port != port {
		// The pool records another server: ours was replaced (health.go)
		// and the recorded one is not this process's to stop.
		return
	}
	if liveLeases(poolScope(), "", true) > 0 {
		return
	}
	p := st.Port
	if p == 0 {
		p = port
	}
	// A server that would not stop keeps its state, so the next attach
	// reuses it rather than booting a second one beside it.
	if stopSharedByPort(p) {
		_ = os.Remove(poolStatePath())
	}
}

// bootStandalone boots an embedded server for this process alone and marks
// it as this process's (ownerFile), so no other process's reaper takes it.
func bootStandalone() (url string, port uint32, mode int, err error) {
	u, p, ep, claim, berr := bootEmbedded()
	if berr != nil {
		return "", 0, 0, berr
	}
	// Held until the owner lock replaces it, so no reaper sees the server
	// unowned in between.
	defer claim.release()
	retainEmbedded(ep)
	// The owner lock is what keeps every other process's reaper off this
	// server, in any pid namespace: a standalone server is the fallback when
	// the pool cannot coordinate (no pool lock, no lease), which is exactly
	// when a pid-based owner check cannot be trusted. Without the lock the
	// server is not booted for use: stopped, and the attach fails.
	f, lerr := holdOwnerLock(runtimeDir(p))
	if lerr != nil {
		stopSharedByPort(p)
		return "", 0, 0, fmt.Errorf("pgtest: cannot hold the standalone server's owner lock: %w", lerr)
	}
	standaloneOwner = f
	_ = os.WriteFile(filepath.Join(runtimeDir(p), ownerFile), []byte(strconv.Itoa(os.Getpid())), 0o644)
	return u, p, poolModeStandalone, nil
}

// ownerFile, in a standalone server's runtime dir, holds the pid of the one
// process using it, for a person reading the directory and for an older
// forge's reaper. What protects the server is ownerLockFile.
const ownerFile = "forge-owner.pid"

// ownerLockFile, in a standalone server's runtime dir, is held with a shared
// flock by the process using the server for as long as it does. Like a pool
// lease (lease.go), a kernel file lock means the same to every process on the
// filesystem whatever pid namespace it runs in, and dies with its holder, so
// portsInUse can tell a live owner from a dead one across namespaces, which
// the pid in ownerFile cannot.
const ownerLockFile = "forge-owner.lock"

// standaloneOwner is this process's open owner lock on its standalone server
// (bootStandalone), closed when the server is stopped (releaseLocal).
var standaloneOwner *os.File

// holdOwnerLock creates dir's owner lock file and takes its shared lock.
func holdOwnerLock(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, ownerLockFile), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := lockFileShared(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// ownerLockHeld reports whether a process holds dir's owner lock. A lock
// that cannot be tested counts as held: a server is never reaped on doubt.
func ownerLockHeld(dir string) bool {
	f, err := os.Open(filepath.Join(dir, ownerLockFile))
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	held, err := tryLockFileExclusive(f)
	return err != nil || !held
}

// sharedPatience is how long an attaching process keeps trying to reach a
// shared server whose postmaster is alive before it stops believing it will
// answer. A var so tests can shorten it.
var sharedPatience = 30 * time.Second

type sharedVerdict int

const (
	// sharedGone: no live postmaster, or one that never answered and that
	// nobody uses — replace it.
	sharedGone sharedVerdict = iota
	// sharedReady: reachable — reuse it.
	sharedReady
	// sharedBusy: the postmaster is alive and in use but did not answer
	// within sharedPatience — never stop it; run beside it.
	sharedBusy
	// sharedBroken: the postmaster is alive but its data directory cannot
	// serve a CREATE DATABASE (health.go) — stop it and replace it, in use or
	// not: it fails every caller.
	sharedBroken
)

// probeShared decides what an attaching process may do with the shared server
// recorded at port/url.
//
// A server that does not answer a probe is NOT therefore dead. On a loaded
// machine, or one that just woke from sleep, a postmaster can take seconds to
// fork a backend. The probe used to allow 2s to connect and 3s to ping and
// then `pg_ctl stop -m fast` the server — under every other process using it,
// which saw 57P01 "terminating connection due to administrator command" and
// 57P03 "the database system is shutting down" mid-test. So: a dead postmaster
// is gone at once; a live one is retried for sharedPatience (on the monotonic
// clock, so a wall-clock jump neither shortens nor stretches the wait); and if
// it still does not answer, it is stopped only when nobody else uses it.
//
// A server that answers is not therefore usable either: one whose data
// directory was deleted under it answers a ping and fails every CREATE
// DATABASE. That is sharedBroken, decided from the files before any ping.
func probeShared(st poolState, inUse bool) sharedVerdict {
	pidFile := filepath.Join(runtimeDir(st.Port), "data", "postmaster.pid")
	start := time.Now()
	for {
		pid, alive := postmaster(pidFile)
		if !alive {
			pid = 0
		}
		if sharedBrokenReason(st, pid) != "" {
			return sharedBroken
		}
		if !alive {
			return sharedGone
		}
		if CanReach(st.URL) {
			return sharedReady
		}
		if time.Since(start) >= sharedPatience {
			if inUse {
				return sharedBusy
			}
			return sharedGone
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// stopSharedByPort tears down the embedded server whose data dir is
// runtimeDir(port): a graceful pg_ctl fast-stop (which RELEASES the SysV
// semaphore sets and shared-memory segment as part of an orderly shutdown —
// a SIGKILL would leak them), the shm reclaim as a backstop, and removal of
// the whole runtime dir. Every step is best-effort; a partial teardown is
// still swept later by reapStaleInstances.
//
// Teardown is by DATA DIR, not an *EmbeddedPostgres handle, precisely so the
// last user can stop the server even when it only REUSED one another process
// booted (the pg_ctl binary the booting process extracted lives beside the
// data dir in the same runtime dir).
//
// It signals only a postmaster it has identified as this runtime dir's own
// postgres (isPostgresOf). The pid in postmaster.pid is a pid of the
// namespace the postmaster started in; from another pid namespace it names
// nothing, or an unrelated process, and pg_ctl would signal whatever it
// names. And it removes the runtime dir only once no postmaster runs from it
// (serverRunning): deleting a live server's data directory leaves a
// postmaster that answers pings and fails every CREATE DATABASE (health.go).
// It reports whether the server is gone; when it is not (a postmaster this
// process cannot see or stop), everything is left as it was.
func stopSharedByPort(port uint32) (gone bool) {
	if port == 0 {
		return true
	}
	dir := runtimeDir(port)
	dataPath := filepath.Join(dir, "data")
	pidFile := filepath.Join(dataPath, "postmaster.pid")
	pgCtl := filepath.Join(dir, "bin", pgCtlBinary)

	if pid, alive := postmaster(pidFile); alive && isPostgresOf(pid, dir) {
		stopped := false
		if _, statErr := os.Stat(pgCtl); statErr == nil {
			// -m fast: roll back open transactions and shut down promptly.
			// These databases are ephemeral, so a smart (wait-for-clients)
			// shutdown is unnecessary and could hang on a leaked backend.
			if runErr := exec.Command(pgCtl, "stop", "-w", "-D", dataPath, "-m", "fast").Run(); runErr == nil {
				stopped = true
			}
		}
		if !stopped {
			// Fallback when pg_ctl is missing or failed: SIGINT (fast
			// shutdown), grace, then SIGKILL. SIGINT still lets the
			// postmaster release its IPC on the way out; SIGKILL is the
			// last resort that does not.
			if gracefulStop(pid) {
				for i := 0; i < 30 && processAlive(pid); i++ {
					time.Sleep(100 * time.Millisecond)
				}
			}
			if processAlive(pid) {
				forceKill(pid)
				for i := 0; i < 20 && processAlive(pid); i++ {
					time.Sleep(100 * time.Millisecond)
				}
			}
		}
	}
	if serverRunning(dir) {
		return false
	}
	// Reclaim the SysV shm interlock segment while the pid file that names it
	// still exists. No-op after a clean stop (postgres already removed both
	// the segment and the pid file); load-bearing only on the SIGKILL path.
	reclaimShmSegment(pidFile)
	_ = os.RemoveAll(dir)
	return true
}

// retainEmbedded keeps a booted handle reachable for the process lifetime.
func retainEmbedded(ep *Embedded) {
	if ep != nil {
		retained = append(retained, ep)
	}
}

// --- pool state file I/O (all callers hold the pool lock) ---

func poolRoot() string      { return instanceRoot() }
func poolStatePath() string { return poolStatePathOf(poolScope()) }
func poolLockPath() string  { return filepath.Join(poolRoot(), poolScopesLockFile) }

// poolStatePathOf is scope's pool state file.
func poolStatePathOf(scope string) string {
	return filepath.Join(poolRoot(), poolScopeStatePrefix+scope+".json")
}

func readPoolState() poolState { return readPoolStateOf(poolScope()) }

func readPoolStateOf(scope string) poolState {
	var st poolState
	b, err := os.ReadFile(poolStatePathOf(scope))
	if err != nil {
		return st
	}
	_ = json.Unmarshal(b, &st)
	return st
}

// readLegacyPoolState reads the pool state an older forge keeps
// (legacyPoolStateFile).
func readLegacyPoolState() legacyPoolState {
	var st legacyPoolState
	b, err := os.ReadFile(filepath.Join(poolRoot(), legacyPoolStateFile))
	if err != nil {
		return st
	}
	_ = json.Unmarshal(b, &st)
	return st
}

// publishBootedPool releases the boot claim only after atomic registration or
// owned cleanup. stop is injected so failure ordering has a deterministic proof.
func publishBootedPool(st poolState, claim *bootClaim, stop func()) error {
	defer claim.release()
	if err := writePoolState(st); err != nil {
		stop()
		return err
	}
	return nil
}

func writePoolState(st poolState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := poolStatePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, poolStatePath())
}

// lockPool takes an exclusive advisory lock on the pool lockfile. The lock is
// tied to the open file handle, so the OS releases it automatically if the
// holder dies — a crashed lock-holder never wedges the pool.
//
// The locking primitive itself is per-platform (flock(2) on unix,
// LockFileEx on Windows); see pool_lock_unix.go / pool_lock_windows.go.
func lockPool() (unlock func(), err error) {
	f, err := os.OpenFile(poolLockPath(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	unlockFile, err := lockFileExclusive(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		unlockFile()
		_ = f.Close()
	}, nil
}

// prunePIDs drops PIDs whose process is no longer alive. A signal-0 probe
// returning EPERM means the process exists but is owned by another user —
// still alive — so it is kept. Only the legacy pool's users are pids
// (portsInUse); within one pid namespace, as an older forge assumes.
func prunePIDs(pids []int) []int {
	var live []int
	for _, p := range pids {
		if p <= 0 {
			continue
		}
		if processAlive(p) {
			live = append(live, p)
		}
	}
	return live
}
