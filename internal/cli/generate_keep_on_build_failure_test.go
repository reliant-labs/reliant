// --keep-on-build-failure: the documented path for a migration whose
// hand-written code is ported AFTER generating (docs/proposals/connect-v2.md,
// section 5, step 4).
//
// The defect it fixes: a project mid-way to connect-go v2 ran `forge
// generate` as the migration guide says, the final compile check failed
// (the hand-written code was not ported yet — that is steps 5 to 8), and
// generate rolled back every file it wrote: exactly the v2 output the next
// steps port the code against. The only way through was two hidden flags,
// --no-revert --skip-validate.
//
// Pinned contract:
//
//   - Default: a failed compile check rolls the tree back, and the failure
//     report names --keep-on-build-failure.
//   - With the flag: the files stay on disk, the compile errors are still
//     printed, the run exits non-zero, and it is never recorded as a
//     verified fixed point — so the next plain `forge generate` runs every
//     step and checks the tree again.
//   - The flag and --strict contradict each other; the command refuses both.
package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// stubGoBuildValidate replaces the validate step's compile check with fn.
func stubGoBuildValidate(t *testing.T, fn func(string) error) {
	t.Helper()
	prev := goBuildValidate
	goBuildValidate = func(dir string) (validateCaveats, error) { return validateCaveats{}, fn(dir) }
	t.Cleanup(func() { goBuildValidate = prev })
}

// migrationCompileError is what the compile check reports for a project
// whose hand-written code still speaks connect-go v1 against v2 output.
func migrationCompileError() error {
	return &validateBuildError{
		Output: "internal/handlers/widget/service.go:12:9: undefined: connect.NewRequest\n",
		err:    errors.New("forge generate (validate generated code): go build failed"),
	}
}

func TestGenerateKeepOnBuildFailureFlag(t *testing.T) {
	cmd := newGenerateCmd()
	f := cmd.Flags().Lookup("keep-on-build-failure")
	if f == nil || f.Hidden {
		t.Fatalf("--keep-on-build-failure must be a visible flag: the migration guide names it, got %+v", f)
	}
	if !strings.Contains(cmd.UsageString(), "--keep-on-build-failure") {
		t.Error("--keep-on-build-failure must appear in `forge generate --help`")
	}

	both := newGenerateCmd()
	if err := both.ParseFlags([]string{"--keep-on-build-failure", "--strict"}); err != nil {
		t.Fatal(err)
	}
	if err := both.ValidateFlagGroups(); err == nil {
		t.Error("--keep-on-build-failure with --strict must be refused: --strict rolls the tree back at the first failed step")
	}

	// The spellings the migration used before this flag existed keep working.
	old := newGenerateCmd()
	if err := old.ParseFlags([]string{"--no-revert", "--skip-validate"}); err != nil {
		t.Fatalf("--no-revert --skip-validate must still parse: %v", err)
	}
}

// The step itself: a failed compile is returned (the run rolls back) unless
// the flag is set, and then it is a failed step the run goes on past.
func TestStepGoBuildValidateKeepsOnBuildFailure(t *testing.T) {
	stubGoBuildValidate(t, func(string) error { return migrationCompileError() })

	plain := &pipelineContext{ProjectDir: t.TempDir()}
	if err := stepGoBuildValidate(plain); err == nil {
		t.Fatal("without the flag a failed compile must fail the step, so the run rolls back")
	}
	if len(plain.SoftFailures) != 0 || plain.keptBuildFailure != nil {
		t.Errorf("without the flag nothing is kept: soft=%v kept=%v", plain.SoftFailures, plain.keptBuildFailure)
	}

	kept := &pipelineContext{ProjectDir: t.TempDir(), KeepOnBuildFailure: true}
	stderr, restore := captureStderr(t)
	err := stepGoBuildValidate(kept)
	restore()
	if err != nil {
		t.Fatalf("with the flag the run must go on past a failed compile, got %v", err)
	}
	if !slices.Equal(kept.SoftFailures, []string{goBuildValidateStep}) {
		t.Errorf("the failed compile must be a failed step of the run, got %v", kept.SoftFailures)
	}
	if kept.keptBuildFailure == nil {
		t.Error("the compile error must be kept for the report the run ends with")
	}
	if !strings.Contains(stderr.String(), "go build failed") {
		t.Errorf("the failure must be printed where it happened:\n%s", stderr.String())
	}

	var b strings.Builder
	var stepsFailed *factory.StepsFailedError
	if err := softFailureOutcome(&b, kept.SoftFailures, kept.softFailureCauses); !errors.As(err, &stepsFailed) {
		t.Errorf("a kept build failure must still fail the run, got %v", err)
	}
}

func TestReportKeptBuildFailure(t *testing.T) {
	var b strings.Builder
	reportKeptBuildFailure(&b, nil)
	if b.Len() != 0 {
		t.Errorf("a run whose compile passed reports nothing, got %q", b.String())
	}
	stderr, restore := captureStderr(t)
	reportKeptBuildFailure(&b, migrationCompileError())
	restore()
	if !strings.Contains(b.String(), "KEPT on disk") || !strings.Contains(b.String(), "without the flag") {
		t.Errorf("the report must say the files were kept and what to run next:\n%s", b.String())
	}
	if !strings.Contains(stderr.String(), "undefined: connect.NewRequest") {
		t.Errorf("the compiler output must be repeated last:\n%s", stderr.String())
	}

	var hint strings.Builder
	hintKeepOnBuildFailure(&hint, errors.New("buf generate failed"))
	if hint.Len() != 0 {
		t.Errorf("only a compile failure gets the hint, got %q", hint.String())
	}
	hintKeepOnBuildFailure(&hint, &generateInterruptedError{signal: os.Interrupt, stepErr: migrationCompileError()})
	if hint.Len() != 0 {
		t.Errorf("an interrupted run gets no hint, got %q", hint.String())
	}
	hintKeepOnBuildFailure(&hint, migrationCompileError())
	if !strings.Contains(hint.String(), "--keep-on-build-failure") {
		t.Errorf("a rolled-back compile failure must name the flag, got %q", hint.String())
	}
}

func TestGenerateCacheModeKeepOnBuildFailure(t *testing.T) {
	// The flag weakens nothing: a run whose compile passes is a full-strength
	// run. One whose compile fails is a soft failure, which never records
	// (TestGenerate_KeepOnBuildFailureKeepsOutputAndNeverRecords).
	got := generateCacheModeFor(pipelineFlags{Cache: true, KeepOnBuildFailure: true})
	if want := (generateCacheMode{consult: true, record: true}); got != want {
		t.Errorf("mode = %+v, want %+v", got, want)
	}
}

// TestGenerate_KeepOnBuildFailureKeepsOutputAndNeverRecords drives the real
// pipeline driver, journal and cache over the production validate step with
// its compile check failing.
func TestGenerate_KeepOnBuildFailureKeepsOutputAndNeverRecords(t *testing.T) {
	f := newCacheFixture(t) // slow: builds a git repo; skipped under -short
	withCacheSystem(t, f)
	origCheck := restoredTreeCompileCheck
	restoredTreeCompileCheck = func(string) rollbackConsistency { return rollbackConsistency{Checked: true, Builds: true} }
	t.Cleanup(func() { restoredTreeCompileCheck = origCheck })

	const genRel = "internal/widget/widget_gen.go"
	const genBody = "package widget // v2 output\n"
	emits := 0
	emitter := GenStep{
		Name: "emit v2 output",
		Gate: always,
		Tag:  "codegen",
		Run: func(ctx *pipelineContext) error {
			emits++
			// A journaled forge write, as the writers make it.
			checksums.RecordPreWrite(ctx.AbsPath, genRel)
			path := filepath.Join(ctx.AbsPath, filepath.FromSlash(genRel))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			return os.WriteFile(path, []byte(genBody), 0o644)
		},
	}
	withPipelineSteps(t, []GenStep{emitter, productionStep(t, goBuildValidateStep)})
	compiles := false
	stubGoBuildValidate(t, func(string) error {
		if compiles {
			return nil
		}
		return migrationCompileError()
	})

	run := func(flags pipelineFlags) (string, error) {
		t.Helper()
		var runErr error
		stdout, stderr := captureStdoutAndStderr(t, func() { runErr = runGeneratePipelineFlags(f.root, flags) })
		return stdout + stderr, runErr
	}
	genPath := filepath.Join(f.root, filepath.FromSlash(genRel))

	// Default: rolled back, and the report names the flag.
	out, err := run(pipelineFlags{Cache: true})
	if err == nil {
		t.Fatalf("a failed compile must fail the run:\n%s", out)
	}
	if _, statErr := os.Stat(genPath); !os.IsNotExist(statErr) {
		t.Errorf("without the flag the run's output must be rolled back, stat err = %v", statErr)
	}
	if !strings.Contains(out, "--keep-on-build-failure") {
		t.Errorf("a rolled-back compile failure must name --keep-on-build-failure:\n%s", out)
	}

	// The flag: the output stays, the errors are reported, the run fails.
	out, err = run(pipelineFlags{Cache: true, KeepOnBuildFailure: true})
	var stepsFailed *factory.StepsFailedError
	if !errors.As(err, &stepsFailed) || !slices.Contains(stepsFailed.Steps, goBuildValidateStep) {
		t.Fatalf("a kept build failure must still exit non-zero, naming the step; got %v\n%s", err, out)
	}
	if got, readErr := os.ReadFile(genPath); readErr != nil || string(got) != genBody {
		t.Fatalf("with the flag the generated file must stay on disk, got %q (%v)\n%s", got, readErr, out)
	}
	for _, want := range []string{"KEPT on disk", "undefined: connect.NewRequest"} {
		if !strings.Contains(out, want) {
			t.Errorf("the run must report %q:\n%s", want, out)
		}
	}

	// A second kept run changes nothing, and still must not record: its
	// compile failed, so the tree is not verified.
	out, _ = run(pipelineFlags{Cache: true, KeepOnBuildFailure: true})
	if strings.Contains(out, "Recorded as a verified fixed point") || loadGenerateCacheManifest(f.root) != nil {
		t.Fatalf("a run whose compile failed must never be recorded as a verified fixed point:\n%s", out)
	}

	// Once the code is ported, a plain run runs every step, and records.
	compiles = true
	before := emits
	out, err = run(pipelineFlags{Cache: true})
	if err != nil {
		t.Fatalf("a run that compiles must pass: %v\n%s", err, out)
	}
	if emits != before+1 || strings.Contains(out, "nothing to do") {
		t.Fatalf("the next plain run must run every step after a kept failure (emits %d -> %d):\n%s", before, emits, out)
	}
	if !strings.Contains(out, "Recorded as a verified fixed point") {
		t.Errorf("a passing run on an unchanged tree records:\n%s", out)
	}
}

// postHookCompileError is what a post generate hook that compiles part of
// the project (`go generate ./internal/llm/tools`) reports on a tree whose
// hand-written code is not ported yet.
func postHookCompileError() *hookFailureError {
	return &hookFailureError{
		Hook:    "tool-catalog",
		Command: "go generate ./internal/llm/tools",
		Phase:   "post",
		Tail:    []string{"internal/llm/tools/registry.go:40:7: undefined: connect.NewRequest"},
		err:     errors.New("exit status 1"),
	}
}

// The hint and the end-of-run report cover a failed post hook as they cover
// a failed compile check; an input hook gets no hint, since the flag never
// keeps one.
func TestKeepOnBuildFailureReportsAndHintsHookFailures(t *testing.T) {
	var hint strings.Builder
	hintKeepOnBuildFailure(&hint, postHookCompileError())
	if !strings.Contains(hint.String(), "--keep-on-build-failure") {
		t.Errorf("a rolled-back post-hook failure must name the flag, got %q", hint.String())
	}
	input := postHookCompileError()
	input.Phase = "input"
	hint.Reset()
	hintKeepOnBuildFailure(&hint, input)
	if hint.Len() != 0 {
		t.Errorf("an input hook's failure is never kept, so it gets no hint, got %q", hint.String())
	}

	var b strings.Builder
	stderr, restore := captureStderr(t)
	reportKeptBuildFailure(&b, postHookCompileError(), nil, migrationCompileError())
	restore()
	if !strings.Contains(b.String(), "KEPT on disk") || !strings.Contains(b.String(), "2 failure(s)") {
		t.Errorf("the report must say the files were kept, counting both failures:\n%s", b.String())
	}
	for _, want := range []string{`generate hook "tool-catalog" failed`, "registry.go:40:7: undefined: connect.NewRequest", "service.go:12:9: undefined: connect.NewRequest"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("the report must repeat %q last:\n%s", want, stderr.String())
		}
	}
}

// The step itself, over real hook processes: without the flag the first
// failed post hook stops the hooks and fails the step (the run rolls back);
// with it the failure is a failed step the run goes on past, and the hooks
// after it still run.
func TestStepGenerateHooksKeepsOnHookFailure(t *testing.T) {
	testkit.Slow(t, "execs the test binary as each hook")
	t.Setenv("FORGE_GENERATE_HOOK_HELPER", "1")
	hooks := []config.GenerateHook{
		helperHook("tool-catalog", "", "stderr", "registry.go:40:7: undefined: connect.NewRequest", "fail"),
		helperHook("after", "", "write", "after.txt"),
	}
	newCtx := func(keep bool) *pipelineContext {
		cfg := &config.ProjectConfig{}
		cfg.Generate.Hooks = hooks
		return &pipelineContext{ProjectDir: t.TempDir(), Cfg: cfg, KeepOnBuildFailure: keep}
	}

	plain := newCtx(false)
	out, restore := captureStderr(t)
	stdout := captureStdout(t, func() {
		if err := stepGenerateHooks(plain); err == nil {
			t.Error("without the flag a failed post hook must fail the step, so the run rolls back")
		}
	})
	restore()
	if _, err := os.Stat(filepath.Join(plain.ProjectDir, "after.txt")); err == nil {
		t.Errorf("without the flag the hooks after a failed one must not run:\n%s%s", stdout, out)
	}

	kept := newCtx(true)
	out, restore = captureStderr(t)
	stdout = captureStdout(t, func() {
		if err := stepGenerateHooks(kept); err != nil {
			t.Errorf("with the flag the run must go on past a failed post hook, got %v", err)
		}
	})
	restore()
	if !slices.Equal(kept.SoftFailures, []string{generateHooksStep}) || len(kept.keptHookFailures) != 1 {
		t.Fatalf("the failed hook must be a failed step of the run, kept for its report: soft=%v kept=%v", kept.SoftFailures, kept.keptHookFailures)
	}
	if _, err := os.Stat(filepath.Join(kept.ProjectDir, "after.txt")); err != nil {
		t.Errorf("with the flag the hooks after a failed one still run: %v\n%s%s", err, stdout, out)
	}
	var stepsFailed *factory.StepsFailedError
	if err := softFailureOutcome(io.Discard, kept.SoftFailures, kept.softFailureCauses); !errors.As(err, &stepsFailed) {
		t.Errorf("a kept hook failure must still fail the run, got %v", err)
	}

	// An input hook is never kept: later steps project from its output.
	input := newCtx(true)
	for i := range input.Cfg.Generate.Hooks {
		input.Cfg.Generate.Hooks[i].Phase = "input"
	}
	_ = captureStdout(t, func() {
		if err := stepGenerateInputHooks(input); err == nil {
			t.Error("a failed input hook must fail the step even with the flag")
		}
	})
}

// TestGenerate_KeepOnBuildFailureKeepsOutputPastAHookThatFailsToCompile
// drives the real pipeline driver, journal and cache over the production
// post-hook step, with a hook — `go run ./tools/catalog` — whose program
// does not compile, as reliant's tool-catalog hook did mid-migration.
func TestGenerate_KeepOnBuildFailureKeepsOutputPastAHookThatFailsToCompile(t *testing.T) {
	f := newCacheFixture(t) // slow: builds a git repo; skipped under -short
	testkit.Slow(t, "each hook runs `go run`")
	withCacheSystem(t, f)
	origCheck := restoredTreeCompileCheck
	restoredTreeCompileCheck = func(string) rollbackConsistency { return rollbackConsistency{Checked: true, Builds: true} }
	t.Cleanup(func() { restoredTreeCompileCheck = origCheck })

	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(f.root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const brokenTool = "package main\n\nfunc main() { _ = connect.NewRequest }\n"
	write("tools/catalog/main.go", brokenTool)
	write("tools/after/main.go", "package main\n\nimport \"os\"\n\nfunc main() { _ = os.WriteFile(\"after.txt\", []byte(\"ok\"), 0o644) }\n")
	hooks := []config.GenerateHook{
		{Name: "tool-catalog", Command: []string{"go", "run", "./tools/catalog"}, Writes: []string{"internal/catalog"}},
		{Name: "after", Command: []string{"go", "run", "./tools/after"}, Writes: []string{"after.txt"}},
	}
	loadHooks := GenStep{
		Name:     "load hooks",
		Gate:     always,
		Tag:      "config",
		ReadOnly: true,
		Run: func(ctx *pipelineContext) error {
			cfg := &config.ProjectConfig{}
			cfg.Generate.Hooks = hooks
			ctx.Cfg = cfg
			return nil
		},
	}
	const genRel = "internal/widget/widget_gen.go"
	const genBody = "package widget // v2 output\n"
	emitter := GenStep{
		Name: "emit v2 output",
		Gate: always,
		Tag:  "codegen",
		Run: func(ctx *pipelineContext) error {
			checksums.RecordPreWrite(ctx.AbsPath, genRel)
			path := filepath.Join(ctx.AbsPath, filepath.FromSlash(genRel))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			return os.WriteFile(path, []byte(genBody), 0o644)
		},
	}
	withPipelineSteps(t, []GenStep{loadHooks, emitter, productionStep(t, generateHooksStep)})

	run := func(flags pipelineFlags) (string, error) {
		t.Helper()
		var runErr error
		stdout, stderr := captureStdoutAndStderr(t, func() { runErr = runGeneratePipelineFlags(f.root, flags) })
		return stdout + stderr, runErr
	}
	genPath := filepath.Join(f.root, filepath.FromSlash(genRel))
	afterPath := filepath.Join(f.root, "after.txt")

	// Default: the failed hook rolls the run back, and the report names the flag.
	out, err := run(pipelineFlags{Cache: true})
	if err == nil {
		t.Fatalf("a hook that fails to compile must fail the run:\n%s", out)
	}
	if _, statErr := os.Stat(genPath); !os.IsNotExist(statErr) {
		t.Errorf("without the flag the run's output must be rolled back, stat err = %v\n%s", statErr, out)
	}
	if _, statErr := os.Stat(afterPath); !os.IsNotExist(statErr) {
		t.Errorf("without the flag the hooks after the failed one must not run, stat err = %v", statErr)
	}
	for _, want := range []string{"undefined: connect", "--keep-on-build-failure"} {
		if !strings.Contains(out, want) {
			t.Errorf("a rolled-back hook failure must report %q:\n%s", want, out)
		}
	}

	// The flag: the output stays, the later hook ran, the compile error is
	// repeated, the run fails naming the hooks step, and it never records.
	out, err = run(pipelineFlags{Cache: true, KeepOnBuildFailure: true})
	var stepsFailed *factory.StepsFailedError
	if !errors.As(err, &stepsFailed) || !slices.Contains(stepsFailed.Steps, generateHooksStep) {
		t.Fatalf("a kept hook failure must still exit non-zero, naming the step; got %v\n%s", err, out)
	}
	if got, readErr := os.ReadFile(genPath); readErr != nil || string(got) != genBody {
		t.Fatalf("with the flag the generated file must stay on disk, got %q (%v)\n%s", got, readErr, out)
	}
	if _, statErr := os.Stat(afterPath); statErr != nil {
		t.Errorf("with the flag the hooks after the failed one still run: %v\n%s", statErr, out)
	}
	for _, want := range []string{"KEPT on disk", `generate hook "tool-catalog" failed`, "undefined: connect"} {
		if !strings.Contains(out, want) {
			t.Errorf("the run must report %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Recorded as a verified fixed point") || loadGenerateCacheManifest(f.root) != nil {
		t.Fatalf("a run whose hook failed must never be recorded as a verified fixed point:\n%s", out)
	}

	// Once the tool compiles, a plain run passes and records.
	write("tools/catalog/main.go", "package main\n\nfunc main() {}\n")
	out, err = run(pipelineFlags{Cache: true})
	if err != nil {
		t.Fatalf("a run whose hooks pass must pass: %v\n%s", err, out)
	}
	if strings.Contains(out, "nothing to do") {
		t.Fatalf("the next plain run must run every step after a kept failure:\n%s", out)
	}
}
