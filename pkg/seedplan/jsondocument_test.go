package seedplan

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// TestSeedJSONDocument_NeverInventsContent pins what the seeder puts in a
// json/jsonb column.
//
// The seeder cannot know what a JSON document MEANS. The column type says
// "a JSON value"; nothing in the schema says which keys, which shape, or
// which of them reference other rows. It used to guess from the column
// NAME — a column whose default was '[]' got two strings drawn from a pool
// of SaaS plan tiers, so a clinic's orders shipped
// `line_items = ["enterprise","free"]`. Valid JSON, passed
// CHECK (jsonb_typeof = 'array'), and the app's flagship RPC rejected all
// twenty seeded rows because they were not order line items.
//
// The rule that replaced the guess: the seeder emits what the SCHEMA
// declares — the column DEFAULT, or the empty document its shape CHECK
// admits — and nothing else. Content is the app author's, through
// db/seeds/vocab.yaml.
func TestSeedJSONDocument_NeverInventsContent(t *testing.T) {
	table := schemadef.Table{
		Name: "orders",
		Columns: []schemadef.Column{
			{Name: "id", Type: schemadef.TypeString, IsPK: true, NotNull: true, DeclType: "text"},
			// The peptides-e8 shape verbatim.
			{Name: "line_items", Type: schemadef.TypeJSON, NotNull: true, DeclType: "jsonb", Default: "'[]'::jsonb"},
			// A NOT NULL array-shaped column with NO default: the CHECK is
			// the only thing that says which empty document is legal.
			{Name: "events", Type: schemadef.TypeJSON, NotNull: true, DeclType: "jsonb"},
			// An object-shaped column with no default.
			{Name: "metadata", Type: schemadef.TypeJSON, DeclType: "jsonb"},
			// A default that is a REAL document: the schema said what a row
			// starts as, so the seeder says exactly that.
			{Name: "settings", Type: schemadef.TypeJSON, NotNull: true, DeclType: "jsonb",
				Default: `'{"theme": "dark"}'::jsonb`},
			// The column names that used to drive the invention.
			{Name: "tags", Type: schemadef.TypeJSON, NotNull: true, DeclType: "jsonb"},
			{Name: "schema_json", Type: schemadef.TypeJSON, NotNull: true, DeclType: "jsonb"},
		},
		Checks: []schemadef.CheckConstraint{
			{Name: "orders_line_items_check", Columns: []string{"line_items"},
				Def: "CHECK ((jsonb_typeof(line_items) = 'array'::text))"},
			{Name: "orders_events_check", Columns: []string{"events"},
				Def: "CHECK ((jsonb_typeof(events) = 'array'::text))"},
			{Name: "orders_settings_check", Columns: []string{"settings"},
				Def: "CHECK ((jsonb_typeof(settings) = 'object'::text))"},
		},
	}

	plan, err := BuildPlan([]schemadef.Table{table}, EnumPools{}, Config{Rows: 6, Salt: 0})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	// Derive the set from the SCHEMA, not from a field list: every json
	// column in the table. An empty derivation is a vacuous test.
	var jsonCols []schemadef.Column
	for _, c := range table.Columns {
		if c.Type == schemadef.TypeJSON {
			jsonCols = append(jsonCols, c)
		}
	}
	if len(jsonCols) == 0 {
		t.Fatal("derived set is empty: the fixture declares no json column, so this test asserts nothing")
	}

	want := map[string]string{
		"line_items":  "[]",
		"events":      "[]",
		"metadata":    "{}",
		"settings":    `{"theme": "dark"}`,
		"tags":        "{}",
		"schema_json": "{}",
	}

	for _, col := range jsonCols {
		seen := map[string]bool{}
		for i := 0; i < 6; i++ {
			lit := plan.valueLiteral("orders", col, i)
			if !strings.HasPrefix(lit, "'") || !strings.HasSuffix(lit, "'") {
				t.Fatalf("%s row %d: not a quoted SQL literal: %s", col.Name, i, lit)
			}
			doc := strings.ReplaceAll(lit[1:len(lit)-1], "''", "'")
			if !json.Valid([]byte(doc)) {
				t.Errorf("%s row %d: not valid JSON: %s", col.Name, i, doc)
			}
			seen[doc] = true
		}
		if len(seen) != 1 {
			t.Errorf("%s: the seeder invented per-row content (%d distinct documents: %v)", col.Name, len(seen), seen)
		}
		for got := range seen {
			if got != want[col.Name] {
				t.Errorf("%s: seeded %s, want %s", col.Name, got, want[col.Name])
			}
		}
	}
}

// TestSeedJSONDocument_VocabIsTheEscapeHatch pins the other half: content
// for a json column is the app author's to supply, and vocab.yaml is where
// it goes. Without this the "never invent" rule would just mean "always
// empty", with no way for anyone who DOES know the shape to fix it.
func TestSeedJSONDocument_VocabIsTheEscapeHatch(t *testing.T) {
	table := schemadef.Table{
		Name: "orders",
		Columns: []schemadef.Column{
			{Name: "id", Type: schemadef.TypeString, IsPK: true, NotNull: true, DeclType: "text"},
			{Name: "line_items", Type: schemadef.TypeJSON, NotNull: true, DeclType: "jsonb", Default: "'[]'::jsonb"},
		},
		Checks: []schemadef.CheckConstraint{
			{Name: "orders_line_items_check", Columns: []string{"line_items"},
				Def: "CHECK ((jsonb_typeof(line_items) = 'array'::text))"},
		},
	}
	plan, err := BuildPlan([]schemadef.Table{table}, EnumPools{}, Config{Rows: 3, Salt: 0})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	authored := `[{"product_id": "p-1", "quantity": "2"}]`
	warns := plan.ApplyVocab(&Vocab{Columns: map[string][]string{"orders.line_items": {authored}}})
	if len(warns) != 0 {
		t.Fatalf("a valid JSON document must be accepted as vocabulary; warnings: %v", warns)
	}
	got := plan.valueLiteral("orders", table.Columns[1], 0)
	if got != sqlString(authored) {
		t.Errorf("vocab document not seeded verbatim: got %s, want %s", got, sqlString(authored))
	}

	// An invalid document is refused with a warning, not silently seeded.
	warns = plan.ApplyVocab(&Vocab{Columns: map[string][]string{"orders.line_items": {"not json"}}})
	if len(warns) == 0 {
		t.Error("an invalid JSON vocabulary value must warn")
	}
}
