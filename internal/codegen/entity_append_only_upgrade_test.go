// File: internal/codegen/entity_append_only_upgrade_test.go
//
// The end-to-end half of the append-only upgrade path: a table that was
// append-only BEFORE forge started writing COMMENT ON TABLE must still
// project as append-only through the entity/plan pipeline the store
// generator reads.
//
// pkg/schemadef proves the marker is DETECTED off the guard trigger. This
// proves the detection survives the two projections between the catalog and
// the generator — buildEntityDef and EntityDefToPlanEntity — because a fact
// that is read correctly and then dropped one layer up is indistinguishable
// from never having been read.

package codegen

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

func ledgerTable(name string, comment string, triggers []string) schemadef.Table {
	return schemadef.Table{
		Name:     name,
		Comment:  comment,
		Triggers: triggers,
		PKCols:   []string{"id"},
		Columns: []schemadef.Column{
			{Name: "id", Type: schemadef.CanonicalType("string"), IsPK: true, NotNull: true},
			{Name: "amount_cents", Type: schemadef.CanonicalType("int64"), NotNull: true},
		},
	}
}

func ledgerService() ServiceDef {
	return ServiceDef{Name: "BillingService", Package: "services.billing.v1", ProtoFile: "proto/services/billing/v1/billing.proto"}
}

// A pre-existing ledger — guard trigger, no comment — must reach the plan
// with AppendOnly set. Without it the generated store keeps UpdateX,
// UpdateXMasked and DeleteX for a table postgres rejects every write to,
// and the immutability guarantee lapses for exactly the tables that had it
// before the feature existed.
func TestAppendOnlyUpgrade_GuardTriggerProjectsThroughToThePlan(t *testing.T) {
	table := ledgerTable("payments", "", []string{schemadef.AppendOnlyGuardTrigger("payments")})

	def := buildEntityDef("Payment", table, ledgerService())
	if !def.AppendOnly {
		t.Fatal("EntityDef must be append-only for a table carrying forge's guard trigger")
	}
	if !EntityDefToPlanEntity(def).AppendOnly {
		t.Error("PlanEntity must carry append-only through — it is what the store generator reads")
	}
}

// The comment path still works: the upgrade fallback must not have replaced
// the declaration it exists to back up.
func TestAppendOnlyUpgrade_CatalogCommentStillProjects(t *testing.T) {
	table := ledgerTable("payments", schemadef.TableMarkerAppendOnly+" — rows are never rewritten.", nil)

	if !EntityDefToPlanEntity(buildEntityDef("Payment", table, ledgerService())).AppendOnly {
		t.Error("a table declaring the marker in its catalog comment must project as append-only")
	}
}

// The negative case is what keeps the fallback honest. An ordinary mutable
// entity with an updated_at stamper must reach the plan WITHOUT append-only
// — inventing the marker deletes Update/Delete from a store whose callers
// legitimately use them, which breaks a build that was correct.
func TestAppendOnlyUpgrade_MutableEntityWithATriggerIsUntouched(t *testing.T) {
	table := ledgerTable("invoices", "", []string{"invoices_set_updated_at"})

	if EntityDefToPlanEntity(buildEntityDef("Invoice", table, ledgerService())).AppendOnly {
		t.Error("an updated_at stamper must not make an ordinary entity append-only")
	}
}
