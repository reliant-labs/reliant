// File: internal/codegen/crud_scoping_snippet.go
//
// Rendering the owner-scoping wrapper for a handler that ALREADY EXISTS.
//
// # The scaffold that structurally cannot fire
//
// When a table declares `forge:owner` and an RPC over it is
// authenticated, the CRUD shim template emits a complete scoping wrapper
// — claims resolved, op seam wrapped, the declared column named in the
// predicate — leaving one placeholder expression for the policy only the
// app can supply. That is a good scaffold and it works.
//
// It works exactly once, at the file's BIRTH. ensureCRUDShimFile does its
// only full write when handlers_crud.go is absent; on every later run it
// appends blocks for method names the file does not already contain, and
// never rewrites an existing method. The file is scaffold-once by design,
// and that design is right — it is the user's file.
//
// But `forge:owner` is a COMMENT ON COLUMN, which lives in a migration.
// In the natural order of work — scaffold the entity, build its handlers,
// later realize the rows belong to distinct principals and declare it —
// the handler file was born long before the declaration existed. So the
// scaffold's precondition (file absent) and the declaration's arrival are
// mutually exclusive in every project that did not know about owner
// scoping on day one.
//
// A downstream run hit exactly this: it declared the marker across five
// tables, ran generate, and found handlers_crud.go untouched. The
// `unscoped_auth` gate then correctly refused to let it ship, pointing at
// a remediation that could not be produced. A refusal naming a next step
// the user cannot take costs more than no refusal — it burns turns and it
// teaches people the gate is arbitrary, which is how a gate gets disabled.
//
// # Printing the code instead of promising it
//
// Forge cannot write into that file, but it knows everything needed to
// SHOW the user what to write: which RPCs are unscoped, which column the
// project declared, which seam resolves the caller, and which op shape
// each RPC delegates to. So the gate hands over the wrapper as text.
//
// The one rule that makes this safe is that the snippet must be the SAME
// BYTES as the birth-time scaffold. A second rendering path would drift,
// and a drifted snippet is worse than none: the user pastes something
// that compiles and scopes differently from what a greenfield project
// receives, and the difference surfaces as a security bug rather than a
// build error. RenderScopedCRUDShim therefore renders the real template
// with Scoped set, through the same renderCRUDShimMethods the scaffolder
// calls. TestRenderScopedCRUDShim_MatchesTheBirthScaffold pins that.
//
// # What it refuses to render
//
// A non-CRUD RPC (`TransferOrder`, `ArchiveWorkspace`) maps to no
// generated op, so there is no Fetch/Filters/Persist seam to wrap and no
// honest wrapper to offer. An absent owner column leaves the predicate
// with nothing to name. Both return an error rather than a plausible
// block, because emitting a snippet that does not apply re-creates the
// dead end this file exists to close.

package codegen

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/internal/naming"
)

// RenderScopedCRUDShim renders the owner-scoping handler wrapper for one
// CRUD RPC: the caller resolution, the placeholder owner expression, and
// the op-seam override that puts ownerColumn into the query.
//
// It is the same template, through the same renderer, that
// ensureCRUDShimFile uses at a handler file's birth — so what a user
// pastes into an existing handlers_crud.go and what a greenfield project
// is scaffolded are identical.
//
// method must be a CRUD RPC name (Create/Get/List/Update/Delete + entity)
// and ownerColumn must be non-empty; anything else is an error rather
// than a guess.
func RenderScopedCRUDShim(method, inputType, outputType, entity, ownerColumn string) (string, error) {
	if strings.TrimSpace(ownerColumn) == "" {
		return "", fmt.Errorf("render scoping wrapper for %s: no owner column — the predicate has nothing to name", method)
	}
	op, _ := parseCRUDOperation(method)
	if op == "" {
		return "", fmt.Errorf("render scoping wrapper for %s: not a CRUD RPC, so forge generated no op seam to wrap", method)
	}

	// The arguments are proto spellings; the shim spells Go identifiers,
	// each by the rule of the generator that declares it.
	data := CRUDMethodTemplateData{
		MethodName:   naming.GoCamelCase(method),
		InputType:    naming.GoCamelCase(inputType),
		OutputType:   naming.GoCamelCase(outputType),
		EntityName:   entity,
		EntityPbType: naming.GoCamelCase(entity),
		EntityLower:  strings.ToLower(entity),
		Operation:    op,
		AuthRequired: true,
		AuthSeam:     crudAuthSeam,
		OwnerColumn:  ownerColumn,
		OwnerField:   naming.ColumnGoName(ownerColumn),
		Scoped:       true,
	}

	block, err := renderCRUDShimMethods([]CRUDMethodTemplateData{data})
	if err != nil {
		return "", fmt.Errorf("render scoping wrapper for %s: %w", method, err)
	}
	return strings.TrimSpace(block), nil
}
