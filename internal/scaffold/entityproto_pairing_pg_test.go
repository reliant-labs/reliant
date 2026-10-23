package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// Entity birth and the CRUD conversion generator share ONE contract, stated
// in this file's own header: birth creates a column only for what the
// generator will map onto it. Nothing held the two ends together, and they
// drifted — birth emitted BIGINT for eight integer kinds, BYTEA for `bytes`,
// and native arrays for every `repeated <scalar>`, while the conversion
// generator projected nine of the fifteen proto scalars to Go `string` and
// every non-BIGINT[] array column to []string. The result was an app with a
// `bytes` or `uint32` field that could not run `forge generate` at all, and
// a `repeated bytes` field that passed the gate and emitted uncompilable Go.
//
// This test is the join. It renders the birth migration from the EMITTER,
// applies it to a REAL postgres, introspects it back through the production
// path, and asserts every column birth created has a conversion in both
// directions. A hand-written mirror of the emitter's mapping table could
// not catch drift in the emitter; only running it can.

// everyProtoScalarKind is the closed set protobuf defines. Enumerating it
// makes the sweep exhaustive rather than a sample.
var everyProtoScalarKind = []string{
	"string", "bool", "bytes", "float", "double",
	"int32", "int64", "uint32", "uint64",
	"sint32", "sint64", "fixed32", "fixed64", "sfixed32", "sfixed64",
}

// TestBirthColumns_HaveAConversion sweeps every proto scalar kind, singular
// and repeated, plus the well-known Timestamp, through birth → postgres →
// the conversion generator.
func TestBirthColumns_HaveAConversion(t *testing.T) {
	if testing.Short() {
		t.Skip("applies a birth migration to a real postgres; skipped under -short")
	}

	var fields []codegen.SchemaFieldDef
	for _, kind := range everyProtoScalarKind {
		fields = append(fields,
			codegen.SchemaFieldDef{Name: "s_" + kind, Kind: kind},
			codegen.SchemaFieldDef{Name: "r_" + kind, Kind: kind, Repeated: true},
			// An `optional` scalar is a nullable column and a POINTER wire
			// field, a third shape with its own conversion path.
			codegen.SchemaFieldDef{Name: "o_" + kind, Kind: kind, Optional: true},
		)
	}
	fields = append(fields,
		codegen.SchemaFieldDef{Name: "s_stamp", Kind: "message", TypeName: "google.protobuf.Timestamp"},
		codegen.SchemaFieldDef{Name: "r_stamp", Kind: "message", TypeName: "google.protobuf.Timestamp", Repeated: true},
	)

	mig := RenderEntityMigrationFromProto(EntityFromProtoSpec{
		Table:     "shapes",
		MessageFQ: "shapes.v1.Shape",
		ProtoPkg:  "shapes.v1",
		Fields:    fields,
	})
	t.Logf("birth migration under test:\n%s", mig.UpSQL)
	for _, n := range mig.Notes {
		t.Logf("birth note: %s", n)
	}

	migDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(migDir, "001_shapes.up.sql"), []byte(mig.UpSQL), 0o644); err != nil {
		t.Fatalf("write migration: %v", err)
	}
	tables, err := schemadef.ApplyAndIntrospect(migDir)
	if err != nil {
		t.Skipf("no reachable postgres (%v) — this test is the only thing holding entity birth "+
			"and the conversion generator together; do not leave it skipped in CI", err)
	}
	if len(tables) != 1 {
		t.Fatalf("introspected %d tables, want 1 — the birth migration did not apply as written", len(tables))
	}
	table := tables[0]

	// Rebuild the entity exactly as BuildSchemaEntities does: columns from
	// the APPLIED schema, wire fields from the descriptor.
	entity := codegen.EntityDef{Name: "Shape", TableName: table.Name}
	for _, c := range table.Columns {
		entity.Columns = append(entity.Columns, codegen.EntityColumn{
			Name: c.Name, Type: string(c.Type), IsArray: c.IsArray,
			NotNull: c.NotNull, IsPK: c.IsPK, DeclType: c.DeclType, Default: c.Default,
		})
	}
	svc := codegen.ServiceDef{
		Package: "shapes.v1",
		Schemas: map[string][]codegen.SchemaFieldDef{"shapes.v1.Shape": fields},
	}
	entity.Fields = codegen.WireEntityFields(svc, "Shape")

	// The set under test is DERIVED from what birth actually created — a
	// column named by a wire field. An empty set means the sweep asserts
	// nothing, which is the defect it exists to catch.
	wireByName := map[string]bool{}
	for _, f := range entity.Fields {
		wireByName[f.Name] = true
	}
	paired := 0
	for _, c := range entity.Columns {
		if wireByName[c.Name] {
			paired++
		}
	}
	if paired == 0 {
		t.Fatal("no born column is named by a wire field — the sweep asserts nothing")
	}
	t.Logf("%d born columns pair with a wire field", paired)

	_, unmapped := codegen.BuildEntityConv(svc, entity)
	if len(unmapped) > 0 {
		var lines []string
		for _, u := range unmapped {
			lines = append(lines, fmt.Sprintf("  %s (%s) <-> %s %s: %s", u.Field, u.Kind, u.Column, u.DeclType, u.Reason))
		}
		sort.Strings(lines)
		t.Errorf("entity birth created %d column(s) the conversion generator refuses. Birth must not "+
			"emit a column the generator will not map — the field is dead over the API, and "+
			"`forge generate` now fails outright:\n%s", len(unmapped), strings.Join(lines, "\n"))
	}
}

// TestBirth_RepeatedTimestampIsRefused pins a DELIBERATE refusal.
//
// `repeated google.protobuf.Timestamp` was the one well-known type birth
// gave a column (TIMESTAMPTZ[]) while every other google.protobuf.* — and
// every other repeated well-known — was carried as a TODO. The conversion
// generator refused that column, so birth was creating a dead field.
//
// It stays refused rather than becoming supported, because the two sides
// cannot represent each other: the wire shape is []*timestamppb.Timestamp,
// whose elements may be nil, and a NULL element in a TIMESTAMPTZ[] column
// makes the whole ROW unscannable ("bun: can't parse time=”") rather than
// producing a zero instant. An array of instants is also a denormalized
// event list whose every query needs unnest; the shape forge generates well
// for that is a child table.
func TestBirth_RepeatedTimestampIsRefused(t *testing.T) {
	mig := RenderEntityMigrationFromProto(EntityFromProtoSpec{
		Table:     "events",
		MessageFQ: "events.v1.Event",
		ProtoPkg:  "events.v1",
		Fields: []codegen.SchemaFieldDef{
			{Name: "at", Kind: "message", TypeName: "google.protobuf.Timestamp"},
			{Name: "marks", Kind: "message", TypeName: "google.protobuf.Timestamp", Repeated: true},
		},
	})
	if strings.Contains(mig.UpSQL, "TIMESTAMPTZ[]") {
		t.Errorf("birth still emits a TIMESTAMPTZ[] column the conversion generator refuses:\n%s", mig.UpSQL)
	}
	if !strings.Contains(mig.UpSQL, "at TIMESTAMPTZ") {
		t.Errorf("a SINGULAR Timestamp must still get its column:\n%s", mig.UpSQL)
	}
	var got string
	for _, n := range mig.Notes {
		if strings.HasPrefix(n, "field marks:") {
			got = n
		}
	}
	if got == "" {
		t.Fatalf("the skipped field must be reported to the author; notes were %v", mig.Notes)
	}
	// The note has to say what to do instead, not just that it was skipped.
	if !strings.Contains(got, "table") {
		t.Errorf("the skip note must point at the shape that works (a child table); got %q", got)
	}
}
