// Stale scaffold-test detection.
//
// FRICTION (front-door P0, 2026-06): the scaffold proto explicitly
// instructs renaming Item to the real entity. Following that instruction
// leaves handlers/<svc>/handlers_scaffold_<rpc>_test.go — ONE-SHOT, user-owned
// file rendered from internal/templates/service/unit_test.go.tmpl, never
// regenerated, no checksum entry — referencing deleted pb types
// (pb.CreateItemRequest, …). `forge generate` succeeds because the final
// validate step is `go build`, which does not compile _test.go files; the
// user then hits an immediate `go test` / `go vet` failure pointing at a
// file forge wrote, with no hint that the file is theirs to fix.
//
// Resolution: NO auto-regen of user-owned files. After codegen has
// refreshed gen/, scan each handlers/<svc>/handlers_scaffold_*_test.go (and
// only those — keep scope tight), resolve its `pb "<module>/gen/…"`
// import to the on-disk generated package, and cross-check every
// `pb.<Ident>` reference against the names that package actually declares.
// Any referenced-but-undeclared ident → one one-line warning naming the
// file and telling the user the file is theirs: delete it or update the
// rows. Default is a warning (generate still succeeds); --strict promotes
// it via the standard ctx.warnOrFail helper.
package cli

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
)

// The detector audits forge's born one-shot scaffold tests and nothing
// else — codegen.IsScaffoldTestFile is the single definition of which
// files those are (the per-RPC handlers_scaffold_<rpc>_test.go, plus the
// pre-split handlers_scaffold_test.go older projects still carry). Scope
// stays tight on purpose: this check exists for the rename-Item-first-day
// footgun, not as a general stale-test linter.

// staleScaffoldFinding is one scaffold test file referencing pb idents
// that the current generated package no longer declares.
type staleScaffoldFinding struct {
	// RelPath is the project-relative path of the scaffold test file.
	RelPath string
	// Missing is the sorted list of pb-qualified identifiers referenced
	// by the file but not declared in the generated package.
	Missing []string
}

// pbRefPattern matches `pb.<Ident>` selector references. Only idents
// starting with an uppercase letter are considered — lowercase selectors
// can't be exported pb declarations, and descriptor-style names
// (pb.File_…, pb.CreateItemRequest_builder) are uppercase and ARE real
// declarations in the gen package, so exact-match against declared names
// handles them without special-casing.
//
// The regexp also matches references inside comments. Accepted: a
// commented reference is only reported when the ident is genuinely
// undeclared in the gen package, and a comment naming a deleted type in
// a one-shot scaffold file is still a fair signal that the file is stale.
var pbRefPattern = regexp.MustCompile(`\bpb\.([A-Z][A-Za-z0-9_]*)`)

// detectStaleScaffoldTests walks handlers/ for forge's born scaffold test
// files and returns one finding per file that references pb idents absent
// from its generated package.
//
// Skip semantics (all silent — this is a safety net, never a new
// failure source):
//   - no handlers/ dir, or no scaffold test files → nothing to do
//   - file has no pb-aliased gen import → not the shape we audit
//   - the resolved gen package dir doesn't exist → codegen is off or
//     failed earlier; never warn about a gen tree we didn't emit
//   - a gen file that doesn't parse → its declarations are simply not
//     counted (downstream `go build` owns reporting broken gen files)
func detectStaleScaffoldTests(projectDir string) []staleScaffoldFinding {
	handlersDir := filepath.Join(projectDir, "internal", "handlers")
	var scaffoldFiles []string
	_ = filepath.WalkDir(handlersDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable subtree → skip, never fail
		}
		if !d.IsDir() && codegen.IsScaffoldTestFile(d.Name()) {
			scaffoldFiles = append(scaffoldFiles, path)
		}
		return nil
	})
	if len(scaffoldFiles) == 0 {
		return nil
	}
	sort.Strings(scaffoldFiles) // deterministic warning order

	// Module path drives the import-path → gen-dir mapping. Best-effort:
	// without a readable go.mod we fall back to splitting on "/gen/".
	modulePath, _ := codegen.GetModulePath(projectDir)

	var findings []staleScaffoldFinding
	for _, path := range scaffoldFiles {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		genDir := resolveScaffoldGenDir(projectDir, modulePath, content)
		if genDir == "" {
			continue // no pb gen import → not the audited shape
		}
		if fi, err := os.Stat(genDir); err != nil || !fi.IsDir() {
			continue // gen package absent → silent skip
		}

		declared := declaredGenNames(genDir)
		if declared == nil {
			continue // no parseable gen files → nothing to compare against
		}

		var missing []string
		seen := map[string]bool{}
		for _, m := range pbRefPattern.FindAllStringSubmatch(string(content), -1) {
			ident := m[1]
			if seen[ident] {
				continue
			}
			seen[ident] = true
			if !declared[ident] {
				missing = append(missing, ident)
			}
		}
		if len(missing) == 0 {
			continue
		}
		sort.Strings(missing)
		rel, err := filepath.Rel(projectDir, path)
		if err != nil {
			rel = path
		}
		findings = append(findings, staleScaffoldFinding{RelPath: rel, Missing: missing})
	}
	return findings
}

// resolveScaffoldGenDir parses the file's imports for the pb-aliased
// generated-package import (`pb "<module>/gen/<proto-pkg>/v1"`, the shape
// unit_test.go.tmpl renders) and maps it to the on-disk directory under
// <projectDir>/gen/. Returns "" when the file has no such import.
func resolveScaffoldGenDir(projectDir, modulePath string, content []byte) string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "scaffold_test.go", content, parser.ImportsOnly)
	if err != nil {
		return ""
	}
	for _, imp := range f.Imports {
		if imp.Name == nil || imp.Name.Name != "pb" {
			continue
		}
		importPath := strings.Trim(imp.Path.Value, `"`)
		// Primary mapping: gen/go.mod's module path is <module>/gen
		// (gen-go.mod.tmpl), so <module>/gen/<rest> lives at gen/<rest>.
		if modulePath != "" {
			if rest, ok := strings.CutPrefix(importPath, modulePath+"/gen/"); ok {
				return filepath.Join(projectDir, "gen", filepath.FromSlash(rest))
			}
		}
		// Fallback (no go.mod / foreign module path): first "/gen/"
		// segment. A module path containing "/gen/" would mis-split, but
		// that shape never comes out of forge's scaffolder.
		if _, rest, ok := strings.Cut(importPath, "/gen/"); ok {
			return filepath.Join(projectDir, "gen", filepath.FromSlash(rest))
		}
		return "" // pb alias pointing somewhere that isn't a gen import
	}
	return ""
}

// declaredGenNames collects every top-level declared identifier (types,
// funcs, vars, consts — exact declared names, no input/output guessing)
// from the generated package dir's *.pb.go and *connect*.go files.
// Returns nil when no gen file parsed, so the caller can distinguish
// "package declares nothing relevant" from "we couldn't read anything"
// and skip rather than flag every reference.
func declaredGenNames(genDir string) map[string]bool {
	entries, err := os.ReadDir(genDir)
	if err != nil {
		return nil
	}
	var declared map[string]bool
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if !strings.HasSuffix(name, ".pb.go") && !strings.Contains(name, "connect") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(genDir, name))
		if err != nil {
			continue
		}
		f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		if declared == nil {
			declared = map[string]bool{}
		}
		collectTopLevelNames(f, declared)
	}
	return declared
}

// collectTopLevelNames records every package-level declared name in f:
// type specs, value specs (vars/consts, incl. enum values and File_…
// descriptors), and non-method func decls. Methods are skipped — they
// are never referenced as pb.<Ident>.
func collectTopLevelNames(f *ast.File, into map[string]bool) {
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					into[s.Name.Name] = true
				case *ast.ValueSpec:
					for _, n := range s.Names {
						into[n.Name] = true
					}
				}
			}
		case *ast.FuncDecl:
			if d.Recv == nil {
				into[d.Name.Name] = true
			}
		}
	}
}

// staleScaffoldWarning renders the one-line warning for a finding:
//
//	handlers/item/handlers_scaffold_test.go references pb.CreateItemRequest
//	(+2 more) that no longer exist in the generated proto — this one-shot
//	scaffold test is yours: delete it or update its rows; forge will not
//	regenerate it
func staleScaffoldWarning(f staleScaffoldFinding) string {
	more := ""
	if n := len(f.Missing) - 1; n > 0 {
		more = fmt.Sprintf(" (+%d more)", n)
	}
	return fmt.Sprintf("%s references pb.%s%s that no longer exist in the generated proto — this one-shot scaffold test is yours: delete it or update its rows; forge will not regenerate it",
		filepath.ToSlash(f.RelPath), f.Missing[0], more)
}

// stepCheckStaleScaffoldTests is the GenStep wrapper. One warning line
// per stale file via the standard warnOrFail helper: default is a
// non-fatal warning (generate still succeeds); --strict promotes it to a
// pipeline failure. Kept in this file so the touch on
// generate_pipeline.go is a single registration line.
func stepCheckStaleScaffoldTests(ctx *pipelineContext) error {
	for _, f := range detectStaleScaffoldTests(ctx.AbsPath) {
		if err := ctx.warnOrFail("stale scaffold test check", errors.New(staleScaffoldWarning(f))); err != nil {
			return err
		}
	}
	return nil
}
