// File: internal/linter/forgeconv/product_event_pii.go
//
// forgeconv-product-event-pii — a product event property whose NAME invites
// personal data: email, name, ip, url, path or token, alone or as one
// `_`/`.`/`-` separated part of the key (`user_email`, `file_path`).
//
// A product event's properties are tags: enums, booleans, counts, buckets.
// They are stored per event for months and joined to a user, so a
// free-text value there is personal data kept far longer than any log line.
// The emitters already drop VALUES that look like an email, an IP or a
// credential at run time (pkg/observe's LooksLikePII), with a WARN; this rule
// catches the property at review time, before anything is dropped, and also
// catches what no value pattern can (a person's name, a repo path).
//
// The analysis is syntactic: every call to Track (observe or cliev) and to
// TrackAuth, and every slog.String/Int/Bool/Any/… or slog.Attr{Key: …}
// argument with a literal key. Warning; an error under --strict. The usual
// `// forge:lint-disable-next-line forgeconv-product-event-pii: <why>` is the
// escape hatch for a key that only looks like one (`plan_name` naming a
// fixed plan).

package forgeconv

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const ruleProductEventPII = "forgeconv-product-event-pii"

// productEventEmitters are the packages whose Track functions record
// product events.
var productEventEmitters = []string{
	"github.com/reliant-labs/forge/pkg/observe",
	"github.com/reliant-labs/forge/pkg/observe/cliev",
}

// productEventPIIWords are the key parts that name personal data.
var productEventPIIWords = map[string]bool{
	"email": true, "name": true, "ip": true, "url": true, "path": true, "token": true,
}

// LintProductEventPII walks rootDir for Go files and reports product event
// properties whose key names personal data. Generated files (gen/,
// *_gen.go), vendor/, node_modules/ and testdata/ are skipped.
func LintProductEventPII(rootDir string, opts LintOptions) (Result, error) {
	sev := SeverityWarning
	if opts.Strict {
		sev = SeverityError
	}
	var result Result
	fset := token.NewFileSet()
	err := filepath.WalkDir(rootDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "gen", "testdata", ".forge":
				if p != rootDir {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_gen.go") {
			return nil
		}
		src, rerr := os.ReadFile(p)
		if rerr != nil || !strings.Contains(string(src), "forge/pkg/observe") {
			return nil
		}
		file, perr := parser.ParseFile(fset, p, src, parser.SkipObjectResolution)
		if perr != nil {
			return nil // the Go toolchain reports parse errors
		}
		rel, relErr := filepath.Rel(rootDir, p)
		if relErr != nil {
			rel = p
		}
		result.Findings = append(result.Findings, lintProductEventPIIFile(fset, file, filepath.ToSlash(rel), sev)...)
		return nil
	})
	if err != nil {
		return Result{}, fmt.Errorf("walk %s: %w", rootDir, err)
	}
	sort.SliceStable(result.Findings, func(i, j int) bool {
		a, b := result.Findings[i], result.Findings[j]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	return result, nil
}

func lintProductEventPIIFile(fset *token.FileSet, file *ast.File, rel string, sev Severity) []Finding {
	emitters := map[string]bool{}
	for _, path := range productEventEmitters {
		if name := importName(file, path); name != "" {
			emitters[name] = true
		}
	}
	slogName := importName(file, "log/slog")
	if len(emitters) == 0 || slogName == "" {
		return nil
	}
	var findings []Finding
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || !emitters[pkg.Name] || (sel.Sel.Name != "Track" && sel.Sel.Name != "TrackAuth") {
			return true
		}
		event := ""
		for i, arg := range call.Args {
			if i == 1 {
				if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					event, _ = strconv.Unquote(lit.Value)
				}
			}
			key, pos := attrKey(arg, slogName)
			if key == "" || !keyNamesPII(key) {
				continue
			}
			what := "a product event property"
			if event != "" {
				what = fmt.Sprintf("product event %q's property", event)
			}
			findings = append(findings, Finding{
				Rule:     ruleProductEventPII,
				Severity: sev,
				File:     rel,
				Line:     fset.Position(pos).Line,
				Message: fmt.Sprintf("%s %q names personal data (email, name, ip, url, path, token): event properties are "+
					"stored for months and joined to a user, and the emitter drops values that look like PII at run time", what, key),
				Remediation: "record a tag instead: an enum, a boolean, a count or a bucket (`has_email`, `domain_kind`, " +
					"`path_depth`). A key that only looks like one takes " +
					"`// forge:lint-disable-next-line " + ruleProductEventPII + ": <why>`.",
			})
		}
		return true
	})
	return findings
}

// attrKey returns the literal key of a slog attr expression:
// slog.String("k", …) and the other constructors, or slog.Attr{Key: "k"}.
func attrKey(arg ast.Expr, slogName string) (string, token.Pos) {
	switch e := arg.(type) {
	case *ast.CallExpr:
		sel, ok := e.Fun.(*ast.SelectorExpr)
		if !ok || len(e.Args) == 0 {
			return "", 0
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != slogName {
			return "", 0
		}
		if lit, ok := e.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			k, _ := strconv.Unquote(lit.Value)
			return k, lit.Pos()
		}
	case *ast.CompositeLit:
		for _, el := range e.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Key" {
				if lit, ok := kv.Value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					k, _ := strconv.Unquote(lit.Value)
					return k, lit.Pos()
				}
			}
		}
	}
	return "", 0
}

// keyNamesPII reports whether one `_`, `.` or `-` separated part of key is a
// personal-data word.
func keyNamesPII(key string) bool {
	for _, part := range strings.FieldsFunc(strings.ToLower(key), func(r rune) bool { return r == '_' || r == '.' || r == '-' }) {
		if productEventPIIWords[part] {
			return true
		}
	}
	return false
}
