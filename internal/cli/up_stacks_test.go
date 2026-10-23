package cli

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These tests spawn REAL marked processes and then reclaim them, because that
// is the only way to exercise the thing that broke: the decision to stop a
// predecessor reads the live process table, and every mock of it agreed with
// the code while the machine accumulated 38 orphans.
//
// Every stack a test creates is stamped with a project id derived from this
// test process's pid, so a sweep can never select a real forge stack running on
// the developer's machine. For the same reason no test calls runUpStopAll (it
// would tear down the developer's own stacks); the loop it runs is exercised
// through stopDiscoveredStacks with a filtered input.

// blockedProc is a live helper process standing in for one child of a running
// stack, with the exit signal a test can wait on. Reaping matters: an unreaped
// child stays a zombie, and a zombie still answers kill(pid, 0).
type blockedProc struct {
	cmd    *exec.Cmd
	exited chan struct{}
}

func (b *blockedProc) pid() int { return b.cmd.Process.Pid }

// waitExit reports whether the helper exited within the budget.
func (b *blockedProc) waitExit(d time.Duration) bool {
	select {
	case <-b.exited:
		return true
	case <-time.After(d):
		return false
	}
}

func (b *blockedProc) alive() bool {
	select {
	case <-b.exited:
		return false
	default:
		return true
	}
}

// spawnMarked starts a helper process carrying the (projectID, env) ownership
// markers — the same stamp stampForgeOwnership puts on every child of a real
// `forge env up`. An empty projectID stamps only the env marker: a stack from a
// forge that predates project scoping, which nothing may ever reclaim.
func spawnMarked(t *testing.T, projectID, env, service string) *blockedProc {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperBlockProcess")
	e := append(os.Environ(), "FORGE_RECLAIM_HELPER=1", forgeUpEnvVar+"="+env, forgeUpServiceVar+"="+service)
	if projectID != "" {
		e = append(e, forgeUpProjectVar+"="+projectID)
	}
	cmd.Env = e
	startInOwnProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	bp := &blockedProc{cmd: cmd, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(bp.exited)
	}()
	t.Cleanup(func() {
		if bp.alive() {
			_ = killProcessTree(bp.pid(), syscall.SIGKILL)
			bp.waitExit(5 * time.Second)
		}
	})
	// Let the child exec into the test binary so its environment is in place
	// for inspection.
	waitForMarker(t, bp.pid(), env)
	return bp
}

// waitForMarker blocks until the helper's environment is readable and carries
// the env marker — the fact every ownership decision reads.
func waitForMarker(t *testing.T, pid int, env string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if environ, ok := newOSProcFacts().environ(pid); ok && markerEnvName(environ) == env {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("helper pid %d never showed its ownership marker — the marker mechanism is non-functional on %s", pid, runtime.GOOS)
}

// testStack sets up an isolated project on disk (HOME redirected so the stack
// records land in a temp cache) and returns its directory and project id.
func testStack(t *testing.T) (dir, projectID, env string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir = t.TempDir()
	writeForgeYAML(t, dir, "name: demo\nmodule_path: github.com/example/demo\nversion: \"0.1.0\"\n")
	return dir, projectIDForDir(dir), fmt.Sprintf("itest-%d", os.Getpid())
}

// freePort returns a port nothing is listening on — the ephemeral-dev-port
// situation, in which a second stack conflicts with nothing at all.
func freePort(t *testing.T) int {
	t.Helper()
	p, err := freeTCPPort()
	if err != nil {
		t.Fatalf("allocate free port: %v", err)
	}
	return p
}

// entitiesOnPort renders one host service bound to port — the shape
// resolveEphemeralHostPorts produces for a scaffolded backend.
func entitiesOnPort(port int) *KCLEntities {
	return &KCLEntities{Workloads: []WorkloadEntity{hostWL("api", withListenPorts(port))}}
}

// TestUpPreflight_StopsThePredecessorOnFreePorts is the defect, pinned.
//
// A stack is already running for this (project, env). The ports the new run
// will bind are FREE — which is the normal case since dev ports became
// kernel-assigned, and which is precisely why nothing conflicts. The pre-flight
// must stop the predecessor anyway: "is my stack already running" is a question
// about ownership, not about ports, and answering it inside a port-conflict
// branch made the reclaim unreachable on every `forge env up`. Eight rounds left
// 38 orphaned processes, 7.5 GB resident, on 15 ports nothing could name.
func TestUpPreflight_StopsThePredecessorOnFreePorts(t *testing.T) {
	requireProcInspection(t)
	dir, projectID, env := testStack(t)

	predecessor := spawnMarked(t, projectID, env, "api")
	// A second project's stack on the SAME env name, and a pre-scoping stack
	// carrying only the env marker: neither may be touched, whatever happens.
	otherProject := spawnMarked(t, projectIDForDir(t.TempDir()), env, "api")
	legacy := spawnMarked(t, "", env, "api")

	// The ledger the predecessor's run wrote, so the record path is exercised
	// too — but the decision must not depend on it.
	reg := newProcRegistry(projectID, dir, env)
	reg.processes = []*managedProcess{{name: "api", pid: predecessor.pid(), cmd: &exec.Cmd{}}}
	reg.persist()

	port := freePort(t)
	if portInUse(port) {
		t.Fatalf("port %d is in use; the test needs a free one", port)
	}
	if err := upPreflight(projectID, env, entitiesOnPort(port), nil, true); err != nil {
		t.Fatalf("upPreflight on free ports: %v", err)
	}

	if !predecessor.waitExit(15 * time.Second) {
		t.Fatalf("the predecessor stack (pid %d) survived `up` on free ports — "+
			"a second stack now runs alongside it, which is how the orphans accumulated",
			predecessor.pid())
	}
	if !otherProject.alive() {
		t.Error("another project's stack on the same env name was reclaimed — ownership must be keyed on (project, env)")
	}
	if !legacy.alive() {
		t.Error("a process without a project marker was reclaimed — an unprovable owner must never be signalled")
	}
	// The records the stopped stack left behind are gone with it.
	statePath, err := upStatePath(projectID, env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Errorf("ledger %s outlived the stack it described (stat err = %v)", statePath, err)
	}
}

// TestUpPreflight_RefusesAForeignPortHolder pins the other half: what the port
// probe is actually for. A port this run needs is held by a process forge did
// not start — that is an error, and the process is never killed.
func TestUpPreflight_RefusesAForeignPortHolder(t *testing.T) {
	requireProcInspection(t)
	_, projectID, env := testStack(t)

	listener, lerr := net.Listen("tcp", "127.0.0.1:0")
	if lerr != nil {
		t.Fatalf("listen: %v", lerr)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	perr := upPreflight(projectID, env, entitiesOnPort(port), nil, true)
	if perr == nil {
		t.Fatal("a port held by a process forge does not manage must refuse the run")
	}
	if !strings.Contains(perr.Error(), "doesn't manage") {
		t.Errorf("error should name the foreign holder:\n%s", perr)
	}
}

// TestEnvDown_RefusesWhenItCannotTellWhichProject pins Fix 2. Run from a
// directory with no forge.yaml, `forge env down` used to hash "." — a project
// id no stack was ever recorded under — find nothing, print "no tracked stack
// for env=dev" and exit 0. "I don't know what project you mean" must never
// render as "nothing was running": that is a guard that cannot fail, and it
// reported success over a machine holding 38 orphaned processes.
func TestEnvDown_RefusesWhenItCannotTellWhichProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir()) // no forge.yaml here or above

	err := runUpStop("dev")
	if err == nil {
		t.Fatal("`forge env down dev` outside a project reported success; it cannot know whose stack to stop")
	}
	for _, want := range []string{"forge.yaml", "forge env down --all"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must mention %q so the user can act:\n%s", want, err)
		}
	}

	// Identifying the project is insufficient when its environment cannot be
	// rendered: shutdown of declared host infrastructure remains unverified.
	dir := t.TempDir()
	writeForgeYAML(t, dir, "name: demo\nmodule_path: github.com/example/demo\nversion: \"0.1.0\"\n")
	t.Chdir(dir)
	if err := runUpStop("dev"); err == nil {
		t.Fatal("an environment with no renderable KCL reported complete shutdown")
	}
}

// TestStackOutlivingItsProjectDirIsStillReachable pins Fix 3. The cache keys
// every record on sha256(project dir) and recorded nothing else, and a hash
// cannot be inverted: once the directory was deleted, no forge verb could name
// the stack, let alone stop it. That is the state this machine was found in.
func TestStackOutlivingItsProjectDirIsStillReachable(t *testing.T) {
	requireProcInspection(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(t.TempDir(), "round-3")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeForgeYAML(t, dir, "name: demo\nmodule_path: github.com/example/demo\nversion: \"0.1.0\"\n")
	projectID := projectIDForDir(dir)
	// Resolved while the directory still exists: once it is deleted the path
	// can no longer be de-symlinked, which is exactly why the record has to be
	// written at launch and not derived later.
	wantDir := canonicalProjectDir(dir)
	env := fmt.Sprintf("itest-%d", os.Getpid())

	orphan := spawnMarked(t, projectID, env, "api")
	reg := newProcRegistry(projectID, dir, env)
	reg.processes = []*managedProcess{{name: "api", pid: orphan.pid(), cmd: &exec.Cmd{}}}
	reg.persist()

	// The dogfood loop's next round deletes the project tree.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	// The per-project verb structurally cannot reach it any more: there is no
	// forge.yaml left to derive the project id from.
	t.Chdir(t.TempDir())
	if err := runUpStop(env); err == nil {
		t.Error("`forge env down` claimed to have handled a stack whose project directory is gone")
	}

	// Machine-scoped enumeration finds it, names the directory it came from,
	// and says that directory is gone.
	var found *runningStack
	for _, s := range discoverRunningStacks() {
		if s.projectID == projectID && s.env == env {
			cp := s
			found = &cp
		}
	}
	if found == nil {
		t.Fatalf("a live stack for a deleted project was not enumerated; nothing can reach it")
	}
	if found.dir != wantDir {
		t.Errorf("enumerated dir = %q, want the recorded project path %q", found.dir, wantDir)
	}
	if found.dirExists {
		t.Error("the project directory was deleted; the report must say so")
	}
	if !strings.Contains(found.label(), "directory is gone") {
		t.Errorf("label %q should tell the user why `cd && forge env down` is not the fix", found.label())
	}

	// And the teardown `forge env down --all` runs reaches it.
	if res, err := stopDiscoveredStacks([]runningStack{*found}); res.stopped != 1 || len(res.skipped) != 0 || err != nil {
		t.Errorf("stopped %d trees (skipped %v, err %v), want 1", res.stopped, res.skipped, err)
	}
	if !orphan.waitExit(15 * time.Second) {
		t.Fatalf("the orphan (pid %d) survived; it is unreachable forever", orphan.pid())
	}
	// The record is reaped with the stack — the cache must not grow one
	// directory per project ever run.
	projDir, err := upProjectCacheDir(projectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(projDir); !os.IsNotExist(err) {
		t.Errorf("stack records outlived the stack: %s (stat err = %v)", projDir, err)
	}
}

// TestDiscoverMarkedStacks_GroupsByProjectAndEnv is the pure core of
// enumeration: group live processes by the (project, env) their markers name,
// WITHOUT being told which stack to look for. That inversion is what makes a
// stack findable when its project directory — and so its project id — cannot be
// derived from anything on disk.
func TestDiscoverMarkedStacks_GroupsByProjectAndEnv(t *testing.T) {
	f := fakeFacts{
		env: map[int][]string{
			100: markerProj("projA", "dev", "api"),
			101: markerProj("projA", "dev", "web"),
			102: markerProj("projA", "staging", "api"),
			103: markerProj("projB", "dev", "api"),
			104: legacyMarker("dev", "api"), // no project marker: never grouped
			105: {"PATH=/usr/bin"},          // not forge's: never grouped
		},
		ppid: map[int]int{100: 1, 101: 1, 102: 1, 103: 1, 104: 1, 105: 1},
	}
	got := discoverMarkedStacks([]int{100, 101, 102, 103, 104, 105, 1}, f)
	if len(got) != 3 {
		t.Fatalf("discovered %d stacks (%v), want 3: projA/dev, projA/staging, projB/dev", len(got), got)
	}
	if pids := got[stackKey{"projA", "dev"}]; len(pids) != 2 {
		t.Errorf("projA/dev has %v, want both 100 and 101", pids)
	}
	if _, ok := got[stackKey{"", "dev"}]; ok {
		t.Error("a process with no project marker was grouped into a stack; it can never be proven ours")
	}
}

// TestStackTeardownRoots_LedgerIsAFallbackNotAnAuthority pins the rule that
// decides what a teardown signals. The live markers are the authority; the
// recorded pids are consulted only where that authority cannot answer.
//
// The hazard being closed is pid reuse: the cache holds ledgers for projects
// deleted weeks ago, and `forge env down --all` walks all of them. Signalling a
// remembered pid whose process is now a stranger's is how a teardown becomes a
// machine-wide hazard.
func TestStackTeardownRoots_LedgerIsAFallbackNotAnAuthority(t *testing.T) {
	f := fakeFacts{
		env: map[int][]string{
			100: marker("dev", "api"),            // ours, found by the sweep
			101: marker("dev", "api"),            // ours, child of 100
			200: markerProj("projB", "dev", "x"), // the pid was recycled into another project's process
			400: marker("dev", "gone"),           // ours, but dead
			// 300 is absent: its environment cannot be read at all.
		},
		ppid: map[int]int{100: 1, 101: 100, 200: 1, 300: 1, 400: 1},
	}
	alive := func(pid int) bool { return pid != 400 }
	tracked := []trackedProc{
		{name: "api", pid: 100},
		{name: "recycled", pid: 200},
		{name: "unreadable", pid: 300},
		{name: "dead", pid: 400},
	}
	// The live process table holds 400 no longer — a stale ledger entry is the
	// only place a dead pid survives.
	roots := stackTeardownRoots(testProj, "dev", tracked, []int{100, 101, 200, 300}, alive, f)

	got := map[int]bool{}
	for _, pid := range roots {
		got[pid] = true
	}
	if !got[100] {
		t.Error("the live stack root must be signalled")
	}
	if got[101] {
		t.Error("a child of a signalled root must not be signalled separately")
	}
	if got[200] {
		t.Error("a recorded pid now owned by ANOTHER project was signalled — that is a stranger's process")
	}
	if !got[300] {
		t.Error("a live recorded pid whose environment cannot be read was skipped — on a platform with no process table that is the whole teardown")
	}
	if got[400] {
		t.Error("a dead recorded pid was signalled")
	}
	if len(roots) != 2 {
		t.Errorf("roots = %v, want exactly {100, 300}", roots)
	}
}

// TestProcRegistry_LedgerExistsAsSoonAsAChildStarts pins Fix 4. The ledger used
// to be written once, after the readiness gate — and the readiness gate returns
// an error. Every child started by a run that failed to come up was therefore
// left running with nothing on disk naming it: not reachable by
// `forge env down`, not visible to anything. The record exists from the moment
// the child does, so no early return can lose it.
func TestProcRegistry_LedgerExistsAsSoonAsAChildStarts(t *testing.T) {
	requireProcInspection(t)
	dir, projectID, env := testStack(t)
	t.Chdir(dir)

	reg := newProcRegistry(projectID, dir, env)
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperBlockProcess")
	cmd.Env = append(os.Environ(), "FORGE_RECLAIM_HELPER=1")
	if err := reg.start("api", cmd, true); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := reg.processes[0].pid
	t.Cleanup(func() { _ = killProcessTree(pid, syscall.SIGKILL) })

	// No persist() call here on purpose: this is the state a failed readiness
	// gate returns in.
	statePath, err := upStatePath(projectID, env)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("no ledger after a child started: %v — the child is unreachable by `forge env down`", err)
	}
	if want := fmt.Sprintf("api\t%d", pid); !strings.Contains(string(data), want) {
		t.Errorf("ledger %q does not record %q", data, want)
	}
	// The path record lands with it, so the stack is nameable machine-wide.
	if got := readProjectRecord(projectID); got != canonicalProjectDir(dir) {
		t.Errorf("project record = %q, want %q", got, canonicalProjectDir(dir))
	}
}
