// db_migrate_test.go — pins the `db migrate` command tree's migration SOURCE
// and the policy that survived the tier split.
//
// THE PROPERTY THAT MATTERS, AND WHY. The production image carries the binary
// and nothing else: the Dockerfile's runtime stage copies /app/<project> and
// no db/ directory. A migrator that reads `file://db/migrations` therefore
// CANNOT work there — which is exactly why forge had no deploy-time migration
// step for so long. `db migrate` runs off the EMBEDDED set, so the migration
// initContainer runs a command that can actually succeed inside the image it
// ships in.
//
// THE SPLIT THESE TESTS NOW STRADDLE. The command used to be one Tier-1 file
// with a {{if .HasMigrations}} fork through the middle. It is now two files
// with one job each:
//
//   - cmd-tree-db.go.tmpl  scaffold-once, USER-OWNED: the command tree and its
//     error policy. It has NO template conditionals at
//     all — that is the point of the split, and one of
//     the tests below pins it.
//   - db/source_gen.go     Tier-1: the one genuinely re-derived fact, whether
//     MigrationsFS is a symbol that exists, re-read from
//     db/migrations/ each run. It lives in package db
//     (beside the embed it wraps) rather than in the
//     command tree, which now has no generated file at
//     all. Emitted from Go, so it is tested through
//     codegen.GenerateMigrate rather than by rendering.
//
// The two branches of the source file are asserted separately because they are
// wired to different symbols: with migrations it returns MigrationsFS; without
// them db/embed_gen.go does not exist, so naming MigrationsFS would not
// COMPILE — the classic "the emitted text looked right and the emitted project
// didn't build" failure this repo keeps re-learning.
package templates_test

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/templates"
)

// dbTemplateData is the slice of the render payload the db command-tree
// templates read.
type dbTemplateData struct {
	Module        string
	Name          string
	HasMigrations bool
	// ConfigFields is read by cmd-tree-root-gen.go.tmpl, which absorbed the
	// migration source when db_source.go was folded into it.
	ConfigFields map[string]bool
}

// renderDB renders one of the db command-tree templates and parses it, so
// every assertion below is made against something that is at least valid Go.
func renderDB(t *testing.T, tmpl string, hasMigrations bool) string {
	t.Helper()
	out, err := templates.ProjectTemplates().Render(tmpl, dbTemplateData{
		Module:        "example.com/demo",
		Name:          "demo",
		HasMigrations: hasMigrations,
		ConfigFields:  map[string]bool{"AutoMigrate": true},
	})
	if err != nil {
		t.Fatalf("render %s (HasMigrations=%v): %v", tmpl, hasMigrations, err)
	}
	src := string(out)
	if _, perr := parser.ParseFile(token.NewFileSet(), "db.go", src, parser.AllErrors); perr != nil {
		t.Fatalf("rendered %s (HasMigrations=%v) does not parse: %v\n%s", tmpl, hasMigrations, perr, src)
	}
	return src
}

// dbFileImports returns the import paths the rendered file actually declares,
// parsed from its AST.
//
// Parsing rather than grepping matters here: the whole hazard this file guards
// is a reference to a package that does not exist in the emitted project, and
// that is a property of the IMPORT SET the compiler will resolve — not of
// whether a string appears somewhere in the bytes. A doc comment mentioning
// forgedb is harmless; an import of it is a build failure.
func dbFileImports(t *testing.T, src string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "db.go", src, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse imports: %v", err)
	}
	var out []string
	for _, imp := range f.Imports {
		out = append(out, strings.Trim(imp.Path.Value, `"`))
	}
	return out
}

// dbFileFuncs returns the names of the top-level functions the rendered file
// declares, from its AST.
func dbFileFuncs(t *testing.T, src string) map[string]bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "db.go", src, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := map[string]bool{}
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
			out[fn.Name.Name] = true
		}
	}
	return out
}

// renderMigrationSource emits db/source_gen.go for a project with or without
// SQL and returns its contents.
//
// This one is emitted from Go (codegen.GenerateMigrate), not from a template,
// so it is exercised through the real emitter against a real directory rather
// than by rendering a template name.
func renderMigrationSource(t *testing.T, hasMigrations bool) string {
	t.Helper()
	dir := t.TempDir()
	if err := codegen.GenerateMigrate(dir, "example.com/demo", hasMigrations, nil); err != nil {
		t.Fatalf("GenerateMigrate(hasMigrations=%v): %v", hasMigrations, err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "db", "source_gen.go"))
	if err != nil {
		t.Fatalf("read db/source_gen.go: %v", err)
	}
	return string(body)
}

// TestMigrationSource_WithMigrationsExposesTheEmbeddedFS pins the with-SQL
// branch of the one Tier-1 file in the migration wiring: Source() must hand out
// the embedded FS, and nothing may reach for the filesystem.
//
// Asserted on the parsed import set and the declared function set, so the test
// is about what the compiler will resolve rather than about the current
// spelling of any one line. Source() lives in package db beside the embed it
// wraps, so with SQL it needs no import to name MigrationsFS at all.
func TestMigrationSource_WithMigrationsExposesTheEmbeddedFS(t *testing.T) {
	src := renderMigrationSource(t, true)

	imports := dbFileImports(t, src)

	// The whole point: no filesystem source. `file://db/migrations` cannot
	// resolve inside the runtime image, so a path-sourced migrator must not
	// appear anywhere in the import set.
	for _, forbidden := range []string{
		"github.com/golang-migrate/migrate/v4/source/file",
		"path/filepath",
		"os",
	} {
		if containsString(imports, forbidden) {
			t.Errorf("rendered db/source_gen.go imports %q — the production image has no "+
				"db/migrations directory, so the source must be the embedded FS; imports = %v",
				forbidden, imports)
		}
	}

	if funcs := dbFileFuncs(t, src); !funcs["Source"] {
		t.Errorf("rendered db/source_gen.go does not declare Source; db.go's openMigrator and "+
			"serve.go's AutoMigrate both call it. declared = %v", funcs)
	}
	if !strings.Contains(src, "return MigrationsFS") {
		t.Errorf("with migrations, Source() must return the embedded set:\n%s", src)
	}
}

// TestMigrationSource_WithoutMigrationsCompilesAndYieldsNoSource pins the
// no-SQL branch. db/embed_gen.go is NOT generated for a project with no .sql,
// so ANY reference to MigrationsFS would fail to compile — this is the
// assertion a text-only test would miss and a real build would catch.
//
// It must still declare Source, because db.go and serve.go are scaffold-once
// (identical in both branches) and call it unconditionally.
func TestMigrationSource_WithoutMigrationsCompilesAndYieldsNoSource(t *testing.T) {
	src := renderMigrationSource(t, false)

	if strings.Contains(src, "MigrationsFS") {
		t.Errorf("without migrations, db/embed_gen.go is not generated, so db/source_gen.go "+
			"must not name MigrationsFS — the project would not compile:\n%s", src)
	}

	funcs := dbFileFuncs(t, src)
	if !funcs["Source"] {
		t.Errorf("rendered db/source_gen.go does not declare Source; the scaffold-once db.go "+
			"calls it in BOTH branches, so omitting it breaks the build. declared = %v", funcs)
	}
	if !strings.Contains(src, "return nil") {
		t.Errorf("without migrations, Source() must return nil (migratekit tolerates that at "+
			"boot and refuses it at `db migrate up`):\n%s", src)
	}
}

// TestMigrationSource_DeclaresHasMigrationsInBothBranches pins the constant the
// command tree branches on, in BOTH states.
//
// WHY A CONSTANT AND NOT A NIL CHECK — this is a regression test, not a style
// preference. db.go used to ask `forgedb.Source() == nil`. With migrations
// embedded, Source() returns MigrationsFS, and `var MigrationsFS embed.FS` is a
// concrete struct VALUE that is never nil, so that comparison is provably false
// and staticcheck reports SA4023. That made `forge lint` exit 1 on a freshly
// scaffolded project the moment it had one migration — breaking the two e2e
// tests whose entire purpose is asserting the scaffold is lint-clean, plus any
// real user who ran `forge lint`.
//
// The value must DIFFER between the branches: a constant that read `true` in
// both states would compile and lint just as cleanly while silently skipping
// every migration in the no-SQL project.
func TestMigrationSource_DeclaresHasMigrationsInBothBranches(t *testing.T) {
	withSQL := renderMigrationSource(t, true)
	if !strings.Contains(withSQL, "const HasMigrations = true") {
		t.Errorf("with migrations, db/source_gen.go must declare HasMigrations = true; "+
			"cmd/<bin>/cmd/db.go branches on it:\n%s", withSQL)
	}

	withoutSQL := renderMigrationSource(t, false)
	if !strings.Contains(withoutSQL, "const HasMigrations = false") {
		t.Errorf("without migrations, db/source_gen.go must declare HasMigrations = false, "+
			"or `db migrate up` would try to apply an embedded set that does not exist:\n%s",
			withoutSQL)
	}
}

// TestCmdTreeDB_IsFreeOfTemplateConditionals is the structural property the
// split bought, and the reason db.go can be handed to the user at all.
//
// A scaffold-once file is written ONCE, from whatever the project's inputs
// looked like at that moment, and then never re-rendered. So a conditional in
// it is worse than in a Tier-1 file: it freezes one branch forever, and no
// later `forge generate` can correct it if the project's inputs change. The
// input-dependent sliver was moved to root_gen.go precisely so that this file
// has no branch to freeze.
//
// Asserted on the RAW TEMPLATE, not the render: a render has already resolved
// its conditionals away, so only the template source can answer whether any
// existed.
func TestCmdTreeDB_IsFreeOfTemplateConditionals(t *testing.T) {
	raw, err := templates.ProjectTemplates().Get("cmd-tree-db.go.tmpl")
	if err != nil {
		t.Fatalf("read cmd-tree-db.go.tmpl: %v", err)
	}
	body := string(raw)

	// The forms that make a rendered file's SHAPE depend on project inputs.
	// Plain {{.Field}} substitution is fine — it fills a value in, it does
	// not add or remove code — and the module path is exactly that.
	for _, control := range []string{"{{if", "{{- if", "{{range", "{{- range", "{{with", "{{- with"} {
		if strings.Contains(body, control) {
			t.Errorf("cmd-tree-db.go.tmpl contains the control action %q. This file is "+
				"scaffold-once: it is written from the project's inputs at ONE moment and never "+
				"re-rendered, so a conditional freezes one branch permanently. Input-dependent "+
				"code belongs in the Tier-1 cmd-tree-root-gen.go.tmpl instead.", control)
		}
	}

	// Both renders must be byte-identical, which is the same property stated
	// from the other side and catches a conditional spelled some way the
	// literal scan above missed.
	if withSQL, without := renderDB(t, "cmd-tree-db.go.tmpl", true), renderDB(t, "cmd-tree-db.go.tmpl", false); withSQL != without {
		t.Error("cmd-tree-db.go.tmpl renders differently depending on HasMigrations; " +
			"a scaffold-once file must not vary with project inputs")
	}
}

// TestCmdTreeDB_KeepsItsOperationalPolicy pins the DECISIONS that stayed in
// the user-owned file. These are the reason db.go exists as a file at all
// rather than being absorbed into pkg/migratekit, so if they drift into the
// library the split has failed and the user has nothing left to own.
//
// Each is asserted through the AST or through the policy's own distinguishing
// behaviour rather than by matching a full sentence, so rewording a message
// does not fail the test but removing the policy does.
func TestCmdTreeDB_KeepsItsOperationalPolicy(t *testing.T) {
	src := renderDB(t, "cmd-tree-db.go.tmpl", true)

	// The dirty-schema fail-hard, asserted INSIDE the `up` subcommand's own
	// RunE body. Scope matters: `status` legitimately reads the dirty flag to
	// print it, so a whole-file search for state.Dirty stays green even after
	// up's guard is deleted — which is the regression that actually matters,
	// because in an initContainer this guard is what stalls the rollout with
	// the old pods still serving instead of applying migrations on top of a
	// half-applied one.
	up := dbSubcommandBody(t, src, "up")
	if !strings.Contains(up, "Dirty") {
		t.Errorf("`db migrate up` never inspects the dirty flag — the fail-hard-on-dirty-schema "+
			"policy is gone from the subcommand that applies migrations:\n%s", up)
	}
	if !strings.Contains(up, "schema is dirty") {
		t.Errorf("`db migrate up` has no dirty-schema diagnostic; the operator needs to be told "+
			"what to fix:\n%s", up)
	}

	// "Nothing pending" is success, and it is `up` that has to say so.
	// res.Changed is false both for a release that shipped no migration and
	// for a replica that lost the advisory-lock race, so treating either as
	// failure would fail most of a healthy concurrent deploy.
	if !strings.Contains(up, "Changed") {
		t.Errorf("`db migrate up` does not consult Result.Changed — it can no longer tell "+
			"'applied nothing' from 'applied something', so a lost advisory-lock race and a "+
			"real application are indistinguishable:\n%s", up)
	}

	// Unattended (initContainer) execution: a failure must leave the SQL
	// error at the end of the pod log, not a cobra flag table. One per
	// subcommand.
	if got, want := strings.Count(src, "SilenceUsage: true"), 3; got != want {
		t.Errorf("rendered db.go sets SilenceUsage on %d subcommands, want %d — a migration "+
			"failure must leave the SQL error at the end of the pod log, not a flag table:\n%s",
			got, want, src)
	}

	// The migrator itself must come from the library: if db.go grew its own
	// golang-migrate wiring back, the invariant half has leaked into the
	// user's file where it stops receiving improvements.
	imports := dbFileImports(t, src)
	if !containsString(imports, "github.com/reliant-labs/forge/pkg/migratekit") {
		t.Errorf("rendered db.go does not import pkg/migratekit; imports = %v", imports)
	}
	for _, leaked := range imports {
		if strings.HasPrefix(leaked, "github.com/golang-migrate/") {
			t.Errorf("rendered db.go imports %q directly — the migrator ceremony belongs in "+
				"pkg/migratekit so improvements reach projects by dependency bump; imports = %v",
				leaked, imports)
		}
	}
}

// TestCmdTreeDB_DeclaresUpAndStatusAndNoDown pins the command SHAPE. `up`
// and `status` are the surface the deploy pipeline and the Taskfile invoke by
// name, so losing one silently breaks a deploy step rather than a build;
// `force` is the way out of up's dirty refusal (see the next test). And
// there is no `down`: forge rolls forward only, so the scaffolded binary ships
// no command that runs down SQL (and hence no m.Steps(-1)).
func TestCmdTreeDB_DeclaresUpAndStatusAndNoDown(t *testing.T) {
	src := renderDB(t, "cmd-tree-db.go.tmpl", true)
	for _, use := range []string{`Use:   "up"`, `Use:          "status"`, `Use:   "force <version>"`} {
		if !strings.Contains(src, use) {
			t.Errorf("rendered db.go does not declare %s:\n%s", use, src)
		}
	}
	for _, forbidden := range []string{`"down"`, `Steps(-`} {
		if strings.Contains(src, forbidden) {
			t.Errorf("rendered db.go contains %s — forge never runs down migrations", forbidden)
		}
	}
}

// TestCmdTreeDB_DirtyRefusalNamesACommandTheBinaryHas: up's dirty refusal is
// read in a pod log, by an operator holding the image and little else — no
// forge, no checkout, and for a hosted database no route to it from a laptop.
// It told them to run `migrate force N`, golang-migrate's CLI, which forge no
// longer uses and the image does not carry. The refusal must name this
// binary's own `force`, by the command path cobra resolves at run time (so a
// renamed binary stays right), and the binary must declare it and run it
// through migratekit, which keeps the applied record in step.
func TestCmdTreeDB_DirtyRefusalNamesACommandTheBinaryHas(t *testing.T) {
	src := renderDB(t, "cmd-tree-db.go.tmpl", true)

	up := dbSubcommandBody(t, src, "up")
	if !strings.Contains(up, "cmd.Parent().CommandPath()") || !strings.Contains(up, "force %d") {
		t.Errorf("up's dirty refusal does not name this binary's own `<path> force N`:\n%s", up)
	}
	if strings.Contains(up, "`migrate force") {
		t.Errorf("up's dirty refusal still names golang-migrate's `migrate force`, a CLI the image does not carry:\n%s", up)
	}

	force := dbSubcommandBody(t, src, "force <version>")
	if !strings.Contains(force, "m.Force(version)") {
		t.Errorf("`db migrate force` does not run migratekit's Force:\n%s", force)
	}
}

// TestCmdTreeDB_GatesOnHasMigrationsNotANilSource is the regression guard for
// staticcheck SA4023.
//
// `Source() == nil` is dead code whenever migrations are embedded, because
// Source() then returns MigrationsFS — a concrete embed.FS VALUE, never nil.
// staticcheck proves it, so its reappearance would once again make `forge lint`
// exit 1 on a freshly scaffolded project that has written its first migration.
//
// This asserts on db.go rather than on the linter because the scaffolded file
// is written ONCE and never revised: a project generated today keeps whatever
// this template said, so the template is the only place the guard holds.
func TestCmdTreeDB_GatesOnHasMigrationsNotANilSource(t *testing.T) {
	src := renderDB(t, "cmd-tree-db.go.tmpl", true)

	// Scoped to the `up` subcommand's CODE, re-printed from the AST so
	// comments drop out — this file's own comments necessarily quote the
	// forbidden expression to explain why it is forbidden.
	up := dbSubcommandBody(t, src, "up")
	if strings.Contains(up, "Source() == nil") || strings.Contains(up, "Source() != nil") {
		t.Errorf("rendered db.go compares Source() against nil. With migrations embedded "+
			"Source() returns a concrete embed.FS that is never nil, so staticcheck flags "+
			"the branch as dead (SA4023) and `forge lint` fails on a fresh scaffold. "+
			"Branch on forgedb.HasMigrations instead:\n%s", up)
	}
	if !strings.Contains(src, "forgedb.HasMigrations") {
		t.Errorf("rendered db.go does not consult forgedb.HasMigrations, so `migrate up` can "+
			"no longer tell an empty db/migrations/ from a populated one and a fresh project "+
			"cannot boot:\n%s", src)
	}
}

// dbSubcommandBody returns the source text of the cobra command literal whose
// Use field is use — so an assertion can be scoped to `up` rather than to the
// whole file.
//
// The scoping is the point. Several of these policies are about ONE
// subcommand: `status` reads the dirty flag to print it, while `up` reads it to
// REFUSE. A whole-file search cannot tell those apart, so it stays green after
// the refusal is deleted, which is the regression worth catching.
//
// Located through the AST (find the composite literal carrying `Use: <use>`)
// rather than by brace counting, so nested closures and string literals
// containing braces cannot mis-delimit it. The literal is then re-PRINTED from
// the AST, which drops comments: an assertion must be about what the compiler
// sees, not about what a doc comment happens to mention. That distinction is
// not academic — the first version of this helper returned raw source, and a
// deleted dirty-schema guard went undetected because the explanatory comment
// above it still contained the word the test was looking for.
func dbSubcommandBody(t *testing.T, src, use string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "db.go", src, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var found string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != "Use" {
				continue
			}
			val, ok := kv.Value.(*ast.BasicLit)
			if !ok || strings.Trim(val.Value, `"`) != use {
				continue
			}
			var buf bytes.Buffer
			if perr := printer.Fprint(&buf, fset, lit); perr != nil {
				t.Fatalf("print %q subcommand: %v", use, perr)
			}
			found = buf.String()
			return false
		}
		return true
	})

	if found == "" {
		t.Fatalf("no cobra command literal with Use: %q found in rendered db.go:\n%s", use, src)
	}
	return found
}

func containsString(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// TestMigratorDSNDoesNotLoadWholeConfig pins the migrator's CREDENTIAL
// SURFACE to the one field it consumes.
//
// The scaffold used to resolve the DSN with config.Load, which validates every
// field marked `required: true` across the whole config message. That coupled
// `db migrate` to credentials it never reads, and the two constraints are not
// jointly satisfiable in a real deployment: the migration Job is deployed with
// none of the app's secrets (a schema migration has no business holding a
// signing key — forge's own guidance is to give each workload only what it
// reads), so the Job exited at config load with
//
//	load config: required config field internal_service_secret is not set
//
// before opening a single connection. Every workload gated behind that Job
// then failed against a schema nothing had created, surfacing as a missing
// relation — which reads like a migration-ordering bug and is not one. The
// migration had never run.
//
// So: openMigrator must NOT call config.Load. It resolves --database-url /
// DATABASE_URL directly. A required field the SERVER needs is still enforced
// where the server loads its config.
func TestMigratorDSNDoesNotLoadWholeConfig(t *testing.T) {
	src := renderDB(t, "cmd-tree-db.go.tmpl", true)

	funcs := dbFileFuncs(t, src)
	if !funcs["migratorDSN"] {
		t.Error("rendered db.go declares no migratorDSN — the DSN must be resolved from its own " +
			"field, not from a whole-config load that enforces unrelated required fields")
	}

	body := dbFuncBody(t, src, "migratorDSN")
	if strings.Contains(body, "config.Load") {
		t.Errorf("migratorDSN calls config.Load, which validates EVERY required field in the "+
			"config message. That is what made the migration Job unable to start without "+
			"credentials it never reads. Resolve the DSN field directly.\n%s", body)
	}
	if !strings.Contains(body, "DATABASE_URL") {
		t.Errorf("migratorDSN does not reference DATABASE_URL; it must read the same env var "+
			"the server does so the two cannot target different databases.\n%s", body)
	}

	openBody := dbFuncBody(t, src, "openMigrator")
	if strings.Contains(openBody, "config.Load") {
		t.Errorf("openMigrator still calls config.Load — the whole-config validation is exactly "+
			"what this guards against.\n%s", openBody)
	}
}

// dbFuncBody returns the printed source of one top-level function in the
// rendered file. Asserting against the FUNCTION rather than the whole file
// keeps the doc comments — which necessarily mention config.Load to explain
// why it is not called — from failing the check.
func dbFuncBody(t *testing.T, src, name string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "db.go", src, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name != name {
			continue
		}
		var buf bytes.Buffer
		if perr := printer.Fprint(&buf, fset, fn.Body); perr != nil {
			t.Fatalf("print %s body: %v", name, perr)
		}
		return buf.String()
	}
	t.Fatalf("no top-level func %q in rendered db.go:\n%s", name, src)
	return ""
}
