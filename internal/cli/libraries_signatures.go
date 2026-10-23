// File: internal/cli/libraries_signatures.go
//
// The deep half of `forge project libraries`: not just which forge/pkg
// packages exist, but what each one EXPORTS, with real signatures.
//
// WHY THIS EXISTS. The one-line-per-package inventory answers "does svcerr
// exist" — a question nobody was asking. A measured ten-unit fan-out wave
// issued 36 `go doc` calls out of 53 total unit bash calls: `go doc
// .../svcerr` in four separate threads, `go doc .../tdd RPCCase` in three,
// `go doc .../orm Context` in three. Every one of those threads had
// already been handed the inventory. They went to the toolchain because a
// synopsis cannot answer "what are svcerr.WithReason's parameters" or
// "what are the fields of tdd.Case" — and because the inventory's closing
// paragraph told them to go run exactly that command.
//
// The web-runtime half of this same verb has always listed symbols, and
// pasting THAT half into a unit brief demonstrably worked. This is the Go
// half catching up to a shape the command already proved.
//
// Everything here is ENUMERATED, never transcribed, on the same rule the
// rest of this verb follows:
//
//   - the DIRECTORY is still `go list -m`'s answer (resolveForgePkgDir),
//     so these are the signatures of the pkg version this project
//     compiles against, not of whatever the forge binary was built beside.
//   - the DECLARATIONS are parsed out of that directory's own .go files
//     and re-printed by go/printer, so a signature here IS the signature
//     in the source — not a summary of one, and not a copy of one.
//   - the FILTER is Go's own export rule (ast.IsExported), so a package
//     that gains an exported symbol gains a line here with no edit
//     anywhere, and one that unexports a symbol stops advertising it.
//
// There is deliberately no curated symbol list and no curated package
// list. A hand-maintained API digest is a lie with a timestamp: the
// hand-written `forge-libraries` skill this verb replaced listed a
// `pkg/dialects` that never existed while omitting nine packages that did.
//
// What IS cut is prose. The parse mode below excludes comments outright,
// so no doc text can reach this output even by accident — the synopsis
// line the inventory already prints above each block is the package's
// description, and the signatures are its API. `go doc` on the five
// packages the wave actually hammered is ~17,000 characters, the large
// majority of it doc comments.
package cli

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// allSignatures is the selector that means "every package in the
// inventory". It is spelled out rather than implied by a bare --signatures
// so that the flag never has an optional value: `--signatures orm` and
// `--signatures=orm` then behave identically, which a flag with an
// optional value does not.
const allSignatures = "all"

// LibrarySymbol is one exported declaration of a forge/pkg package, as
// that package's own source declares it.
//
// Decl is the rendered declaration: the full signature for a func, the
// exported fields for a struct, the method set for an interface. It is
// produced by go/printer from the parsed source, so it cannot say
// something the source does not.
type LibrarySymbol struct {
	// Kind is "const", "var", "func", "method" or "type".
	Kind string `json:"kind"`
	// Name is the exported identifier; for a method, "Type.Method".
	Name string `json:"name"`
	// Decl is the declaration as printed from source.
	Decl string `json:"decl"`
}

// readPackageSignatures returns every exported declaration of the Go
// package in dir, ordered the way `go doc` orders them: consts, vars,
// funcs, then each type followed by its own methods.
//
// Parsing beats shelling out to `go doc`: it needs no build (the module
// directory alone is enough, even when the package's own dependencies are
// not downloaded), it costs no process per package, and it returns
// structure rather than a screen-shaped blob the --json surface would have
// to re-parse.
//
// Build constraints are not evaluated — every non-test .go file in the
// directory contributes. For a library whose whole point is to be
// portable that over-reports nothing today; if forge/pkg ever grows
// per-GOOS files, a caller seeing both is still strictly better informed
// than a caller seeing neither.
func readPackageSignatures(dir string) ([]LibrarySymbol, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)

	fset := token.NewFileSet()
	var consts, vars, funcs []LibrarySymbol
	types := map[string]LibrarySymbol{}
	methods := map[string][]LibrarySymbol{}

	for _, name := range names {
		path := filepath.Join(dir, name)
		// No parser.ParseComments: doc text is the half this output
		// exists to omit, and not reading it is a stronger guarantee than
		// remembering to strip it.
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			// One unparseable file must not silently shrink the list. A
			// short API list reads as "the package is small", and a
			// caller who believes that writes the missing half by hand —
			// the exact failure this command exists to prevent.
			return nil, fmt.Errorf("parse %s: %w", path, perr)
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if !ast.IsExported(d.Name.Name) {
					continue
				}
				if d.Recv == nil {
					funcs = append(funcs, LibrarySymbol{
						Kind: "func", Name: d.Name.Name, Decl: renderFunc(fset, d),
					})
					continue
				}
				recv, ok := receiverTypeName(d.Recv)
				if !ok || !ast.IsExported(recv) {
					continue
				}
				methods[recv] = append(methods[recv], LibrarySymbol{
					Kind: "method", Name: recv + "." + d.Name.Name, Decl: renderFunc(fset, d),
				})
			case *ast.GenDecl:
				switch d.Tok {
				case token.CONST, token.VAR:
					if sym, ok := valueGroup(fset, d); ok {
						if d.Tok == token.CONST {
							consts = append(consts, sym)
						} else {
							vars = append(vars, sym)
						}
					}
				case token.TYPE:
					for _, spec := range d.Specs {
						ts, ok := spec.(*ast.TypeSpec)
						if !ok || !ast.IsExported(ts.Name.Name) {
							continue
						}
						types[ts.Name.Name] = LibrarySymbol{
							Kind: "type",
							Name: ts.Name.Name,
							Decl: "type " + render(fset, exportedTypeSpec(ts)),
						}
					}
				}
			}
		}
	}

	byName := func(s []LibrarySymbol) {
		sort.Slice(s, func(i, j int) bool { return s[i].Name < s[j].Name })
	}
	byName(consts)
	byName(vars)
	byName(funcs)

	typeNames := make([]string, 0, len(types))
	for n := range types {
		typeNames = append(typeNames, n)
	}
	sort.Strings(typeNames)

	out := make([]LibrarySymbol, 0, len(consts)+len(vars)+len(funcs)+len(types))
	out = append(out, consts...)
	out = append(out, vars...)
	out = append(out, funcs...)
	for _, n := range typeNames {
		out = append(out, types[n])
		ms := methods[n]
		byName(ms)
		out = append(out, ms...)
	}
	return out, nil
}

// renderFunc prints a function or method declaration without its body.
func renderFunc(fset *token.FileSet, d *ast.FuncDecl) string {
	clone := *d
	clone.Body = nil
	return render(fset, &clone)
}

// receiverTypeName is the bare type name a method hangs off, with the
// pointer and any type parameters stripped: `func (c *Client[T]) Do()` is
// a method on Client.
func receiverTypeName(recv *ast.FieldList) (string, bool) {
	if recv == nil || len(recv.List) != 1 {
		return "", false
	}
	expr := recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name, true
	case *ast.IndexExpr: // Client[T]
		if id, ok := t.X.(*ast.Ident); ok {
			return id.Name, true
		}
	case *ast.IndexListExpr: // Client[K, V]
		if id, ok := t.X.(*ast.Ident); ok {
			return id.Name, true
		}
	}
	return "", false
}

// valueGroup renders a const or var block, keeping only the specs that
// declare an exported name.
//
// The whole group is kept as one unit rather than split per name because
// an iota block only means anything as a block: `ModeDevelopment` on its
// own line, torn out of `ModeProduction RuntimeMode = iota`, has neither a
// type nor a value.
//
// Values are kept. A const's value is frequently the API itself
// (`ReasonHeader = "x-forge-error-reason"` is a header a caller has to
// write), and a var's value is what distinguishes sixteen identically
// shaped sentinel errors from each other.
func valueGroup(fset *token.FileSet, d *ast.GenDecl) (LibrarySymbol, bool) {
	clone := *d
	clone.Specs = nil
	first := ""
	for _, spec := range d.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		exported := false
		for _, n := range vs.Names {
			if ast.IsExported(n.Name) {
				exported = true
				if first == "" {
					first = n.Name
				}
			}
		}
		if !exported {
			continue
		}
		clone.Specs = append(clone.Specs, vs)
	}
	if len(clone.Specs) == 0 {
		return LibrarySymbol{}, false
	}
	kind := "const"
	if d.Tok == token.VAR {
		kind = "var"
	}
	return LibrarySymbol{Kind: kind, Name: first, Decl: render(fset, &clone)}, true
}

// exportedTypeSpec returns a copy of ts with unexported struct fields and
// unexported interface methods dropped.
//
// Unexported members are cut because they are unreachable from a project:
// printing one costs a line and buys the reader nothing they can call.
// Nothing else is cut — a struct's exported fields ARE its API, and
// `go doc -short` collapsing them to `struct{ ... }` is precisely why
// three separate threads had to run `go doc .../tdd RPCCase` by hand.
func exportedTypeSpec(ts *ast.TypeSpec) *ast.TypeSpec {
	clone := *ts
	switch t := ts.Type.(type) {
	case *ast.StructType:
		st := *t
		st.Fields = exportedFields(t.Fields, false)
		clone.Type = &st
	case *ast.InterfaceType:
		it := *t
		it.Methods = exportedFields(t.Methods, true)
		clone.Type = &it
	}
	return &clone
}

// exportedFields drops unexported entries from a struct's field list or an
// interface's method list. Entries with no name are kept: an embedded
// exported type is part of the surface, and a type-set element of a
// constraint interface IS the constraint.
func exportedFields(list *ast.FieldList, iface bool) *ast.FieldList {
	if list == nil {
		return nil
	}
	clone := *list
	clone.List = nil
	for _, f := range list.List {
		if len(f.Names) > 0 {
			var keep []*ast.Ident
			for _, n := range f.Names {
				if ast.IsExported(n.Name) {
					keep = append(keep, n)
				}
			}
			if len(keep) == 0 {
				continue
			}
			cf := *f
			cf.Names = keep
			clone.List = append(clone.List, &cf)
			continue
		}
		if !iface && !exportedEmbedded(f.Type) {
			continue
		}
		clone.List = append(clone.List, f)
	}
	return &clone
}

// exportedEmbedded reports whether an embedded struct field names an
// exported type.
func exportedEmbedded(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return exportedEmbedded(t.X)
	case *ast.Ident:
		return ast.IsExported(t.Name)
	case *ast.SelectorExpr: // pkg.Type — exported by construction
		return true
	case *ast.IndexExpr:
		return exportedEmbedded(t.X)
	case *ast.IndexListExpr:
		return exportedEmbedded(t.X)
	}
	return false
}

// render prints a node with go/printer and normalizes the whitespace a
// filtered AST leaves behind.
//
// Dropping fields from a struct leaves the surviving fields' original line
// numbers, which go/printer honours as blank lines. Nothing else occupies
// those lines — the parse never read the comments — so they are collapsed.
func render(fset *token.FileSet, node ast.Node) string {
	var buf bytes.Buffer
	// Spaces, not tabs: this output's destination is a pasted brief and a
	// JSON field, where a tab renders at whatever width the reader's
	// viewer picked.
	cfg := printer.Config{Mode: printer.UseSpaces, Tabwidth: 4}
	if err := cfg.Fprint(&buf, fset, node); err != nil {
		// go/printer only fails on a write error and this writes to
		// memory; naming the failure beats emitting a blank line.
		return fmt.Sprintf("<unprintable declaration: %v>", err)
	}
	var lines []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, strings.TrimRight(line, " \t"))
	}
	return strings.Join(lines, "\n")
}

// attachSignatures fills the Symbols field of the packages the selector
// list names. An empty list leaves the spec untouched, which is why the
// default output is unchanged by this file existing.
//
// SCOPE IS THE CALLER'S DECISION, and that is the whole reason this is a
// selector list rather than a curated set baked into forge.
//
// The obvious alternative — have forge decide which packages "a backend
// unit needs" and always print those — was rejected twice over. First, it
// is a hand-maintained list, the exact artifact the rest of this verb
// exists to abolish: the `forge-libraries` skill's hand-written table
// listed a `pkg/dialects` that never existed, and a hardcoded
// {svcerr, orm, tdd, testkit, crud} would rot the same way the moment
// forge/pkg grows the sixth package everyone needs. Second, it is a fact
// about the WORK, not about forge: a wave of frontend units needs none of
// them, a wave of worker units needs cmdkit and lifecyclekit, and forge
// does not know which wave is running. Under the same test forge.yaml is
// held to — decision or lookup? — this is a decision, so it is declared by
// the caller rather than guessed here.
//
// What forge does own is that no selector can be silently wrong: an
// unknown package or symbol is an error naming what does exist, so a
// briefing step whose selector list has gone stale fails loudly instead of
// quietly under-briefing ten units.
func attachSignatures(spec *LibrariesSpec, selectors string) error {
	sel, err := parseSignatureSelectors(selectors, spec.Packages)
	if err != nil {
		return err
	}
	if len(sel) == 0 {
		return nil
	}
	for i := range spec.Packages {
		want, ok := sel[spec.Packages[i].Name]
		if !ok {
			continue
		}
		syms, rerr := readPackageSignatures(spec.Packages[i].Dir)
		if rerr != nil {
			return rerr
		}
		if len(want) > 0 {
			syms, rerr = filterSymbols(spec.Packages[i].Name, syms, want)
			if rerr != nil {
				return rerr
			}
		}
		spec.Packages[i].Symbols = syms
	}
	spec.SignatureSelectors = normalizeSelectorList(selectors)
	return nil
}

// parseSignatureSelectors turns a `go doc`-shaped selector list into a
// per-package set of wanted symbol names. A package with an empty set
// means "every exported symbol".
//
// The grammar is `go doc`'s, deliberately: `svcerr` and `orm.Context` are
// what the units were already typing by hand, so there is no second
// convention to learn. `all` selects every package in the inventory.
func parseSignatureSelectors(list string, pkgs []LibrarySpec) (map[string]map[string]bool, error) {
	list = strings.TrimSpace(list)
	if list == "" {
		return nil, nil
	}
	known := make(map[string]bool, len(pkgs))
	names := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		known[p.Name] = true
		names = append(names, p.Name)
	}

	out := map[string]map[string]bool{}
	for _, raw := range strings.Split(list, ",") {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		if item == allSignatures {
			for _, p := range pkgs {
				out[p.Name] = map[string]bool{}
			}
			continue
		}
		// A full import path is accepted too: it is what `go doc` takes
		// and what the inventory prints, and rejecting it would be forge
		// being pedantic about a spelling it emitted itself.
		item = strings.TrimPrefix(item, forgePkgModule+"/")
		pkg, symbol, _ := strings.Cut(item, ".")
		if !known[pkg] {
			// Named without a flag prefix: the selector now arrives
			// positionally far more often than through --signatures, and an
			// error that quotes a flag the caller never typed reads as a bug
			// in forge rather than a typo in the argument.
			return nil, fmt.Errorf(
				"%s is not a forge/pkg package\nAvailable: %s\nUse `all` for every package, or `<pkg>.<Symbol>` for one declaration",
				pkg, strings.Join(names, ", "))
		}
		if _, seen := out[pkg]; !seen {
			out[pkg] = map[string]bool{}
		}
		if symbol != "" {
			out[pkg][symbol] = true
		} else {
			// A bare package name after a symbol selector widens to the
			// whole package rather than narrowing it.
			out[pkg] = map[string]bool{}
		}
	}
	return out, nil
}

// filterSymbols keeps the named symbols and, for a named type, the methods
// declared on it — a type whose methods were withheld is the shape that
// sends a reader straight back to `go doc`.
func filterSymbols(pkg string, syms []LibrarySymbol, want map[string]bool) ([]LibrarySymbol, error) {
	var out []LibrarySymbol
	found := map[string]bool{}
	for _, s := range syms {
		owner, _, isMethod := strings.Cut(s.Name, ".")
		switch {
		case want[s.Name]:
			found[s.Name] = true
		case isMethod && want[owner]:
			found[owner] = true
		default:
			continue
		}
		out = append(out, s)
	}
	for name := range want {
		if found[name] {
			continue
		}
		var exported []string
		for _, s := range syms {
			if !strings.Contains(s.Name, ".") {
				exported = append(exported, s.Name)
			}
		}
		return nil, fmt.Errorf(
			"%s exports no %s\nIt exports: %s",
			forgePkgModule+"/"+pkg, name, strings.Join(exported, ", "))
	}
	return out, nil
}

// normalizeSelectorList echoes the selectors back for the section header,
// so a pasted brief records which slice of the API it is carrying — and,
// just as importantly, which it is not.
func normalizeSelectorList(list string) string {
	var parts []string
	for _, raw := range strings.Split(list, ",") {
		if item := strings.TrimSpace(raw); item != "" {
			parts = append(parts, item)
		}
	}
	return strings.Join(parts, ", ")
}

// writeSignatures renders the deep section: one delimited block per
// selected package, in the inventory's order.
func writeSignatures(w io.Writer, spec LibrariesSpec) error {
	if spec.SignatureSelectors == "" {
		return nil
	}
	if _, err := fmt.Fprintf(w,
		"\nEXPORTED SIGNATURES — %s\n"+
			"Printed from the source resolved above, with doc prose omitted. This is the\n"+
			"complete exported surface of what was selected; nothing was summarized.\n",
		spec.SignatureSelectors); err != nil {
		return err
	}
	for _, p := range spec.Packages {
		if len(p.Symbols) == 0 {
			continue
		}
		if _, err := fmt.Fprintf(w, "\n── %s ──\n", p.ImportPath); err != nil {
			return err
		}
		for _, s := range p.Symbols {
			if _, err := fmt.Fprintln(w, s.Decl); err != nil {
				return err
			}
		}
	}
	return nil
}
