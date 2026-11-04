// File: internal/scaffold/entityproto_test.go
//
// Unit tests for the --from-proto migration renderer: the full mapping
// table (scalars + zero defaults, optional→nullable, enum→CHECK,
// repeated→array, map/nested→JSONB, *_id→FK suggestion), the TODO
// carriers (oneof, Any, cross-package), managed-column skipping, and —
// the whole contract — that the emitted SQL survives the same shadow
// apply `forge generate` runs.

package scaffold

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

const entityProtoPkg = "services.tasks.v1"

// entityProtoFixtureSpec covers every row of the mapping table plus the
// TODO carriers and managed fields.
func entityProtoFixtureSpec() EntityFromProtoSpec {
	return EntityFromProtoSpec{
		Table:     "orders",
		MessageFQ: entityProtoPkg + ".Order",
		ProtoPkg:  entityProtoPkg,
		Fields: []codegen.SchemaFieldDef{
			{Name: "id", Kind: "string"},                                                                 // managed → skipped
			{Name: "sku", Kind: "string"},                                                                // TEXT NOT NULL DEFAULT ''
			{Name: "note", Kind: "string", Optional: true},                                               // nullable TEXT
			{Name: "quantity", Kind: "int64"},                                                            // BIGINT
			{Name: "price", Kind: "double"},                                                              // DOUBLE PRECISION
			{Name: "active", Kind: "bool"},                                                               // BOOLEAN
			{Name: "payload", Kind: "bytes"},                                                             // BYTEA
			{Name: "customer_id", Kind: "string"},                                                        // TEXT NOT NULL + FK suggestion
			{Name: "parent_id", Kind: "string", Optional: true},                                          // nullable TEXT + FK suggestion
			{Name: "status", Kind: "enum", TypeName: entityProtoPkg + ".OrderStatus"},                    // TEXT + CHECK
			{Name: "priority", Kind: "enum", TypeName: entityProtoPkg + ".Priority", Optional: true},     // nullable TEXT + CHECK
			{Name: "tags", Kind: "string", Repeated: true},                                               // TEXT[]
			{Name: "scheduled_at", Kind: "message", TypeName: "google.protobuf.Timestamp"},               // TIMESTAMPTZ
			{Name: "attrs", Kind: "map", MapKeyKind: "string", MapValueKind: "int64"},                    // JSONB
			{Name: "shipping", Kind: "message", TypeName: entityProtoPkg + ".Address"},                   // JSONB
			{Name: "items", Kind: "message", TypeName: entityProtoPkg + ".Item", Repeated: true},         // JSONB '[]'
			{Name: "payment", Kind: "string", Oneof: "method"},                                           // TODO: oneof
			{Name: "blob", Kind: "message", TypeName: "google.protobuf.Any"},                             // TODO: Any
			{Name: "external", Kind: "message", TypeName: "other.v1.Thing"},                              // TODO: cross-package
			{Name: "created_at", Kind: "message", TypeName: "google.protobuf.Timestamp"},                 // managed → skipped
			{Name: "deleted_at", Kind: "message", TypeName: "google.protobuf.Timestamp", Optional: true}, // managed → skipped (soft delete)
		},
		Enums: map[string][]string{
			entityProtoPkg + ".OrderStatus": {"ORDER_STATUS_UNSPECIFIED", "ORDER_STATUS_OPEN"},
			entityProtoPkg + ".Priority":    {"PRIORITY_UNSPECIFIED", "PRIORITY_HIGH"},
		},
		SoftDelete: true, // the CLI flips this on when the message carries deleted_at
		Timestamps: true,
		// customer_id / parent_id resolve because Customer + Parent are real
		// entities (tables customers / parents) known to this birth, and both
		// tables already exist, so both references become real constraints.
		KnownTables:    map[string]bool{"customers": true, "parents": true},
		ExistingTables: []ExistingTable{{Name: "customers"}, {Name: "parents"}},
	}
}

func TestRenderEntityMigrationFromProto_MappingTable(t *testing.T) {
	mig := RenderEntityMigrationFromProto(entityProtoFixtureSpec())

	for _, want := range []string{
		"-- Born from proto message services.tasks.v1.Order",
		"forge never re-reads the proto",
		"CREATE TABLE orders (",
		"id TEXT PRIMARY KEY CHECK (id <> '')",
		"sku TEXT NOT NULL DEFAULT ''",
		"note TEXT,",
		"quantity BIGINT NOT NULL DEFAULT 0",
		"price DOUBLE PRECISION NOT NULL DEFAULT 0",
		"active BOOLEAN NOT NULL DEFAULT FALSE",
		`payload BYTEA NOT NULL DEFAULT '\x'`,
		"customer_id TEXT NOT NULL,",
		"parent_id TEXT,",
		// The proto zero is the "caller did not set this" value, so it is
		// neither the DEFAULT nor a member of the CHECK vocabulary — see
		// bornEnumVocabulary and entityproto_enum_sentinel_test.go. The
		// nullable form spells the same thing as NULL.
		"status TEXT NOT NULL DEFAULT 'ORDER_STATUS_OPEN' CHECK (status IN ('ORDER_STATUS_OPEN'))",
		`-- status: the proto zero (ORDER_STATUS_UNSPECIFIED) means "unset", never a state a`,
		"priority TEXT CHECK (priority IN ('PRIORITY_HIGH'))",
		"tags TEXT[] NOT NULL DEFAULT '{}'",
		"scheduled_at TIMESTAMPTZ,",
		"attrs JSONB NOT NULL DEFAULT '{}'",
		"shipping JSONB NOT NULL DEFAULT '{}'",
		"items JSONB NOT NULL DEFAULT '[]'",
		`-- TODO: proto field "payment" skipped — member of oneof "method"`,
		`-- TODO: proto field "blob" skipped — well-known type google.protobuf.Any`,
		`-- TODO: proto field "external" skipped — cross-package message other.v1.Thing`,
		"created_at TIMESTAMPTZ NOT NULL DEFAULT (now())",
		"updated_at TIMESTAMPTZ NOT NULL DEFAULT (now())",
		"deleted_at TIMESTAMPTZ\n",
		"ALTER TABLE orders ADD CONSTRAINT orders_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers (id);",
		"CREATE INDEX orders_customer_id_idx ON orders (customer_id);",
		"ALTER TABLE orders ADD CONSTRAINT orders_parent_id_fkey FOREIGN KEY (parent_id) REFERENCES parents (id);",
	} {
		if !strings.Contains(mig.UpSQL, want) {
			t.Errorf("up.sql missing %q:\n%s", want, mig.UpSQL)
		}
	}

	// Reference columns never get a zero default — an empty-string
	// reference is a bug, not a value.
	if strings.Contains(mig.UpSQL, "customer_id TEXT NOT NULL DEFAULT") {
		t.Errorf("customer_id must not carry a zero default:\n%s", mig.UpSQL)
	}
	// Managed fields on the message must appear exactly once — as the
	// migration's own convention columns, never as mapped duplicates.
	if n := strings.Count(mig.UpSQL, "created_at"); n != 1 {
		t.Errorf("created_at appears %d times, want exactly 1 (the managed column):\n%s", n, mig.UpSQL)
	}
	if n := strings.Count(mig.UpSQL, "deleted_at"); n != 1 {
		t.Errorf("deleted_at appears %d times, want exactly 1 (the soft-delete column):\n%s", n, mig.UpSQL)
	}

	// Every skip/TODO is reported — never silent.
	notes := strings.Join(mig.Notes, "\n")
	for _, want := range []string{"payment", "blob", "external", "created_at", "deleted_at", "id:"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes missing a mention of %q:\n%s", want, notes)
		}
	}
}

// TestScalarSQL_TotalOverTheProtoScalarVocabulary derives its obligation
// from codegen.ProtoScalarKinds() — the key set of the one table forge
// writes the fifteen scalar names down in — rather than from a list kept
// here.
//
// A kind scalarSQL does not map is not an error at the call site: the birth
// renderer carries it as a TODO comment and emits NO column, so the field
// silently does not reach the database. That is indistinguishable from the
// TODO a genuinely unmappable shape (a oneof, an Any) earns, which is why
// a gap here would read as intended behaviour.
func TestScalarSQL_TotalOverTheProtoScalarVocabulary(t *testing.T) {
	kinds := codegen.ProtoScalarKinds()
	if len(kinds) == 0 {
		t.Fatal("codegen.ProtoScalarKinds() is EMPTY — every obligation below is " +
			"derived from it, so an empty set would loop zero times and pass " +
			"while proving nothing")
	}
	if len(kinds) < 15 {
		t.Fatalf("codegen.ProtoScalarKinds() has %d members, expected 15: %v — the "+
			"vocabulary shrank, which silently narrows this check", len(kinds), kinds)
	}

	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			sqlType, zero, ok := scalarSQL(kind)
			if !ok {
				t.Fatalf("scalarSQL(%q) has no column mapping — a field of this kind "+
					"would be skipped with a TODO comment and never reach the schema", kind)
			}
			if sqlType == "" || zero == "" {
				t.Fatalf("scalarSQL(%q) = (%q, %q), want a column type and a zero default",
					kind, sqlType, zero)
			}
			// A numeric kind mapped to TEXT is the defect this file's
			// integer arm used to be one edit away from: the column
			// accepts the writes and compares as text.
			goType, _ := codegen.ProtoScalarGoType(kind)
			switch goType {
			case "int32", "int64", "uint32", "uint64", "float32", "float64":
				if sqlType == "TEXT" {
					t.Errorf("scalarSQL(%q) = TEXT for a numeric kind (Go type %q) — "+
						"the column would sort and compare lexically", kind, goType)
				}
			}
		})
	}
}

// TestProtoSQLMappings_CoversTheWholeScalarVocabulary pins the DUMP against
// the same derived vocabulary. The dump is the only description of the
// proto→column mapping forge ships, so a kind missing from it reads exactly
// like a kind forge has no mapping for.
//
// It also pins the GROUPING: the rows are ordered so that kinds sharing a
// column type are adjacent, which is what makes the ten integer widths
// legible as one BIGINT family in a terminal dump.
func TestProtoSQLMappings_CoversTheWholeScalarVocabulary(t *testing.T) {
	kinds := codegen.ProtoScalarKinds()
	if len(kinds) == 0 {
		t.Fatal("codegen.ProtoScalarKinds() is EMPTY — this check would be vacuous")
	}
	scalarKind := map[string]bool{}
	for _, k := range kinds {
		scalarKind[k] = true
	}

	dumped := map[string]bool{}
	var scalarSQLSeq []string
	for _, m := range ProtoSQLMappings() {
		dumped[m.Proto] = true
		if scalarKind[m.Proto] {
			scalarSQLSeq = append(scalarSQLSeq, m.SQL)
		}
	}
	for _, kind := range kinds {
		if !dumped[kind] {
			t.Errorf("ProtoSQLMappings() never mentions %q — `forge project annotations` "+
				"would not tell an author what column this kind gets", kind)
		}
	}

	// Each column type must occupy ONE contiguous run.
	seenClosed := map[string]bool{}
	for i, sql := range scalarSQLSeq {
		if i > 0 && sql == scalarSQLSeq[i-1] {
			continue
		}
		if seenClosed[sql] {
			t.Errorf("column %q appears in more than one run of the dump — kinds sharing "+
				"a column are no longer adjacent, so the integer family reads as scattered "+
				"unrelated rows: %v", sql, scalarSQLSeq)
		}
		seenClosed[sql] = true
	}
}

// TestProtoSQLMappings_MatchTheRenderer pins the dump against the emitter.
//
// ProtoSQLMappings is what `forge project annotations --kind field` prints,
// and it is the ONLY description of the proto→column mapping forge ships now
// that the `field:type` vocabulary is gone. Its scalar half is projected from
// scalarSQL and cannot drift; its structural half is a literal, which can —
// so every row is re-derived here by rendering a real migration for a field
// of that shape and reading the column back out. A mapping row that no longer
// describes what a birth emits fails here rather than misleading an author
// who trusted the dump.
func TestProtoSQLMappings_MatchTheRenderer(t *testing.T) {
	// One probe field per mapping row, and the column text the row claims
	// the renderer emits for it. `<type>` placeholders are resolved against
	// the probe's own scalar kind, which is what the row abbreviates.
	probes := []struct {
		row   string // the ProtoSQLMappings row this exercises
		field codegen.SchemaFieldDef
		want  string // the column line the migration must contain
	}{
		{"string", codegen.SchemaFieldDef{Name: "sku", Kind: "string"}, "sku TEXT NOT NULL DEFAULT ''"},
		{"bool", codegen.SchemaFieldDef{Name: "active", Kind: "bool"}, "active BOOLEAN NOT NULL DEFAULT FALSE"},
		{"bytes", codegen.SchemaFieldDef{Name: "payload", Kind: "bytes"}, `payload BYTEA NOT NULL DEFAULT '\x'`},
		{"int64", codegen.SchemaFieldDef{Name: "quantity", Kind: "int64"}, "quantity BIGINT NOT NULL DEFAULT 0"},
		{"double", codegen.SchemaFieldDef{Name: "price", Kind: "double"}, "price DOUBLE PRECISION NOT NULL DEFAULT 0"},
		{"optional <scalar>", codegen.SchemaFieldDef{Name: "note", Kind: "string", Optional: true}, "note TEXT\n"},
		{"repeated <scalar>", codegen.SchemaFieldDef{Name: "tags", Kind: "string", Repeated: true}, "tags TEXT[] NOT NULL DEFAULT '{}'"},
		{"string <x>_id", codegen.SchemaFieldDef{Name: "customer_id", Kind: "string"}, "customer_id TEXT NOT NULL\n"},
		{"enum E (same package)", codegen.SchemaFieldDef{Name: "status", Kind: "enum", TypeName: entityProtoPkg + ".OrderStatus"},
			"status TEXT NOT NULL DEFAULT 'ORDER_STATUS_OPEN' CHECK (status IN ('ORDER_STATUS_OPEN'))"},
		{"repeated enum E", codegen.SchemaFieldDef{Name: "labels", Kind: "enum", TypeName: entityProtoPkg + ".OrderStatus", Repeated: true},
			"labels TEXT[] NOT NULL DEFAULT '{}'"},
		{"google.protobuf.Timestamp", codegen.SchemaFieldDef{Name: "scheduled_at", Kind: "message", TypeName: "google.protobuf.Timestamp"}, "scheduled_at TIMESTAMPTZ\n"},
		{"nested message (same package)", codegen.SchemaFieldDef{Name: "shipping", Kind: "message", TypeName: entityProtoPkg + ".Address"}, "shipping JSONB NOT NULL DEFAULT '{}'"},
		{"map<K, scalar>", codegen.SchemaFieldDef{Name: "attrs", Kind: "map", MapKeyKind: "string", MapValueKind: "int64"}, "attrs JSONB NOT NULL DEFAULT '{}'"},
		{"<field> // forge:generated <expr>", codegen.SchemaFieldDef{Name: "line_total", Kind: "int64", Generated: "quantity * 2"},
			"line_total BIGINT NOT NULL GENERATED ALWAYS AS (quantity * 2) STORED"},
	}

	for _, p := range probes {
		t.Run(p.row, func(t *testing.T) {
			mig := RenderEntityMigrationFromProto(EntityFromProtoSpec{
				Table:     "orders",
				MessageFQ: entityProtoPkg + ".Order",
				ProtoPkg:  entityProtoPkg,
				Fields:    []codegen.SchemaFieldDef{p.field},
				Enums: map[string][]string{
					entityProtoPkg + ".OrderStatus": {"ORDER_STATUS_UNSPECIFIED", "ORDER_STATUS_OPEN"},
				},
			})
			if !strings.Contains(mig.UpSQL, p.want) {
				t.Errorf("ProtoSQLMappings advertises %q for a %s field, but the renderer emitted:\n%s\n(wanted the column %q)",
					p.row, p.row, mig.UpSQL, p.want)
			}
		})
	}

	// Every row the dump advertises must have been exercised above: a row
	// added without a probe is a claim nothing checks.
	probed := map[string]bool{}
	for _, p := range probes {
		probed[p.row] = true
	}
	for _, m := range ProtoSQLMappings() {
		if !probed[m.Proto] {
			// The numeric widths all funnel through the same BIGINT
			// branch as int64, which IS probed.
			if m.SQL == "BIGINT NOT NULL DEFAULT 0" || m.SQL == "DOUBLE PRECISION NOT NULL DEFAULT 0" {
				continue
			}
			t.Errorf("ProtoSQLMappings row %q has no probe — it advertises a column nothing verifies", m.Proto)
		}
	}
}

// TestResolveFKTable pins the `<x>_id` → referenced-table resolver: a real
// entity resolves to its actual table (never naive pluralization of the
// column stem), a role-prefixed column resolves to the trailing entity,
// and a stem that names no known table resolves to nothing (auth/
// polymorphic columns get no FK suggestion).
func TestResolveFKTable(t *testing.T) {
	known := map[string]bool{
		"providers": true, // Provider entity
		"orders":    true, // Order entity
		"people":    true, // Person entity (irregular plural)
	}
	cases := []struct {
		name    string
		column  string
		known   map[string]bool
		wantTbl string
		wantOK  bool
	}{
		{"bare stem → entity table", "provider_id", known, "providers", true},
		{"role prefix resolves to trailing entity", "assigned_provider_id", known, "providers", true},
		{"multi-word role prefix", "primary_provider_id", known, "providers", true},
		{"self/hierarchy role → own table", "parent_order_id", known, "orders", true},
		{"irregular plural resolves", "person_id", known, "people", true},
		{"unknown stem → no suggestion", "user_id", known, "", false},
		{"unknown role-prefixed → no suggestion", "actor_user_id", known, "", false},
		{"unknown bare polymorphic → no suggestion", "entity_id", known, "", false},
		{"empty registry → no suggestion", "provider_id", nil, "", false},
		// An empty owner is not a reference: `_id` has no stem to resolve,
		// and a FK aimed at a table named by nothing fails the migration.
		{"empty owner is not a reference", "_id", known, "", false},
		// A column that merely CONTAINS "id" is not a reference — the
		// suffix must be the whole `_id` tail.
		{"a column that merely contains id", "paid", known, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tbl, ok := resolveFKTable(tc.column, tc.known)
			if tbl != tc.wantTbl || ok != tc.wantOK {
				t.Errorf("resolveFKTable(%q) = (%q, %v), want (%q, %v)", tc.column, tbl, ok, tc.wantTbl, tc.wantOK)
			}
		})
	}
}

// TestRenderEntityMigrationFromProto_FKsResolveOrOmit is the end-to-end
// shape: an entity with provider_id + assigned_provider_id (Provider is a
// real, already-created entity) + user_id (no User entity) constrains BOTH
// provider columns to providers and emits NOTHING for user_id — never
// REFERENCES assigned_providers or REFERENCES users.
//
// The constraints are APPLIED, not commented. Shipping them commented left
// every generated database without referential integrity, and the seeder —
// which reads the FK graph by introspecting the live database — filled
// every child column with a synthesized string instead of a real parent id.
func TestRenderEntityMigrationFromProto_FKsResolveOrOmit(t *testing.T) {
	spec := EntityFromProtoSpec{
		Table:     "orders",
		MessageFQ: entityProtoPkg + ".Order",
		ProtoPkg:  entityProtoPkg,
		Fields: []codegen.SchemaFieldDef{
			{Name: "id", Kind: "string"},
			{Name: "provider_id", Kind: "string"},
			{Name: "assigned_provider_id", Kind: "string"},
			{Name: "user_id", Kind: "string"}, // auth column — no User entity
		},
		Timestamps:     true,
		KnownTables:    map[string]bool{"providers": true, "orders": true},
		ExistingTables: []ExistingTable{{Name: "providers"}},
	}
	mig := RenderEntityMigrationFromProto(spec)
	up := mig.UpSQL

	for _, want := range []string{
		"ALTER TABLE orders ADD CONSTRAINT orders_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES providers (id);",
		"CREATE INDEX orders_provider_id_idx ON orders (provider_id);",
		"ALTER TABLE orders ADD CONSTRAINT orders_assigned_provider_id_fkey FOREIGN KEY (assigned_provider_id) REFERENCES providers (id);",
		"CREATE INDEX orders_assigned_provider_id_idx ON orders (assigned_provider_id);",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("up.sql missing FK statement %q:\n%s", want, up)
		}
	}
	// A constraint forge writes commented out is a constraint forge does
	// not build. Nothing here may be commented.
	if strings.Contains(up, "-- ALTER TABLE") || strings.Contains(up, "-- CREATE INDEX") {
		t.Errorf("foreign keys must be applied, not commented out:\n%s", up)
	}
	// The naive-pluralization bug, and the absent-target cases.
	for _, bad := range []string{
		"assigned_providers",          // naive pluralize of the role-prefixed stem
		"REFERENCES users",            // user_id has no User entity
		"orders_user_id_fkey",         // …so no constraint at all
		"CREATE INDEX orders_user_id", // …and no lone index either
	} {
		if strings.Contains(up, bad) {
			t.Errorf("up.sql must not contain %q (wrong/absent-target reference):\n%s", bad, up)
		}
	}
	if len(mig.PendingRefColumns) != 0 {
		t.Errorf("PendingRefColumns = %v, want none (providers already exists)", mig.PendingRefColumns)
	}
}

// TestRenderEntityMigrationFromProto_FKOrderIndependence is the property
// that lets forge apply these constraints at all: a reference may only name
// a table an EARLIER migration created, and entities are born in whatever
// order the author asks for.
//
// A child born BEFORE its parent gets the index and a pending column, never
// a constraint the database would reject. The parent's own birth migration
// then back-fills it — and its down migration drops it, because DROP TABLE
// parents would otherwise fail on a constraint it does not own.
func TestRenderEntityMigrationFromProto_FKOrderIndependence(t *testing.T) {
	known := map[string]bool{"orders": true, "customers": true}

	// 1. orders is born first. customers does not exist yet.
	child := RenderEntityMigrationFromProto(EntityFromProtoSpec{
		Table:     "orders",
		MessageFQ: entityProtoPkg + ".Order",
		ProtoPkg:  entityProtoPkg,
		Fields: []codegen.SchemaFieldDef{
			{Name: "id", Kind: "string"},
			{Name: "customer_id", Kind: "string"},
		},
		KnownTables: known,
	})
	if strings.Contains(child.UpSQL, "ADD CONSTRAINT") {
		t.Errorf("a reference to a table that does not exist yet must not be constrained here:\n%s", child.UpSQL)
	}
	if !strings.Contains(child.UpSQL, "CREATE INDEX orders_customer_id_idx ON orders (customer_id);") {
		t.Errorf("the index does not depend on the referent and must still be emitted:\n%s", child.UpSQL)
	}
	if got := child.PendingRefColumns; len(got) != 1 || got[0] != "customer_id" {
		t.Fatalf("PendingRefColumns = %v, want [customer_id]", got)
	}

	// 2. customers is born second, carrying orders' pending column.
	parent := RenderEntityMigrationFromProto(EntityFromProtoSpec{
		Table:       "customers",
		MessageFQ:   entityProtoPkg + ".Customer",
		ProtoPkg:    entityProtoPkg,
		Fields:      []codegen.SchemaFieldDef{{Name: "id", Kind: "string"}},
		KnownTables: known,
		ExistingTables: []ExistingTable{
			{Name: "orders", UnconstrainedRefColumns: child.PendingRefColumns},
		},
	})
	want := "ALTER TABLE orders ADD CONSTRAINT orders_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers (id);"
	if !strings.Contains(parent.UpSQL, want) {
		t.Errorf("customers must back-fill the reference orders could not constrain:\n%s", parent.UpSQL)
	}
	if got := parent.BackfilledRefColumns["orders"]; len(got) != 1 || got[0] != "customer_id" {
		t.Errorf("BackfilledRefColumns[orders] = %v, want [customer_id]", got)
	}
}

// TestRenderEntityMigrationFromProto_SelfReferenceIsSatisfiable pins the
// one forward case with no earlier table behind it: a hierarchy column
// points at the table this very migration just created.
func TestRenderEntityMigrationFromProto_SelfReferenceIsSatisfiable(t *testing.T) {
	mig := RenderEntityMigrationFromProto(EntityFromProtoSpec{
		Table:     "orders",
		MessageFQ: entityProtoPkg + ".Order",
		ProtoPkg:  entityProtoPkg,
		Fields: []codegen.SchemaFieldDef{
			{Name: "id", Kind: "string"},
			{Name: "parent_order_id", Kind: "string", Optional: true},
		},
		KnownTables: map[string]bool{"orders": true},
	})
	want := "ALTER TABLE orders ADD CONSTRAINT orders_parent_order_id_fkey FOREIGN KEY (parent_order_id) REFERENCES orders (id);"
	if !strings.Contains(mig.UpSQL, want) {
		t.Errorf("a self-reference names the table this migration just created:\n%s", mig.UpSQL)
	}
	if len(mig.PendingRefColumns) != 0 {
		t.Errorf("PendingRefColumns = %v, want none", mig.PendingRefColumns)
	}
}

// TestRenderEntityMigrationFromProto_ValidateConstraints pins the
// protovalidate → DB CHECK projection: numeric bounds, string length,
// pattern, email, and required, plus the zero-default suppression when the
// zero value would violate the emitted CHECK.
func TestRenderEntityMigrationFromProto_ValidateConstraints(t *testing.T) {
	u := func(n uint64) *uint64 { return &n }
	spec := EntityFromProtoSpec{
		Table:     "widgets",
		MessageFQ: entityProtoPkg + ".Widget",
		ProtoPkg:  entityProtoPkg,
		Fields: []codegen.SchemaFieldDef{
			{Name: "amount_cents", Kind: "int64", Validate: &codegen.FieldConstraints{Gte: "0", Lte: "100000"}},
			{Name: "name", Kind: "string", Validate: &codegen.FieldConstraints{MinLen: u(2), MaxLen: u(64)}},
			{Name: "sku", Kind: "string", Validate: &codegen.FieldConstraints{Pattern: "^SKU-[0-9]+$"}},
			{Name: "email", Kind: "string", Validate: &codegen.FieldConstraints{Email: true}},
			{Name: "slug", Kind: "string", Validate: &codegen.FieldConstraints{Required: true}},
			{Name: "note", Kind: "string", Optional: true, Validate: &codegen.FieldConstraints{MaxLen: u(500)}},
		},
		Timestamps: true,
	}
	mig := RenderEntityMigrationFromProto(spec)
	for _, want := range []string{
		"amount_cents BIGINT NOT NULL DEFAULT 0 CHECK (amount_cents >= 0) CHECK (amount_cents <= 100000)",
		"name TEXT NOT NULL CHECK (char_length(name) BETWEEN 2 AND 64)",
		"sku TEXT NOT NULL CHECK (sku ~ '^SKU-[0-9]+$')",
		"slug TEXT NOT NULL CHECK (char_length(slug) >= 1)",
		"note TEXT CHECK (char_length(note) <= 500)",
	} {
		if !strings.Contains(mig.UpSQL, want) {
			t.Errorf("up.sql missing %q:\n%s", want, mig.UpSQL)
		}
	}
	// email → permissive regex CHECK, no zero default.
	if !strings.Contains(mig.UpSQL, "email TEXT NOT NULL CHECK (email ~ '^[^@") {
		t.Errorf("email column should carry a regex CHECK and no default:\n%s", mig.UpSQL)
	}
	// A lower-bound / non-empty field must NOT keep a zero default.
	for _, bad := range []string{"name TEXT NOT NULL DEFAULT", "sku TEXT NOT NULL DEFAULT", "slug TEXT NOT NULL DEFAULT"} {
		if strings.Contains(mig.UpSQL, bad) {
			t.Errorf("constrained column kept a zero default (%q):\n%s", bad, mig.UpSQL)
		}
	}

	// The emitted CHECK SQL (BETWEEN, char_length, ~ regex, email regex)
	// must apply verbatim on the same real ephemeral postgres forge
	// introspects — a bad regex/quoting would break entity birth.
	if testing.Short() {
		return
	}
	dir := filepath.Join(t.TempDir(), "db", "migrations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "00001_create_widgets.up.sql"), []byte(mig.UpSQL), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := schemadef.ApplyAndIntrospect(dir); err != nil {
		t.Fatalf("validate-constrained migration failed the shadow apply: %v\n%s", err, mig.UpSQL)
	}
}

func TestRenderEntityMigrationFromProto_NoTimestampsNoSoftDelete(t *testing.T) {
	spec := entityProtoFixtureSpec()
	spec.SoftDelete = false
	spec.Timestamps = false
	mig := RenderEntityMigrationFromProto(spec)
	for _, absent := range []string{"created_at", "updated_at", "deleted_at"} {
		if strings.Contains(mig.UpSQL, absent) {
			t.Errorf("up.sql must not contain %q with timestamps/soft-delete off:\n%s", absent, mig.UpSQL)
		}
	}
}

// TestRenderEntityMigrationFromProto_BoolDefaultNamesItsSource pins the
// honesty comment on a NOT NULL bool column.
//
// The mapping is defensible and stays: unlike an enum's *_UNSPECIFIED,
// `false` IS a valid row state, and `optional bool` already gives a nullable
// column for the "unset" case. What is NOT derivable from a descriptor is
// which direction is SAFE — a flag like rx_required / requires_approval /
// is_locked is born permissive, and a real workflow run hand-edited exactly
// such a column to TRUE right after birth. So the migration says where the
// value came from and leaves the decision to its owner: comment only, no
// domain assumption, no changed DDL.
func TestRenderEntityMigrationFromProto_BoolDefaultNamesItsSource(t *testing.T) {
	spec := entityProtoFixtureSpec()
	spec.Fields = append(spec.Fields,
		codegen.SchemaFieldDef{Name: "rx_required", Kind: "bool"},
		codegen.SchemaFieldDef{Name: "gift_wrapped", Kind: "bool", Optional: true},
		codegen.SchemaFieldDef{Name: "flags", Kind: "bool", Repeated: true},
	)
	mig := RenderEntityMigrationFromProto(spec)

	// The DDL is unchanged — this fix adds a comment, it does not pick a
	// direction.
	if !strings.Contains(mig.UpSQL, "rx_required BOOLEAN NOT NULL DEFAULT FALSE") {
		t.Errorf("the bool mapping itself must not change:\n%s", mig.UpSQL)
	}
	for _, want := range []string{
		"-- rx_required: DEFAULT FALSE is the proto3 zero, not a domain decision.",
		"--   Change it to TRUE if the SAFE state for this flag is on.",
		// Every non-optional bool, not just the one that reads like a flag:
		// forge cannot tell which names are safety-bearing.
		"-- active: DEFAULT FALSE is the proto3 zero, not a domain decision.",
	} {
		if !strings.Contains(mig.UpSQL, want) {
			t.Errorf("up.sql missing %q:\n%s", want, mig.UpSQL)
		}
	}
	// Columns with no FALSE default get no comment: an optional bool is
	// nullable with no default at all, and a repeated bool defaults to an
	// empty array.
	for _, absent := range []string{
		"-- gift_wrapped: DEFAULT FALSE",
		"-- flags: DEFAULT FALSE",
	} {
		if strings.Contains(mig.UpSQL, absent) {
			t.Errorf("up.sql must not carry %q — that column has no FALSE default:\n%s", absent, mig.UpSQL)
		}
	}
}

// TestRenderEntityMigrationFromProto_ShadowRoundTrip is the contract:
// the emitted SQL (CHECK constraints, arrays, BYTEA default, interleaved
// TODO comments, comma placement) must apply verbatim on the same real
// ephemeral postgres `forge generate` introspects.
func TestRenderEntityMigrationFromProto_ShadowRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("shadow postgres round-trip skipped in -short mode")
	}
	mig := RenderEntityMigrationFromProto(entityProtoFixtureSpec())

	dir := filepath.Join(t.TempDir(), "db", "migrations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The referents the fixture declares as already-existing. Writing them
	// as an earlier migration is not test scaffolding — it is the ordering
	// the emitted FOREIGN KEYs depend on, and applying them proves the
	// constraints forge writes are constraints postgres accepts.
	parents := "CREATE TABLE customers (id TEXT PRIMARY KEY);\nCREATE TABLE parents (id TEXT PRIMARY KEY);\n"
	if err := os.WriteFile(filepath.Join(dir, "00001_create_referents.up.sql"), []byte(parents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "00002_create_orders.up.sql"), []byte(mig.UpSQL), 0o644); err != nil {
		t.Fatal(err)
	}

	tables, err := schemadef.ApplyAndIntrospect(dir)
	if err != nil {
		t.Fatalf("emitted migration failed the shadow apply: %v\n%s", err, mig.UpSQL)
	}
	byTable := map[string]schemadef.Table{}
	for _, tb := range tables {
		byTable[tb.Name] = tb
	}
	orders, ok := byTable["orders"]
	if !ok {
		t.Fatalf("tables = %+v", tables)
	}
	// The point of the change: the constraints forge emitted are DECLARED
	// in the applied schema, so every consumer that introspects the FK
	// graph — the seeder above all — can see them.
	gotFK := map[string]string{}
	for _, fk := range orders.ForeignKeys {
		gotFK[fk.Column] = fk.RefTable
	}
	for col, want := range map[string]string{"customer_id": "customers", "parent_id": "parents"} {
		if gotFK[col] != want {
			t.Errorf("orders.%s introspects as REFERENCES %q, want %q (foreign keys = %+v)",
				col, gotFK[col], want, orders.ForeignKeys)
		}
	}
	tables = []schemadef.Table{orders}
	conv := schemadef.DetectConventions(tables[0])
	if !conv.SoftDelete || !conv.Timestamps {
		t.Errorf("conventions = %+v, want soft-delete + timestamps", conv)
	}

	byName := map[string]schemadef.Column{}
	for _, c := range tables[0].Columns {
		byName[c.Name] = c
	}
	if c, ok := byName["note"]; !ok || c.NotNull {
		t.Errorf("optional field must introspect as a nullable column: %+v", byName["note"])
	}
	if c, ok := byName["sku"]; !ok || !c.NotNull {
		t.Errorf("required scalar must introspect NOT NULL: %+v", byName["sku"])
	}
	if c, ok := byName["tags"]; !ok || !c.IsArray {
		t.Errorf("repeated scalar must introspect as an array column: %+v", byName["tags"])
	}
	for _, todoField := range []string{"payment", "blob", "external"} {
		if _, exists := byName[todoField]; exists {
			t.Errorf("TODO-carried field %q must not become a column", todoField)
		}
	}

	// Postgres itself, not a string match, reports what a row omitting
	// `status` is born as. It must be a real state. This is the property
	// the 953950c8 create-op path reads back out of the schema, so a
	// sentinel here becomes a sentinel in every generated create.
	if c, ok := byName["status"]; !ok || !strings.Contains(c.Default, "ORDER_STATUS_OPEN") {
		t.Errorf("status column default = %q, want the first real OrderStatus member", byName["status"].Default)
	}
	if strings.Contains(byName["status"].Default, "UNSPECIFIED") {
		t.Errorf("status column default is the proto zero (%q) — a row is never in the \"unset\" state",
			byName["status"].Default)
	}
}

// TestBornEnumDefault covers the edges of the birth-state choice. The
// sentinel is a wire concept; the rule has to survive enums that do not
// carry one, and enums that carry nothing else.
func TestBornEnumDefault(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   string
	}{
		{
			name:   "conventional enum skips the proto zero",
			values: []string{"ORDER_STATUS_UNSPECIFIED", "ORDER_STATUS_DRAFT", "ORDER_STATUS_SHIPPED"},
			want:   "ORDER_STATUS_DRAFT",
		},
		{
			name:   "the _UNKNOWN spelling is a sentinel too",
			values: []string{"SOURCE_UNKNOWN", "SOURCE_WEB"},
			want:   "SOURCE_WEB",
		},
		{
			name:   "an enum whose zero is a real member keeps it",
			values: []string{"COLOR_RED", "COLOR_BLUE"},
			want:   "COLOR_RED",
		},
		{
			name:   "sentinel-only enum falls back rather than emitting an empty literal",
			values: []string{"THING_UNSPECIFIED"},
			want:   "THING_UNSPECIFIED",
		},
		{
			// _NONE / _INVALID are frequently real states, so they are not
			// treated as sentinels — the shared seeddata predicate decides.
			name:   "_NONE is a real member",
			values: []string{"TIER_NONE", "TIER_PRO"},
			want:   "TIER_NONE",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bornEnumDefault(tt.values); got != tt.want {
				t.Errorf("bornEnumDefault(%v) = %q, want %q", tt.values, got, tt.want)
			}
		})
	}
}

// TestRenderEntityMigrationFromProto_OptionalIsNeverNotNull is the
// nullability property, stated once for EVERY field kind rather than left
// implicit in one line of the big mapping assertion.
//
// A NOT NULL column for an OPTIONAL relationship is not a cosmetic slip: it
// makes the schema contradict the model the proto declares, and every row
// then has to carry a value the domain says is absent. A real-workflow run
// hand-repaired exactly this on a `prescription_id` before it could start
// work. The renderer has been right about it since the first commit — this
// pins it so it stays right, and it pins the MIRROR too (a field without
// `optional` must still be NOT NULL), so the property cannot be satisfied
// by making everything nullable.
//
// Proof by BUILDING, not by grepping: the migration is applied to the same
// real postgres `forge generate` introspects, and nullability is read back
// out of postgres's own catalog.
func TestRenderEntityMigrationFromProto_OptionalIsNeverNotNull(t *testing.T) {
	// One field per kind, in both spellings. The `_id` pair is the exact
	// shape that broke: a reference column is emitted by its own branch.
	kinds := []struct {
		name  string
		field codegen.SchemaFieldDef
	}{
		{"string", codegen.SchemaFieldDef{Kind: "string"}},
		{"bool", codegen.SchemaFieldDef{Kind: "bool"}},
		{"int64", codegen.SchemaFieldDef{Kind: "int64"}},
		{"double", codegen.SchemaFieldDef{Kind: "double"}},
		{"bytes", codegen.SchemaFieldDef{Kind: "bytes"}},
		{"enum", codegen.SchemaFieldDef{Kind: "enum", TypeName: entityProtoPkg + ".Priority"}},
		{"timestamp", codegen.SchemaFieldDef{Kind: "message", TypeName: "google.protobuf.Timestamp"}},
		{"message", codegen.SchemaFieldDef{Kind: "message", TypeName: entityProtoPkg + ".Address"}},
		// The reference form: `<x>_id` takes a dedicated branch, and a
		// reference is exactly where "optional" carries domain weight.
		{"reference_id", codegen.SchemaFieldDef{Kind: "string"}},
		// A constrained scalar: the CHECK suffix must not drag NOT NULL
		// back in.
		{"validated", codegen.SchemaFieldDef{Kind: "string", Validate: &codegen.FieldConstraints{Required: true}}},
	}

	spec := EntityFromProtoSpec{
		Table:     "nullability",
		MessageFQ: entityProtoPkg + ".Nullability",
		ProtoPkg:  entityProtoPkg,
		Enums:     map[string][]string{entityProtoPkg + ".Priority": {"PRIORITY_UNSPECIFIED", "PRIORITY_HIGH"}},
		Fields:    []codegen.SchemaFieldDef{{Name: "id", Kind: "string"}},
	}
	// want[column] = the nullability the proto declares.
	want := map[string]bool{}
	for _, k := range kinds {
		req, opt := k.field, k.field
		req.Name = "req_" + k.name
		opt.Name = "opt_" + k.name
		if k.name == "reference_id" {
			req.Name, opt.Name = "req_customer_id", "opt_customer_id"
		}
		opt.Optional = true
		spec.Fields = append(spec.Fields, req, opt)
		// A google.protobuf.Timestamp column is nullable in BOTH spellings
		// (the mapping mirrors the field-list form's `time`), so it is the
		// one kind whose required form carries no NOT NULL.
		want[req.Name] = k.name != "timestamp"
		want[opt.Name] = false
	}

	mig := RenderEntityMigrationFromProto(spec)

	// An optional field must never carry NOT NULL, in the SQL text.
	for col, notNull := range want {
		if notNull {
			continue
		}
		if strings.Contains(mig.UpSQL, col+" ") && strings.Contains(lineFor(mig.UpSQL, col), "NOT NULL") {
			t.Errorf("optional field %s emitted NOT NULL — the schema then contradicts the proto:\n  %s",
				col, lineFor(mig.UpSQL, col))
		}
	}

	if testing.Short() {
		return
	}
	// And in the SCHEMA postgres actually installs.
	dir := filepath.Join(t.TempDir(), "db", "migrations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "00001_nullability.up.sql"), []byte(mig.UpSQL), 0o644); err != nil {
		t.Fatal(err)
	}
	tables, err := schemadef.ApplyAndIntrospect(dir)
	if err != nil {
		t.Fatalf("the nullability migration does not apply: %v\n%s", err, mig.UpSQL)
	}
	if len(tables) != 1 {
		t.Fatalf("introspected %d tables, want 1", len(tables))
	}
	got := map[string]bool{}
	for _, c := range tables[0].Columns {
		got[c.Name] = c.NotNull
	}
	for col, notNull := range want {
		have, ok := got[col]
		if !ok {
			t.Errorf("column %s was never emitted", col)
			continue
		}
		if have != notNull {
			t.Errorf("postgres reports %s NOT NULL = %v, want %v (the proto says optional = %v)",
				col, have, notNull, !notNull)
		}
	}
}

// lineFor returns the CREATE TABLE line defining a column, for a failure
// message that shows the offending DDL rather than the whole migration.
func lineFor(sqlText, column string) string {
	for _, line := range strings.Split(sqlText, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), column+" ") {
			return strings.TrimSpace(line)
		}
	}
	return "<column not found>"
}

// TestRenderEntityMigrationFromProto_UniqueAndDateAreNotDeclarable records
// two schema facts forge has NO WAY to express at birth. Both cost a
// measured real-workflow run hand-edits immediately after scaffolding.
//
// UNIQUE. Nothing in the birth vocabulary declares it: not the `field:type`
// tokens (entityTypeVocab), not a `// forge:` marker, and not protovalidate
// — buf.validate is a per-MESSAGE rule engine and uniqueness is a statement
// about a whole table, so it could not live there even in principle. The
// author's only route is a hand-written follow-up migration, which forge's
// own generated code already anticipates (crud_gen's second lifecycle-test
// row exists because "the first UNIQUE index the author adds" would
// otherwise break a scaffold-once file).
//
// DATE. A `google.protobuf.Timestamp` field is born TIMESTAMPTZ, which is
// correct for an instant and wrong for a calendar date: a date of birth
// acquires a time zone it does not have, and every consumer then has to
// decide which midnight it meant. The proto side has no date type either
// (google.type.Date exists but is not a well-known type forge maps), so the
// declaration has nowhere to live today.
//
// This test pins TODAY'S behavior, deliberately. It is the record that the
// gap is known and unclosed — not an endorsement. Closing it means adding a
// DECLARATION (a `// forge:unique` marker on the field, a `date` token in
// the field-list vocabulary and a google.type.Date mapping in the
// descriptor path), never a name heuristic: a column called `date_of_birth`
// silently becoming DATE while `dob` stays TIMESTAMPTZ is exactly the magic
// forge exists to not have. When the declaration lands, replace this test
// with one that asserts it is honored.
func TestRenderEntityMigrationFromProto_UniqueAndDateAreNotDeclarable(t *testing.T) {
	mig := RenderEntityMigrationFromProto(EntityFromProtoSpec{
		Table:     "members",
		MessageFQ: entityProtoPkg + ".Member",
		ProtoPkg:  entityProtoPkg,
		Fields: []codegen.SchemaFieldDef{
			{Name: "id", Kind: "string"},
			// Every spelling a field could plausibly use to ask for either.
			{Name: "email", Kind: "string", Validate: &codegen.FieldConstraints{Email: true, Required: true}},
			{Name: "date_of_birth", Kind: "message", TypeName: "google.protobuf.Timestamp"},
			{Name: "starts_on", Kind: "message", TypeName: "google.protobuf.Timestamp"},
		},
		Timestamps: true,
	})

	if strings.Contains(strings.ToUpper(mig.UpSQL), "UNIQUE") {
		t.Errorf("a UNIQUE constraint appeared without any way to declare one — if a declaration was added, "+
			"replace this test with one asserting it is honored:\n%s", mig.UpSQL)
	}
	// `\bDATE\b` so a column NAMED date_of_birth does not read as a DATE
	// column type.
	if regexp.MustCompile(`\bDATE\b`).MatchString(strings.ToUpper(mig.UpSQL)) {
		t.Errorf("a DATE column appeared without any way to declare one — if a declaration was added, "+
			"replace this test with one asserting it is honored:\n%s", mig.UpSQL)
	}
	for _, col := range []string{"date_of_birth", "starts_on"} {
		if !strings.Contains(mig.UpSQL, col+" TIMESTAMPTZ") {
			t.Errorf("%s should be born TIMESTAMPTZ today (the documented gap):\n%s", col, mig.UpSQL)
		}
	}
}
