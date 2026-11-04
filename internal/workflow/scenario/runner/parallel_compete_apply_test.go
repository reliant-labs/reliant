// Copyright (c) 2025 Reliant Labs
package runner

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/memtemp"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// The parallel-compete P0 (2026-10-06): when the reviewer picked use_winner,
// `apply_winner` ran `rsync -av --delete <winner>/ "{{workflow.path}}/"` with
// workflow.path never populated, i.e. `rsync --delete <winner>/ /`, as the
// user, for 172s before it was killed.
//
// These tests take the command the REAL runtime renders for the REAL builtin
// workflow and run it — but only inside a t.TempDir() sandbox, and only after
// checking that every absolute path it names is inside that sandbox. Nothing
// here ever executes against /, $HOME, or any directory a test did not create.

// sandbox is a throwaway machine: a HOME, a git project inside it, the
// candidate worktrees parallel-compete's create_worktree would have made under
// $HOME/.reliant/worktrees, and sentinel files outside the project that a
// correct apply must leave alone.
type sandbox struct {
	root        string // everything lives under here
	home        string
	project     string
	candidates  map[int]string // candidate number -> worktree path
	sentinels   []string       // files outside the project that must survive
	userNotes   string         // untracked user file inside the project
	baseContent map[string]string
}

func requireTools(t *testing.T) {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("apply_winner is a bash script; the Windows daemon runs PowerShell")
	}
	for _, tool := range []string{"bash", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	cmd.Env = sandboxEnv("")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return string(out)
}

// sandboxEnv is the environment a sandboxed command runs with: no inherited
// HOME, no user or system git config.
func sandboxEnv(home string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "LC_ALL=C"}
	if home != "" {
		env = append(env, "HOME="+home)
	}
	return env
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// sandboxLayout names where everything in a sandbox rooted at root lives. It
// touches nothing on disk.
func sandboxLayout(root string) *sandbox {
	s := &sandbox{
		root:        root,
		home:        filepath.Join(root, "home"),
		project:     filepath.Join(root, "home", "projects", "app"),
		candidates:  map[int]string{},
		baseContent: map[string]string{"a.txt": "base a\n", "b.txt": "base b\n", "keep.txt": "keep\n"},
	}
	s.userNotes = filepath.Join(s.project, "notes.txt")
	s.sentinels = []string{
		filepath.Join(root, "outside", "SENTINEL"),
		filepath.Join(s.home, "SENTINEL"),
		filepath.Join(s.home, "projects", "other", "SENTINEL"),
	}
	for n := 1; n <= 3; n++ {
		s.candidates[n] = filepath.Join(s.home, ".reliant", "worktrees", "app", "compete-impl-"+strconv.Itoa(n)+"-default-test-wf-0a1b2c3d")
	}
	return s
}

// newSandbox's state is built once per test binary and copied per test.
//
// Building it is ten git processes, every one a fork and an exec, and on a
// loaded machine a process costs hundreds of milliseconds: TestApplyWinner_Guards
// built eleven of these one after another and took 30-53s of the package's 60s
// -short budget. A byte copy costs no process at all. Every absolute path the
// copy carries (the worktrees' gitdir links) is rewritten to the copy's root.
var sandboxTemplate struct {
	once sync.Once
	root string
	err  error
}

func TestMain(m *testing.M) {
	code := m.Run()
	if sandboxTemplate.root != "" {
		_ = os.RemoveAll(sandboxTemplate.root)
	}
	os.Exit(code)
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	requireTools(t)
	sandboxTemplate.once.Do(func() {
		dir, err := os.MkdirTemp("", "runner-sandbox-template-")
		if err == nil {
			// Canonical, so the paths rewritten below are byte-identical
			// to the ones git wrote.
			dir, err = filepath.EvalSymlinks(dir)
		}
		if err == nil {
			sandboxTemplate.root = dir
			err = buildSandbox(sandboxLayout(dir))
		}
		sandboxTemplate.err = err
	})
	require.NoError(t, sandboxTemplate.err, "build the sandbox template")
	// In memory: apply_winner and the subtests' own setup still run git in
	// the copy, and on the shared disk under a full test lane their writes
	// are a third of the time (internal/memtemp).
	root, err := filepath.EvalSymlinks(memtemp.Dir(t))
	require.NoError(t, err)
	require.NoError(t, copySandbox(sandboxTemplate.root, root))
	return sandboxLayout(root)
}

// buildSandbox creates s on disk: the project, its uncommitted user file, the
// sentinels, and the candidate worktrees.
func buildSandbox(s *sandbox) error {
	write := func(path, content string) error {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(content), 0o644)
	}
	for name, content := range s.baseContent {
		if err := write(filepath.Join(s.project, name), content); err != nil {
			return err
		}
	}
	for _, args := range [][]string{
		{"init", "-q"},
		// No background housekeeping: every commit would otherwise spawn a
		// `git maintenance run --auto` child, one more process on a machine
		// where a process is the whole cost. apply_winner never depends on it.
		{"config", "maintenance.auto", "false"},
		{"config", "gc.auto", "0"},
		{"add", "-A"},
		{"commit", "-qm", "base"},
	} {
		if err := sandboxGit(s.project, args...); err != nil {
			return err
		}
	}

	// Uncommitted user work in the project. rsync --delete removed anything
	// the candidate lacked, so even a CORRECT destination lost this.
	if err := write(s.userNotes, "my uncommitted notes\n"); err != nil {
		return err
	}
	for _, p := range s.sentinels {
		if err := write(p, "must survive\n"); err != nil {
			return err
		}
	}

	// Candidate N rewrites a.txt and adds new_N.txt. Candidate 1 commits its
	// work (an agent may), the others leave it uncommitted; candidate 2 also
	// deletes b.txt, which must reach the project as a deletion.
	for n := 1; n <= 3; n++ {
		wt := s.candidates[n]
		if err := sandboxGit(s.project, "worktree", "add", "-q", "-b", "compete-"+strconv.Itoa(n), wt, "HEAD"); err != nil {
			return err
		}
		if err := write(filepath.Join(wt, "a.txt"), "candidate "+strconv.Itoa(n)+"\n"); err != nil {
			return err
		}
		if err := write(filepath.Join(wt, "new_"+strconv.Itoa(n)+".txt"), "added by candidate\n"); err != nil {
			return err
		}
		if n == 2 {
			if err := os.Remove(filepath.Join(wt, "b.txt")); err != nil {
				return err
			}
		}
		if n == 1 {
			if err := sandboxGit(wt, "add", "-A"); err != nil {
				return err
			}
			if err := sandboxGit(wt, "commit", "-qm", "candidate 1"); err != nil {
				return err
			}
		}
	}
	return nil
}

// sandboxGit runs git as gitIn does, without a *testing.T: the template
// outlives whichever test happened to build it.
func sandboxGit(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	cmd.Env = sandboxEnv("")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return nil
}

// copySandbox copies the template at src into the empty dst, keeping
// permissions and modification times, and rewrites every occurrence of src in
// git's link files — a worktree's .git file and its admin dir's gitdir name
// each other by absolute path — so the copy is self-contained.
func copySandbox(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case d.Type().IsRegular():
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if name := d.Name(); name == ".git" || name == "gitdir" {
				b = bytes.ReplaceAll(b, []byte(src), []byte(dst))
			}
			if err := os.WriteFile(target, b, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chtimes(target, info.ModTime(), info.ModTime())
		default:
			return fmt.Errorf("copySandbox: %s is neither a file nor a directory", path)
		}
	})
}

// quotedAbsPath matches a quoted literal absolute path — not shell syntax that
// happens to sit between quotes, like `"$rel"/*) ... ("`.
var quotedAbsPath = regexp.MustCompile(`['"](/[^'"\s$(){}*]*)['"]`)

// requireConfinedToSandbox fails the test, WITHOUT running anything, if the
// command names an absolute path outside the sandbox. On the vulnerable
// workflow this is where the test stops: the destination is "/".
func (s *sandbox) requireConfinedToSandbox(t *testing.T, command string) {
	t.Helper()
	matches := quotedAbsPath.FindAllStringSubmatch(command, -1)
	require.NotEmpty(t, matches, "expected the command to name its paths; got:\n%s", command)
	for _, m := range matches {
		p := filepath.Clean(m[1])
		if p != s.root && !strings.HasPrefix(p, s.root+string(filepath.Separator)) {
			t.Fatalf("REFUSING TO EXECUTE: the rendered command names %q, outside the sandbox %s.\ncommand:\n%s", m[1], s.root, command)
		}
	}
}

// run executes command with bash inside the sandbox, as the daemon would: cwd
// is the chat's project checkout and HOME is the sandbox's.
func (s *sandbox) run(t *testing.T, command string) (string, error) {
	t.Helper()
	s.requireConfinedToSandbox(t, command)
	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = s.project
	cmd.Env = sandboxEnv(s.home)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func readOr(path, missing string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return missing
	}
	return string(b)
}

const absent = "<absent>"

func (s *sandbox) requireUntouchedOutsideProject(t *testing.T) {
	t.Helper()
	for _, p := range s.sentinels {
		require.Equal(t, "must survive\n", readOr(p, absent), "sentinel outside the project: %s", p)
	}
	require.Equal(t, "my uncommitted notes\n", readOr(s.userNotes, absent), "the user's untracked file in the project")
	for n, wt := range s.candidates {
		require.Equal(t, "candidate "+strconv.Itoa(n)+"\n", readOr(filepath.Join(wt, "a.txt"), absent),
			"candidate %d's worktree must not be modified", n)
	}
}

func (s *sandbox) requireProjectUnchanged(t *testing.T) {
	t.Helper()
	for name, content := range s.baseContent {
		require.Equal(t, content, readOr(filepath.Join(s.project, name), absent), "project file %s", name)
	}
	for n := 1; n <= 3; n++ {
		require.Equal(t, absent, readOr(filepath.Join(s.project, "new_"+strconv.Itoa(n)+".txt"), absent))
	}
}

// requireWinnerApplied asserts the project is base + candidate n's changes and
// nothing else.
func (s *sandbox) requireWinnerApplied(t *testing.T, n int) {
	t.Helper()
	require.Equal(t, "candidate "+strconv.Itoa(n)+"\n", readOr(filepath.Join(s.project, "a.txt"), absent))
	require.Equal(t, "added by candidate\n", readOr(filepath.Join(s.project, "new_"+strconv.Itoa(n)+".txt"), absent))
	wantB := "base b\n"
	if n == 2 {
		wantB = absent
	}
	require.Equal(t, wantB, readOr(filepath.Join(s.project, "b.txt"), absent), "b.txt")
	require.Equal(t, "keep\n", readOr(filepath.Join(s.project, "keep.txt"), absent))
	for m := 1; m <= 3; m++ {
		if m != n {
			require.Equal(t, absent, readOr(filepath.Join(s.project, "new_"+strconv.Itoa(m)+".txt"), absent),
				"candidate %d's changes must not be applied", m)
		}
	}
	s.requireUntouchedOutsideProject(t)
}

// TestParallelCompete_UseWinnerWritesOnlyInsideTheProject runs the REAL
// builtin parallel-compete through review → use_winner → apply_winner on the
// deterministic scenario runner, takes the command the runtime rendered for
// apply_winner, and runs it in the sandbox.
func TestParallelCompete_UseWinnerWritesOnlyInsideTheProject(t *testing.T) {
	testkit.Slow(t, "runs the parallel-compete workflow over real git worktrees")
	s := newSandbox(t)

	wf := loadBuiltin(t, "parallel-compete")
	sc := findScenario(t, loadScenarios(t, "parallel-compete"), "parallel_compete_use_winner")
	// The chat's checkout, as the launcher injects it.
	sc.Inputs = map[string]interface{}{"project_path": s.project}
	// create_worktree's outputs point at the sandbox's real candidates.
	n := 0
	for i := range sc.Events {
		if sc.Events[i].Node == "implementations.create_wt" {
			n++
			sc.Events[i].Output = map[string]interface{}{"path": s.candidates[n], "branch": "compete-" + strconv.Itoa(n)}
		}
	}
	require.Equal(t, 3, n)

	var mu sync.Mutex
	commands := map[string]string{}
	res := New(wf, Options{OnRunStep: func(nodePath, command string) {
		mu.Lock()
		defer mu.Unlock()
		commands[nodePath] = command
	}}).Run(sc)
	require.Equal(t, "completed", res.Execution.Outcome, "mismatches=%v error=%+v", res.Mismatches, res.Execution.Error)

	command, ok := commands["apply_winner"]
	require.True(t, ok, "apply_winner never dispatched; run commands: %v", commands)
	s.requireConfinedToSandbox(t, command)

	// Which candidate the loop filed under _results["2"] depends on the order
	// the parallel iterations consumed the create_wt mocks, so read the source
	// the runtime actually rendered rather than assuming candidate 2.
	src := regexp.MustCompile(`(?m)^src='([^']*)'$`).FindStringSubmatch(command)
	require.Len(t, src, 2, "apply_winner must bind its source to src='…'; command:\n%s", command)
	winner := 0
	for num, wt := range s.candidates {
		if wt == src[1] {
			winner = num
		}
	}
	require.NotZero(t, winner, "apply_winner's source %q is not one of the run's candidates", src[1])

	out, err := s.run(t, command)
	require.NoError(t, err, "apply_winner failed:\n%s", out)
	s.requireWinnerApplied(t, winner)
}

// applyWinnerNode returns the shipped apply_winner node.
func applyWinnerNode(t *testing.T) *reliantv1.Node {
	t.Helper()
	for _, node := range loadBuiltin(t, "parallel-compete").GetNodes() {
		if node.GetId() == "apply_winner" {
			return node
		}
	}
	t.Fatal("parallel-compete has no apply_winner node")
	return nil
}

// renderApplyWinner resolves apply_winner exactly as the runtime does for a run
// whose project directory is projectPath and whose reviewer picked the
// candidate at winnerPath.
func renderApplyWinner(t *testing.T, projectPath, winnerPath string) (string, error) {
	t.Helper()
	nodes := map[string]interface{}{
		"implementations": map[string]interface{}{"_results": map[string]interface{}{
			"2": map[string]interface{}{"worktree_path": winnerPath},
		}},
		"review": map[string]interface{}{"response": map[string]interface{}{"winner": float64(2), "strategy": "use_winner"}},
	}
	resolved, err := runtime.EvaluateNodeConfig(applyWinnerNode(t), nodes, "wf-1", "parallel-compete",
		map[string]interface{}{}, nil, nil, &runtime.ExecutionContext{ProjectPath: projectPath})
	if err != nil {
		return "", err
	}
	return model.CelStringValue(resolved.GetRun().GetCommand()), nil
}

// Every refusal leaves the project, the candidates and everything outside them
// exactly as they were.
//
// The subtests run in parallel: each builds its own sandbox (about ten git
// commands) and shares nothing, and one after another their process waits were
// 32-58s of the package's 60s -short budget under a full test lane (2026-10-22).
func TestApplyWinner_Guards(t *testing.T) {
	t.Run("applies the winner and nothing else", func(t *testing.T) {
		t.Parallel()
		s := newSandbox(t)
		command, err := renderApplyWinner(t, s.project, s.candidates[2])
		require.NoError(t, err)
		out, err := s.run(t, command)
		require.NoError(t, err, out)
		s.requireWinnerApplied(t, 2)
	})

	t.Run("an empty project path never renders", func(t *testing.T) {
		t.Parallel()
		s := newSandbox(t)
		_, err := renderApplyWinner(t, "", s.candidates[2])
		require.Error(t, err)
		require.Contains(t, err.Error(), "workflow.path")
	})

	t.Run("an empty winner path never renders", func(t *testing.T) {
		t.Parallel()
		s := newSandbox(t)
		_, err := renderApplyWinner(t, s.project, "")
		require.Error(t, err)
		require.Contains(t, err.Error(), "worktree_path")
	})

	refusals := []struct {
		name  string
		setup func(t *testing.T, s *sandbox) (projectPath, winnerPath string)
	}{
		{"destination is not this run's checkout", func(t *testing.T, s *sandbox) (string, string) {
			other := filepath.Join(s.home, "projects", "other")
			return other, s.candidates[2]
		}},
		{"destination is HOME", func(t *testing.T, s *sandbox) (string, string) {
			return s.home, s.candidates[2]
		}},
		{"destination is relative", func(t *testing.T, s *sandbox) (string, string) {
			return ".", s.candidates[2]
		}},
		{"source is the project itself", func(t *testing.T, s *sandbox) (string, string) {
			return s.project, s.project
		}},
		{"source is outside ~/.reliant/worktrees", func(t *testing.T, s *sandbox) (string, string) {
			stray := filepath.Join(s.root, "outside", "compete-impl-2-x")
			gitIn(t, s.project, "worktree", "add", "-q", "-b", "stray", stray, "HEAD")
			writeFile(t, filepath.Join(stray, "a.txt"), "stray\n")
			return s.project, stray
		}},
		{"source is a worktree of a different repository", func(t *testing.T, s *sandbox) (string, string) {
			foreign := filepath.Join(s.root, "foreign")
			writeFile(t, filepath.Join(foreign, "a.txt"), "foreign\n")
			gitIn(t, foreign, "init", "-q")
			gitIn(t, foreign, "add", "-A")
			gitIn(t, foreign, "commit", "-qm", "foreign")
			wt := filepath.Join(s.home, ".reliant", "worktrees", "app", "compete-impl-2-foreign")
			gitIn(t, foreign, "worktree", "add", "-q", "-b", "f", wt, "HEAD")
			writeFile(t, filepath.Join(wt, "a.txt"), "foreign change\n")
			return s.project, wt
		}},
		{"source is not a parallel-compete candidate", func(t *testing.T, s *sandbox) (string, string) {
			wt := filepath.Join(s.home, ".reliant", "worktrees", "app", "my-branch-chat")
			gitIn(t, s.project, "worktree", "add", "-q", "-b", "mine", wt, "HEAD")
			writeFile(t, filepath.Join(wt, "a.txt"), "mine\n")
			return s.project, wt
		}},
		{"the project has a conflicting uncommitted edit", func(t *testing.T, s *sandbox) (string, string) {
			writeFile(t, filepath.Join(s.project, "a.txt"), "the user's own edit\n")
			s.baseContent["a.txt"] = "the user's own edit\n"
			return s.project, s.candidates[2]
		}},
	}
	for _, tc := range refusals {
		t.Run("refuses: "+tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSandbox(t)
			projectPath, winnerPath := tc.setup(t, s)
			command, err := renderApplyWinner(t, projectPath, winnerPath)
			require.NoError(t, err)
			out, err := s.run(t, command)
			require.Error(t, err, "apply_winner should have refused; output:\n%s", out)
			require.Contains(t, out, "apply_winner: refusing", "the refusal must say why")
			t.Logf("%s", strings.TrimSpace(out))
			s.requireProjectUnchanged(t)
			s.requireUntouchedOutsideProject(t)
		})
	}
}

func parallelCompeteNode(t *testing.T, id string) *reliantv1.Node {
	t.Helper()
	for _, node := range loadBuiltin(t, "parallel-compete").GetNodes() {
		if node.GetId() == id {
			return node
		}
	}
	t.Fatalf("parallel-compete has no %s node", id)
	return nil
}

// The synthesizer agent is told to copy files into its working directory, so
// that directory must be the chat's checkout — and a run without one must fail
// at the node, not start an agent in an unknown directory. The `complete`
// message tells the user where to `cd`; it may never render that empty either.
func TestParallelCompete_SynthesizerAndCompleteUseTheProjectDirectory(t *testing.T) {
	t.Parallel()
	nodes := map[string]interface{}{
		"improve_prompt": map[string]interface{}{"message": map[string]interface{}{"text": "task"}},
		"implementations": map[string]interface{}{"_results": map[string]interface{}{
			"1": map[string]interface{}{"worktree_path": "/home/u/.reliant/worktrees/app/compete-impl-1-x"},
			"2": map[string]interface{}{"worktree_path": "/home/u/.reliant/worktrees/app/compete-impl-2-x"},
			"3": map[string]interface{}{"worktree_path": "/home/u/.reliant/worktrees/app/compete-impl-3-x"},
		}},
		"review": map[string]interface{}{"response": map[string]interface{}{
			"winner": float64(2), "strategy": "synthesize", "confidence": float64(7),
			"rationale": "r", "synthesis_instructions": "take the tests from 3",
		}},
	}
	inputs := map[string]interface{}{"mode": "auto", "model": map[string]interface{}{"id": "m"}}
	resolve := func(id, projectPath string) (*reliantv1.Node, error) {
		return runtime.EvaluateNodeConfig(parallelCompeteNode(t, id), nodes, "wf-1", "parallel-compete",
			inputs, nil, nil, &runtime.ExecutionContext{ProjectPath: projectPath})
	}

	synth, err := resolve("synthesizer", "/home/u/projects/app")
	require.NoError(t, err)
	require.Equal(t, "/home/u/projects/app", model.NodeProjectPath(synth), "the synthesizer must work in the chat's checkout")
	_, err = resolve("synthesizer", "")
	require.ErrorContains(t, err, "workflow.path", "a run with no project directory must not start the synthesizer")

	complete, err := resolve("complete", "/home/u/projects/app")
	require.NoError(t, err)
	content := model.CelStringValue(complete.GetSaveMessageNode().GetContent())
	require.Contains(t, content, "cd /home/u/projects/app\n")
	_, err = resolve("complete", "")
	require.ErrorContains(t, err, "workflow.path")
}
