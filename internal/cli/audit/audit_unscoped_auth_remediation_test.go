package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// These tests pin the OTHER half of the owner-scoping story: what the
// gate tells you to do once it has correctly refused to let you ship.
//
// The gate arms on a `forge:owner` declaration, which lives in a
// COMMENT ON COLUMN in a migration. The remediation the auth skill
// described — a scoping wrapper forge scaffolds into handlers_crud.go —
// can only be written when that file is BORN, because the file is
// scaffold-once and later runs append blocks for missing methods only.
// So in the natural order of work (scaffold the entity, build handlers,
// later realize the rows need owner scoping and declare it) the entity's
// handlers already exist and the wrapper can never be emitted.
//
// A refusal that names a next step the user cannot take is worse than no
// refusal: it burns turns and it teaches people the gate is arbitrary.
// The fix is that the gate CARRIES the remediation. Forge knows every
// input — which RPCs are unscoped, which column is declared, which seam
// resolves the caller, and the exact template that would have written the
// wrapper — so it can print the code rather than promise it.

// writeExistingShim marks the handler file as already-scaffolded, which
// is the state every real project is in by the time it declares an owner
// column. It is the precondition for the whole finding.
func writeExistingShim(t *testing.T, projectDir, src string) {
	t.Helper()
	path := filepath.Join(projectDir, "internal", "handlers", "shop", "handlers_crud.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestUnscopedAuth_GatingFindingCarriesAPasteableSnippet is the core new
// assertion. The gating detail must include the actual wrapper code for
// each failing RPC — not a sentence about a scaffold that will never
// appear.
func TestUnscopedAuth_GatingFindingCarriesAPasteableSnippet(t *testing.T) {
	src := handlerHeader + delegationBody("GetCustomer")
	dir := writeProject(t, "ShopService", map[string]bool{"GetCustomer": true}, src)
	writeExistingShim(t, dir, src)
	writeOwnerMigration(t, dir, "customers", "company_id")

	cat := auditUnscopedAuth(nil, dir)

	gating, ok := cat.Details["owner_scoped_unscoped_rpcs"].([]unscopedRPC)
	if !ok || len(gating) != 1 {
		t.Fatalf("owner_scoped_unscoped_rpcs = %v, want one entry", cat.Details["owner_scoped_unscoped_rpcs"])
	}
	snippet := gating[0].Remediation
	if snippet == "" {
		t.Fatal("the gating finding carries no remediation — the wrapper cannot be scaffolded into an existing handlers_crud.go, so the gate must hand the user the code instead of naming a scaffold that never runs")
	}
	for _, want := range []string{
		"func (s *Service) GetCustomer(",
		codegen.CRUDAuthSeam() + "(ctx)",
		`orm.WhereEq("company_id", owner)`,
	} {
		if !strings.Contains(snippet, want) {
			t.Errorf("remediation is missing %q — it must be the whole wrapper, naming the declared column\n---\n%s", want, snippet)
		}
	}
}

// TestUnscopedAuth_GatingHintSaysTheScaffoldCannotFire is the honesty
// half. The reason the user has no wrapper is that their handler file
// predates the declaration, and the gate must SAY that — otherwise the
// reader's first move is to re-run `forge generate` and conclude forge is
// broken when nothing changes.
func TestUnscopedAuth_GatingHintSaysTheScaffoldCannotFire(t *testing.T) {
	src := handlerHeader + delegationBody("GetCustomer")
	dir := writeProject(t, "ShopService", map[string]bool{"GetCustomer": true}, src)
	writeExistingShim(t, dir, src)
	writeOwnerMigration(t, dir, "customers", "company_id")

	cat := auditUnscopedAuth(nil, dir)

	hint, _ := cat.Details["owner_scoping_hint"].(string)
	if hint == "" {
		t.Fatal("no owner_scoping_hint on an armed gate — the user needs to be told why re-running generate will not produce the wrapper")
	}
	for _, want := range []string{"handlers_crud.go", "scaffold"} {
		if !strings.Contains(hint, want) {
			t.Errorf("owner_scoping_hint = %q, want it to name %q so the reader can check the claim against their own file", hint, want)
		}
	}
	// It must also name the file the user is expected to edit.
	if !strings.Contains(hint, "internal/handlers") {
		t.Errorf("owner_scoping_hint = %q, want it to name where the snippet goes", hint)
	}
}

// TestUnscopedAuth_AdvisoryFindingCarriesNoSnippet keeps the remediation
// tied to the gate. An unscoped RPC over a table nobody declared owned is
// advice, and forge has no column to name in a predicate — offering a
// snippet there would be inventing a policy the project never declared.
func TestUnscopedAuth_AdvisoryFindingCarriesNoSnippet(t *testing.T) {
	src := handlerHeader + delegationBody("GetProduct")
	dir := writeProject(t, "ShopService", map[string]bool{"GetProduct": true}, src)
	writeExistingShim(t, dir, src)

	cat := auditUnscopedAuth(nil, dir)

	unscoped, ok := cat.Details["unscoped_rpcs"].([]unscopedRPC)
	if !ok || len(unscoped) != 1 {
		t.Fatalf("unscoped_rpcs = %v, want one entry", cat.Details["unscoped_rpcs"])
	}
	if unscoped[0].Remediation != "" {
		t.Errorf("an advisory finding must carry no remediation — no table declared an owner column, so there is no predicate to write\n---\n%s", unscoped[0].Remediation)
	}
	if _, present := cat.Details["owner_scoping_hint"]; present {
		t.Error("owner_scoping_hint must be absent when the gate is not armed")
	}
}

// TestUnscopedAuth_NonCRUDGatingFindingDegradesHonestly covers the
// coverage hole the category documents. A custom RPC maps to no
// generated op, so there is no seam to wrap and no snippet forge can
// honestly render. It must stay silent rather than emit a block that
// does not apply.
func TestUnscopedAuth_NonCRUDRPCGetsNoSnippet(t *testing.T) {
	src := handlerHeader + delegationBody("TransferOrder")
	dir := writeProject(t, "ShopService", map[string]bool{"TransferOrder": true}, src)
	writeExistingShim(t, dir, src)
	writeOwnerMigration(t, dir, "orders", "company_id")

	cat := auditUnscopedAuth(nil, dir)

	unscoped, ok := cat.Details["unscoped_rpcs"].([]unscopedRPC)
	if !ok || len(unscoped) != 1 {
		t.Fatalf("unscoped_rpcs = %v, want one entry", cat.Details["unscoped_rpcs"])
	}
	if unscoped[0].Remediation != "" {
		t.Errorf("a non-CRUD RPC has no generated op seam, so forge must not offer a wrapper for it\n---\n%s", unscoped[0].Remediation)
	}
}

// TestUnscopedAuth_SummaryStillNamesTheDeclaration guards against the
// remediation work weakening the existing message.
func TestUnscopedAuth_SummaryStillNamesTheDeclaration(t *testing.T) {
	src := handlerHeader + delegationBody("GetCustomer")
	dir := writeProject(t, "ShopService", map[string]bool{"GetCustomer": true}, src)
	writeExistingShim(t, dir, src)
	writeOwnerMigration(t, dir, "customers", "company_id")

	cat := auditUnscopedAuth(nil, dir)

	if !strings.Contains(cat.Summary, schemadef.ColumnMarkerOwner) {
		t.Errorf("summary = %q, want it to still name the declaration that armed the gate", cat.Summary)
	}
}
