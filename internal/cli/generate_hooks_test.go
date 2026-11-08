package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/config"
)

// TestGenerateHookHelperProcess is not a test: it is the command the hook
// tests run, re-executing this test binary so they need no shell and behave
// the same on every platform. Its argv after "--" is the action:
//
//	write <file>          create <file> in the working directory
//	require <file> write <out>  fail unless <file> exists, then create <out>
//	on-path <dir>         fail unless <dir> is on PATH
//	lines <n>             print "hook line 1" … "hook line <n>" to stdout
//	stderr <text>         print <text> to stderr
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
		case "lines":
			n, err := strconv.Atoi(args[1])
			if err != nil {
				os.Exit(8)
			}
			for i := 1; i <= n; i++ {
				fmt.Printf("hook line %d\n", i)
			}
			args = args[2:]
		case "stderr":
			fmt.Fprintln(os.Stderr, args[1])
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

// A hook that compiles the tree (reliant's tool-catalog: `go generate` over
// a package that imports the ORM) must compile what this run produced, in
// its final form. So it runs after every step that writes forge's output —
// the emitters, the root tidy, goimports, the stale-codegen sweep, the drift
// adjudication — and before the tidies and validation that check what it
// emitted.
func TestGenerateHooksRunAfterEveryForgeWriter(t *testing.T) {
	steps := generateSteps()
	index := map[string]int{}
	for i, s := range steps {
		index[s.Name] = i
	}
	hooks, ok := index[generateHooksStep]
	if !ok {
		t.Fatalf("no pipeline step is named %q", generateHooksStep)
	}
	writerTags := map[string]bool{"codegen": true, "frontend": true, "deploy": true, "migrations": true, "proto": true}
	mustPrecede := []string{"go mod tidy (root)", "goimports on generated Go", "cleanup stale codegen", "rehash tracked files", adjudicateDriftStep}
	for i, s := range steps {
		if i > hooks && (writerTags[s.Tag] || slices.Contains(mustPrecede, s.Name)) {
			t.Errorf("%q (tag %s) runs after the generate hooks: a hook compiling the tree would see output this run had not finished writing", s.Name, s.Tag)
		}
	}
	for _, after := range []string{"go mod tidy (gen/)", generateHooksTidyStep, "post-gen validation", "go build (validate generated code)"} {
		if i, ok := index[after]; !ok || i < hooks {
			t.Errorf("%q must run after the generate hooks (so what a hook emits is tidied and validated this run); index %d vs hooks %d", after, i, hooks)
		}
	}
}

// A failed hook's error says which hook, what it ran, and carries the tail
// of what it printed — stdout and stderr together — while the whole output
// lands in its log.
func TestRunGenerateHooks_FailureCarriesTheTailOfItsOutput(t *testing.T) {
	t.Setenv("FORGE_GENERATE_HOOK_HELPER", "1")
	root := t.TempDir()
	total := hookOutputTailLines + 50
	hook := helperHook("tool-catalog", "", "lines", strconv.Itoa(total), "stderr", "./zz.go:3:9: undefined: missingSymbol", "fail")
	var out bytes.Buffer
	err := runGenerateHooks(root, []config.GenerateHook{hook}, &out, &out)

	var he *hookFailureError
	if !errors.As(err, &he) {
		t.Fatalf("want a *hookFailureError, got %T: %v", err, err)
	}
	if he.Hook != "tool-catalog" || !strings.Contains(he.Command, "-test.run=^TestGenerateHookHelperProcess$") {
		t.Errorf("the error must name the hook and its command, got hook %q command %q", he.Hook, he.Command)
	}
	if len(he.Tail) != hookOutputTailLines {
		t.Fatalf("tail holds %d line(s), want the last %d", len(he.Tail), hookOutputTailLines)
	}
	if last := he.Tail[len(he.Tail)-1]; last != "./zz.go:3:9: undefined: missingSymbol" {
		t.Errorf("the tail must end with what the hook printed last (its stderr), got %q", last)
	}
	if first := he.Tail[0]; first != fmt.Sprintf("hook line %d", total-hookOutputTailLines+2) {
		t.Errorf("the tail must be the LAST %d lines, starts with %q", hookOutputTailLines, first)
	}
	if he.Omitted != total+1-hookOutputTailLines {
		t.Errorf("Omitted = %d, want %d", he.Omitted, total+1-hookOutputTailLines)
	}
	wantLog := generateHooksLogDir + "/tool-catalog.log"
	if he.LogPath != wantLog {
		t.Errorf("LogPath = %q, want %q", he.LogPath, wantLog)
	}
	for _, want := range []string{`generate hook "tool-catalog"`, "failed: exit status 3", wantLog} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must contain %q: %v", want, err)
		}
	}
	logged, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(wantLog)))
	if readErr != nil {
		t.Fatalf("the full log must be written: %v", readErr)
	}
	for _, want := range []string{"hook line 1\n", fmt.Sprintf("hook line %d\n", total), "undefined: missingSymbol"} {
		if !strings.Contains(string(logged), want) {
			t.Errorf("the log must hold all of the hook's output, missing %q", want)
		}
	}
	// It still streamed live, as before.
	if !strings.Contains(out.String(), "hook line 1\n") {
		t.Errorf("the hook's output must still stream to the caller:\n%s", out.String())
	}
}

// The repeated tail names the hook, the command and the log, and is the
// hook's own words.
func TestWriteHookFailureTail(t *testing.T) {
	he := &hookFailureError{
		Hook: "tool-catalog", Command: "go generate ./internal/llm/tools",
		Tail: []string{"# example.com/app/internal/store", "./x.go:3:9: row.Subject undefined"}, Omitted: 7,
		LogPath: ".forge/generate-hooks/tool-catalog.log", err: errors.New("exit status 1"),
	}
	var b bytes.Buffer
	writeHookFailureTail(&b, he)
	for _, want := range []string{
		`generate hook "tool-catalog" failed: exit status 1`,
		"command: go generate ./internal/llm/tools",
		"7 earlier line(s) not shown; full output in .forge/generate-hooks/tool-catalog.log",
		"| ./x.go:3:9: row.Subject undefined",
		"full output: .forge/generate-hooks/tool-catalog.log",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("want %q in:\n%s", want, b.String())
		}
	}
}

// The reliant failure, end to end through the real pipeline driver: a forge
// writer changes a mock, then a generate hook fails. The tree must come back
// byte for byte, and the LAST thing on stderr must be what the hook printed —
// not `exit status 1` above a list of reverted files.
func TestGenerate_FailedHookRestoresTheTreeAndEndsWithItsOutput(t *testing.T) {
	t.Setenv("FORGE_GENERATE_HOOK_HELPER", "1")
	dir := t.TempDir()
	mockRel := "internal/store/mock_gen.go"
	stamped, _ := checksums.Stamp(mockRel, []byte("package store\n\n// the committed render\n"))
	writeTreeFileT(t, filepath.Join(dir, filepath.FromSlash(mockRel)), string(stamped))
	before := snapshotTree(t, dir)

	loadHooks := GenStep{
		Name: "load project config", Gate: always, Tag: "config", ReadOnly: true,
		Run: func(ctx *pipelineContext) error {
			ctx.Cfg = &config.ProjectConfig{Generate: config.GenerateConfig{Hooks: []config.GenerateHook{
				helperHook("tool-catalog", "", "lines", "3", "stderr", "./zz.go:3:9: undefined: missingSymbol", "fail"),
			}}}
			return nil
		},
	}
	mocks := GenStep{
		Name: "internal package contracts", Gate: always, Tag: "codegen",
		Run: func(ctx *pipelineContext) error {
			_, err := checksums.WriteDerivedFileOutcome(ctx.AbsPath, mockRel, []byte("package store\n\n// this run's render\n"), &checksums.FileChecksums{}, true)
			return err
		},
	}
	withPipelineSteps(t, []GenStep{loadHooks, mocks, productionStep(t, generateHooksStep)})

	stderr, restore := captureStderr(t)
	var runErr error
	stdout := captureStdout(t, func() { runErr = runGeneratePipelineFlags(dir, pipelineFlags{}) })
	restore()
	errOut := stderr.String()

	var he *hookFailureError
	if !errors.As(runErr, &he) {
		t.Fatalf("want the hook failure to fail the run, got %v\n%s%s", runErr, stdout, errOut)
	}
	if diffs := diffTrees(before, snapshotTree(t, dir)); len(diffs) > 0 {
		t.Errorf("a failed generate must leave the tree byte-identical, changed:\n  %s\n%s", strings.Join(diffs, "\n  "), errOut)
	}
	reverted := strings.LastIndex(errOut, "- "+mockRel)
	cause := strings.LastIndex(errOut, "| ./zz.go:3:9: undefined: missingSymbol")
	if reverted < 0 || cause < 0 || cause < reverted {
		t.Errorf("the hook's own output must be repeated AFTER the reverted-file list (reverted@%d, cause@%d):\n%s", reverted, cause, errOut)
	}
	if !strings.Contains(errOut[cause:], "full output: "+generateHooksLogDir+"/tool-catalog.log") {
		t.Errorf("the repeated output must point to the full log:\n%s", errOut[cause:])
	}
	errFile, _ := os.ReadFile(filepath.Join(dir, failedGenerateDir, failedGenerateErrorFile))
	if !strings.Contains(string(errFile), "undefined: missingSymbol") {
		t.Errorf("%s must carry the hook's output too, got:\n%s", failedGenerateErrorFile, errFile)
	}
}
