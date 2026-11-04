package codegen

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// crud_owner_scaffold_test.go pins the `forge:owner` shim scaffold end to
// end through GenerateCRUDHandlers.
//
// What is being pinned is a REMEDIATION, not a behavior. `forge:owner`
// injects no WHERE clause and changes no runtime semantics — it arms the
// `unscoped_auth` audit gate, and what these tests assert is that when
// the gate fires, the scaffold that answers it is already in the file:
// claims resolved, op seam wrapped, column named, one placeholder left.
// A gate whose remediation is ten lines of boilerplate per RPC is a gate
// people delete the marker to silence, which was the measured outcome the
// seam change exists to prevent.

func ownerTestHandlerDir(t *testing.T) (projectDir, handlerDir string) {
	t.Helper()
	projectDir = t.TempDir()
	handlerDir = filepath.Join(projectDir, "internal", "handlers", "crews")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	serviceGo := `package crews

import "github.com/reliant-labs/forge/pkg/orm"

type Deps struct {
	DB orm.Context
}

type Service struct {
	deps Deps
}
`
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"), []byte(serviceGo), 0o644); err != nil {
		t.Fatal(err)
	}
	return projectDir, handlerDir
}

// ownerTestServiceAndEntity builds the full CRUD quintet over one entity
// whose company_id column carries the supplied owner flag, with every RPC
// authenticated unless authRequired says otherwise.
func ownerTestServiceAndEntity(owner, authRequired bool) (ServiceDef, []EntityDef) {
	svc := ServiceDef{
		Name:       "CrewsService",
		Package:    "crews.v1",
		GoPackage:  "example.com/test/gen/proto/services/crews/v1",
		PkgName:    "crewsv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "CreateCrew", InputType: "CreateCrewRequest", OutputType: "CreateCrewResponse", AuthRequired: authRequired},
			{Name: "GetCrew", InputType: "GetCrewRequest", OutputType: "GetCrewResponse", AuthRequired: authRequired},
			{Name: "ListCrews", InputType: "ListCrewsRequest", OutputType: "ListCrewsResponse", AuthRequired: authRequired},
			{Name: "UpdateCrew", InputType: "UpdateCrewRequest", OutputType: "UpdateCrewResponse", AuthRequired: authRequired},
			{Name: "DeleteCrew", InputType: "DeleteCrewRequest", OutputType: "DeleteCrewResponse", AuthRequired: authRequired},
		},
		Schemas: map[string][]SchemaFieldDef{
			"crews.v1.CreateCrewRequest": {
				{Name: "name", Kind: "string"},
			},
		},
	}
	entities := []EntityDef{{
		Name: "Crew", TableName: "crews", PkField: "id", PkGoType: "string",
		Fields: []EntityField{
			{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "name", GoName: "Name", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "company_id", GoName: "CompanyId", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
		},
		Columns: []EntityColumn{
			{Name: "id", Type: "string", NotNull: true, IsPK: true},
			{Name: "name", Type: "string", NotNull: true},
			{Name: "company_id", Type: "string", NotNull: true, Owner: owner},
		},
	}}
	return svc, entities
}

func generateOwnerShim(t *testing.T, owner, authRequired bool) string {
	t.Helper()
	projectDir, handlerDir := ownerTestHandlerDir(t)
	svc, entities := ownerTestServiceAndEntity(owner, authRequired)
	crudMethods := MatchCRUDMethods(svc, entities)
	if len(crudMethods) != 5 {
		t.Fatalf("fixture should match all five CRUD shapes, matched %d", len(crudMethods))
	}
	if _, err := GenerateCRUDHandlers(svc, crudMethods, "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDHandlers: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(handlerDir, "handlers_crud.go"))
	if err != nil {
		t.Fatalf("read shim: %v", err)
	}
	return string(data)
}

// TestOwnerColumn_ScaffoldsScopingOnEveryShape is the whole point of the
// seam change: a declared owner column plus an authenticated RPC produces
// a shim that already scopes, in every one of the five shapes.
func TestOwnerColumn_ScaffoldsScopingOnEveryShape(t *testing.T) {
	shim := generateOwnerShim(t, true, true)

	// Every shape resolves the caller. This is also exactly what the
	// unscoped_auth audit scans for (a body naming the auth seam), so a
	// scaffold that omitted it on one shape would leave that RPC failing
	// a gate whose remediation forge claimed to have written.
	// Counted as the statement, not the identifier: the file header and
	// the per-method docs both mention the seam in prose.
	if got := countOccurrences(shim, "\n\tclaims, err := middleware.GetUser(ctx)\n"); got != 5 {
		t.Errorf("all five shims must resolve the caller; found %d GetUser calls:\n%s", got, shim)
	}
	// The stale "nothing below reads WHO they are" line must not survive
	// next to a body that does exactly that — a scaffold whose comment
	// contradicts its code teaches the reader to stop believing the
	// comments, which is the failure this whole file's prose fights.
	if contains(shim, "AUTHENTICATED, UNSCOPED") {
		t.Errorf("a scoped shim must not carry the UNSCOPED disclaimer:\n%s", shim)
	}

	for _, want := range []string{
		// List scopes in the query, preserving the generated per-field filters.
		"op.Filters = func(ctx context.Context, r *pb.ListCrewsRequest) ([]orm.QueryOption, error)",
		// Get and Delete pass the predicate INTO the PK operation rather
		// than comparing after it — the seam that did not exist before.
		"op.Fetch = func(ctx context.Context, id string, opts ...orm.QueryOption) (*db.Crew, error)",
		"op.Persist = func(ctx context.Context, id string, opts ...orm.QueryOption) error",
		// Update needs both halves: stamp the owner, and check the stored row.
		"op.Persist = func(ctx context.Context, e *db.Crew, opts ...orm.QueryOption) error",
		// Create stamps from the caller, never from the wire.
		"op.Entity = func(ctx context.Context, r *pb.CreateCrewRequest) (*db.Crew, error)",
		"e.CompanyID = owner",
		// The column is named in the predicate, not left as a TODO.
		`orm.WhereEq("company_id", owner)`,
	} {
		if !contains(shim, want) {
			t.Errorf("scoping scaffold missing %q:\n%s", want, shim)
		}
	}

	// The one thing forge cannot know stays marked, so `forge lint
	// --scaffolds` and `forge project audit` keep failing until a person
	// writes the real claims-to-column mapping.
	if !contains(shim, "FORGE_SCAFFOLD") {
		t.Errorf("the placeholder owner expression must stay marked:\n%s", shim)
	}

	if _, err := parser.ParseFile(token.NewFileSet(), "shim.go", shim, parser.SkipObjectResolution); err != nil {
		t.Fatalf("scoped shim is not valid Go: %v\n----\n%s", err, shim)
	}
}

// TestOwnerColumn_ScopedShimImportsWhatItUses pins the import block. A
// scaffold that names orm and middleware without importing them does not
// compile, which turns a security remediation into a broken build.
func TestOwnerColumn_ScopedShimImportsWhatItUses(t *testing.T) {
	shim := generateOwnerShim(t, true, true)
	for _, want := range []string{
		`"github.com/reliant-labs/forge/pkg/orm"`,
		`"example.com/test/pkg/middleware"`,
		`"example.com/test/internal/db"`,
	} {
		if !contains(shim, want) {
			t.Errorf("scoped shim must import %s:\n%s", want, shim)
		}
	}
	if got := countOccurrences(shim, `"github.com/reliant-labs/forge/pkg/orm"`); got != 1 {
		t.Errorf("orm must be imported exactly once, got %d (a duplicate import does not compile)", got)
	}
}

// TestNoOwnerColumn_ScaffoldsNothing is the contrast that proves the
// scaffold is driven by the declaration and not by "this looks like an id
// column". Without the marker the shims stay bare delegations — which is
// also what keeps a fresh scaffold passing forge's own lint.
func TestNoOwnerColumn_ScaffoldsNothing(t *testing.T) {
	shim := generateOwnerShim(t, false, true)
	for _, unwanted := range []string{"\n\tclaims, err := middleware.GetUser(ctx)\n", "orm.WhereEq", "FORGE_SCAFFOLD"} {
		if contains(shim, unwanted) {
			t.Errorf("an undeclared owner must scaffold nothing, found %q:\n%s", unwanted, shim)
		}
	}
	if !contains(shim, "return crud.HandleGet(s.crudGetCrewOp())(ctx, req)") {
		t.Errorf("unscoped shims must stay bare delegations:\n%s", shim)
	}
}

// TestOwnerColumn_PublicRPCIsNotScoped pins the second half of the
// predicate. An owned table reached by an RPC the proto declares
// auth_required: false is not an unscoped_auth finding, and scaffolding a
// GetUser call there would reject the credential-less callers the proto
// advertises as welcome — forge overruling a declaration its author made
// deliberately.
func TestOwnerColumn_PublicRPCIsNotScoped(t *testing.T) {
	shim := generateOwnerShim(t, true, false)
	if contains(shim, "\n\tclaims, err := middleware.GetUser(ctx)\n") {
		t.Errorf("a public RPC must not be given a GetUser call it would fail on:\n%s", shim)
	}
}

func countOccurrences(haystack, needle string) int {
	n := 0
	for i := 0; i+len(needle) <= len(haystack); {
		if haystack[i:i+len(needle)] == needle {
			n++
			i += len(needle)
			continue
		}
		i++
	}
	return n
}
