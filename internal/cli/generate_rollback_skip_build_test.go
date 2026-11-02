// The post-rollback compile check runs only when the failed run touched a Go
// build input.
//
// It used to run on every rollback. A run that failed a pre-flight check
// after rewriting nothing but forge.yaml then spent about 9 minutes on
// reliant building the whole project, to report a verdict about Go sources
// that were byte-for-byte the ones the user started with.
package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// rollbackFixture lays down a small Go project, arms the journal and takes
// the pre-run fingerprint, as the pipeline does before its first step. The
// returned counter reports how many times the compile check ran; the check
// itself is stubbed, so these tests never spawn a toolchain.
func rollbackFixture(t *testing.T) (root string, preRun *workTreeSnapshot, checks *int) {
	t.Helper()
	root = t.TempDir()
	for rel, body := range map[string]string{
		"go.mod":               "module example.com/app\n\ngo 1.22\n",
		"forge.yaml":           "name: app\n",
		"internal/app/app.go":  "package app\n",
		"frontends/web/api.ts": "export {}\n",
	} {
		writeRollbackFile(t, root, rel, body)
	}

	checksums.ResetSkipWrite()
	checksums.ResetPerRunState()
	t.Cleanup(checksums.ResetPerRunState)
	checksums.BeginRollbackJournal(root)
	t.Cleanup(checksums.CommitRollback)
	preRun = snapshotWorkTree(root)

	checks = new(int)
	orig := restoredTreeCompileCheck
	restoredTreeCompileCheck = func(string) rollbackConsistency {
		*checks++
		return rollbackConsistency{Checked: true, Builds: true}
	}
	t.Cleanup(func() { restoredTreeCompileCheck = orig })
	return root, preRun, checks
}

func writeRollbackFile(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// journaledWrite is a forge write: captured, then changed on disk.
func journaledWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	checksums.RecordPreWrite(root, rel)
	writeRollbackFile(t, root, rel, body)
}

func runRollback(t *testing.T, root string, preRun *workTreeSnapshot) string {
	t.Helper()
	stderr, restore := captureStderr(t)
	rollbackGeneratedTreeVerified(root, errors.New("pre-flight check failed"), false, preRun)
	restore()
	return stderr.String()
}

// The reported case: the run changed forge.yaml (and a frontend file) and
// failed. No Go input changed, so no build runs, and the report says why it
// makes no compile claim.
func TestRollback_NoGoFileChangedSkipsCompileCheck(t *testing.T) {
	root, preRun, checks := rollbackFixture(t)
	journaledWrite(t, root, "forge.yaml", "name: app\nkind: service\n")
	journaledWrite(t, root, "frontends/web/api.ts", "export const x = 1\n")
	// Journaled but rewritten with the bytes it had: not a change.
	journaledWrite(t, root, "internal/app/app.go", "package app\n")

	out := runRollback(t, root, preRun)

	if *checks != 0 {
		t.Errorf("the compile check ran %d time(s) after a run that changed no Go file", *checks)
	}
	if !strings.Contains(out, "back to its pre-run state") {
		t.Errorf("the faithful revert must still be reported:\n%s", out)
	}
	if !strings.Contains(out, "changed no Go file, so there was nothing to recompile") {
		t.Errorf("the report must say why it makes no compile claim:\n%s", out)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "forge.yaml")); string(got) != "name: app\n" {
		t.Errorf("forge.yaml was not restored: %q", got)
	}
}

// A Go file the run changed (and the revert put back) is what the check is
// for: a kept file may depend on the reverted one.
func TestRollback_GoFileChangedRunsCompileCheck(t *testing.T) {
	for name, rel := range map[string]string{
		"go source": "internal/app/app.go",
		"go.sum":    "go.sum",
		"new file":  "internal/app/new_gen.go",
	} {
		t.Run(name, func(t *testing.T) {
			root, preRun, checks := rollbackFixture(t)
			journaledWrite(t, root, "forge.yaml", "name: app\nkind: service\n")
			journaledWrite(t, root, rel, "package app // changed this run\n")

			out := runRollback(t, root, preRun)

			if *checks != 1 {
				t.Errorf("the compile check ran %d time(s), want 1 after a run that changed %s", *checks, rel)
			}
			if !strings.Contains(out, "verified: it compiles") {
				t.Errorf("the compile verdict must be reported:\n%s", out)
			}
		})
	}
}

// A Go file changed OUTSIDE the journal (an undeclared writer) is in the
// residue, and still owes the check.
func TestRollback_UnjournaledGoChangeRunsCompileCheck(t *testing.T) {
	root, preRun, checks := rollbackFixture(t)
	journaledWrite(t, root, "forge.yaml", "name: app\nkind: service\n")
	writeRollbackFile(t, root, "internal/app/app.go", "package app // an undeclared writer\n")

	runRollback(t, root, preRun)

	if *checks != 1 {
		t.Errorf("the compile check ran %d time(s), want 1: a Go file outside the journal changed", *checks)
	}
}

// Without a pre-run fingerprint nothing proves the tree outside the journal
// is untouched, so the check runs.
func TestRollback_NoFingerprintRunsCompileCheck(t *testing.T) {
	root, _, checks := rollbackFixture(t)
	journaledWrite(t, root, "forge.yaml", "name: app\nkind: service\n")

	runRollback(t, root, nil)

	if *checks != 1 {
		t.Errorf("the compile check ran %d time(s), want 1 when the residue cannot be checked", *checks)
	}
}

func TestIsGoBuildInput(t *testing.T) {
	for rel, want := range map[string]bool{
		"internal/app/app.go":          true,
		"internal/app/app_test.go":     true,
		"go.mod":                       true,
		"gen/go.sum":                   true,
		"go.work":                      true,
		"internal/native/shim.c":       true,
		"internal/native/shim.h":       true,
		"internal/asm/add_amd64.s":     true,
		"vendor/modules.txt":           true,
		"forge.yaml":                   false,
		"frontends/web/api.ts":         false,
		"db/migrations/1_init.up.sql":  false,
		"deploy/kcl/dev/main.k":        false,
		"internal/app/README.md":       false,
		"internal/app/go.mod.template": false,
	} {
		if got := isGoBuildInput(rel); got != want {
			t.Errorf("isGoBuildInput(%q) = %v, want %v", rel, got, want)
		}
	}
}
