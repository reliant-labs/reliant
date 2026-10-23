package codegen

import (
	"strings"
	"testing"
)

// versionedEntity builds a minimal entity carrying a forge:version column,
// so a test can vary only whether the wire message declares the matching
// field.
func versionedEntity(fields []EntityField) EntityDef {
	return EntityDef{
		Name:      "WorkOrder",
		TableName: "work_orders",
		Fields:    fields,
		Columns: []EntityColumn{
			{Name: "id", Type: "string", DeclType: "TEXT", NotNull: true, IsPK: true},
			{Name: "scope", Type: "string", DeclType: "TEXT", NotNull: true},
			{Name: "version", Type: "int64", DeclType: "BIGINT", NotNull: true, Default: "0", Version: true},
		},
	}
}

func TestFindUnversionableEntities_FiresWhenTheWireCannotCarryTheVersion(t *testing.T) {
	entity := versionedEntity([]EntityField{
		{Name: "scope", GoName: "Scope", ProtoType: "string", GoType: "string"},
	})

	got := FindUnversionableEntities(entity)
	if len(got) != 1 {
		t.Fatalf("a forge:version column with no matching wire field must be flagged; got %+v", got)
	}
	if got[0].Entity != "WorkOrder" || got[0].Table != "work_orders" || got[0].Column != "version" {
		t.Errorf("finding = %+v, want WorkOrder/work_orders/version", got[0])
	}

	err := UnversionableEntitiesError(got)
	if err == nil {
		t.Fatal("UnversionableEntitiesError must render a non-nil error for a non-empty finding set")
	}
	// The message has to name the remedy, because the symptom (Aborted on
	// every update after the first) points at concurrency rather than at
	// the missing field that actually causes it.
	if !strings.Contains(err.Error(), "forge:read-only") {
		t.Errorf("error should show the field declaration to add; got:\n%s", err)
	}
}

func TestFindUnversionableEntities_SilentWhenTheFieldIsOnTheWire(t *testing.T) {
	entity := versionedEntity([]EntityField{
		{Name: "scope", GoName: "Scope", ProtoType: "string", GoType: "string"},
		{Name: "version", GoName: "Version", ProtoType: "int64", GoType: "int64", ReadOnly: true},
	})
	if got := FindUnversionableEntities(entity); len(got) != 0 {
		t.Errorf("a version field present on the wire round-trips correctly; got %+v", got)
	}
}

// A plain (non-read-only) version field still round-trips: the repo
// overwrites whatever the client proposed with `version = version + 1`, so
// the concurrency guarantee holds either way. read-only is the better
// shape, but this check is about reachability, not about style.
func TestFindUnversionableEntities_SilentWhenTheFieldIsNotReadOnly(t *testing.T) {
	entity := versionedEntity([]EntityField{
		{Name: "version", GoName: "Version", ProtoType: "int64", GoType: "int64"},
	})
	if got := FindUnversionableEntities(entity); len(got) != 0 {
		t.Errorf("a writable version field still carries the value; got %+v", got)
	}
}

// Entities that never opted in are entirely unaffected — no marker, no
// finding, no behavior change.
func TestFindUnversionableEntities_SilentWithoutTheMarker(t *testing.T) {
	entity := EntityDef{
		Name:      "Material",
		TableName: "materials",
		Fields:    []EntityField{{Name: "sku", GoName: "Sku", ProtoType: "string", GoType: "string"}},
		Columns: []EntityColumn{
			{Name: "sku", Type: "string", DeclType: "TEXT", NotNull: true},
			// Named "version" but carrying no marker: an ordinary column
			// that happens to share the name is not opted in.
			{Name: "version", Type: "string", DeclType: "TEXT", NotNull: true},
		},
	}
	if got := FindUnversionableEntities(entity); len(got) != 0 {
		t.Errorf("no forge:version marker means no concurrency control and nothing to check; got %+v", got)
	}
	if err := UnversionableEntitiesError(nil); err != nil {
		t.Errorf("empty finding set must render a nil error; got %v", err)
	}
}
