package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
)

// These tests exercise --project-dir / -C, the flag that lets an in-process
// embedder target a project without mutating the process-global CWD.
//
// None of them call t.Parallel(): they use t.Chdir, and Go panics if a test
// that changed directory is also parallel.

// writeProject creates a minimal but loadable forge.yaml in a fresh temp dir
// and returns the dir. The name is echoed back in the config so a test can tell
// WHICH project got resolved, not merely that one did.
func writeProjectDirFixture(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	// t.TempDir can hand back a symlinked path (/var -> /private/var on
	// macOS); resolve it so comparisons against the absolute path forge
	// reports are apples-to-apples.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", dir, err)
	}
	body := "name: " + name + "\nmodule_path: github.com/example/" + name + "\n"
	if err := os.WriteFile(filepath.Join(resolved, "forge.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	return resolved
}

// clearProjectDir restores CWD-based resolution after a test installs an
// override, so one test cannot leak its root into the next.
func clearProjectDir(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if err := cmdutil.SetProjectDir(""); err != nil {
			t.Fatalf("clear project dir: %v", err)
		}
	})
}

// 1. -C <dir> resolves THAT project while the CWD is somewhere else entirely.
func TestProjectDirResolvesTargetWhileCwdElsewhere(t *testing.T) {
	clearProjectDir(t)
	target := writeProjectDirFixture(t, "target-project")

	// CWD is a directory with no forge.yaml anywhere beneath it... except
	// that a temp dir's ancestors are outside any forge project, which is
	// exactly the point: without the flag this CWD resolves nothing.
	elsewhere, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	t.Chdir(elsewhere)

	if _, err := findProjectConfigFile(); !errors.Is(err, ErrProjectConfigNotFound) {
		t.Fatalf("precondition: expected no project from CWD %s, got err=%v", elsewhere, err)
	}

	if err := cmdutil.SetProjectDir(target); err != nil {
		t.Fatalf("SetProjectDir(%s): %v", target, err)
	}

	got, err := findProjectConfigFile()
	if err != nil {
		t.Fatalf("findProjectConfigFile with -C %s: %v", target, err)
	}
	if want := filepath.Join(target, "forge.yaml"); got != want {
		t.Errorf("findProjectConfigFile() = %q, want %q", got, want)
	}

	// The loaded config is the TARGET's, not some other project's.
	cfg, err := loadProjectConfig()
	if err != nil {
		t.Fatalf("loadProjectConfig: %v", err)
	}
	if cfg.Name != "target-project" {
		t.Errorf("resolved project name = %q, want %q", cfg.Name, "target-project")
	}

	// The override must not have moved the process CWD — that is the whole
	// reason this flag exists rather than an os.Chdir.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if resolved, _ := filepath.EvalSymlinks(cwd); resolved != elsewhere {
		t.Errorf("CWD changed to %q; -C must never chdir (want %q)", resolved, elsewhere)
	}
}

// 2. Regression guard: with no flag, resolution walks up from the CWD exactly
// as it did before --project-dir existed.
func TestProjectDirUnsetResolvesFromCwd(t *testing.T) {
	clearProjectDir(t)
	project := writeProjectDirFixture(t, "cwd-project")

	// Resolution is a WALK UP, so start from a nested subdirectory to pin
	// that behavior too.
	nested := filepath.Join(project, "internal", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	t.Chdir(nested)

	got, err := findProjectConfigFile()
	if err != nil {
		t.Fatalf("findProjectConfigFile: %v", err)
	}
	if want := filepath.Join(project, "forge.yaml"); got != want {
		t.Errorf("findProjectConfigFile() = %q, want %q", got, want)
	}

	if _, ok := cmdutil.ProjectDirOverride(); ok {
		t.Error("no override should be installed when the flag is unset")
	}
}

// An explicitly empty -C means "no override": resolution stays on the CWD
// rather than becoming an error or resolving "".
func TestProjectDirEmptyClearsOverride(t *testing.T) {
	clearProjectDir(t)
	other := writeProjectDirFixture(t, "other-project")
	cwdProject := writeProjectDirFixture(t, "cwd-project")
	t.Chdir(cwdProject)

	if err := cmdutil.SetProjectDir(other); err != nil {
		t.Fatalf("SetProjectDir: %v", err)
	}
	if err := cmdutil.SetProjectDir(""); err != nil {
		t.Fatalf("SetProjectDir(\"\"): %v", err)
	}

	got, err := findProjectConfigFile()
	if err != nil {
		t.Fatalf("findProjectConfigFile: %v", err)
	}
	if want := filepath.Join(cwdProject, "forge.yaml"); got != want {
		t.Errorf("after clearing override, resolved %q, want %q", got, want)
	}
}

// 3. -C at a directory that is not (and is not inside) a forge project yields
// the ordinary ErrProjectConfigNotFound, not some new confusing error.
func TestProjectDirNonForgeDirYieldsNotFound(t *testing.T) {
	clearProjectDir(t)
	project := writeProjectDirFixture(t, "real-project")
	t.Chdir(project) // CWD *is* a project, so a leak would resolve successfully

	empty := t.TempDir()
	if err := cmdutil.SetProjectDir(empty); err != nil {
		t.Fatalf("SetProjectDir(%s): %v", empty, err)
	}

	_, err := findProjectConfigFile()
	if !errors.Is(err, ErrProjectConfigNotFound) {
		t.Fatalf("findProjectConfigFile() err = %v, want ErrProjectConfigNotFound", err)
	}
	if !errors.Is(err, cmdutil.ErrProjectConfigNotFound) {
		t.Error("error must be the shared cmdutil sentinel so every command group compares equal")
	}
}

// A -C pointing at something that does not exist, or is a file, fails loudly at
// the flag rather than degrading into a puzzling "no project found".
func TestProjectDirRejectsBadPaths(t *testing.T) {
	clearProjectDir(t)
	base := t.TempDir()
	file := filepath.Join(base, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	for _, tc := range []struct{ name, dir, wantSubstr string }{
		{"missing", filepath.Join(base, "nope"), "project dir"},
		{"file", file, "is not a directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := cmdutil.SetProjectDir(tc.dir)
			if err == nil {
				t.Fatalf("SetProjectDir(%s) = nil, want error", tc.dir)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("err = %q, want it to mention %q", err, tc.wantSubstr)
			}
			if _, ok := cmdutil.ProjectDirOverride(); ok {
				t.Error("a rejected -C must not install an override")
			}
		})
	}
}

// 4. A relative -C resolves against the CWD.
func TestProjectDirRelativeResolvesAgainstCwd(t *testing.T) {
	clearProjectDir(t)
	parent := t.TempDir()
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	child := filepath.Join(resolvedParent, "child-project")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}
	body := "name: child-project\nmodule_path: github.com/example/child\n"
	if err := os.WriteFile(filepath.Join(child, "forge.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}

	t.Chdir(resolvedParent)
	if err := cmdutil.SetProjectDir("child-project"); err != nil {
		t.Fatalf("SetProjectDir(relative): %v", err)
	}

	got, err := findProjectConfigFile()
	if err != nil {
		t.Fatalf("findProjectConfigFile: %v", err)
	}
	if want := filepath.Join(child, "forge.yaml"); got != want {
		t.Errorf("relative -C resolved to %q, want %q", got, want)
	}
}

// Every project-locating helper must honor the override, not just the one in
// config.go. cmdutil.ProjectRoot (exact-dir) and cmdutil.FindProjectRoot
// (walk-up) are the other two seams; if a future refactor adds an os.Getwd()
// back into either, this catches it.
func TestProjectDirHonoredByCmdutilResolvers(t *testing.T) {
	clearProjectDir(t)
	target := writeProjectDirFixture(t, "resolver-project")
	t.Chdir(t.TempDir())

	if err := cmdutil.SetProjectDir(target); err != nil {
		t.Fatalf("SetProjectDir: %v", err)
	}

	root, err := cmdutil.ProjectRoot()
	if err != nil {
		t.Fatalf("cmdutil.ProjectRoot: %v", err)
	}
	if root != target {
		t.Errorf("cmdutil.ProjectRoot() = %q, want %q", root, target)
	}

	found, err := cmdutil.FindProjectRoot()
	if err != nil {
		t.Fatalf("cmdutil.FindProjectRoot: %v", err)
	}
	if found != target {
		t.Errorf("cmdutil.FindProjectRoot() = %q, want %q", found, target)
	}

	// projectDirForKCL (build.go) is what `forge env list` and the KCL
	// render use to find deploy/kcl — the path the smoke test exercises.
	if got := projectDirForKCL(); got != target {
		t.Errorf("projectDirForKCL() = %q, want %q", got, target)
	}
}

// 5. THE ONE THAT MATTERS — and the one that documents a real limitation.
//
// The use case driving this flag is an in-process embedder (reliant's daemon)
// serving forge commands for many projects. The honest state of the
// implementation:
//
//   - Concurrent access to the resolution root is MEMORY-SAFE. An RWMutex
//     guards it, so concurrent Set/Get cannot race or tear. This test asserts
//     that much, and `go test -race` is what gives it teeth.
//
//   - Concurrent invocations with DIFFERENT -C values are NOT isolated. The
//     override is one process-global variable, so the last Set wins for every
//     in-flight command. An embedder MUST serialize forge invocations.
//
// That is still a strict improvement on the status quo it replaces: under
// os.Chdir, serializing forge calls was not sufficient either, because chdir
// corrupted relative paths on every other goroutine in the process. Now a
// caller-side mutex is a complete answer.
//
// Making concurrent differing -C values genuinely safe means threading the root
// per-invocation: the ~15 call sites of findProjectConfigFile / ProjectRoot /
// FindProjectRoot would each need it passed in (or a resolver read off the
// cobra command's context, which those leaf helpers do not currently receive).
// That is a mechanical but broad change, deliberately not bundled here.
func TestProjectDirConcurrentAccessIsMemorySafe(t *testing.T) {
	clearProjectDir(t)
	a := writeProjectDirFixture(t, "project-a")
	b := writeProjectDirFixture(t, "project-b")
	t.Chdir(t.TempDir())

	// Seed an override before the readers start. Otherwise a reader that
	// wins the race against every writer legitimately observes the CWD
	// fallback, which is correct behavior but not what this test is about.
	if err := cmdutil.SetProjectDir(a); err != nil {
		t.Fatalf("seed SetProjectDir: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		dir := a
		if i%2 == 1 {
			dir = b
		}
		wg.Add(1)
		go func(dir string) {
			defer wg.Done()
			if err := cmdutil.SetProjectDir(dir); err != nil {
				t.Errorf("SetProjectDir(%s): %v", dir, err)
			}
		}(dir)
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Whatever we observe must be one of the two roots — never
			// a torn value, and never empty (nothing clears it here).
			root, err := cmdutil.ResolutionRoot()
			if err != nil {
				t.Errorf("ResolutionRoot: %v", err)
				return
			}
			if root != a && root != b {
				t.Errorf("observed resolution root %q, want %q or %q", root, a, b)
			}
		}()
	}
	wg.Wait()
}

// The limitation above, pinned as an executable claim rather than a comment: a
// second Set overwrites the first for ALL callers. If someone makes the root
// per-invocation, this test SHOULD fail — that is the signal to delete it and
// enable a real isolation test in its place.
func TestProjectDirOverrideIsProcessGlobalNotPerInvocation(t *testing.T) {
	clearProjectDir(t)
	a := writeProjectDirFixture(t, "project-a")
	b := writeProjectDirFixture(t, "project-b")
	t.Chdir(t.TempDir())

	if err := cmdutil.SetProjectDir(a); err != nil {
		t.Fatalf("SetProjectDir(a): %v", err)
	}
	if err := cmdutil.SetProjectDir(b); err != nil {
		t.Fatalf("SetProjectDir(b): %v", err)
	}

	got, err := findProjectConfigFile()
	if err != nil {
		t.Fatalf("findProjectConfigFile: %v", err)
	}
	if want := filepath.Join(b, "forge.yaml"); got != want {
		t.Fatalf("resolved %q, want %q — if the root became per-invocation, "+
			"delete this test and replace it with a real concurrent-isolation test", got, want)
	}
}
