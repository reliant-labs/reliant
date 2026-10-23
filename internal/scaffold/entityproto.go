// File: internal/scaffold/entityproto.go
//
// The proto→schema birth affordance behind
// `forge scaffold entity <name> --from-proto <svc>[.<Message>]`:
// render the create-table migration pair for an entity whose wire
// message is ALREADY authored in the service proto (read from the
// compiled descriptor). This is a one-time convenience at birth — the
// emitted migration is USER-OWNED the moment it is written, and forge
// never re-reads the proto to regenerate or edit it. Evolution is a new
// migration plus a proto edit; the two truths evolve on independent
// clocks ("the line is the filesystem", docs/design/VERTICAL_SCAFFOLDING.md §6).
//
// ── proto → column mapping ───────────────────────────────────────────
//
//	proto field                        column
//	─────────────────────────────────  ─────────────────────────────────────────
//	string                             TEXT NOT NULL DEFAULT ''
//	bool                               BOOLEAN NOT NULL DEFAULT FALSE
//	                                   (+ a comment naming the proto3 zero as
//	                                   the source of the default — a safety
//	                                   flag's safe direction is domain policy)
//	int32/int64/sint*/sfixed*/(u)ints  BIGINT NOT NULL DEFAULT 0
//	float/double                       DOUBLE PRECISION NOT NULL DEFAULT 0
//	bytes                              BYTEA NOT NULL DEFAULT '\x'
//	optional <scalar>                  same type, nullable, no default
//	<x>_id string                      TEXT NOT NULL (nullable when optional) +
//	                                   an APPLIED FOREIGN KEY + index when the
//	                                   stem resolves to a known table — forward
//	                                   when the target already exists, pending
//	                                   (index only, constraint deferred to that
//	                                   table's own birth) when it is born later
//	                                   in this run, or backfilled onto an
//	                                   already-born table that references this
//	                                   one (see planForeignKeys)
//	google.protobuf.Timestamp          TIMESTAMPTZ (nullable — a message field
//	                                   has wire presence; the REPEATED form is
//	                                   a TODO, see below)
//	enum E (same package)              TEXT + CHECK (col IN (<value names MINUS
//	                                   the proto zero sentinel>)), NOT NULL
//	                                   DEFAULT <first admitted value> unless
//	                                   optional, where NULL is "unset"
//	                                   (see bornEnumVocabulary)
//	repeated <scalar>                  <type>[] NOT NULL DEFAULT '{}'
//	repeated enum                      TEXT[] NOT NULL DEFAULT '{}' (+ values comment)
//	map<K, scalar>                     JSONB NOT NULL DEFAULT '{}'
//	nested message (same package)      JSONB NOT NULL DEFAULT '{}' ('[]' when
//	                                   repeated, plain JSONB when optional)
//	any of: scalar / enum / Timestamp  <type> NOT NULL GENERATED ALWAYS AS
//	  + `// forge:generated <expr>`    (<expr>) STORED — no DEFAULT; nullable
//	                                   when optional (and for a Timestamp);
//	                                   an enum keeps its CHECK. Repeated, map,
//	                                   oneof and JSONB fields are refused
//	                                   (see GeneratedColumnRefusal)
//
// Deliberately carried as TODO comment lines inside the CREATE TABLE —
// never silently dropped: oneof members, google.protobuf.* well-knowns
// other than a SINGULAR Timestamp (Duration, FieldMask, Struct/Value/Any,
// wrappers, and `repeated Timestamp`), cross-package message/enum
// references, and maps whose VALUES are messages or enums. These are the
// rule this file shares with the CRUD conversion generator: birth creates a
// column only for what the generator will map onto it, because a column
// with no conversion is a field that is dead over the API while everything
// reports green.
//
// Managed columns are forge's, never the message's: id / created_at /
// updated_at / deleted_at fields on the message are SKIPPED — the table
// gets the canonical PK + timestamp columns instead, and a deleted_at
// field flips soft-delete on.

package scaffold

import (
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/pkg/schemadef"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// EntityFromProtoSpec is the resolved input to
// RenderEntityMigrationFromProto. The add command resolves identities
// (descriptor service, message, table name, flags); this renderer owns
// the mapping table above and the SQL text.
type EntityFromProtoSpec struct {
	// Table is the target table name (already pluralized/overridden).
	Table string
	// MessageFQ is the fully-qualified proto message the entity is born
	// from (recorded in the migration header for provenance).
	MessageFQ string
	// ProtoPkg is the service's own proto package — the boundary for
	// cross-package detection.
	ProtoPkg string
	// Fields is the message's descriptor schema, in declaration order.
	Fields []codegen.SchemaFieldDef
	// Enums maps fully-qualified enum names to their value names (the
	// CHECK-constraint vocabulary).
	Enums map[string][]string
	// SoftDelete adds the deleted_at column (set by the flag, the
	// `// forge:soft-delete` marker, or the message itself carrying a
	// deleted_at field).
	SoftDelete bool
	// Timestamps adds the managed created_at/updated_at columns.
	Timestamps bool
	// AppendOnly adds a DB guard (a trigger raising an exception) that
	// rejects every UPDATE/DELETE on the table — the storage half of the
	// `// forge:append-only` marker (the wire half omits Update/Delete
	// RPCs). An audit/ledger table: rows are immutable once written.
	AppendOnly bool
	// KnownTables is the set of table names an `<x>_id` FK may reference:
	// the applied schema UNION every table born in this run (descriptor
	// CRUD entities + `// forge:entity` messages). A `<x>_id` column whose
	// stem names no table in this set is polymorphic/auth
	// (user_id/actor_user_id/entity_id in a project with no such entity)
	// and gets NO foreign key — a REFERENCES to a table that doesn't exist
	// is worse than silence. Empty/nil suppresses every foreign key.
	KnownTables map[string]bool
	// ExistingTables are the tables that ALREADY EXIST at the moment this
	// migration runs — the applied schema plus everything born earlier in
	// the same sweep. It is what makes the emitted constraints applyable:
	// a REFERENCES may only name a table an EARLIER migration created.
	//
	// It also carries the reverse direction. A child born before its parent
	// could not constrain its own column; the parent's birth migration
	// back-fills that constraint, because the parent's migration is the
	// first point at which it can be applied. Between the two directions
	// every resolvable reference becomes a real constraint exactly once, in
	// whatever order the entities are born.
	//
	// Empty/nil means "nothing exists yet", which suppresses forward
	// constraints — the drift renderer runs this way.
	ExistingTables []ExistingTable
}

// ExistingTable is one already-created table, as seen by a migration being
// born after it.
type ExistingTable struct {
	// Name is the table name.
	Name string
	// UnconstrainedRefColumns are its TEXT `<x>_id` columns that carry no
	// FOREIGN KEY yet — the columns a later table's birth may still be
	// able to constrain, once it creates the table they point at.
	UnconstrainedRefColumns []string
}

// resolveFKTable resolves the table an `<x>_id` column references, given
// the set of tables the birth knows are real (KnownTables). It matches on
// the TRAILING entity name so a role-prefixed column resolves to the same
// entity as its bare form — `assigned_provider_id` and `provider_id` both
// resolve to the Provider entity's `providers` table, `parent_order_id`
// (a self/hierarchy role) to `orders`. The prefix is a role, not a
// distinct table. Progressively drops leading `_`-segments, longest
// (most specific) candidate first, and matches the pluralized stem
// against KnownTables (both sides go through naming.Pluralize, so a
// regularly-named entity's column and its table agree). Returns the real
// table name and true on a hit; ("", false) when the stem names no known
// table (auth/polymorphic columns — the FK is then omitted entirely).
func resolveFKTable(column string, known map[string]bool) (string, bool) {
	stem := strings.TrimSuffix(column, "_id")
	if stem == "" || len(known) == 0 {
		return "", false
	}
	parts := strings.Split(stem, "_")
	for i := 0; i < len(parts); i++ {
		if table := naming.Pluralize(strings.Join(parts[i:], "_")); known[table] {
			return table, true
		}
	}
	return "", false
}

// EntityFromProtoMigration is the rendered forward migration (forge writes
// no down migrations — it rolls forward only) plus the per-field notes
// (skips, TODOs) the CLI prints.
type EntityFromProtoMigration struct {
	UpSQL string
	Notes []string
	// PendingRefColumns are this table's `<x>_id` columns that resolve to a
	// known entity whose table does not exist YET, so no constraint could be
	// emitted here. The caller carries them forward as this table's
	// ExistingTable.UnconstrainedRefColumns, and the referenced table's own
	// birth migration back-fills them.
	PendingRefColumns []string
	// BackfilledRefColumns names, per earlier table, the reference columns
	// THIS migration constrained on its behalf. The caller strikes them off
	// that table's pending list so a third birth cannot propose the same
	// constraint twice.
	BackfilledRefColumns map[string][]string
}

// managedEntityColumns are the convention columns the migration itself
// owns; message fields with these names are skipped — the table gets the
// canonical PK and lifecycle columns instead.
var managedEntityColumns = map[string]bool{
	"id":         true,
	"created_at": true,
	"updated_at": true,
	"deleted_at": true,
}

// tableItem is one rendered line inside CREATE TABLE: a column
// definition (comma-joined) or a full-line comment (TODO carrier).
type tableItem struct {
	comment bool
	text    string
}

// ProtoSQLMapping is one row of the proto→column mapping this renderer
// applies: the proto field shape as an author writes it, the column it is
// born as, and the one-line reason where the column is not simply the
// scalar type.
type ProtoSQLMapping struct {
	// Proto is the field shape as declared in the message ("string",
	// "optional <scalar>", "repeated enum", "map<K, scalar>").
	Proto string
	// SQL is the column the birth migration emits for it.
	SQL string
	// Notes is the one-line qualification, or "" when there is none.
	Notes string
}

// protoSQLStructuralMappings are the mapping rows whose column is decided by
// a field's SHAPE rather than by its scalar kind — the branches of
// RenderEntityMigrationFromProto's field switch that scalarSQL never sees.
// Each row's SQL text is the literal one that branch emits.
var protoSQLStructuralMappings = []ProtoSQLMapping{
	{Proto: "optional <scalar>", SQL: "<type>", Notes: "nullable column, no default — proto3 presence is the column's nullability"},
	{Proto: "repeated <scalar>", SQL: "<type>[] NOT NULL DEFAULT '{}'", Notes: "native postgres array"},
	{Proto: "string <x>_id", SQL: "TEXT NOT NULL", Notes: "no zero default (an empty reference is a bug, not a value); a stem resolving to a known entity also gets an applied REFERENCES + index"},
	{Proto: "enum E (same package)", SQL: "TEXT NOT NULL DEFAULT '<first>' CHECK (col IN (...))", Notes: "value NAMES, minus a leading *_UNSPECIFIED zero sentinel; optional ⇒ nullable, NULL is \"unset\""},
	{Proto: "repeated enum E", SQL: "TEXT[] NOT NULL DEFAULT '{}'", Notes: "elements take the value names"},
	{Proto: "google.protobuf.Timestamp", SQL: "TIMESTAMPTZ", Notes: "nullable; the REPEATED form is refused — an array of instants is an event list, give it its own table. BUCKETING WARNING: two-argument `date_trunc('day', col)` truncates a TIMESTAMPTZ in the SESSION timezone, which the driver sets from the client host — so every bucketed total moves with the deploy host, silently. Pin the zone: `date_trunc('day', col, 'UTC')`. See `forge lint --time-bucketing` and the `db` skill"},
	{Proto: "nested message (same package)", SQL: "JSONB NOT NULL DEFAULT '{}'", Notes: "'[]' when repeated; plain JSONB when optional"},
	{Proto: "map<K, scalar>", SQL: "JSONB NOT NULL DEFAULT '{}'", Notes: "maps with message or enum VALUES are refused — the CRUD generator emits no conversion for them"},
	{Proto: "<field> // forge:generated <expr>", SQL: "<type> NOT NULL GENERATED ALWAYS AS (<expr>) STORED", Notes: "the expression is copied verbatim and applied to the shadow database before birth writes anything — postgres rejecting it fails the birth naming the marker's file and line; no DEFAULT; nullable when the field is optional (and for a Timestamp); an enum keeps its CHECK; single-valued scalar, enum and Timestamp fields only; the field is read-only (omitted from the born Create request)"},
}

// ProtoSQLMappings returns the full proto→column mapping the birth renderer
// applies, scalar rows first (iterated from the real scalarSQL function, so
// adding a kind there shows up here for free) then the structural rows.
//
// It exists so `forge project annotations` can DUMP the mapping rather than
// transcribe it. The scalar half is genuinely derived; the structural half is
// a literal projection of this file's own field switch, pinned by
// TestProtoSQLMappings_MatchTheRenderer.
//
// The kinds come from codegen.ProtoScalarKinds() — the key set of the one
// table forge writes the fifteen names down in — rather than from a copy
// kept here. A copy is a guard that cannot fail: a kind missing from it is
// simply never dumped, and a mapping absent from the dump reads exactly
// like a mapping that does not exist.
//
// They are GROUPED BY COLUMN rather than listed alphabetically, so the ten
// integer widths read as the one BIGINT family they are; alphabetical order
// scatters them through the float and text rows. The grouping is derived
// from each kind's own mapping, so it needs no second list to stay correct.
func ProtoSQLMappings() []ProtoSQLMapping {
	kinds := codegen.ProtoScalarKinds()
	if len(kinds) == 0 {
		panic("scaffold: codegen.ProtoScalarKinds() is empty — the scalar half of " +
			"the proto→SQL mapping dump derives from it, so an empty set would " +
			"silently publish a mapping with no scalar rows at all")
	}
	// Stable within a family (ProtoScalarKinds is sorted); families ordered
	// by first appearance so the dump's shape does not depend on the SQL
	// spellings sorting in a useful order.
	familyRank := map[string]int{}
	for _, k := range kinds {
		sqlType, _, ok := scalarSQL(k)
		if !ok {
			continue
		}
		if _, seen := familyRank[sqlType]; !seen {
			familyRank[sqlType] = len(familyRank)
		}
	}
	sort.SliceStable(kinds, func(i, j int) bool {
		si, _, _ := scalarSQL(kinds[i])
		sj, _, _ := scalarSQL(kinds[j])
		return familyRank[si] < familyRank[sj]
	})
	out := make([]ProtoSQLMapping, 0, len(kinds)+len(protoSQLStructuralMappings))
	for _, k := range kinds {
		sqlType, zero, ok := scalarSQL(k)
		if !ok {
			// scalarSQL is the renderer's own projection and the kinds
			// above are the closed proto scalar vocabulary, so a gap here
			// is a kind a birth would emit a TODO for rather than a
			// column. Refusing beats dumping a mapping that omits it.
			panic("scaffold: no column mapping for proto scalar kind " + k)
		}
		notes := ""
		if k == "bool" {
			notes = "DEFAULT FALSE is the proto3 zero, not a domain decision — the migration says so in a comment"
		}
		out = append(out, ProtoSQLMapping{
			Proto: k,
			SQL:   fmt.Sprintf("%s NOT NULL DEFAULT %s", sqlType, zero),
			Notes: notes,
		})
	}
	return append(out, protoSQLStructuralMappings...)
}

// scalarSQL maps a proto scalar kind to its column type and NOT NULL
// zero default; ok=false for non-scalars.
//
// It switches on the kind's GO type rather than on the kind, because the
// column is a function of the value's family and not of its wire encoding:
// all ten integer kinds share BIGINT, and both float kinds share DOUBLE
// PRECISION. Naming the ten spellings here was a second copy of the closed
// vocabulary — the shape that let kclTypeForProtoConfig name four of the
// twelve integer kinds and validate the other eight as text. Deriving means
// a kind cannot be missing from this projection: it is either a proto
// scalar, and lands on its family below, or it is not a scalar at all.
func scalarSQL(kind string) (sqlType, zero string, ok bool) {
	goType, ok := codegen.ProtoScalarGoType(kind)
	if !ok {
		return "", "", false
	}
	switch goType {
	case "string":
		return "TEXT", "''", true
	case "bool":
		return "BOOLEAN", "FALSE", true
	case "int32", "uint32", "int64", "uint64":
		return "BIGINT", "0", true
	case "float32", "float64":
		return "DOUBLE PRECISION", "0", true
	case "[]byte":
		return "BYTEA", `'\x'`, true
	}
	// A proto scalar whose Go type names no family above: a kind was added
	// to the vocabulary and this projection was not extended. Reporting it
	// keeps the birth renderer's TODO path honest rather than inventing a
	// column.
	return "", "", false
}

// quoteSQLList renders enum value names as a quoted, comma-separated
// SQL list for a CHECK (col IN (...)) constraint.
func quoteSQLList(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = "'" + strings.ReplaceAll(v, "'", "''") + "'"
	}
	return strings.Join(quoted, ", ")
}

// bornEnumVocabulary is the set of values an enum column's CHECK admits:
// every declared member except a LEADING proto zero sentinel.
//
// The sentinel is a WIRE concept — proto3 gives a plain enum field no
// presence, so a field left unset and a field set to the zero are
// byte-identical on the wire, and `*_UNSPECIFIED` can only mean "the
// caller did not set this field". That is a fact about a REQUEST. It is
// never a fact about a stored row, and a column that admits it admits a
// value the domain does not have, which every reader then has to handle.
// Real-workflow run 1 is the evidence — all four born migrations were
// hand-edited off the sentinel, four times out of four, by agents who
// were not asked to.
//
// forge treats the sentinel as not-a-value everywhere else it touches
// one: the seeder refuses to draw it (seedplan.SeedEnumChoices), the born
// CRUD fixtures refuse to use it (codegen.enumFixture), the scaffolded
// create form refuses to submit it, and the generated write path stores
// the column DEFAULT instead of it (codegen.assignToDB). A CHECK that
// admitted it was forge contradicting itself in the same statement as the
// comment that says so.
//
// Two boundaries the rule does NOT cross:
//
//   - Only the member at NUMBER ZERO carries the no-presence meaning, so
//     only the leading member is dropped. A member named `*_UNKNOWN`
//     declared at 3 is somebody's real "we asked and nobody knew"; the
//     seeder's name filter is right to skip drawing it and this would be
//     wrong to delete it.
//   - An enum whose zero member is NOT named as a sentinel has no
//     spelling for "unset" at all, and an enum that is ONLY its sentinel
//     has no real member to fall back to. Both keep the full vocabulary —
//     `CHECK (col IN ())` is not applyable SQL, and deleting a domain
//     value is worse than admitting one. seedplan.SeedEnumChoices decides
//     which member is the sentinel (and carries the same fallback), so
//     there is ONE definition of that in the codebase.
func bornEnumVocabulary(values []string) []string {
	if len(values) == 0 || seedplan.SeedEnumChoices(values)[0] == values[0] {
		return values
	}
	return values[1:]
}

// bornEnumDefault picks the value a NOT NULL enum column starts at: the
// first value its CHECK admits. It is the value an INSERT that omits the
// column gets — and, because proto3 cannot distinguish unset from zero,
// the value the generated write path stores for an unset enum field.
func bornEnumDefault(values []string) string {
	return bornEnumVocabulary(values)[0]
}

// shortEnumName renders a fully-qualified proto enum name as its bare
// type name, for a comment that has to read like prose.
func shortEnumName(fq string) string {
	if i := strings.LastIndex(fq, "."); i >= 0 {
		return fq[i+1:]
	}
	return fq
}

// emitEnumColumn emits the column (and any explanatory comments) for one enum
// field, or records a TODO when the enum has no mechanical mapping.
//
// The vocabulary a row may hold is not always the full proto value list: when
// bornEnumVocabulary drops the zero value as an "unset" sentinel, that
// sentinel is never admitted by the CHECK, and the emitted comments say so —
// the column's spelling for unset is NULL (optional) or the first real member
// (DEFAULT), not the proto zero.
func emitEnumColumn(
	f codegen.SchemaFieldDef,
	spec EntityFromProtoSpec,
	samePackage func(string) bool,
	col func(string, ...any),
	comment func(string, ...any),
	todo func(codegen.SchemaFieldDef, string),
) {
	if !samePackage(f.TypeName) {
		todo(f, fmt.Sprintf("cross-package enum %s", f.TypeName))
		return
	}
	values, ok := spec.Enums[f.TypeName]
	if !ok || len(values) == 0 {
		todo(f, fmt.Sprintf("enum %s has no values in the descriptor (stale? run `forge generate`)", f.TypeName))
		return
	}
	vocab := bornEnumVocabulary(values)
	sentinel := ""
	if len(vocab) != len(values) {
		sentinel = values[0]
	}
	switch {
	case f.Repeated:
		comment("-- %s: elements take the %s value names (%s)", f.Name, f.TypeName, strings.Join(values, " | "))
		col("%s TEXT[] NOT NULL DEFAULT '{}'", f.Name)
	case f.Optional:
		// A CHECK (col IN (...)) passes NULL rows by SQL three-valued logic,
		// so nullable needs no special form — and NULL is already this
		// column's spelling for "unset", which is the only thing the proto
		// zero could have meant.
		if sentinel != "" {
			comment("-- %s: NULL is \"unset\". The proto zero (%s) means the same", f.Name, sentinel)
			comment("--   thing on the wire, so it is stored as NULL rather than admitted here.")
		}
		col("%s TEXT CHECK (%s IN (%s))", f.Name, f.Name, quoteSQLList(vocab))
	default:
		if sentinel != "" {
			comment("-- %s: the proto zero (%s) means \"unset\", never a state a", f.Name, sentinel)
			comment("--   row is in — so it is not admitted, and DEFAULT is the first real")
			comment("--   %s member. A create that leaves the field unset stores that DEFAULT.", shortEnumName(f.TypeName))
		}
		col("%s TEXT NOT NULL DEFAULT '%s' CHECK (%s IN (%s))", f.Name, bornEnumDefault(values), f.Name, quoteSQLList(vocab))
	}
}

// GeneratedColumnRefusal returns why a `// forge:generated` field cannot be
// born as a GENERATED column, or "" when it can.
//
// The supported set is every field birth gives ONE single-valued column with
// a mechanical conversion: a scalar (including a `<x>_id` reference), a
// same-package enum with known values, and a singular Timestamp. Repeated,
// map and nested-message fields are JSONB or array columns whose generated
// form nobody has asked for; refusing them keeps the marker's meaning one
// sentence long, and the owned migration takes any hand-written generated
// column after birth. A field birth carries as a TODO has no column to
// generate at all, and a managed column (id, created_at, …) is the
// migration's own.
//
// Exported so the CLI refuses the birth BEFORE writing anything, naming the
// marker's file and line; the renderer applies the same rule as a TODO.
func GeneratedColumnRefusal(f codegen.SchemaFieldDef, spec EntityFromProtoSpec) string {
	switch {
	case managedEntityColumns[f.Name]:
		return fmt.Sprintf("cannot apply to %s — the migration provides that managed column itself", f.Name)
	case f.Oneof != "":
		return fmt.Sprintf("cannot apply to a member of oneof %q — oneof members get no column", f.Oneof)
	case f.Kind == "map", f.Repeated:
		return "applies to single-valued fields only — a repeated or map field is an array/JSONB column; write its GENERATED form in the migration by hand"
	case f.Kind == "enum":
		if !strings.HasPrefix(f.TypeName, spec.ProtoPkg+".") {
			return fmt.Sprintf("cannot apply to cross-package enum %s — it gets no column", f.TypeName)
		}
		if len(spec.Enums[f.TypeName]) == 0 {
			return fmt.Sprintf("cannot apply to enum %s — it has no values in the descriptor (stale? run `forge generate`)", f.TypeName)
		}
		return ""
	case f.Kind == "message":
		if f.TypeName == "google.protobuf.Timestamp" {
			return ""
		}
		return fmt.Sprintf("cannot apply to a %s field — only scalar, enum and Timestamp fields are born as GENERATED columns", f.TypeName)
	}
	if _, _, ok := scalarSQL(f.Kind); !ok {
		return fmt.Sprintf("cannot apply to a field of kind %q — it gets no column", f.Kind)
	}
	return ""
}

// emitGeneratedColumn emits the column for one `// forge:generated <expr>`
// field GeneratedColumnRefusal accepted, and reports whether it is a
// `<x>_id` reference that joins the foreign-key plan.
//
// The column is the type the field would be born with anyway, minus the
// DEFAULT (postgres refuses one on a generated column, and the expression is
// the value), with GENERATED ALWAYS AS (<expr>) STORED in its place.
//
// NOT NULL follows the rule every other born column follows — proto3
// presence IS the column's nullability — so a plain field is NOT NULL and an
// `optional` one is nullable, as is a Timestamp, whose message type always
// carries presence. The consequence is deliberate: an expression that
// yields NULL for some row (it reads a nullable column, divides by a NULL)
// fails that INSERT loudly instead of storing a NULL the non-optional wire
// field cannot represent. Mark the field `optional` when NULL is a real
// answer.
//
// protovalidate rules still project to CHECKs (postgres checks a generated
// value like any other), and an enum keeps its vocabulary CHECK, so the
// expression cannot produce a value the domain does not have.
func emitGeneratedColumn(
	f codegen.SchemaFieldDef,
	spec EntityFromProtoSpec,
	col func(string, ...any),
	comment func(string, ...any),
) (isRef bool) {
	notNull := " NOT NULL"
	if f.Optional || f.Kind == "message" {
		notNull = ""
	}
	generated := fmt.Sprintf(" GENERATED ALWAYS AS (%s) STORED", f.Generated)
	comment("-- %s: forge:generated — postgres computes it; no INSERT or UPDATE may write it.", f.Name)
	switch f.Kind {
	case "enum":
		vocab := bornEnumVocabulary(spec.Enums[f.TypeName])
		col("%s TEXT%s%s CHECK (%s IN (%s))", f.Name, notNull, generated, f.Name, quoteSQLList(vocab))
		return false
	case "message": // google.protobuf.Timestamp — GeneratedColumnRefusal admits no other
		col("%s TIMESTAMPTZ%s", f.Name, generated)
		return false
	}
	sqlType, _, _ := scalarSQL(f.Kind)
	checkSuffix := ""
	if f.Validate.HasAny() {
		if cs := f.Validate.SQLChecks(f.Name, f.Kind); len(cs) > 0 {
			checkSuffix = " " + strings.Join(cs, " ")
		}
	}
	col("%s %s%s%s%s", f.Name, sqlType, notNull, generated, checkSuffix)
	return f.Kind == "string" && strings.HasSuffix(f.Name, "_id")
}

// RenderEntityMigrationFromProto renders the create-table migration pair
// for one already-authored proto message, per the mapping table in the
// file header. Pure: no filesystem, no descriptor loading — the CLI owns
// resolution and file placement.
func RenderEntityMigrationFromProto(spec EntityFromProtoSpec) EntityFromProtoMigration { //nolint:funlen // one straight-line statement per emitted SQL clause; the statement count IS the column/constraint count.
	var (
		items []tableItem
		notes []string
		fks   []string // *_id columns collecting the applied-FK block
	)
	col := func(format string, args ...any) {
		items = append(items, tableItem{text: fmt.Sprintf(format, args...)})
	}
	todo := func(field codegen.SchemaFieldDef, reason string) {
		items = append(items, tableItem{comment: true,
			text: fmt.Sprintf("-- TODO: proto field %q skipped — %s; add a column by hand if it should be stored.", field.Name, reason)})
		notes = append(notes, fmt.Sprintf("field %s: %s — carried as a TODO comment in the migration", field.Name, reason))
	}
	comment := func(format string, args ...any) {
		items = append(items, tableItem{comment: true, text: fmt.Sprintf(format, args...)})
	}
	samePackage := func(fq string) bool {
		return strings.HasPrefix(fq, spec.ProtoPkg+".")
	}

	// String PK with the empty-id guard — identical to the field-list
	// form (empty-id rows were the silent-upsert data-loss vector).
	col("id TEXT PRIMARY KEY CHECK (id <> '')")

	for _, f := range spec.Fields {
		if managedEntityColumns[f.Name] {
			notes = append(notes, fmt.Sprintf("field %s: managed by convention — the migration provides it; skipped from the mapped columns", f.Name))
			continue
		}

		if f.Generated != "" {
			// The CLI refuses an unsupported generated field before it ever
			// renders; this TODO is the renderer's own refusal to emit a
			// plain column in silence should a caller skip that check.
			if reason := GeneratedColumnRefusal(f, spec); reason != "" {
				todo(f, "`forge:generated` "+reason)
				continue
			}
			if emitGeneratedColumn(f, spec, col, comment) {
				fks = append(fks, f.Name)
			}
			continue
		}

		switch {
		case f.Oneof != "":
			todo(f, fmt.Sprintf("member of oneof %q (proto oneofs have no mechanical column mapping)", f.Oneof))

		case f.Kind == "map":
			// Only a map of SCALARS gets a column. A map of messages or of
			// enums has no conversion the CRUD generator will emit — Go's
			// JSON encoder would store protobuf struct internals for the
			// first and enum wire NUMBERS for the second, where every other
			// enum column in the app stores value names. Birthing a column
			// the generator then refuses is how a field ends up dead over
			// the API with everything reporting green, so the refusal
			// happens HERE, at the one moment the proto is still being
			// written.
			if k := f.MapValueKind; k == "message" || k == "enum" || k == "" {
				todo(f, fmt.Sprintf("map with %s values has no mechanical column mapping (only maps of scalars do)", mapValueLabel(f)))
				break
			}
			col("%s JSONB NOT NULL DEFAULT '{}'", f.Name)

		case f.Kind == "enum":
			emitEnumColumn(f, spec, samePackage, col, comment, todo)

		case f.Kind == "message":
			switch {
			case f.TypeName == "google.protobuf.Timestamp" && !f.Repeated:
				// Nullable: a Timestamp message field has presence on the
				// wire, and the column's nullability is how that is stored.
				col("%s TIMESTAMPTZ", f.Name)

			case f.TypeName == "google.protobuf.Timestamp":
				// `repeated Timestamp` was the ONE well-known type birth
				// gave a column (TIMESTAMPTZ[]) while every other
				// google.protobuf.* — repeated or not — was carried as a
				// TODO. The conversion generator refused that column, so
				// birth was creating a field that is dead over the API.
				//
				// It stays refused rather than becoming supported, because
				// the two sides cannot represent each other: the wire shape
				// is []*timestamppb.Timestamp, whose elements may be nil,
				// and a NULL element in a TIMESTAMPTZ[] makes the whole ROW
				// unscannable ("bun: can't parse time=''") rather than
				// producing a zero instant. Nor is the column the shape to
				// reach for: an array of instants is a denormalized event
				// list whose every query needs unnest, and the normalized
				// form is a child table forge already generates well.
				todo(f, "an array of timestamps has no column mapping — a repeated instant is an event list; "+
					"give it its own table with a foreign key back to this one")
			case strings.HasPrefix(f.TypeName, "google.protobuf."):
				todo(f, fmt.Sprintf("well-known type %s has no mechanical column mapping", f.TypeName))
			case !samePackage(f.TypeName):
				todo(f, fmt.Sprintf("cross-package message %s", f.TypeName))
			case f.Repeated:
				col("%s JSONB NOT NULL DEFAULT '[]'", f.Name)
			case f.Optional:
				col("%s JSONB", f.Name)
			default:
				col("%s JSONB NOT NULL DEFAULT '{}'", f.Name)
			}

		default: // scalar
			sqlType, zero, ok := scalarSQL(f.Kind)
			if !ok {
				todo(f, fmt.Sprintf("unsupported field kind %q", f.Kind))
				break
			}
			// protovalidate (buf.validate.field) rules project to DB CHECK
			// constraints appended to the column, and a lower-bound/non-empty
			// rule suppresses the zero default (its zero value would violate
			// the CHECK just emitted — mirrors the *_id reference treatment).
			// Arrays are out of scope for the scalar projection.
			checkSuffix := ""
			suppressDefault := false
			if !f.Repeated && f.Validate.HasAny() {
				if cs := f.Validate.SQLChecks(f.Name, f.Kind); len(cs) > 0 {
					checkSuffix = " " + strings.Join(cs, " ")
				}
				suppressDefault = f.Validate.SuppressesZeroDefault(f.Kind)
			}
			switch {
			case f.Repeated:
				col("%s %s[] NOT NULL DEFAULT '{}'", f.Name, sqlType)
			case f.Kind == "string" && f.Name != "id" && strings.HasSuffix(f.Name, "_id"):
				// Reference-looking column: plain TEXT (no zero default —
				// an empty-string reference is a bug, not a value). An
				// applied FOREIGN KEY + index is emitted at the bottom when
				// the stem resolves to a known table (see planForeignKeys).
				if f.Optional {
					col("%s TEXT%s", f.Name, checkSuffix)
				} else {
					col("%s TEXT NOT NULL%s", f.Name, checkSuffix)
				}
				fks = append(fks, f.Name)
			case f.Optional:
				col("%s %s%s", f.Name, sqlType, checkSuffix)
			case suppressDefault:
				col("%s %s NOT NULL%s", f.Name, sqlType, checkSuffix)
			default:
				if f.Kind == "bool" {
					// FALSE is the proto3 zero, and unlike an enum's
					// *_UNSPECIFIED it IS a valid row state — so the
					// default stands, unlike bornEnumDefault's. But a
					// SAFETY flag (rx_required, requires_approval,
					// is_locked) is then born in the permissive
					// direction, and which direction is safe is a
					// DOMAIN fact forge cannot read off a descriptor.
					// Real-workflow evidence: an agent hand-edited such
					// a column to TRUE right after birth. So: say where
					// the value came from and let the author decide.
					comment("-- %s: DEFAULT FALSE is the proto3 zero, not a domain decision.", f.Name)
					comment("--   Change it to TRUE if the SAFE state for this flag is on.")
				}
				col("%s %s NOT NULL DEFAULT %s%s", f.Name, sqlType, zero, checkSuffix)
			}
		}
	}

	if spec.Timestamps {
		col("created_at TIMESTAMPTZ NOT NULL DEFAULT (now())")
		col("updated_at TIMESTAMPTZ NOT NULL DEFAULT (now())")
	}
	if spec.SoftDelete {
		col("deleted_at TIMESTAMPTZ")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "-- Born from proto message %s (`forge scaffold entity --from-proto`).\n", spec.MessageFQ)
	b.WriteString("-- This migration is YOURS from birth: forge never re-reads the proto to\n")
	b.WriteString("-- regenerate or edit it. Evolution is a NEW migration plus a proto edit —\n")
	b.WriteString("-- the schema truth and the wire truth evolve on independent clocks.\n")
	fmt.Fprintf(&b, "CREATE TABLE %s (\n", spec.Table)
	// Column lines take a trailing comma when another COLUMN follows;
	// comment lines carry no comma and never affect the comma placement.
	lastCol := -1
	for i, it := range items {
		if !it.comment {
			lastCol = i
		}
	}
	for i, it := range items {
		if it.comment {
			b.WriteString("    " + it.text + "\n")
			continue
		}
		suffix := ","
		if i == lastCol {
			suffix = ""
		}
		b.WriteString("    " + it.text + suffix + "\n")
	}
	b.WriteString(");\n")

	// forge:append-only — the STORAGE half of the marker (the wire half omits
	// the Update/Delete RPCs): a trigger that rejects every UPDATE/DELETE so
	// no bug or compromised caller can rewrite or erase a ledger row. LOUD
	// (raises an exception) rather than a `DO INSTEAD NOTHING` rule that would
	// silently swallow the write and report success.
	if spec.AppendOnly {
		fmt.Fprintf(&b, "\n-- forge:append-only — %s is an immutable ledger: UPDATE and DELETE are\n", spec.Table)
		b.WriteString("-- rejected at the database (the generated API carries no Update/Delete RPC\n")
		b.WriteString("-- either). This guard is defense in depth.\n")

		// DECLARE it in the catalog as well as enforcing it. The trigger
		// defends the rows; this comment is what every later pass READS —
		// the proto marker is a birth-time instruction that nothing carries
		// forward, so without this the applied schema looks ordinary and
		// the generated store happily offers UpdateX/DeleteX for a table
		// postgres refuses every write to.
		fmt.Fprintf(&b, "COMMENT ON TABLE %s IS '%s — rows are inserted and read, never rewritten or erased.';\n",
			spec.Table, schemadef.TableMarkerAppendOnly)

		fmt.Fprintf(&b, "CREATE OR REPLACE FUNCTION %s_forbid_mutation() RETURNS trigger\n", spec.Table)
		b.WriteString("    LANGUAGE plpgsql AS $$\n")
		b.WriteString("BEGIN\n")
		fmt.Fprintf(&b, "    RAISE EXCEPTION 'table %s is append-only: %% is not permitted', TG_OP;\n", spec.Table)
		b.WriteString("END;\n")
		b.WriteString("$$;\n")
		fmt.Fprintf(&b, "CREATE TRIGGER %s_append_only\n", spec.Table)
		fmt.Fprintf(&b, "    BEFORE UPDATE OR DELETE ON %s\n", spec.Table)
		fmt.Fprintf(&b, "    FOR EACH ROW EXECUTE FUNCTION %s_forbid_mutation();\n", spec.Table)
	}

	// Foreign keys + indexes for the `*_id` columns — but only for columns
	// whose stem resolves to a table the birth knows is real (an entity
	// being born or an applied table). A REFERENCES to a naively-pluralized
	// table that doesn't exist (`assigned_provider_id` →
	// `assigned_providers`, `user_id` → `users` with no User entity) is a
	// worse defect than an absent constraint. Auth/polymorphic columns get
	// silence.
	//
	// These are APPLIED, not commented. A constraint forge writes commented
	// out is forge stating the correct schema and then declining to build
	// it: the database ships with no referential integrity, and — because
	// the seeder reads the FK graph by introspecting the live database —
	// every seeded child row gets a synthesized string in its parent column
	// instead of a real parent id. Dangling-by-default is not a safer
	// default than a constraint the author can delete from a migration they
	// own from birth.
	fkPlan := planForeignKeys(spec, fks)
	if len(fkPlan.forward) > 0 {
		b.WriteString("\n-- Foreign keys + indexes for the *_id columns above. The referenced\n")
		b.WriteString("-- tables were resolved from the entities forge knows; a column whose\n")
		b.WriteString("-- stem names no entity is left unconstrained on purpose.\n")
		for _, r := range fkPlan.forward {
			writeFKStatements(&b, spec.Table, r.col, r.table)
		}
	}
	if len(fkPlan.pending) > 0 {
		b.WriteString("\n-- Indexes for the *_id columns whose referenced table does not exist\n")
		b.WriteString("-- yet. The FOREIGN KEY is added by that table's own birth migration,\n")
		b.WriteString("-- which is the first migration in which it can be applied:\n")
		for _, r := range fkPlan.pending {
			fmt.Fprintf(&b, "CREATE INDEX %s_%s_idx ON %s (%s);\n", spec.Table, r.col, spec.Table, r.col)
		}
	}
	if len(fkPlan.backfill) > 0 {
		fmt.Fprintf(&b, "\n-- These tables were born before %s existed, so they could not constrain\n", spec.Table)
		b.WriteString("-- their reference to it. This is the first migration that can:\n")
		for _, r := range fkPlan.backfill {
			fmt.Fprintf(&b, "ALTER TABLE %s ADD CONSTRAINT %s_%s_fkey FOREIGN KEY (%s) REFERENCES %s (id);\n",
				r.child, r.child, r.col, r.col, spec.Table)
		}
	}

	return EntityFromProtoMigration{
		UpSQL:                b.String(),
		Notes:                notes,
		PendingRefColumns:    fkPlan.pendingColumns(),
		BackfilledRefColumns: fkPlan.backfilledColumns(),
	}
}

// fkRef is one reference this table makes: its own `<x>_id` column and the
// table that column points at.
type fkRef struct{ col, table string }

// fkBackfill is one reference an EARLIER table makes to the table being
// born now — a constraint that could not be written at that table's birth.
type fkBackfill struct{ child, col string }

// fkPlan splits a table's references three ways by what the database can
// actually accept in THIS migration.
type fkPlan struct {
	// forward: the referenced table already exists (or is this table
	// itself, created a few lines above) — constrain it here.
	forward []fkRef
	// pending: the referenced table is a known entity that has not been
	// created yet. Nothing can be constrained here; the referenced table's
	// own birth migration back-fills it.
	pending []fkRef
	// backfill: references made TO this table by tables born before it.
	backfill []fkBackfill
}

func (p fkPlan) pendingColumns() []string {
	if len(p.pending) == 0 {
		return nil
	}
	out := make([]string, 0, len(p.pending))
	for _, r := range p.pending {
		out = append(out, r.col)
	}
	return out
}

func (p fkPlan) backfilledColumns() map[string][]string {
	if len(p.backfill) == 0 {
		return nil
	}
	out := map[string][]string{}
	for _, r := range p.backfill {
		out[r.child] = append(out[r.child], r.col)
	}
	return out
}

// planForeignKeys resolves every `<x>_id` column both ways: the references
// this table makes, and the references already-born tables make to it.
func planForeignKeys(spec EntityFromProtoSpec, fks []string) fkPlan {
	exists := make(map[string]bool, len(spec.ExistingTables))
	for _, t := range spec.ExistingTables {
		exists[t.Name] = true
	}

	var plan fkPlan
	for _, name := range fks {
		table, ok := resolveFKTable(name, spec.KnownTables)
		if !ok {
			continue
		}
		// A self-reference names the table this migration just created, so
		// it is satisfiable here even though nothing "existed" before.
		if table == spec.Table || exists[table] {
			plan.forward = append(plan.forward, fkRef{col: name, table: table})
			continue
		}
		plan.pending = append(plan.pending, fkRef{col: name, table: table})
	}

	for _, t := range spec.ExistingTables {
		if t.Name == spec.Table {
			continue
		}
		for _, col := range t.UnconstrainedRefColumns {
			if ref, ok := resolveFKTable(col, spec.KnownTables); ok && ref == spec.Table {
				plan.backfill = append(plan.backfill, fkBackfill{child: t.Name, col: col})
			}
		}
	}
	return plan
}

// writeFKStatements emits the constraint and the index every join and
// filter on a reference column needs.
func writeFKStatements(b *strings.Builder, table, col, refTable string) {
	fmt.Fprintf(b, "ALTER TABLE %s ADD CONSTRAINT %s_%s_fkey FOREIGN KEY (%s) REFERENCES %s (id);\n",
		table, table, col, col, refTable)
	fmt.Fprintf(b, "CREATE INDEX %s_%s_idx ON %s (%s);\n", table, col, table, col)
}

// mapValueLabel names a map field's value type for the TODO comment: the
// message/enum's fully-qualified name when it has one, the bare kind
// otherwise.
func mapValueLabel(f codegen.SchemaFieldDef) string {
	if f.MapValueTypeName != "" {
		return f.MapValueTypeName
	}
	if f.MapValueKind == "" {
		return "unknown"
	}
	return f.MapValueKind
}
