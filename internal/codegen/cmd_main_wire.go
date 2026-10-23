package codegen

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// Wiring a new component into cmd/<bin>/main.go.
//
// main.go is OWNED code — forge writes it once and then never touches it (see
// writeForgeScaffoldOnce and the header comment in cmd-main.go.tmpl). That
// intent is real and this file does not weaken it. What it removes is a
// SEPARATE problem that ownership was being used to justify: forge is willing
// to render a fully-wired main.go at `project new` time, from exactly the same
// inputs, but on the incremental path it printed instructions and made the
// caller hand-edit. The measured cost of that gap was four tool calls and ~35
// seconds per scaffold — reading root.go, grepping for the Execute signature,
// grepping for the constructor, then editing.
//
// The resolution is an APPEND, not a re-derivation, and it is the same shape
// forge already uses for the other user-owned file it must extend
// (AppendWorkloadStanza → deploy/kcl/workloads.k):
//
//   - It adds ONE argument line and, if needed, ONE import line. Every other
//     byte of the file is left exactly as the user left it.
//   - It uses the AST to LOCATE the insertion points and then splices TEXT at
//     those offsets, rather than reprinting the file from the tree. Reprinting
//     is what a go/format round-trip does, and it reflows things the user
//     chose: a multi-line cmd.Execute call collapses onto one line, and any
//     alignment or grouping they maintained is normalized away. The result
//     compiles and is unreviewable — a large diff in a file whose entire
//     purpose is that a human reads it. Locating with the AST keeps the
//     robustness (the call is found wherever they moved it) without the
//     rewrite.
//   - When it does NOT recognize the file, it changes nothing and says so, and
//     the caller falls back to printing the instructions it prints today.
//
// A whole-file re-derive was the other candidate and is deliberately not what
// this does. Re-deriving would silently discard whatever the user put in main.go
// — the composition root is where people put log setup, profiling hooks, and
// build-tag branches — and it would make an ordinary `forge scaffold` a
// destructive operation on a file forge promised not to touch. An append can be
// wrong in only one direction (it declines), which is the direction that costs
// a printed instruction rather than someone's afternoon.

// WireOutcome reports what WireComponentIntoMain did, so the caller can
// print the right thing.
type WireOutcome int

const (
	// WireUnrecognized means forge did not find the structure it appends to
	// and changed NOTHING. The caller must print the manual instruction.
	WireUnrecognized WireOutcome = iota
	// WireApplied means the constructor (and any missing import) was added.
	WireApplied
	// WireAlreadyWired means the constructor was already referenced; the
	// file was not touched. This is the --resume/--force re-run case.
	WireAlreadyWired
)

func (o WireOutcome) String() string {
	switch o {
	case WireApplied:
		return "applied"
	case WireAlreadyWired:
		return "already-wired"
	default:
		return "unrecognized"
	}
}

// WireComponentIntoMain appends `<group>.<ctor>` to the cmd.Execute(...) call
// in cmd/<bin>/main.go, adding the group subpackage import when it is absent.
//
// It NEVER rewrites a file it cannot confidently parse and locate the call in:
// every failure to recognize returns WireUnrecognized with the file untouched,
// so the caller can print the manual instruction instead. The error return is
// reserved for a real I/O failure on a file forge DID recognize (e.g. the
// write itself failing) — an unrecognized or absent main.go is an outcome, not
// an error, because it is a normal state for a project whose owner rewrote it.
func WireComponentIntoMain(root, bin, modulePath, group, ctor string) (WireOutcome, error) {
	rel := filepath.Join("cmd", bin, "main.go")
	path := filepath.Join(root, rel)
	src, err := os.ReadFile(path)
	if err != nil {
		return WireUnrecognized, nil // absent or unreadable: not ours to guess at
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return WireUnrecognized, nil // hand-rolled into something we can't read
	}

	call := findExecuteCall(file)
	if call == nil {
		return WireUnrecognized, nil
	}

	// Already wired? Compare on the AST, not on a substring: a match inside
	// a comment or a string literal is not a registration.
	for _, arg := range call.Args {
		if sel, ok := arg.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == group && sel.Sel.Name == ctor {
				return WireAlreadyWired, nil
			}
		}
	}

	// Collect the two splices, then apply them back-to-front so the earlier
	// offset is still valid when the later one has already been inserted.
	var edits []textEdit

	argEdit, ok := argumentEdit(fset, src, call, group, ctor)
	if !ok {
		return WireUnrecognized, nil
	}
	edits = append(edits, argEdit)

	importPath := modulePath + "/cmd/" + bin + "/cmd/" + group
	if !hasImport(file, importPath) {
		impEdit, ok := importEdit(fset, src, file, importPath)
		if !ok {
			return WireUnrecognized, nil
		}
		edits = append(edits, impEdit)
	}

	out := applyEdits(src, edits)

	// The splice must still be Go. It always is for the shapes we recognize,
	// but a file is never written on the strength of that reasoning alone.
	if _, err := parser.ParseFile(token.NewFileSet(), path, out, parser.AllErrors); err != nil {
		return WireUnrecognized, nil
	}

	// Journal the pre-write state so a failed generate run rewinds this edit
	// along with everything else the run wrote — same discipline as every
	// other write into a project tree.
	checksums.RecordPreWriteAbs(path)
	if err := atomicfile.WriteFile(path, out, 0o644); err != nil {
		return WireUnrecognized, fmt.Errorf("write %s: %w", rel, err)
	}
	return WireApplied, nil
}

// textEdit is one splice into the original source: text inserted at offset,
// replacing the `replace` bytes that follow it. replace is 0 for the ordinary
// pure-insertion case; the single-import promotion is the one edit that
// rewrites bytes rather than only adding them.
type textEdit struct {
	offset  int
	replace int
	text    string
}

// applyEdits splices every edit into src, back-to-front so each offset still
// refers to the original bytes when it is applied.
func applyEdits(src []byte, edits []textEdit) []byte {
	sorted := append([]textEdit(nil), edits...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].offset > sorted[j].offset })
	out := src
	for _, e := range sorted {
		var buf bytes.Buffer
		buf.Write(out[:e.offset])
		buf.WriteString(e.text)
		buf.Write(out[e.offset+e.replace:])
		out = buf.Bytes()
	}
	return out
}

// argumentEdit builds the splice that appends `<group>.<ctor>` to the call,
// MATCHING the layout the call already uses:
//
//   - a multi-line arg list (the scaffolded shape, and the one people keep)
//     gets a new line indented like the last argument, with the trailing
//     comma the list already has;
//   - a single-line call gets `, <group>.<ctor>` inline.
//
// Reporting the layout from the source rather than imposing one is the whole
// point: the user's file keeps looking like the user's file.
func argumentEdit(fset *token.FileSet, src []byte, call *ast.CallExpr, group, ctor string) (textEdit, bool) {
	rparen := fset.Position(call.Rparen)
	if !rparen.IsValid() || rparen.Offset <= 0 || rparen.Offset > len(src) {
		return textEdit{}, false
	}
	ref := group + "." + ctor

	// Empty arg list: `cmd.Execute()`. Open it into the multi-line shape the
	// template uses, so the next append has a list to extend.
	if len(call.Args) == 0 {
		indent := lineIndent(src, rparen.Offset)
		return textEdit{
			offset: rparen.Offset,
			text:   "\n" + indent + "\t" + ref + ",\n" + indent,
		}, true
	}

	// Anchor on the last argument FROM THE SAME GROUP when there is one, and
	// on the last argument overall otherwise. The scaffolded call labels its
	// sections (`// services`, `// workers`, `// operators`) and keeps each
	// group's constructors together; appending a service after the final
	// operator compiles fine and leaves the file reading as a lie, which in
	// the composition root — the file that exists to be read — is the whole
	// cost being avoided.
	anchor := lastArgInGroup(call.Args, group)
	if anchor == nil {
		anchor = call.Args[len(call.Args)-1]
	}
	lastEnd := fset.Position(anchor.End())
	if !lastEnd.IsValid() || lastEnd.Offset > len(src) {
		return textEdit{}, false
	}

	// Single-line call: append inline after the last argument.
	if lastEnd.Line == rparen.Line {
		return textEdit{offset: lastEnd.Offset, text: ", " + ref}, true
	}

	// Multi-line: insert a line after the anchor's line, indented to match it.
	// Extending to end-of-line steps past the trailing comma and any line
	// comment, so the new line lands after a complete entry rather than
	// inside one.
	insertAt := endOfLine(src, lastEnd.Offset)
	indent := lineIndent(src, lastEnd.Offset)
	return textEdit{offset: insertAt, text: "\n" + indent + ref + ","}, true
}

// lastArgInGroup returns the final argument of the form `<group>.Something`,
// or nil when the call has none. That is the entry a new member of the same
// group belongs beside.
func lastArgInGroup(args []ast.Expr, group string) ast.Expr {
	var found ast.Expr
	for _, arg := range args {
		sel, ok := arg.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == group {
			found = arg
		}
	}
	return found
}

// importEdit builds the splice that adds importPath to the file's import
// block, placed in sorted position among the specs it will sit beside so the
// result is already what gofmt/goimports would keep.
func importEdit(fset *token.FileSet, src []byte, file *ast.File, importPath string) (textEdit, bool) {
	quoted := strconv.Quote(importPath)

	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.IMPORT {
			continue
		}

		// A single unparenthesized `import "x"` cannot hold a second spec, so
		// this one splice does promote it to a block — the only case where
		// forge rewrites an existing line. It is a mechanical, one-line,
		// gofmt-canonical transform of a declaration that has exactly one
		// possible shape, and the alternative is refusing to wire the
		// greenfield composition root, which is precisely the file that most
		// needs wiring.
		if gen.Lparen == token.NoPos {
			if len(gen.Specs) != 1 {
				return textEdit{}, false
			}
			spec, ok := gen.Specs[0].(*ast.ImportSpec)
			if !ok || spec.Path == nil || spec.Name != nil {
				return textEdit{}, false
			}
			start := fset.Position(gen.Pos())
			end := fset.Position(spec.End())
			if !start.IsValid() || !end.IsValid() || end.Offset > len(src) {
				return textEdit{}, false
			}
			first, second := spec.Path.Value, quoted
			if strings.Compare(second, first) < 0 {
				first, second = second, first
			}
			return textEdit{
				offset:  start.Offset,
				replace: end.Offset - start.Offset,
				text:    "import (\n\t" + first + "\n\t" + second + "\n)",
			}, true
		}
		if len(gen.Specs) == 0 {
			return textEdit{}, false
		}

		// Insert before the first spec that sorts after us; otherwise after
		// the last one. Both are line insertions at the block's own indent.
		for _, spec := range gen.Specs {
			imp, ok := spec.(*ast.ImportSpec)
			if !ok || imp.Path == nil || imp.Name != nil {
				continue
			}
			if strings.Compare(imp.Path.Value, quoted) <= 0 {
				continue
			}
			pos := fset.Position(imp.Pos())
			if !pos.IsValid() || pos.Offset > len(src) {
				return textEdit{}, false
			}
			start := startOfLine(src, pos.Offset)
			return textEdit{offset: start, text: lineIndent(src, pos.Offset) + quoted + "\n"}, true
		}

		last, ok := gen.Specs[len(gen.Specs)-1].(*ast.ImportSpec)
		if !ok || last.Path == nil {
			return textEdit{}, false
		}
		pos := fset.Position(last.Pos())
		end := fset.Position(last.End())
		if !pos.IsValid() || !end.IsValid() || end.Offset > len(src) {
			return textEdit{}, false
		}
		return textEdit{
			offset: endOfLine(src, end.Offset),
			text:   "\n" + lineIndent(src, pos.Offset) + quoted,
		}, true
	}

	// No import declaration at all. A main.go with no imports is not the
	// composition root — decline and let the caller print the instruction.
	return textEdit{}, false
}

// lineIndent returns the leading whitespace of the line containing offset.
func lineIndent(src []byte, offset int) string {
	start := startOfLine(src, offset)
	i := start
	for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	return string(src[start:i])
}

// startOfLine returns the offset just after the newline preceding offset.
func startOfLine(src []byte, offset int) int {
	if offset > len(src) {
		offset = len(src)
	}
	if i := bytes.LastIndexByte(src[:offset], '\n'); i >= 0 {
		return i + 1
	}
	return 0
}

// endOfLine returns the offset of the newline ending the line containing
// offset (or len(src) when the file does not end in one).
func endOfLine(src []byte, offset int) int {
	if offset > len(src) {
		return len(src)
	}
	if i := bytes.IndexByte(src[offset:], '\n'); i >= 0 {
		return offset + i
	}
	return len(src)
}

// findExecuteCall locates the `<pkg>.Execute(...)` call inside func main.
// Scoped to main's body on purpose: a helper elsewhere in the file that
// happens to call something named Execute is not the composition root, and
// appending to it would wire the component into nothing.
func findExecuteCall(file *ast.File) *ast.CallExpr {
	var found *ast.CallExpr
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Name.Name != "main" || fn.Recv != nil || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if found != nil {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != "Execute" {
				return true
			}
			if _, ok := sel.X.(*ast.Ident); !ok {
				return true
			}
			found = call
			return false
		})
	}
	return found
}

// hasImport reports whether the file already imports importPath.
func hasImport(file *ast.File, importPath string) bool {
	for _, spec := range file.Imports {
		if spec.Path == nil {
			continue
		}
		if p, err := strconv.Unquote(spec.Path.Value); err == nil && p == importPath {
			return true
		}
	}
	return false
}
