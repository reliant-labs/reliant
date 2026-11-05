package scaffold

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/pkg/orm"
	"google.golang.org/protobuf/types/known/structpb"
)

// google.protobuf.Struct is the natural wire type for a JSONB column —
// evidence links, raw API payloads, workflow inputs — and birth carried it
// as a TODO ("well-known type google.protobuf.Struct has no mechanical
// column mapping"), so every such field needed a hand-written column after
// the first scaffold. The CRUD conversion generator already mapped a JSONB
// column under a Struct field (protojson encodes a Struct as the object
// itself), so only birth was missing it. These pin the born columns.
func TestRenderEntityMigrationFromProto_JSONWellKnowns(t *testing.T) {
	mig := RenderEntityMigrationFromProto(EntityFromProtoSpec{
		Table:     "learnings",
		MessageFQ: entityProtoPkg + ".Learning",
		ProtoPkg:  entityProtoPkg,
		Fields: []codegen.SchemaFieldDef{
			{Name: "evidence", Kind: "message", TypeName: "google.protobuf.Struct"},
			{Name: "extra", Kind: "message", TypeName: "google.protobuf.Struct", Optional: true},
			{Name: "steps", Kind: "message", TypeName: "google.protobuf.ListValue"},
			{Name: "maybe_steps", Kind: "message", TypeName: "google.protobuf.ListValue", Optional: true},
			{Name: "answer", Kind: "message", TypeName: "google.protobuf.Value"},
			{Name: "docs", Kind: "message", TypeName: "google.protobuf.Struct", Repeated: true},
		},
	})

	for _, want := range []string{
		"evidence JSONB NOT NULL DEFAULT '{}',",
		"extra JSONB,",
		"steps JSONB NOT NULL DEFAULT '[]',",
		"maybe_steps JSONB,",
		// Never NOT NULL: see TestJSONWellKnowns_AbsentValueHasNoDocument.
		"answer JSONB\n",
		`-- TODO: proto field "docs" skipped — repeated google.protobuf.Struct has no column mapping`,
	} {
		if !strings.Contains(mig.UpSQL, want) {
			t.Errorf("up.sql missing %q:\n%s", want, mig.UpSQL)
		}
	}
	if strings.Contains(mig.UpSQL, "well-known type google.protobuf.Struct has no mechanical column mapping") {
		t.Errorf("a singular Struct must be born as a column, not a TODO:\n%s", mig.UpSQL)
	}
	notes := strings.Join(mig.Notes, "\n")
	if !strings.Contains(notes, "field docs:") || !strings.Contains(notes, "google.protobuf.ListValue") {
		t.Errorf("the repeated-Struct skip must be reported and name the shape that works (ListValue); notes:\n%s", notes)
	}
	for _, n := range mig.Notes {
		for _, name := range []string{"evidence", "extra", "steps", "maybe_steps", "answer"} {
			if strings.HasPrefix(n, "field "+name+":") {
				t.Errorf("field %s was born as a column, but birth still reports it skipped: %q", name, n)
			}
		}
	}
}

// TestJSONWellKnowns_AbsentValueHasNoDocument pins why the born columns'
// nullability differs: the write path marshals the wire field with
// orm.MarshalJSONBMessage, and that is total for an absent Struct or
// ListValue (the empty document, which is also the column DEFAULT) but
// refuses an absent Value. A NOT NULL Value column would therefore fail
// every Create that leaves the field out; nullable, the generated write
// path stores SQL NULL for it instead.
func TestJSONWellKnowns_AbsentValueHasNoDocument(t *testing.T) {
	if got, err := orm.MarshalJSONBMessage((*structpb.Struct)(nil)); err != nil || got != "{}" {
		t.Errorf("absent Struct = %q, %v; want the column DEFAULT '{}'", got, err)
	}
	if got, err := orm.MarshalJSONBMessage((*structpb.ListValue)(nil)); err != nil || got != "[]" {
		t.Errorf("absent ListValue = %q, %v; want the column DEFAULT '[]'", got, err)
	}
	if _, err := orm.MarshalJSONBMessage((*structpb.Value)(nil)); err == nil {
		t.Error("an absent Value now marshals; its column could be NOT NULL — revisit emitJSONWellKnownColumn")
	}
}
