package codegen

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// unsatisfiableEntity builds a minimal entity: one read-only wire field
// paired with one column, so a test can vary only the column's NOT
// NULL / DEFAULT / FillStrategy facts.
func unsatisfiableEntity(col EntityColumn) EntityDef {
	return EntityDef{
		Name:      "Customer",
		TableName: "customers",
		Fields: []EntityField{
			{Name: col.Name, GoName: "CompanyId", ProtoType: "string", GoType: "string", ReadOnly: true},
		},
		Columns: []EntityColumn{col},
	}
}

func TestFindUnsatisfiableColumns_FiresOnTheUnsatisfiableShape(t *testing.T) {
	entity := unsatisfiableEntity(EntityColumn{
		Name: "company_id", Type: "string", NotNull: true, DeclType: "TEXT",
	})
	got := FindUnsatisfiableColumns(entity)
	if len(got) != 1 {
		t.Fatalf("NOT NULL + no DEFAULT + forge:read-only + no forge:fill must be flagged; got %+v", got)
	}
	if got[0].Column != "company_id" || got[0].Entity != "Customer" || got[0].Table != "customers" {
		t.Errorf("finding = %+v, want company_id/Customer/customers", got[0])
	}
	if err := UnsatisfiableColumnsError(got); err == nil {
		t.Error("UnsatisfiableColumnsError must render a non-nil error for a non-empty finding set")
	}
}

func TestFindUnsatisfiableColumns_DoesNotFireWithADefault(t *testing.T) {
	entity := unsatisfiableEntity(EntityColumn{
		Name: "status", Type: "string", NotNull: true, DeclType: "TEXT", Default: "'ACTIVE'::text",
	})
	if got := FindUnsatisfiableColumns(entity); len(got) != 0 {
		t.Errorf("a column with a DB DEFAULT is satisfiable; got %+v", got)
	}
}

func TestFindUnsatisfiableColumns_DoesNotFireWhenNullable(t *testing.T) {
	entity := unsatisfiableEntity(EntityColumn{
		Name: "archived_reason", Type: "string", DeclType: "TEXT",
	})
	if got := FindUnsatisfiableColumns(entity); len(got) != 0 {
		t.Errorf("a nullable column is satisfiable (NULL is a legal insert); got %+v", got)
	}
}

// The suppression contrast: forge:fill= must silence the finding, proving
// the check reads FillStrategy rather than being broken/inert.
func TestFindUnsatisfiableColumns_SuppressedByForgeFill(t *testing.T) {
	for _, strategy := range []string{schemadef.FillStrategyULID, schemadef.FillStrategyHandler} {
		entity := unsatisfiableEntity(EntityColumn{
			Name: "company_id", Type: "string", NotNull: true, DeclType: "TEXT",
			FillStrategy: strategy,
		})
		if got := FindUnsatisfiableColumns(entity); len(got) != 0 {
			t.Errorf("forge:fill=%s must suppress the finding; got %+v", strategy, got)
		}
	}
}

func TestFindUnsatisfiableColumns_IgnoresColumnsThatArentReadOnly(t *testing.T) {
	entity := EntityDef{
		Name:      "Customer",
		TableName: "customers",
		Fields: []EntityField{
			{Name: "name", GoName: "Name", ProtoType: "string", GoType: "string"},
		},
		Columns: []EntityColumn{{Name: "name", Type: "string", NotNull: true, DeclType: "TEXT"}},
	}
	if got := FindUnsatisfiableColumns(entity); len(got) != 0 {
		t.Errorf("a client-settable (non-read-only) column is filled by the request itself; got %+v", got)
	}
}

func TestFindUnsatisfiableColumns_IgnoresPKAndGeneratedColumns(t *testing.T) {
	pk := unsatisfiableEntity(EntityColumn{Name: "company_id", Type: "string", NotNull: true, IsPK: true})
	if got := FindUnsatisfiableColumns(pk); len(got) != 0 {
		t.Errorf("a PK is filled by ULID generation, not the create request; got %+v", got)
	}
	generated := unsatisfiableEntity(EntityColumn{Name: "company_id", Type: "string", NotNull: true, IsGenerated: true})
	if got := FindUnsatisfiableColumns(generated); len(got) != 0 {
		t.Errorf("a GENERATED ALWAYS column is computed by postgres, not inserted; got %+v", got)
	}
}

func TestUnsatisfiableColumnsError_NilForEmptySet(t *testing.T) {
	if err := UnsatisfiableColumnsError(nil); err != nil {
		t.Errorf("empty set must render a nil error; got %v", err)
	}
}
