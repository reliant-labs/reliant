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
// records {URL, port, live-user PIDs} in a state file guarded by an advisory
// file lock (flock); every concurrent process reads that file, verifies the
// server is alive, and REUSES it — each creating its own scratch database on
// the one shared server (the NewAtURL pattern). Databases are cheap; the
// server boot (and its IPC footprint) is paid once.
//
// # Teardown strategy — reference counting via the lockfile
//
// The shared server intentionally OUTLIVES a single invocation, so a bare
// defer-Stop in the process that booted it would break the siblings still
// using it. Teardown is therefore reference-counted across processes: the
// state file holds the set of live user PIDs. Each process removes its PID on
// exit (pgtest.Shutdown, wired into the forge CLI); the process that removes
// the LAST live PID performs the actual teardown — a clean `pg_ctl stop`
// (which releases the semaphore sets and shared-memory segment), removal of
// the data directory, and deletion of the state file. Dead PIDs (a process
// that was SIGKILLed before it could detach) are pruned on every attach/
// detach, so a crashed user never pins the server open; and startup's
// reapStaleInstances remains as a belt-and-suspenders backstop for the case
// where the very last user also crashed (age-based, 30 min).
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
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// pool state-file layout, alongside the per-instance runtime dirs under
// instanceRoot() (os.TempDir()/forge-pgtest for the default version). Both
// are FILES (not dirs), so reapStaleInstances — which only considers
// directory entries — skips them.
const (
	poolStateFile = "pool.json"
	poolLockFile  = "pool.lock"
)

// poolState is the on-disk registry of the shared embedded server. Users is
// the set of live process PIDs currently sharing the server; the server is
// torn down when the last one detaches.
type poolState struct {
	URL   string `json:"url"`
	Port  uint32 `json:"port"`
	Users []int  `json:"users"`
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

	st := readPoolState()
	st.Users = prunePIDs(st.Users)
	if st.URL != "" && st.Port != 0 {
		switch probeShared(st.Port, st.URL, len(st.Users) > 0) {
		case sharedReady:
			// Reuse the live shared server.
			st.Users = addPID(st.Users, os.Getpid())
			writePoolState(st)
			return st.URL, st.Port, poolModePooled, nil
		case sharedBusy:
			// Alive, in use, and still not answering: leave it to the
			// processes using it and run on a server of our own.
			return bootStandalone()
		case sharedGone:
		}
	}

	// Absent, dead, or wedged with nobody using it: clean any remnant, then
	// boot fresh.
	if st.Port != 0 {
		stopSharedByPort(st.Port)
	}
	u, p, ep, berr := bootEmbedded()
	if berr != nil {
		_ = os.Remove(poolStatePath()) // let the next attach retry cleanly
		return "", 0, 0, berr
	}
	retainEmbedded(ep)
	writePoolState(poolState{URL: u, Port: p, Users: []int{os.Getpid()}})
	return u, p, poolModePooled, nil
}

// detachEmbedded removes this process from the shared server's live-user set;
// the process that empties the set tears the server down.
func detachEmbedded(port uint32) {
	unlock, err := lockPool()
	if err != nil {
		// Can't coordinate; do NOT stop — other processes may still be using
		// the shared server. Leave it for reapStaleInstances.
		return
	}
	defer unlock()

	st := readPoolState()
	st.Users = removePID(prunePIDs(st.Users), os.Getpid())
	if len(st.Users) == 0 {
		p := st.Port
		if p == 0 {
			p = port
		}
		stopSharedByPort(p)
		_ = os.Remove(poolStatePath())
		return
	}
	writePoolState(st)
}

// bootStandalone boots an embedded server for this process alone and marks
// it as this process's (ownerFile), so no other process's reaper takes it.
func bootStandalone() (url string, port uint32, mode int, err error) {
	u, p, ep, berr := bootEmbedded()
	if berr != nil {
		return "", 0, 0, berr
	}
	retainEmbedded(ep)
	_ = os.WriteFile(filepath.Join(runtimeDir(p), ownerFile), []byte(strconv.Itoa(os.Getpid())), 0o644)
	return u, p, poolModeStandalone, nil
}

// ownerFile, in a standalone server's runtime dir, holds the pid of the one
// process using it. While that process lives, reapStaleInstances leaves the
// server alone however old it is.
const ownerFile = "forge-owner.pid"

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
func probeShared(port uint32, url string, inUse bool) sharedVerdict {
	pidFile := filepath.Join(runtimeDir(port), "data", "postmaster.pid")
	start := time.Now()
	for {
		if _, alive := postmaster(pidFile); !alive {
			return sharedGone
		}
		if CanReach(url) {
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
func stopSharedByPort(port uint32) {
	if port == 0 {
		return
	}
	dir := runtimeDir(port)
	dataPath := filepath.Join(dir, "data")
	pidFile := filepath.Join(dataPath, "postmaster.pid")
	pgCtl := filepath.Join(dir, "bin", pgCtlBinary)

	stopped := false
	if _, statErr := os.Stat(pgCtl); statErr == nil {
		// -m fast: roll back open transactions and shut down promptly. These
		// databases are ephemeral, so a smart (wait-for-clients) shutdown is
		// unnecessary and could hang on a leaked backend.
		if runErr := exec.Command(pgCtl, "stop", "-w", "-D", dataPath, "-m", "fast").Run(); runErr == nil {
			stopped = true
		}
	}
	if !stopped {
		// Fallback when pg_ctl is missing or failed: SIGINT (fast shutdown),
		// grace, then SIGKILL. SIGINT still lets the postmaster release its
		// IPC on the way out; SIGKILL is the last resort that does not.
		if pid, alive := postmaster(pidFile); alive && verifyRecordedPid(pid, pidFile, postgresBinary(dir)) == verdictOurs {
			if gracefulStop(pid) {
				for i := 0; i < 30; i++ {
					if _, a := postmaster(pidFile); !a {
						break
					}
					time.Sleep(100 * time.Millisecond)
				}
			}
			if _, a := postmaster(pidFile); a {
				forceKill(pid)
			}
		}
	}
	// Reclaim the SysV shm interlock segment while the pid file that names it
	// still exists. No-op after a clean stop (postgres already removed both
	// the segment and the pid file); load-bearing only on the SIGKILL path.
	reclaimShmSegment(pidFile)
	_ = os.RemoveAll(dir)
}

// retainEmbedded keeps a booted handle reachable for the process lifetime.
func retainEmbedded(ep *Embedded) {
	if ep != nil {
		retained = append(retained, ep)
	}
}

// --- pool state file I/O (all callers hold the pool lock) ---

func poolRoot() string      { return instanceRoot() }
func poolStatePath() string { return filepath.Join(poolRoot(), poolStateFile) }
func poolLockPath() string  { return filepath.Join(poolRoot(), poolLockFile) }

func readPoolState() poolState {
	var st poolState
	b, err := os.ReadFile(poolStatePath())
	if err != nil {
		return st
	}
	_ = json.Unmarshal(b, &st)
	return st
}

func writePoolState(st poolState) {
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	tmp := poolStatePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, poolStatePath())
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
// still alive — so it is kept.
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

func addPID(pids []int, pid int) []int {
	for _, p := range pids {
		if p == pid {
			return pids
		}
	}
	return append(pids, pid)
}

func removePID(pids []int, pid int) []int {
	out := make([]int, 0, len(pids))
	for _, p := range pids {
		if p != pid {
			out = append(out, p)
		}
	}
	return out
}
