package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/forgedelegate"
)

// actAsDelegate makes this process the forge a hand-off exec'd.
func actAsDelegate(t *testing.T) {
	t.Helper()
	prev := isDelegate
	t.Cleanup(func() { isDelegate = prev })
	isDelegate = func() bool { return true }
}

// Review of #1536: an embedder runs many invocations in one process, and the
// root's PersistentPreRun leaves the last one's -C installed. A later
// invocation without -C must resolve ITS project (the working directory),
// not the previous one's — or it execs the previous project's pinned forge
// against this one.
func TestHandOffToPinned_ResolvesThisInvocationsProjectNotThePreviousOnes(t *testing.T) {
	prevVersion, prevCommit := buildinfo.Version(), buildinfo.GitCommit()
	buildinfo.Set(skewTestBinary, "", "6c0c3edb1322")
	t.Cleanup(func() { buildinfo.Set(prevVersion, "", prevCommit) })
	const pinA = "v0.1.40-0.20261001000000-aaaaaaaaaaaa"
	projectA, projectB := t.TempDir(), t.TempDir()
	writeForgeYAML(t, projectA, "name: a\nmodule_path: example.com/a\nforge_version: "+pinA+"\n")
	writeForgeYAML(t, projectB, "name: b\nmodule_path: example.com/b\nforge_version: "+skewTestPin+"\n")
	t.Chdir(projectB)

	// An earlier invocation in this process, with -C A.
	root := newTestRootCmd(t)
	root.SetArgs([]string{"version", "-C", projectA})
	root.SetOut(io.Discard)
	if err := root.Execute(); err != nil {
		t.Fatalf("forge version -C A: %v", err)
	}
	if dir, ok := cmdutil.ProjectDirOverride(); !ok || dir != projectA {
		t.Fatalf("precondition: the earlier invocation left -C %q installed, want %s", dir, projectA)
	}

	h, installs := armDelegation(t)
	handed, _, err := handOffForTest(t, "generate")
	if err != nil || !handed || h.path == "" {
		t.Fatalf("handed %v, err %v", handed, err)
	}
	if len(*installs) != 1 || !strings.HasSuffix((*installs)[0], "@"+skewTestPin) {
		t.Errorf("built %v, want project B's pin %s — the previous invocation's -C chose the forge", *installs, skewTestPin)
	}
}

// Stages of the process tree TestDelegateMarkerDoesNotReachGrandchildren
// builds out of this test binary.
const (
	markerStageEnv   = "FORGE_TEST_MARKER_STAGE"
	markerProjectEnv = "FORGE_TEST_MARKER_PROJECT"
)

// The marker a hand-off sets is good for the forge it exec'd and nothing
// below it. A delegate that starts another forge — `forge env up` starting a
// daemon whose shell runs `reliant forge` in a project pinned to something
// else — must leave that forge to make its own decision, and it delegates.
// So must a grandchild handed a COPY of the delegate's marker by a program
// that captured its environment.
func TestDelegateMarkerDoesNotReachGrandchildren(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the delegate is exec'd in place, which Windows cannot do")
	}
	if testing.Short() {
		// The marker's own process-tree proof (forgedelegate's
		// TestMarker_ProcessTree) runs under -short.
		t.Skip("re-executes this test binary three times (~5s); runs in task test")
	}
	project := t.TempDir()
	writeForgeYAML(t, project, "name: other\nmodule_path: example.com/other\nforge_version: "+skewTestPin+"\n")

	cmd := exec.Command(os.Args[0], "-test.run=^TestDelegateMarkerHelperProcess$")
	cmd.Env = append(os.Environ(), markerStageEnv+"=exec", markerProjectEnv+"="+project)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the process tree failed: %v\n%s", err, out)
	}
	for _, want := range []string{
		"delegate: delegated=true marker-in-env=false",
		"grandchild(inherited): delegated=false handed=true",
		"grandchild(copied): delegated=false handed=true",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// TestDelegateMarkerHelperProcess is not a test: re-executed by the test
// above, it plays one process of the tree.
func TestDelegateMarkerHelperProcess(t *testing.T) {
	switch os.Getenv(markerStageEnv) {
	case "exec":
		// A forge handing its command over: exec in place, as the hand-off does.
		// exec(2) does not de-duplicate: the first stage entry would win.
		var env []string
		for _, kv := range forgedelegate.Env("forge_version " + skewTestPin) {
			if !strings.HasPrefix(kv, markerStageEnv+"=") {
				env = append(env, kv)
			}
		}
		env = append(env, markerStageEnv+"=delegate")
		err := forgedelegate.Exec(os.Args[0], os.Args[1:], env)
		fmt.Printf("exec: %v\n", err)
		os.Exit(1)
	case "delegate":
		_, inEnv := os.LookupEnv(forgedelegate.MarkerEnv)
		fmt.Printf("delegate: delegated=%v marker-in-env=%v\n", forgedelegate.Delegated(), inEnv)
		copied := forgedelegate.MarkerEntry(os.Getpid(), "forge_version "+skewTestPin)
		for name, extra := range map[string][]string{"inherited": nil, "copied": {copied}} {
			cmd := exec.Command(os.Args[0], os.Args[1:]...)
			cmd.Dir = os.Getenv(markerProjectEnv)
			cmd.Env = append(append(os.Environ(), extra...), markerStageEnv+"=grandchild:"+name)
			out, err := cmd.CombinedOutput()
			fmt.Printf("%s", out)
			if err != nil {
				fmt.Printf("grandchild(%s) failed: %v\n", name, err)
			}
		}
		os.Exit(0)
	}
	if name, ok := strings.CutPrefix(os.Getenv(markerStageEnv), "grandchild:"); ok {
		// A forge in a project pinned to another forge, started by the
		// delegate: it decides for itself.
		buildinfo.Set(skewTestBinary, "", "6c0c3edb1322")
		h, _ := armDelegation(t)
		handed, err := HandOffToPinned(context.Background(), []string{"generate"}, io.Discard)
		fmt.Printf("grandchild(%s): delegated=%v handed=%v err=%v exec=%q\n",
			name, forgedelegate.Delegated(), handed, err, filepath.Base(h.path))
		os.Exit(0)
	}
}

// An embedder runs invocation after invocation in one process. Each one's
// hand-off is decided by its own arguments and working directory: never by
// the -C an earlier one left installed, and without installing one itself.
func TestHandOffToPinned_ConsecutiveEmbeddedInvocationsEachResolveTheirOwnProject(t *testing.T) {
	prevVersion, prevCommit := buildinfo.Version(), buildinfo.GitCommit()
	buildinfo.Set(skewTestBinary, "", "6c0c3edb1322")
	t.Cleanup(func() { buildinfo.Set(prevVersion, "", prevCommit) })
	const pinA = "v0.1.40-0.20261001000000-aaaaaaaaaaaa"
	projectA, projectB := t.TempDir(), t.TempDir()
	writeForgeYAML(t, projectA, "name: a\nmodule_path: example.com/a\nforge_version: "+pinA+"\n")
	writeForgeYAML(t, projectB, "name: b\nmodule_path: example.com/b\nforge_version: "+skewTestPin+"\n")
	nested := filepath.Join(projectB, "internal", "x")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmdutil.SetProjectDir("") })

	for i, step := range []struct {
		cwd     string
		args    []string
		wantPin string
	}{
		{projectB, []string{"generate", "-C", projectA}, pinA},
		{projectB, []string{"generate"}, skewTestPin},
		{projectA, []string{"generate"}, pinA},
		{projectA, []string{"--project-dir=" + projectB, "lint"}, skewTestPin},
		{nested, []string{"generate"}, skewTestPin},
		{projectA, []string{"-C", nested, "generate"}, skewTestPin},
		{projectA, []string{"generate"}, pinA},
	} {
		t.Chdir(step.cwd)
		// What an earlier in-process run of the tree leaves behind.
		stale := projectA
		if step.wantPin == pinA {
			stale = projectB
		}
		if err := cmdutil.SetProjectDir(stale); err != nil {
			t.Fatal(err)
		}
		h, installs := armDelegation(t)
		handed, _, err := handOffForTest(t, step.args...)
		if err != nil || !handed || h.path == "" {
			t.Fatalf("step %d %v in %s: handed %v, err %v", i, step.args, step.cwd, handed, err)
		}
		if len(*installs) != 1 || !strings.HasSuffix((*installs)[0], "@"+step.wantPin) {
			t.Errorf("step %d %v in %s: built %v, want %s", i, step.args, step.cwd, *installs, step.wantPin)
		}
		if got, _ := cmdutil.ProjectDirOverride(); got != stale {
			t.Errorf("step %d: the hand-off changed the process-wide project dir to %q", i, got)
		}
	}
}

// Review of #1536: a repeated -C/--project-dir is read as cobra reads it, the
// last one winning, in every spelling and for commands this forge does not
// know. Taking the first handed `forge <unknown> -C a -C b` to a's pinned forge
// with arguments that select b in the receiver.
func TestHandOffToPinned_RepeatedProjectDirTheLastOneWins(t *testing.T) {
	prevVersion, prevCommit := buildinfo.Version(), buildinfo.GitCommit()
	buildinfo.Set(skewTestBinary, "", "6c0c3edb1322")
	t.Cleanup(func() { buildinfo.Set(prevVersion, "", prevCommit) })
	const pinA = "v0.1.40-0.20261001000000-aaaaaaaaaaaa"
	a, b := t.TempDir(), t.TempDir()
	writeForgeYAML(t, a, "name: a\nmodule_path: example.com/a\nforge_version: "+pinA+"\n")
	writeForgeYAML(t, b, "name: b\nmodule_path: example.com/b\nforge_version: "+skewTestPin+"\n")
	t.Chdir(t.TempDir())

	for _, tc := range []struct {
		args []string
		pin  string
	}{
		{[]string{"a-command-from-the-future", "-C", a, "-C", b}, skewTestPin},
		{[]string{"a-command-from-the-future", "-C", b, "-C", a}, pinA},
		{[]string{"a-command-from-the-future", "--project-dir", a, "--project-dir=" + b}, skewTestPin},
		{[]string{"-C" + b, "a-command-from-the-future", "-C=" + a}, pinA},
		{[]string{"a-command-from-the-future", "-C", a, "--project-dir=" + b, "--", "-C", a}, skewTestPin},
		{[]string{"project", "a-subcommand-from-the-future", "-C", a, "-C", b}, skewTestPin},
		{[]string{"generate", "-C", b, "--project-dir", a}, pinA},
	} {
		h, installs := armDelegation(t)
		handed, _, err := handOffForTest(t, tc.args...)
		if err != nil || !handed || h.path == "" {
			t.Errorf("%v: handed %v, err %v", tc.args, handed, err)
			continue
		}
		if len(*installs) != 1 || !strings.HasSuffix((*installs)[0], "@"+tc.pin) {
			t.Errorf("%v: built %v, want %s — the receiver would run in the other project", tc.args, *installs, tc.pin)
		}
	}
}

func TestScanProjectDir(t *testing.T) {
	for want, args := range map[string][]string{
		"b":   {"x", "-C", "a", "-C", "b"},
		"c":   {"-C", "a", "--project-dir", "b", "--project-dir=c"},
		"d":   {"-Ca", "-C=d"},
		"a":   {"-C", "a", "--", "-C", "b"},
		"-Cx": {"-C", "-Cx"},
		"":    {"x", "--", "-C", "a"},
	} {
		if got := scanProjectDir(args); got != want {
			t.Errorf("scanProjectDir(%q) = %q, want %q", args, got, want)
		}
	}
}

// Review of #1536: forge mcp serves every tool call for its own project, in
// process. Handed at start-up to the forge its starting directory pins, the
// server was a delegate for good, and a delegate skips the pin check — so a
// generate for a project pinned to another forge ran under the wrong one.
// mcp is never handed off, and each request is checked against its own pin
// even in a process that was.
func TestMCP_EachRequestIsCheckedAgainstItsOwnProjectsPin(t *testing.T) {
	prevVersion, prevCommit := buildinfo.Version(), buildinfo.GitCommit()
	buildinfo.Set(skewTestBinary, "", "6c0c3edb1322")
	t.Cleanup(func() { buildinfo.Set(prevVersion, "", prevCommit) })
	const pinA = "v0.1.40-0.20261001000000-aaaaaaaaaaaa"
	a, b := t.TempDir(), t.TempDir()
	writeForgeYAML(t, a, "name: a\nmodule_path: example.com/a\nforge_version: "+pinA+"\n")
	writeForgeYAML(t, b, "name: b\nmodule_path: example.com/b\nforge_version: "+skewTestPin+"\n")
	t.Chdir(a)

	// The server is never handed to A's forge.
	h, installs := armDelegation(t)
	if handed, _, err := handOffForTest(t, "mcp"); handed || err != nil || h.path != "" || len(*installs) != 0 {
		t.Fatalf("forge mcp was handed off: handed %v, err %v, built %v", handed, err, *installs)
	}

	// And a server that is a delegate anyway checks each request.
	actAsDelegate(t)
	prevDisabled := pinDelegationDisabled
	t.Cleanup(func() { pinDelegationDisabled = prevDisabled })
	serveRequestsUnderTheirOwnPins()
	cs := connectMCP(t, a)
	out, isErr := callMCP(t, cs, "generate", map[string]any{"dir": b})
	if !isErr || !strings.Contains(out, "pins forge_version "+skewTestPin) {
		t.Errorf("generate for B ran in a server started in A (error %v):\n%s", isErr, out)
	}
}

// Review of #1536: the command a line names skips root's flags AND their
// values, so `forge -C version future-command` (a project directory called
// version) names future-command, which reaches the pinned forge.
func TestHandOffToPinned_AFlagValueIsNotTheCommand(t *testing.T) {
	skewedProject(t)
	root, _ := os.Getwd()
	parent := filepath.Dir(root)
	for _, exempt := range []string{"version", "help", "storage", "slot", "mcp", "report-bug"} {
		dir := filepath.Join(parent, exempt)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeForgeYAML(t, dir, "name: x\nmodule_path: example.com/x\nforge_version: "+skewTestPin+"\n")
		t.Chdir(parent)
		for _, args := range [][]string{
			{"-C", exempt, "a-command-from-the-future"},
			{"--project-dir", exempt, "a-command-from-the-future"},
		} {
			h, _ := armDelegation(t)
			handed, _, err := handOffForTest(t, args...)
			if err != nil || !handed || h.path == "" {
				t.Errorf("%v: handed %v, err %v — the flag's value was taken for the command", args, handed, err)
			}
		}
	}
	r := newTestRootCmd(t)
	for want, args := range map[string][]string{
		"future":  {"-C", "version", "future"},
		"future2": {"--project-dir=version", "future2"},
		"future3": {"-Cversion", "future3"},
		"future4": {"--help", "future4"},
		"":        {"-C", "x", "--", "future"},
	} {
		if got := firstCommandWord(r, args); got != want {
			t.Errorf("firstCommandWord(%q) = %q, want %q", args, got, want)
		}
	}
}
