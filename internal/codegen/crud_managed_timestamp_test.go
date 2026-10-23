package codegen

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// The managed-timestamp column shape is decided in ONE place —
// schemadef.DetectConventions — and read by four:
//
//   - internal/generator projects the column to a struct field and sets
//     Timestamps: true on the entity's repo Spec;
//   - pkg/crud's writeStamp stamps it (RFC3339Nano for a string column,
//     the instant for a time column);
//   - internal/scaffold births TIMESTAMPTZ for a fresh entity;
//   - this package pairs it with the proto field on the wire.
//
// A reader that disagrees with DetectConventions used to ship the
// disagreement as a comment in generated code — `// CreatedAt: unmapped
// (wire kind timestamp, column TEXT)` — and the field was dead over the
// API while everything reported green. Once an unmappable pairing became a
// hard `forge generate` failure, the same disagreement stopped a legacy
// schema from generating at all: forge's own fixture corpus (a pre-forge
// table with TEXT created_at/updated_at, kalshi fr-3fba9166ba) aborted with
//
//	2 proto field(s) have a column but no conversion, so they would be
//	silently dropped:
//	  - Trade.created_at (timestamp) <-> trades.created_at TEXT
//	  - Trade.updated_at (timestamp) <-> trades.updated_at TEXT
//
// while `go test ./...` stayed green, because the only test that ran the
// gate carried a build tag.
//
// The set under test is DERIVED from DetectConventions rather than named
// here: whatever column types it admits as managed timestamps, the
// conversion generator must map. Adding a type to the convention without a
// pairing now fails HERE, in an untagged test, instead of in a tagged one.

// canonicalColumnTypes is every canonical type schemadef can classify a
// column as. Deriving the candidate set from the vocabulary — rather than
// from the two types that happen to work today — is what makes a new
// vocabulary entry visible instead of silently untested.
var canonicalColumnTypes = []struct {
	canonical schemadef.CanonicalType
	declType  string
}{
	{schemadef.TypeString, "TEXT"},
	{schemadef.TypeInt, "BIGINT"},
	{schemadef.TypeFloat, "DOUBLE PRECISION"},
	{schemadef.TypeBool, "BOOLEAN"},
	{schemadef.TypeTime, "TIMESTAMPTZ"},
	{schemadef.TypeJSON, "JSONB"},
	{schemadef.TypeBytes, "BYTEA"},
}

// TestManagedTimestampColumns_AllHaveAConversion asserts that every column
// type schemadef.DetectConventions counts as a stampable managed timestamp
// also has a proto<->entity conversion. The two halves must agree; a column
// the runtime stamps but the wire cannot carry is a dead field.
func TestManagedTimestampColumns_AllHaveAConversion(t *testing.T) {
	var admitted []string
	for _, ct := range canonicalColumnTypes {
		table := schemadef.Table{
			Name: "trades",
			Columns: []schemadef.Column{
				{Name: "id", Type: schemadef.TypeString, DeclType: "TEXT", NotNull: true, IsPK: true},
				{Name: schemadef.ColCreatedAt, Type: ct.canonical, DeclType: ct.declType, NotNull: true},
				{Name: schemadef.ColUpdatedAt, Type: ct.canonical, DeclType: ct.declType, NotNull: true},
			},
		}
		if !schemadef.DetectConventions(table).Timestamps {
			continue // not a managed-timestamp shape; nothing to pair
		}
		admitted = append(admitted, ct.declType)

		entity := EntityDef{
			Name: "Trade", TableName: "trades",
			Fields: []EntityField{
				{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
				{Name: "created_at", GoName: "CreatedAt", ProtoType: "message",
					MessageType: "google.protobuf.Timestamp", GoType: "*timestamppb.Timestamp", Kind: FieldKindTimestamp},
				{Name: "updated_at", GoName: "UpdatedAt", ProtoType: "message",
					MessageType: "google.protobuf.Timestamp", GoType: "*timestamppb.Timestamp", Kind: FieldKindTimestamp},
			},
			Columns: columnsFromTable(table),
		}
		if _, unmapped := BuildEntityConv(ServiceDef{Package: "trades.v1"}, entity); len(unmapped) != 0 {
			for _, u := range unmapped {
				t.Errorf("%s created_at/updated_at is a MANAGED timestamp per schemadef.DetectConventions "+
					"(pkg/crud stamps it at runtime) but the conversion generator refuses it: %s.%s <-> %s.%s %s: %s",
					ct.declType, u.Message, u.Field, u.Table, u.Column, u.DeclType, u.Reason)
			}
		}
	}
	if len(admitted) == 0 {
		t.Fatal("derived set is empty: DetectConventions admitted no column type as a managed timestamp, " +
			"so this test asserts nothing")
	}
	// Both halves of the convention must be represented, or the test would
	// pass while covering only the shape birth emits.
	if len(admitted) < 2 {
		t.Errorf("only %v admitted as managed timestamps — schemadef documents a time column AND a legacy "+
			"TEXT column (kalshi fr-3fba9166ba); one of them stopped being detected", admitted)
	}
}

// columnsFromTable projects an introspected table onto the EntityColumn
// slice BuildSchemaEntities would build from it, so the fixture cannot
// drift from the real join.
func columnsFromTable(tbl schemadef.Table) []EntityColumn {
	var cols []EntityColumn
	for _, c := range tbl.Columns {
		cols = append(cols, EntityColumn{
			Name: c.Name, Type: string(c.Type), IsArray: c.IsArray,
			NotNull: c.NotNull, IsPK: c.IsPK, DeclType: c.DeclType, Default: c.Default,
		})
	}
	return cols
}

// TestLegacyTextTimestamp_ConversionShape pins the emitted code for a
// google.protobuf.Timestamp over a TEXT column, in both directions and for
// both nullabilities.
//
// The format is not a choice this generator makes: pkg/crud's writeStamp
// writes time.RFC3339Nano into a string timestamp column, so the pairing
// must read and write the SAME layout or the stamps the runtime produces
// come back unparseable.
func TestLegacyTextTimestamp_ConversionShape(t *testing.T) {
	entity := EntityDef{
		Name: "Trade", TableName: "trades",
		Fields: []EntityField{
			{Name: "created_at", GoName: "CreatedAt", ProtoType: "message",
				MessageType: "google.protobuf.Timestamp", GoType: "*timestamppb.Timestamp", Kind: FieldKindTimestamp},
			{Name: "settled_at", GoName: "SettledAt", ProtoType: "message",
				MessageType: "google.protobuf.Timestamp", GoType: "*timestamppb.Timestamp", Kind: FieldKindTimestamp},
		},
		Columns: []EntityColumn{
			{Name: "created_at", Type: "string", DeclType: "TEXT", NotNull: true},
			{Name: "settled_at", Type: "string", DeclType: "TEXT"}, // nullable -> *string
		},
	}
	conv, unmapped := BuildEntityConv(ServiceDef{Package: "trades.v1"}, entity)
	if len(unmapped) != 0 {
		t.Fatalf("legacy TEXT timestamp reported unmappable: %+v", unmapped)
	}
	toProto := strings.Join(conv.ToProtoAssigns, "\n")
	fromProto := strings.Join(conv.FromProtoAssigns, "\n")

	for _, want := range []string{
		// NOT NULL write: format the instant as text.
		"e.CreatedAt = m.CreatedAt.AsTime().Format(time.RFC3339Nano)",
		// nullable write: through an addressable local.
		"v := m.SettledAt.AsTime().Format(time.RFC3339Nano)",
		"e.SettledAt = &v",
	} {
		if !strings.Contains(fromProto, want) {
			t.Errorf("write path missing %q; got:\n%s", want, fromProto)
		}
	}
	for _, want := range []string{
		// NOT NULL read: empty text is an absent value, never an error.
		`if e.CreatedAt != "" {`,
		"time.Parse(time.RFC3339Nano, e.CreatedAt)",
		"m.CreatedAt = timestamppb.New(t)",
		// nullable read: nil AND empty are both absent.
		`if e.SettledAt != nil && *e.SettledAt != "" {`,
		"time.Parse(time.RFC3339Nano, *e.SettledAt)",
	} {
		if !strings.Contains(toProto, want) {
			t.Errorf("read path missing %q; got:\n%s", want, toProto)
		}
	}
	// Non-empty text that does not parse is CORRUPTION and must be loud —
	// the silent alternative reads every such row back as unset.
	//
	// Asserted as the WHOLE emitted line, not as a substring of the prose:
	// the first cut of this builder nested one Sprintf inside another and
	// shipped
	//
	//	fmt.Errorf("unparseable timestamp "e.CreatedAt" for column
	//	created_at: %!w(MISSING)", %!s(MISSING), err)
	//
	// which contains every phrase a substring check would look for and does
	// not compile. A format string is only correct as a whole.
	for _, want := range []string{
		`return nil, fmt.Errorf("unparseable timestamp %q for column created_at: %w", e.CreatedAt, err)`,
		`return nil, fmt.Errorf("unparseable timestamp %q for column settled_at: %w", *e.SettledAt, err)`,
	} {
		if !strings.Contains(toProto, want) {
			t.Errorf("read path missing the corrupt-value error\n  want line: %s\n  got:\n%s", want, toProto)
		}
	}

	// The imports the emitted code needs must be gated on, or the file does
	// not compile.
	convs := []EntityConvTemplateData{conv}
	if !ConvNeedsTime(convs) {
		t.Error("ConvNeedsTime is false for a conversion that calls time.Parse/Format — the `time` import would be missing")
	}
	if !ConvNeedsFmt(convs) {
		t.Error("ConvNeedsFmt is false for a conversion that calls fmt.Errorf — the `fmt` import would be missing")
	}
	if !ConvNeedsTimestamppb(convs) {
		t.Error("ConvNeedsTimestamppb is false for a conversion that calls timestamppb.New")
	}
}

// TestConvNeedsTime_FalseWithoutALegacyTextTimestamp is the other half of
// the import gate: an entity with a normal TIMESTAMPTZ column must NOT pull
// in `time`, or every generated ops file carries an unused import.
func TestConvNeedsTime_FalseWithoutALegacyTextTimestamp(t *testing.T) {
	entity := EntityDef{
		Name: "Trade", TableName: "trades",
		Fields: []EntityField{
			{Name: "created_at", GoName: "CreatedAt", ProtoType: "message",
				MessageType: "google.protobuf.Timestamp", GoType: "*timestamppb.Timestamp", Kind: FieldKindTimestamp},
		},
		Columns: []EntityColumn{
			{Name: "created_at", Type: "time", DeclType: "TIMESTAMPTZ", NotNull: true},
		},
	}
	conv, unmapped := BuildEntityConv(ServiceDef{Package: "trades.v1"}, entity)
	if len(unmapped) != 0 {
		t.Fatalf("TIMESTAMPTZ timestamp reported unmappable: %+v", unmapped)
	}
	if ConvNeedsTime([]EntityConvTemplateData{conv}) {
		t.Errorf("ConvNeedsTime is true for a TIMESTAMPTZ-only entity — the ops file would carry an unused `time` import; got:\n%s\n%s",
			strings.Join(conv.ToProtoAssigns, "\n"), strings.Join(conv.FromProtoAssigns, "\n"))
	}
}
