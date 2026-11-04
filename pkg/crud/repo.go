package crud

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/schema"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// repoTracer is the span source for the generic repository. The pre-generic
// per-entity ORM emitted one tracer per package ("orm"); the library keeps
// the same tracer name so existing dashboards/queries that filter on the
// "orm" tracer and the "orm.<Op><Entity>" span names keep working.
var repoTracer = otel.Tracer("orm")

// Everything a Repo needs is DERIVED from the Bun-tagged model at first use
// (once per entity, never in the hot path): table name, primary key column +
// Go field, server-allocated PK, soft delete in both its native and legacy
// forms, managed timestamps, array columns, the full column set, and the
// per-column write policy carried by Bun's ,skipupdate tag.
//
// There is no per-entity descriptor to pass, and that is the point. A
// descriptor would be a second copy of facts the struct already carries —
// checked against nothing, silently wrong when the two drifted. The struct
// tags are the projection of the applied schema, so reading them IS reading
// the schema.
//
// The one thing reflection cannot answer is where a column's write policy
// comes from: `forge:immutable` is declared in the migration (COMMENT ON
// COLUMN) and projected onto the tag by the generator. The repo reads that
// tag itself in ensureMeta rather than letting Bun enforce it at write time —
// Bun's own enforcement is unconditional and would also block the masked
// path, which must be able to write the column on an explicit, deliberate
// mask (see updatable vs updatableSet below).
//
// The same is true of `forge:version`, the optimistic-concurrency column,
// with one extra wrinkle: Bun has no native concept for it at all, so the
// generator marks it in a SECOND tag namespace (`forge:"version"`) that
// Bun's tag parser never inspects. See ensureMeta for why that beats
// inventing a private bun tag option.

// meta is the reflection-derived, cached half of a Repo's knowledge. It is
// computed once (sync.Once, off the first IDB the repo sees) from Bun's
// table schema for Model and never recomputed — Bun itself caches the
// *schema.Table per type, so even the first computation is a single schema
// build shared process-wide.
type meta struct {
	table         string // SQL table name
	entityName    string // model Go type name (for span names)
	entityLabel   string // the entity in a not-found error: entityName, lower-cased
	pkColumn      string // primary-key column name ("" for a composite key)
	pkGoField     string // primary-key struct field (Go) name
	pkAutoInc     bool   // server-allocated PK (,autoincrement: identity or DEFAULT) → RETURNING
	pkIsString    bool   // string PK → ULID-generate when empty on Create, unless pkAutoInc
	nativeSoftDel bool   // Bun owns soft delete (,soft_delete time column)
	// legacyTextSoftDel: a deleted_at column Bun's ,soft_delete did not
	// claim (a TEXT column it cannot round-trip a time.Time through), so
	// the repo hand-rolls the IS NULL filter and the stamp.
	legacyTextSoftDel bool
	// timestamps: created_at AND updated_at both exist and both project to
	// a stampable type — forge's managed-timestamp convention, read off the
	// columns rather than declared.
	timestamps   bool
	columns      []string // ordered declared column allowlist
	hasUpdatedAt bool     // updated_at column exists (managed-stamp target)
	nilSliceCols []sliceCol
	updatable    []string // columns settable by a full Update (SET clause)
	updatableSet map[string]bool
	skipUpdate   map[string]int        // ,skipupdate column → struct field index; see UpdateMasked
	stampFields  map[string]stampField // created_at/updated_at → field info
	// pkColumns are every primary-key column, in key order: one for a
	// single-column key, several for a composite one. A composite-key row
	// is addressed by all of them at once (Bun's WherePK over a key
	// entity) — see GetByPK, DeleteByPK and wherePK.
	pkColumns []string
	// versionColumn is the SQL name of the `forge:version` column, "" when
	// the entity declared none. A non-empty value is what switches
	// Update/UpdateMasked from last-writer-wins to optimistic concurrency
	// control; an entity without one never builds the predicate and never
	// issues the disambiguating re-query, so it costs exactly one string
	// comparison per write.
	versionColumn string
	// versionFieldIndex is the struct-field index of that column, used to
	// read the caller's last-seen value for the WHERE predicate.
	versionFieldIndex int
	// fillULIDFields are the struct-field indices of columns declared
	// `forge:fill=ulid` (a NON-PK column — the PK's own ULID generation is
	// pkIsString above). Create ULID-generates each when the caller left it
	// at its Go zero (empty string), the same chokepoint and same
	// empty-means-unset convention as the PK.
	fillULIDFields []int
	// generatedCols are the SQL names of `forge:generated` columns —
	// GENERATED ALWAYS AS (...) STORED, computed by the database. Create
	// and Upsert ExcludeColumn each of them so the INSERT never names one,
	// which postgres rejects outright. columnExcludedFromSet also bars them
	// from both update allowlists.
	//
	// A write-set exclusion rather than reliance on the struct's own
	// ,nullzero, because nullzero keys off the GO zero: an entity read back
	// from the database holds the real computed value, and re-inserting it
	// would send that value literally. See generatedTagValue.
	generatedCols []string
	// insertDefaultable are the fields Bun may send as DEFAULT on INSERT —
	// a nil pointer, or a Go zero on a ,nullzero or ,default: field — so
	// the DATABASE picks the stored value. Create and Upsert RETURN each one
	// that did, so the caller's entity holds what was stored rather than
	// the zero it sent. See returnDatabaseFilled.
	insertDefaultable []*schema.Field
	// unsettable are the NOT NULL fields whose TYPE can say "unset": a
	// pointer (nil), or a ,nullzero field (its zero). The generator emits
	// those for a column whose DEFAULT is not its Go zero, or whose zero is
	// never a stored value (codegen.ColumnInsertDefault). Unset means the
	// same thing on every write: Create omits the column so its DEFAULT
	// fires (Bun's own rule), a full-replace Update and an Upsert's
	// conflict branch leave the stored value alone. See withoutUnset.
	unsettable []*schema.Field
	// fields maps each column to its Bun field, for rendering a SET value
	// as a bind parameter (see bind.go).
	fields map[string]*schema.Field
	// softDeleteColumn is the column Bun's native ,soft_delete owns ("" when
	// there is none); Delete stamps it itself so the stamp can be a bind
	// parameter.
	softDeleteColumn string
	// bindParams: the by-PK statements run with bind parameters through the
	// raw passthrough. False only for a model implementing a bun model
	// hook, which bun's own executor must call. See bind.go.
	bindParams bool
}

// sliceCol pairs a NOT NULL slice-typed column's struct-field index with
// its SQL name so the repo can nil-normalize it (and, for masked writes,
// only when named).
//
// In Go a nil slice binds as SQL NULL, which a NOT NULL column rejects — so
// any such column left unset fails the INSERT outright. That is true of the
// `repeated <scalar>` columns forge has always emitted (`TEXT[] NOT NULL
// DEFAULT '{}'`) and equally of a `bytes` column (`BYTEA NOT NULL DEFAULT
// '\x'`), which is a slice field WITHOUT the `,array` tag and so was missed
// while `bytes` was a shape the generator could not produce at all.
//
// Selected on NOT NULL rather than on the tag: for a NULLABLE slice column
// nil IS the absent value, and normalizing it would erase the distinction
// between "no value" and "empty value" that the column was declared to keep.
type sliceCol struct {
	fieldIndex int
	column     string
}

// stampField records how to set a managed-timestamp column: its struct
// field index, whether it is a pointer field, and whether its Go type is
// string (RFC3339Nano) or time.Time. A column whose type the repo can't
// stamp is absent from the map (left untouched, matching the generator's
// stampableTimestamp guard).
type stampField struct {
	index    int
	isPtr    bool
	isString bool
}

// Repo is the generic data-access layer over a Bun-tagged model M. One
// Repo per entity replaces the ~250 LOC of per-entity Create/Get/List/
// Count/ListAll/Update/UpdateMasked/Delete the generator used to emit; the
// generated code now supplies only the Bun-tagged struct, the ToProto/
// FromProto pair, and a single crud.NewRepo[Model]() line.
//
// All lifecycle semantics the pre-generic code carried are preserved
// exactly: Bun-native + legacy-TEXT soft delete, the
// deleted_at IS NULL update-guard (Bun auto-scopes SELECT/DELETE to live
// rows but NOT UPDATE), AIP-134 masked updates with an updatable allowlist
// → orm.UnknownFieldError, managed-timestamp stamping, server-allocated /
// ULID PKs, and array nil→{} normalization. The QueryOption escape hatch
// (orm.QueryOption func(*bun.SelectQuery)) is threaded straight through to
// List/Count, and bun.IDB (db.Bun()) remains the raw-SQL escape hatch.
type Repo[M any] struct {
	once sync.Once
	m    meta
}

// NewRepo constructs a Repo for model M. Metadata derivation is deferred to
// the first call, which needs a live bun.IDB to reach the dialect's table
// cache; everything it needs comes off the model's own Bun tags.
func NewRepo[M any]() *Repo[M] {
	return &Repo[M]{}
}

// modelType returns the (dereferenced) struct type of M.
func (r *Repo[M]) modelType() reflect.Type {
	var zero M
	typ := reflect.TypeOf(zero)
	if typ == nil {
		// M is an interface or pointer with a nil zero value; fall back to
		// the element type of *M.
		typ = reflect.TypeOf((*M)(nil)).Elem()
	}
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	return typ
}

// ensureMeta derives and caches the reflection metadata from Bun's schema
// for M, the first time the repo is handed a database handle. Bun caches
// the *schema.Table per type process-wide, so this is one schema build per
// entity regardless of how many Repos or calls reference it.
//
// # Finding the optimistic-concurrency column
//
// Every other fact here is read off Bun's own parsed tag, because Bun has
// a native concept for it (f.SkipUpdate, f.IsPK, tbl.SoftDeleteField).
// Optimistic concurrency is the one fact Bun has no vocabulary for, so the
// generator declares it in a SEPARATE struct-tag namespace and this reads
// it back off the raw reflect.StructField Bun keeps beside its own tag:
//
//	Version int64 `bun:"version,notnull,skipupdate" forge:"version"`
//
// The alternative — an invented bun option, `bun:"version,occ"` — was
// rejected because Bun validates its own option vocabulary
// (schema.isKnownFieldOption) and logs "unknown tag option" for anything
// outside it, so every generated struct carrying one would print a warning
// forge cannot suppress and users cannot act on. A separate namespace is
// also the honest description: this is forge's declaration, not Bun's, and
// nothing about it should look like a feature of the query engine.
//
// Deriving it from the column NAME instead (any column called "version")
// was rejected for the opposite reason: "version" is an ordinary domain
// word — a document's revision label, a schema_version, an app's semver
// string — and silently adding a WHERE predicate to a table because a
// column's name matched would break writes on schemas that never asked for
// OCC. The marker is opt-in precisely so it cannot be triggered by accident.
func (r *Repo[M]) ensureMeta(db orm.Context) {
	r.once.Do(func() {
		typ := r.modelType()
		tbl := db.Bun().Dialect().Tables().Get(typ)

		r.m.table = tbl.Name
		r.m.entityName = typ.Name()
		r.m.entityLabel = strings.ToLower(typ.Name())
		r.m.columns = make([]string, 0, len(tbl.Fields))
		r.m.stampFields = map[string]stampField{}
		r.m.fields = make(map[string]*schema.Field, len(tbl.Fields))
		r.m.bindParams = !hasModelHooks(typ)

		// Managed timestamps are a property of the COLUMNS, so they are read
		// off the columns rather than declared: forge stamps created_at and
		// updated_at only when both exist AND both project to a type the repo
		// can actually stamp. An exotic pair (epoch integers, arrays) is plain
		// schema, and stampFieldFor is the same gate the generator applies.
		//
		// Deriving it here rather than accepting it as a flag keeps one answer
		// to the question. A flag would be a second copy of a fact the struct
		// already carries, and the two could disagree.
		var created, updated *schema.Field
		for _, f := range tbl.Fields {
			switch f.Name {
			case "created_at":
				created = f
			case "updated_at":
				updated = f
			}
		}
		if created != nil && updated != nil {
			_, createdOK := stampFieldFor(created)
			_, updatedOK := stampFieldFor(updated)
			r.m.timestamps = createdOK && updatedOK
		}

		for _, f := range tbl.Fields {
			r.m.columns = append(r.m.columns, f.Name)
			r.m.fields[f.Name] = f
			// A ,nullzero slice's empty value is "unset" (its DEFAULT, or
			// the stored value), not the empty array, so it is left alone.
			if f.NotNull && f.IndirectType.Kind() == reflect.Slice && !f.NullZero {
				r.m.nilSliceCols = append(r.m.nilSliceCols, sliceCol{fieldIndex: f.Index[0], column: f.Name})
			}
			if f.Name == "updated_at" {
				r.m.hasUpdatedAt = true
			}
			if r.m.timestamps && (f.Name == "created_at" || f.Name == "updated_at") {
				if sf, ok := stampFieldFor(f); ok {
					r.m.stampFields[f.Name] = sf
				}
			}
		}
		for _, pk := range tbl.PKs {
			r.m.pkColumns = append(r.m.pkColumns, pk.Name)
		}
		switch len(tbl.PKs) {
		case 1:
			pk := tbl.PKs[0]
			r.m.pkColumn = pk.Name
			r.m.pkGoField = pk.GoName
			r.m.pkAutoInc = pk.AutoIncrement || pk.Identity
			r.m.pkIsString = pk.IndirectType.Kind() == reflect.String
		case 0:
			r.m.pkColumn = "id"
			r.m.pkGoField = "Id"
			r.m.pkIsString = true
		default:
			// Composite PK (bun's tbl.PKs has more than one member): auto id
			// generation and PK-cursor pagination are only meaningful for a
			// single key column, so both are disabled rather than guessing
			// off PKs[0]. Guessing is exactly the bug this guards — a table
			// like PRIMARY KEY (company_id, kind) where company_id is ALSO
			// a foreign key would otherwise report pkIsString=true for
			// company_id, and Create's ULID-on-empty branch below would
			// overwrite the FK with a fresh ULID, silently corrupting the
			// reference. Leaving pkColumn "" is the existing, correct
			// escape hatch orderKeysetSafe already treats as "no PK-cursor
			// pagination" (see pkg/crud/crud.go); pkGoField/pkAutoInc/
			// pkIsString stay at their zero values so pkFieldValue and the
			// ULID branch are never reached for these entities.
			r.m.pkColumn = ""
		}
		if tbl.SoftDeleteField != nil {
			r.m.nativeSoftDel = true
			r.m.softDeleteColumn = tbl.SoftDeleteField.Name
		}

		// The legacy TEXT form: a deleted_at Bun's ,soft_delete cannot own
		// (it stamps a time.Time a TEXT column cannot round-trip), DECLARED
		// with forge's soft-delete tag, so the repo hand-rolls the filter and
		// the stamp. Declared rather than inferred from the column's name:
		// a deleted_at is also how a schema spells an archive stamp
		// (`forge:no-soft-delete`), and a repo that soft-deleted every
		// column called deleted_at hid archived rows and turned Delete into
		// an UPDATE behind a declaration that said otherwise.
		if !r.m.nativeSoftDel {
			for _, f := range tbl.Fields {
				if softDeleteTagged(f) && f.Name == "deleted_at" {
					r.m.legacyTextSoftDel = true
					break
				}
			}
		}

		// The optimistic-concurrency column, if the entity declared one.
		// Resolved BEFORE the allowlists below, which exclude it by name
		// through columnExcludedFromSet.
		//
		// Only the FIRST such column is honored. Two version columns cannot
		// both be authoritative — a write satisfying one predicate and
		// failing the other has no coherent answer — and silently checking
		// only one while incrementing both would be worse than either. The
		// generator cannot produce this shape (a second forge:version marker
		// is a migration authoring mistake), so the recovery is to pick
		// deterministically rather than to panic in a library that runs
		// inside somebody's request path.
		for _, f := range tbl.Fields {
			if versionTagged(f) {
				r.m.versionColumn = f.Name
				r.m.versionFieldIndex = f.Index[0]
				break
			}
		}

		// forge:fill=ulid columns: every one of them, not just the first —
		// unlike forge:version (a single predicate slot) there is no
		// conflict in generating more than one ULID per row (an invite
		// code AND a share token, say).
		for _, f := range tbl.Fields {
			if fillULIDTagged(f) {
				r.m.fillULIDFields = append(r.m.fillULIDFields, f.Index[0])
			}
		}

		// Generated columns, resolved BEFORE the allowlists below (which
		// exclude them by name through columnExcludedFromSet) and read by
		// Create/Upsert to ExcludeColumn each from the INSERT.
		for _, f := range tbl.Fields {
			if generatedTagged(f) {
				r.m.generatedCols = append(r.m.generatedCols, f.Name)
			}
		}

		// Columns the database may fill on INSERT. The PK is handled on its
		// own (server-allocated → always returned; string → ULID-filled
		// here), and generated columns are always returned, so neither is
		// listed.
		for _, f := range tbl.Fields {
			if f.IsPK || generatedTagged(f) {
				continue
			}
			if f.IsPtr || f.NullZero || f.SQLDefault != "" {
				r.m.insertDefaultable = append(r.m.insertDefaultable, f)
			}
			if f.NotNull && (f.IsPtr || f.NullZero) {
				r.m.unsettable = append(r.m.unsettable, f)
			}
		}

		// Two distinct allowlists, both starting from every declared column
		// EXCEPT the PK, deleted_at, the version column, and — under managed
		// timestamps — created_at and updated_at (the latter is repo-stamped,
		// never caller-set):
		//
		//   - updatableSet — the MASKED-update allowlist: a path an
		//     update_mask may name. A ,skipupdate column IS here, because a
		//     mask that names it is the caller deliberately asserting a new
		//     value (Create's sibling write path).
		//   - updatable    — the FULL-REPLACE SET list. A ,skipupdate column
		//     is EXCLUDED here: Bun's own ,skipupdate enforcement is
		//     unconditional (it would drop the column from a masked SET too,
		//     see UpdateMasked), so the repo applies forge's conditional
		//     rule itself instead of delegating to Bun.
		r.m.updatableSet = make(map[string]bool, len(tbl.Fields))
		r.m.skipUpdate = make(map[string]int)
		for _, f := range tbl.Fields {
			if r.columnExcludedFromSet(f.Name) {
				continue
			}
			r.m.updatableSet[f.Name] = true
			if f.SkipUpdate() {
				r.m.skipUpdate[f.Name] = f.Index[0]
				continue
			}
			r.m.updatable = append(r.m.updatable, f.Name)
		}
	})
}

// stampFieldFor builds the stampField for a managed-timestamp Bun field, or
// returns ok=false when its Go type isn't one the repo can stamp (matching
// the generator's stampableTimestamp: time.Time or string, plus their
// nullable pointer variants).
func stampFieldFor(f *schema.Field) (stampField, bool) {
	t := f.IndirectType
	switch t.Kind() {
	case reflect.String:
		return stampField{index: f.Index[0], isPtr: f.IsPtr, isString: true}, true
	case reflect.Struct:
		if t == reflect.TypeOf(time.Time{}) {
			return stampField{index: f.Index[0], isPtr: f.IsPtr, isString: false}, true
		}
	}
	return stampField{}, false
}

// forgeTagKey is the struct-tag namespace forge uses for declarations Bun
// has no vocabulary for. Separate from `bun:"..."` so Bun's tag parser
// never sees it (see ensureMeta).
const forgeTagKey = "forge"

// versionTagValue is the forge-tag value marking the optimistic-concurrency
// column. It matches the `forge:version` catalog-comment marker the user
// writes in the migration (pkg/schemadef.ColumnMarkerVersion), minus
// the `forge:` prefix the tag namespace already supplies.
const versionTagValue = "version"

// fillULIDTagValue is the forge-tag value marking a `forge:fill=ulid`
// column: a non-PK column the generic Repo ULID-generates at Create when
// the caller left it at its Go zero (empty string), the same convention
// as the PK's own ULID-on-empty behavior below.
const fillULIDTagValue = "fill=ulid"

// generatedTagValue is the forge-tag value marking a
// GENERATED ALWAYS AS (...) STORED column: one the DATABASE computes, and
// which postgres refuses to see named in an INSERT column list or an
// UPDATE SET clause.
//
// The generated struct also carries Bun's ,nullzero for it, which covers
// the ordinary insert — a zero-valued nullzero field marshals to the
// literal DEFAULT keyword, which postgres accepts. But ,nullzero keys off
// the GO zero value, so it cannot cover the case that actually bites: an
// entity READ back from the database holds the real computed value, and
// re-inserting it (an idempotent ingest, a seed-or-update, any
// read-modify-write) would send that non-zero value literally and be
// rejected. Bun has no tag option for "never name this column in a write",
// so the repo drops it from the write set itself — see the ExcludeColumn
// calls in Create and Upsert.
//
// NOT derivable from Bun's schema: Bun models a generated column as an
// ordinary field, and the ,scanonly option that superficially fits is the
// bug this replaced (it removes the field from Table.Fields entirely, so
// SELECT stops projecting the column and every read returns the Go zero).
const generatedTagValue = "generated"

// softDeleteTagValue is the forge-tag value marking a legacy TEXT deleted_at
// the repo soft-deletes by hand (Bun's ,soft_delete owns the time-typed
// form). Only a soft-delete deleted_at carries it; one declared
// `forge:no-soft-delete` (pkg/schemadef.ColumnMarkerNoSoftDelete) is an
// ordinary column.
const softDeleteTagValue = "soft-delete"

// versionTagged reports whether a Bun field carries forge's version tag.
// Read off the raw StructField rather than Bun's parsed Tag: the value
// lives in forge's own tag namespace, which Bun does not parse.
func versionTagged(f *schema.Field) bool {
	return f.StructField.Tag.Get(forgeTagKey) == versionTagValue
}

// fillULIDTagged reports whether a Bun field carries forge's fill=ulid tag.
func fillULIDTagged(f *schema.Field) bool {
	return f.StructField.Tag.Get(forgeTagKey) == fillULIDTagValue
}

// softDeleteTagged reports whether a Bun field carries forge's soft-delete
// tag: the legacy TEXT deleted_at the repo soft-deletes by hand.
func softDeleteTagged(f *schema.Field) bool {
	return f.StructField.Tag.Get(forgeTagKey) == softDeleteTagValue
}

// generatedTagged reports whether a Bun field carries forge's generated tag.
func generatedTagged(f *schema.Field) bool {
	return f.StructField.Tag.Get(forgeTagKey) == generatedTagValue
}

// columnIsGenerated reports whether col is a GENERATED ALWAYS AS (...)
// STORED column, resolved from the struct's forge tags in ensureMeta.
//
// Read by columnExcludedFromSet, which runs DURING the same ensureMeta pass
// that populates generatedCols — the generated-column loop deliberately
// precedes the allowlist loop so this lookup is already answerable.
func (r *Repo[M]) columnIsGenerated(col string) bool {
	return slices.Contains(r.m.generatedCols, col)
}

// excludeGeneratedColumns drops every generated column from an INSERT's
// column list.
//
// Necessary in ADDITION to the struct's own ,nullzero: nullzero keys off
// the GO zero value, so it emits DEFAULT only while the field is unset. An
// entity read back from the database carries the real computed value, and
// re-inserting that entity would name the column with a literal value —
// which postgres rejects outright for a generated column. Excluding it by
// name makes the write correct whatever the struct happens to hold.
func (r *Repo[M]) excludeGeneratedColumns(q *bun.InsertQuery) *bun.InsertQuery {
	for _, col := range r.m.generatedCols {
		q = q.ExcludeColumn(col)
	}
	return q
}

// returnDatabaseFilled makes an INSERT read back, via RETURNING, every
// column whose stored value the DATABASE chose: a server-allocated PK, every
// generated column, and each column this entity is about to send as DEFAULT.
// Bun scans the returned row into entity, so after Create a caller holds the
// stored row — `line_total_cents` computed, `status` defaulted — without the
// re-read every hand-written caller otherwise adds.
//
// The list is explicit rather than `RETURNING *`: a column added by a
// migration that is applied before the code that knows about it (the expand
// half of a rolling deploy) would fail every insert with "does not have
// column". Naming only the struct's own columns keeps writes working across
// that window, exactly as the SELECT projection does.
//
// Bun has its own RETURNING for DEFAULT-sent fields, but it switches itself
// off as soon as any explicit Returning is present — and generated columns
// are excluded from the column list, so Bun never sees them to return. The
// repo therefore states the whole list itself.
func (r *Repo[M]) returnDatabaseFilled(q *bun.InsertQuery, entity *M) *bun.InsertQuery {
	cols := make([]string, 0, 1+len(r.m.generatedCols)+len(r.m.insertDefaultable))
	if r.m.pkAutoInc {
		cols = append(cols, r.m.pkColumn)
	}
	cols = append(cols, r.m.generatedCols...)
	v := reflect.ValueOf(entity).Elem()
	for _, f := range r.m.insertDefaultable {
		// The same predicate Bun applies when it writes DEFAULT for the
		// field (InsertQuery.marshalsToDefault).
		if (f.IsPtr && f.HasNilValue(v)) || (f.HasZeroValue(v) && (f.NullZero || f.SQLDefault != "")) {
			cols = append(cols, f.Name)
		}
	}
	for _, col := range cols {
		q = q.Returning("?", bun.Ident(col))
	}
	return q
}

// returnGenerated makes an UPDATE read back every generated column. A
// generated column is recomputed from the row's other columns, so after
// Update or UpdateMasked the value the caller holds is stale whenever a
// column it depends on changed — and the dependency may be on a column the
// mask did not even name. Nothing else the UPDATE writes comes back: every
// other column holds exactly what the caller sent (updated_at is stamped in
// Go, the version column is advanced by advanceVersion). The columns the
// UPDATE does NOT write come back too, via readBackUnwritten.
//
// It reports whether it added any RETURNING column.
func (r *Repo[M]) returnGenerated(q *bun.UpdateQuery) bool {
	for _, col := range r.m.generatedCols {
		q.Returning("?", bun.Ident(col))
	}
	return len(r.m.generatedCols) > 0
}

// isSoftDeleteColumn reports whether col is the column this repo
// soft-deletes by — Bun's native ,soft_delete column, or the tagged legacy
// TEXT deleted_at. A deleted_at that is neither is an ordinary column: the
// caller writes it, and reads and deletes ignore it.
func (r *Repo[M]) isSoftDeleteColumn(col string) bool {
	switch {
	case r.m.nativeSoftDel:
		return col == r.m.softDeleteColumn
	case r.m.legacyTextSoftDel:
		return col == "deleted_at"
	}
	return false
}

// columnExcludedFromSet mirrors the generator's excludedFromSet: columns
// that never appear in an UPDATE SET clause a CALLER controls.
//
// The version column is excluded for the same reason the PK and created_at
// are: the repo owns its value. It is stricter than ,skipupdate, which
// only governs the full-replace path — a version column is barred from the
// masked path too, so an update_mask naming it is an UnknownFieldError
// rather than a way to hand-pick the version a write claims to have read.
// Letting a client set it would make the predicate self-satisfying, which
// is the whole guarantee gone.
func (r *Repo[M]) columnExcludedFromSet(col string) bool {
	if col == r.m.pkColumn || r.isSoftDeleteColumn(col) || slices.Contains(r.m.pkColumns, col) {
		return true
	}
	if r.m.versionColumn != "" && col == r.m.versionColumn {
		return true
	}
	// A generated column is excluded from BOTH allowlists, like the version
	// column and for a stronger reason: postgres rejects it in a SET clause,
	// so a mask naming it could not be honored by any means. Barring it from
	// updatableSet turns that mask into an UnknownFieldError — a legible 400
	// — instead of a database error surfacing from deep inside the write.
	if r.columnIsGenerated(col) {
		return true
	}
	if r.m.timestamps && (col == "created_at" || col == "updated_at") {
		return true
	}
	return false
}

// Columns is the entity's declared column allowlist — the value the List
// handler shim used to pull from the generated db.<Entity>Columns var and
// hand to pkg/crud for order_by validation. Derived from Bun's schema.
func (r *Repo[M]) Columns(db orm.Context) []string {
	r.ensureMeta(db)
	return r.m.columns
}

// PkColumn is the primary-key column name (the List handler's cursor
// column). Derived from Bun's schema.
func (r *Repo[M]) PkColumn(db orm.Context) string {
	r.ensureMeta(db)
	return r.m.pkColumn
}

// startSpan opens a child span named like the pre-generic per-entity ORM
// ("orm.<Op><Entity>") so existing traces/dashboards keep their span names.
//
// Only under a parent, as observe/dbtrace does for the SQL beneath it: a call
// made with no span in ctx (a background loop with no observe.RunRoot, a
// write detached with context.Background()) would otherwise be a one-span
// root trace that says which table was touched and nothing about why — in
// Reliant's worker, a stream of parentless `orm.CreateStepExecution` traces.
// The returned span is then ctx's own non-recording one, safe to End.
func (r *Repo[M]) startSpan(ctx context.Context, op string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return ctx, trace.SpanFromContext(ctx)
	}
	name := "orm." + op + r.m.entityName
	all := append([]attribute.KeyValue{attribute.String("table", r.m.table)}, attrs...)
	return repoTracer.Start(ctx, name, trace.WithAttributes(all...))
}

func recordErr(span trace.Span, err error) {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

// normalizeArrays sets every nil NOT NULL slice field to a non-nil empty
// slice so it binds as the empty value the column's DEFAULT already holds
// (`{}` for an array, `\x` for BYTEA) rather than as NULL.
func (r *Repo[M]) normalizeArrays(entity *M) {
	if len(r.m.nilSliceCols) == 0 {
		return
	}
	v := reflect.ValueOf(entity).Elem()
	for _, sc := range r.m.nilSliceCols {
		normalizeSlice(v.Field(sc.fieldIndex))
	}
}

// normalizeArraysFor normalizes only the slice fields named in mask.
func (r *Repo[M]) normalizeArraysFor(entity *M, mask map[string]bool) {
	if len(r.m.nilSliceCols) == 0 {
		return
	}
	v := reflect.ValueOf(entity).Elem()
	for _, sc := range r.m.nilSliceCols {
		if mask[sc.column] {
			normalizeSlice(v.Field(sc.fieldIndex))
		}
	}
}

func normalizeSlice(f reflect.Value) {
	if f.Kind() == reflect.Slice && f.IsNil() && f.CanSet() {
		f.Set(reflect.MakeSlice(f.Type(), 0, 0))
	}
}

// pkFieldValue returns the addressable PK struct field of entity.
func (r *Repo[M]) pkFieldValue(entity *M) reflect.Value {
	return reflect.ValueOf(entity).Elem().FieldByName(r.m.pkGoField)
}

// fillULIDColumns generates a ULID for every `forge:fill=ulid` column left
// at its Go zero (empty string) — the same empty-means-unset convention
// pkIsString uses for the PK. Called only from Create, matching where the
// PK's own ULID generation lives: Upsert deliberately does NOT ULID-generate
// its PK (see that method's doc comment), and the same reasoning against
// silent rotation applies here — an Upsert whose DO UPDATE branch ran this
// on every call would stomp an existing row's real token with a fresh one
// whenever the caller's struct left the field zero.
func (r *Repo[M]) fillULIDColumns(entity *M) {
	if len(r.m.fillULIDFields) == 0 {
		return
	}
	v := reflect.ValueOf(entity).Elem()
	for _, idx := range r.m.fillULIDFields {
		f := v.Field(idx)
		if f.Kind() == reflect.String && f.CanSet() && f.String() == "" {
			f.SetString(ulid.Make().String())
		}
	}
}

// ─── Create ────────────────────────────────────────────────────────────

// Create inserts a new row. Plain INSERT, never an upsert: a duplicate PK is
// a real error. Chokepoint invariants:
//   - a server-allocated PK (,autoincrement: the schema gives the key an
//     identity or a DEFAULT) left at its zero is excluded from the INSERT
//     and read back via RETURNING; a key the caller set is inserted as set;
//   - any other string PK is ULID-generated when empty — never one the
//     database allocates, whose DEFAULT (gen_random_uuid()) knows the
//     column's type and a ULID does not;
//   - managed timestamps are stamped; nil array fields are normalized to {}.
func (r *Repo[M]) Create(ctx context.Context, db orm.Context, entity *M) error {
	r.ensureMeta(db)
	ctx, span := r.startSpan(ctx, "Create")
	defer span.End()

	if r.m.pkIsString && !r.m.pkAutoInc {
		pk := r.pkFieldValue(entity)
		if pk.IsValid() && pk.Kind() == reflect.String && pk.String() == "" {
			pk.SetString(ulid.Make().String())
		}
	}
	r.fillULIDColumns(entity)
	if r.m.timestamps {
		r.stampCreate(entity)
	}
	r.normalizeArrays(entity)

	q := r.returnDatabaseFilled(r.excludeGeneratedColumns(db.Bun().NewInsert().Model(entity)), entity)
	if r.m.pkAutoInc {
		if pk := r.pkFieldValue(entity); !pk.IsValid() || pk.IsZero() {
			q = q.ExcludeColumn(r.m.pkColumn)
		}
	}
	if _, err := q.Exec(ctx); err != nil {
		recordErr(span, err)
		return fmt.Errorf("create %s: %w", r.m.table, err)
	}
	return nil
}

// ─── Upsert ────────────────────────────────────────────────────────────

// Upsert writes entity by its primary key: INSERT, or on a PK conflict,
// UPDATE the existing row. It is Repo's ONLY upsert verb — Create stays a
// plain INSERT (see its doc comment) for exactly the callers this one is
// not for: an idempotent ingest, a sync-from-external-system, or a
// seed-or-update flow that does not know in advance whether the row
// exists.
//
// # Conflict target
//
// The conflict target is always the primary key. Upserting on an
// arbitrary unique index (a natural key distinct from the PK) is a
// materially bigger design — it needs its own conflict-column parameter,
// its own EXCLUDED-vs-existing-PK reconciliation, and its own tests — and
// is out of scope here. A caller that needs it writes the ON CONFLICT
// query by hand against db.Bun().
//
// # SET list
//
// The DO UPDATE SET list is r.m.updatable — the SAME allowlist a
// full-replace Update writes, which already excludes the PK, deleted_at,
// and (under managed timestamps) created_at/updated_at, plus any
// ,skipupdate column. An upsert is a write like any other: it must not
// let a round-tripped entity clobber a server-owned or secret column any
// more than Update may, so the two share one list rather than each
// maintaining its own idea of what is writable.
//
// # Timestamps
//
// Both branches follow Create's rule (stampCreate): a created_at or
// updated_at the caller set is kept, an unset one is now. created_at is
// written only on the INSERT path; updated_at is written on both, the same
// value either way, so the row's updated_at does not depend on which branch
// ran. An upsert that rewrote created_at on a conflict would misreport
// when the row was actually born — the DO UPDATE SET list never names
// created_at for exactly that reason.
//
// # Soft delete
//
// An upsert onto a tombstoned row RESURRECTS it (clears deleted_at along
// with the rest of the updatable set) rather than silently no-op'ing or
// erroring. Bun does not auto-scope INSERT the way it scopes SELECT/
// DELETE, and there is no WHERE clause on an INSERT to guard with the way
// Update's deleted_at IS NULL guard works — the row does not "exist" from
// the read side, so an upsert that refused to touch it would look
// indistinguishable from one that inserted successfully. Resurrection is
// the one behavior consistent with the verb's own name: the caller
// asserted "this row should exist with these values", and a soft-deleted
// row is, for that purpose, an absent one that Upsert is allowed to
// (re)create. A caller that wants tombstones to stay dead must Get first
// and branch.
//
// # Server-allocated / string PKs
//
// A caller supplying an EMPTY PK on a server-allocated (autoincrement)
// column cannot conflict with anything — there is no matching value to
// find — so it always inserts a fresh row exactly like Create. The same
// is true of an empty string PK, except Upsert does NOT ULID-generate one
// the way Create does: a generated PK can never already be present in the
// table, so ON CONFLICT could not fire regardless, and doing so would
// make Upsert silently degrade into Create for its most common accidental
// misuse (forgetting to set the PK) instead of surfacing the caller's
// mistake as a NOT NULL violation.
func (r *Repo[M]) Upsert(ctx context.Context, db orm.Context, entity *M) error {
	r.ensureMeta(db)
	ctx, span := r.startSpan(ctx, "Upsert")
	defer span.End()

	if r.m.timestamps {
		r.stampCreate(entity)
	}
	r.normalizeArrays(entity)

	q := r.excludeGeneratedColumns(db.Bun().NewInsert().Model(entity)).
		On("CONFLICT ("+strings.TrimSuffix(strings.Repeat("?, ", len(r.pkIdents())), ", ")+") DO UPDATE", r.pkIdents()...)
	// An unset column (unsettable) took its DEFAULT on the insert branch;
	// on the conflict branch it keeps the stored value, as Update does.
	for _, col := range r.withoutUnset(r.m.updatable, entity) {
		q = q.Set("? = EXCLUDED.?", bun.Ident(col), bun.Ident(col))
	}
	if r.m.timestamps && r.m.hasUpdatedAt {
		q = q.Set("? = ?", bun.Ident("updated_at"), r.updatedAtStampValue(entity))
	}
	// Resurrection (see the doc comment above): deleted_at is excluded
	// from r.m.updatable like the PK and the other managed timestamps, so
	// without an explicit clear here the DO UPDATE branch would leave a
	// tombstoned row's deleted_at exactly as it was, silently defeating
	// the documented behavior.
	if r.m.nativeSoftDel || r.m.legacyTextSoftDel {
		q = q.Set("? = NULL", bun.Ident("deleted_at"))
	}

	q = r.returnDatabaseFilled(q, entity)
	if _, err := q.Exec(ctx); err != nil {
		recordErr(span, err)
		return fmt.Errorf("upsert %s: %w", r.m.table, err)
	}
	return nil
}

// updatedAtStampValue reads back the value stampCreate just wrote to
// updated_at, so the DO UPDATE branch sets it to the SAME instant as the
// DO INSERT branch instead of a value from a second, later time.Now()
// call. EXCLUDED.updated_at would read equally well, but it is excluded
// from r.m.updatable and therefore not guaranteed to be part of the
// INSERT's column list in every future refactor of that allowlist.
func (r *Repo[M]) updatedAtStampValue(entity *M) any {
	sf := r.m.stampFields["updated_at"]
	return reflect.ValueOf(entity).Elem().Field(sf.index).Interface()
}

// ─── Get ───────────────────────────────────────────────────────────────

// Get retrieves a row by primary key. A missing row is the repo's
// not-found error (notFound): svcerr.NotFound, and errors.Is-true for
// orm.ErrNoRows. The legacy-TEXT deleted_at filter is
// applied; Bun-native soft delete auto-excludes tombstones from the SELECT.
func (r *Repo[M]) Get(ctx context.Context, db orm.Context, id any) (*M, error) {
	r.ensureMeta(db)
	ctx, span := r.startSpan(ctx, "Get", attribute.String("id", fmt.Sprint(id)))
	defer span.End()

	entity := new(M)
	b := r.newBinder(db)
	q := db.Bun().NewSelect().Model(entity).
		Where("? = ?", bun.Ident(r.m.pkColumn), b.value(id)).Limit(1)
	r.scopeRead(q)
	if err := b.get(ctx, span, db, q, entity); err != nil {
		recordErr(span, err)
		return nil, r.missErr("get %s by id", err)
	}
	return entity, nil
}

// GetByPK retrieves the row whose primary key equals key's — every key
// column, so it is how a COMPOSITE-key entity is read (Get takes one id).
// Only key's primary-key fields are read. A missing row is the repo's
// not-found error (notFound).
func (r *Repo[M]) GetByPK(ctx context.Context, db orm.Context, key *M) (*M, error) {
	r.ensureMeta(db)
	ctx, span := r.startSpan(ctx, "Get")
	defer span.End()

	entity := new(M)
	*entity = *key
	bnd := r.newBinder(db)
	q := db.Bun().NewSelect().Model(entity).Limit(1)
	r.wherePK(q.QueryBuilder(), bnd, entity)
	r.scopeRead(q)
	if err := bnd.get(ctx, span, db, q, entity); err != nil {
		recordErr(span, err)
		return nil, r.missErr("get %s by key", err)
	}
	return entity, nil
}

// wherePK addresses entity's row by its primary key: the one column for a
// single-column key, every key column for a composite one — each value
// through b, so a by-key statement binds its key like every other by-PK
// statement (see bind.go).
func (r *Repo[M]) wherePK(q bun.QueryBuilder, b *binder, entity *M) {
	if len(r.m.pkColumns) > 1 {
		v := reflect.ValueOf(entity).Elem()
		for _, col := range r.m.pkColumns {
			q.Where("? = ?", bun.Ident(col), b.value(r.m.fields[col].Value(v).Interface()))
		}
		return
	}
	q.Where("? = ?", bun.Ident(r.m.pkColumn), b.value(r.pkFieldValue(entity).Interface()))
}

// pkIdents are the primary-key columns as identifiers, for a conflict
// target naming them all.
func (r *Repo[M]) pkIdents() []any {
	if len(r.m.pkColumns) == 0 { // no declared key: the conventional id
		return []any{bun.Ident(r.m.pkColumn)}
	}
	out := make([]any, len(r.m.pkColumns))
	for i, c := range r.m.pkColumns {
		out[i] = bun.Ident(c)
	}
	return out
}

// ─── List / Count / ListAll ──────────────────────────────────────────────

// List retrieves rows with optional QueryOption filtering/ordering/limit.
// The legacy-TEXT soft-delete filter is applied; Bun's ,soft_delete excludes
// tombstones natively.
//
// With no orm.WithLimit, List returns EVERY matching row. It used to cap a
// limit-less call at 10,000 rows, silently: a caller asking for all of a
// table's rows got the first 10,000 and no sign there were more, which is a
// wrong answer that only appears once the table grows. A query's answer must
// not depend on the size of the table it reads. Bounding a read is the
// caller's decision, made where the caller can act on it: orm.WithLimit for
// a page, and orm.WhereGt on the key with WithOrderBy + WithLimit to walk a
// table that grows without bound one page at a time. The paged List rpcs in
// this package always pass their own clamped page size (MaxPageSize), so the
// wire stays bounded.
func (r *Repo[M]) List(ctx context.Context, db orm.Context, opts ...orm.QueryOption) ([]*M, error) {
	r.ensureMeta(db)
	ctx, span := r.startSpan(ctx, "List")
	defer span.End()

	var results []*M
	q := db.Bun().NewSelect().Model(&results)
	r.scopeRead(q)
	for _, opt := range opts {
		opt(q)
	}
	if err := q.Scan(ctx); err != nil {
		recordErr(span, err)
		return nil, fmt.Errorf("list %s: %w", r.m.table, err)
	}
	return results, nil
}

// Count returns the number of matching rows under the same scope as List.
func (r *Repo[M]) Count(ctx context.Context, db orm.Context, opts ...orm.QueryOption) (int64, error) {
	r.ensureMeta(db)
	ctx, span := r.startSpan(ctx, "Count")
	defer span.End()

	q := db.Bun().NewSelect().Model((*M)(nil))
	r.scopeRead(q)
	for _, opt := range opts {
		opt(q)
	}
	n, err := q.Count(ctx)
	if err != nil {
		recordErr(span, err)
		return 0, fmt.Errorf("count %s: %w", r.m.table, err)
	}
	return int64(n), nil
}

// ListAll retrieves rows INCLUDING soft-deleted ones. Bun-native soft delete
// needs an explicit WhereAllWithDeleted to see tombstones; the legacy-TEXT
// path simply omits the deleted_at filter.
func (r *Repo[M]) ListAll(ctx context.Context, db orm.Context, opts ...orm.QueryOption) ([]*M, error) {
	r.ensureMeta(db)
	ctx, span := r.startSpan(ctx, "ListAll")
	defer span.End()

	var results []*M
	q := db.Bun().NewSelect().Model(&results)
	if r.m.nativeSoftDel {
		q = q.WhereAllWithDeleted()
	}
	for _, opt := range opts {
		opt(q)
	}
	if err := q.Scan(ctx); err != nil {
		recordErr(span, err)
		return nil, fmt.Errorf("list all %s: %w", r.m.table, err)
	}
	return results, nil
}

// scopeRead applies the soft-delete read filter to a SELECT. Bun owns the
// native-soft-delete exclusion automatically; only the legacy-TEXT path needs
// the explicit deleted_at IS NULL filter here.
func (r *Repo[M]) scopeRead(q *bun.SelectQuery) {
	if r.m.legacyTextSoftDel {
		q.Where("? IS NULL", bun.Ident("deleted_at"))
	}
}

// ─── Update / UpdateMasked ────────────────────────────────────────────────

// Update writes the full updatable column set of an existing row by PK.
// updated_at is re-stamped under managed timestamps; created_at, the PK,
// deleted_at, and any column tagged ,skipupdate — the projection of a
// `forge:immutable` column declaration — are excluded from r.m.updatable and
// so never named in the SET clause. That is what keeps a maskless Update
// built from a round-tripped entity (one whose stripped or server-owned
// columns came back zero-valued) from writing those zeros over stored data;
// a new value is set via Create or a masked Update naming the column. The
// deleted_at IS NULL guard applies to BOTH soft-delete modes: Bun
// auto-scopes SELECT/DELETE to live rows but NOT UPDATE, so without the
// guard an UPDATE could mutate a tombstoned row.
//
// An entity that declared a `forge:version` column additionally gets
// optimistic concurrency control: see applyVersionGuard.
//
// Every column the SET clause does not name is read back into entity (see
// readBackUnwritten), so after a nil return the caller holds the row as
// stored — a held-back column reports its stored value, not whatever the
// caller's struct carried for it. NoReadBack turns that off for one call.
//
// opts tune this one call; see Preserve and NoReadBack.
func (r *Repo[M]) Update(ctx context.Context, db orm.Context, entity *M, opts ...UpdateOption) error {
	r.ensureMeta(db)
	var o updateOptions
	for _, opt := range opts {
		opt(&o)
	}
	cols, err := r.withoutPreserved(r.m.updatable, o.preserve)
	if err != nil {
		return err
	}
	cols = r.withoutUnset(cols, entity)
	if len(cols) == 0 {
		return nil // no updatable fields
	}
	ctx, span := r.startSpan(ctx, "Update")
	defer span.End()

	if r.m.timestamps {
		r.stampUpdated(entity)
	}
	r.normalizeArrays(entity)

	if r.m.timestamps && r.m.hasUpdatedAt {
		cols = appendCol(cols, "updated_at")
	}
	b := r.newBinder(db)
	q := db.Bun().NewUpdate().Model(entity)
	b.setColumns(q, r.m.fields, reflect.ValueOf(entity).Elem(), cols)
	r.wherePK(q.QueryBuilder(), b, entity)
	r.scopeWrite(q)
	r.applyVersionGuard(q, b, entity)
	returning := false
	if !o.noReadBack {
		returning = r.returnGenerated(q)
		returning = r.readBackUnwritten(q, cols) || returning
	}
	res, err := b.write(ctx, span, db, q, entity, returning)
	if err != nil {
		recordErr(span, err)
		return fmt.Errorf("update %s: %w", r.m.table, err)
	}
	if err := r.requireWriteLanded(ctx, db, res, entity); err != nil {
		recordErr(span, err)
		return err
	}
	r.advanceVersion(entity)
	return nil
}

// UpdateMasked writes ONLY the named columns (AIP-134 update_mask paths;
// proto field names == column names). Paths outside the updatable allowlist
// return *orm.UnknownFieldError, which pkg/crud maps to a clean
// InvalidArgument. updated_at is stamped on masked writes too.
//
// A ,skipupdate column named by the mask is a deliberate assertion (rotating
// a secret, reassigning an owner) and must be written — the opposite of
// Update's full-replace rule. Bun's own SET-clause builder applies
// ,skipupdate unconditionally regardless of which path asked for the
// column, so such columns are pulled out of Column(...) and set explicitly
// via SetColumn(...), which Bun does not filter.
//
// The `forge:version` column is the exception to that exception: it is
// excluded from updatableSet entirely, so a mask naming it is an
// UnknownFieldError rather than a deliberate assertion. A masked write is
// still version-CHECKED — writing one field of a row someone else has since
// rewritten is the same lost update as replacing the whole row — so the
// same predicate and increment apply here. See applyVersionGuard.
//
// Like Update, every column the write did not set is read back, so the
// unmasked fields of entity hold the stored row afterwards rather than
// whatever the caller's struct carried. For a narrow write — stamping one
// column of a wide row — that read-back is most of the statement's cost;
// pass NoReadBack to make it exec-only.
//
// opts tune this one call. Preserve is a full-replace option and is
// rejected here: the mask already names exactly what is written.
func (r *Repo[M]) UpdateMasked(ctx context.Context, db orm.Context, entity *M, fields []string, opts ...UpdateOption) error {
	r.ensureMeta(db)
	var o updateOptions
	for _, opt := range opts {
		opt(&o)
	}
	if len(o.preserve) > 0 {
		return fmt.Errorf("update %s: Preserve applies to a full-replace Update; a masked update writes only its mask", r.m.table)
	}
	if len(fields) == 0 {
		return nil
	}
	if len(r.m.updatableSet) == 0 {
		// A concrete path can only ever be unknown.
		return &orm.UnknownFieldError{Field: fields[0]}
	}
	ctx, span := r.startSpan(ctx, "UpdateMasked")
	defer span.End()

	stampUpdated := r.m.timestamps && r.m.hasUpdatedAt
	if stampUpdated {
		r.stampUpdated(entity)
	}

	cols := make([]string, 0, len(fields)+1)
	var forced []string
	seen := make(map[string]bool, len(fields))
	for _, f := range fields {
		if !r.m.updatableSet[f] {
			return &orm.UnknownFieldError{Field: f}
		}
		if seen[f] {
			continue
		}
		seen[f] = true
		if _, skip := r.m.skipUpdate[f]; skip {
			forced = append(forced, f)
			continue
		}
		cols = append(cols, f)
	}
	r.normalizeArraysFor(entity, seen)
	if stampUpdated && !seen["updated_at"] {
		cols = append(cols, "updated_at")
	}

	b := r.newBinder(db)
	q := db.Bun().NewUpdate().Model(entity)
	v := reflect.ValueOf(entity).Elem()
	if len(cols) > 0 {
		b.setColumns(q, r.m.fields, v, cols)
	}
	for _, f := range forced {
		q.SetColumn(f, "?", b.value(v.Field(r.m.skipUpdate[f]).Interface()))
	}
	r.wherePK(q.QueryBuilder(), b, entity)
	r.scopeWrite(q)
	r.applyVersionGuard(q, b, entity)
	returning := false
	if !o.noReadBack {
		returning = r.returnGenerated(q)
		returning = r.readBackUnwritten(q, slices.Concat(cols, forced)) || returning
	}
	res, err := b.write(ctx, span, db, q, entity, returning)
	if err != nil {
		recordErr(span, err)
		return fmt.Errorf("update %s: %w", r.m.table, err)
	}
	if err := r.requireWriteLanded(ctx, db, res, entity); err != nil {
		recordErr(span, err)
		return err
	}
	r.advanceVersion(entity)
	return nil
}

// UpdateOption tunes one Update or UpdateMasked call. See Preserve and
// NoReadBack.
type UpdateOption func(*updateOptions)

type updateOptions struct {
	preserve   []string
	noReadBack bool
}

// NoReadBack makes ONE Update or UpdateMasked exec-only: the statement
// carries no RETURNING clause, so nothing is scanned back into the entity.
//
// By default a write reads back every column it did not set, plus every
// generated column (see readBackUnwritten), so the caller holds the stored
// row afterwards. That is the right default for a CRUD response packed from
// the entity, and the wrong cost for a write whose caller discards the
// entity — a worker stamping dispatched_at on a 20-column row paid for 19
// columns of RETURNING and their scan on every call (10 → 102 allocations,
// 360 B → 9.8 KB, against the hand-written UPDATE it replaced).
//
// With NoReadBack, after a nil return the entity holds exactly what the
// caller put in it, plus what the repository computes in Go: the stamped
// updated_at and, for a forge:version entity, the advanced version. The
// columns the write did not set, and any GENERATED column, are NOT
// refreshed — re-read the row if you need them. Everything else is
// unchanged: the soft-delete guard, the version check, and a zero-row
// write returning orm.ErrNoRows (or Aborted on a version conflict).
func NoReadBack() UpdateOption {
	return func(o *updateOptions) { o.noReadBack = true }
}

// Preserve keeps the named columns out of ONE full-replace Update's SET
// clause, whatever the entity holds for them. Like every column the write
// does not set, they are read back, so the entity reports the stored values
// afterwards.
//
// It is the per-CALL twin of ,skipupdate. A ,skipupdate column
// (forge:immutable, a forge:secret field) is held back from every full
// replace, because what makes rewriting it unsafe is a fact about the
// COLUMN. Preserve expresses a fact about the CALLER. The generated CRUD
// Update op passes the entity's forge:read-only and forge:computed columns:
// a client may not write those, but the custom RPC that owns them writes
// through this same repository and must. Declaring that on the column
// would take the write away from the code that owns it.
//
// A name that is not a column of the entity is an error, not a no-op: a
// misspelled Preserve would otherwise drop its protection with no symptom.
// A column a full replace never writes anyway (the PK, a forge:version or
// GENERATED column, a ,skipupdate column) is accepted and changes nothing.
func Preserve(columns ...string) UpdateOption {
	return func(o *updateOptions) { o.preserve = append(o.preserve, columns...) }
}

// withoutUnset returns cols minus the unsettable columns entity leaves
// unset (a nil pointer, a ,nullzero zero), as a fresh slice when it drops
// any — cols may be the shared r.m.updatable allowlist.
//
// Writing them would store NULL into a NOT NULL column (a loud failure at
// best) or reset a defaulted column to its DEFAULT; leaving them out keeps
// the stored value, and readBackUnwritten reports it.
func (r *Repo[M]) withoutUnset(cols []string, entity *M) []string {
	if len(r.m.unsettable) == 0 {
		return cols
	}
	v := reflect.ValueOf(entity).Elem()
	var unset []string
	for _, f := range r.m.unsettable {
		if (f.IsPtr && f.HasNilValue(v)) || (f.NullZero && f.HasZeroValue(v)) {
			unset = append(unset, f.Name)
		}
	}
	if len(unset) == 0 {
		return cols
	}
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		if !slices.Contains(unset, c) {
			out = append(out, c)
		}
	}
	return out
}

// withoutPreserved returns cols minus preserve, as a fresh slice — cols is
// the shared r.m.updatable allowlist and must never be mutated.
func (r *Repo[M]) withoutPreserved(cols, preserve []string) ([]string, error) {
	if len(preserve) == 0 {
		return cols, nil
	}
	for _, p := range preserve {
		if !slices.Contains(r.m.columns, p) {
			return nil, fmt.Errorf("update %s: Preserve names %q, which is not a column of the table", r.m.table, p)
		}
	}
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		if !slices.Contains(preserve, c) {
			out = append(out, c)
		}
	}
	return out, nil
}

// readBackUnwritten adds a RETURNING clause for every column the UPDATE's
// SET does not name, so Bun scans the stored values of those columns into
// the entity in the same round trip. It is the complement of
// returnGenerated: that one refreshes the columns the database recomputed,
// this one the columns the write left alone.
//
// Without it an update leaves the caller holding values the row does not
// have. Every column a write deliberately skips is one the caller's struct
// may disagree about — a ,skipupdate column a client blanked, a column
// Preserve held back, the columns a mask did not name — and the CRUD
// response is packed from that struct. Before this, a full replace that
// correctly left a read-only status alone still answered with the status
// the request carried, so the client was told of a reset that never
// happened.
//
// The PK addresses the row and already matches. Generated columns are
// returnGenerated's (naming one twice would scan it twice). The
// forge:version column is left to advanceVersion, which mirrors the
// increment this statement makes; reading it back as well would advance it
// twice.
//
// With RETURNING, Bun scans instead of executing (UpdateQuery.scanOrExec),
// and the sql.Result it hands back counts scanned rows — so zero rows still
// reaches requireWriteLanded as zero rows affected, with the entity untouched.
//
// It reports whether it added any RETURNING column.
func (r *Repo[M]) readBackUnwritten(q *bun.UpdateQuery, written []string) bool {
	added := false
	for _, c := range r.m.columns {
		if c == r.m.pkColumn || slices.Contains(r.m.pkColumns, c) || c == r.m.versionColumn ||
			slices.Contains(written, c) || slices.Contains(r.m.generatedCols, c) {
			continue
		}
		q.Returning("?", bun.Ident(c))
		added = true
	}
	return added
}

// scopeWrite applies the soft-delete guard to an UPDATE.
func (r *Repo[M]) scopeWrite(q *bun.UpdateQuery) {
	if r.m.nativeSoftDel || r.m.legacyTextSoftDel {
		q.Where("? IS NULL", bun.Ident("deleted_at"))
	}
}

// ─── optimistic concurrency control ───────────────────────────────────────
//
// Opt-in, per entity, by declaring one column `forge:version` in a
// migration. An entity without one never reaches any of the code below:
// versionColumn is "", every function here returns immediately, and the
// write path is byte-for-byte the last-writer-wins behaviour it always was.

// applyVersionGuard turns an UPDATE into a compare-and-swap: the row is
// matched only while its stored version still equals the one the caller
// read, and the same statement increments it.
//
// Both halves must be in the ONE statement. Reading the version, comparing
// it in Go and then writing is the lost update this exists to prevent,
// merely with a smaller window — the compare and the write have to be a
// single atomic act, and `WHERE version = $n` + `SET version = version + 1`
// is that act, enforced by the row lock the UPDATE already takes. It needs
// no explicit transaction and no isolation level above postgres's default
// READ COMMITTED: a concurrent writer either committed before this
// statement acquired the row (the predicate then fails and matches nothing)
// or after (it finds the incremented value and fails in turn).
//
// The increment is expressed as `version = version + 1` — the DATABASE's
// value plus one, not the in-memory value plus one. They are equal whenever
// the predicate matches, but writing it as an expression keeps the
// statement true by construction rather than true by an assumption about
// what the caller's struct holds.
func (r *Repo[M]) applyVersionGuard(q *bun.UpdateQuery, b *binder, entity *M) {
	if r.m.versionColumn == "" {
		return
	}
	col := bun.Ident(r.m.versionColumn)
	q.Where("? = ?", col, b.value(r.versionFieldValue(entity).Interface()))
	q.Set("? = ? + 1", col, col)
}

// versionFieldValue is the version column's struct field on entity.
func (r *Repo[M]) versionFieldValue(entity *M) reflect.Value {
	return reflect.ValueOf(entity).Elem().Field(r.m.versionFieldIndex)
}

// advanceVersion mirrors the increment the database just performed onto the
// caller's in-memory entity, so a caller holding it can issue a SECOND
// update without re-reading the row.
//
// Without this, the write succeeds, the stored version moves to n+1, the
// struct still says n, and the caller's next Update fails Aborted against a
// row nobody else touched — a conflict invented by this package. Only ever
// called after a write that DID land, so it can never advance past a
// version the database rejected.
func (r *Repo[M]) advanceVersion(entity *M) {
	if r.m.versionColumn == "" {
		return
	}
	f := r.versionFieldValue(entity)
	if !f.CanSet() {
		return
	}
	switch f.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		f.SetInt(f.Int() + 1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		f.SetUint(f.Uint() + 1)
	}
	// Any other Go type is left alone rather than guessed at. The column is
	// meant to be an integer counter; a non-integer one still gets the
	// database-side `+ 1` (postgres decides whether that is legal for the
	// type) and simply forces the caller to re-read before writing again.
}

// requireWriteLanded is requireRowTouched plus the disambiguation optimistic
// concurrency control forces on it.
//
// Zero rows affected used to have exactly one meaning — no such row — and
// requireRowTouched turned it into orm.ErrNoRows → CodeNotFound. With a
// version predicate in the WHERE clause it has two, and they are not
// remotely the same news for a client:
//
//   - the row is gone (or was never there, or is soft-deleted): NotFound.
//     Retrying is pointless; the resource does not exist.
//   - the row is there but its version moved: somebody else committed a
//     write between this caller's read and its write. Aborted. Re-read and
//     retry is not just viable, it is the prescribed recovery — which is
//     precisely what connect.CodeAborted means, and why it is not
//     FailedPrecondition (a state the caller must fix) or AlreadyExists.
//
// Reporting a conflict as NotFound would tell a client its row had been
// deleted when it had merely been edited, and a UI acting on that ("this
// record no longer exists") would be lying about data still sitting in the
// table. So on zero rows the repo asks ONE narrow follow-up question — does
// a row with this PK exist at all, ignoring the version — and answers from
// the result.
//
// That re-query is racy in the strict sense: the row could be deleted
// between the failed UPDATE and this SELECT, turning a genuine conflict
// into a NotFound. That is acceptable and the ordering makes it honest —
// the row IS gone by the time the caller is told so, and re-reading (the
// prescribed response to Aborted) would have discovered exactly that. The
// converse mistake, calling a deletion a conflict, is equally bounded and
// equally self-correcting on the caller's re-read.
//
// It costs one extra round trip ONLY on the failure path of a
// version-checked entity: the happy path never reaches it, and an entity
// with no version column returns from the first branch without ever
// building the query.
func (r *Repo[M]) requireWriteLanded(ctx context.Context, db orm.Context, res sql.Result, entity *M) error {
	if err := r.requireRowTouched(res, "update"); err == nil {
		return nil
	}
	if r.m.versionColumn == "" {
		return r.notFound("update")
	}

	// The version predicate is deliberately absent here: this asks only
	// whether the ROW exists, which is the single fact that separates the
	// two answers. The soft-delete scoping stays, so a tombstoned row reads
	// as absent exactly as it does to Get — the two verbs must keep
	// agreeing about what "this row does not exist" means.
	key := new(M)
	*key = *entity
	q := db.Bun().NewSelect().Model(key)
	r.wherePK(q.QueryBuilder(), &binder{}, key)
	r.scopeRead(q)
	exists, err := q.Exists(ctx)
	if err != nil {
		// The follow-up query itself failed, so which of the two answers
		// applies is unknown. Report the query's error rather than pick one:
		// a guess here would be indistinguishable from a real verdict.
		return fmt.Errorf("update %s: classify zero-row write: %w", r.m.table, err)
	}
	if !exists {
		return r.notFound("update")
	}
	return svcerr.Aborted(fmt.Sprintf(
		"%s was modified by another writer; re-read it and retry", r.m.entityName))
}

func appendCol(cols []string, c string) []string {
	for _, e := range cols {
		if e == c {
			return cols
		}
	}
	// Copy to avoid mutating the shared r.m.updatable backing array.
	out := make([]string, len(cols), len(cols)+1)
	copy(out, cols)
	return append(out, c)
}

// ─── Delete ───────────────────────────────────────────────────────────────

// Delete removes a row by PK. With Bun-native soft delete a plain NewDelete
// stamps deleted_at (Bun rewrites it to UPDATE) and auto-scopes to live
// rows. With legacy-TEXT soft delete the repo hand-rolls the
// CURRENT_TIMESTAMP stamp + deleted_at IS NULL guard (Bun's time.Time stamp
// can't round-trip a TEXT column). Otherwise it is a hard DELETE.
//
// A PK that matched no live row returns the repo's not-found error
// (notFound), which pkg/crud maps to CodeNotFound — the same answer Get
// gives for the same id. See
// requireRowTouched for why that, and not a silent success.
func (r *Repo[M]) Delete(ctx context.Context, db orm.Context, id any) error {
	r.ensureMeta(db)
	ctx, span := r.startSpan(ctx, "Delete", attribute.String("id", fmt.Sprint(id)))
	defer span.End()

	b := r.newBinder(db)
	if r.m.legacyTextSoftDel {
		q := db.Bun().NewUpdate().Model((*M)(nil)).
			Set("? = CURRENT_TIMESTAMP", bun.Ident("deleted_at")).
			Where("? = ?", bun.Ident(r.m.pkColumn), b.value(id)).
			Where("? IS NULL", bun.Ident("deleted_at"))
		res, err := b.write(ctx, span, db, q, nil, false)
		if err != nil {
			recordErr(span, err)
			return fmt.Errorf("delete %s: %w", r.m.table, err)
		}
		if err := r.requireRowTouched(res, "delete"); err != nil {
			recordErr(span, err)
			return err
		}
		return nil
	}

	// Native soft delete: Bun rewrites NewDelete to an UPDATE that stamps
	// deleted_at with a time it formats into the text — a new statement
	// for every call. In bind mode the repo writes that UPDATE itself, with
	// the stamp (the same time.Now() bun would use) as a parameter.
	if r.m.nativeSoftDel && b.bind {
		col := bun.Ident(r.m.softDeleteColumn)
		q := db.Bun().NewUpdate().Model((*M)(nil)).
			Set("? = ?", col, b.value(time.Now())).
			Where("? = ?", bun.Ident(r.m.pkColumn), b.value(id)).
			Where("? IS NULL", col)
		res, err := b.write(ctx, span, db, q, nil, false)
		if err != nil {
			recordErr(span, err)
			return fmt.Errorf("delete %s: %w", r.m.table, err)
		}
		if err := r.requireRowTouched(res, "delete"); err != nil {
			recordErr(span, err)
			return err
		}
		return nil
	}

	// Hard delete, and native soft delete through bun's executor (Bun
	// rewrites NewDelete → UPDATE deleted_at and scopes to live rows).
	q := db.Bun().NewDelete().Model((*M)(nil)).
		Where("? = ?", bun.Ident(r.m.pkColumn), b.value(id))
	res, err := b.write(ctx, span, db, q, nil, false)
	if err != nil {
		recordErr(span, err)
		return fmt.Errorf("delete %s: %w", r.m.table, err)
	}
	if err := r.requireRowTouched(res, "delete"); err != nil {
		recordErr(span, err)
		return err
	}
	return nil
}

// DeleteByPK removes the row whose primary key equals key's — every key
// column, so it is how a COMPOSITE-key entity is deleted (Delete takes one
// id). Soft delete and the not-found answer are Delete's.
func (r *Repo[M]) DeleteByPK(ctx context.Context, db orm.Context, key *M) error {
	r.ensureMeta(db)
	ctx, span := r.startSpan(ctx, "Delete")
	defer span.End()

	b := r.newBinder(db)
	var q bunWrite
	switch {
	case r.m.legacyTextSoftDel:
		uq := db.Bun().NewUpdate().Model((*M)(nil)).
			Set("? = CURRENT_TIMESTAMP", bun.Ident("deleted_at")).
			Where("? IS NULL", bun.Ident("deleted_at"))
		r.wherePK(uq.QueryBuilder(), b, key)
		q = uq
	case r.m.nativeSoftDel && b.bind:
		// As Delete: in bind mode the repo writes the soft-delete UPDATE
		// itself, with the stamp as a parameter.
		col := bun.Ident(r.m.softDeleteColumn)
		uq := db.Bun().NewUpdate().Model((*M)(nil)).
			Set("? = ?", col, b.value(time.Now())).
			Where("? IS NULL", col)
		r.wherePK(uq.QueryBuilder(), b, key)
		q = uq
	default:
		dq := db.Bun().NewDelete().Model((*M)(nil))
		r.wherePK(dq.QueryBuilder(), b, key)
		q = dq
	}
	res, err := b.write(ctx, span, db, q, nil, false)
	if err != nil {
		recordErr(span, err)
		return fmt.Errorf("delete %s: %w", r.m.table, err)
	}
	if err := r.requireRowTouched(res, "delete"); err != nil {
		recordErr(span, err)
		return err
	}
	return nil
}

// requireRowTouched turns a write that matched no row into the repo's
// not-found error (see notFound).
//
// UPDATE and DELETE report a row count, not sql.ErrNoRows, so a repository
// that discards the count cannot tell "row deleted" from "no such row" —
// and every generated Delete answered a typo'd id with 200 and an empty
// body while Get answered the same id with 404. Two verbs resolving the
// same row, by the same PK, under the same soft-delete scoping and the
// same application-owned filters, must agree about what "this row does not
// exist" means.
//
// NotFound is the answer, not idempotent-200:
//
//   - AIP-135, the resource-Delete design this CRUD shape follows, returns
//     NOT_FOUND unless the request explicitly opts into allow_missing.
//   - HTTP idempotency (RFC 9110 §9.2.2) is a property of the EFFECT on
//     server state, not of the status code. A repeated DELETE that answers
//     404 is still idempotent; nothing about returning 200 is required.
//   - The 200 was not merely permissive, it was unfalsifiable: a caller
//     could not distinguish a delete that worked from a wrong id, a row an
//     application-owned filter excluded, or a row already tombstoned.
//
// A project that genuinely wants absent-is-success owns the shim: swallow
// orm.ErrNoRows (still errors.Is-true; see notFound) in that entity's
// Persist closure in handlers_crud.go.
//
// A driver that cannot report the count is treated as "touched" —
// inventing a not-found out of a missing count would fail a write that
// actually succeeded.
func (r *Repo[M]) requireRowTouched(res sql.Result, op string) error {
	n, err := res.RowsAffected()
	if err != nil || n > 0 {
		return nil
	}
	return r.notFound(op)
}

// notFound is the error every by-key verb — Get, GetByPK, Update,
// UpdateMasked, Delete, DeleteByPK — answers a missing row with:
// svcerr.NotFound("<entity>"), carrying orm.ErrNoRows as its cause, wrapped
// in the operation and table for the log.
//
// One answer, so a caller checks one thing. Before, Get's generated delegate
// said svcerr.NotFound while Update and Delete said orm.ErrNoRows, and every
// caller that touched both wrote both checks. svcerr.NotFound is the one
// that is a domain category: it maps to CodeNotFound on the wire through
// svcerr.Wrap, and a service can return it as-is. orm.ErrNoRows (that is,
// sql.ErrNoRows) stays reachable through errors.Is because it is the
// documented way to opt a Persist closure into absent-is-success, and the
// check every existing caller already has; a miss satisfies both
// errors.Is(err, svcerr.ErrNotFound) and errors.Is(err, orm.ErrNoRows).
func (r *Repo[M]) notFound(op string) error {
	return fmt.Errorf("%s %s: %w", op, r.m.table,
		svcerr.WithCause(svcerr.NotFound(r.m.entityLabel), orm.ErrNoRows))
}

// missErr wraps a read's error with format (one %s: the table), as
// notFound when it is a missing row.
func (r *Repo[M]) missErr(format string, err error) error {
	op := fmt.Sprintf(format, r.m.table)
	if errors.Is(err, orm.ErrNoRows) {
		return fmt.Errorf("%s: %w", op, svcerr.WithCause(svcerr.NotFound(r.m.entityLabel), orm.ErrNoRows))
	}
	return fmt.Errorf("%s: %w", op, err)
}

// ─── managed-timestamp stamping ──────────────────────────────────────────

// stampCreate stamps a new row's managed timestamps: each of created_at and
// updated_at the caller left unset gets now (one instant for both), and one
// the caller set is kept. Fields whose type the repo can't stamp are absent
// from stampFields and left untouched.
//
// The rule is explicit-beats-default, the one a NOT NULL DEFAULT column
// follows (db/write-policy): a managed timestamp is a default, so it fills a
// gap and never overwrites a value. A row imported, synced or replayed from
// elsewhere — reliant's drafts and scenarios carry their own — keeps its
// times; one the caller built fresh is stamped as before. Update is not
// Create: a full-replace or masked update re-stamps updated_at always
// (stampUpdated), because an entity read back and written again carries its
// old updated_at, which is not a decision.
func (r *Repo[M]) stampCreate(entity *M) {
	now := time.Now().UTC()
	v := reflect.ValueOf(entity).Elem()
	for _, col := range [...]string{"created_at", "updated_at"} {
		if sf, ok := r.m.stampFields[col]; ok && stampIsEmpty(v.Field(sf.index), sf) {
			writeStamp(v.Field(sf.index), sf, now)
		}
	}
}

// stampUpdated re-stamps updated_at to now.
func (r *Repo[M]) stampUpdated(entity *M) {
	if sf, ok := r.m.stampFields["updated_at"]; ok {
		writeStamp(reflect.ValueOf(entity).Elem().Field(sf.index), sf, time.Now().UTC())
	}
}

// stampIsEmpty reports whether a managed timestamp field is unset (so a
// caller-provided value wins): nil pointer, empty string, or zero time.
func stampIsEmpty(f reflect.Value, sf stampField) bool {
	if sf.isPtr {
		return f.IsNil()
	}
	if sf.isString {
		return f.String() == ""
	}
	t, ok := f.Interface().(time.Time)
	return ok && t.IsZero()
}

// writeStamp sets f to now in its projected type (string → RFC3339Nano,
// time.Time → the instant), allocating through an addressable local for
// pointer fields.
func writeStamp(f reflect.Value, sf stampField, now time.Time) {
	if !f.CanSet() {
		return
	}
	if sf.isString {
		s := now.Format(time.RFC3339Nano)
		if sf.isPtr {
			p := reflect.New(f.Type().Elem())
			p.Elem().SetString(s)
			f.Set(p)
			return
		}
		f.SetString(s)
		return
	}
	if sf.isPtr {
		p := reflect.New(f.Type().Elem())
		p.Elem().Set(reflect.ValueOf(now))
		f.Set(p)
		return
	}
	f.Set(reflect.ValueOf(now))
}
