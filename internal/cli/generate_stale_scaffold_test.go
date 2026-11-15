// Tests for the stale scaffold-test detection (generate_stale_scaffold.go).
//
// All cases are pure-filesystem in t.TempDir() — no subprocesses, no
// network — so the file runs unconditionally in -short mode.
package cli

import (
	"path/filepath"
	"reflect"
	"testing"
)

// writeStaleScaffoldProject fabricates the minimal project layout the
// detector walks: a root go.mod (module path source), a
// handlers/<svc>/handlers_scaffold_test.go, and a gen/ package dir whose
// .pb.go declares a controlled set of names.
func writeStaleScaffoldProject(t *testing.T, scaffoldTest, pbGo string) string {
	t.Helper()
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "go.mod"), "module example.com/myapp\n\ngo 1.22\n")
	if scaffoldTest != "" {
		mustWriteFile(t, filepath.Join(dir, "internal", "handlers", "item", "handlers_scaffold_test.go"), scaffoldTest)
	}
	if pbGo != "" {
		mustWriteFile(t, filepath.Join(dir, "gen", "services", "item", "v1", "item.pb.go"), pbGo)
	}
	return dir
}

// scaffoldTestReferencingItem is a trimmed handlers_scaffold_test.go in
// the shape unit_test.go.tmpl renders: a pb-aliased gen import plus
// pb.<Ident> references in rows.
const scaffoldTestReferencingItem = `package item_test

import (
	"testing"

	pb "example.com/myapp/gen/services/item/v1"
)

func TestCreateItem_Generated(t *testing.T) {
	_ = &pb.CreateItemRequest{}
	var _ *pb.CreateItemResponse
	_ = pb.GetItemRequest{}
}
`

// TestStaleScaffoldDetection_Stale is the bug-shaped case: the user
// renamed Item → Bookmark in the proto, the gen package now declares
// only Bookmark types, but the one-shot scaffold test still references
// the deleted pb.*Item* types.
func TestStaleScaffoldDetection_Stale(t *testing.T) {
	t.Parallel()
	dir := writeStaleScaffoldProject(t, scaffoldTestReferencingItem, `package itemv1

type CreateBookmarkRequest struct{}

type CreateBookmarkResponse struct{}
`)

	findings := detectStaleScaffoldTests(dir)
	if len(findings) != 1 {
		t.Fatalf("detectStaleScaffoldTests = %d findings, want 1: %+v", len(findings), findings)
	}
	f := findings[0]
	wantPath := filepath.Join("internal", "handlers", "item", "handlers_scaffold_test.go")
	if f.RelPath != wantPath {
		t.Errorf("RelPath = %q, want %q", f.RelPath, wantPath)
	}
	wantMissing := []string{"CreateItemRequest", "CreateItemResponse", "GetItemRequest"}
	if !reflect.DeepEqual(f.Missing, wantMissing) {
		t.Errorf("Missing = %v, want %v", f.Missing, wantMissing)
	}
}

// TestStaleScaffoldDetection_AllDeclared is the negative case: every
// referenced pb ident is declared in the gen package (types AND funcs
// count as declarations) → no findings.
func TestStaleScaffoldDetection_AllDeclared(t *testing.T) {
	t.Parallel()
	dir := writeStaleScaffoldProject(t, scaffoldTestReferencingItem, `package itemv1

type CreateItemRequest struct{}

type CreateItemResponse struct{}

func GetItemRequest() {}
`)

	if findings := detectStaleScaffoldTests(dir); len(findings) != 0 {
		t.Fatalf("detectStaleScaffoldTests = %+v, want none (all idents declared)", findings)
	}
}

// TestStaleScaffoldDetection_NoPbImport: a scaffold test with no pb
// gen-import is skipped — nothing to cross-check.
func TestStaleScaffoldDetection_NoPbImport(t *testing.T) {
	t.Parallel()
	dir := writeStaleScaffoldProject(t, `package item_test

import "testing"

func TestPlaceholder(t *testing.T) {}
`, `package itemv1

type CreateBookmarkRequest struct{}
`)

	if findings := detectStaleScaffoldTests(dir); len(findings) != 0 {
		t.Fatalf("detectStaleScaffoldTests = %+v, want none (no pb import)", findings)
	}
}

// TestStaleScaffoldDetection_GenDirMissing: the generated package dir
// doesn't exist (codegen disabled / failed earlier) → silent skip, never
// a warning about a gen tree we didn't emit.
func TestStaleScaffoldDetection_GenDirMissing(t *testing.T) {
	t.Parallel()
	dir := writeStaleScaffoldProject(t, scaffoldTestReferencingItem, "")

	if findings := detectStaleScaffoldTests(dir); len(findings) != 0 {
		t.Fatalf("detectStaleScaffoldTests = %+v, want none (gen dir absent)", findings)
	}
}

// TestStaleScaffoldDetection_DeclaredInConnectFile: declarations in
// *connect*.go siblings satisfy references too (NewXServiceHandler-style
// funcs and connect wrappers live there).
func TestStaleScaffoldDetection_DeclaredInConnectFile(t *testing.T) {
	t.Parallel()
	dir := writeStaleScaffoldProject(t, `package item_test

import (
	pb "example.com/myapp/gen/services/item/v1"
)

var _ = pb.NewItemServiceHandler
`, `package itemv1
`)
	mustWriteFile(t, filepath.Join(dir, "gen", "services", "item", "v1", "item.connect.go"), `package itemv1

func NewItemServiceHandler() {}
`)

	if findings := detectStaleScaffoldTests(dir); len(findings) != 0 {
		t.Fatalf("detectStaleScaffoldTests = %+v, want none (declared in connect file)", findings)
	}
}

// TestStaleScaffoldWarningLine pins the exact one-line warning text per
// finding, both the single-ident and the "+N more" shapes.
func TestStaleScaffoldWarningLine(t *testing.T) {
	t.Parallel()
	multi := staleScaffoldFinding{
		RelPath: "handlers/item/handlers_scaffold_test.go",
		Missing: []string{"CreateItemRequest", "CreateItemResponse", "GetItemRequest"},
	}
	want := "handlers/item/handlers_scaffold_test.go references pb.CreateItemRequest (+2 more) that no longer exist in the generated proto — this one-shot scaffold test is yours: delete it or update its rows; forge will not regenerate it"
	if got := staleScaffoldWarning(multi); got != want {
		t.Errorf("warning =\n  %q\nwant\n  %q", got, want)
	}

	single := staleScaffoldFinding{
		RelPath: "handlers/item/handlers_scaffold_test.go",
		Missing: []string{"CreateItemRequest"},
	}
	wantSingle := "handlers/item/handlers_scaffold_test.go references pb.CreateItemRequest that no longer exist in the generated proto — this one-shot scaffold test is yours: delete it or update its rows; forge will not regenerate it"
	if got := staleScaffoldWarning(single); got != wantSingle {
		t.Errorf("warning =\n  %q\nwant\n  %q", got, wantSingle)
	}
}
