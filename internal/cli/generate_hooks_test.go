package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// TestGenerateHookHelperProcess is not a test: it is the command the hook
// tests run, re-executing this test binary so they need no shell and behave
// the same on every platform. Its argv after "--" is the action:
//
//	write <file>          create <file> in the working directory
//	require <file> write <out>  fail unless <file> exists, then create <out>
//	on-path <dir>         fail unless <dir> is on PATH
//	fail                  exit 3
func TestGenerateHookHelperProcess(t *testing.T) {
	if os.Getenv("FORGE_GENERATE_HOOK_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	for len(args) > 0 {
		switch args[0] {
		case "fail":
			os.Exit(3)
		case "require":
			if _, err := os.Stat(args[1]); err != nil {
				os.Exit(4)
			}
			args = args[2:]
		case "on-path":
			if !slices.Contains(filepath.SplitList(os.Getenv("PATH")), args[1]) {
				os.Exit(7)
			}
			args = args[2:]
		case "write":
			if err := os.WriteFile(args[1], []byte("ok"), 0o644); err != nil {
				os.Exit(5)
			}
			args = args[2:]
		default:
			os.Exit(6)
		}
	}
	os.Exit(0)
}

func helperHook(name, dir string, action ...string) config.GenerateHook {
	cmd := append([]string{os.Args[0], "-test.run=^TestGenerateHookHelperProcess$", "--"}, action...)
	return config.GenerateHook{Name: name, Command: cmd, Dir: dir}
}

func TestRunGenerateHooks_InOrderAndInDir(t *testing.T) {
	t.Setenv("FORGE_GENERATE_HOOK_HELPER", "1")
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	hooks := []config.GenerateHook{
		helperHook("first", "", "write", "a.txt"),
		// Runs in sub/, and only succeeds if "first" already ran.
		helperHook("second", "sub", "require", filepath.Join("..", "a.txt"), "write", "b.txt"),
	}
	var out bytes.Buffer
	if err := runGenerateHooks(root, hooks, &out, &out); err != nil {
		t.Fatalf("runGenerateHooks: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(root, "sub", "b.txt")); err != nil {
		t.Errorf("second hook did not run in its dir: %v", err)
	}
	if !strings.Contains(out.String(), `generate hook "first"`) || !strings.Contains(out.String(), `generate hook "second"`) {
		t.Errorf("each hook should be announced, got:\n%s", out.String())
	}
}

func TestRunGenerateHooks_FailureStopsAndNamesTheHook(t *testing.T) {
	t.Setenv("FORGE_GENERATE_HOOK_HELPER", "1")
	root := t.TempDir()
	hooks := []config.GenerateHook{
		helperHook("broken", "", "fail"),
		helperHook("after", "", "write", "after.txt"),
	}
	var out bytes.Buffer
	err := runGenerateHooks(root, hooks, &out, &out)
	if err == nil || !strings.Contains(err.Error(), `generate hook "broken"`) {
		t.Fatalf("want an error naming the failed hook, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "after.txt")); statErr == nil {
		t.Error("a hook after a failed one must not run")
	}
}

// A hook sees the pinned codegen tools on its PATH (prepended), exactly as
// forge's own buf step does: a hook that runs `buf` or a protoc plugin must not get
// whichever version the developer's PATH has.
func TestRunGenerateHooks_PinnedToolsOnPath(t *testing.T) {
	t.Setenv("FORGE_GENERATE_HOOK_HELPER", "1")
	root := t.TempDir()
	pinned := t.TempDir()
	key := root + "\x00" + os.Getenv("PATH")
	pinMu.Lock()
	pinCache[key] = &codegenTools{Dirs: []string{pinned}, paths: map[string]string{}}
	pinMu.Unlock()
	t.Cleanup(func() {
		pinMu.Lock()
		delete(pinCache, key)
		pinMu.Unlock()
	})

	var out bytes.Buffer
	if err := runGenerateHooks(root, []config.GenerateHook{helperHook("buf", "", "on-path", pinned, "write", "a.txt")}, &out, &out); err != nil {
		t.Fatalf("runGenerateHooks: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err != nil {
		t.Errorf("the hook did not see the pinned tool dir on its PATH: %v", err)
	}
}

func TestRunGenerateHooks_MissingCommand(t *testing.T) {
	hooks := []config.GenerateHook{{Name: "q", Command: []string{"forge-test-no-such-binary-6b1f"}}}
	var out bytes.Buffer
	err := runGenerateHooks(t.TempDir(), hooks, &out, &out)
	if err == nil || !strings.Contains(err.Error(), "not found on PATH") {
		t.Fatalf("want a not-found-on-PATH error, got %v", err)
	}
}

func TestWritesGenerateHooks(t *testing.T) {
	ctx := &pipelineContext{Cfg: &config.ProjectConfig{Generate: config.GenerateConfig{Hooks: []config.GenerateHook{
		{Name: "a", Writes: []string{"internal/db/sqlc", "./docs/"}},
		{Name: "b", Writes: []string{"docs", "web/src/gen"}},
	}}}}
	got := writesGenerateHooks(ctx)
	want := []string{"docs", "internal/db/sqlc", "web/src/gen"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("writesGenerateHooks = %v, want %v", got, want)
	}
	if hasGenerateHooks(&pipelineContext{Cfg: &config.ProjectConfig{}}) {
		t.Error("a project with no hooks must skip the step")
	}
}

// The output names every hook before any runs, and confirms each one that
// ran — "which hooks ran" is answered by the log, not by inference.
func TestRunGenerateHooks_ReportsWhatRan(t *testing.T) {
	t.Setenv("FORGE_GENERATE_HOOK_HELPER", "1")
	t.Setenv(gitHooksOptOutEnv, "")
	t.Setenv(legacyGitHooksOptOutEnv, "")
	hooks := []config.GenerateHook{
		helperHook("sqlc", "", "write", "a.txt"),
		helperHook("docs", "", "write", "b.txt"),
	}
	var out bytes.Buffer
	if err := runGenerateHooks(t.TempDir(), hooks, &out, &out); err != nil {
		t.Fatalf("runGenerateHooks: %v\n%s", err, out.String())
	}
	for _, want := range []string{
		"Running 2 generate hook(s) from forge.yaml generate.hooks: sqlc, docs",
		`✓ generate hook "sqlc" ran`,
		`✓ generate hook "docs" ran`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output must contain %q, got:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "does not skip these") {
		t.Errorf("no git-hooks variable is set, so nothing should be said about one:\n%s", out.String())
	}
}

// FORGE_NO_HOOKS read as "skip forge.yaml's hooks" to the agents who set it,
// and it never did. Under either git-hooks variable the hooks still run, and
// the step says so where the agent is looking.
func TestRunGenerateHooks_GitHooksOptOutDoesNotSkipThem(t *testing.T) {
	for _, env := range []string{gitHooksOptOutEnv, legacyGitHooksOptOutEnv} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("FORGE_GENERATE_HOOK_HELPER", "1")
			t.Setenv(gitHooksOptOutEnv, "")
			t.Setenv(legacyGitHooksOptOutEnv, "")
			t.Setenv(env, "1")
			root := t.TempDir()
			var out bytes.Buffer
			if err := runGenerateHooks(root, []config.GenerateHook{helperHook("sqlc", "", "write", "a.txt")}, &out, &out); err != nil {
				t.Fatalf("runGenerateHooks: %v\n%s", err, out.String())
			}
			if _, err := os.Stat(filepath.Join(root, "a.txt")); err != nil {
				t.Errorf("%s=1 must not skip a generate hook: %v", env, err)
			}
			if want := env + " is set: it stops forge configuring git hooks in this clone and does not skip these"; !strings.Contains(out.String(), want) {
				t.Errorf("output must explain %s, want %q in:\n%s", env, want, out.String())
			}
		})
	}
}

// A narrowed run that leaves the hooks out says so, naming them and what
// they write; a run that includes the step, or a project without hooks, says
// nothing.
func TestReportGenerateHooksNotRun(t *testing.T) {
	ctx := &pipelineContext{Cfg: &config.ProjectConfig{Generate: config.GenerateConfig{Hooks: []config.GenerateHook{
		{Name: "sqlc", Command: []string{"make", "sqlc"}, Writes: []string{"internal/db/postgres/generated"}},
	}}}}
	withHooks := []GenStep{{Name: "load project config"}, {Name: generateHooksStep}}
	withoutHooks := []GenStep{{Name: "load project config"}}

	var out bytes.Buffer
	reportGenerateHooksNotRun(&out, ctx, withoutHooks, pipelineFlags{TemplatesOnly: true})
	want := "⏩ generate hooks NOT run (--templates-only): sqlc — their output (internal/db/postgres/generated) was not refreshed"
	if !strings.Contains(out.String(), want) {
		t.Errorf("want %q, got:\n%s", want, out.String())
	}

	for name, tc := range map[string]struct {
		ctx   *pipelineContext
		steps []GenStep
	}{
		"the step ran":        {ctx, withHooks},
		"no hooks declared":   {&pipelineContext{Cfg: &config.ProjectConfig{}}, withoutHooks},
		"config never loaded": {&pipelineContext{}, withoutHooks},
	} {
		out.Reset()
		reportGenerateHooksNotRun(&out, tc.ctx, tc.steps, pipelineFlags{Steps: "mocks"})
		if out.Len() != 0 {
			t.Errorf("%s: expected no report, got:\n%s", name, out.String())
		}
	}
}

// The hooks step is pinned to the name presets and the plan use.
func TestGenerateHooksStepIsInThePipeline(t *testing.T) {
	for _, s := range generateSteps() {
		if s.Name == generateHooksStep {
			return
		}
	}
	t.Fatalf("no pipeline step is named %q", generateHooksStep)
}
