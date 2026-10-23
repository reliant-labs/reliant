// Package schemadef projects a project's APPLIED database schema —
// db/migrations/*.up.sql executed in order against a REAL ephemeral
// postgres — into a typed model that code generation consumes.
//
// SQL is the schema language in forge: migrations are the single source
// of truth for what tables and columns exist. Everything else (entity
// structs, the ORM, CRUD wiring, frontend gating) is a projection of
// the schema this package reports.
//
// # Shadow strategy
//
// forge is postgres-pinned. Migrations are applied verbatim — byte for
// byte, no rewriting — to a real ephemeral postgres (pkg/pgtest), then the
// resulting schema is read back through postgres's own catalog
// (information_schema / pg_catalog). WHICH server hosts that shadow is not
// schemadef's decision: the codegen/env layer (internal/shadowdb) resolves
// a reachable configured server and passes it to ApplyAndIntrospectAt,
// falling back to pgtest's embedded default only when none is available.
//
// A caller that ALREADY has a migrated database skips the shadow entirely
// and calls Introspect on its own handle. Both entries return the same
// model; they differ only in who applied the migrations.
//
// A caller with several readers of one schema applies it once instead:
// ReadMigrationSet names the files by content digest, and
// MigrationSet.ApplyTemplateAt builds a Template whose Tables serve the
// model and whose Open hands out private copies of the applied database
// (template.go). internal/shadowdb uses it to make one `forge generate`
// apply each migration set once.
//
// # Why this ships in pkg
//
// The model this package produces is the input to forge's seed planner
// (pkg/seedplan), and that planner has to be callable from a user's test —
// a flow test seeds a foreign-key spine into its own database by calling a
// Go function, not by shelling the forge binary or importing SQL that was
// frozen at the last `forge generate`. A package the generator alone can
// reach cannot do that, so schema introspection and seed planning both live
// in the shipped module. Nothing here is server code: it reads catalogs and
// builds structs.
//
// This is a hard improvement over the previous in-memory SQLite shadow,
// which approximated postgres by exploiting SQLite's permissive type
// affinity and required a normalization pass (DEFAULT (now()) wrapping,
// '::type' cast stripping, multi-ADD splitting) to coax idiomatic
// postgres DDL through SQLite's parser. That approximation broke the
// moment a project used a construct SQLite couldn't parse — most
// notably schema-qualified DDL (CREATE TABLE controlplane.foo), which
// froze cp-forge's ORM. Real postgres needs none of that: it IS the
// target, so the schema the generator sees is exactly the schema
// production runs.
//
// Statements that fail to apply are still skipped when they cannot
// affect the table/column model (DML data movement, CREATE FUNCTION /
// TRIGGER / EXTENSION / VIEW, COMMENT, SET ...) — postgres rejects some
// of these in the bare ephemeral DB (e.g. an extension that isn't
// installed) and that must not abort introspection. A failing CREATE
// TABLE / ALTER TABLE / DROP TABLE / CREATE INDEX is a hard error: those
// define the schema being projected, so silently skipping one would
// generate an ORM that lies.
package schemadef

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// Table is one introspected table of the applied schema.
type Table struct {
	Name string
	// Schema is the postgres schema the table lives in ("public",
	// "controlplane", …). Tables are still keyed by Name; Schema exists so SQL
	// forge emits against the shadow can address the table when its schema is
	// not on the connection's search_path.
	Schema  string
	Columns []Column
	// Comment is the table's catalog comment (COMMENT ON TABLE), "" when
	// none. It is where a TABLE-level `forge:*` declaration lives, the twin
	// of Column.Comment one level up.
	Comment string
	// Triggers lists the table's trigger names, sorted. It exists because a
	// declaration forge started writing at some release is absent from
	// every table born before it, and a table's ENFORCEMENT outlives the
	// release that wrote it: AppendOnly reads the guard trigger as a
	// second, older declaration of the same fact. See appendOnlyGuardTrigger.
	Triggers []string
	// PKCols lists primary-key column names in key order.
	PKCols []string
	// Indexes lists non-PK indexes (unique and plain).
	Indexes []Index
	// ForeignKeys lists declared REFERENCES constraints.
	ForeignKeys []ForeignKey
	// IsView marks a VIEW (IntrospectViews), not a BASE TABLE. A view has
	// columns and a comment and nothing else: no key, no constraints, and
	// every column nullable as far as the catalog can tell.
	IsView bool
	// Checks lists the table's CHECK constraints, read back through
	// postgres's own catalog (pg_get_constraintdef). Because both the
	// applied migration dir and any freshly-projected DDL are introspected
	// through the SAME catalog, two semantically-identical CHECKs — however
	// they were spelled in the source SQL (`col IN (...)` vs the postgres
	// `col = ANY (ARRAY[...])` normal form) — read back byte-identical here.
	// That is what lets a drift comparison diff CHECK vocabulary reliably
	// without re-implementing postgres's expression normalizer.
	Checks []CheckConstraint
}

// Column is one introspected column.
type Column struct {
	Name string
	// DeclType is the declared SQL type verbatim from the migration
	// (e.g. "TIMESTAMPTZ", "TEXT[]", "DOUBLE PRECISION").
	DeclType string
	// Type is the canonical forge type the declared type maps to.
	Type CanonicalType
	// IsArray is true for declared array types ("TEXT[]"); Type then
	// holds the ELEMENT type.
	IsArray bool
	// TypeKnown is false when DeclType matched no arm of the mapping
	// table — MONEY, INET, INTERVAL, TSVECTOR, a native pg enum. Type is
	// then TypeString as a fallback, NOT as a mapping, and anything that
	// projects a wire contract from this column must say so rather than
	// publish a `string` field postgres will reject writes to.
	TypeKnown bool
	// MaxChars is the declared character cap of a varchar(n)/char(n) column,
	// 0 when the type is unbounded. DeclType cannot carry it: it is rebuilt
	// from udt_name, which spells both `varchar` and `varchar(4)` as
	// "VARCHAR". Anything that must not overflow the column (seed synthesis,
	// generated test fixtures) reads this.
	MaxChars int
	NotNull  bool
	// Default is the raw default expression, "" when none.
	Default string
	IsPK    bool
	// IsGenerated is true for GENERATED ALWAYS AS (...) STORED columns —
	// the database computes them, so they must never be written on
	// INSERT/UPDATE (postgres rejects writes to generated columns).
	IsGenerated bool
	// IsIdentity is true for a GENERATED { ALWAYS | BY DEFAULT } AS IDENTITY
	// column. The catalog reports no column_default for one (its value comes
	// from an implicit sequence), so Default alone cannot say the database
	// supplies it; see DatabaseSupplied.
	IsIdentity bool
	// Comment is the column's catalog comment (COMMENT ON COLUMN), "" when
	// none. It carries the `forge:*` column declarations the same way a
	// constraint comment carries `forge:ref` — plain SQL, applied by the
	// migration, landing in the postgres catalog where forge, `psql \d+` and
	// anything else can read it. Only the `forge:` clause is interpreted;
	// prose in the same comment is fine.
	Comment string
}

// ColumnMarkerImmutable declares that a column must not be written by a
// full-replace UPDATE. It projects to Bun's `skipupdate` tag option.
const ColumnMarkerImmutable = "forge:immutable"

// ColumnMarkerRef declares which side of a two-paths-to-one-parent
// reference is authoritative (values: authoritative, derived-from=<col>,
// independent). Unlike ColumnMarkerImmutable it is declared on the
// FOREIGN KEY constraint, not the column — COMMENT ON CONSTRAINT, not
// COMMENT ON COLUMN — but it shares the same `forge:*` catalog-comment
// vocabulary, so it lives in this registry alongside it. Consumed by
// `forge db seed`'s diamond resolution; see pkg/seedplan/diamond.go
// for the full grammar and pkg/seedplan's `db/seeding` skill for the
// authoritative docs.
const ColumnMarkerRef = "forge:ref"

// ColumnMarkerClosedSet declares that a CHECK constraint's value list
// mirrors a closed Go set: `COMMENT ON CONSTRAINT access_tokens_scopes ON
// access_tokens IS 'forge:closed-set accesstoken.AllScopes'`. Like
// ColumnMarkerRef it is a constraint comment — on the CHECK, not the column.
// `forge lint`'s closed-sets lane compares the list the migrations last
// defined with the named set and prints the migration that reconciles them
// (internal/closedset).
const ColumnMarkerClosedSet = "forge:closed-set"

// ColumnMarkerVersion opts a column into optimistic concurrency control:
// pkg/crud's generic Repo requires Update/UpdateMasked's WHERE clause to
// still match the caller's last-read value and increments it atomically
// on a successful write. A write that loses the race — the value moved
// under a concurrent writer between that writer's read and this one's —
// fails with svcerr.ErrAborted (Connect code Aborted, "re-read and
// retry") instead of silently clobbering the other writer's change or
// reporting the row missing.
//
// Unlike forge:immutable this column is NEVER caller-settable, not even
// via an explicit update_mask path: the repo owns the increment, and a
// client-supplied value would defeat the guarantee the marker exists to
// give. See forge/pkg/crud's meta.versionColumn for the write-path detail
// and internal/generator's bunTag for how the marker also becomes Bun's
// `,skipupdate` — defense in depth against a hand-rolled full-struct Bun
// query that bypasses the generic Repo entirely.
const ColumnMarkerVersion = "forge:version"

// ColumnMarkerFill declares WHO fills a `forge:read-only` column that the
// generated Create/Update requests never carry a value for — the gap the
// unsatisfiable-column lint (internal/cli/lint/lint_unsatisfiable_columns.go)
// exists to close. Takes exactly one of two argument values, spelled
// `forge:fill=<strategy>`:
//
//	forge:fill=ulid     forge generates a ULID for this column at Create,
//	                    the same chokepoint (pkg/crud.Repo.Create) that
//	                    already ULID-generates an empty string PK. For a
//	                    non-PK column only — a share token, an invite code.
//	forge:fill=handler  pure acknowledgement: "I stamp this myself." Changes
//	                    no codegen behavior; its only mechanical effect is
//	                    suppressing the lint (and, where the CRUD shim is
//	                    scaffolded fresh, seeding an op.Entity FORGE_SCAFFOLD
//	                    reminder instead of a bare delegation).
//
// `forge:fill=uuid` is deliberately NOT a strategy: forge pins
// postgres:17-alpine, where uuidv7() (time-ordered, postgres 18+) does not
// exist — only gen_random_uuid() (UUIDv4, random) is available. Forge's
// keyset pagination (pkg/crud's orderKeysetSafe) assumes pk-ascending
// insertion order; a random id would make every list page's order
// unpredictable. ULIDs are time-ordered and sort in creation order, which is
// why forge offers that strategy and not a UUID one.
const ColumnMarkerFill = "forge:fill"

// FillStrategyULID and FillStrategyHandler are the only two values
// `forge:fill=` may take. See ColumnMarkerFill.
const (
	FillStrategyULID    = "ulid"
	FillStrategyHandler = "handler"
)

// ColumnMarkerOwner declares that a column names WHO a row belongs to:
// the rows of this table are the property of distinct principals, and
// this is the column that says whose.
//
// Forge ships no ownership of its own — no implicit scope, no ambient
// principal — and this marker does not change that. It injects no WHERE
// clause and changes no codegen, because only the application knows how
// a caller's claims map onto this column's values (a direct user id, a
// company id read from a membership table, an org resolved from a
// subdomain). Inventing that mapping is exactly the domain policy forge
// deliberately does not own. What the marker changes is what forge is
// willing to let SHIP.
//
// It ARMS the `unscoped_auth` audit category. That category has always
// detected an authenticated RPC whose handler never resolves the caller,
// and has always reported it as advice, on the sound reasoning that a
// freshly-scaffolded project is entirely unscoped by construction: forge
// emits the delegations, the developer writes the scoping, and erroring
// would make forge's own output fail forge's own gate.
//
// That reasoning conflated two different claims. "Unscoped" is a fact
// about handlers; "these rows belong to someone" is a fact about data,
// and forge could not previously observe the second one. A project that
// never says any row belongs to anyone has no boundary to breach. A
// project that declares this marker has stated the exact thing the
// fresh-scaffold argument assumed was unknowable — so from that point an
// authenticated RPC over this table that reads no caller is not a nudge,
// it is one principal reading (or writing) another's rows, and it fails
// the audit.
//
// Measured, against a real downstream app: forge reported "63 of 74
// authenticated RPCs never resolve the caller" and exited 0. Any
// authenticated user could read, update, delete and create rows
// belonging to every other one. The diagnosis was exactly right and
// nothing gated on it.
const ColumnMarkerOwner = "forge:owner"

// TableMarkerAppendOnly declares that a table is an immutable ledger:
// rows may be inserted and read, never rewritten or erased. It is the
// STORAGE half of the `forge:append-only` proto marker, and it is what
// every post-birth pass reads.
//
// It is declared in the migration rather than inferred from the proto for
// the same reason forge:immutable is: whether a row may be rewritten is a
// fact about STORAGE. The proto marker is a birth-time instruction that
// stops existing the moment the birth is over — it writes the trigger and
// omits the Update/Delete RPCs, and then nothing carries it forward. That
// is precisely how an append-only entity ended up with a generated store
// exposing UpdateX, UpdateXMasked and DeleteX: the only pass that had ever
// heard of the marker was the one that wrote the migration.
//
// Declaring it on the table closes that, because the applied schema is the
// one truth every generate-time pass already reads. It is also honest
// about the general case: a table can be append-only because a hand-written
// migration made it so, with no proto marker anywhere.
const TableMarkerAppendOnly = "forge:append-only"

// TableMarkerNoEntity opts one table out of the generated data layer: no
// entity struct, no store, no delegates, no factory. Every other applied
// BASE TABLE is projected, whether or not a service declares CRUD rpcs for
// it — SQL is the schema truth, and the store follows the schema, not the
// wire. A table carrying it is plain schema reached through hand-written
// SQL (a migration bookkeeping table, a partition parent, a table another
// tool owns).
//
// It is the per-table twin of the project-level `//forge:no-orm`, and it
// removes the table from the CRUD join too: a CRUD-named rpc over it is
// custom, as if the table did not exist.
const TableMarkerNoEntity = "forge:no-entity"

// TableMarkerEntityName declares the Go/wire name of the entity a table
// projects to: `COMMENT ON TABLE daemon_attachment IS
// 'forge:entity-name=DaemonAttachment'`. Without it the name is derived from
// the table (the singular, camel-cased), and a CRUD rpc's entity finds its
// table by that name, its plural or its singular. The declaration is for a
// table whose name derives to something else — an irregular plural, a
// prefix, a legacy spelling — so the entity, its store and any `Get<E>`
// rpc over it are named for what the table holds.
const TableMarkerEntityName = "forge:entity-name"

// KnownTableMarkers is the complete set of `forge:*` marker NAMES a
// COMMENT ON TABLE may carry — the table-level twin of
// KnownColumnMarkers, and registered for the same reason: a vocabulary
// nothing enumerates is a vocabulary a typo silently leaves inert.
var KnownTableMarkers = []string{TableMarkerAppendOnly, TableMarkerNoEntity, TableMarkerEntityName, ViewMarkerEntity}

// DeclaredEntityName returns the name a TableMarkerEntityName declaration
// gives the table's entity, "" when it declares none.
func (t Table) DeclaredEntityName() string {
	if m := entityNameTableMarkerRE.FindStringSubmatch(t.Comment); m != nil {
		return m[1]
	}
	return ""
}

var entityNameTableMarkerRE = regexp.MustCompile(regexp.QuoteMeta(TableMarkerEntityName) + `=([A-Za-z_][A-Za-z0-9_]*)`)

// ViewMarkerEntity opts a VIEW into the generated data layer as a READ-ONLY
// entity: `COMMENT ON VIEW chats_with_activity IS 'forge:entity'`. It gets
// the row struct plus List/Count, and Get<E>ByID by its key column — `id`
// when the view has one, or the one `forge:entity key=<column>` names. No
// write is generated: a view is a derived read. Views are opt-in because
// most are reporting conveniences nothing in Go reads.
const ViewMarkerEntity = "forge:entity"

// DeclaresEntity reports whether a view's comment opts it in with
// ViewMarkerEntity, and the key column a `key=<column>` names ("" for none).
func (t Table) DeclaresEntity() (ok bool, key string) {
	m := viewEntityMarkerRE.FindStringSubmatch(t.Comment)
	if m == nil {
		return false, ""
	}
	return true, m[1]
}

var viewEntityMarkerRE = regexp.MustCompile(regexp.QuoteMeta(ViewMarkerEntity) + `(?:\s+key=([A-Za-z_][A-Za-z0-9_]*))?(?:[^\w:=-]|$)`)

// NoEntity reports whether the table declares TableMarkerNoEntity in its
// catalog comment, matched as a whole token.
func (t Table) NoEntity() bool { return noEntityTableMarkerRE.MatchString(t.Comment) }

var noEntityTableMarkerRE = regexp.MustCompile(regexp.QuoteMeta(TableMarkerNoEntity) + `(?:[^\w:-]|$)`)

// AppendOnly reports whether the table is an append-only ledger: INSERT and
// SELECT only, no UPDATE and no DELETE.
//
// TWO declarations satisfy it, and the second one is not redundancy — it is
// the upgrade path.
//
//  1. TableMarkerAppendOnly in the catalog comment. The declaration forge
//     writes at birth, and the one a hand-written migration should use.
//     Matched as a WHOLE token so a longer marker that merely begins with
//     this one (a hypothetical `forge:append-only-soft`) cannot read as
//     this one — the same discipline declaresOwnerMarker applies to
//     forge:owner, and stricter than Column.HasMarker's substring test.
//
//  2. forge's own guard trigger, `<table>_append_only`. Forge wrote that
//     trigger long before it wrote the comment, so EVERY append-only table
//     that predates the declaration has the trigger and no comment. Reading
//     only the comment would mean the tables with the longest-standing
//     immutability guarantee were the ones that silently lost it — the
//     generated store would hand back UpdateX/DeleteX for a table postgres
//     rejects every write to. The trigger is a real declaration of the same
//     fact, made in the only vocabulary that release had.
//
// The trigger arm is matched by forge's exact NAME, never by the shape
// "some trigger fires on UPDATE". An updated_at stamper is also BEFORE
// UPDATE and means the opposite. The asymmetry of harm sets that precision:
// missing the marker silently weakens a guarantee, while inventing one
// DELETES Update/Delete from a generated store and breaks a build that was
// correct. A hand-written guard under any other name declares itself with
// the comment.
func (t Table) AppendOnly() bool {
	if appendOnlyTableMarkerRE.MatchString(t.Comment) {
		return true
	}
	guard := appendOnlyGuardTrigger(t.Name)
	for _, trg := range t.Triggers {
		if trg == guard {
			return true
		}
	}
	return false
}

// AppendOnlyDeclared reports whether the table carries the CATALOG
// declaration specifically — the comment, not the guard-trigger fallback
// AppendOnly also accepts.
//
// It exists so a test can pin that its fixture really does reproduce the
// pre-declaration shape. Without it an upgrade test keeps passing if a
// future change starts writing the comment into the fixture, while proving
// nothing about the upgrade it was written to cover.
func (t Table) AppendOnlyDeclared() bool { return appendOnlyTableMarkerRE.MatchString(t.Comment) }

// AppendOnlyGuardTrigger returns the name of the guard trigger forge
// installs on an append-only table. Exported because the upgrade path has
// to name the same trigger forge writes, and two spellings of one name is
// how a migration ends up looking for a guard that is really there.
func AppendOnlyGuardTrigger(table string) string { return appendOnlyGuardTrigger(table) }

func appendOnlyGuardTrigger(table string) string { return table + "_append_only" }

var appendOnlyTableMarkerRE = regexp.MustCompile(regexp.QuoteMeta(TableMarkerAppendOnly) + `(?:[^\w:-]|$)`)

// KnownColumnMarkers is the complete set of `forge:*` marker NAMES a
// COMMENT ON COLUMN or COMMENT ON CONSTRAINT may carry. It is the single
// source of truth shared by `forge project annotations --kind column`
// (internal/cli/annotations.go) and the unknown-column-marker lint check
// (internal/cli/lint) — both project from this slice rather than each
// maintaining its own copy, which is exactly the discoverability gap
// forge:ref fell into (undiscoverable outside two skill files) that this
// registry exists to close.
var KnownColumnMarkers = []string{ColumnMarkerImmutable, ColumnMarkerRef, ColumnMarkerClosedSet, ColumnMarkerVersion, ColumnMarkerFill, ColumnMarkerOwner, ColumnMarkerGoType, ColumnMarkerNoSoftDelete, ColumnMarkerSecret, ColumnMarkerReadable}

// ColumnMarkerSecret declares a column the database's read-only role (pkg/dbgrants)
// must never read: `COMMENT ON COLUMN git_credentials.access_token IS
// 'forge:secret'`. It is the SQL spelling of the proto `// forge:secret` field
// marker and the only one a column with no wire field can use — most credential
// tables have none. Either spelling withholds the column from the role; only
// the proto one also strips it from read responses.
const ColumnMarkerSecret = "forge:secret"

// ColumnMarkerReadable declares a column whose name looks like a credential but
// is not one, so the read-only role may read it: `COMMENT ON COLUMN
// usage_events.token_hash IS 'forge:readable — a content hash of the prompt, not
// of a credential'`. The reason is mandatory; it is the review record of an
// exception to the role's fail-closed default. Secret wins when both are declared.
const ColumnMarkerReadable = "forge:readable"

var (
	secretMarkerRE   = regexp.MustCompile(regexp.QuoteMeta(ColumnMarkerSecret) + `(?:[^\w=-]|$)`)
	readableMarkerRE = regexp.MustCompile(regexp.QuoteMeta(ColumnMarkerReadable) + `(?:[^\w=-]|$)`)
)

// DeclaresSecret reports whether the column's catalog comment carries
// ColumnMarkerSecret, matched as a whole token.
func (c Column) DeclaresSecret() bool { return secretMarkerRE.MatchString(c.Comment) }

// ReadableReason returns the reason a ColumnMarkerReadable declaration gives,
// and whether the column declares the marker at all. A declaration with no
// reason returns ("", true): it is declared, and CheckMarkers rejects it.
func (c Column) ReadableReason() (reason string, declared bool) {
	return readableReason(c.Comment)
}

func readableReason(comment string) (string, bool) {
	loc := readableMarkerRE.FindStringIndex(comment)
	if loc == nil {
		return "", false
	}
	rest := comment[loc[0]+len(ColumnMarkerReadable):]
	// Another marker ends the reason; the separator (`—`, `-`, `:`) is not part of it.
	if i := strings.Index(rest, "forge:"); i >= 0 {
		rest = rest[:i]
	}
	rest = strings.TrimLeft(rest, " \t:;,.-–—=")
	rest = strings.TrimSpace(rest)
	for _, r := range rest {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return rest, true
		}
	}
	return "", true
}

// ColumnMarkerNoSoftDelete declares that a table's `deleted_at` column is an
// ordinary nullable timestamp, not forge's soft-delete tombstone:
// `COMMENT ON COLUMN worktrees.deleted_at IS 'forge:no-soft-delete'`.
//
// By convention a time-typed deleted_at IS the soft-delete declaration — the
// generated Get, List and Update skip rows where it is set, and Delete stamps
// it instead of deleting. That is the right default and stays the default.
// But the same column name is also how a schema spells an ARCHIVE stamp,
// where an archived row must stay readable and writable and a delete must
// delete. Without a declaration the only way out was to route every query
// through the builder with WhereAllWithDeleted and ForceDelete, re-deriving
// by hand what the generated store should have said.
//
// With the marker the column projects like any other nullable timestamp:
// no Bun ,soft_delete tag, no ListAll<E>, the column is writable by Update,
// every row is visible to Get and List, and Delete is a real DELETE. Only
// meaningful on deleted_at — on any other column it is rejected, because it
// would do nothing (see CheckMarkers).
const ColumnMarkerNoSoftDelete = "forge:no-soft-delete"

// DeclaresNoSoftDelete reports whether the column's catalog comment carries
// ColumnMarkerNoSoftDelete, matched as a whole token.
func (c Column) DeclaresNoSoftDelete() bool {
	return noSoftDeleteMarkerRE.MatchString(c.Comment)
}

var noSoftDeleteMarkerRE = regexp.MustCompile(regexp.QuoteMeta(ColumnMarkerNoSoftDelete) + `(?:[^\w:=-]|$)`)

// ColumnMarkerGoType declares the Go type a column projects to on the
// generated entity struct, in place of the one forge derives from its SQL
// type: `forge:go-type=github.com/acme/app/gen/app/v1;appv1.ContentBlockType`
// for an enum kept as an integer, `forge:go-type=map[string]any` for a json
// document read as a map. The type must scan from and bind to the column
// (Bun's own rules: a named integer for an integer column, a map or struct
// for json). A qualified type names its import path, with `;<name>` when
// the package's name is not the path's last element — the go_package
// convention. Read with Column.GoTypeOverride.
const ColumnMarkerGoType = "forge:go-type"

// GoTypeOverride returns the type a ColumnMarkerGoType declaration names,
// "" when the column declares none.
func (c Column) GoTypeOverride() string {
	if m := goTypeMarkerRE.FindStringSubmatch(c.Comment); m != nil {
		return m[1]
	}
	return ""
}

var goTypeMarkerRE = regexp.MustCompile(regexp.QuoteMeta(ColumnMarkerGoType) + `=(\S+)`)

// HasMarker reports whether the column's catalog comment carries marker.
//
// forge:fill takes an argument (`forge:fill=ulid`), so a plain substring
// check against the bare marker name would never match it; FillStrategy
// is the accessor for that marker instead. HasMarker still answers "was
// forge:fill declared at all" correctly, because ColumnMarkerFill (without
// the `=value` suffix) is itself a substring of the full declaration.
func (c Column) HasMarker(marker string) bool {
	return strings.Contains(c.Comment, marker)
}

// DatabaseSupplied reports whether the database gives the column a value
// when an INSERT leaves it out: an identity column, or any DEFAULT — a
// serial's nextval(...), gen_random_uuid(), a literal. It is the applied
// schema's answer to "is this key server-allocated", which the column's
// type does not give: `integer PRIMARY KEY` with no DEFAULT is a key the
// caller must set.
func (c Column) DatabaseSupplied() bool {
	return c.IsIdentity || strings.TrimSpace(c.Default) != ""
}

// FillStrategy returns the value of a `forge:fill=<strategy>` declaration
// on the column's catalog comment, and whether one was present. An
// unrecognized value (neither FillStrategyULID nor FillStrategyHandler)
// still reports ok=true with the raw string — the caller decides how to
// report a bad value; this accessor only parses the grammar.
func (c Column) FillStrategy() (strategy string, ok bool) {
	idx := strings.Index(c.Comment, ColumnMarkerFill+"=")
	if idx < 0 {
		return "", false
	}
	rest := c.Comment[idx+len(ColumnMarkerFill)+1:]
	end := strings.IndexAny(rest, " \t\n'\"")
	if end < 0 {
		end = len(rest)
	}
	return rest[:end], true
}

// Index is a non-PK index on a table.
type Index struct {
	Name    string
	Columns []string
	Unique  bool
	// Predicate is the WHERE clause of a PARTIAL index, "" for a full one.
	//
	// It is load-bearing for a UNIQUE index and cannot be dropped: a
	// partial `UNIQUE (email) WHERE deleted_at IS NULL` permits duplicate
	// emails among soft-deleted rows, and reporting it as plain
	// `UNIQUE (email)` describes a stricter table than the one that
	// exists. An unqualified claim is worse than no claim.
	Predicate string
	// Expression is true when at least one index key is an EXPRESSION
	// rather than a bare column — `UNIQUE (lower(name))`,
	// `UNIQUE (supplier_id, lower(sku))`.
	//
	// It exists because such an index cannot be described by Columns
	// alone, and describing it partially is worse than not describing it.
	// pg_catalog stores an expression key as attnum 0, which carries no
	// pg_attribute row, so a column-joining introspection query drops that
	// key silently: `UNIQUE (lower(name))` came back as an index with NO
	// columns, and `UNIQUE (supplier_id, lower(sku))` came back as
	// `UNIQUE (supplier_id)` — a DIFFERENT, stricter constraint than the
	// one the table has.
	//
	// Columns holds only the bare-column subset, so it must never be read
	// as the whole key: `lower(name)` being unique does not make `name`
	// unique, and `(supplier_id, lower(sku))` being unique does not make
	// `supplier_id` unique. Read Keys for the full key, in order.
	Expression bool
	// Keys is every index key in order, as postgres renders it: a bare
	// column is its name, an expression key is its SQL text
	// (`lower(name)`). It is the only faithful description of an
	// expression index, and it is what lets a consumer recognise the
	// common case-insensitive form and plan against the underlying column
	// instead of giving up on the index entirely.
	//
	// Populated for every index by introspection. A hand-constructed Index
	// may leave it nil, so read it through [Index.KeyList], which falls
	// back to Columns.
	Keys []IndexKey
}

// KeyList returns the index's keys in order, falling back to one plain key
// per entry in Columns when Keys is unset.
//
// The fallback exists because Index is constructed in two ways: introspection
// (which fills Keys) and by hand, in tests and in callers that only ever
// describe bare columns. Requiring both fields at every construction site
// would make the zero value a trap — an index with keys silently missing —
// so the accessor derives the one from the other instead.
func (ix Index) KeyList() []IndexKey {
	if len(ix.Keys) > 0 {
		return ix.Keys
	}
	out := make([]IndexKey, 0, len(ix.Columns))
	for _, c := range ix.Columns {
		out = append(out, IndexKey{Expr: c, Column: c})
	}
	return out
}

// IndexKey is one key of an index — a column, or an expression over columns.
type IndexKey struct {
	// Expr is the key as postgres renders it: "name" for a bare column,
	// "lower(name)" for an expression.
	Expr string
	// Column is the bare column this key is over, when that is knowable:
	// the column itself for a plain key, and the argument for a
	// single-column case fold like `lower(name)`. Empty when the key spans
	// several columns or uses an expression this package does not read
	// (`(a || b)`, `(meta->>'k')`).
	//
	// A consumer that can only place a value for a real column uses this
	// to decide whether the key is reachable at all.
	Column string
	// Fold names the case-folding function wrapping Column — "lower" or
	// "upper" — and is empty for every other key, including a plain one.
	//
	// This is the one expression form worth reading rather than refusing.
	// `UNIQUE (lower(email))` is how case-insensitive uniqueness is
	// spelled in postgres and is common in hand-written migrations, and
	// its rule is exactly "these values must be distinct ignoring case" —
	// which a value generator CAN satisfy without evaluating SQL.
	Fold string
}

// ForeignKey is a declared REFERENCES constraint.
type ForeignKey struct {
	Column    string
	RefTable  string
	RefColumn string
	// Name is the constraint name postgres knows it by — explicit
	// (`CONSTRAINT orders_patient_id_fkey`) or auto-derived from an inline
	// REFERENCES. It is what a COMMENT ON CONSTRAINT statement names.
	Name string
	// OnDelete is the referential action as information_schema spells it:
	// "NO ACTION" (postgres's default), "RESTRICT", "CASCADE", "SET NULL"
	// or "SET DEFAULT".
	//
	// It is a behaviour of the reference, not a decoration on it: CASCADE
	// means deleting the parent silently deletes these rows. Anything
	// reporting that a reference exists without saying what it does on
	// delete has described the cheap half.
	OnDelete string
	// Comment is the constraint's catalog comment (COMMENT ON CONSTRAINT
	// ... IS '...'), "" when none. It is where a project DECLARES a domain
	// fact about the reference that the schema alone cannot express — see
	// seeddata's diamond resolution. Read from the applied catalog, never
	// from migration text, so there is one source of truth.
	Comment string
}

// CheckConstraint is one introspected CHECK constraint on a table.
type CheckConstraint struct {
	// Name is the constraint name — postgres auto-derives one
	// (`<table>_<col>_check`, `_check1`, …) for CHECKs written inline
	// without an explicit CONSTRAINT name, which is how forge emits them.
	Name string
	// Def is the canonical constraint definition from
	// pg_get_constraintdef, e.g. "CHECK (amount_cents >= 0)" or
	// "CHECK (status = ANY (ARRAY['A'::text, 'B'::text]))".
	Def string
	// Columns are the column names the check references (its conkey),
	// in key order. Single-column for every check forge itself projects
	// (enum vocabulary, protovalidate bounds, the id-non-empty guard).
	Columns []string
	// Comment is the constraint's catalog comment (COMMENT ON CONSTRAINT
	// ... IS '...'), "" when none — where a CHECK declares the closed set
	// its value list mirrors (ColumnMarkerClosedSet).
	Comment string
}

// CanonicalType is the dialect-neutral type vocabulary the generators
// consume. The mapping from declared SQL types is documented on
// MapDeclaredType.
type CanonicalType string

// The Type* constants enumerate every CanonicalType the generators
// recognise.
const (
	TypeString CanonicalType = "string"
	TypeInt    CanonicalType = "int64"
	TypeFloat  CanonicalType = "float64"
	TypeBool   CanonicalType = "bool"
	TypeTime   CanonicalType = "time"
	TypeJSON   CanonicalType = "json"
	TypeBytes  CanonicalType = "bytes"
)

// Conventions are the behavior-by-convention signals derived from real
// columns. No annotations: the columns ARE the declaration.
//
//	deleted_at (time)            ⇒ soft delete (UPDATE-not-DELETE,
//	                               reads filter IS NULL, ListAll* unfiltered)
//	                               unless the column declares
//	                               forge:no-soft-delete (ColumnMarkerNoSoftDelete)
//	created_at + updated_at      ⇒ managed timestamps
//	text columns                 ⇒ searchable by the generated list filter
type Conventions struct {
	SoftDelete bool
	Timestamps bool
	// AppendOnly mirrors Table.AppendOnly (a COMMENT ON TABLE
	// `forge:append-only` declaration). Unlike the two above it is a
	// DECLARATION rather than a column-shape convention — no arrangement of
	// columns can express "this table refuses UPDATE" — but it rides here
	// because every consumer of behavior-by-table already reads
	// Conventions, and a second channel is how one pass ends up honouring
	// what another ignores.
	AppendOnly bool
	// SearchColumns are the text columns (excluding the PK and managed
	// columns) the generated search filter matches against.
	SearchColumns []string
}

// managed column names recognized by convention.
const (
	ColCreatedAt = "created_at"
	ColUpdatedAt = "updated_at"
	ColDeletedAt = "deleted_at"
)

// DetectConventions reads the behavior conventions off a table's columns.
func DetectConventions(t Table) Conventions {
	var c Conventions
	byName := map[string]Column{}
	for _, col := range t.Columns {
		byName[col.Name] = col
	}
	if col, ok := byName[ColDeletedAt]; ok && col.Type == TypeTime && !col.DeclaresNoSoftDelete() {
		c.SoftDelete = true
	}
	c.AppendOnly = t.AppendOnly()
	// Managed timestamps are type-gated like deleted_at: the pair counts
	// only when the generator can actually STAMP both columns — time
	// columns (stamped as time.Time) or legacy TEXT columns (stamped as
	// RFC3339Nano strings; kalshi fr-3fba9166ba). An exotic type (epoch
	// INTEGER, arrays) opts the table out of managed timestamps; the
	// columns stay plain schema instead of driving stamping code the
	// emitter can't express.
	stampable := func(name string) bool {
		col, ok := byName[name]
		return ok && !col.IsArray && (col.Type == TypeTime || col.Type == TypeString)
	}
	c.Timestamps = stampable(ColCreatedAt) && stampable(ColUpdatedAt)
	for _, col := range t.Columns {
		if col.Type != TypeString || col.IsArray || col.IsPK {
			continue
		}
		c.SearchColumns = append(c.SearchColumns, col.Name)
	}
	return c
}

// ApplyAndIntrospect applies every *.up.sql under migDir (in lexical
// order) to a fresh real-postgres shadow database and returns the
// resulting schema. A missing or empty migrations directory returns
// (nil, nil): no schema, no entities, nothing to project.
//
// This is the zero-config entry: the shadow lives on pkg/pgtest's default
// ephemeral server (embedded-postgres, or the FORGE_TEST_POSTGRES_URL
// server when that env var is set). Callers that have resolved a shadow
// SERVER from forge's config/environment (internal/shadowdb) call
// ApplyAndIntrospectAt instead, passing that server down — schemadef itself
// stays env-agnostic and does not decide which postgres to use.
func ApplyAndIntrospect(migDir string) ([]Table, error) {
	return ApplyAndIntrospectAt(migDir, "")
}

// ApplyAndIntrospectAt is ApplyAndIntrospect against an explicitly resolved
// shadow SERVER. baseURL is a maintenance-DB DSN
// (postgres://user:pass@host:port/postgres?...) on an already-running
// server; the shadow is a freshly-created, uniquely-named scratch database
// on it, dropped before returning, so the server's application database is
// never touched. An empty baseURL falls back to pkg/pgtest's default
// ephemeral server (embedded-postgres / FORGE_TEST_POSTGRES_URL) — the
// zero-setup / CI path.
//
// The server choice is deliberately the CALLER's: forge's codegen/env layer
// (internal/shadowdb) resolves a reachable configured server and passes it
// here, so the fragile embedded default is used only when no configured
// server is available. schemadef and pkg/pgtest remain dumb mechanics.
func ApplyAndIntrospectAt(migDir, baseURL string) ([]Table, error) {
	tables, shadow, err := ApplyAndIntrospectShadowAt(migDir, baseURL)
	if shadow != nil {
		defer shadow.Close()
	}
	return tables, err
}

// Shadow is a LIVE handle on the applied-schema shadow database, handed to
// callers that must do more than read the model: ask postgres itself a
// question about the schema it just applied.
//
// The distinction matters because a schema model is a description and
// postgres is the authority. A generator that DERIVES a value from the model
// — a fixture satisfying a CHECK — has an opinion; running that value past
// the applied constraint turns the opinion into a verified fact, and the only
// place that can happen is a database with the migrations already on it.
//
// The caller owns Close and must call it; the underlying scratch database
// lives until then.
type Shadow struct {
	db      *sql.DB
	cleanup func()
}

// DB is the open connection to the shadow database, with every migration
// under the directory already applied.
func (s *Shadow) DB() *sql.DB { return s.db }

// Close drops the scratch database and releases the connection. Safe to call
// on a nil *Shadow, so `defer shadow.Close()` needs no guard.
func (s *Shadow) Close() {
	if s == nil || s.cleanup == nil {
		return
	}
	s.cleanup()
	s.cleanup = nil
}

// ApplyAndIntrospectShadowAt is ApplyAndIntrospectAt that KEEPS the shadow
// database open and returns it. The schema model is identical — this is the
// same apply-then-introspect pass — but the caller can additionally interrogate
// the applied schema directly.
//
// A non-nil *Shadow is returned whenever one was opened, INCLUDING on a later
// error, so the caller's deferred Close always reclaims the scratch database.
func ApplyAndIntrospectShadowAt(migDir, baseURL string) ([]Table, *Shadow, error) {
	set, err := ReadMigrationSet(migDir)
	if err != nil || set.Len() == 0 {
		return nil, nil, err
	}

	db, cleanup, err := openShadow(baseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("open postgres shadow db: %w", err)
	}
	shadow := &Shadow{db: db, cleanup: cleanup}
	if err := set.apply(db); err != nil {
		return nil, shadow, err
	}

	tables, err := introspect(db)
	return tables, shadow, err
}

// OpenShadowAt opens a scratch shadow database with every migration under
// migDir applied and returns it live: the replay ApplyAndIntrospectShadowAt
// performs, without the introspection. Unlike that function it opens a shadow
// even when migDir holds no migrations yet (or does not exist) — a project's
// FIRST migration is exactly the one a caller wants to try against postgres
// before writing it.
//
// On error there is nothing to close: a shadow opened before the replay
// failed is dropped here, so the caller checks err and only then defers
// Close — the ordinary Go contract. (It used to return the live shadow
// beside a replay error, which made `defer shadow.Close()` BEFORE the err
// check the only correct spelling at the call site.)
func OpenShadowAt(migDir, baseURL string) (*Shadow, error) {
	set, err := ReadMigrationSet(migDir)
	if err != nil {
		return nil, err
	}
	db, cleanup, err := openShadow(baseURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres shadow db: %w", err)
	}
	shadow := &Shadow{db: db, cleanup: cleanup}
	if err := set.apply(db); err != nil {
		shadow.Close()
		return nil, err
	}
	return shadow, nil
}

// TryApply runs sqlText against the shadow inside a transaction that is
// ALWAYS rolled back, and returns postgres's verdict on it: nil when every
// schema-defining statement applied, else the first one's error, unwrapped,
// so the caller can attribute it. The shadow is left exactly as it was, so
// one shadow answers any number of "would this migration apply?" questions —
// postgres DDL is transactional, which is what makes the rollback complete.
//
// The statement policy is applyMigration's: a statement that cannot affect
// the table/column model (a function, a COMMENT, a trigger) may fail without
// failing the try. Each one runs under its own savepoint so its failure does
// not abort the transaction around the statements that do count.
func (s *Shadow) TryApply(ctx context.Context, sqlText string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin shadow transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for i, stmt := range SplitStatements(sqlText) {
		if isSchemaDefining(stmt) {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
			continue
		}
		savepoint := fmt.Sprintf("forge_try_apply_%d", i)
		if _, err := tx.ExecContext(ctx, "SAVEPOINT "+savepoint); err != nil {
			return fmt.Errorf("shadow savepoint: %w", err)
		}
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			if _, rerr := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+savepoint); rerr != nil {
				return fmt.Errorf("shadow rollback to savepoint: %w", rerr)
			}
		}
	}
	return nil
}

// openShadow creates the ephemeral shadow database: a scratch DB on the
// resolved server when baseURL is set, else pkg/pgtest's default ephemeral
// server.
func openShadow(baseURL string) (*sql.DB, func(), error) {
	if baseURL != "" {
		return pgtest.NewAtURL(baseURL)
	}
	return pgtest.New()
}

// applyMigration executes one migration file statement by statement
// against the real-postgres shadow. Schema-defining statements that fail
// are hard errors; statements that cannot affect the table/column model
// are skipped on failure (postgres DML data movement, or auxiliary DDL
// the bare ephemeral DB can't satisfy — an extension that isn't
// installed, a role that doesn't exist).
//
// Migrations apply VERBATIM: postgres is the target, so there is no
// normalization pass. Every construct in the migration — schema-qualified
// names, postgres-only types, '::type' casts, multi-ADD ALTERs,
// generated/identity columns — runs exactly as written.
func applyMigration(db *sql.DB, sqlText string) error {
	for _, stmt := range SplitStatements(sqlText) {
		if !isSchemaDefining(stmt) {
			// Non-schema statement (data movement, functions, triggers,
			// comments, extensions): failure cannot change the
			// table/column model — best-effort apply, skip on error.
			_, _ = db.ExecContext(context.Background(), stmt)
			continue
		}
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			return fmt.Errorf("%w\nstatement:\n%s", err, strings.TrimSpace(stmt))
		}
	}
	return nil
}

// isSchemaDefining reports whether a statement defines the table/column
// model the ORM is projected from. Leading SQL comments are stripped
// first — migration files routinely carry banner comments, and a
// comment-prefixed CREATE TABLE that silently fell into the skip path
// would produce a partial schema with no error (an ORM that lies).
func isSchemaDefining(stmt string) bool {
	head := strings.ToUpper(strings.Join(strings.Fields(stripLeadingSQLComments(stmt)), " "))
	for _, p := range []string{
		"CREATE TABLE", "ALTER TABLE", "DROP TABLE",
		"CREATE INDEX", "CREATE UNIQUE INDEX", "DROP INDEX",
	} {
		if strings.HasPrefix(head, p) {
			return true
		}
	}
	return false
}

// stripLeadingSQLComments removes leading `--` line comments, `/* */`
// block comments and blank lines from a statement.
func stripLeadingSQLComments(stmt string) string {
	s := stmt
	for {
		s = strings.TrimLeft(s, " \t\r\n")
		switch {
		case strings.HasPrefix(s, "--"):
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = s[i+1:]
			} else {
				return ""
			}
		case strings.HasPrefix(s, "/*"):
			if i := strings.Index(s, "*/"); i >= 0 {
				s = s[i+2:]
			} else {
				return ""
			}
		default:
			return s
		}
	}
}

// SplitStatements splits SQL text into individual statements, honoring
// single/double quotes, line and block comments, postgres dollar-quoted
// strings ($tag$ ... $tag$), and trigger bodies (BEGIN ... END;).
func SplitStatements(sqlText string) []string {
	var stmts []string
	var b strings.Builder
	s := sqlText
	i := 0
	beginDepth := 0
	flush := func() {
		stmt := strings.TrimSpace(b.String())
		b.Reset()
		if stmt != "" && !isOnlyComments(stmt) {
			stmts = append(stmts, stmt)
		}
	}
	for i < len(s) {
		c := s[i]
		switch {
		case c == '-' && i+1 < len(s) && s[i+1] == '-': // line comment
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				j = len(s) - i
			}
			b.WriteString(s[i : i+j])
			i += j
		case c == '/' && i+1 < len(s) && s[i+1] == '*': // block comment
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				j = len(s) - i - 2
			}
			b.WriteString(s[i : i+j+4])
			i += j + 4
		case c == '\'' || c == '"': // quoted string / identifier
			q := c
			j := i + 1
			for j < len(s) {
				if s[j] == q {
					if j+1 < len(s) && s[j+1] == q { // doubled quote escape
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			b.WriteString(s[i:j])
			i = j
		case c == '$': // possible dollar-quoted string
			if end := dollarQuoteEnd(s, i); end > i {
				b.WriteString(s[i:end])
				i = end
			} else {
				b.WriteByte(c)
				i++
			}
		case c == ';':
			b.WriteByte(c)
			i++
			if beginDepth == 0 {
				flush()
			}
		default:
			// BEGIN ... END; bodies inside a CREATE TRIGGER statement must
			// not be split on the inner `;`. A bare transaction BEGIN;
			// never lands here because the buffer wouldn't contain
			// CREATE TRIGGER.
			if isWordBoundary(s, i) {
				inTrigger := beginDepth > 0 ||
					strings.Contains(strings.ToUpper(b.String()), "CREATE TRIGGER")
				switch {
				case inTrigger && (hasWordAt(s, i, "BEGIN") || hasWordAt(s, i, "CASE")):
					beginDepth++
				case inTrigger && hasWordAt(s, i, "END"):
					if beginDepth > 0 {
						beginDepth--
					}
				}
			}
			b.WriteByte(c)
			i++
		}
	}
	flush()
	return stmts
}

func isOnlyComments(stmt string) bool {
	for _, line := range strings.Split(stmt, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "--") || t == ";" {
			continue
		}
		return false
	}
	return true
}

func isWordBoundary(s string, i int) bool {
	if i == 0 {
		return true
	}
	p := s[i-1]
	return (p < 'a' || p > 'z') && (p < 'A' || p > 'Z') && (p < '0' || p > '9') && p != '_'
}

func hasWordAt(s string, i int, word string) bool {
	if i+len(word) > len(s) {
		return false
	}
	if !strings.EqualFold(s[i:i+len(word)], word) {
		return false
	}
	j := i + len(word)
	if j == len(s) {
		return true
	}
	n := s[j]
	return (n < 'a' || n > 'z') && (n < 'A' || n > 'Z') && (n < '0' || n > '9') && n != '_'
}

// dollarQuoteEnd returns the index just past a postgres dollar-quoted
// string starting at i, or i when s[i:] is not a dollar quote.
func dollarQuoteEnd(s string, i int) int {
	j := i + 1
	for j < len(s) && (s[j] == '_' || s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z' || s[j] >= '0' && s[j] <= '9') {
		j++
	}
	if j >= len(s) || s[j] != '$' {
		return i
	}
	tag := s[i : j+1] // "$tag$" or "$$"
	end := strings.Index(s[j+1:], tag)
	if end < 0 {
		return i
	}
	return j + 1 + end + len(tag)
}

// Queryer is the read-only database handle introspection needs: one method,
// the catalog query. *sql.DB satisfies it, and so does anything else that
// can run a query — a *sql.Tx, a pool wrapper, or the ORM handle a forge
// test already holds. Introspection only ever READS the catalog, and this
// type says so at the signature: a caller can hand over its live database
// knowing the model-builder has no way to write to it.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Introspect reads the schema model out of a database that ALREADY has the
// migrations on it, through postgres's own catalog. It is the shadow-free
// entry: no migration directory, no scratch database, no second postgres —
// the caller hands over a live handle and gets back the same model
// ApplyAndIntrospect produces after applying migrations itself.
//
// This is what a TEST uses. A test already owns a migrated database (forge
// scaffolds app.NewMigratedTestDB for exactly that), so making it name a
// migrations directory and boot a second postgres to describe the schema it
// is already connected to would be ceremony, not safety. Booting a shadow
// stays the generator's path, because a generator starts from files on disk
// and has no database yet.
func Introspect(db Queryer) ([]Table, error) { return introspect(db) }

// IntrospectViews returns every user VIEW (regular and materialized) of the
// database, each as a Table with IsView set: its columns, in ordinal order,
// and its catalog comment, where an opt-in (ViewMarkerEntity) lives.
func IntrospectViews(db Queryer) ([]Table, error) {
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `
		SELECT n.nspname, c.relname
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind IN ('v', 'm')
		  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
		  AND n.nspname NOT LIKE 'pg_toast%'
		ORDER BY n.nspname, c.relname`)
	if err != nil {
		return nil, fmt.Errorf("list shadow views: %w", err)
	}
	type ref struct{ schema, name string }
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.schema, &r.name); err != nil {
			_ = rows.Close()
			return nil, err
		}
		refs = append(refs, r)
	}
	cerr := rows.Err()
	_ = rows.Close()
	if cerr != nil {
		return nil, cerr
	}
	var views []Table
	for _, r := range refs {
		v, err := introspectView(ctx, db, r.schema, r.name)
		if err != nil {
			return nil, fmt.Errorf("introspect view %s.%s: %w", r.schema, r.name, err)
		}
		views = append(views, v)
	}
	return views, nil
}

// introspectView reads a view's comment and columns. pg_attribute rather
// than information_schema.columns, which omits materialized views.
func introspectView(ctx context.Context, db Queryer, schema, name string) (Table, error) {
	v := Table{Name: name, Schema: schema, IsView: true}
	comment, err := introspectTableComment(ctx, db, schema, name)
	if err != nil {
		return v, err
	}
	v.Comment = comment
	rows, err := db.QueryContext(ctx, `
		SELECT a.attname, t.typname, a.attndims > 0 OR t.typcategory = 'A',
		       COALESCE(col_description(a.attrelid, a.attnum), '')
		FROM pg_attribute a
		JOIN pg_type t ON t.oid = a.atttypid
		WHERE a.attrelid = format('%I.%I', $1::text, $2::text)::regclass
		  AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`, schema, name)
	if err != nil {
		return v, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var col, udt, colComment string
		var isArray bool
		if err := rows.Scan(&col, &udt, &isArray, &colComment); err != nil {
			return v, err
		}
		dataType := ""
		if isArray {
			dataType = "ARRAY"
		}
		c := Column{Name: col, DeclType: pgDeclType(dataType, udt), Comment: colComment}
		c.Type, c.IsArray, c.TypeKnown = MapDeclaredType(c.DeclType)
		v.Columns = append(v.Columns, c)
	}
	return v, rows.Err()
}

// introspect reads the full table model out of the real-postgres shadow
// through the postgres catalog (information_schema / pg_catalog). It
// enumerates BASE TABLEs across every user schema — not just `public` —
// so schema-qualified migrations (CREATE TABLE controlplane.foo) are
// introspected like any other. Tables are keyed by their bare name in
// the returned model (the projection layer matches entities by table
// name); pg's own catalog rejects same-named tables in different schemas
// only at query time, which forge projects do not do.
func introspect(db Queryer) ([]Table, error) {
	ctx := context.Background()

	// schema, table for every user BASE TABLE. Exclude the system schemas
	// and the migration-bookkeeping tables (golang-migrate's
	// schema_migrations, migratekit's schema_migrations_compat and
	// schema_migrations_applied); keep
	// everything a project defined, including non-public schemas.
	rows, err := db.QueryContext(ctx, `
		SELECT table_schema, table_name
		FROM information_schema.tables
		WHERE table_type = 'BASE TABLE'
		  AND table_schema NOT IN ('pg_catalog', 'information_schema')
		  AND table_name NOT IN ('schema_migrations', 'schema_migrations_compat', 'schema_migrations_applied')
		ORDER BY table_schema, table_name`)
	if err != nil {
		return nil, fmt.Errorf("list shadow tables: %w", err)
	}
	type ref struct{ schema, name string }
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.schema, &r.name); err != nil {
			_ = rows.Close()
			return nil, err
		}
		refs = append(refs, r)
	}
	cerr := rows.Err()
	_ = rows.Close()
	if cerr != nil {
		return nil, cerr
	}

	var tables []Table
	for _, r := range refs {
		t, err := introspectTable(ctx, db, r.schema, r.name)
		if err != nil {
			return nil, fmt.Errorf("introspect table %s.%s: %w", r.schema, r.name, err)
		}
		tables = append(tables, t)
	}
	return tables, nil
}

func introspectTable(ctx context.Context, db Queryer, schema, name string) (Table, error) {
	t := Table{Name: name, Schema: schema}

	// The TABLE's own catalog comment, where a table-level `forge:*`
	// declaration lives (TableMarkerAppendOnly). Read by regclass for the
	// same reason col_description is: information_schema exposes no path
	// to pg_description. Queryer is deliberately one method wide, so this
	// reads a single row through QueryContext rather than widening the
	// interface every implementer would then have to satisfy.
	comment, err := introspectTableComment(ctx, db, schema, name)
	if err != nil {
		return t, err
	}
	t.Comment = comment

	// Trigger names, which carry a declaration of their own: forge's
	// append-only guard predates the catalog comment that now declares the
	// same fact, so a table born before that change is recognized by its
	// enforcement instead. See Table.AppendOnly.
	triggers, err := introspectTriggers(ctx, db, schema, name)
	if err != nil {
		return t, err
	}
	t.Triggers = triggers

	// Columns in ordinal order. udt_name carries the precise postgres
	// type (int8, timestamptz, jsonb, _text for a text[] …) which
	// MapDeclaredType already understands; data_type = 'ARRAY' marks
	// array columns (udt_name then has the `_elem` form).
	//
	// col_description carries the COMMENT ON COLUMN text, which is where a
	// column's `forge:*` declarations live. information_schema exposes no
	// path to it, so it is read from the catalog by regclass + ordinal —
	// the same shape introspectFKComments uses for constraint comments.
	rows, err := db.QueryContext(ctx, `
		SELECT column_name, data_type, udt_name, is_nullable, column_default, is_generated,
		       is_identity, character_maximum_length,
		       COALESCE(col_description(format('%I.%I', table_schema, table_name)::regclass,
		                                ordinal_position), '')
		FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2
		ORDER BY ordinal_position`, schema, name)
	if err != nil {
		return t, err
	}
	for rows.Next() {
		var (
			col         Column
			dataType    string
			udtName     string
			isNullable  string
			deflt       sql.NullString
			isGenerated string
			isIdentity  string
			maxChars    sql.NullInt64
			comment     string
		)
		if err := rows.Scan(&col.Name, &dataType, &udtName, &isNullable, &deflt, &isGenerated, &isIdentity, &maxChars, &comment); err != nil {
			_ = rows.Close()
			return t, err
		}
		col.Comment = comment
		if maxChars.Valid && maxChars.Int64 > 0 {
			col.MaxChars = int(maxChars.Int64)
		}
		col.DeclType = pgDeclType(dataType, udtName)
		col.Type, col.IsArray, col.TypeKnown = MapDeclaredType(col.DeclType)
		col.NotNull = isNullable == "NO"
		// is_generated is 'ALWAYS' for GENERATED ALWAYS AS (...) STORED
		// columns, 'NEVER' otherwise.
		col.IsGenerated = isGenerated == "ALWAYS"
		col.IsIdentity = isIdentity == "YES"
		if deflt.Valid {
			col.Default = deflt.String
		}
		t.Columns = append(t.Columns, col)
	}
	cerr := rows.Err()
	_ = rows.Close()
	if cerr != nil {
		return t, cerr
	}

	pks, err := introspectPrimaryKey(ctx, db, schema, name)
	if err != nil {
		return t, err
	}
	t.PKCols = pks
	pkSet := make(map[string]bool, len(pks))
	for _, p := range pks {
		pkSet[p] = true
	}
	for i := range t.Columns {
		if pkSet[t.Columns[i].Name] {
			t.Columns[i].IsPK = true
			// PK columns are NOT NULL by definition even when the
			// catalog reports nullable (it never does, but be explicit).
			t.Columns[i].NotNull = true
		}
	}

	idx, err := introspectIndexes(ctx, db, schema, name)
	if err != nil {
		return t, err
	}
	t.Indexes = idx

	fks, err := introspectForeignKeys(ctx, db, schema, name)
	if err != nil {
		return t, err
	}
	t.ForeignKeys = fks

	checks, err := introspectChecks(ctx, db, schema, name)
	if err != nil {
		return t, err
	}
	t.Checks = checks

	return t, nil
}

// introspectChecks returns the table's CHECK constraints via pg_catalog:
// the canonical definition (pg_get_constraintdef) plus the referenced
// column names (conkey → attname). Column names come back as a single
// comma-joined string (string_agg) so no array-scanning driver support is
// needed. NOT NULL is a column attribute, not a check constraint, so it
// never appears here.
func introspectChecks(ctx context.Context, db Queryer, schema, table string) ([]CheckConstraint, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT con.conname,
		       pg_get_constraintdef(con.oid) AS def,
		       COALESCE((
		         SELECT string_agg(a.attname, ',' ORDER BY k.ord)
		         FROM unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord)
		         JOIN pg_attribute a
		           ON a.attrelid = con.conrelid AND a.attnum = k.attnum
		       ), '') AS cols,
		       COALESCE(obj_description(con.oid, 'pg_constraint'), '') AS comment
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE con.contype = 'c'
		  AND n.nspname = $1
		  AND c.relname = $2
		ORDER BY con.conname`, schema, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var checks []CheckConstraint
	for rows.Next() {
		var (
			ck   CheckConstraint
			cols string
		)
		if err := rows.Scan(&ck.Name, &ck.Def, &cols, &ck.Comment); err != nil {
			return nil, err
		}
		if cols != "" {
			ck.Columns = strings.Split(cols, ",")
		}
		checks = append(checks, ck)
	}
	return checks, rows.Err()
}

// pgDeclType reconstructs a declared-type string MapDeclaredType
// understands from postgres's data_type / udt_name pair. For arrays
// (data_type == "ARRAY") udt_name is the internal "_elem" form
// (e.g. "_text"); strip the leading underscore and append "[]".
func pgDeclType(dataType, udtName string) string {
	if strings.EqualFold(dataType, "ARRAY") {
		elem := strings.TrimPrefix(udtName, "_")
		return strings.ToUpper(elem) + "[]"
	}
	return strings.ToUpper(udtName)
}

// introspectPrimaryKey returns the primary-key column names in key order.
func introspectPrimaryKey(ctx context.Context, db Queryer, schema, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT kcu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
		  ON tc.constraint_name = kcu.constraint_name
		  AND tc.table_schema = kcu.table_schema
		WHERE tc.constraint_type = 'PRIMARY KEY'
		  AND tc.table_schema = $1
		  AND tc.table_name = $2
		ORDER BY kcu.ordinal_position`, schema, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var pks []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		pks = append(pks, c)
	}
	return pks, rows.Err()
}

// introspectIndexes returns the non-PK indexes (unique and plain) of a
// table via pg_catalog, with columns in index order.
//
// An index key may be an EXPRESSION (`lower(name)`) rather than a column.
// pg_catalog spells that as attnum 0, which has no pg_attribute row, so
// the join below cannot yield a name for it. The index is still reported —
// with the bare-column subset in Columns and Expression set — because the
// alternative is what this query used to do: emit `UNIQUE (lower(name))`
// as an index with no columns at all, and `UNIQUE (supplier_id,
// lower(sku))` as plain `UNIQUE (supplier_id)`. The second is the
// dangerous one: it does not look malformed, it looks like a stricter
// table, and every consumer downstream believed it.
//
// indnatts (the key count) is selected so the expression case is detected
// from the catalog rather than inferred from a row that never arrived —
// an index whose keys are ALL expressions produces no join rows at all,
// so it is picked up by the separate no-column pass below.
func introspectIndexes(ctx context.Context, db Queryer, schema, table string) ([]Index, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT ic.relname AS index_name,
		       idx.indisunique AS is_unique,
		       COALESCE(pg_get_expr(idx.indpred, idx.indrelid), '') AS predicate,
		       idx.indnkeyatts AS key_count,
		       k.ord AS ord,
		       pg_get_indexdef(idx.indexrelid, k.ord::int, true) AS key_expr,
		       a.attname AS column_name
		FROM pg_class t
		JOIN pg_namespace n ON n.oid = t.relnamespace
		JOIN pg_index idx ON idx.indrelid = t.oid
		JOIN pg_class ic ON ic.oid = idx.indexrelid
		JOIN LATERAL generate_series(1, idx.indnkeyatts) AS k(ord) ON TRUE
		LEFT JOIN pg_attribute a
		       ON a.attrelid = t.oid
		      AND a.attnum = idx.indkey[k.ord - 1]
		      AND a.attnum <> 0
		WHERE n.nspname = $1
		  AND t.relname = $2
		  AND NOT idx.indisprimary
		ORDER BY ic.relname, k.ord`, schema, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var (
		order    []string
		byName   = map[string]*Index{}
		keyCount = map[string]int{}
	)
	for rows.Next() {
		var (
			idxName   string
			unique    bool
			predicate string
			keyAtts   int
			ord       int
			keyExpr   string
			colName   sql.NullString
		)
		if err := rows.Scan(&idxName, &unique, &predicate, &keyAtts, &ord, &keyExpr, &colName); err != nil {
			return nil, err
		}
		ix, ok := byName[idxName]
		if !ok {
			ix = &Index{Name: idxName, Unique: unique, Predicate: predicate}
			byName[idxName] = ix
			keyCount[idxName] = keyAtts
			order = append(order, idxName)
		}
		key := IndexKey{Expr: strings.TrimSpace(keyExpr)}
		if colName.Valid {
			// A bare column key: attnum resolved to a real column.
			key.Column = colName.String
			ix.Columns = append(ix.Columns, colName.String)
		} else {
			// An expression key (attnum 0, no pg_attribute row). Read the
			// case-fold form; anything else stays unattributed.
			key.Column, key.Fold = parseFoldedKey(key.Expr)
		}
		ix.Keys = append(ix.Keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	idxs := make([]Index, 0, len(order))
	for _, n := range order {
		ix := byName[n]
		// Fewer named columns than declared keys means the rest are
		// expressions. Columns then describes a PREFIX of the real key, so
		// mark it opaque rather than let a consumer read it as the whole.
		ix.Expression = len(ix.Columns) < keyCount[n]
		idxs = append(idxs, *ix)
	}
	return idxs, nil
}

// foldedKeyRE matches a case-folded single-column index key as postgres
// renders it: `lower(name)`, `upper((sku)::text)`, `lower(email::text)`.
// pg_get_indexdef parenthesises and casts freely, so the column is read
// out of the middle rather than by exact match.
var foldedKeyRE = regexp.MustCompile(`^(?i)(lower|upper)\(\s*\(?\s*"?([a-zA-Z_][a-zA-Z0-9_$]*)"?\s*\)?\s*(?:::[a-zA-Z_][a-zA-Z0-9_ ]*)?\s*\)$`)

// parseFoldedKey reads `lower(col)` / `upper(col)` and returns the column and
// the fold. Everything else returns ("", "") — an expression this package
// cannot attribute to one column is left unattributed rather than guessed at,
// because a wrong attribution is the failure this whole path exists to end.
func parseFoldedKey(expr string) (column, fold string) {
	m := foldedKeyRE.FindStringSubmatch(strings.TrimSpace(expr))
	if m == nil {
		return "", ""
	}
	return m[2], strings.ToLower(m[1])
}

// introspectForeignKeys returns the declared REFERENCES constraints of a
// table via information_schema.
func introspectForeignKeys(ctx context.Context, db Queryer, schema, table string) ([]ForeignKey, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT kcu.column_name,
		       ccu.table_name AS ref_table,
		       ccu.column_name AS ref_column,
		       COALESCE(rc.delete_rule, '') AS delete_rule
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
		  ON tc.constraint_name = kcu.constraint_name
		  AND tc.table_schema = kcu.table_schema
		JOIN information_schema.constraint_column_usage ccu
		  ON tc.constraint_name = ccu.constraint_name
		  AND tc.table_schema = ccu.table_schema
		LEFT JOIN information_schema.referential_constraints rc
		  ON rc.constraint_name = tc.constraint_name
		  AND rc.constraint_schema = tc.table_schema
		WHERE tc.constraint_type = 'FOREIGN KEY'
		  AND tc.table_schema = $1
		  AND tc.table_name = $2
		ORDER BY kcu.ordinal_position`, schema, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var fks []ForeignKey
	for rows.Next() {
		var fk ForeignKey
		if err := rows.Scan(&fk.Column, &fk.RefTable, &fk.RefColumn, &fk.OnDelete); err != nil {
			return nil, err
		}
		if fk.RefColumn == "" {
			fk.RefColumn = "id"
		}
		fks = append(fks, fk)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	names, comments, err := introspectFKComments(ctx, db, schema, table)
	if err != nil {
		return nil, err
	}
	for i := range fks {
		fks[i].Name = names[fks[i].Column]
		fks[i].Comment = comments[fks[i].Column]
	}
	return fks, nil
}

// introspectTableComment returns a table's COMMENT ON TABLE text, "" when
// it carries none. A table with no comment returns no row from
// obj_description's join, so an empty result is the ordinary case and not
// an error.
func introspectTableComment(ctx context.Context, db Queryer, schema, table string) (string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT COALESCE(obj_description(cl.oid, 'pg_class'), '')
		FROM pg_class cl
		JOIN pg_namespace ns ON ns.oid = cl.relnamespace
		WHERE ns.nspname = $1 AND cl.relname = $2`, schema, table)
	if err != nil {
		return "", fmt.Errorf("read comment on table %s.%s: %w", schema, table, err)
	}
	defer func() { _ = rows.Close() }()

	var comment string
	if rows.Next() {
		if err := rows.Scan(&comment); err != nil {
			return "", err
		}
	}
	return comment, rows.Err()
}

// introspectTriggers returns the table's trigger names, sorted.
//
// Read from pg_trigger rather than information_schema.triggers because the
// latter emits one ROW PER EVENT — a `BEFORE UPDATE OR DELETE` guard
// appears twice under the same name — and a caller asking "is the guard
// installed" should not have to de-duplicate the catalog's shape. Internal
// constraint triggers (tgisinternal: the ones postgres creates to enforce
// foreign keys) are excluded; they are an implementation detail of a
// constraint already reported in ForeignKeys.
func introspectTriggers(ctx context.Context, db Queryer, schema, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT tg.tgname
		FROM pg_trigger tg
		JOIN pg_class cl ON cl.oid = tg.tgrelid
		JOIN pg_namespace ns ON ns.oid = cl.relnamespace
		WHERE ns.nspname = $1 AND cl.relname = $2 AND NOT tg.tgisinternal
		ORDER BY tg.tgname`, schema, table)
	if err != nil {
		return nil, fmt.Errorf("read triggers on %s.%s: %w", schema, table, err)
	}
	defer func() { _ = rows.Close() }()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// introspectFKComments returns, per constrained column, the foreign-key
// constraint's name and its catalog comment.
//
// It reads pg_catalog rather than information_schema because a COMMENT lives
// in pg_description and information_schema exposes no path to it. Kept as a
// separate query so the reference resolution above — which several unrelated
// consumers depend on — stays exactly as it was.
//
// Single-column keys only: `attnum = ANY(conkey)` would attribute a composite
// key's comment to each of its columns, and every reference forge models (and
// every one the seeder can resolve) is single-column.
func introspectFKComments(ctx context.Context, db Queryer, schema, table string) (names, comments map[string]string, err error) {
	rows, err := db.QueryContext(ctx, `
		SELECT att.attname,
		       con.conname,
		       COALESCE(obj_description(con.oid, 'pg_constraint'), '')
		FROM pg_constraint con
		JOIN pg_class cl ON cl.oid = con.conrelid
		JOIN pg_namespace ns ON ns.oid = cl.relnamespace
		JOIN pg_attribute att ON att.attrelid = con.conrelid AND att.attnum = con.conkey[1]
		WHERE con.contype = 'f'
		  AND array_length(con.conkey, 1) = 1
		  AND ns.nspname = $1
		  AND cl.relname = $2`, schema, table)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()

	names, comments = map[string]string{}, map[string]string{}
	for rows.Next() {
		var col, name, comment string
		if err := rows.Scan(&col, &name, &comment); err != nil {
			return nil, nil, err
		}
		names[col] = name
		comments[col] = comment
	}
	return names, comments, rows.Err()
}

// MapDeclaredType maps a declared SQL column type to the canonical
// forge type. isArray is true for array types ("TEXT[]"), in which case
// the canonical type describes the ELEMENT.
//
// Mapping table (case-insensitive, length/precision suffixes ignored):
//
//	TEXT, VARCHAR, CHAR, CITEXT, UUID            → string
//	BIGINT, INTEGER, INT, SMALLINT, *SERIAL      → int64
//	DOUBLE PRECISION, REAL, FLOAT, NUMERIC, DECIMAL → float64
//	BOOLEAN, BOOL                                → bool
//	TIMESTAMPTZ, TIMESTAMP[ WITH(OUT) TIME ZONE], DATE, DATETIME → time
//	JSONB, JSON                                  → json
//	BYTEA, BLOB                                  → bytes
//
// known is FALSE for anything else — MONEY, INET, INTERVAL, TSVECTOR,
// a native pg enum, an empty udt. The canonical type is TypeString in
// that case, and callers must treat it as a FALLBACK rather than a
// mapping: postgres will not accept arbitrary text into any of those
// columns, so a `string` field projected from one is a wire contract
// that accepts what the database rejects.
//
// The distinction is the whole point of the third return. Without it
// every unknown arrived indistinguishable from a real TEXT column, and
// downstream guards written to catch exactly this could not fire —
// the loss happened here, upstream of them, in silence.
func MapDeclaredType(decl string) (canonical CanonicalType, isArray bool, known bool) {
	d := strings.ToUpper(strings.TrimSpace(decl))
	if strings.HasSuffix(d, "[]") {
		isArray = true
		d = strings.TrimSpace(strings.TrimSuffix(d, "[]"))
	}
	// Strip length/precision: VARCHAR(255), NUMERIC(10,2).
	if p := strings.IndexByte(d, '('); p >= 0 {
		d = strings.TrimSpace(d[:p])
	}
	if c, ok := declaredSQLTypes[d]; ok {
		return c, isArray, true
	}
	return TypeString, isArray, false
}

// declaredSQLTypes is the CLOSED set of declared SQL type spellings forge
// maps, keyed uppercase with length/precision already stripped.
//
// It is a table rather than a switch so that CanonicalTypes below can be
// its VALUE SET rather than a second list of the same seven names.
var declaredSQLTypes = map[string]CanonicalType{
	"TEXT": TypeString, "VARCHAR": TypeString, "CHARACTER VARYING": TypeString,
	"CHAR": TypeString, "CHARACTER": TypeString, "CITEXT": TypeString, "UUID": TypeString,

	"BIGINT": TypeInt, "INTEGER": TypeInt, "INT": TypeInt, "INT2": TypeInt,
	"INT4": TypeInt, "INT8": TypeInt, "SMALLINT": TypeInt, "MEDIUMINT": TypeInt,
	"TINYINT": TypeInt, "BIGSERIAL": TypeInt, "SERIAL": TypeInt, "SMALLSERIAL": TypeInt,

	"DOUBLE PRECISION": TypeFloat, "DOUBLE": TypeFloat, "REAL": TypeFloat,
	"FLOAT": TypeFloat, "FLOAT4": TypeFloat, "FLOAT8": TypeFloat,
	"NUMERIC": TypeFloat, "DECIMAL": TypeFloat,

	"BOOLEAN": TypeBool, "BOOL": TypeBool,

	"TIMESTAMPTZ": TypeTime, "TIMESTAMP": TypeTime,
	"TIMESTAMP WITH TIME ZONE": TypeTime, "TIMESTAMP WITHOUT TIME ZONE": TypeTime,
	"DATE": TypeTime, "DATETIME": TypeTime,

	"JSONB": TypeJSON, "JSON": TypeJSON,

	"BYTEA": TypeBytes, "BLOB": TypeBytes,
}

// CanonicalTypes returns the canonical type vocabulary, sorted — derived as
// the VALUE SET of the declared-SQL-type table, never as a restatement of
// the Type* constants.
//
// Deriving it rather than listing it is what makes it honest in both
// directions. A canonical type no declared SQL type maps to is a type no
// column can ever carry, so it does not belong in an obligation downstream
// projections are held to; and a canonical type introduced with a mapping
// appears here the moment the mapping does, which is what fails the
// projection tests that derive their coverage from this function instead of
// carrying a list someone has to remember to update.
func CanonicalTypes() []CanonicalType {
	seen := map[CanonicalType]bool{}
	for _, c := range declaredSQLTypes {
		seen[c] = true
	}
	out := make([]CanonicalType, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
