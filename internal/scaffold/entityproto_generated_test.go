// File: internal/scaffold/entityproto_generated_test.go
//
// `// forge:generated <expr>` at birth: the column the marker produces, the
// NOT NULL rule it follows, the fields it refuses, and — against the same
// real postgres `forge generate` introspects — that the born column is one
// postgres computes and refuses to let anyone write.

package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// lineItemSpec is the roofers estimate line item that motivated the marker:
// its line total was born `BIGINT NOT NULL DEFAULT 0` and hand-edited into a
// generated column right after birth.
func lineItemSpec() EntityFromProtoSpec {
	return EntityFromProtoSpec{
		Table:     "line_items",
		MessageFQ: entityProtoPkg + ".LineItem",
		ProtoPkg:  entityProtoPkg,
		Fields: []codegen.SchemaFieldDef{
			{Name: "quantity", Kind: "double"},
			{Name: "unit_price_cents", Kind: "int64"},
			{Name: "line_total_cents", Kind: "int64", ReadOnly: true,
				Generated: "round(quantity * unit_price_cents)::BIGINT"},
			{Name: "discount_cents", Kind: "int64", Optional: true, ReadOnly: true,
				Generated: "CASE WHEN quantity > 10 THEN unit_price_cents END"},
			{Name: "band", Kind: "enum", TypeName: entityProtoPkg + ".Band", ReadOnly: true,
				Generated: "CASE WHEN quantity > 10 THEN 'BAND_BULK' ELSE 'BAND_SINGLE' END"},
		},
		Enums: map[string][]string{
			entityProtoPkg + ".Band": {"BAND_UNSPECIFIED", "BAND_SINGLE", "BAND_BULK"},
		},
		Timestamps: true,
	}
}

func TestRenderGeneratedColumns(t *testing.T) {
	up := RenderEntityMigrationFromProto(lineItemSpec()).UpSQL
	for _, want := range []string{
		// Plain field: NOT NULL, no DEFAULT, the expression verbatim.
		"line_total_cents BIGINT NOT NULL GENERATED ALWAYS AS (round(quantity * unit_price_cents)::BIGINT) STORED,",
		// optional field: nullable — presence is the column's nullability.
		"discount_cents BIGINT GENERATED ALWAYS AS (CASE WHEN quantity > 10 THEN unit_price_cents END) STORED,",
		// enum: TEXT, keeps its vocabulary CHECK (sentinel excluded), no DEFAULT.
		"band TEXT NOT NULL GENERATED ALWAYS AS (CASE WHEN quantity > 10 THEN 'BAND_BULK' ELSE 'BAND_SINGLE' END) STORED CHECK (band IN ('BAND_SINGLE', 'BAND_BULK')),",
		"-- line_total_cents: forge:generated",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("born migration missing %q:\n%s", want, up)
		}
	}
	// A generated column can carry no DEFAULT; postgres refuses the pair.
	for _, line := range strings.Split(up, "\n") {
		if strings.Contains(line, "GENERATED ALWAYS") && strings.Contains(line, "DEFAULT") {
			t.Errorf("a generated column was born with a DEFAULT: %q", line)
		}
	}
}

func TestRenderGeneratedColumn_TimestampAndCheckAndReference(t *testing.T) {
	spec := EntityFromProtoSpec{
		Table:     "visits",
		MessageFQ: entityProtoPkg + ".Visit",
		ProtoPkg:  entityProtoPkg,
		Fields: []codegen.SchemaFieldDef{
			{Name: "starts_at", Kind: "message", TypeName: "google.protobuf.Timestamp"},
			{Name: "day_start", Kind: "message", TypeName: "google.protobuf.Timestamp", Generated: "date_trunc('day', starts_at, 'UTC')"},
			{Name: "minutes", Kind: "int64"},
			{Name: "hours", Kind: "int64", Generated: "minutes / 60",
				Validate: &codegen.FieldConstraints{Gte: "0"}},
			{Name: "primary_order_id", Kind: "string", Generated: "'o-' || minutes"},
		},
		KnownTables:    map[string]bool{"orders": true},
		ExistingTables: []ExistingTable{{Name: "orders"}},
	}
	up := RenderEntityMigrationFromProto(spec).UpSQL
	for _, want := range []string{
		// A Timestamp carries presence: nullable.
		"day_start TIMESTAMPTZ GENERATED ALWAYS AS (date_trunc('day', starts_at, 'UTC')) STORED,",
		// protovalidate still projects to a CHECK on the generated value.
		"hours BIGINT NOT NULL GENERATED ALWAYS AS (minutes / 60) STORED CHECK (hours >= 0)",
		// A generated reference is still a reference.
		"ALTER TABLE visits ADD CONSTRAINT visits_primary_order_id_fkey FOREIGN KEY (primary_order_id) REFERENCES orders (id);",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("born migration missing %q:\n%s", want, up)
		}
	}
}

// Fields birth gives no single-valued column are refused, never quietly
// born as a plain column the marker's author believes is computed.
func TestGeneratedColumnRefusal(t *testing.T) {
	spec := EntityFromProtoSpec{
		ProtoPkg: entityProtoPkg,
		Enums:    map[string][]string{entityProtoPkg + ".Band": {"BAND_A"}},
	}
	cases := []struct {
		field  codegen.SchemaFieldDef
		refuse bool
	}{
		{codegen.SchemaFieldDef{Name: "total", Kind: "int64"}, false},
		{codegen.SchemaFieldDef{Name: "label", Kind: "string", Optional: true}, false},
		{codegen.SchemaFieldDef{Name: "band", Kind: "enum", TypeName: entityProtoPkg + ".Band"}, false},
		{codegen.SchemaFieldDef{Name: "at", Kind: "message", TypeName: "google.protobuf.Timestamp"}, false},
		{codegen.SchemaFieldDef{Name: "tags", Kind: "string", Repeated: true}, true},
		{codegen.SchemaFieldDef{Name: "attrs", Kind: "map", MapKeyKind: "string", MapValueKind: "int64"}, true},
		{codegen.SchemaFieldDef{Name: "addr", Kind: "message", TypeName: entityProtoPkg + ".Address"}, true},
		{codegen.SchemaFieldDef{Name: "pick", Kind: "string", Oneof: "choice"}, true},
		{codegen.SchemaFieldDef{Name: "other", Kind: "enum", TypeName: "other.v1.Band"}, true},
		{codegen.SchemaFieldDef{Name: "created_at", Kind: "message", TypeName: "google.protobuf.Timestamp"}, true},
	}
	for _, c := range cases {
		reason := GeneratedColumnRefusal(c.field, spec)
		if (reason != "") != c.refuse {
			t.Errorf("GeneratedColumnRefusal(%s) = %q, want refused=%v", c.field.Name, reason, c.refuse)
		}
	}

	// The renderer applies the same rule: a refused field becomes a TODO,
	// never the plain column the author did not ask for.
	spec.Table, spec.MessageFQ = "things", entityProtoPkg+".Thing"
	spec.Fields = []codegen.SchemaFieldDef{{Name: "tags", Kind: "string", Repeated: true, Generated: "ARRAY['a']"}}
	mig := RenderEntityMigrationFromProto(spec)
	if strings.Contains(mig.UpSQL, "tags TEXT[]") || !strings.Contains(mig.UpSQL, "-- TODO: proto field \"tags\"") {
		t.Errorf("a refused generated field must be carried as a TODO:\n%s", mig.UpSQL)
	}
}

// The contract, end to end on real postgres: the born migration applies, the
// generated columns are introspected as GENERATED (which is what makes the
// ORM leave them out of every write), postgres computes them on INSERT and
// recomputes on UPDATE, and refuses a direct write.
func TestGeneratedColumns_PostgresComputesAndRefusesWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a real postgres; skipped under -short")
	}
	migDir := t.TempDir()
	up := RenderEntityMigrationFromProto(lineItemSpec()).UpSQL
	if err := os.WriteFile(filepath.Join(migDir, "00001_create_line_items.up.sql"), []byte(up), 0o644); err != nil {
		t.Fatal(err)
	}
	// The same apply-then-introspect pass `forge generate` runs.
	tables, shadow, err := schemadef.ApplyAndIntrospectShadowAt(migDir, "")
	defer shadow.Close()
	if err != nil {
		t.Fatalf("born migration does not apply:\n%s\nerr: %v", up, err)
	}
	db := shadow.DB()

	if _, err := db.Exec(`INSERT INTO line_items (id, quantity, unit_price_cents) VALUES ('li1', 2.5, 1000)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var total int64
	var band string
	if err := db.QueryRow(`SELECT line_total_cents, band FROM line_items WHERE id = 'li1'`).Scan(&total, &band); err != nil {
		t.Fatal(err)
	}
	if total != 2500 || band != "BAND_SINGLE" {
		t.Errorf("generated values = (%d, %q), want (2500, BAND_SINGLE)", total, band)
	}
	if _, err := db.Exec(`UPDATE line_items SET quantity = 12 WHERE id = 'li1'`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT line_total_cents, band FROM line_items WHERE id = 'li1'`).Scan(&total, &band); err != nil {
		t.Fatal(err)
	}
	if total != 12000 || band != "BAND_BULK" {
		t.Errorf("recomputed values = (%d, %q), want (12000, BAND_BULK)", total, band)
	}
	if _, err := db.Exec(`UPDATE line_items SET line_total_cents = 1 WHERE id = 'li1'`); err == nil {
		t.Error("postgres accepted a direct write to a generated column")
	}

	generated := map[string]bool{}
	for _, tbl := range tables {
		if tbl.Name != "line_items" {
			continue
		}
		for _, c := range tbl.Columns {
			generated[c.Name] = c.IsGenerated
		}
	}
	for _, col := range []string{"line_total_cents", "discount_cents", "band"} {
		if !generated[col] {
			t.Errorf("%s is not introspected as GENERATED — the ORM would write it", col)
		}
	}
	if generated["quantity"] {
		t.Error("quantity is a plain column")
	}
}
