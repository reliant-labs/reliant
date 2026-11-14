package contract

import (
	"os"
	"path/filepath"
	"testing"
)

// A mock's bytes are the run's FINAL bytes, whatever else in the tree is
// true at the time.
//
// Observed on reliant: a `forge generate` whose generate hook failed left
// mock_gen.go files on disk with `"…/gen/reliant/v1"` where the committed
// file had `reliantv1 "…/gen/reliant/v1"`. Two defects met there. The mock
// writer rendered the spec goimports later REWRITES (goimports spells a name
// whenever the package's declared name is not the one it assumes from the
// path), so until the pipeline's goimports pass ran, the bytes on disk were
// not the bytes the run would end with. And a Deps interface whose package
// did not type-check — a package mid-edit in a shared checkout — was
// qualified by the foreign file's own alias instead of the declared name,
// so the mock's spelling depended on whether some unrelated code compiled.

// TestImportDeclarationIsTheGoimportsFixedPoint pins Declaration to the spec
// goimports would leave: a name is spelled exactly when goimports would
// spell it (or the contract spelled it).
func TestImportDeclarationIsTheGoimportsFixedPoint(t *testing.T) {
	for _, tc := range []struct {
		imp  ImportDef
		want string
	}{
		// Declared name differs from the path's last element: goimports
		// writes it out, so the render must too.
		{ImportDef{Path: "github.com/acme/app/gen/reliant/v1", Name: "reliantv1"}, `reliantv1 "github.com/acme/app/gen/reliant/v1"`},
		// The assumed name already matches: no name.
		{ImportDef{Path: "github.com/nats-io/nats.go", Name: "nats"}, `"github.com/nats-io/nats.go"`},
		{ImportDef{Path: "gopkg.in/yaml.v3", Name: "yaml"}, `"gopkg.in/yaml.v3"`},
		{ImportDef{Path: "github.com/google/go-github", Name: "github"}, `"github.com/google/go-github"`},
		{ImportDef{Path: "github.com/jackc/pgx/v5", Name: "pgx"}, `"github.com/jackc/pgx/v5"`},
		{ImportDef{Path: "context", Name: "context"}, `"context"`},
		// An explicit alias the contract wrote stays, even when redundant.
		{ImportDef{Path: "github.com/acme/app/internal/db/core", Name: "core", Explicit: true}, `core "github.com/acme/app/internal/db/core"`},
		// No name known: the bare path.
		{ImportDef{Path: "github.com/acme/app/thing"}, `"github.com/acme/app/thing"`},
	} {
		if got := tc.imp.Declaration(); got != tc.want {
			t.Errorf("Declaration(%+v) = %s, want %s", tc.imp, got, tc.want)
		}
	}
}

// depsAliasProject is a project whose Deps names an interface from a package
// that imports a generated proto package under an alias of its own (`pb`),
// where the package's declared name (`apiv1`) is not its path's last element
// (`v1`) — reliant's shape exactly.
func depsAliasProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"go.mod": "module example.com/proj\n\ngo 1.22\n",
		"gen/api/v1/api.go": `package apiv1

type Thing struct{ ID string }
`,
		"internal/db/store.go": `package db

import (
	"context"

	pb "example.com/proj/gen/api/v1"
)

type Store interface {
	SaveThing(ctx context.Context, thing *pb.Thing) error
}
`,
		"internal/pipeline/contract.go": `package pipeline

import "context"

// Service is the pipeline domain boundary.
type Service interface {
	Approve(ctx context.Context, id string) error
}
`,
		"internal/pipeline/service.go": `package pipeline

import "example.com/proj/internal/db"

// Deps holds dependencies for the pipeline package.
type Deps struct {
	Store db.Store
}
`,
	})
	return root
}

// TestGenerate_ForeignMockIsTheSameWhenItsPackageDoesNotTypeCheck renders the
// mock from the dep's type-checked package, then as it is rendered when that
// package did not type-check (the syntax fallback: the Resolver holds no
// types for it), and requires identical bytes — spelled the way goimports
// would leave them, and compiling.
func TestGenerate_ForeignMockIsTheSameWhenItsPackageDoesNotTypeCheck(t *testing.T) {
	skipTypeCheckUnderShort(t)
	root := depsAliasProject(t)
	contractPath := filepath.Join(root, "internal", "pipeline", "contract.go")
	mockPath := filepath.Join(root, "internal", "pipeline", "mock_gen.go")

	typed := NewResolver(contractPath)
	if typed.foreignPackage("example.com/proj/internal/db") == nil {
		t.Fatal("fixture: internal/db should type-check")
	}
	if err := GenerateWithOptions(contractPath, Options{Resolver: typed}); err != nil {
		t.Fatalf("Generate (typed): %v", err)
	}
	fromTypes, err := os.ReadFile(mockPath)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, mockPath, `apiv1 "example.com/proj/gen/api/v1"`)
	assertContains(t, mockPath, "SaveThingFunc func(context.Context, *apiv1.Thing) error")
	buildDepsProject(t, root)

	// What a load that could not type internal/db leaves: names, no types.
	untyped := NewResolver(contractPath)
	delete(untyped.pkgs, "example.com/proj/internal/db")
	if err := GenerateWithOptions(contractPath, Options{Resolver: untyped}); err != nil {
		t.Fatalf("Generate (syntax fallback): %v", err)
	}
	fromSyntax, err := os.ReadFile(mockPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(fromSyntax) != string(fromTypes) {
		t.Errorf("mock_gen.go depends on whether internal/db type-checked.\n--- typed ---\n%s\n--- syntax fallback ---\n%s", fromTypes, fromSyntax)
	}
}
