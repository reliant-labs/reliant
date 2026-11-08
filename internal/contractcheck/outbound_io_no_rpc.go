// File: internal/contractcheck/outbound_io_no_rpc.go
//
// The forgeconv-outbound-io-no-rpc rule warns when a package marked
// `// forge:outbound-io` also imports `connectrpc.com/connect` and
// registers Connect RPC handlers (i.e., calls something matching
// `connect.NewXxxHandler(...)`).
//
// Why the marker is named for the property, not for a role
//
// `// forge:outbound-io` asserts one checkable thing: this package is
// an OUTBOUND I/O boundary — it calls out to a third-party system and
// nothing calls in. Two mechanisms read it, and both want exactly that
// property: this rule (an inbound RPC handler contradicts it) and the
// component-observe I/O heuristic in internal/cli/lint (a package that
// does I/O should be instrumented).
//
// The marker is named for the invariant, not for a package "kind", so
// that knowing WHEN to stamp it requires nothing but reading it. The
// `adapter` scaffold verb and skill keep the pattern name — "adapter"
// is standard hexagonal-architecture vocabulary for the DESIGN. This
// marker names the INVARIANT the design implies, which is the only
// part a linter can check.
//
// Detection
//
// Per-file walk over `internal/<pkg>/`:
//
//   1. Every file in the package contributes to a single decision per
//      package: is this an outbound boundary, and does it touch
//      `connectrpc.com/connect` in a handler-registration shape?
//   2. The package is treated as an outbound boundary when ANY file's
//      package doc comment contains the literal `forge:outbound-io`
//      tag (matching the `// forge:entity` / `// forge:operator-scheme`
//      style already used elsewhere in forge codegen).
//   3. An RPC-handler shape is a call expression whose function name
//      matches `connect.NewXxxHandler` (registration helper exposed by
//      buf-generated `*_connect.go` files), OR an import of any
//      `*_connect` package paired with such a call.
//
// Severity is warning — a boundary under construction may temporarily
// import connect for type re-exports, and the cost of a stray warning
// is low compared to the cost of letting an outbound boundary quietly
// grow into a service.

package contractcheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/linter/forgeconv"
)

// lintOutboundIONoRPC walks rootDir/internal/ for packages marked
// `// forge:outbound-io` and warns when any file in the package
// registers a Connect RPC handler. Returns findings in deterministic order
// (file, then line). A missing internal/ tree is not an error.
func lintOutboundIONoRPC(rootDir string) (forgeconv.Result, error) {
	internalDir := filepath.Join(rootDir, "internal")
	if _, err := os.Stat(internalDir); os.IsNotExist(err) {
		return forgeconv.Result{}, nil
	}

	// Collect package directories under internal/ — one warning per
	// package, attached to the first file we found the RPC shape in.
	var pkgDirs []string
	err := filepath.WalkDir(internalDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == "testdata" || d.Name() == "node_modules" || d.Name() == "vendor" {
			return filepath.SkipDir
		}
		// Only look at directories that actually contain Go files.
		entries, readErr := os.ReadDir(p)
		if readErr != nil {
			return nil
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
				pkgDirs = append(pkgDirs, p)
				break
			}
		}
		return nil
	})
	if err != nil {
		return forgeconv.Result{}, fmt.Errorf("walk %s: %w", internalDir, err)
	}
	sort.Strings(pkgDirs)

	var result forgeconv.Result
	for _, dir := range pkgDirs {
		findings, lintErr := lintOutboundIOPkg(dir, rootDir)
		if lintErr != nil {
			return forgeconv.Result{}, lintErr
		}
		result.Findings = append(result.Findings, findings...)
	}

	sort.SliceStable(result.Findings, func(i, j int) bool {
		if result.Findings[i].File != result.Findings[j].File {
			return result.Findings[i].File < result.Findings[j].File
		}
		if result.Findings[i].Line != result.Findings[j].Line {
			return result.Findings[i].Line < result.Findings[j].Line
		}
		return result.Findings[i].Rule < result.Findings[j].Rule
	})
	return result, nil
}

// lintOutboundIOPkg parses every .go file in a single package directory.
// If the package carries the `forge:outbound-io` marker AND any file
// invokes a Connect RPC handler shape, emit one warning per offending
// file (so the user can chase down each call site independently).
func lintOutboundIOPkg(pkgDir, rootDir string) ([]forgeconv.Finding, error) {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", pkgDir, err)
	}

	fset := token.NewFileSet()
	var (
		isOutboundIO bool
		rpcHits      []rpcHit
		parsedFiles  []*ast.File
	)

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		// Skip test files — boundary test files routinely import
		// connect for assertion helpers; that's not the foot-gun.
		if strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		fp := filepath.Join(pkgDir, e.Name())
		file, parseErr := parser.ParseFile(fset, fp, nil, parser.ParseComments|parser.SkipObjectResolution)
		if parseErr != nil {
			// Don't report parse errors — the Go toolchain already
			// surfaces them. Just skip the file.
			continue
		}
		parsedFiles = append(parsedFiles, file)
		if hasOutboundIOMarker(file) {
			isOutboundIO = true
		}
		rpcHits = append(rpcHits, findConnectHandlerCalls(fset, file, fp)...)
	}

	if !isOutboundIO || len(rpcHits) == 0 {
		_ = parsedFiles
		return nil, nil
	}

	// One finding per offending call site. Use module-relative paths
	// for stable output across machines.
	var findings []forgeconv.Finding
	for _, hit := range rpcHits {
		rel, relErr := filepath.Rel(rootDir, hit.path)
		if relErr != nil {
			rel = hit.path
		}
		findings = append(findings, forgeconv.Finding{
			Rule:     string(RuleOutboundIONoRPC),
			Severity: forgeconv.SeverityWarning,
			File:     rel,
			Line:     hit.line,
			Message: fmt.Sprintf(
				"package marked `// forge:outbound-io` registers Connect RPC handler %q; the marker asserts the package only calls OUT — an inbound RPC handler contradicts it",
				hit.calleeName),
			Remediation: "either drop the `// forge:outbound-io` marker (this package serves inbound traffic, so it is a service) or move the RPC handler to internal/handlers/<svc>/ and have it depend on this package's Service. Skill: forge skill load adapter",
		})
	}
	return findings, nil
}

// rpcHit captures one Connect-handler-registration call in a file.
type rpcHit struct {
	path       string
	line       int
	calleeName string
}

// hasOutboundIOMarker reports whether the file's package doc OR any
// top-level comment carries the `forge:outbound-io` directive. The
// directive is the package's claim to being an outbound boundary; we
// look for it in either the package doc (Go-standard place for a
// package directive) or any top-level comment group near the package
// clause (so users who put the marker on a separate line above
// `package foo` are still detected).
func hasOutboundIOMarker(f *ast.File) bool {
	if f.Doc != nil && containsForgeMarker(f.Doc.Text(), "outbound-io") {
		return true
	}
	// Comments before the package clause occasionally land in
	// f.Comments rather than f.Doc when there's a blank line between
	// them. Scan the early comment groups too.
	pkgPos := f.Package
	for _, cg := range f.Comments {
		if cg.End() >= pkgPos {
			break
		}
		if containsForgeMarker(cg.Text(), "outbound-io") {
			return true
		}
	}
	return false
}

// containsForgeMarker checks for a `forge:<name>` directive anywhere
// in the comment text. Mirrors the existing conventions
// (`// forge:entity`, `// forge:operator-scheme`) — match the `forge:`
// prefix + directive token, ignoring leading slash/space variations.
func containsForgeMarker(text, name string) bool {
	needle := "forge:" + name
	for _, line := range strings.Split(text, "\n") {
		// Trim leading `//` markers and whitespace; comment groups
		// strip them already, but be defensive.
		trimmed := strings.TrimLeft(line, "/ \t")
		if strings.HasPrefix(trimmed, needle) {
			return true
		}
	}
	return false
}

// findConnectHandlerCalls walks the file for call expressions matching
// the Connect RPC-handler-registration shape:
//
//   - a function call whose callee identifier starts with `New` and
//     ends with `Handler`, AND is qualified by an imported package
//     whose path contains a `_connect` segment OR is exactly
//     `connectrpc.com/connect`.
//
// Returns one rpcHit per match. The shape is intentionally narrow:
// `New<Svc>Handler` is the canonical name buf's protoc-gen-connect-go
// emits for every service.
func findConnectHandlerCalls(fset *token.FileSet, f *ast.File, path string) []rpcHit {
	connectAliases := connectImportAliases(f)
	if len(connectAliases) == 0 {
		return nil
	}
	var hits []rpcHit
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if !connectAliases[pkgIdent.Name] {
			return true
		}
		name := sel.Sel.Name
		if !strings.HasPrefix(name, "New") || !strings.HasSuffix(name, "Handler") {
			return true
		}
		hits = append(hits, rpcHit{
			path:       path,
			line:       fset.Position(call.Pos()).Line,
			calleeName: pkgIdent.Name + "." + name,
		})
		return true
	})
	return hits
}

// connectImportAliases returns the set of package names (as used in
// the file's selector expressions) for any import path that looks
// like a Connect-generated package: paths containing `_connect`
// (buf-generated `*_connect` packages) or paths matching
// `connectrpc.com/connect` itself.
//
// The returned map is keyed by the local-package name (alias or last
// path segment), which is what we'll see on the LHS of selector
// expressions like `userv1connect.NewUserHandler(...)`.
func connectImportAliases(f *ast.File) map[string]bool {
	out := map[string]bool{}
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		path := strings.Trim(imp.Path.Value, `"`)
		isConnect := strings.Contains(path, "_connect") ||
			path == "connectrpc.com/connect" ||
			strings.HasSuffix(path, "/connect")
		if !isConnect {
			continue
		}
		var alias string
		if imp.Name != nil {
			alias = imp.Name.Name
		} else {
			alias = path
			if i := strings.LastIndex(alias, "/"); i >= 0 {
				alias = alias[i+1:]
			}
		}
		if alias == "_" || alias == "." {
			continue
		}
		out[alias] = true
	}
	return out
}
