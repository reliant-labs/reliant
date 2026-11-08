package cli

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeFacts is the map-backed procFacts used by the ownership-resolution
// unit tests. A pid absent from env is "unreadable" (environ ok=false),
// modelling a dead pid / SIP-redacted system binary. A pid absent from ppid
// has an unknown parent (parent ok=false), ending the ancestry walk. A pid
// absent from args has unreadable argv (argv ok=false), modelling a platform
// with no argv source — which duplicate attribution must report as an
// explicit unknown rather than a guess.
type fakeFacts struct {
	env  map[int][]string
	ppid map[int]int
	args map[int][]string
}

func (f fakeFacts) environ(pid int) ([]string, bool) {
	e, ok := f.env[pid]
	return e, ok
}

func (f fakeFacts) parent(pid int) (int, bool) {
	p, ok := f.ppid[pid]
	return p, ok
}

func (f fakeFacts) argv(pid int) ([]string, bool) {
	a, ok := f.args[pid]
	return a, ok
}

// testProj is the reclaiming project id the ownership unit tests pass to the
// resolvers; marker() stamps it so a marked process reads as OURS. markerProj
// stamps an ARBITRARY project id for the cross-project isolation cases, and
// legacyMarker stamps only the env (no project) to model a pre-fix stack.
const testProj = "projA"

func marker(env, svc string) []string {
	return markerProj(testProj, env, svc)
}

func markerProj(proj, env, svc string) []string {
	return []string{"PATH=/usr/bin", forgeUpProjectVar + "=" + proj, forgeUpEnvVar + "=" + env, forgeUpServiceVar + "=" + svc}
}

// legacyMarker models a process spawned by a PRE-FIX forge: it carries the env
// marker but NO project marker — the live-reliant-stack case Part B must never
// reap. markerMatches treats it as foreign for any reclaiming project.
func legacyMarker(env, svc string) []string {
	return []string{"PATH=/usr/bin", forgeUpEnvVar + "=" + env, forgeUpServiceVar + "=" + svc}
}

func TestForgeOwnerOfPID(t *testing.T) {
	f := fakeFacts{
		env: map[int][]string{
			100: marker("dev", "api"),     // direct holder carries the marker
			201: marker("dev", "web"),     // ancestor of unreadable 200
			300: {"PATH=/usr/bin"},        // no marker anywhere
			400: marker("staging", "api"), // wrong env
			// 200 and 500 deliberately absent → unreadable
		},
		ppid: map[int]int{
			100: 1,
			200: 201, 201: 1, // 200's env unreadable; marked parent 201
			300: 1,
			400: 1,
			500: 1, // unreadable holder whose only ancestor is init
			// cycle: 600 <-> 601, no markers
			600: 601, 601: 600,
		},
	}

	t.Run("direct marker match", func(t *testing.T) {
		svc, owned := forgeOwnerOfPID(100, testProj, "dev", f)
		if !owned || svc != "api" {
			t.Fatalf("got (%q, %v); want (api, true)", svc, owned)
		}
	})
	t.Run("ancestor marker match (grandchild reparent case)", func(t *testing.T) {
		svc, owned := forgeOwnerOfPID(200, testProj, "dev", f)
		if !owned || svc != "web" {
			t.Fatalf("got (%q, %v); want (web, true)", svc, owned)
		}
	})
	t.Run("no marker is foreign", func(t *testing.T) {
		if _, owned := forgeOwnerOfPID(300, testProj, "dev", f); owned {
			t.Fatal("unmarked process must be foreign")
		}
	})
	t.Run("wrong-env marker is foreign", func(t *testing.T) {
		if _, owned := forgeOwnerOfPID(400, testProj, "dev", f); owned {
			t.Fatal("staging-marked process must be foreign for env=dev")
		}
	})
	t.Run("unreadable holder to init is foreign", func(t *testing.T) {
		if _, owned := forgeOwnerOfPID(500, testProj, "dev", f); owned {
			t.Fatal("unreadable holder whose only ancestor is init must be foreign")
		}
	})
	t.Run("ppid cycle terminates and is foreign", func(t *testing.T) {
		if _, owned := forgeOwnerOfPID(600, testProj, "dev", f); owned {
			t.Fatal("cycle without markers must be foreign (and must terminate)")
		}
	})
	t.Run("init pid is never inspected", func(t *testing.T) {
		// Even if pid 1 somehow carried the marker, the walk stops before it.
		f2 := fakeFacts{env: map[int][]string{1: marker("dev", "root")}, ppid: map[int]int{}}
		if _, owned := forgeOwnerOfPID(1, testProj, "dev", f2); owned {
			t.Fatal("pid 1 must never be classified as forge-owned")
		}
	})
}

func TestClassifyPortConflicts(t *testing.T) {
	f := fakeFacts{
		env: map[int][]string{
			100: marker("dev", "admin-server"), // holds :8090, ours
			200: {"PATH=/usr/bin"},             // holds :3000, foreign
		},
		ppid: map[int]int{100: 1, 200: 1},
	}
	resolve := func(port int) int {
		switch port {
		case 8090:
			return 100
		case 3000:
			return 200
		default:
			return 0 // :9999 unresolvable (e.g. lsof missing)
		}
	}
	conflicts := []portConflict{
		{name: "admin-server", port: 8090},
		{name: "reliant-web", port: 3000},
		{name: "ghost", port: 9999},
	}
	owned, foreign := classifyPortConflicts(testProj, "dev", conflicts, resolve, f)
	if len(owned) != 1 || owned[0].name != "admin-server" {
		t.Fatalf("owned = %v; want [admin-server]", names(owned))
	}
	// Foreign includes both the unmarked holder AND the unresolvable port —
	// an unidentifiable holder is conservatively foreign, never reclaimed.
	if got := names(foreign); len(got) != 2 || got[0] != "reliant-web" || got[1] != "ghost" {
		t.Fatalf("foreign = %v; want [reliant-web ghost]", got)
	}
}

// TestClassify_DeadLedgerPidLiveMarkedOrphan pins the headline case: the
// ledger's tracked pid is dead (air re-exec'd under a new pid), but the LIVE
// process squatting the port carries our marker — it must classify as ours,
// not foreign, so the user is told forge still holds the port instead of being
// misdirected at lsof+kill.
func TestClassify_DeadLedgerPidLiveMarkedOrphan(t *testing.T) {
	const liveOrphan = 4242 // not the (dead) ledger pid
	f := fakeFacts{
		env:  map[int][]string{liveOrphan: marker("dev", "admin-server")},
		ppid: map[int]int{liveOrphan: 1},
	}
	resolve := func(int) int { return liveOrphan }
	owned, foreign := classifyPortConflicts(testProj, "dev",
		[]portConflict{{name: "admin-server", port: 8090}}, resolve, f)
	if len(owned) != 1 || len(foreign) != 0 {
		t.Fatalf("owned=%v foreign=%v; want the live marked orphan classified as ours",
			names(owned), names(foreign))
	}
}

func TestMarkedOrphanRoots_SweepKeepsSubtreeRoots(t *testing.T) {
	// Table sweep: 900(dev) is a child of 901(dev); 902 is a different env.
	// Only 901 (the subtree root for env=dev) is returned; 900 is dropped
	// (its marked parent is also killed) and 902 is excluded (wrong env).
	f := fakeFacts{
		env: map[int][]string{
			900: marker("dev", "next"),
			901: marker("dev", "npm"),
			902: marker("staging", "api"),
		},
		ppid: map[int]int{900: 901, 901: 1, 902: 1},
	}
	roots := markedOrphanRoots(testProj, "dev", []int{900, 901, 902}, f)
	sort.Ints(roots)
	if len(roots) != 1 || roots[0] != 901 {
		t.Fatalf("roots = %v; want [901]", roots)
	}
}

func TestStampForgeOwnership_ForcesAndDedups(t *testing.T) {
	// Seed a STALE env marker AND a stale project marker (as if inherited from
	// a nested / different-project forge-up) — both must be overwritten with
	// this stamp's values, each appearing exactly once.
	cmd := &exec.Cmd{Env: []string{"PATH=/usr/bin", forgeUpEnvVar + "=stale", forgeUpProjectVar + "=stale-proj"}}
	stampForgeOwnership(cmd, testProj, "dev", "admin-server")

	got := map[string]int{} // key -> count
	var envVal, svcVal, projVal string
	for _, kv := range cmd.Env {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			got[kv[:i]]++
			switch kv[:i] {
			case forgeUpEnvVar:
				envVal = kv[i+1:]
			case forgeUpServiceVar:
				svcVal = kv[i+1:]
			case forgeUpProjectVar:
				projVal = kv[i+1:]
			}
		}
	}
	if got[forgeUpEnvVar] != 1 {
		t.Errorf("FORGE_UP_ENV must appear exactly once (dedup), got %d: %v", got[forgeUpEnvVar], cmd.Env)
	}
	if got[forgeUpProjectVar] != 1 {
		t.Errorf("FORGE_UP_PROJECT must appear exactly once (dedup), got %d: %v", got[forgeUpProjectVar], cmd.Env)
	}
	if envVal != "dev" {
		t.Errorf("FORGE_UP_ENV = %q; want dev (stale value overwritten)", envVal)
	}
	if projVal != testProj {
		t.Errorf("FORGE_UP_PROJECT = %q; want %q (stale value overwritten)", projVal, testProj)
	}
	if svcVal != "admin-server" {
		t.Errorf("FORGE_UP_SERVICE = %q; want admin-server", svcVal)
	}
	if got["PATH"] != 1 {
		t.Errorf("PATH must survive stamping: %v", cmd.Env)
	}
}

func TestMarkerFields(t *testing.T) {
	env, svc, proj := markerFields([]string{"A=1", forgeUpEnvVar + "=dev", "B=2", forgeUpServiceVar + "=web", forgeUpProjectVar + "=projX"})
	if env != "dev" || svc != "web" || proj != "projX" {
		t.Fatalf("got (%q,%q,%q); want (dev,web,projX)", env, svc, proj)
	}
	// Last duplicate wins (exec env semantics).
	env, _, _ = markerFields([]string{forgeUpEnvVar + "=old", forgeUpEnvVar + "=new"})
	if env != "new" {
		t.Fatalf("duplicate: got %q; want new", env)
	}
	if e, s, p := markerFields([]string{"PATH=/x"}); e != "" || s != "" || p != "" {
		t.Fatalf("no marker: got (%q,%q,%q); want empty", e, s, p)
	}
}

// TestMarkerMatches_ProjectScoping pins the heart of Part B: ownership requires
// BOTH the env AND the project marker to match, and a MISSING project marker
// (a pre-fix stack) is always foreign. This is the invariant that keeps the
// pre-flight reclaim / `env down` in one project off another project's — and
// off the live, pre-fix reliant "dev" stack's — processes.
func TestMarkerMatches_ProjectScoping(t *testing.T) {
	t.Run("same project + env matches", func(t *testing.T) {
		if svc, ok := markerMatches(markerProj("projA", "dev", "api"), "projA", "dev"); !ok || svc != "api" {
			t.Fatalf("got (%q,%v); want (api,true)", svc, ok)
		}
	})
	t.Run("different project is foreign (same env name)", func(t *testing.T) {
		if _, ok := markerMatches(markerProj("projB", "dev", "api"), "projA", "dev"); ok {
			t.Fatal("a DIFFERENT project's process on the same env must be foreign")
		}
	})
	t.Run("missing project marker is foreign (legacy/pre-fix stack)", func(t *testing.T) {
		if _, ok := markerMatches(legacyMarker("dev", "api"), "projA", "dev"); ok {
			t.Fatal("a process with no FORGE_UP_PROJECT (pre-fix stack) must be foreign")
		}
	})
	t.Run("same project but wrong env is foreign", func(t *testing.T) {
		if _, ok := markerMatches(markerProj("projA", "staging", "api"), "projA", "dev"); ok {
			t.Fatal("same project on a different env must be foreign")
		}
	})
	t.Run("empty reclaiming project id matches nothing (fails closed)", func(t *testing.T) {
		if _, ok := markerMatches(markerProj("projA", "dev", "api"), "", "dev"); ok {
			t.Fatal("an empty reclaiming project id must never match (fail closed)")
		}
	})
}

// TestReclaim_ExcludesOtherProjectsAndLegacy proves the sweep + port-conflict
// classifiers never select another project's process, nor a pre-fix stack that
// carries only the env marker — the two ways the incident could recur.
func TestReclaim_ExcludesOtherProjectsAndLegacy(t *testing.T) {
	// 100: OURS (projA/dev). 200: another project (projB/dev). 300: a pre-fix
	// stack (env marker only). All three share env=dev.
	f := fakeFacts{
		env: map[int][]string{
			100: markerProj("projA", "dev", "api"),
			200: markerProj("projB", "dev", "api"),
			300: legacyMarker("dev", "api"),
		},
		ppid: map[int]int{100: 1, 200: 1, 300: 1},
	}
	roots := markedOrphanRoots("projA", "dev", []int{100, 200, 300}, f)
	if len(roots) != 1 || roots[0] != 100 {
		t.Fatalf("sweep roots = %v; want [100] (projB and the legacy stack must be excluded)", roots)
	}

	resolve := func(port int) int {
		switch port {
		case 8001:
			return 100
		case 8002:
			return 200
		case 8003:
			return 300
		default:
			return 0
		}
	}
	owned, foreign := classifyPortConflicts("projA", "dev", []portConflict{
		{name: "ours", port: 8001},
		{name: "other-project", port: 8002},
		{name: "legacy", port: 8003},
	}, resolve, f)
	if len(owned) != 1 || owned[0].name != "ours" {
		t.Fatalf("owned = %v; want [ours] only", names(owned))
	}
	if got := names(foreign); len(got) != 2 || got[0] != "other-project" || got[1] != "legacy" {
		t.Fatalf("foreign = %v; want [other-project legacy]", got)
	}
}

// TestHelperBlockProcess is the re-exec target for the real-process
// integration test below: it blocks (bounded, so a leak self-reaps) only
// when the harness env flag is set, and is an inert no-op under normal
// `go test` runs.
func TestHelperBlockProcess(t *testing.T) {
	if os.Getenv("FORGE_RECLAIM_HELPER") != "1" {
		return
	}
	time.Sleep(30 * time.Second)
}

// TestForgeOwnership_RealProcess exercises the REAL osProcFacts against a
// live child on the actual platform — the load-bearing, platform-specific
// half (sysctl KERN_PROCARGS2 on darwin, /proc on linux) that the mock
// tests can't cover. It spawns this test binary re-exec'd with the marker
// in its env and asserts osProcFacts reads it back and forgeOwnerOfPID
// classifies it as ours; a child WITHOUT the marker classifies as foreign.
func TestForgeOwnership_RealProcess(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("process-env inspection is implemented for darwin/linux only")
	}
	env := fmt.Sprintf("itest-%d", os.Getpid())
	proj := fmt.Sprintf("proj-%d", os.Getpid())

	spawn := func(withMarker bool) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=TestHelperBlockProcess")
		e := append(os.Environ(), "FORGE_RECLAIM_HELPER=1")
		if withMarker {
			e = append(e, forgeUpProjectVar+"="+proj, forgeUpEnvVar+"="+env, forgeUpServiceVar+"=svc")
		}
		cmd.Env = e
		startInOwnProcessGroup(cmd)
		if err := cmd.Start(); err != nil {
			t.Fatalf("start helper: %v", err)
		}
		return cmd
	}

	marked := spawn(true)
	unmarked := spawn(false)
	t.Cleanup(func() {
		for _, c := range []*exec.Cmd{marked, unmarked} {
			if c.Process != nil {
				_ = killProcessTree(c.Process.Pid, syscall.SIGKILL)
				_ = c.Wait()
			}
		}
	})

	// Give the children a moment to exec into the test binary so their env
	// is in place for inspection.
	time.Sleep(300 * time.Millisecond)

	facts := newOSProcFacts()

	// The marked child's env must be readable and identify it as ours.
	gotEnv, ok := facts.environ(marked.Process.Pid)
	if !ok {
		t.Fatalf("could not read env of our own child pid %d — the marker mechanism is non-functional on %s", marked.Process.Pid, runtime.GOOS)
	}
	if name := markerEnvName(gotEnv); name != env {
		t.Fatalf("marked child FORGE_UP_ENV = %q; want %q (env read: %d vars)", name, env, len(gotEnv))
	}
	if _, owned := forgeOwnerOfPID(marked.Process.Pid, proj, env, facts); !owned {
		t.Fatalf("marked child pid %d classified as foreign; want ours", marked.Process.Pid)
	}
	// A DIFFERENT project id must read the SAME live marked child as foreign —
	// the real-process proof that ownership is keyed per-project, not per-env.
	if _, owned := forgeOwnerOfPID(marked.Process.Pid, "some-other-project", env, facts); owned {
		t.Fatalf("marked child pid %d classified as ours for a different project; want foreign", marked.Process.Pid)
	}

	// The unmarked child must classify as foreign for our env.
	if _, owned := forgeOwnerOfPID(unmarked.Process.Pid, proj, env, facts); owned {
		t.Fatalf("unmarked child pid %d classified as ours; want foreign", unmarked.Process.Pid)
	}
}

func names(cs []portConflict) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.name
	}
	return out
}
