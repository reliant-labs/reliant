//go:build e2e

package cli

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/ast/astutil"
)

// TestE2EGenerateKeepsOpForShimMovedToSiblingFile reproduces the roofers
// dogfood failure through the REAL pipeline.
//
// An agent moved CreatePayment — still the scaffolded delegation to
// s.crudCreatePaymentOp() — out of the user-owned handlers_crud.go into a
// sibling invoice_ops.go. Go does not care which file declares a method;
// forge did. The next `forge generate` read "declared outside
// handlers_crud.go" as "implemented by hand", stopped emitting
// crudCreatePaymentOp, failed its own validate build with
// `s.crudCreatePaymentOp undefined`, and reverted 70 files under a ROOT
// CAUSE line that blamed missing imports.
//
// Pinned here:
//  1. The moved shim keeps its op: generate succeeds, the op is still in
//     handlers_crud_ops_gen.go, handlers_crud.go does not get a second
//     CreateGadget, and the project builds.
//  2. When validation DOES fail on a hand-written file, the ROOT CAUSE line
//     quotes the compiler error (file:line:col: message), not "exit status
//     1" and not the import advice.
func TestE2EGenerateKeepsOpForShimMovedToSiblingFile(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "moveapp", "--mod", "example.com/moveapp", "--service", "widget")
	projectDir := filepath.Join(dir, "moveapp")
	addCorpusForgePkgReplace(t, projectDir)

	protoPath := filepath.Join(projectDir, "proto", "services", "widget", "v1", "widget.proto")
	proto := readFileE2E(t, protoPath)
	proto += "\n// forge:entity\nmessage Gadget { string id = 1; string name = 2; }\n"
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("author widget proto: %v", err)
	}
	runCmd(t, projectDir, forgeBin, "scaffold")

	widgetDir := filepath.Join(projectDir, "internal", "handlers", "widget")
	shimPath := filepath.Join(widgetDir, "handlers_crud.go")
	opsPath := filepath.Join(widgetDir, "handlers_crud_ops_gen.go")
	if !strings.Contains(readFileE2E(t, opsPath), "func (s *Service) crudCreateGadgetOp()") {
		t.Fatalf("scaffold did not emit crudCreateGadgetOp — fixture is stale:\n%s", readFileE2E(t, opsPath))
	}

	// ── The agent's edit: move CreateGadget verbatim into a sibling ──────
	shimSrc := readFileE2E(t, shimPath)
	rest, moved := cutServiceMethodE2E(t, shimSrc, "CreateGadget")
	if !strings.Contains(moved, "s.crudCreateGadgetOp()") {
		t.Fatalf("scaffolded CreateGadget does not delegate to its op:\n%s", moved)
	}
	importBlock := importBlockE2E(t, shimSrc)
	writeGoPrunedE2E(t, shimPath, rest)
	writeGoPrunedE2E(t, filepath.Join(widgetDir, "gadget_ops.go"), "package widget\n\n"+importBlock+"\n\n"+moved+"\n")
	// The edit itself is sound: the package builds before any regenerate.
	runCmd(t, projectDir, "go", "build", "./...")

	// ── (1) Regenerate: the moved shim keeps its op ──────────────────────
	runCmd(t, projectDir, forgeBin, "generate")

	if ops := readFileE2E(t, opsPath); !strings.Contains(ops, "func (s *Service) crudCreateGadgetOp()") {
		t.Errorf("generate dropped crudCreateGadgetOp although gadget_ops.go calls it:\n%s", ops)
	}
	if got := strings.Count(readFileE2E(t, shimPath), "func (s *Service) CreateGadget("); got != 0 {
		t.Errorf("generate re-appended CreateGadget to handlers_crud.go (%d copies) although gadget_ops.go declares it", got)
	}
	runCmd(t, projectDir, "go", "build", "./...")

	// ── (2) A real validation failure names the real error ──────────────
	brokenPath := filepath.Join(widgetDir, "gadget_broken.go")
	broken := "package widget\n\nfunc (s *Service) frobnicate() error {\n\treturn s.crudFrobnicateGadgetOp()\n}\n"
	if err := os.WriteFile(brokenPath, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(forgeBin, "generate")
	cmd.Dir = projectDir
	outBytes, err := cmd.CombinedOutput()
	out := string(outBytes)
	if err == nil {
		t.Fatalf("generate must fail validation on gadget_broken.go, output:\n%s", out)
	}
	// The verdict line is the failing step's error: printed under the ROOT
	// CAUSE marker when the run reverted files, and as the run's last
	// error either way. The raw compiler dump also contains the coordinate,
	// so the assertion is on THIS line, not on the output as a whole.
	const coordinate = "internal/handlers/widget/gadget_broken.go:4:11: s.crudFrobnicateGadgetOp undefined"
	verdict := lineContainingE2E(out, "go build failed")
	t.Logf("validate verdict line: %s", verdict)
	if !strings.Contains(verdict, coordinate) {
		t.Errorf("the validate verdict must quote the compiler error with its coordinate; got %q\nfull output:\n%s", verdict, out)
	}
	if !strings.Contains(verdict, "gadget_broken.go is hand-written") {
		t.Errorf("the validate verdict must say whose file it is; got %q", verdict)
	}
	if strings.Contains(verdict, "ensure all referenced types are imported") || strings.Contains(verdict, "exit status 1") {
		t.Errorf("the validate verdict must not fall back to exit status / generic import advice; got %q", verdict)
	}
	if rootCause := lineAfterE2E(out, rollbackRootCauseMarker); rootCause != "" && !strings.Contains(rootCause, coordinate) {
		t.Errorf("ROOT CAUSE line must quote the compiler error; got %q", rootCause)
	}
	if err := os.Remove(brokenPath); err != nil {
		t.Fatal(err)
	}
}

// cutServiceMethodE2E removes a *Service method (and its doc comment) from
// src, returning the rest of the file and the method's source.
func cutServiceMethodE2E(t *testing.T, src, name string) (rest, method string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "handlers_crud.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse handlers_crud.go: %v", err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Name.Name != name {
			continue
		}
		start := fn.Pos()
		if fn.Doc != nil {
			start = fn.Doc.Pos()
		}
		from, to := fset.Position(start).Offset, fset.Position(fn.End()).Offset
		return src[:from] + src[to:], src[from:to]
	}
	t.Fatalf("method %s not found in handlers_crud.go:\n%s", name, src)
	return "", ""
}

// importBlockE2E returns src's import declaration verbatim.
func importBlockE2E(t *testing.T, src string) string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse imports: %v", err)
	}
	for _, decl := range file.Decls {
		if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
			return src[fset.Position(gd.Pos()).Offset:fset.Position(gd.End()).Offset]
		}
	}
	t.Fatalf("no import block in:\n%s", src)
	return ""
}

// writeGoPrunedE2E writes src to path with the imports it no longer uses
// removed — what an editor's goimports does when a method moves.
func writeGoPrunedE2E(t *testing.T, path, src string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, src)
	}
	for _, spec := range file.Imports {
		importPath, _ := strconv.Unquote(spec.Path.Value)
		if astutil.UsesImport(file, importPath) {
			continue
		}
		name := ""
		if spec.Name != nil {
			name = spec.Name.Name
		}
		astutil.DeleteNamedImport(fset, file, name, importPath)
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		t.Fatalf("format %s: %v", path, err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// lineContainingE2E returns the first line of out containing substr,
// trimmed, or "" when there is none.
func lineContainingE2E(out, substr string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, substr) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// lineAfterE2E returns the trimmed line following the first line that
// contains marker, or "" when there is none.
func lineAfterE2E(out, marker string) string {
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if strings.Contains(line, marker) && i+1 < len(lines) {
			return strings.TrimSpace(lines[i+1])
		}
	}
	return ""
}
