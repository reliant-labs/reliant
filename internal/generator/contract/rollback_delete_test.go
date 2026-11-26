// Regression tests for the aborted-generate atomicity defect.
//
// THE BUG. `forge generate` arms a rollback journal (checksums.BeginRollbackJournal)
// so a run that fails its own validation rewinds the tree to its pre-run state.
// The journal captures a path's pre-run bytes at the WRITE chokepoint —
// WriteGeneratedFile and friends call recordPreWrite before mutating.
//
// But contract codegen DELETES forge-owned files with a raw os.Remove that
// never touched that chokepoint: removeLegacyWrappers (here), and
// RemoveObservedDecorator. Deleting first and capturing later poisons the
// journal's baseline in the worst possible direction:
//
//  1. removeLegacyWrappers deletes internal/<pkg>/middleware_gen.go  (unjournaled)
//  2. WriteObservedDecorator rewrites it -> recordPreWrite runs NOW and,
//     seeing no file, records existed=false — "this path did not exist pre-run"
//  3. a later step fails; RestoreRollback honours existed=false by DELETING
//
// So the rewind that exists to hand the tree back clean is what removes the
// file, and the damage lands on a package unrelated to the actual failure. In
// the observed incident a proto validation error left internal/coupon with no
// middleware_gen.go, and every agent in the shared checkout got
// `undefined: couponsvc.NewServiceWithForgeMiddleware` from `go build ./...`.
//
// The fix is to journal the delete too. recordPreWrite is first-write-wins, so
// capturing at the delete records the TRUE pre-run bytes and the later write's
// capture is a no-op.
//
// These tests assert the tree is UNCHANGED after a rewind — not merely that
// generate returned an error.
package contract

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// legacyWrapperBody is a pre-run middleware_gen.go: bytes that exist on disk
// before the run and must survive a rewind byte-for-byte.
const legacyWrapperBody = `package thing

// NewServiceWithForgeMiddleware is the pre-run decorator — the symbol the
// rest of the project compiles against.
func NewServiceWithForgeMiddleware(inner Service) Service { return inner }
`

// TestRollback_RestoresLegacyWrapperDeletedByGenerate is the direct
// reproduction of the incident: a generate run deletes middleware_gen.go, the
// run then aborts, and the rewind must put the ORIGINAL back.
func TestRollback_RestoresLegacyWrapperDeletedByGenerate(t *testing.T) {
	root, _, opts := retireFixture(t)
	dir := filepath.Join(root, "internal", "thing")
	wrapperPath := filepath.Join(dir, "middleware_gen.go")

	// A middleware_gen.go from an earlier forge version, on disk pre-run.
	if err := os.WriteFile(wrapperPath, []byte(legacyWrapperBody), 0o644); err != nil {
		t.Fatalf("seed middleware_gen.go: %v", err)
	}

	// Arm the journal exactly as runGeneratePipelineFlags does.
	checksums.BeginRollbackJournal(root)
	t.Cleanup(checksums.CommitRollback)

	// The run: contract codegen rewrites the mock and sweeps the legacy
	// wrapper. This is the step that deletes the file.
	if err := GenerateWithOptions(filepath.Join(dir, "contract.go"), opts); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if _, err := os.Stat(wrapperPath); !os.IsNotExist(err) {
		t.Fatalf("precondition: generate should have deleted middleware_gen.go (stat err = %v)", err)
	}

	// ...and now a later step fails, so the pipeline rewinds.
	checksums.RestoreRollback(root)

	// The tree must be back to its pre-run state. Before the fix the file is
	// simply gone: the delete was never journaled, so the rewind had nothing
	// to restore — and `go build ./...` breaks on the missing symbol.
	got, err := os.ReadFile(wrapperPath)
	if err != nil {
		t.Fatalf("aborted generate left the tree LESS buildable than it found it: "+
			"middleware_gen.go was deleted and never restored (%v)", err)
	}
	if string(got) != legacyWrapperBody {
		t.Fatalf("rollback did not restore the pre-run bytes:\n got: %q\nwant: %q", got, legacyWrapperBody)
	}
}

// TestRollback_RestoresDecoratorRemovedByOptOut covers the sibling delete
// path: a package that is not (or no longer) instrumented has its decorator
// swept by RemoveObservedDecorator. Same rule — an aborted run must put it back.
func TestRollback_RestoresDecoratorRemovedByOptOut(t *testing.T) {
	root, _, _ := retireFixture(t)
	dir := filepath.Join(root, "internal", "thing")
	wrapperPath := filepath.Join(dir, "middleware_gen.go")

	if err := os.WriteFile(wrapperPath, []byte(legacyWrapperBody), 0o644); err != nil {
		t.Fatalf("seed middleware_gen.go: %v", err)
	}

	checksums.BeginRollbackJournal(root)
	t.Cleanup(checksums.CommitRollback)

	if err := RemoveObservedDecorator(dir); err != nil {
		t.Fatalf("RemoveObservedDecorator: %v", err)
	}
	if _, err := os.Stat(wrapperPath); !os.IsNotExist(err) {
		t.Fatalf("precondition: the decorator should have been removed (stat err = %v)", err)
	}

	checksums.RestoreRollback(root)

	got, err := os.ReadFile(wrapperPath)
	if err != nil {
		t.Fatalf("aborted generate left a swept decorator deleted rather than restored (%v)", err)
	}
	if string(got) != legacyWrapperBody {
		t.Fatalf("rollback did not restore the pre-run bytes:\n got: %q\nwant: %q", got, legacyWrapperBody)
	}
}

// TestRollback_RestoresRetiredArtifacts covers the third delete path: a
// package that opted out of contract codegen has its artifacts retired.
// Retirement is correct on a SUCCESSFUL run; on an aborted one it must rewind.
func TestRollback_RestoresRetiredArtifacts(t *testing.T) {
	root, rel, opts := retireFixture(t)
	mockPath := filepath.Join(root, filepath.FromSlash(rel))

	preRun, err := os.ReadFile(mockPath)
	if err != nil {
		t.Fatalf("read seeded mock: %v", err)
	}

	checksums.BeginRollbackJournal(root)
	t.Cleanup(checksums.CommitRollback)

	if _, err := RetireExcludedArtifacts(filepath.Join(root, "internal", "thing"), opts); err != nil {
		t.Fatalf("RetireExcludedArtifacts: %v", err)
	}
	if _, err := os.Stat(mockPath); !os.IsNotExist(err) {
		t.Fatalf("precondition: retirement should have removed the mock (stat err = %v)", err)
	}

	checksums.RestoreRollback(root)

	got, err := os.ReadFile(mockPath)
	if err != nil {
		t.Fatalf("aborted generate left a retired artifact deleted rather than restored (%v)", err)
	}
	if string(got) != string(preRun) {
		t.Fatalf("rollback did not restore the pre-run mock bytes")
	}
}
