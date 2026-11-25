// File: internal/codegen/unwired_stub_excise.go
//
// Excision of pristine `forge:gen unwired-stub` handler methods — the
// stub→CRUD transition.
//
// The unwired-stub marker (see unwired_stub.go) means "forge-emitted
// placeholder": a proto RPC that had no implementation yet, so the handler
// template emitted a `func (s *Service) X(...) { return CodeUnimplemented }`
// into the RPC's own user-owned rpc_<name>.go (or, in a project born before
// the per-RPC split, into the shared handlers.go).
//
// When that RPC later becomes ENTITY-BACKED (a table is added so it is now
// a CRUD op), CRUD gen wants to own the method as a delegating shim. But
// the marked stub is still sitting in the handler package, and ScanExistingMethods
// would count it as "already implemented" and suppress the CRUD shim —
// leaving the RPC stuck on CodeUnimplemented forever, or (if forced)
// colliding as a duplicate method. CRUD gen therefore EXCISES the pristine
// marked stub for a method it is about to implement, so the generated CRUD
// shim takes over with no duplicate-method compile error.
//
// Only a PRISTINE marked stub is ever removed: the doc comment must still
// carry the marker AND the body must still be the untouched single-return
// CodeUnimplemented shape. A stub the user has EDITED (marker removed, or
// body changed) is user code and is left exactly where it is.

package codegen

import (
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/ast/astutil"

	"github.com/reliant-labs/forge/internal/checksums"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// ExciseUnwiredStubs removes, from the user-owned handler files in dir,
// every *Service method whose name is in `methods` that is still a pristine
// forge-emitted unwired stub. Returns the method names it removed (sorted
// by discovery order). Non-test, non-_gen.go files only — the marker only
// ever lands in the user-owned handler files. A missing dir is not an error
// (nothing to excise).
func ExciseUnwiredStubs(projectDir, dir string, methods map[string]bool) ([]string, error) {
	if len(methods) == 0 {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var removed []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_gen.go") {
			continue
		}
		p := filepath.Join(dir, name)
		// Remove one matching stub at a time, re-parsing between removals so
		// the byte offsets stay valid; loop until the file has no more.
		touched := false
		for {
			m, ok, rerr := removePristineUnwiredStub(projectDir, p, methods)
			if rerr != nil {
				return removed, rerr
			}
			if !ok {
				break
			}
			removed = append(removed, m)
			touched = true
		}
		if touched {
			if rerr := removeEmptiedRPCHandlerFile(p); rerr != nil {
				return removed, rerr
			}
		}
	}
	return removed, nil
}

// removeEmptiedRPCHandlerFile deletes a per-RPC handler file that excision
// just emptied.
//
// Since the stub scaffolder writes one file per RPC (writeRPCHandlerStubs),
// excising a file's only method leaves a bare `package x` husk. It compiles,
// so nothing breaks — it is simply litter forge wrote and would then leave
// behind for the user to notice and delete, which is the same "forge makes
// work it then asks you to undo" shape the per-RPC split exists to remove.
//
// Two guards keep this from ever reaching the user's own code:
//
//   - the filename must be one forge itself emits (RPCHandlerFileName's
//     rpc_ prefix), so a hand-named file — including the shared handlers.go
//     that pre-split projects still carry — is never removed, however empty
//     excision leaves it; and
//   - the file must have NO remaining declarations at all: no funcs, no
//     types, no vars, and no imports. One surviving line of the user's is
//     enough to keep the file.
//
// The delete is rollback-journaled like every other pipeline mutation, so a
// `forge generate` that fails later restores the file with the rest of the run.
func removeEmptiedRPCHandlerFile(path string) error {
	if !strings.HasPrefix(filepath.Base(path), rpcHandlerPrefix) {
		return nil
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return nil // it is already gone; nothing to clean up
	}
	fset := token.NewFileSet()
	file, perr := parser.ParseFile(fset, path, src, parser.ParseComments)
	if perr != nil || len(file.Decls) > 0 {
		return nil
	}
	// A doc comment or a stray note is content; only a truly bare file goes.
	for _, c := range file.Comments {
		if c.Pos() > file.Name.End() {
			return nil
		}
	}
	checksums.RecordPreWriteAbs(path)
	return os.Remove(path)
}

// removePristineUnwiredStub removes the FIRST pristine marked unwired stub
// in path whose method name is in `methods` (including its doc comment),
// prunes imports the removal orphaned, canonical-formats, and rewrites.
// Returns the removed method name and ok=true when it removed one; ok=false
// (no error) when there is nothing left to remove.
func removePristineUnwiredStub(projectDir, path string, methods map[string]bool) (string, bool, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	fset := token.NewFileSet()
	file, perr := parser.ParseFile(fset, path, src, parser.ParseComments)
	if perr != nil {
		return "", false, nil // unparseable user file — leave it alone
	}
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || !isPristineUnwiredStub(fd, methods) {
			continue
		}
		start := fd.Pos()
		if fd.Doc != nil {
			start = fd.Doc.Pos()
		}
		startOff := fset.Position(start).Offset
		endOff := fset.Position(fd.End()).Offset
		// Swallow trailing newlines so the excision doesn't leave a blank hole.
		for endOff < len(src) && src[endOff] == '\n' {
			endOff++
		}
		content := append([]byte{}, src[:startOff]...)
		content = append(content, src[endOff:]...)

		// Prune imports the removal orphaned.
		newFset := token.NewFileSet()
		edited, eerr := parser.ParseFile(newFset, path, content, parser.ParseComments)
		if eerr != nil {
			// A pristine single-return stub removal never breaks the parse; if
			// it somehow did, leave the file untouched rather than corrupt it.
			return "", false, nil
		}
		snapshot := make([]*ast.ImportSpec, len(edited.Imports))
		copy(snapshot, edited.Imports)
		for _, imp := range snapshot {
			ip := strings.Trim(imp.Path.Value, `"`)
			if astutil.UsesImport(edited, ip) {
				continue
			}
			nm := ""
			if imp.Name != nil {
				nm = imp.Name.Name
			}
			astutil.DeleteNamedImport(newFset, edited, nm, ip)
		}
		var buf strings.Builder
		if ferr := format.Node(&buf, newFset, edited); ferr != nil {
			return "", false, nil
		}
		if werr := formatAndRewriteGo(projectDir, path, []byte(buf.String())); werr != nil {
			return "", false, werr
		}
		return fd.Name.Name, true, nil
	}
	return "", false, nil
}

// isPristineUnwiredStub reports whether fd is an untouched forge-emitted
// unwired-stub handler method for a wanted method name: a *Service method,
// whose doc carries the `forge:gen unwired-stub` marker, whose body is the
// pristine single-return CodeUnimplemented shape.
func isPristineUnwiredStub(fd *ast.FuncDecl, wanted map[string]bool) bool {
	if fd.Recv == nil || fd.Name == nil || !wanted[fd.Name.Name] {
		return false
	}
	if !receiverIsService(fd) {
		return false
	}
	if fd.Doc == nil {
		return false
	}
	marked := false
	for _, c := range fd.Doc.List {
		if UnwiredStubMarkerRE.MatchString(c.Text) {
			marked = true
			break
		}
	}
	if !marked {
		return false
	}
	// Pristine body: exactly one return statement whose result is still an
	// Unimplemented constructor (the emitters' stub shape). A user who
	// changed the body — even keeping the marker — keeps their code.
	if fd.Body == nil || len(fd.Body.List) != 1 {
		return false
	}
	ret, ok := fd.Body.List[0].(*ast.ReturnStmt)
	if !ok {
		return false
	}
	return returnMentionsUnimplemented(ret)
}

// receiverIsService reports whether fd's receiver is *Service.
func receiverIsService(fd *ast.FuncDecl) bool {
	if fd.Recv == nil || len(fd.Recv.List) != 1 {
		return false
	}
	star, ok := fd.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	id, ok := star.X.(*ast.Ident)
	return ok && id.Name == "Service"
}

// unimplementedSelectors are the selector names an untouched forge stub
// body reaches for. `svcerr.ScaffoldStub` is what every emitter writes
// today; `svcerr.Unimplemented` is what they wrote before the
// scaffold-stub sentinel, and `connect.CodeUnimplemented` before the
// error-reason rewrite. Both older spellings are still on disk in projects
// that have not regenerated since, and excision has to keep recognising
// them or those stubs become permanently unexcisable.
//
// This list was `CodeUnimplemented` alone for two days after the emitters
// switched to svcerr, during which isPristineUnwiredStub matched NOTHING
// and the whole excision pass was a no-op — the marker survived, CRUD gen
// filtered the method out as "already implemented", and the RPC stayed on
// Unimplemented forever. TestExciseUnwiredStubs_MatchesTheEmittedShape
// feeds this the REAL rendered template so the pair cannot drift apart
// again.
//
// Note this set is BROADER than the scaffold test row's assertion, and the
// asymmetry is deliberate. Excision asks "does this look like a stub I may
// delete", where matching one spelling too many costs nothing (the marker
// and the pristine-shape check already gate it). The test row asks "has
// nobody implemented this", where matching one spelling too many is what
// made 78 rows pass against finished handlers.
var unimplementedSelectors = map[string]bool{
	"ScaffoldStub":      true,
	"Unimplemented":     true,
	"CodeUnimplemented": true,
}

// returnMentionsUnimplemented reports whether a return statement still
// constructs an Unimplemented error anywhere in its result expressions.
func returnMentionsUnimplemented(ret *ast.ReturnStmt) bool {
	found := false
	for _, r := range ret.Results {
		ast.Inspect(r, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel != nil && unimplementedSelectors[sel.Sel.Name] {
				found = true
				return false
			}
			return true
		})
	}
	return found
}

// formatAndRewriteGo canonical-formats edited Go source (goimports
// FormatOnly, project-local import grouping) and rewrites the file in place.
func formatAndRewriteGo(projectDir, absPath string, content []byte) error {
	rel, err := filepath.Rel(projectDir, absPath)
	if err != nil {
		rel = absPath
	}
	formatted, ferr := checksums.CanonicalGoSource(checksums.GoImportsLocalPrefix(projectDir), rel, content)
	if ferr != nil {
		return ferr
	}
	return atomicfile.WriteFile(absPath, formatted, 0o644)
}
