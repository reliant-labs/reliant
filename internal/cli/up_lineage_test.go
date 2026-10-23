package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/procguard"
)

// The lineage guard: no forge stop path may signal the process running it or
// any of that process's ancestors. The case it exists for is an agent session
// hosted by a forge-started server — the server's ownership markers are
// inherited by every shell it spawns, so `forge env down` typed in that shell
// selects the server hosting it.

// hostedSessionTable models that machine:
//
//	1   init
//	└─ 300 agent server        (stack projA/dev, service api)  ← hosts the session
//	   ├─ 400 shell            (inherits projA/dev markers)
//	   │  └─ 500 forge env down (inherits projA/dev markers)   ← this command
//	   └─ 410 another session  (inherits projA/dev markers)
//	└─ 600 web frontend        (stack projA/dev, service frontend:web)
//	   └─ 610 next dev server
//	└─ 800 another project's server (stack projB/dev)
func hostedSessionTable() fakeFacts {
	return fakeFacts{
		env: map[int][]string{
			300: marker("dev", "api"),
			400: marker("dev", "api"),
			500: marker("dev", "api"),
			410: marker("dev", "api"),
			600: marker("dev", "frontend:web"),
			610: marker("dev", "frontend:web"),
			800: markerProj("projB", "dev", "api"),
		},
		ppid: map[int]int{300: 1, 400: 300, 500: 400, 410: 300, 600: 1, 610: 600, 800: 1},
		args: map[int][]string{
			300: {"/usr/local/bin/reliant", "serve", "--port", "3090"},
			600: {"/usr/bin/node", "next", "dev"},
		},
	}
}

// lineageOf walks the injected table from self, exactly as procguard.Self
// walks the real one.
func lineageOf(f fakeFacts, self int) procguard.Lineage {
	return procguard.Walk(self, func(pid int) (procguard.Entry, bool) {
		ppid, ok := f.parent(pid)
		return procguard.Entry{PPID: ppid}, ok
	})
}

func allPIDs(f fakeFacts) []int {
	pids := make([]int, 0, len(f.ppid))
	for pid := range f.ppid {
		pids = append(pids, pid)
	}
	return pids
}

func alwaysAlive(int) bool { return true }

func TestPlanStackStop_SkipsTheTreeHostingThisCommand(t *testing.T) {
	f := hostedSessionTable()
	plan := planStackStop(testProj, "dev", nil, nil, allPIDs(f), alwaysAlive, f, lineageOf(f, 500))

	if len(plan.skipped) != 1 || plan.skipped[0].pid != 300 {
		t.Fatalf("skipped = %+v, want exactly the agent server (300) that hosts this command", plan.skipped)
	}
	if got, want := plan.skipped[0].command, "reliant serve --port 3090"; got != want {
		t.Errorf("skip names the process as %q, want %q", got, want)
	}
	if len(plan.roots) != 1 || plan.roots[0] != 600 {
		t.Errorf("roots = %v, want only the frontend (600): everything that does not host this command is still stopped", plan.roots)
	}
	for _, pid := range []int{400, 410, 500, 610, 800} {
		for _, r := range plan.roots {
			if r == pid {
				t.Errorf("pid %d must not be a root of its own", pid)
			}
		}
	}

	// The same machine seen from a shell OUTSIDE the stack (pid 999, child of
	// init): nothing is in its lineage, so the whole stack is stopped.
	f.ppid[999] = 1
	outside := planStackStop(testProj, "dev", nil, nil, allPIDs(f), alwaysAlive, f, lineageOf(f, 999))
	if len(outside.skipped) != 0 || len(outside.roots) != 2 {
		t.Errorf("from outside the stack: roots = %v skipped = %+v, want both roots stopped and nothing skipped", outside.roots, outside.skipped)
	}
}

// TestPlanStackStop_LedgerFallbackCannotReachAnAncestor pins the second way a
// root is selected: a recorded pid whose environment cannot be read is still
// signalled, because the record is then forge's only evidence. That fallback
// must not become a way around the guard.
func TestPlanStackStop_LedgerFallbackCannotReachAnAncestor(t *testing.T) {
	f := hostedSessionTable()
	delete(f.env, 300) // the server's environment is unreadable
	tracked := []trackedProc{{name: "api", pid: 300}, {name: "frontend:web", pid: 600}}

	plan := planStackStop(testProj, "dev", nil, tracked, allPIDs(f), alwaysAlive, f, lineageOf(f, 500))
	skipped := map[int]bool{}
	for _, s := range plan.skipped {
		skipped[s.pid] = true
	}
	// With the server unreadable, its children carry the only readable
	// markers, so the shell (400) is a root of its own: it is skipped as an
	// ancestor too.
	if !skipped[300] || !skipped[400] || len(skipped) != 2 {
		t.Fatalf("skipped = %+v, want the recorded ancestor 300 and the shell 400", plan.skipped)
	}
	// The sibling session (410) also surfaced as a root, but it sits inside
	// the skipped server's tree: it is the server's to manage, never forge's.
	if len(plan.roots) != 1 || plan.roots[0] != 600 {
		t.Fatalf("roots = %v, want only the frontend 600", plan.roots)
	}
}

func TestPlanStackStop_ScopedStopStillSkipsAncestor(t *testing.T) {
	f := hostedSessionTable()
	plan := planStackStop(testProj, "dev", []string{"api"}, nil, allPIDs(f), alwaysAlive, f, lineageOf(f, 500))
	if len(plan.roots) != 0 || len(plan.skipped) != 1 {
		t.Errorf("--target api from inside the api server: roots = %v skipped = %+v, want nothing signalled and the server skipped", plan.roots, plan.skipped)
	}
}

func TestAncestorSkipLine(t *testing.T) {
	got := ancestorSkip{pid: 1234, command: "forge env up dev"}.String()
	want := "skipped pid 1234 (forge env up dev): it is an ancestor of this command — stopping it would end the session running you"
	if got != want {
		t.Errorf("skip line =\n  %q\nwant\n  %q", got, want)
	}
}

func TestCommandLabel(t *testing.T) {
	if got := commandLabel([]string{"/opt/homebrew/bin/forge", "env", "up", "dev"}, true); got != "forge env up dev" {
		t.Errorf("got %q", got)
	}
	if got := commandLabel(nil, false); got != "command unreadable" {
		t.Errorf("unreadable argv: got %q", got)
	}
	long := commandLabel([]string{"node", strings.Repeat("x", 200)}, true)
	if n := len([]rune(long)); n != commandLabelMax || !strings.HasSuffix(long, "…") {
		t.Errorf("long command = %q (%d runes), want %d runes ending in …", long, n, commandLabelMax)
	}
}

// TestCompleteStackStop_SkipPreservesRecords: a stack left running because it
// hosts this command is still running, so its records must survive — `forge
// env ps` keeps listing it and `forge env down` from outside still reaches it.
func TestCompleteStackStop_SkipPreservesRecords(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ledger, err := upStatePath(testProj, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(ledger), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledger, []byte("api\t300\nfrontend:web\t600\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var signalled []int
	plan := stackStopPlan{roots: []int{600}, skipped: []ancestorSkip{{pid: 300, command: "reliant serve"}}}
	var res stackStop
	out := captureStdout(t, func() {
		res, err = executeStackStop(testProj, "dev", nil, plan, hostedSessionTable(), func(pids []int) error {
			signalled = append(signalled, pids...)
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(signalled) != 1 || signalled[0] != 600 {
		t.Errorf("signalled %v, want only 600", signalled)
	}
	if res.stopped != 1 || len(res.skipped) != 1 {
		t.Errorf("result = %+v, want 1 stopped and 1 skipped", res)
	}
	if !strings.Contains(out, "skipped pid 300 (reliant serve): it is an ancestor of this command") {
		t.Errorf("output does not report the skip:\n%s", out)
	}
	if _, err := os.Stat(ledger); err != nil {
		t.Errorf("ledger removed although the stack is still running: %v", err)
	}
}

func TestReportUpStop_NamesWhatWasLeftRunning(t *testing.T) {
	out := captureStdout(t, func() {
		_ = reportUpStop("dev", "/project", stackStop{stopped: 1, skipped: []ancestorSkip{{pid: 300}}}, 0, nil)
	})
	if strings.Contains(out, "no owned Forge host processes") {
		t.Errorf("a skipped hosting stack was reported as an empty environment:\n%s", out)
	}
	for _, want := range []string{"signalled 1 host process tree(s)", "left 1 process tree(s)", "host this command"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
}

func TestErrStackHostsThisCommand(t *testing.T) {
	err := errStackHostsThisCommand("dev", []ancestorSkip{{pid: 300, command: "reliant serve"}})
	for _, want := range []string{"pid 300 (reliant serve)", "nothing was stopped", "from a shell outside that stack"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal lacks %q:\n%s", want, err)
		}
	}
}

// TestEnvDown_RealProcess_NeverStopsItsOwnParent runs the real teardown —
// real process table, real ownership markers, real signals — from a process
// whose PARENT belongs to the stack being stopped:
//
//	test ─┬─ parent helper  (marked projectID/env "api")  ← must survive
//	      │   └─ stopper    (inherits the markers; runs stopStack)
//	      └─ decoy          (marked projectID/env "web")  ← must be stopped
//
// The registry (the stack's ledger) records both the parent and the decoy.
// Before the guard, the stopper selected its own parent as a stack root and
// SIGTERMed it.
func TestEnvDown_RealProcess_NeverStopsItsOwnParent(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns and kills real process trees; runs in task test")
	}
	requireProcInspection(t)
	dir, projectID, env := testStack(t)

	decoy := spawnMarked(t, projectID, env, "web")

	parent := exec.Command(os.Args[0], "-test.run=TestHelperLineageProcess")
	parent.Env = append(os.Environ(),
		"FORGE_LINEAGE_HELPER=parent",
		"FORGE_LINEAGE_PROJECT="+projectID,
		"FORGE_LINEAGE_ENV="+env,
		forgeUpEnvVar+"="+env, forgeUpServiceVar+"=api", forgeUpProjectVar+"="+projectID)
	stdin, err := parent.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := parent.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Start(); err != nil {
		t.Fatalf("start parent helper: %v", err)
	}
	parentExited := make(chan struct{})
	go func() {
		_ = parent.Wait()
		close(parentExited)
	}()
	t.Cleanup(func() {
		_ = killProcessTree(parent.Process.Pid, syscall.SIGKILL)
		<-parentExited
	})
	waitForMarker(t, parent.Process.Pid, env)

	// The fake registry: the stack's ledger names the parent and the decoy.
	reg := newProcRegistry(projectID, dir, env)
	reg.processes = []*managedProcess{
		{name: "api", pid: parent.Process.Pid, cmd: &exec.Cmd{}},
		{name: "web", pid: decoy.pid(), cmd: &exec.Cmd{}},
	}
	reg.persist()

	var mu sync.Mutex
	var output strings.Builder
	done := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			mu.Lock()
			output.WriteString(line + "\n")
			mu.Unlock()
			if code, ok := strings.CutPrefix(line, "stopper-exit="); ok {
				done <- code
			}
		}
		_, _ = io.Copy(io.Discard, stdout)
	}()
	if _, err := io.WriteString(stdin, "go\n"); err != nil {
		t.Fatal(err)
	}

	var code string
	select {
	case code = <-done:
	case <-parentExited:
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("the parent helper exited before its child finished — the teardown signalled its own ancestor.\noutput:\n%s", output.String())
	case <-time.After(60 * time.Second):
		t.Fatal("stopper never finished")
	}
	mu.Lock()
	out := output.String()
	mu.Unlock()

	if code != "0" {
		t.Fatalf("stopper failed (exit %s):\n%s", code, out)
	}
	t.Logf("teardown run by the child of a stack process:\n%s", out)
	select {
	case <-parentExited:
		t.Fatalf("the parent (pid %d) died: a teardown signalled the process running it.\n%s", parent.Process.Pid, out)
	default:
	}
	if !decoy.waitExit(15 * time.Second) {
		t.Errorf("the decoy (pid %d) is not this command's ancestor and must be stopped; it survived.\n%s", decoy.pid(), out)
	}
	wantSkip := fmt.Sprintf("skipped pid %d (", parent.Process.Pid)
	if !strings.Contains(out, wantSkip) || !strings.Contains(out, "it is an ancestor of this command — stopping it would end the session running you") {
		t.Errorf("output does not report skipping the parent (want %q):\n%s", wantSkip, out)
	}
	ledger, err := upStatePath(projectID, env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ledger); err != nil {
		t.Errorf("records removed while the skipped tree is still running: %v", err)
	}
}

// TestHelperLineageProcess is not a test: it is the helper process for
// TestEnvDown_RealProcess_NeverStopsItsOwnParent, selected by
// FORGE_LINEAGE_HELPER.
//
//   - parent: wait for "go" on stdin, run the stopper as a CHILD (it inherits
//     this process's ownership markers, as a shell under a forge-started
//     server does), relay its exit code, then block until killed.
//   - stopper: run the real per-env teardown and exit 0 only when exactly one
//     tree was stopped and exactly one — its parent — was skipped.
func TestHelperLineageProcess(t *testing.T) {
	switch os.Getenv("FORGE_LINEAGE_HELPER") {
	case "parent":
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		child := exec.Command(os.Args[0], "-test.run=TestHelperLineageProcess")
		child.Env = append(os.Environ(), "FORGE_LINEAGE_HELPER=stopper")
		child.Stdout = os.Stdout
		child.Stderr = os.Stdout
		code := 0
		if err := child.Run(); err != nil {
			code = 1
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			}
		}
		fmt.Printf("stopper-exit=%d\n", code)
		time.Sleep(60 * time.Second)
		os.Exit(0)
	case "stopper":
		res, err := stopStack(os.Getenv("FORGE_LINEAGE_PROJECT"), os.Getenv("FORGE_LINEAGE_ENV"))
		fmt.Printf("stopped=%d skipped=%d err=%v\n", res.stopped, len(res.skipped), err)
		if err != nil || res.stopped != 1 || len(res.skipped) != 1 || res.skipped[0].pid != os.Getppid() {
			fmt.Printf("want stopped=1 and skipped=[ppid %s]\n", strconv.Itoa(os.Getppid()))
			os.Exit(2)
		}
		os.Exit(0)
	}
}
