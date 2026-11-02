// Package pkgguard holds the repository-wide proof that forge/pkg — the
// runtime library that compiles INTO every generated binary — obeys the rules
// forge's own generated projects are held to.
//
// It exists because pkg/ went unchecked for a long time. It used to be a
// SEPARATE Go module, and `go test ./...` and `golangci-lint run` at the repo
// root both operate on the module they are invoked in, so for as long as CI
// only ran them from the root, nothing in pkg/ was ever tested or linted.
// Twelve direct environment reads accumulated outside the sanctioned readers
// — in a library whose shipped lint config fails a user's build for the same
// call. pkg/ is part of the root module now, so the root `go test ./...`
// reaches it, but the root lint config still excludes it: pkg/ is linted
// only under pkg/.golangci.yml, by a run from pkg/.
//
// The scanner lives here (rather than in the test) so its inputs are
// explicit: it derives BOTH the forbidden calls and the exempt packages from
// pkg/.golangci.yml, the file that actually gates CI. Nothing here hardcodes
// a function name or a file list — a rule added to that config is enforced by
// this guard on the next run, and a rule deleted from it stops being
// enforced loudly rather than silently.

//forge:exclude-contract: analyzer-shaped guard: package-level funcs over a directory tree plus a predicate on a parsed value; the test IS the consumer
package pkgguard

import (
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

	yaml "gopkg.in/yaml.v3"
)

// ForbidigoPolicy is the forbidigo half of a golangci-lint config: whether
// the linter GATES (membership in linters.enable, not merely a settings
// block), what it forbids, and which paths are exempted.
type ForbidigoPolicy struct {
	// Enabled reports whether forbidigo is in linters.enable. A settings
	// block alone configures a linter that never runs.
	Enabled bool
	// Forbid are the compiled `forbid[].pattern` regexes, matched against a
	// call expression's rendered form ("os.Getenv") exactly as forbidigo
	// matches them.
	Forbid []*regexp.Regexp
	// Exempt are the compiled `exclusions.rules[].path` regexes of every
	// rule that names forbidigo, matched against the pkg/-relative path.
	Exempt []*regexp.Regexp
}

// Finding is one forbidden reference: where it is and what it named.
type Finding struct {
	// Path is relative to the scanned root (pkg/) and slash-separated —
	// the form pkg/.golangci.yml's exclusion paths are written against.
	Path string
	Line int
	// Ref is the reference in canonical form — "os.Getenv" — whatever the
	// file imported the package as.
	Ref string
}

func (f Finding) String() string { return fmt.Sprintf("%s:%d: %s", f.Path, f.Line, f.Ref) }

// yamlConfig is the subset of a golangci-lint v2 config this guard reads.
type yamlConfig struct {
	Linters struct {
		Enable   []string `yaml:"enable"`
		Settings struct {
			Forbidigo struct {
				Forbid []struct {
					Pattern string `yaml:"pattern"`
				} `yaml:"forbid"`
			} `yaml:"forbidigo"`
		} `yaml:"settings"`
		Exclusions struct {
			Rules []struct {
				Path    string   `yaml:"path"`
				Linters []string `yaml:"linters"`
			} `yaml:"rules"`
		} `yaml:"exclusions"`
	} `yaml:"linters"`
}

// LoadForbidigoPolicy parses the golangci-lint config at path.
func LoadForbidigoPolicy(path string) (ForbidigoPolicy, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // a repo-relative config path
	if err != nil {
		return ForbidigoPolicy{}, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg yamlConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return ForbidigoPolicy{}, fmt.Errorf("parse %s: %w", path, err)
	}
	var p ForbidigoPolicy
	for _, l := range cfg.Linters.Enable {
		if l == "forbidigo" {
			p.Enabled = true
		}
	}
	for _, f := range cfg.Linters.Settings.Forbidigo.Forbid {
		re, cerr := regexp.Compile(f.Pattern)
		if cerr != nil {
			return ForbidigoPolicy{}, fmt.Errorf("%s: forbid pattern %q: %w", path, f.Pattern, cerr)
		}
		p.Forbid = append(p.Forbid, re)
	}
	for _, r := range cfg.Linters.Exclusions.Rules {
		named := false
		for _, l := range r.Linters {
			if l == "forbidigo" {
				named = true
			}
		}
		if !named || r.Path == "" {
			continue
		}
		re, cerr := regexp.Compile(r.Path)
		if cerr != nil {
			return ForbidigoPolicy{}, fmt.Errorf("%s: exclusion path %q: %w", path, r.Path, cerr)
		}
		p.Exempt = append(p.Exempt, re)
	}
	return p, nil
}

// Exempted reports whether a pkg/-relative path is allowlisted.
func (p ForbidigoPolicy) Exempted(rel string) bool {
	for _, re := range p.Exempt {
		if re.MatchString(rel) {
			return true
		}
	}
	return false
}

// skipDirs are never scanned: not source, or not the library's own code.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "testdata": true,
	"bin": true, "tmp": true, "dist": true,
}

// Scan walks the tree rooted at root and returns every REFERENCE matching
// policy.Forbid outside an exempt path, plus the number of Go files it
// actually parsed. A caller that gets files == 0 has a broken walk, not a
// clean tree — the assertion is only as real as the set it inspected.
//
// A reference, not only a call: `getenv: os.Getenv` stores the function and
// reads the environment later, through a field, at a call site that names
// no package at all. That is the shape that reached main in #555 — forbidigo
// flagged the line, and a call-only scan passed it.
func Scan(root string, policy ForbidigoPolicy) (findings []Finding, files int, err error) {
	fset := token.NewFileSet()
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") {
			return nil
		}
		files++
		if policy.Exempted(rel) {
			return nil
		}
		file, perr := parser.ParseFile(fset, p, nil, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", rel, perr)
		}
		for _, r := range references(file) {
			for _, re := range policy.Forbid {
				if re.MatchString(r.name) {
					findings = append(findings, Finding{
						Path: rel,
						Line: fset.Position(r.pos).Line,
						Ref:  r.name,
					})
					break
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		return nil, files, walkErr
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Path != findings[j].Path {
			return findings[i].Path < findings[j].Path
		}
		return findings[i].Line < findings[j].Line
	})
	return findings, files, nil
}

// reference is one package-level name a file uses, in canonical
// "<package>.<Name>" form.
type reference struct {
	name string
	pos  token.Pos
}

// references returns every use in file of a name from an imported package,
// rendered as "<package>.<Name>" — the form forbidigo matches its patterns
// against when it knows the types (pkg.Name() + "." + field) — however the
// file spells it:
//
//   - a qualified use, `os.Getenv`, whether called or passed as a value;
//   - an aliased import, `goos.Getenv`, reported as os.Getenv;
//   - a dot import's bare `Getenv`, reported as os.Getenv.
//
// It works from the file's own import table and the parser's identifier
// resolution, without type-checking, so it cannot be fooled by a name and
// does not need the module to build: a qualifier the file declared itself
// (a local or parameter named os) is not the package, and a selector's
// right-hand side, a struct field, a composite-literal key and a method name
// are names, not references.
func references(file *ast.File) []reference {
	qualified := map[string]string{} // local qualifier -> package name
	var dotted []string              // package names imported with "."
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, "`\"")
		pkg := path[strings.LastIndex(path, "/")+1:]
		switch {
		case imp.Name == nil:
			qualified[pkg] = pkg
		case imp.Name.Name == ".":
			dotted = append(dotted, pkg)
		case imp.Name.Name != "_":
			qualified[imp.Name.Name] = pkg
		}
	}

	// Identifiers that are names rather than uses. ast.Inspect visits a
	// parent before its children, so each is recorded before it is reached.
	names := map[*ast.Ident]bool{file.Name: true}
	var refs []reference
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ImportSpec:
			return false
		case *ast.FuncDecl:
			names[n.Name] = true // a method name is not resolved, so mark it
		case *ast.CompositeLit:
			for _, elt := range n.Elts {
				if kv, ok := elt.(*ast.KeyValueExpr); ok {
					if key, isIdent := kv.Key.(*ast.Ident); isIdent {
						names[key] = true
					}
				}
			}
		case *ast.SelectorExpr:
			names[n.Sel] = true
			if x, ok := n.X.(*ast.Ident); ok && x.Obj == nil {
				if pkg, imported := qualified[x.Name]; imported {
					refs = append(refs, reference{name: pkg + "." + n.Sel.Name, pos: n.Pos()})
				}
			}
		case *ast.Ident:
			// Only a dot import makes a bare identifier a package's name,
			// and only when nothing in the file declared it (Obj == nil).
			if names[n] || n.Obj != nil {
				return true
			}
			for _, pkg := range dotted {
				refs = append(refs, reference{name: pkg + "." + n.Name, pos: n.Pos()})
			}
		}
		return true
	})
	return refs
}
