// File: internal/codegen/named_type_zero_value_test.go
//
// Regression tests for the auto-stub zero-value emitter across the
// WHOLE named-type family, not one member of it at a time.
//
// History: the emitter's fallback for any type it did not recognize was
// `T{}`. That is a composite literal, which Go accepts only for a
// struct, array, slice or map. A first fix taught it to emit `nil` when
// the caller could prove the result was an interface. The next dogfood
// run then hit the identical failure one family over — `type Role
// string` on an orgpolicy service's Deps produced
//
//	func (stubOrgsOrgPolicy) RoleFor(...) (orgpolicy.Role, error) { return orgpolicy.Role{}, nil }
//
// "invalid composite literal type orgpolicy.Role". Named scalars, named
// pointers and defined array/func types are all in the same hole.
//
// So these tests do not assert on emitted TEXT, which is what let the
// bug survive the previous fix — they TYPE-CHECK the generated stub
// against the real package that declares the types. A stub that does
// not compile fails here no matter what shape the generator picked.

package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// namedTypeFixture writes a module whose `orgpolicy` package declares one
// member of every type family a stub result can land in: the named
// scalars that were broken, a named pointer, a defined array, a defined
// func type, plus the struct / interface / slice cases that already
// worked and must not regress.
func namedTypeFixture(t *testing.T) string {
	t.Helper()
	return writeStubFixtureModule(t, "internal/handlers/orgs", map[string]string{
		"internal/orgpolicy/orgpolicy.go": `package orgpolicy

import "context"

// Role is the real reproduction: the ordinary Go way to write a typed
// enum, and exactly what an orgpolicy package has.
type Role string

// Level, Enabled and Ratio round out the named-scalar family.
type Level int
type Enabled bool
type Ratio float64

// Target is a named POINTER type — ` + "`Target{}`" + ` is invalid too.
type Principal struct{ ID string }
type Target *Principal

// Key is a defined ARRAY type; Handler a defined FUNC type.
type Key [4]byte
type Handler func(ctx context.Context) error

// Snapshot is a named STRUCT — the case the composite literal was
// always correct for.
type Snapshot struct{ N int }

// Auditor is an INTERFACE result, fixed in the previous wave.
type Auditor interface {
	Audit(ctx context.Context) error
}

type Service interface {
	RoleFor(ctx context.Context, userID, orgID string) (Role, error)
	LevelFor(ctx context.Context, userID string) (Level, error)
	EnabledFor(ctx context.Context, userID string) (Enabled, error)
	RatioFor(ctx context.Context, userID string) (Ratio, error)
	TargetFor(ctx context.Context, userID string) (Target, error)
	KeyFor(ctx context.Context, userID string) (Key, error)
	HandlerFor(ctx context.Context, userID string) (Handler, error)
	SnapshotFor(ctx context.Context, userID string) (Snapshot, error)
	AuditorFor(ctx context.Context, userID string) (Auditor, error)
	OrgIDsFor(ctx context.Context, userID string) ([]string, error)
	Both(ctx context.Context) (Role, Snapshot, error)
}
`,
		"internal/handlers/orgs/service.go": `package orgs

import (
	"log/slog"

	"example.com/proj/internal/orgpolicy"
)

type Deps struct {
	Logger *slog.Logger
	OrgPolicy  orgpolicy.Service
}
`,
	})
}

// typeCheckStub writes the rendered stub methods into a real file in
// the fixture module and type-checks the package. This is the assertion
// that matters: it is blind to the generator's chosen syntax and only
// asks whether the output is valid Go.
func typeCheckStub(t *testing.T, handlerDir string, res CrossPkgInterfaceResult, ifaceQualified string) {
	t.Helper()

	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\n", filepath.Base(handlerDir))
	b.WriteString("import (\n")
	for _, imp := range SortedNeededImports(res.NeededImports) {
		fmt.Fprintf(&b, "\t%s %q\n", imp.Alias, imp.Path)
	}
	b.WriteString(")\n\n")
	b.WriteString("type zeroCheckStub struct{}\n\n")
	fmt.Fprintf(&b, "var _ %s = zeroCheckStub{}\n\n", ifaceQualified)
	for _, m := range res.Methods {
		results := ""
		if m.Results != "" {
			results = " " + m.Results
		}
		fmt.Fprintf(&b, "func (zeroCheckStub) %s(%s)%s { %s }\n\n", m.Name, m.Params, results, m.ReturnStatement)
	}

	src := b.String()
	path := filepath.Join(handlerDir, "zerocheck_stub.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedTypes | packages.NeedDeps | packages.NeedImports | packages.NeedSyntax, Dir: handlerDir}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		t.Fatalf("load generated stub package: %v", err)
	}
	var problems []string
	for _, p := range pkgs {
		for _, e := range p.Errors {
			problems = append(problems, e.Error())
		}
	}
	if len(problems) > 0 {
		t.Errorf("generated stub does not compile:\n  %s\n--- generated ---\n%s--- end ---",
			strings.Join(problems, "\n  "), src)
	}
}

// TestResolveCrossPkgInterface_NamedTypeResultsCompile is the direct
// reproduction of the docvault3 failure and its whole family.
func TestResolveCrossPkgInterface_NamedTypeResultsCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks or builds fixture modules via the go toolchain; runs in task test")
	}
	handlerDir := namedTypeFixture(t)

	res, ok := ResolveCrossPkgInterface(handlerDir, "orgpolicy", "Service")
	if !ok {
		t.Fatal("expected resolver to succeed for orgpolicy.Service")
	}
	typeCheckStub(t, handlerDir, res, "orgpolicy.Service")
}

// TestResolveCrossPkgInterface_NamedScalarDoesNotUseCompositeLiteral
// pins the specific emission that broke, so a failure names the cause
// rather than only reporting a type error. `orgpolicy.Role{}` is invalid
// for `type Role string` however the rest of the file looks.
func TestResolveCrossPkgInterface_NamedScalarDoesNotUseCompositeLiteral(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks or builds fixture modules via the go toolchain; runs in task test")
	}
	handlerDir := namedTypeFixture(t)

	res, ok := ResolveCrossPkgInterface(handlerDir, "orgpolicy", "Service")
	if !ok {
		t.Fatal("expected resolver to succeed for orgpolicy.Service")
	}
	byName := map[string]InterfaceMethod{}
	for _, m := range res.Methods {
		byName[m.Name] = m
	}

	for _, name := range []string{"RoleFor", "LevelFor", "EnabledFor", "RatioFor", "TargetFor"} {
		m, ok := byName[name]
		if !ok {
			t.Fatalf("missing %s in resolved methods", name)
		}
		if strings.Contains(m.ReturnStatement, "{}") {
			t.Errorf("%s returns a NAMED NON-STRUCT type; a composite literal is invalid Go for it.\n got: %q",
				name, m.ReturnStatement)
		}
	}

	// The previously-fixed families must still be right.
	if got := byName["AuditorFor"].ReturnStatement; got != "return nil, nil" {
		t.Errorf("interface result must still emit nil.\n got: %q\nwant: %q", got, "return nil, nil")
	}
	if got := byName["OrgIDsFor"].ReturnStatement; got != "return nil, nil" {
		t.Errorf("slice result must still emit nil.\n got: %q\nwant: %q", got, "return nil, nil")
	}
}

// TestResolveCrossPkgInterface_MultiNamedResultsAreDistinct guards the
// multi-return construction: two results that both need a declared
// zero must not collide on one name.
func TestResolveCrossPkgInterface_MultiNamedResultsAreDistinct(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks or builds fixture modules via the go toolchain; runs in task test")
	}
	handlerDir := namedTypeFixture(t)

	res, ok := ResolveCrossPkgInterface(handlerDir, "orgpolicy", "Service")
	if !ok {
		t.Fatal("expected resolver to succeed for orgpolicy.Service")
	}
	for _, m := range res.Methods {
		if m.Name != "Both" {
			continue
		}
		// Whatever form the generator picks, the two non-error results
		// must be independently expressed — a body that declares the
		// same identifier twice is a redeclaration error.
		if strings.Count(m.ReturnStatement, "orgpolicy.Role") == 0 {
			t.Errorf("Both's zero for orgpolicy.Role went missing: %q", m.ReturnStatement)
		}
		return
	}
	t.Fatal("missing Both in resolved methods")
}

// TestParseLocalInterfaces_NamedTypeResultsCompile covers the
// locally-declared half of the generator, which renders from the AST
// and shares the same zero-value emitter.
func TestParseLocalInterfaces_NamedTypeResultsCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks or builds fixture modules via the go toolchain; runs in task test")
	}
	handlerDir := writeStubFixtureModule(t, "internal/handlers/orgs", map[string]string{
		"internal/handlers/orgs/service.go": `package orgs

import "context"

type Role string
type Level int
type Snapshot struct{ N int }

type RoleLookup interface {
	RoleFor(ctx context.Context, userID string) (Role, error)
	LevelFor(ctx context.Context, userID string) (Level, error)
	SnapshotFor(ctx context.Context, userID string) (Snapshot, error)
	Both(ctx context.Context) (Role, Level, error)
}

type Deps struct {
	OrgPolicy RoleLookup
}
`,
	})

	locals, err := ParseLocalInterfaces(handlerDir)
	if err != nil {
		t.Fatalf("ParseLocalInterfaces: %v", err)
	}
	iface, ok := locals["RoleLookup"]
	if !ok {
		t.Fatalf("RoleLookup not parsed; got %v", locals)
	}

	// Render the stub into the same package and type-check it — the
	// local path needs no extra imports beyond context.
	var b strings.Builder
	b.WriteString("package orgs\n\nimport \"context\"\n\ntype localZeroStub struct{}\n\nvar _ RoleLookup = localZeroStub{}\n\nvar _ = context.Background\n\n")
	for _, m := range iface.Methods {
		results := ""
		if m.Results != "" {
			results = " " + m.Results
		}
		fmt.Fprintf(&b, "func (localZeroStub) %s(%s)%s { %s }\n\n", m.Name, m.Params, results, m.ReturnStatement)
	}
	src := b.String()
	if err := os.WriteFile(filepath.Join(handlerDir, "zerocheck_stub.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedTypes | packages.NeedDeps | packages.NeedImports | packages.NeedSyntax, Dir: handlerDir}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		t.Fatalf("load generated stub package: %v", err)
	}
	for _, p := range pkgs {
		for _, e := range p.Errors {
			t.Errorf("locally-declared stub does not compile: %v\n--- generated ---\n%s--- end ---", e, src)
		}
	}
}

// TestZeroReturn_ParamNameCollision is the hazard a declared-variable
// form introduces and a literal never had: Go puts parameters in the
// function BODY's scope, so `func (s) F(zero string) Role { var zero
// Role; ... }` is "zero redeclared in this block". A generator that
// assumes no parameter is ever called `zero` is making the same class
// of guess that produced this bug in the first place.
func TestZeroReturn_ParamNameCollision(t *testing.T) {
	handlerDir := writeStubFixtureModule(t, "internal/handlers/orgs", map[string]string{
		"internal/handlers/orgs/service.go": `package orgs

type Role string

type RoleLookup interface {
	// Parameters deliberately named to collide with any obvious
	// generated zero-value variable name.
	RoleFor(zero string, zero0 string, zero1 string) (Role, Role, error)
}

type Deps struct {
	OrgPolicy RoleLookup
}
`,
	})

	locals, err := ParseLocalInterfaces(handlerDir)
	if err != nil {
		t.Fatalf("ParseLocalInterfaces: %v", err)
	}
	iface := locals["RoleLookup"]
	if len(iface.Methods) != 1 {
		t.Fatalf("expected one method, got %v", iface.Methods)
	}
	m := iface.Methods[0]

	var b strings.Builder
	b.WriteString("package orgs\n\ntype collideStub struct{}\n\nvar _ RoleLookup = collideStub{}\n\n")
	fmt.Fprintf(&b, "func (collideStub) %s(%s) %s { %s }\n", m.Name, m.Params, m.Results, m.ReturnStatement)
	src := b.String()
	if err := os.WriteFile(filepath.Join(handlerDir, "zerocheck_stub.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedTypes | packages.NeedDeps | packages.NeedImports | packages.NeedSyntax, Dir: handlerDir}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		t.Fatalf("load generated stub package: %v", err)
	}
	for _, p := range pkgs {
		for _, e := range p.Errors {
			t.Errorf("stub collides with a parameter name: %v\n--- generated ---\n%s--- end ---", e, src)
		}
	}
}
