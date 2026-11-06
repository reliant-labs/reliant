package frontenddeps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// pinnedFixture lays out what pnpm 10 leaves behind when it switches to a
// pnpm 12 pin with the release's install script blocked: the pinned package
// unpacked under a tools directory, its bin a placeholder rather than the
// native binary, and a `pnpm` on PATH that reports the failed switch the way
// pnpm does. preinstall is the package's install step; it decides whether a
// repair works.
type pinnedFixture struct {
	project, placeholder, repairs string
}

func newPinnedFixture(t *testing.T, systemVersion, preinstall string) pinnedFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake package managers are sh scripts")
	}
	root := t.TempDir()
	f := pinnedFixture{
		project:     filepath.Join(root, "web"),
		placeholder: filepath.Join(root, "tools", "pnpm", "12.15.0", "node_modules", "pnpm", "pnpm"),
		repairs:     filepath.Join(root, "repairs"),
	}
	writeFileAt(t, f.project, "package.json", `{"packageManager":"pnpm@12.15.0"}`)
	pkg := filepath.Dir(f.placeholder)
	writeFileAt(t, pkg, "package.json", `{"name":"pnpm","version":"12.15.0","scripts":{"preinstall":`+
		`"echo run >> `+f.repairs+`; `+preinstall+`"}}`)
	writeFileAt(t, pkg, "pnpm", "# pnpm's native binary replaces this file during installation\n")
	bin := filepath.Join(root, "tools", "pnpm", "12.15.0", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.placeholder, filepath.Join(bin, "pnpm")); err != nil {
		t.Fatal(err)
	}
	// The system pnpm: an older release that switches to the pin by running
	// the tools copy, and reports node's spawn error when that copy is still
	// the placeholder.
	system := filepath.Join(root, "path", "pnpm")
	writeFileAt(t, filepath.Dir(system), "pnpm", `#!/bin/sh
if [ -n "`+systemVersion+`" ]; then echo `+systemVersion+`; exit 0; fi
if grep -q 'native binary replaces' `+f.placeholder+`; then
  echo "ERR_PNPM_VERSION_SWITCH_FAIL Failed to switch pnpm to v12.15.0. Looks like pnpm CLI is missing at \"`+bin+`\" or is incorrect" >&2
  echo "spawnSync `+filepath.Join(bin, "pnpm")+` ENOEXEC" >&2
  exit 1
fi
exec sh `+f.placeholder+` "$@"
`)
	if err := os.Chmod(system, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(system)+string(os.PathListSeparator)+os.Getenv("PATH"))
	return f
}

func (f pinnedFixture) repairRuns(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(f.repairs)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "run")
}

func TestVerifyPinnedRunnerAcceptsThePin(t *testing.T) {
	f := newPinnedFixture(t, "12.15.0", "true")
	if err := VerifyPinnedRunner(context.Background(), f.project, "pnpm"); err != nil {
		t.Fatalf("the pinned release on PATH was refused: %v", err)
	}
	if n := f.repairRuns(t); n != 0 {
		t.Errorf("a runnable pin was repaired %d times", n)
	}
}

// The owner's failure: the switch target is a placeholder. Running the
// package's own install step (what npm would have) makes it the real binary.
func TestVerifyPinnedRunnerRepairsAPlaceholderOnce(t *testing.T) {
	f := newPinnedFixture(t, "", `printf 'echo 12.15.0\n' > pnpm`)
	if err := VerifyPinnedRunner(context.Background(), f.project, "pnpm"); err != nil {
		t.Fatalf("the repaired pin was refused: %v", err)
	}
	if n := f.repairRuns(t); n != 1 {
		t.Errorf("install step ran %d times, want 1", n)
	}
}

// A repair that does not take is reported, with the fix, after ONE attempt:
// the broken executable is never handed to an install, nor retried.
func TestVerifyPinnedRunnerFailsActionablyWhenTheRepairDoesNotTake(t *testing.T) {
	f := newPinnedFixture(t, "", "true")
	err := VerifyPinnedRunner(context.Background(), f.project, "pnpm")
	if !errors.Is(err, ErrRunnerUnusable) {
		t.Fatalf("err = %v, want ErrRunnerUnusable", err)
	}
	for _, want := range []string{"ENOEXEC", "npm install -g pnpm@12.15.0", "re-running its install step"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
	if n := f.repairRuns(t); n != 1 {
		t.Errorf("install step ran %d times, want 1", n)
	}
	// Installing through the verifier never reaches the package manager.
	if err := Install(context.Background(), f.project, "", false); !errors.Is(err, ErrRunnerUnusable) {
		t.Errorf("Install = %v, want ErrRunnerUnusable", err)
	}
}

func TestVerifyPinnedRunnerRefusesAnotherRelease(t *testing.T) {
	f := newPinnedFixture(t, "10.26.0", "true")
	err := VerifyPinnedRunner(context.Background(), f.project, "pnpm")
	if !errors.Is(err, ErrRunnerUnusable) || !strings.Contains(err.Error(), `printed "10.26.0", not 12.15.0`) {
		t.Fatalf("err = %v, want the version mismatch", err)
	}
	if n := f.repairRuns(t); n != 0 {
		t.Errorf("a runnable other release was repaired %d times", n)
	}
}

func TestVerifyPinnedRunnerIgnoresUnpinnedFrontends(t *testing.T) {
	dir := t.TempDir()
	writeFileAt(t, dir, "package.json", `{}`)
	t.Setenv("PATH", "")
	if err := VerifyPinnedRunner(context.Background(), dir, "pnpm"); err != nil {
		t.Errorf("an unpinned frontend: %v", err)
	}
	writeFileAt(t, dir, "package.json", `{"packageManager":"npm@11.0.0"}`)
	if err := VerifyPinnedRunner(context.Background(), dir, "npm"); err != nil {
		t.Errorf("an npm frontend: %v", err)
	}
}

func TestUnstartableExecutable(t *testing.T) {
	for out, want := range map[string]string{
		"hint: spawnSync /home/u/.local/share/pnpm/.tools/pnpm/12.15.0/bin/pnpm ENOEXEC": "/home/u/.local/share/pnpm/.tools/pnpm/12.15.0/bin/pnpm",
		"fork/exec /usr/local/bin/pnpm: exec format error":                               "/usr/local/bin/pnpm",
		"ERR_PNPM_FETCH_404 GET https://registry.npmjs.org/x: Not Found":                 "",
	} {
		if got := unstartableExecutable(out, nil); got != want {
			t.Errorf("unstartableExecutable(%q) = %q, want %q", out, got, want)
		}
	}
}
