package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ─── handlers never reach tenant data through an unscoped store call ───────
//
// internal/db is being retired (dev-docs: facade plan). Its methods carry the
// tenant scoping handlers rely on: "WithUserCheck" reads, empty-owner guards,
// owner invariants on writes. A handler that calls internal/store directly
// skips all of it: store.GetChatRow plus a forgotten authz check is a
// cross-tenant read, and a generated store List with an empty owner returns
// every row. This guard keeps that door shut while the facade is dismantled.
//
// For code under internal/handlers/** and internal/llm/tools/** (non-test,
// non-generated files):
//
//  1. Sealed tables are deny-all. api_keys, provider_accounts and
//     connection_secrets hold sealed credentials;
//     only the credential packages may read or write them. No store
//     identifier from their files, and no SQL naming them, may appear. No
//     exception.
//  2. The generated CRUD (Get<E>ByID, List<E>, Update<E>, Delete<E>, the
//     <E>Store interfaces) takes no owner. It is forbidden unless the line
//     carries `// scope: <why>` naming what scopes the call.
//  3. Every other exported store function is a hand-written query. It is
//     allowed only from the files and names in storeCallAllowlist, which is
//     where a wave records that its ext function takes a typed owner and
//     fails closed on an empty one.
//  4. Calling .ORM() hands out the raw orm handle, which would let a handler
//     call anything in 2 and 3 through a variable. Same `// scope:` rule.
//
// Types, constants and variables of the store package are free (they carry no
// query), except the sealed and generated ones in 1 and 2.

const (
	storePkgDir      = "internal/store"
	scopeCommentWord = "// scope:"
)

// storeScopeDirs are the code trees the guard covers.
var storeScopeDirs = []string{"internal/handlers", "internal/llm/tools"}

// sealedStoreEntities are the store files (by entity prefix) of tables that
// hold sealed credentials.
var sealedStoreEntities = []string{
	"api_key", "provider_account", "connection_secret",
}

// sealedStoreExtraFiles are sealed store files that follow no entity naming.
var sealedStoreExtraFiles = []string{"org_credentials.go"}

// sealedTableSQL matches SQL that names a sealed table.
var sealedTableSQL = regexp.MustCompile(
	`(?is)\b(?:from|join|into|update|delete\s+from|truncate(?:\s+table)?)\s+(?:only\s+)?(?:public\.)?"?` +
		`(?:api_keys|provider_accounts|connection_secrets)"?(?:[^a-z0-9_]|$)`)

// storeCallAllowlist maps a handler or tool package directory to the store
// functions it may call. An entry's function must take a typed owner (or an
// id the caller already authorized) and fail closed on an empty one. Empty
// today: handlers reach the store only through the internal/db facade.
var storeCallAllowlist = map[string][]string{}

// storeIdents classifies the exported identifiers of internal/store.
type storeIdents struct {
	sealed    map[string]bool // anything declared in a sealed file
	generated map[string]bool // generated unscoped CRUD funcs and <E>Store interfaces
	funcs     map[string]bool // hand-written exported package-level funcs
}

func loadStoreIdents(t *testing.T, repo string) storeIdents {
	t.Helper()
	dir := filepath.Join(repo, storePkgDir)
	sealedFiles := map[string]bool{}
	for _, e := range sealedStoreEntities {
		for _, suffix := range []string{"_orm_gen.go", "_store_gen.go", "_repo_ext.go"} {
			sealedFiles[e+suffix] = true
		}
	}
	for _, f := range sealedStoreExtraFiles {
		sealedFiles[f] = true
	}
	ids := storeIdents{sealed: map[string]bool{}, generated: map[string]bool{}, funcs: map[string]bool{}}
	seen := map[string]bool{}
	entries, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, path := range entries {
		name := filepath.Base(path)
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		sealed := sealedFiles[name]
		seen[name] = true
		isORMGen := strings.HasSuffix(name, "_orm_gen.go")
		isStoreGen := strings.HasSuffix(name, "_store_gen.go")
		note := func(id *ast.Ident, isFunc bool) {
			if !id.IsExported() {
				return
			}
			switch {
			case sealed:
				ids.sealed[id.Name] = true
			case isORMGen && isFunc, isStoreGen:
				ids.generated[id.Name] = true
			case isFunc:
				ids.funcs[id.Name] = true
			}
		}
		for _, d := range file.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					note(d.Name, true)
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					switch s := s.(type) {
					case *ast.TypeSpec:
						note(s.Name, false)
					case *ast.ValueSpec:
						for _, n := range s.Names {
							note(n, false)
						}
					}
				}
			}
		}
	}
	for f := range sealedFiles {
		if !seen[f] {
			t.Errorf("sealedStoreEntities/sealedStoreExtraFiles names %s/%s, which does not exist: update the list in store_scope_test.go", storePkgDir, f)
		}
	}
	return ids
}

// scopeFinding is one violation.
type scopeFinding struct {
	pos  string
	rule string
}

// scanStoreScope reports the violations in one file at repo-relative path
// rel. allowed holds the store function names rel's package may call.
func scanStoreScope(fset *token.FileSet, file *ast.File, rel string, ids storeIdents, allowed map[string]bool, used map[string]bool) []scopeFinding {
	storeName := ""
	for _, imp := range file.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == storeImportPath {
			storeName = "store"
			if imp.Name != nil {
				storeName = imp.Name.Name
			}
		}
	}
	// A `// scope: <why>` comment on the line of a use, or the line above it.
	scoped := map[int]bool{}
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			if i := strings.Index(c.Text, scopeCommentWord); i >= 0 && strings.TrimSpace(c.Text[i+len(scopeCommentWord):]) != "" {
				line := fset.Position(c.Pos()).Line
				scoped[line] = true
				scoped[line+1] = true
			}
		}
	}
	var out []scopeFinding
	add := func(pos token.Pos, rule string) {
		out = append(out, scopeFinding{pos: rel + ":" + strconv.Itoa(fset.Position(pos).Line), rule: rule})
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			line := fset.Position(n.Pos()).Line
			x, ok := n.X.(*ast.Ident)
			if !ok || storeName == "" || x.Name != storeName {
				return true
			}
			name := n.Sel.Name
			switch {
			case ids.sealed[name]:
				add(n.Pos(), "sealed: store."+name+" touches a sealed credential table; only the credential packages may")
			case ids.generated[name]:
				if !scoped[line] {
					add(n.Pos(), "unscoped: store."+name+" is generated CRUD with no owner; add `"+scopeCommentWord+" <what scopes it>` or use a scoped query")
				}
			case ids.funcs[name]:
				if allowed[name] {
					used[name] = true
				} else {
					add(n.Pos(), "unlisted: store."+name+" is not in storeCallAllowlist for this package")
				}
			}
		case *ast.CallExpr:
			if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "ORM" && len(n.Args) == 0 {
				if !scoped[fset.Position(n.Pos()).Line] {
					add(n.Pos(), "raw orm handle: .ORM() lets a handler call any store function; add `"+scopeCommentWord+" <why>` or take a store dependency")
				}
			}
		case *ast.BasicLit:
			if n.Kind == token.STRING {
				if lit, err := strconv.Unquote(n.Value); err == nil && sealedTableSQL.MatchString(lit) {
					add(n.Pos(), "sealed: SQL naming a sealed credential table")
				}
			}
		}
		return true
	})
	return out
}

func parseScopeFile(t *testing.T, fset *token.FileSet, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file
}

func TestHandlersMakeNoUnscopedStoreCalls(t *testing.T) {
	t.Parallel()
	repo := root(t)
	ids := loadStoreIdents(t, repo)
	if len(ids.generated) == 0 || len(ids.sealed) == 0 || len(ids.funcs) == 0 {
		t.Fatalf("store classification found generated=%d sealed=%d funcs=%d: the file layout changed, fix loadStoreIdents",
			len(ids.generated), len(ids.sealed), len(ids.funcs))
	}

	fset := token.NewFileSet()
	var findings []scopeFinding
	usedByDir := map[string]map[string]bool{}
	for _, top := range storeScopeDirs {
		err := filepath.WalkDir(filepath.Join(repo, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			name := d.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_gen.go") {
				return nil
			}
			rel, _ := filepath.Rel(repo, path)
			rel = filepath.ToSlash(rel)
			dir := filepath.ToSlash(filepath.Dir(rel))
			allowed := map[string]bool{}
			for _, n := range storeCallAllowlist[dir] {
				allowed[n] = true
			}
			if usedByDir[dir] == nil {
				usedByDir[dir] = map[string]bool{}
			}
			findings = append(findings, scanStoreScope(fset, parseScopeFile(t, fset, path), rel, ids, allowed, usedByDir[dir])...)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", top, err)
		}
	}
	for _, f := range findings {
		t.Errorf("%s: %s", f.pos, f.rule)
	}
	// An allowlist entry no code uses is stale: drop it, so the list stays
	// the exact set of direct store calls handlers make.
	for dir, names := range storeCallAllowlist {
		for _, n := range names {
			if !ids.funcs[n] {
				t.Errorf("storeCallAllowlist[%s] lists store.%s, which is not a hand-written exported store function", dir, n)
			} else if !usedByDir[dir][n] {
				t.Errorf("storeCallAllowlist[%s] lists store.%s, which that package no longer calls: remove the entry", dir, n)
			}
		}
	}
}

// TestStoreScopeGuardSelfCheck pins what the guard flags and allows, on
// fixture sources, so a refactor of the scanner cannot quietly stop it
// detecting the cases it exists for.
func TestStoreScopeGuardSelfCheck(t *testing.T) {
	t.Parallel()
	ids := storeIdents{
		sealed:    map[string]bool{"GetApiKeyByID": true, "ListProviderAccount": true, "OpenOrgCredential": true},
		generated: map[string]bool{"GetChatByID": true, "ListPlan": true, "PlanStore": true},
		funcs:     map[string]bool{"GetChatRow": true, "ListTasksByPlan": true},
	}
	const header = "package h\nimport \"github.com/reliant-labs/reliant/internal/store\"\n"
	cases := []struct {
		name    string
		body    string
		allowed map[string]bool
		want    string // substring of the one expected finding; "" for none
	}{
		{"sealed generated", "func f() { store.GetApiKeyByID() }", nil, "sealed: store.GetApiKeyByID"},
		{"sealed ext", "func f() { store.OpenOrgCredential() }", map[string]bool{"OpenOrgCredential": true}, "sealed: store.OpenOrgCredential"},
		{"sealed even with a scope comment", "func f() {\n// scope: because\nstore.ListProviderAccount() }", nil, "sealed: store.ListProviderAccount"},
		{"sealed type", "var _ store.ListProviderAccount", nil, "sealed: store.ListProviderAccount"},
		{"sealed sql", "var q = `SELECT secret FROM provider_accounts WHERE id = $1`", nil, "sealed: SQL"},
		{"sealed sql update", "var q = \"UPDATE connection_secrets SET x = 1\"", nil, "sealed: SQL"},
		{"unrelated sql", "var q = \"SELECT 1 FROM provider_account_grants\"", nil, ""},
		{"generated crud", "func f() { store.GetChatByID() }", nil, "unscoped: store.GetChatByID"},
		{"generated interface", "var _ store.PlanStore", nil, "unscoped: store.PlanStore"},
		{"generated with reason", "func f() {\n// scope: filtered to the caller below\nstore.ListPlan() }", nil, ""},
		{"generated with same-line reason", "func f() { store.ListPlan() } // scope: caller's plans", nil, ""},
		{"empty scope reason", "func f() {\n// scope:\nstore.ListPlan() }", nil, "unscoped: store.ListPlan"},
		{"unlisted ext", "func f() { store.GetChatRow() }", nil, "unlisted: store.GetChatRow"},
		{"listed ext", "func f() { store.GetChatRow() }", map[string]bool{"GetChatRow": true}, ""},
		{"raw orm", "func f(r interface{ ORM() int }) { r.ORM() }", nil, "raw orm handle"},
		{"raw orm with reason", "func f(r interface{ ORM() int }) {\n// scope: read-only health probe\nr.ORM() }", nil, ""},
		{"constant is free", "var _ = store.LLMPurposeChat", nil, ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "h.go", header+tc.body, parser.SkipObjectResolution|parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			got := scanStoreScope(fset, file, "internal/handlers/h/h.go", ids, tc.allowed, map[string]bool{})
			var rules []string
			for _, f := range got {
				rules = append(rules, f.rule)
			}
			sort.Strings(rules)
			switch {
			case tc.want == "" && len(rules) != 0:
				t.Errorf("want no finding, got %q", rules)
			case tc.want != "" && (len(rules) != 1 || !strings.Contains(rules[0], tc.want)):
				t.Errorf("want one finding containing %q, got %q", tc.want, rules)
			}
		})
	}
}

// ─── only internal/credentials/** reaches the sealed credential tables ─────
//
// api_keys, provider_accounts, connection_secrets and the *_auth_token tables
// hold sealed credentials. The first guard keeps handlers/** and llm/tools/**
// out of them; this one keeps EVERY other package out too, so the credential
// packages under internal/credentials are the only path to them: they alone seal
// on write, open on read, and scope each statement to an owner.
//
// Outside internal/store itself (whose files hold the statements), a non-test,
// non-generated file may not name a sealed store identifier or write SQL against
// a sealed table. sealedPathAllowlist is the code that has not moved yet; an
// entry is deleted in the wave that moves its file, and one that no longer
// matches a violation fails the test, so the list only shrinks.

// credentialDirs are the packages that own the sealed tables.
var credentialDirs = []string{"internal/credentials"}

// sealedScanSkip are the trees the sealed guard does not read.
var sealedScanSkip = []string{
	"internal/store", "internal/credentials", "gen", "sdk", "node_modules", "frontends", "electron", "docs", "dev-docs", "web",
}

// sealedPathAllowlist maps a repo-relative file to why it may still touch a sealed
// table.
var sealedPathAllowlist = map[string]string{
	"internal/accountpurge/accountpurge.go":  "deletes a purged account's rows in its own transaction; moves with the credentials purge in the connection wave",
	"internal/db/provider_accounts_store.go": "the provider-account store; moves to internal/credentials/provideraccounts",
	"internal/db/org_provider_accounts.go":   "an org's accounts, grants, seats; moves to internal/credentials/orgaccounts",
	"internal/db/org_credentials.go":         "the gone-org purge and stale-grant sweep; moves to internal/credentials/orgaccounts",
	"internal/db/connections.go":             "the connection store and its secrets lock; moves to internal/credentials/connectionvault",
	"internal/db/llm_usage.go":               "reads an account's display name for the usage ledger; moves with the account reads",
}

func sealedFinding(t *testing.T, repo string, ids storeIdents) map[string][]scopeFinding {
	t.Helper()
	fset := token.NewFileSet()
	out := map[string][]scopeFinding{}
	err := filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(repo, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && rel != "." || d.Name() == "testdata" {
				return filepath.SkipDir
			}
			for _, skip := range sealedScanSkip {
				if rel == skip {
					return filepath.SkipDir
				}
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_gen.go") {
			return nil
		}
		for _, f := range scanStoreScope(fset, parseScopeFile(t, fset, path), rel, ids, nil, map[string]bool{}) {
			if strings.HasPrefix(f.rule, "sealed:") {
				out[rel] = append(out[rel], f)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", repo, err)
	}
	return out
}

func TestOnlyCredentialPackagesReachSealedTables(t *testing.T) {
	t.Parallel()
	repo := root(t)
	ids := loadStoreIdents(t, repo)
	found := sealedFinding(t, repo, ids)
	for file, fs := range found {
		if _, ok := sealedPathAllowlist[file]; ok {
			continue
		}
		for _, f := range fs {
			t.Errorf("%s: %s: only %s may touch a sealed credential table", f.pos, f.rule, strings.Join(credentialDirs, ", "))
		}
	}
	for file := range sealedPathAllowlist {
		if len(found[file]) == 0 {
			t.Errorf("sealedPathAllowlist lists %s, which no longer touches a sealed table: remove the entry", file)
		}
	}
}
