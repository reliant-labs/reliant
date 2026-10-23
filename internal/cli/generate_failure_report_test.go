// Tests for the generate failure UX: failed-output preservation and
// compiler-output repetition (defects 1+3 of the failed-scaffold report).
//
// The observed failure: `go build` validation failed citing
// file:line coordinates in handlers_crud_ops_gen.go, the rollback then
// DELETED that file (debugging a compiler error against source you
// cannot read), and the compiler output scrolled away behind the long
// reverted-file list (tail -60 showed only bookkeeping).
//
// Pinned contract:
//
//   - rollbackGeneratedTree preserves every changed journaled file under
//     .forge/failed-generate/ (same relative paths) BEFORE rewinding,
//     writes error.txt with the full error there, says where it put
//     them, and REPEATS the compiler output at the END of the report.
//   - runGoBuildValidate returns a *validateBuildError carrying the full
//     `go build ./...` stderr, unwrapping to the user-facing message.
package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

func TestRollbackGeneratedTree_PreservesFailedSourcesAndRepeatsCompilerOutput(t *testing.T) {
	root := t.TempDir()

	checksums.ResetSkipWrite()
	checksums.ResetPerRunState()
	t.Cleanup(checksums.ResetPerRunState)
	checksums.BeginRollbackJournal(root)
	t.Cleanup(checksums.CommitRollback)

	// The failed run's writes: a Tier-1 ops file (chokepoint) and a
	// scaffold-once shim (write-if-missing) — the exact pair the real
	// failed scaffold run split apart.
	opsRel := filepath.Join("internal", "handlers", "orders", "handlers_crud_ops_gen.go")
	shimRel := filepath.Join("internal", "handlers", "orders", "handlers_crud.go")
	if _, err := checksums.WriteGeneratedFile(root, opsRel, []byte("package orders // broken ops\n"), &checksums.FileChecksums{}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := checksums.WriteScaffoldIfMissing(root, shimRel, []byte("package orders // shim\n")); err != nil {
		t.Fatal(err)
	}

	compilerOutput := "internal/handlers/orders/handlers_crud_ops_gen.go:264:22: cannot use &v (value of type *string) as string value\n" +
		"internal/handlers/orders/handlers_crud_ops_gen.go:301:9: undefined: convGadget\n"
	stepErr := &validateBuildError{
		Output: compilerOutput,
		err:    fmt.Errorf("forge generate (validate generated code): go build failed: exit status 1"),
	}

	stderr, restore := captureStderr(t)
	rolled := rollbackGeneratedTree(root, stepErr, false)
	restore()
	if !rolled {
		t.Fatal("rollbackGeneratedTree should report a rollback ran (journal was armed)")
	}
	out := stderr.String()

	// The tree is rewound — but the failed sources are inspectable.
	for _, rel := range []string{opsRel, shimRel} {
		if _, err := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(err) {
			t.Errorf("%s should be reverted, stat err = %v", rel, err)
		}
		preservedAt := filepath.Join(root, failedGenerateDir, rel)
		if _, err := os.Stat(preservedAt); err != nil {
			t.Errorf("%s must be preserved under %s: %v", rel, failedGenerateDir, err)
		}
	}

	// error.txt carries the full compiler output next to the sources.
	errFile, err := os.ReadFile(filepath.Join(root, failedGenerateDir, failedGenerateErrorFile))
	if err != nil {
		t.Fatalf("%s/%s must exist: %v", failedGenerateDir, failedGenerateErrorFile, err)
	}
	if !strings.Contains(string(errFile), compilerOutput) {
		t.Errorf("error file must contain the full compiler output, got:\n%s", errFile)
	}

	// The report says WHERE the preserved sources live…
	if !strings.Contains(out, failedGenerateDir) {
		t.Errorf("failure report must point at the preserve directory %s:\n%s", failedGenerateDir, out)
	}
	// …and repeats the compiler output AFTER the reverted-file list, so
	// the tail of the run shows the error, not just bookkeeping.
	revertedIdx := strings.Index(out, "reverted")
	compilerIdx := strings.LastIndex(out, "handlers_crud_ops_gen.go:264:22")
	if compilerIdx == -1 {
		t.Fatalf("compiler output must be repeated in the failure report:\n%s", out)
	}
	if revertedIdx == -1 || compilerIdx < revertedIdx {
		t.Errorf("compiler output must come AFTER the reverted-file list (tail-visibility), report:\n%s", out)
	}
}

func TestRollbackGeneratedTree_NonBuildErrorHasNoCompilerBlock(t *testing.T) {
	root := t.TempDir()
	checksums.ResetSkipWrite()
	checksums.ResetPerRunState()
	t.Cleanup(checksums.ResetPerRunState)
	checksums.BeginRollbackJournal(root)
	t.Cleanup(checksums.CommitRollback)
	if _, err := checksums.WriteGeneratedFile(root, "pkg/app/wire_gen.go", []byte("package app\n"), &checksums.FileChecksums{}, false); err != nil {
		t.Fatal(err)
	}

	stderr, restore := captureStderr(t)
	rollbackGeneratedTree(root, errors.New("step failed for a non-build reason"), false)
	restore()

	if strings.Contains(stderr.String(), "Compiler output") {
		t.Errorf("no compiler block should print for a non-build failure:\n%s", stderr.String())
	}
	// Preservation still happens — whatever the failing step, the run's
	// output stays inspectable.
	if _, err := os.Stat(filepath.Join(root, failedGenerateDir, "pkg", "app", "wire_gen.go")); err != nil {
		t.Errorf("failed output should still be preserved: %v", err)
	}
}

func TestReprintCompilerOutput_TruncatesLongOutput(t *testing.T) {
	var lines []string
	for i := 0; i < compilerOutputTailLines+40; i++ {
		lines = append(lines, fmt.Sprintf("pkg/x/file_gen.go:%d:1: error %d", i+1, i+1))
	}
	stepErr := &validateBuildError{Output: strings.Join(lines, "\n") + "\n", err: errors.New("go build failed")}

	stderr, restore := captureStderr(t)
	reprintCompilerOutput(stepErr, failedGenerateDir)
	restore()
	out := stderr.String()

	if !strings.Contains(out, lines[0]) {
		t.Errorf("first (most actionable) line must be shown:\n%s", out)
	}
	if strings.Contains(out, lines[len(lines)-1]) {
		t.Errorf("output beyond the cap should be elided:\n%s", out)
	}
	if !strings.Contains(out, "40 more line(s)") {
		t.Errorf("elision must say how much was cut and where the full output lives:\n%s", out)
	}
	if !strings.Contains(out, failedGenerateDir+"/"+failedGenerateErrorFile) {
		t.Errorf("elision must point at the preserved full output:\n%s", out)
	}
}

// TestRunGoBuildValidate_CarriesCompilerOutput exercises the REAL
// validate step against a deliberately non-compiling module and asserts
// the returned error carries the full compiler stderr for the failure
// report to repeat.
func TestRunGoBuildValidate_CarriesCompilerOutput(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/brokenapp\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() { undefinedSymbol() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The step streams to os.Stderr live; silence it for the test run.
	_, restore := captureStderr(t)
	err := runGoBuildValidate(dir)
	restore()
	if err == nil {
		t.Fatal("runGoBuildValidate should fail on a non-compiling module")
	}
	var ve *validateBuildError
	if !errors.As(err, &ve) {
		t.Fatalf("error should be a *validateBuildError, got %T: %v", err, err)
	}
	if !strings.Contains(ve.Output, "undefinedSymbol") {
		t.Errorf("captured compiler output should cite the failing symbol, got:\n%s", ve.Output)
	}
	if !strings.Contains(err.Error(), "go build failed") {
		t.Errorf("user-facing message should be preserved through the wrap: %v", err)
	}
}
