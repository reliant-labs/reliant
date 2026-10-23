package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The CRUD-handler step carries the two checks the generator makes
// *because* the alternative is silent data loss: a list filter naming no
// column, and a proto field whose column has no conversion. Both are worth
// nothing unless the step they live on is FATAL.
//
// It was not. `stepCRUDHandlers` ran through `ctx.warnOrFail`, which prints
// and returns nil unless --strict, so `forge generate` printed
// "✅ Code generation complete" and exited 0 on a field it had just
// identified as dead. Removing the wrapper is what gave the guard teeth.
//
// Nothing proved that. The only test that exercised the gate end to end
// carried `//go:build e2e`, which `go test ./...` does not run — so the
// wrapper could be reinstated, or reintroduced by a refactor, and the
// entire suite would stay green. A guard whose enforcement is invisible to
// the default test run is the same class of defect as the guard it
// replaced: it reports green either way.
//
// This test closes that. It asserts the PROPERTY — an error out of CRUD
// handler generation reaches the caller with Strict false — rather than the
// step's source text, so it survives the step being renamed, moved, or
// rewritten, and fails the moment the step becomes lenient again.
//
// Strict:false is the load-bearing part of the fixture: warnOrFail only
// swallows in the non-strict path, which is the DEFAULT every user gets.
func TestStepCRUDHandlers_IsFatalOnADefaultGenerate(t *testing.T) {
	dir := t.TempDir()

	// A descriptor the CRUD step cannot read. Any hard error out of
	// generateCRUDHandlers exercises the same wrapper; this one needs no
	// database, no protoc and no fixture project, so it stays in the inner
	// loop where a guard about a default `forge generate` belongs.
	if err := os.MkdirAll(filepath.Join(dir, "gen"), 0o755); err != nil {
		t.Fatalf("mkdir gen: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gen", "forge_descriptor.json"),
		[]byte("{ this is not json"), 0o644); err != nil {
		t.Fatalf("write descriptor: %v", err)
	}

	ctx := &pipelineContext{ProjectDir: dir, Strict: false}

	// Sanity: the fixture must actually reach the CRUD generator. If
	// rowServiceDefs failed first, this test would pass for the wrong
	// reason — that error path is upstream of the wrapper.
	if _, err := ctx.rowServiceDefs(); err != nil {
		t.Fatalf("fixture does not reach the CRUD generator: rowServiceDefs failed first: %v", err)
	}

	err := stepCRUDHandlers(ctx)
	if err == nil {
		t.Fatal("stepCRUDHandlers returned nil on a failing CRUD generation with Strict=false — " +
			"the step is warn-and-continue again, so every guard it carries (unmappable proto<->column " +
			"pairings, list filters naming no column) reports green on a default `forge generate`")
	}
	if !strings.Contains(err.Error(), "CRUD handler generation") {
		t.Errorf("the failure must name the step that produced it; got %v", err)
	}
}

// TestStepCRUDHandlers_NoServicesIsNotAFailure is the other half: the step
// is fatal on a real failure, not fatal on an absent one. A project with no
// descriptor yet (a fresh tree, before protoc-gen-forge has run) must pass
// through, or `forge generate` could never bootstrap itself.
func TestStepCRUDHandlers_NoServicesIsNotAFailure(t *testing.T) {
	ctx := &pipelineContext{ProjectDir: t.TempDir(), Strict: false}
	if err := stepCRUDHandlers(ctx); err != nil {
		t.Fatalf("stepCRUDHandlers must pass through a project with no descriptor; got %v", err)
	}
}
