package codegen

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Two defects in the owner-scoping scaffold, both found building a
// multi-tenant app whose every table declared `forge:owner` at birth.

// shimMethod returns one method of a handlers_crud.go, from its func line
// to the next top-level declaration.
func shimMethod(t *testing.T, shim, name string) string {
	t.Helper()
	start := strings.Index(shim, "func (s *Service) "+name+"(")
	if start < 0 {
		t.Fatalf("shim has no %s:\n%s", name, shim)
	}
	body := shim[start:]
	if end := strings.Index(body[1:], "\nfunc "); end >= 0 {
		body = body[:end+1]
	}
	return body
}

// tenantServiceAndEntity is a table owned by itself: tenants.id is both the
// key and the forge:owner column.
func tenantServiceAndEntity() (ServiceDef, []EntityDef) {
	svc, entities := ownerTestServiceAndEntity(false, true)
	e := &entities[0]
	for i := range e.Columns {
		e.Columns[i].Owner = e.Columns[i].Name == "id"
	}
	return svc, entities
}

func generateShim(t *testing.T, svc ServiceDef, entities []EntityDef) (string, error) {
	t.Helper()
	projectDir, handlerDir := ownerTestHandlerDir(t)
	if _, err := GenerateCRUDHandlers(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, nil); err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(handlerDir, "handlers_crud.go"))
	if err != nil {
		t.Fatalf("read shim: %v", err)
	}
	return string(data), nil
}

// When the owner column IS the primary key, the Update wrapper stamped it:
// `e.ID = owner`. An update aimed at another tenant was silently redirected
// onto the caller's own row — `{id: "other-tenant", slug: "other-tenant"}`
// came back AlreadyExists on the caller's slug instead of NotFound. The
// predicate alone scopes such a write; the stamp must never be emitted.
func TestOwnerColumnIsPK_UpdatePredicatesWithoutStamping(t *testing.T) {
	svc, entities := tenantServiceAndEntity()
	shim, err := generateShim(t, svc, entities)
	if err != nil {
		t.Fatalf("GenerateCRUDHandlers: %v", err)
	}
	update := shimMethod(t, shim, "UpdateCrew")
	if strings.Contains(update, "e.ID = owner") || strings.Contains(update, "op.Entity =") {
		t.Errorf("an Update over a self-owned table must never stamp the key:\n%s", update)
	}
	for _, want := range []string{
		"\n\tclaims, err := middleware.GetUser(ctx)\n",
		`persist(ctx, e, append(opts, orm.WhereEq("id", owner))...)`,
	} {
		if !strings.Contains(update, want) {
			t.Errorf("the self-owned Update must still resolve the caller and predicate the stored row (%q):\n%s", want, update)
		}
	}
	// Create keeps the stamp: it is how a row becomes the caller's at all.
	if create := shimMethod(t, shim, "CreateCrew"); !strings.Contains(create, "e.ID = owner") {
		t.Errorf("the Create wrapper must still stamp the owner:\n%s", create)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "shim.go", shim, parser.SkipObjectResolution); err != nil {
		t.Fatalf("shim is not valid Go: %v\n%s", err, shim)
	}

	// The audit's paste-in snippet is the same bytes as the birth scaffold.
	snippet, err := RenderScopedCRUDShim("UpdateCrew", "UpdateCrewRequest", "UpdateCrewResponse", "Crew", "id", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(update, snippet[strings.Index(snippet, "func "):]) {
		t.Errorf("the audit snippet drifted from the birth scaffold:\n--- snippet\n%s\n--- birth\n%s", snippet, update)
	}
}

// ownedReadOnlyEntity marks company_id the way a tenant column is declared:
// NOT NULL with no DEFAULT, forge:read-only (clients must not write it) and
// forge:owner.
func ownedReadOnlyEntity(authRequired bool) (ServiceDef, []EntityDef) {
	svc, entities := ownerTestServiceAndEntity(true, authRequired)
	for i := range entities[0].Fields {
		if entities[0].Fields[i].Name == "company_id" {
			entities[0].Fields[i].ReadOnly = true
		}
	}
	return svc, entities
}

// forge:read-only + forge:owner on a NOT NULL column failed generate with
// "can never be populated by the generated Create path" — while the scoped
// Create wrapper forge scaffolds stamps exactly that column from the
// caller. The only fix was a second marker in the column's one comment
// (`'forge:owner forge:fill=handler'`), documented nowhere. An owner column
// on an authenticated Create is filled by the handler by definition.
func TestOwnerReadOnlyColumn_ScopedCreateIsSatisfiable(t *testing.T) {
	svc, entities := ownedReadOnlyEntity(true)
	shim, err := generateShim(t, svc, entities)
	if err != nil {
		t.Fatalf("an owner column the scoped Create stamps must not be reported unsatisfiable: %v", err)
	}
	create := shimMethod(t, shim, "CreateCrew")
	if !strings.Contains(create, "e.CompanyID = owner") {
		t.Errorf("the scoped Create must stamp the owner column:\n%s", create)
	}

	// With fill=handler declared as well, the wrapper's own stamp is the
	// assignment; a FORGE_SCAFFOLD reminder to write a second one is noise.
	for i := range entities[0].Columns {
		if entities[0].Columns[i].Name == "company_id" {
			entities[0].Columns[i].FillStrategy = "handler"
		}
	}
	shim, err = generateShim(t, svc, entities)
	if err != nil {
		t.Fatal(err)
	}
	if create := shimMethod(t, shim, "CreateCrew"); strings.Contains(create, "FORGE_SCAFFOLD: company_id") {
		t.Errorf("the stamped owner column must not also get a fill=handler reminder:\n%s", create)
	}
}

// A PUBLIC Create gets no scoping wrapper, so nothing stamps its owner
// column, and the refusal stands — naming the combined-marker spelling.
func TestOwnerReadOnlyColumn_PublicCreateIsStillUnsatisfiable(t *testing.T) {
	svc, entities := ownedReadOnlyEntity(false)
	_, err := generateShim(t, svc, entities)
	if err == nil {
		t.Fatal("a public Create has no wrapper to stamp the owner column; generate must still refuse it")
	}
	for _, want := range []string{"crews.company_id", "can never be populated", "'forge:owner forge:fill=handler'"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must say %q:\n%v", want, err)
		}
	}
}
