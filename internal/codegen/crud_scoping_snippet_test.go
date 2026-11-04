package codegen

import (
	"strings"
	"testing"
)

// The scoping wrapper forge scaffolds into handlers_crud.go can only ever
// be written at the file's BIRTH: ensureCRUDShimFile does the one full
// write when the file is absent, and thereafter appends blocks for
// method names it cannot find. A `forge:owner` declaration that arrives
// in a later migration — which is the normal order of work, since the
// marker is a COMMENT ON COLUMN and the entity's handlers were scaffolded
// when the entity was born — therefore reaches a file forge will never
// rewrite.
//
// That leaves the `unscoped_auth` gate correctly refusing to let the user
// ship, while the remediation it points at cannot be generated. The way
// out is to hand the user the exact code instead, and the only honest
// source for "the exact code" is the template that WOULD have written it.
// RenderScopedCRUDShim renders that template directly, so the snippet a
// user pastes and the scaffold a greenfield project receives cannot drift
// apart — they are the same bytes from the same file.

func TestRenderScopedCRUDShim_GetScopesTheFetch(t *testing.T) {
	got, err := RenderScopedCRUDShim("GetCustomer", "GetCustomerRequest", "GetCustomerResponse", "Customer", "company_id")
	if err != nil {
		t.Fatalf("RenderScopedCRUDShim: %v", err)
	}
	for _, want := range []string{
		"func (s *Service) GetCustomer(",
		CRUDAuthSeam() + "(ctx)",
		"owner := claims.UserID",
		"FORGE_SCAFFOLD",
		`orm.WhereEq("company_id", owner)`,
		"op.Fetch = func(",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered Get shim is missing %q — the snippet must be the whole wrapper, not a description of it\n---\n%s", want, got)
		}
	}
}

func TestRenderScopedCRUDShim_CreateStampsTheOwner(t *testing.T) {
	got, err := RenderScopedCRUDShim("CreateCustomer", "CreateCustomerRequest", "CreateCustomerResponse", "Customer", "company_id")
	if err != nil {
		t.Fatalf("RenderScopedCRUDShim: %v", err)
	}
	// Create stamps the column from the caller rather than filtering on
	// it: a create that read company_id off the wire would let any
	// caller file a row under another principal's name.
	if !strings.Contains(got, "e.CompanyID = owner") {
		t.Errorf("rendered Create shim must stamp the owner field from the caller\n---\n%s", got)
	}
	if strings.Contains(got, `orm.WhereEq("company_id", owner)`) {
		t.Errorf("Create must not filter on the owner column — there is no stored row to filter\n---\n%s", got)
	}
}

func TestRenderScopedCRUDShim_ListScopesTheQueryNotThePage(t *testing.T) {
	got, err := RenderScopedCRUDShim("ListCustomers", "ListCustomersRequest", "ListCustomersResponse", "Customer", "company_id")
	if err != nil {
		t.Fatalf("RenderScopedCRUDShim: %v", err)
	}
	if !strings.Contains(got, "op.Filters = func(") {
		t.Errorf("List must wrap op.Filters so the predicate lands in the QUERY — a post-filtered page still reports a total_count over rows the caller may not see\n---\n%s", got)
	}
}

func TestRenderScopedCRUDShim_UpdateGuardsTheStoredRow(t *testing.T) {
	got, err := RenderScopedCRUDShim("UpdateCustomer", "UpdateCustomerRequest", "UpdateCustomerResponse", "Customer", "company_id")
	if err != nil {
		t.Fatalf("RenderScopedCRUDShim: %v", err)
	}
	// Both halves are needed: the predicate is checked against the STORED
	// row so a caller cannot take ownership by rewriting company_id in
	// the request, and the stamp keeps the written row on the same owner.
	if !strings.Contains(got, "op.Persist = func(") || !strings.Contains(got, "e.CompanyID = owner") {
		t.Errorf("Update needs BOTH the stored-row predicate and the stamp\n---\n%s", got)
	}
}

func TestRenderScopedCRUDShim_DeleteScopesThePersist(t *testing.T) {
	got, err := RenderScopedCRUDShim("DeleteCustomer", "DeleteCustomerRequest", "DeleteCustomerResponse", "Customer", "company_id")
	if err != nil {
		t.Fatalf("RenderScopedCRUDShim: %v", err)
	}
	if !strings.Contains(got, `orm.WhereEq("company_id", owner)`) {
		t.Errorf("Delete must be scoped like Get — a row that is not this caller's is not deleted\n---\n%s", got)
	}
}

// A non-CRUD RPC maps to no generated op, so there is no seam to wrap and
// no honest snippet to offer. Returning an error rather than a plausible
// block is the point: a remediation that does not apply is how this
// finding became a dead end in the first place.
func TestRenderScopedCRUDShim_RejectsNonCRUDMethod(t *testing.T) {
	if _, err := RenderScopedCRUDShim("TransferOrder", "TransferOrderRequest", "TransferOrderResponse", "Order", "company_id"); err == nil {
		t.Fatal("want an error for a non-CRUD RPC — forge has no op seam to wrap, so it must not invent a snippet")
	}
}

func TestRenderScopedCRUDShim_RejectsEmptyOwnerColumn(t *testing.T) {
	if _, err := RenderScopedCRUDShim("GetCustomer", "GetCustomerRequest", "GetCustomerResponse", "Customer", ""); err == nil {
		t.Fatal("want an error with no owner column — the predicate has nothing to name")
	}
}

// The snippet and the birth-time scaffold must be the SAME bytes. If they
// are rendered by different code they will drift, and a drifted snippet is
// worse than none: the user pastes something that compiles and scopes
// differently from what forge writes on a greenfield project.
func TestRenderScopedCRUDShim_MatchesTheBirthScaffold(t *testing.T) {
	data := CRUDMethodTemplateData{
		MethodName: "GetCustomer", InputType: "GetCustomerRequest", OutputType: "GetCustomerResponse",
		EntityName: "Customer", Operation: "get", AuthRequired: true, AuthSeam: CRUDAuthSeam(),
		OwnerColumn: "company_id", OwnerField: "CompanyId", Scoped: true,
	}
	birth, err := renderCRUDShimMethods([]CRUDMethodTemplateData{data})
	if err != nil {
		t.Fatalf("render birth scaffold: %v", err)
	}
	snippet, err := RenderScopedCRUDShim("GetCustomer", "GetCustomerRequest", "GetCustomerResponse", "Customer", "company_id")
	if err != nil {
		t.Fatalf("RenderScopedCRUDShim: %v", err)
	}
	if strings.TrimSpace(birth) != strings.TrimSpace(snippet) {
		t.Errorf("the offered snippet and the birth-time scaffold differ — they must render from one template\n--- birth ---\n%s\n--- snippet ---\n%s", birth, snippet)
	}
}
