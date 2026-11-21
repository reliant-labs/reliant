package codegen

// disk_resolver_test.go — regression tests for the disk-first package
// identity resolver (kalshi-trader FORGE_BACKLOG #1).
//
// Reproduced pre-fix behavior (characterized 2026-06-10 before the fix
// landed): a worker scaffolded at workers/engine_shadow with
// `package engine_shadow` and listed in forge.yaml as "engine_shadow"
// produced, on every `forge generate`:
//
//   - bootstrap.go + wire_gen.go importing
//     `engineshadow "example.com/proj/internal/workers/engineshadow"` — a
//     directory that does NOT exist (the synthesis compacted the
//     separators), breaking the build;
//   - an EMPTY `engineshadow.Deps{}` literal in wireWorkerEngineShadowDeps
//     because the Deps AST probe also looked in the synthesized (absent)
//     dir — silently dropping Logger/Config/etc. wiring;
//   - fallible-constructor detection probing the wrong dir, so
//     `New() (T, error)` workers were rendered with the infallible
//     call shape.
//
// These tests pin the disk-first behavior: import paths come from the
// REAL directory, package selectors/aliases from the REAL package
// clause, and the generated files must at minimum parse as valid Go.

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scaffoldComponentDir writes <projectDir>/<roleRoot>/<dir>/<file> with a
// minimal Deps + New shape under the given package clause.
func scaffoldComponentDir(t *testing.T, projectDir, roleRoot, dir, file, pkgClause string) {
	t.Helper()
	d := filepath.Join(projectDir, roleRoot, dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `package ` + pkgClause + `

import "log/slog"

type Deps struct {
	Logger *slog.Logger
}

type Worker struct{ deps Deps }

func New(deps Deps) *Worker { return &Worker{deps: deps} }
`
	if err := os.WriteFile(filepath.Join(d, file), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mustParseGo asserts content is syntactically valid Go — the cheapest
// "generated output compiles" gate available without a full module
// (imports here point at a synthetic module path, so type-checking is
// not possible in-process).
func mustParseGo(t *testing.T, filename string, content []byte) {
	t.Helper()
	if _, err := parser.ParseFile(token.NewFileSet(), filename, content, parser.AllErrors); err != nil {
		t.Fatalf("generated %s does not parse: %v\n--- content ---\n%s", filename, err, content)
	}
}

// TestResolveComponentDir_SnakeDirSnakePackage is the core disk-first
// contract: dir and package clause both snake_case — both must surface
// verbatim, no compaction.
func TestResolveComponentDir_SnakeDirSnakePackage(t *testing.T) {
	projectDir := t.TempDir()
	scaffoldComponentDir(t, projectDir, "workers", "engine_shadow", "worker.go", "engine_shadow")

	res, err := ResolveComponentDir(projectDir, "workers", "engine_shadow")
	if err != nil {
		t.Fatalf("ResolveComponentDir: %v", err)
	}
	if !res.FromDisk {
		t.Fatal("expected FromDisk=true for an existing dir")
	}
	if res.ImportLeaf != "engine_shadow" {
		t.Errorf("ImportLeaf = %q, want %q", res.ImportLeaf, "engine_shadow")
	}
	if res.PackageName != "engine_shadow" {
		t.Errorf("PackageName = %q, want %q", res.PackageName, "engine_shadow")
	}
}

// TestResolveComponentDir_PackageClauseDiffersFromDirName covers the
// legal-but-tricky shape: the directory is snake_case but the package
// clause is compact. The import path must follow the DIR, the
// selector/alias must follow the CLAUSE.
func TestResolveComponentDir_PackageClauseDiffersFromDirName(t *testing.T) {
	projectDir := t.TempDir()
	scaffoldComponentDir(t, projectDir, "workers", "engine_shadow", "worker.go", "engineshadow")

	res, err := ResolveComponentDir(projectDir, "workers", "engine_shadow")
	if err != nil {
		t.Fatalf("ResolveComponentDir: %v", err)
	}
	if res.ImportLeaf != "engine_shadow" {
		t.Errorf("ImportLeaf = %q, want %q (dir truth)", res.ImportLeaf, "engine_shadow")
	}
	if res.PackageName != "engineshadow" {
		t.Errorf("PackageName = %q, want %q (clause truth)", res.PackageName, "engineshadow")
	}
}

// TestResolveComponentDir_NameVariantsFindSameDir asserts every spelling
// a user/forge.yaml/proto may hold for the same component (kebab,
// PascalCase, snake) resolves to the one real directory.
func TestResolveComponentDir_NameVariantsFindSameDir(t *testing.T) {
	projectDir := t.TempDir()
	scaffoldComponentDir(t, projectDir, "workers", "engine_shadow", "worker.go", "engine_shadow")

	for _, name := range []string{"engine_shadow", "engine-shadow", "EngineShadow"} {
		res, err := ResolveComponentDir(projectDir, "workers", name)
		if err != nil {
			t.Fatalf("ResolveComponentDir(%q): %v", name, err)
		}
		if !res.FromDisk || res.ImportLeaf != "engine_shadow" {
			t.Errorf("ResolveComponentDir(%q) = %+v, want FromDisk dir engine_shadow", name, res)
		}
	}
}

// TestResolveComponentDir_FallbackSynthesizesForNewComponents pins the
// scaffolding path: no dir on disk → synthesized snake_case canonical
// identity (naming.GoPackage — the post-2026-06-08 scaffold form),
// FromDisk=false, NO error (forge is about to create the component).
func TestResolveComponentDir_FallbackSynthesizesForNewComponents(t *testing.T) {
	res, err := ResolveComponentDir(t.TempDir(), "workers", "engine_shadow")
	if err != nil {
		t.Fatalf("ResolveComponentDir: %v", err)
	}
	if res.FromDisk {
		t.Fatal("expected FromDisk=false when no dir exists")
	}
	if res.ImportLeaf != "engine_shadow" || res.PackageName != "engine_shadow" {
		t.Errorf("fallback = %q/%q, want snake engine_shadow/engine_shadow", res.ImportLeaf, res.PackageName)
	}
	// Empty projectDir (unit-test convention for "no project context")
	// must also synthesize without touching the filesystem.
	res, err = ResolveComponentDir("", "workers", "calibrator_refit")
	if err != nil || res.FromDisk || res.PackageName != "calibrator_refit" {
		t.Errorf("empty projectDir: res=%+v err=%v, want synthesized calibrator_refit", res, err)
	}
}

// TestParsePackageClause_Diagnostics pins the two mismatch-diagnostic
// modes: dir with no buildable .go file, and conflicting clauses (with
// file:line in the message). Neither may silently fall back to synthesis.
func TestParsePackageClause_Diagnostics(t *testing.T) {
	t.Run("no go files", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := ParsePackageClause(dir); err == nil ||
			!strings.Contains(err.Error(), "no buildable .go file") {
			t.Errorf("expected no-buildable-file error, got %v", err)
		}
	})

	t.Run("unparseable only", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "broken.go"), []byte("pkg broken {"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ParsePackageClause(dir); err == nil ||
			!strings.Contains(err.Error(), "no parseable .go file") {
			t.Errorf("expected no-parseable-file error, got %v", err)
		}
	})

	t.Run("conflicting clauses", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package alpha\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package beta\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := ParsePackageClause(dir)
		if err == nil {
			t.Fatal("expected conflicting-clauses error")
		}
		for _, want := range []string{"conflicting package clauses", "alpha", "beta", "a.go:1", "b.go:1"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error missing %q: %v", want, err)
			}
		}
	})

	t.Run("test files ignored", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package alpha\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// External test package must not count as a conflict.
		if err := os.WriteFile(filepath.Join(dir, "a_test.go"), []byte("package alpha_test\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		pkg, err := ParsePackageClause(dir)
		if err != nil || pkg != "alpha" {
			t.Errorf("ParsePackageClause = %q, %v; want alpha, nil", pkg, err)
		}
	})
}

// TestGenerateBootstrapTesting_SnakeCaseHandlerDir asserts testing.go
// imports the real handler dir too (it renders from the same component
// data through a separate template path).
func TestGenerateBootstrapTesting_SnakeCaseHandlerDir(t *testing.T) {
	projectDir := t.TempDir()
	scaffoldComponentDir(t, projectDir, "internal/handlers", "engine_shadow", "service.go", "engine_shadow")

	services := []ServiceDef{{Name: "EngineShadowService", ModulePath: "example.com/proj"}}
	if err := GenerateBootstrapTesting(BootstrapTestingGenInput{
		GenContext: GenContext{ProjectDir: projectDir, ModulePath: "example.com/proj", Checksums: nil},
		Services:   services,
		Packages:   nil,
		Workers:    nil,
		Operators:  nil,
	}); err != nil {
		t.Fatalf("GenerateBootstrapTesting: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(projectDir, ComponentTestHelperRelPath("engine_shadow")))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	mustParseGo(t, "testing.go", data)

	// The helper file now lives INSIDE the handler package, so the disk-first
	// resolution shows up as the package CLAUSE (and the file's location)
	// rather than as an import of the handler package. Both still prove the
	// same thing: the on-disk `engine_shadow` leaf was honored, not the
	// synthesized `engineshadow` form.
	for _, want := range []string{
		"package engine_shadow\n",
		"func NewTestEngineShadow(",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("helpers_gen_test.go missing %q\n--- content ---\n%s", want, content)
		}
	}
	if strings.Contains(content, "engineshadow") {
		t.Errorf("helpers_gen_test.go still references synthesized engineshadow:\n%s", content)
	}
}

// writeFileT writes content to path, creating parent dirs.
func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWorkerDataFromNames_ConflictingClausesError pins the mismatch
// diagnostic surfacing through the public builder: a worker dir whose
// files disagree on the package clause must fail loudly, not fall back
// to a guessed identity.
func TestWorkerDataFromNames_ConflictingClausesError(t *testing.T) {
	projectDir := t.TempDir()
	scaffoldComponentDir(t, projectDir, "internal/workers", "engine_shadow", "worker.go", "engine_shadow")
	stray := filepath.Join(projectDir, "internal", "workers", "engine_shadow", "stray.go")
	if err := os.WriteFile(stray, []byte("package engineshadow\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := WorkerDataFromNames([]string{"engine_shadow"}, projectDir)
	if err == nil {
		t.Fatal("expected conflicting-clauses error, got nil")
	}
	for _, want := range []string{"conflicting package clauses", "stray.go:1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
}

// TestGenerateMissingHandlerStubs_DiskFirstPackageClause asserts the
// incremental stub scaffolder stamps the RPC's file with the EXISTING
// directory's package clause rather than the synthesized compact form —
// pre-fix it would write `package engineshadow` into a dir declaring
// `package engine_shadow`, breaking the build with a mixed-package dir.
func TestGenerateMissingHandlerStubs_DiskFirstPackageClause(t *testing.T) {
	projectDir := t.TempDir()
	scaffoldComponentDir(t, projectDir, "internal/handlers", "engine_shadow", "service.go", "engine_shadow")
	targetDir := filepath.Join(projectDir, "internal", "handlers", "engine_shadow")

	svc := ServiceDef{
		Name:       "EngineShadowService",
		Package:    "engine_shadow.v1",
		GoPackage:  "example.com/proj/gen/services/engine_shadow/v1",
		PkgName:    "engine_shadowv1",
		ModulePath: "example.com/proj",
		ProtoFile:  "proto/services/engine_shadow/v1/engine_shadow.proto",
		Methods: []Method{
			{Name: "Echo", InputType: "EchoRequest", OutputType: "EchoResponse"},
		},
	}

	result, err := GenerateMissingHandlerStubs(svc, projectDir, targetDir, nil, nil)
	if err != nil {
		t.Fatalf("GenerateMissingHandlerStubs: %v", err)
	}
	if result.AllUpToDate {
		t.Fatal("expected Echo stub to be generated")
	}

	stub := RPCHandlerFileName("Echo")
	data, err := os.ReadFile(filepath.Join(targetDir, stub))
	if err != nil {
		t.Fatal(err)
	}
	mustParseGo(t, stub, data)
	if !strings.Contains(string(data), "package engine_shadow\n") {
		t.Errorf("%s must declare the dir's real clause:\n%s", stub, data)
	}
	if strings.Contains(string(data), "package engineshadow") {
		t.Errorf("%s still declares the synthesized clause:\n%s", stub, data)
	}
}

// TestGenerateCRUDHandlers_DiskFirstTargetDir asserts CRUD codegen lands
// in the existing snake_case handler dir (pre-fix it created a SECOND
// compact dir next to it — the duplicate-dir bug) and declares the dir's
// real package clause.
func TestGenerateCRUDHandlers_DiskFirstTargetDir(t *testing.T) {
	projectDir := t.TempDir()
	dir := filepath.Join(projectDir, "internal", "handlers", "engine_shadow")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `package engine_shadow

type Deps struct{}

type Service struct{ deps Deps }
`
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := ServiceDef{
		Name:       "EngineShadowService",
		GoPackage:  "example.com/proj/gen/services/engine_shadow/v1",
		PkgName:    "engine_shadowv1",
		ModulePath: "example.com/proj",
		Methods: []Method{
			{Name: "CreateTrade", InputType: "CreateTradeRequest", OutputType: "CreateTradeResponse"},
		},
	}
	entities := []EntityDef{{
		Name:      "Trade",
		TableName: "trades",
		PkField:   "id",
		PkGoType:  "int64",
		Fields:    []EntityField{{Name: "id", GoName: "ID", GoType: "int64"}},
	}}
	crudMethods := MatchCRUDMethods(svc, entities)
	if len(crudMethods) == 0 {
		t.Fatal("expected CreateTrade to match CRUD pattern")
	}

	if _, err := GenerateCRUDHandlers(svc, crudMethods, "example.com/proj", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDHandlers: %v", err)
	}

	// Both halves of the split must land in the REAL dir, not a
	// synthesized sibling, and declare the dir's real package clause.
	for _, name := range []string{"handlers_crud_ops_gen.go", "handlers_crud.go"} {
		out := filepath.Join(dir, name)
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatalf("%s not written into existing dir: %v", name, err)
		}
		mustParseGo(t, name, data)
		if !strings.Contains(string(data), "package engine_shadow\n") {
			t.Errorf("%s must declare the dir's real clause:\n%.300s", name, data)
		}
	}
	if _, err := os.Stat(filepath.Join(projectDir, "internal", "handlers", "engineshadow")); !os.IsNotExist(err) {
		t.Error("synthesized duplicate dir handlers/engineshadow was created")
	}
}

// TestPackageDataFromNames_DiskFirstPackageClause asserts internal
// packages read the real clause from disk (internal/email_sender may
// declare `package email_sender`, which the old compact synthesis
// rendered as the nonexistent identifier "emailsender").
func TestPackageDataFromNames_DiskFirstPackageClause(t *testing.T) {
	projectDir := t.TempDir()
	dir := filepath.Join(projectDir, "internal", "email_sender")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "contract.go"), []byte("package email_sender\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pkgs, err := PackageDataFromNames([]string{"email_sender"}, projectDir)
	if err != nil {
		t.Fatalf("PackageDataFromNames: %v", err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("expected 1 package, got %d", len(pkgs))
	}
	if pkgs[0].Package != "email_sender" || pkgs[0].Alias != "email_sender" {
		t.Errorf("Package/Alias = %q/%q, want email_sender (disk clause)", pkgs[0].Package, pkgs[0].Alias)
	}
	if pkgs[0].ImportPath != "email_sender" {
		t.Errorf("ImportPath = %q, want email_sender", pkgs[0].ImportPath)
	}
}
