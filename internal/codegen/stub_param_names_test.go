package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// stubParamCollisionFixture declares one interface in its own package
// (the cross-package auto-stub path) and one in the handler package (the
// local path). Every parameter is named after something a stub's BODY
// spells: the package its result type comes from, a local type the zero
// value names, and `new` (the universal `*new(T)` zero).
//
// An interface method has no body, so all of it is legal Go. The generated
// stub has one, and used to declare the same names.
func stubParamCollisionFixture(t *testing.T) string {
	t.Helper()
	return writeStubFixtureModule(t, "internal/handlers/orgs", map[string]string{
		"internal/orgpolicy/orgpolicy.go": `package orgpolicy

import "context"

type Role string
type Snapshot struct{ N int }

type Service interface {
	RoleFor(ctx context.Context, orgpolicy string, new string) (Role, error)
	SnapshotFor(orgpolicy string) (Snapshot, error)
}
`,
		"internal/handlers/orgs/service.go": `package orgs

import "example.com/proj/internal/orgpolicy"

type Snapshot struct{ N int }

type Lookup interface {
	SnapshotFor(Snapshot string, new string) (Snapshot, error)
	PolicyFor(orgpolicy string) (orgpolicy.Snapshot, error)
}

type Deps struct {
	Policy orgpolicy.Service
	Lookup Lookup
}
`,
	})
}

// TestAutoStub_ParamsNamedLikeBodyIdentifiers type-checks the auto-stubs the
// test harness renders for both fixture interfaces. They compile only if the
// stub's signature leaves the interface's parameter names out of the body's
// scope.
func TestAutoStub_ParamsNamedLikeBodyIdentifiers(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks the fixture module via the go toolchain; runs in task test")
	}
	handlerDir := stubParamCollisionFixture(t)

	res, ok := ResolveCrossPkgInterface(handlerDir, "orgpolicy", "Service")
	if !ok {
		t.Fatal("expected resolver to succeed for orgpolicy.Service")
	}
	locals, err := ParseLocalInterfaces(handlerDir)
	if err != nil {
		t.Fatalf("ParseLocalInterfaces: %v", err)
	}
	lookup, ok := locals["Lookup"]
	if !ok {
		t.Fatalf("Lookup not parsed; got %v", locals)
	}

	var b strings.Builder
	b.WriteString("package orgs\n\nimport (\n\t\"context\"\n\n\t\"example.com/proj/internal/orgpolicy\"\n)\n\nvar _ context.Context\n\n")
	b.WriteString("type crossStub struct{}\n\nvar _ orgpolicy.Service = crossStub{}\n\n")
	for _, m := range res.Methods {
		fmt.Fprintf(&b, "func (crossStub) %s(%s) %s { %s }\n\n", m.Name, m.Params, m.Results, m.ReturnStatement)
	}
	b.WriteString("type localStub struct{}\n\nvar _ Lookup = localStub{}\n\n")
	for _, m := range lookup.Methods {
		fmt.Fprintf(&b, "func (localStub) %s(%s) %s { %s }\n\n", m.Name, m.Params, m.Results, m.ReturnStatement)
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
			t.Errorf("auto-stub does not compile: %v\n--- generated ---\n%s--- end ---", e, src)
		}
	}
}
