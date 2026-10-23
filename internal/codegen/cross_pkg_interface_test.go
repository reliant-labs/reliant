// File: internal/codegen/cross_pkg_interface_test.go
//
// Tests for ResolveCrossPkgInterface + the new selector branch of
// computeAutoStubs. These are heavier than the rest of the codegen
// tests because they build a tiny on-disk Go module that
// golang.org/x/tools/go/packages can actually load — the resolver is
// real-go-types-based, so a fixture has to be real Go source.

package codegen

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// writeTestModule lays out a temp Go module with:
//
//	<root>/go.mod                     (module example.com/proj)
//	<root>/internal/repo/repo.go      (interface declaration the handler imports)
//	<root>/internal/handlers/billing/service.go (Deps struct referencing repo.Repository)
//
// Returns the handler dir so callers can pass it straight to the
// auto-stub helpers.
//
// Each test variant tweaks one of the three files via the maps; passing
// nil keeps the default content.
func writeTestModule(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()

	defaults := map[string]string{
		"go.mod": `module example.com/proj

go 1.22
`,
		"internal/repo/repo.go": `package repo

import "context"

// Repository is a cross-package interface the billing handler depends on.
type Repository interface {
	GetByID(ctx context.Context, id string) (*User, error)
	List(ctx context.Context) ([]User, error)
	Delete(ctx context.Context, id string) error
}

// User is referenced by Repository's method signatures.
type User struct {
	ID   string
	Name string
}
`,
		"internal/handlers/billing/service.go": `package billing

import (
	"log/slog"

	"example.com/proj/internal/repo"
)

// Deps is the billing service's dependency graph.
type Deps struct {
	Logger *slog.Logger
	Repo   repo.Repository
}
`,
	}

	for path, content := range defaults {
		if override, ok := files[path]; ok {
			content = override
		}
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	return filepath.Join(root, "internal", "handlers", "billing")
}

// TestResolveCrossPkgInterface_HappyPath verifies that a Deps field
// typed `repo.Repository` resolves to the right interface, with all
// three methods and their fully qualified parameter / result types.
func TestResolveCrossPkgInterface_HappyPath(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks or builds fixture modules via the go toolchain; runs in task test")
	}
	handlerDir := writeTestModule(t, nil)

	res, ok := ResolveCrossPkgInterface(handlerDir, "repo", "Repository")
	if !ok {
		t.Fatal("expected resolver to succeed for repo.Repository")
	}

	if res.PackageName != "repo" {
		t.Errorf("PackageName = %q, want %q", res.PackageName, "repo")
	}
	if res.PackagePath != "example.com/proj/internal/repo" {
		t.Errorf("PackagePath = %q, want %q", res.PackagePath, "example.com/proj/internal/repo")
	}
	if len(res.Methods) != 3 {
		t.Fatalf("Methods len = %d, want 3 (GetByID, List, Delete)", len(res.Methods))
	}

	// Spot-check signatures. types.TypeString renders pointer / slice
	// types in canonical form, so we can string-match exactly.
	wantSig := map[string]struct {
		params  string
		results string
	}{
		"GetByID": {params: "context.Context, string", results: "(*repo.User, error)"},
		"List":    {params: "context.Context", results: "([]repo.User, error)"},
		"Delete":  {params: "context.Context, string", results: "error"},
	}
	got := map[string]InterfaceMethod{}
	for _, m := range res.Methods {
		got[m.Name] = m
	}
	for name, want := range wantSig {
		m, ok := got[name]
		if !ok {
			t.Errorf("missing method %q", name)
			continue
		}
		if m.Params != want.params {
			t.Errorf("%s params = %q, want %q", name, m.Params, want.params)
		}
		if m.Results != want.results {
			t.Errorf("%s results = %q, want %q", name, m.Results, want.results)
		}
	}

	// NeededImports must include both the interface's own package
	// (for the stub-type declaration to compile) and the context
	// package (referenced by every method signature).
	if _, ok := res.NeededImports["example.com/proj/internal/repo"]; !ok {
		t.Error("NeededImports missing the interface's own package")
	}
	if _, ok := res.NeededImports["context"]; !ok {
		t.Error("NeededImports missing the context package")
	}
}

// TestResolveCrossPkgInterface_EmbeddedInterface verifies that the
// types-package method-set walk picks up methods on embedded interfaces
// without recursing manually — Go's types package flattens for us.
func TestResolveCrossPkgInterface_EmbeddedInterface(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks or builds fixture modules via the go toolchain; runs in task test")
	}
	handlerDir := writeTestModule(t, map[string]string{
		"internal/repo/repo.go": `package repo

import "context"

// Reader is embedded into Repository.
type Reader interface {
	GetByID(ctx context.Context, id string) (*User, error)
}

// Writer is also embedded.
type Writer interface {
	Save(ctx context.Context, u *User) error
}

// Repository composes Reader + Writer plus its own Delete method.
type Repository interface {
	Reader
	Writer
	Delete(ctx context.Context, id string) error
}

type User struct {
	ID string
}
`,
	})

	res, ok := ResolveCrossPkgInterface(handlerDir, "repo", "Repository")
	if !ok {
		t.Fatal("expected resolver to succeed")
	}
	names := map[string]bool{}
	for _, m := range res.Methods {
		names[m.Name] = true
	}
	for _, want := range []string{"GetByID", "Save", "Delete"} {
		if !names[want] {
			t.Errorf("method set should include %q after embed flattening, got %v", want, names)
		}
	}
}

// TestResolveCrossPkgInterface_NotAnInterface confirms the resolver
// rejects a named type that isn't actually an interface (e.g. a
// struct). The caller falls through to the "field stays nil"
// behavior, which is correct.
func TestResolveCrossPkgInterface_NotAnInterface(t *testing.T) {
	handlerDir := writeTestModule(t, map[string]string{
		"internal/repo/repo.go": `package repo

// User is a struct, NOT an interface. Asking for a stub here is
// meaningless — the auto-stub feature only synthesizes interface
// implementations.
type User struct {
	ID string
}
`,
		"internal/handlers/billing/service.go": `package billing

import "example.com/proj/internal/repo"

type Deps struct {
	Repo repo.User
}
`,
	})

	if _, ok := ResolveCrossPkgInterface(handlerDir, "repo", "User"); ok {
		t.Error("expected ok=false for a non-interface named type")
	}
}

// TestResolveCrossPkgInterface_UnknownAlias confirms the resolver
// returns ok=false when the alias doesn't correspond to any import.
// This is the "graceful skip" path computeAutoStubs relies on.
func TestResolveCrossPkgInterface_UnknownAlias(t *testing.T) {
	handlerDir := writeTestModule(t, nil)
	if _, ok := ResolveCrossPkgInterface(handlerDir, "doesnotexist", "Repository"); ok {
		t.Error("expected ok=false for an alias not present in the handler dir")
	}
}

// TestResolveCrossPkgInterface_UnloadablePackage confirms that when the
// imported package fails to load (here: import path that doesn't exist
// in the module), the resolver returns ok=false rather than crashing.
func TestResolveCrossPkgInterface_UnloadablePackage(t *testing.T) {
	handlerDir := writeTestModule(t, map[string]string{
		"internal/handlers/billing/service.go": `package billing

import broken "example.com/proj/internal/does_not_exist"

type Deps struct {
	Repo broken.Repository
}
`,
		// And don't write a "internal/repo/repo.go" so the typed
		// import-path is invalid.
		"internal/repo/repo.go": "",
	})
	if _, ok := ResolveCrossPkgInterface(handlerDir, "broken", "Repository"); ok {
		t.Error("expected ok=false when the imported package can't load")
	}
}

// TestComputeAutoStubs_CrossPackage verifies that the selector branch
// of computeAutoStubs produces a DepsAutoStub with CrossPackage=true,
// the right InterfaceQualified, and a non-empty ExtraImports set.
func TestComputeAutoStubs_CrossPackage(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks or builds fixture modules via the go toolchain; runs in task test")
	}
	handlerDir := writeTestModule(t, nil)

	stubs, unresolved := computeAutoStubs(handlerDir, "billing")
	if len(unresolved) != 0 {
		t.Errorf("expected zero unresolved stubs on the happy path, got %v", unresolved)
	}
	if len(stubs) != 1 {
		t.Fatalf("got %d stubs, want 1", len(stubs))
	}
	s := stubs[0]
	if !s.CrossPackage {
		t.Error("stub should be CrossPackage=true for repo.Repository")
	}
	if s.FieldName != "Repo" {
		t.Errorf("FieldName = %q, want %q", s.FieldName, "Repo")
	}
	if s.InterfaceQualified != "repo.Repository" {
		t.Errorf("InterfaceQualified = %q, want %q", s.InterfaceQualified, "repo.Repository")
	}
	if s.StubType != "StubRepo" {
		t.Errorf("StubType = %q, want %q", s.StubType, "StubRepo")
	}
	if len(s.ExtraImports) == 0 {
		t.Error("CrossPackage stub should carry ExtraImports")
	}
	// The repo and context packages must both be present.
	paths := map[string]bool{}
	for _, ei := range s.ExtraImports {
		paths[ei.Path] = true
	}
	if !paths["example.com/proj/internal/repo"] {
		t.Errorf("ExtraImports missing the interface's package; got paths %v", paths)
	}
	if !paths["context"] {
		t.Errorf("ExtraImports missing context; got paths %v", paths)
	}
}

// TestComputeAutoStubs_LocalInterfacePreserved confirms the existing
// local-interface path is unchanged. A regression here would break
// every project that currently relies on the auto-stub feature.
func TestComputeAutoStubs_LocalInterfacePreserved(t *testing.T) {
	handlerDir := writeTestModule(t, map[string]string{
		"internal/repo/repo.go": "", // not used in this variant
		"internal/handlers/billing/service.go": `package billing

import "log/slog"

// Repository is declared LOCALLY in the billing package, same shape as
// every existing forge project before this lane.
type Repository interface {
	GetByID(id string) error
}

type Deps struct {
	Logger *slog.Logger
	Repo   Repository
}
`,
	})

	stubs, unresolved := computeAutoStubs(handlerDir, "billing")
	if len(unresolved) != 0 {
		t.Errorf("expected zero unresolved stubs for a local interface, got %v", unresolved)
	}
	if len(stubs) != 1 {
		t.Fatalf("got %d stubs, want 1", len(stubs))
	}
	s := stubs[0]
	if s.CrossPackage {
		t.Error("local-interface stub should NOT be flagged CrossPackage")
	}
	if s.InterfaceQualified != "<alias>.Repository" {
		t.Errorf("local-interface InterfaceQualified = %q, want the <alias>. placeholder",
			s.InterfaceQualified)
	}
	if len(s.ExtraImports) != 0 {
		t.Errorf("local-interface stub should not carry ExtraImports, got %v", s.ExtraImports)
	}
}

// TestGenerateBootstrapTesting_CrossPackageStub is the end-to-end
// validation: lay down a tiny module with internal/handlers/billing/service.go
// pointing at an interface in internal/repo, run GenerateBootstrapTesting
// against that project root, and inspect the resulting pkg/app/testing.go.
//
// The assertions span the three template touches:
//   - the cross-package import is added to the import block
//   - the auto-stub default-assignment line exists inside NewTestBilling
//   - the stub struct itself + at least one method are emitted
func TestGenerateBootstrapTesting_CrossPackageStub(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks or builds fixture modules via the go toolchain; runs in task test")
	}
	handlerDir := writeTestModule(t, nil)
	projectRoot := filepath.Dir(filepath.Dir(filepath.Dir(handlerDir))) // <root>/internal/handlers/billing -> <root>

	services := []ServiceDef{
		{Name: "BillingService", ModulePath: "example.com/proj"},
	}

	if err := GenerateBootstrapTesting(BootstrapTestingGenInput{
		GenContext: GenContext{ProjectDir: projectRoot, ModulePath: "example.com/proj", Checksums: nil},
		Services:   services,
		Packages:   nil,
		Workers:    nil,
		Operators:  nil,
	}); err != nil {
		t.Fatalf("GenerateBootstrapTesting: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(projectRoot, ComponentTestHelperRelPath("billing")))
	if err != nil {
		t.Fatalf("read testing.go: %v", err)
	}
	content := string(body)

	assertions := []struct {
		name string
		want string
	}{
		{"cross-package import block has repo path",
			`repo "example.com/proj/internal/repo"`},
		{"NewTestBilling assigns the auto-stub",
			"deps.Repo = &StubRepo{}"},
		{"stub struct is declared",
			"type StubRepo struct {"},
		{"stub method GetByID is emitted with qualified types",
			"GetByID(a0 context.Context, a1 string) (*repo.User, error)"},
	}
	for _, a := range assertions {
		if !strings.Contains(content, a.want) {
			t.Errorf("%s: testing.go missing %q\n--- rendered ---\n%s\n--- end ---",
				a.name, a.want, content)
		}
	}

	// Regression (cp-forge-shaped fixture, 2026-06): stub method
	// signatures referencing context.Context used to re-emit
	// `context "context"` in the ExtraImports block, colliding with the
	// template's unconditional `"context"` import — two declarations of
	// the same name, so the generated file failed `go build`. Assert the
	// import block carries no duplicate declared names at all.
	fset := token.NewFileSet()
	parsed, perr := parser.ParseFile(fset, "testing.go", body, parser.ImportsOnly)
	if perr != nil {
		t.Fatalf("generated testing.go does not parse: %v", perr)
	}
	seen := map[string]string{}
	for _, imp := range parsed.Imports {
		path, ierr := importPathFromSpec(imp)
		if ierr != nil {
			t.Fatalf("malformed import in generated testing.go: %v", ierr)
		}
		name := aliasForImport(imp, path)
		if prior, dup := seen[name]; dup {
			t.Errorf("duplicate import name %q in generated testing.go: %q and %q\n--- rendered ---\n%s\n--- end ---",
				name, prior, path, content)
		}
		seen[name] = path
	}
}

// TestGenerateBootstrapTesting_CrossPackageStubImportUsed is the regression
// for the control-plane testing.go disown's residual compile bug: a cross-
// package auto-stub whose interface's METHOD SIGNATURES reference a DIFFERENT
// package than the interface's own (e.g. an external-component domain service
// `internal/user` whose Service methods return `db.User`). The stub's method
// bodies then never name the interface type, only its parameter/result types,
// so the interface package (internal/user) would be imported SOLELY for the
// stub yet referenced nowhere in code → "imported and not used" build failure.
// The generated compile-time guard `var _ <iface> = <stub>{}` both proves the
// stub satisfies the interface AND keeps that import used.
func TestGenerateBootstrapTesting_CrossPackageStubImportUsed(t *testing.T) {
	// Render the template directly with a hand-built CrossPackage AutoStub
	// whose method signatures reference a THIRD package (dbpkg.User), never the
	// interface's own package — the control-plane `internal/user` shape. The
	// stub's interface type (userpkg.Service) then appears ONLY in comments, so
	// without the compile-time guard its import is unused (build failure). This
	// pins the TEMPLATE behavior independent of go/packages resolution.
	svc := BootstrapTestServiceData{
		Name: "billing", Package: "billing", ImportPath: "billing", FieldName: "Billing",
		ConnectServiceGoName:   "BillingService",
		ProtoConnectImportPath: "example.com/proj/gen/services/billing/v1/billingv1connect",
		ProtoConnectPkg:        "billingv1connect",
		Alias:                  "billing", VarName: "billing",
		AutoStubs: []DepsAutoStub{{
			FieldName:          "Users",
			StubType:           "StubUsers",
			InterfaceQualified: "userpkg.Service",
			CrossPackage:       true,
			Methods: []InterfaceMethod{{
				Name: "GetUser", Params: "context.Context, string",
				ParamTypes: []string{"context.Context", "string"},
				Results:    "(*dbpkg.User, error)", ResultTypes: []string{"*dbpkg.User", "error"},
				ResultZeros: []string{"nil", "nil"}, ReturnStatement: "return nil, nil",
			}},
			ExtraImports: []ExtraImport{
				{Alias: "userpkg", Path: "example.com/proj/internal/user"},
				{Alias: "dbpkg", Path: "example.com/proj/internal/db"},
			},
		}},
	}
	programStubMethods(&svc.AutoStubs[0])
	data := componentTestHelperData{
		Module:                 "example.com/proj",
		Package:                svc.Package,
		Name:                   svc.Name,
		FieldName:              svc.FieldName,
		IsService:              true,
		ConstructorName:        svc.ConstructorName(),
		ConnectServiceGoName:   svc.ConnectServiceGoName,
		ProtoConnectImportPath: svc.ProtoConnectImportPath,
		ProtoConnectPkg:        svc.ProtoConnectPkg,
		AutoStubs:              svc.AutoStubs,
		ExtraImports: []ExtraImport{
			{Alias: "userpkg", Path: "example.com/proj/internal/user"},
			{Alias: "dbpkg", Path: "example.com/proj/internal/db"},
		},
	}

	body, err := templates.ProjectTemplates().Render("component_test_helpers.go.tmpl", data)
	if err != nil {
		t.Fatalf("render component_test_helpers.go.tmpl: %v", err)
	}
	content := string(body)

	// The compile-time guard must reference the interface in CODE (not just a
	// comment), so the import imported solely for the stub is genuinely used.
	if !strings.Contains(content, "var _ userpkg.Service = (*StubUsers)(nil)") {
		t.Errorf("helpers must emit the cross-package stub interface assertion:\n%s", content)
	}
	mustParseGo(t, "helpers_gen_test.go", body)
}

// TestGenerateBootstrapTesting_UnresolvedSelectorTODO confirms that a
// selector forge can't resolve produces a visible TODO comment in the
// generated NewTest<Svc> body.
func TestGenerateBootstrapTesting_UnresolvedSelectorTODO(t *testing.T) {
	handlerDir := writeTestModule(t, map[string]string{
		"internal/repo/repo.go": "",
		"internal/handlers/billing/service.go": `package billing

import broken "example.com/proj/internal/does_not_exist"

type Deps struct {
	Repo broken.Repository
}
`,
	})
	projectRoot := filepath.Dir(filepath.Dir(filepath.Dir(handlerDir)))

	services := []ServiceDef{
		{Name: "BillingService", ModulePath: "example.com/proj"},
	}
	if err := GenerateBootstrapTesting(BootstrapTestingGenInput{
		GenContext: GenContext{ProjectDir: projectRoot, ModulePath: "example.com/proj", Checksums: nil},
		Services:   services,
		Packages:   nil,
		Workers:    nil,
		Operators:  nil,
	}); err != nil {
		t.Fatalf("GenerateBootstrapTesting: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(projectRoot, ComponentTestHelperRelPath("billing")))
	content := string(body)
	if !strings.Contains(content, "TODO: stub broken.Repository") {
		t.Errorf("testing.go should carry the TODO comment for an unresolved selector\n--- rendered ---\n%s", content)
	}
}

// TestGenerateBootstrapTesting_FuncDefaults is the harness half of the
// Clock/IDGen fix: a service with a required `func() time.Time` and
// `func() string` dep gets real defaults injected into NewTest<Svc>
// (time.Now / ulid), plus the `time` + ulid imports; an un-defaultable
// func dep (`func(string) error`) surfaces as a TODO instead of a silent
// nil-func validateDeps failure.
func TestGenerateBootstrapTesting_FuncDefaults(t *testing.T) {
	handlerDir := writeTestModule(t, map[string]string{
		"internal/repo/repo.go": "",
		"internal/handlers/billing/service.go": `package billing

import (
	"log/slog"
	"time"
)

type Service struct{}

type Deps struct {
	Logger *slog.Logger
	Now    func() time.Time
	NewID  func() string
	Send   func(msg string) error
}

func New(d Deps) (*Service, error) { return &Service{}, nil }
`,
	})
	projectRoot := filepath.Dir(filepath.Dir(filepath.Dir(handlerDir)))

	if err := GenerateBootstrapTesting(BootstrapTestingGenInput{
		GenContext: GenContext{ProjectDir: projectRoot, ModulePath: "example.com/proj", Checksums: nil},
		Services:   []ServiceDef{{Name: "BillingService", ModulePath: "example.com/proj"}},
	}); err != nil {
		t.Fatalf("GenerateBootstrapTesting: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(projectRoot, ComponentTestHelperRelPath("billing")))
	if err != nil {
		t.Fatalf("read testing.go: %v", err)
	}
	content := string(body)
	for _, want := range []string{
		"deps.Now = time.Now",
		"deps.NewID = func() string { return ulid.Make().String() }",
		`"time"`,
		`"github.com/oklog/ulid/v2"`,
		"TODO: no test default for func-typed dep Send",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("testing.go missing %q\n--- rendered ---\n%s\n--- end ---", want, content)
		}
	}

	// The generated harness must at least parse — the func defaults and
	// their imports have to land in a syntactically valid file.
	if _, perr := parser.ParseFile(token.NewFileSet(), "testing.go", body, parser.AllErrors); perr != nil {
		t.Fatalf("generated testing.go does not parse: %v\n%s", perr, content)
	}
}

// TestComputeFuncDefaults_ClassifiesFuncDeps unit-tests the classifier:
// Clock/IDGen get real defaults, other func types become TODOs, and an
// OPTIONAL func dep is skipped entirely (its `//forge:optional-dep`
// contract says nil is acceptable).
func TestComputeFuncDefaults_ClassifiesFuncDeps(t *testing.T) {
	handlerDir := writeTestModule(t, map[string]string{
		"internal/repo/repo.go": "",
		"internal/handlers/billing/service.go": `package billing

import "time"

type Deps struct {
	Now       func() time.Time
	NewID     func() string
	Send      func(msg string) error
	// forge:optional-dep
	OptClock  func() time.Time
}
`,
	})
	defaults, todos := computeFuncDefaults(handlerDir)
	if len(defaults) != 2 {
		t.Fatalf("want 2 func defaults (Now, NewID), got %d: %+v", len(defaults), defaults)
	}
	byField := map[string]DepsFuncDefault{}
	for _, d := range defaults {
		byField[d.FieldName] = d
	}
	if byField["Now"].Expr != frameworkClockExpr || !byField["Now"].NeedsTime {
		t.Errorf("Now default = %+v, want time.Now/NeedsTime", byField["Now"])
	}
	if byField["NewID"].Expr != frameworkIDGenExpr || !byField["NewID"].NeedsULID {
		t.Errorf("NewID default = %+v, want ulid/NeedsULID", byField["NewID"])
	}
	if len(todos) != 1 || todos[0].FieldName != "Send" {
		t.Fatalf("want exactly one TODO (Send), got %+v", todos)
	}
}

// TestComputeAutoStubs_UnresolvedSelector covers the graceful-skip
// path: a Deps field typed `pkg.X` where `pkg` is imported but the
// package fails to load. The field falls back to the "stays nil"
// behavior; computeAutoStubs records the field on the unresolved
// slice so the template can emit a TODO comment.
func TestComputeAutoStubs_UnresolvedSelector(t *testing.T) {
	handlerDir := writeTestModule(t, map[string]string{
		"internal/repo/repo.go": "", // remove the would-be target package
		"internal/handlers/billing/service.go": `package billing

import broken "example.com/proj/internal/does_not_exist"

type Deps struct {
	Repo broken.Repository
}
`,
	})

	stubs, unresolved := computeAutoStubs(handlerDir, "billing")
	if len(stubs) != 0 {
		t.Errorf("expected zero stubs (selector unloadable), got %d", len(stubs))
	}
	if len(unresolved) != 1 {
		t.Fatalf("expected exactly one unresolved entry, got %v", unresolved)
	}
	u := unresolved[0]
	if u.FieldName != "Repo" {
		t.Errorf("unresolved FieldName = %q, want %q", u.FieldName, "Repo")
	}
	if u.TypeExpr != "broken.Repository" {
		t.Errorf("unresolved TypeExpr = %q, want %q", u.TypeExpr, "broken.Repository")
	}
}

// TestMergeExtraImports verifies the dedupe-by-path behavior and the
// deterministic Path-sorted ordering of the assembled list.
func TestMergeExtraImports(t *testing.T) {
	services := []BootstrapTestServiceData{
		{
			AutoStubs: []DepsAutoStub{
				{
					CrossPackage: true,
					ExtraImports: []ExtraImport{
						{Path: "example.com/proj/internal/repo", Alias: "repo"},
						{Path: "context", Alias: "context"},
					},
				},
			},
		},
		{
			AutoStubs: []DepsAutoStub{
				{
					CrossPackage: true,
					ExtraImports: []ExtraImport{
						// Duplicate of the first service's repo import.
						{Path: "example.com/proj/internal/repo", Alias: "repo"},
						{Path: "example.com/proj/internal/audit", Alias: "audit"},
					},
				},
				{
					// A LOCAL-interface stub (CrossPackage=false) that still
					// carries signature imports. mergeExtraImports used to skip
					// these, which is exactly the bug: the interface type is
					// local, but its parameter/result types need not be, and the
					// stub renders those signatures verbatim. Dropping the import
					// produced `undefined: <pkg>` in the generated
					// helpers_gen_test.go (control-plane's proxy_authz handler).
					CrossPackage: false,
					ExtraImports: []ExtraImport{
						{Path: "example.com/proj/internal/localsig", Alias: "localsig"},
					},
				},
			},
		},
	}

	got := mergeExtraImports(services)
	wantPaths := []string{
		"context",
		"example.com/proj/internal/audit",
		"example.com/proj/internal/localsig",
		"example.com/proj/internal/repo",
	}
	if len(got) != len(wantPaths) {
		t.Fatalf("got %d imports, want %d (%v)", len(got), len(wantPaths), got)
	}
	for i, p := range wantPaths {
		if got[i].Path != p {
			t.Errorf("imports[%d].Path = %q, want %q", i, got[i].Path, p)
		}
	}
	// The local-interface stub's import must be present — see the comment
	// on that stub above for why the old "ignore non-CrossPackage" premise
	// was wrong.
	var sawLocal bool
	for _, ei := range got {
		if ei.Path == "example.com/proj/internal/localsig" {
			sawLocal = true
		}
	}
	if !sawLocal {
		t.Error("mergeExtraImports dropped a local-interface stub's signature import")
	}
}

// TestTypesResolver_OneLoadForEveryService pins the batching the harness
// step depends on to be cheap: GenerateBootstrapTesting resolves every
// service's cross-package Deps interfaces through one typesResolver, which
// loads all of them in ONE go/packages call. The step used to type-check
// each field's package from source, separately — 72s of reliant's 74s
// `generate --steps harness`. The stubs must be exactly the ones a
// per-directory resolution produces.
func TestTypesResolver_OneLoadForEveryService(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks fixture modules via the go toolchain; runs in task test")
	}
	billingDir := writeTestModule(t, nil)
	root := filepath.Dir(filepath.Dir(filepath.Dir(billingDir)))
	for rel, src := range map[string]string{
		"internal/audit/audit.go": `package audit

// Log records what a handler did.
type Log interface {
	Record(action string) error
}
`,
		"internal/handlers/invoice/service.go": `package invoice

import (
	"example.com/proj/internal/audit"
	"example.com/proj/internal/repo"
)

// Deps is the invoice service's dependency graph.
type Deps struct {
	Repo  repo.Repository
	Audit audit.Log
}
`,
	} {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	invoiceDir := filepath.Join(root, "internal", "handlers", "invoice")
	dirs := []string{billingDir, invoiceDir}

	if got, want := depsSelectorImports(dirs), []string{"example.com/proj/internal/audit", "example.com/proj/internal/repo"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("depsSelectorImports = %v, want %v", got, want)
	}
	shared := newTypesResolver(root, depsSelectorImports(dirs))
	for _, dir := range dirs {
		got, _, gotUnresolved := computeDepsDoubles(dir, shared)
		want, wantUnresolved := computeAutoStubs(dir, "")
		if len(got) == 0 || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotUnresolved, wantUnresolved) {
			t.Errorf("%s: shared resolver stubs = %+v (unresolved %v), per-directory = %+v (unresolved %v)",
				filepath.Base(dir), got, gotUnresolved, want, wantUnresolved)
		}
	}
	if shared.loads != 1 {
		t.Errorf("shared resolver ran %d go/packages loads for two services' Deps, want 1", shared.loads)
	}
}
