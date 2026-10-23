package codegen

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/shadowdb"
	"github.com/reliant-labs/forge/pkg/schemadef"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// crudTestFixtures is the schema-derived model behind constraint-correct
// create-request fixtures — the values the regenerated New<CreateRequest>
// factories (create_request_factory.go) hand the CRUD lifecycle test. The
// blind `"test-value"`/`1` literals the test scaffold used to emit violate
// real schemas immediately — CHECK vocabularies, email-regex CHECKs,
// char_length CHECKs, numeric range CHECKs, and NOT NULL foreign keys all
// reject them at create #1. This model joins the APPLIED schema (tables + FKs
// + CHECK constraints, the same introspection the rest of the generate
// pipeline runs on) with the seed synthesizer's constraint-aware value
// heuristics, so the requests pass against the real migrated schema — and,
// being re-derived on every generate, keep passing as that schema evolves.
//
// A nil *crudTestFixtures is valid everywhere and means "no schema model"
// (no migrations, or introspection failed): every lookup falls back to the
// legacy type-blind literals.
type crudTestFixtures struct {
	// enumImports maps the alias a create-request literal uses for an enum in
	// a foreign Go package to that package's import path (see enumQualifier).
	enumImports map[string]string
	tables      map[string]schemadef.Table
	pools       seedplan.EnumPools
	bounds      seedplan.CheckBounds
	// vocab is the project's db/seeds/vocab.yaml domain-vocabulary overlay,
	// when present: the parent-closure seed rows then carry the same domain
	// values the dev dataset does (same Plan, same determinism). nil is the
	// built-in behavior.
	vocab *seedplan.Vocab
	// plans maps an entity's table to its FK-parent-closure seed plan. A
	// nil entry means the closure was unsatisfiable (e.g. a NOT NULL FK
	// cycle through the entity itself); that entity keeps legacy fixtures.
	plans map[string]*entitySeedPlan
	// shadow is the LIVE applied-schema database the model was introspected
	// from, kept open so the fixtures this model derives can be verified
	// against the authority that will enforce them (crud_fixture_guard.go).
	// nil in unit tests that build a model from hand-written tables — the
	// guard is then simply not run, never silently reported as passing.
	shadow *schemadef.Shadow
	// unions memoizes the discriminated-union placement of each table's
	// MINIMAL row — the branch every fixture this model hands out is written
	// against. See unionPlacement.
	unions map[string]map[string]seedplan.UnionCell
	// emitted records every fixture handed out, per table, so the guard can
	// verify the exact set the generated file will carry. It is appended by
	// fieldFixture itself rather than reconstructed afterwards: a guard that
	// re-derived the values would be checking its own copy, and could agree
	// with itself while disagreeing with the emitted file.
	emitted map[string][]fixtureValue
}

// close releases the shadow database. Safe on a nil model.
func (fx *crudTestFixtures) close() {
	if fx == nil {
		return
	}
	fx.shadow.Close()
}

// record captures one derived fixture for the guard. goType is the Go type the
// literal is spelled in; values whose SQL form cannot be read back (enum
// constants, timestamp constructor calls) are not recorded, and the guard
// makes no claim about them.
func (fx *crudTestFixtures) record(table, column, goLit, goType string) {
	if fx == nil || fx.emitted == nil {
		return
	}
	sqlLit, ok := goLiteralToSQL(goLit, goType)
	if !ok {
		return
	}
	fx.emitted[table] = append(fx.emitted[table],
		fixtureValue{column: column, sqlLit: sqlLit, goLit: goLit})
}

// entitySeedPlan is one entity's DB-level test setup: a MINIMAL seed plan
// (seedplan.Config.Minimal) over the entity table and its foreign-key parent
// closure, and the rendered INSERT statements for the PARENTS only, which the
// generated create-request factory executes before handing back a request.
// Parents are seeded at the DB level — not through their own create RPCs —
// because foreign keys may cross services (a clinical entity referencing a
// catalog table) while a handler test only has its own service's stack.
//
// The entity's own table is IN the plan but never in seedSQL. It is planned
// so the create-request derivation can ask what a minimal row of it writes
// (plan.Writes) — an enum the DEFAULT already supplies stays off the request,
// a timestamp the schema requires goes on it — and it is never inserted,
// because the rows a lifecycle test asserts on are the ones it creates.
type entitySeedPlan struct {
	plan    *seedplan.Plan
	seedSQL string
}

// buildCRUDTestFixtures introspects the applied schema and builds the
// fixture model for the given CRUD methods' entities. Returns nil when the
// project has no migrations (or introspection fails) — the caller then
// scaffolds with the legacy type-blind values, exactly as before.
// The shadow database is kept OPEN for the model's lifetime so the fixtures it
// derives can be verified against it (see verify); the caller must call close.
func buildCRUDTestFixtures(projectDir string, methods []CRUDMethod) *crudTestFixtures {
	fx, _ := loadCRUDTestFixtures(projectDir, methods)
	return fx
}

// loadCRUDTestFixtures is buildCRUDTestFixtures that reports WHY there is no
// model: a nil model with a nil error means the project has no schema to model
// (no migrations, nothing applied), while an error means the schema could not
// be read — the shadow server could not be reached or fetched, or a migration
// does not apply. Callers that regenerate a file on every run need the
// difference: "nothing to emit" and "could not look" call for different
// actions on a file that already exists.
func loadCRUDTestFixtures(projectDir string, methods []CRUDMethod) (*crudTestFixtures, error) {
	tables, shadow, err := shadowdb.Open(projectDir, filepath.Join(projectDir, "db", "migrations"))
	if err != nil {
		return nil, err
	}
	if len(tables) == 0 {
		shadow.Close()
		return nil, nil
	}
	byName := make(map[string]schemadef.Table, len(tables))
	for _, t := range tables {
		byName[t.Name] = t
	}
	// The domain-vocabulary overlay flows into the fixture seed plans too.
	// Load problems are non-fatal at generate time — the seed CLI is where
	// vocab errors/warnings surface; here a bad file just means built-ins.
	vocab, _ := seedplan.LoadVocab(seedplan.VocabPath(filepath.Join(projectDir, "db", "migrations")))
	fx := &crudTestFixtures{
		tables:  byName,
		pools:   seedplan.PoolsFromTables(tables),
		bounds:  seedplan.BoundsFromTables(tables),
		vocab:   vocab,
		plans:   map[string]*entitySeedPlan{},
		shadow:  shadow,
		unions:  map[string]map[string]seedplan.UnionCell{},
		emitted: map[string][]fixtureValue{},
	}
	for _, cm := range methods {
		tn := cm.Entity.TableName
		if _, done := fx.plans[tn]; done {
			continue
		}
		if _, ok := byName[tn]; !ok {
			continue
		}
		fx.plans[tn] = fx.buildEntitySeedPlan(tn)
	}
	return fx, nil
}

// buildEntitySeedPlan builds the MINIMAL seed plan for one entity table and
// its proper FK ancestors: every table reachable from it over declared
// foreign keys. Only the ancestors are rendered into seedSQL (seeding the
// entity's own table would break the lifecycle test's exact row-count
// assertions). Two rows per parent: row 0 is what create #1 references; row 1
// exists so a UNIQUE (1-1) foreign key can give create #2 a distinct parent.
//
// Minimal, because these rows exist only to be referenced: a parent needs its
// key and whatever its schema REQUIRES, and every column beyond that is one
// more value a later migration (a GENERATED column, a one-way status CHECK)
// can turn into a rejected INSERT.
func (fx *crudTestFixtures) buildEntitySeedPlan(root string) *entitySeedPlan {
	closure := map[string]bool{}
	var walk func(string)
	walk = func(name string) {
		for _, fk := range fx.foreignKeys(fx.tables[name]) {
			// References back to the entity table are not seedable here
			// (BuildPlan force-nulls them when nullable, errors when NOT
			// NULL); self-references resolve inside the referencing table.
			if fk.RefTable == root || fk.RefTable == name || closure[fk.RefTable] {
				continue
			}
			if _, ok := fx.tables[fk.RefTable]; !ok {
				continue
			}
			closure[fk.RefTable] = true
			walk(fk.RefTable)
		}
	}
	walk(root)
	names := make([]string, 0, len(closure)+1)
	for name := range closure {
		names = append(names, name)
	}
	sort.Strings(names)
	names = append(names, root)
	// Hand BuildPlan the SAME view of the foreign keys the walk above used.
	// The closure is discovered through fx.foreignKeys(), which augments the
	// DECLARED keys with the ones forge's own birth migration proposes — but
	// a raw schemadef.Table carries only the declared set. Passing the raw
	// tables let the two disagree, and BuildPlan, seeing no edges, both
	// ordered the INSERTs by the alphabetical base order and synthesized a
	// parent row's FK columns as generic `sample_<column>_<n>` strings. A
	// parent that is itself a child (orders -> prescriptions -> patients)
	// then produced SQL that violates the very constraint forge suggested.
	closureTables := make([]schemadef.Table, 0, len(names))
	for _, name := range names {
		t := fx.tables[name]
		t.ForeignKeys = fx.foreignKeys(t)
		closureTables = append(closureTables, t)
	}
	cfg := seedplan.Config{Rows: 2, Salt: 0, Minimal: true}
	plan, err := seedplan.BuildPlan(closureTables, fx.pools, cfg)
	if err != nil {
		// Unsatisfiable closure (NOT NULL FK back into the entity, or into
		// a cycle). The entity keeps legacy fixtures; the test may need a
		// hand-written setup — which is true of the schema itself.
		return nil
	}
	plan.SetBounds(fx.bounds)
	plan.ApplyVocab(fx.vocab) // warnings surface via the seed CLI, not here
	rootPrefix := "INSERT INTO " + seedplan.QualifiedTable(fx.tables[root]) + " "
	var parents []string
	for _, stmt := range plan.Statements() {
		if !strings.HasPrefix(stmt, rootPrefix) {
			parents = append(parents, strings.TrimSpace(stmt))
		}
	}
	return &entitySeedPlan{plan: plan, seedSQL: strings.Join(parents, "\n")}
}

// unplacedConstraintNote explains a seeding failure that the planner saw
// coming. The seed plan for an entity's parents records every multi-column
// constraint it could not place (a discriminated union that overlaps another
// constraint, a biconditional, …) and leaves those rows satisfying them only by
// chance. When postgres then rejects a parent row, those notes are the cause,
// and the raw constraint violation alone does not say so. Returns "" when the
// plan made no such note.
func (fx *crudTestFixtures) unplacedConstraintNote(table, failure string) string {
	if fx == nil {
		return ""
	}
	esp := fx.plans[table]
	if esp == nil || esp.plan == nil {
		return ""
	}
	// The plan covers the whole parent closure; only the notes about the
	// relation postgres named are the cause of THIS failure.
	var relevant []string
	for _, w := range esp.plan.Warnings() {
		name, _, ok := strings.Cut(strings.TrimPrefix(w, "seed plan: "), " constraint ")
		if ok && strings.Contains(failure, `relation "`+name+`"`) {
			relevant = append(relevant, w)
		}
	}
	if len(relevant) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n  forge could not place these constraints, so the parent rows it seeds satisfy them only by chance:")
	for _, w := range relevant {
		b.WriteString("\n    - " + strings.TrimPrefix(w, "seed plan: "))
	}
	b.WriteString("\n  A vocabulary in db/seeds/vocab.yaml does not help here: a minimal row leaves nullable columns unset, and these constraints require some of them.")
	b.WriteString("\n  Either restate the constraint in a form forge can place (one union per set of columns; a one-way implication instead of a biconditional — see `forge skill load db/seeding`),")
	b.WriteString("\n  or take the factory over with `forge project disown <this file>` and build the request by hand.")
	return b.String()
}

// ensurePlan builds (once) the entity seed plan for a table the model knows.
func (fx *crudTestFixtures) ensurePlan(table string) {
	if fx == nil {
		return
	}
	if _, done := fx.plans[table]; done {
		return
	}
	if _, ok := fx.tables[table]; !ok {
		return
	}
	fx.plans[table] = fx.buildEntitySeedPlan(table)
}

// tableList returns the model's tables, sorted by name.
func (fx *crudTestFixtures) tableList() []schemadef.Table {
	if fx == nil {
		return nil
	}
	names := make([]string, 0, len(fx.tables))
	for name := range fx.tables {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]schemadef.Table, 0, len(names))
	for _, name := range names {
		out = append(out, fx.tables[name])
	}
	return out
}

// minimalWrites reports whether a MINIMAL row of the entity's table writes
// column — whether the schema requires a value the database will not supply
// itself (see seedplan.Config.Minimal). known is false when there is no plan
// to ask (no schema model, or an unsatisfiable closure); the caller then keeps
// its schema-blind behavior.
func (fx *crudTestFixtures) minimalWrites(ent EntityDef, column string) (writes, known bool) {
	if fx == nil {
		return false, false
	}
	esp := fx.plans[ent.TableName]
	if esp == nil || esp.plan == nil {
		return false, false
	}
	if _, ok := fx.tables[ent.TableName]; !ok {
		return false, false
	}
	return esp.plan.Writes(ent.TableName, column), true
}

// columnIsGenerated reports whether the entity's column is GENERATED ALWAYS
// in the applied schema.
func (fx *crudTestFixtures) columnIsGenerated(ent EntityDef, column string) bool {
	if fx == nil {
		return false
	}
	t, ok := fx.tables[ent.TableName]
	if !ok {
		return false
	}
	col, ok := tableColumn(t, column)
	return ok && col.IsGenerated
}

// unionRequiresPresent reports whether the discriminated-union branch the
// fixtures are written against requires the column to hold a value — a
// status guard's consequent on the branch the minimal row takes.
func (fx *crudTestFixtures) unionRequiresPresent(ent EntityDef, column string) bool {
	if fx == nil {
		return false
	}
	t, ok := fx.tables[ent.TableName]
	if !ok {
		return false
	}
	return fx.unionPlacement(t)[column].Present
}

// foreignKeys returns the table's declared foreign keys PLUS the ones
// forge's own birth migration proposes for it.
//
// A `<x>_id` column is born as a plain TEXT column with the FOREIGN KEY and
// its index written directly underneath, COMMENTED OUT, for the author to
// uncomment — and the charter tells them to ("add relationships, indexes,
// and constraints with hand-written migrations"). The scaffold-once
// lifecycle test seeded no parent for such a column, so the moment anyone
// took forge's own suggestion, create #1 failed:
//
//	create #1: failed_precondition: create gadget: a referenced record is missing
//
// Seeding the parent forge already named costs one extra row and makes the
// born test survive the constraint forge itself recommended. A `<x>_id`
// whose stem names no table (user_id in a project with no users) resolves
// to nothing and is left alone, exactly as the FK suggestion is.
func (fx *crudTestFixtures) foreignKeys(t schemadef.Table) []schemadef.ForeignKey {
	out := append([]schemadef.ForeignKey(nil), t.ForeignKeys...)
	declared := make(map[string]bool, len(t.ForeignKeys))
	for _, fk := range t.ForeignKeys {
		declared[fk.Column] = true
	}
	for _, c := range t.Columns {
		if declared[c.Name] || c.IsArray || c.Type != schemadef.TypeString || !strings.HasSuffix(c.Name, "_id") {
			continue
		}
		ref, ok := fx.resolveRefTable(strings.TrimSuffix(c.Name, "_id"), t.Name)
		if !ok {
			continue
		}
		out = append(out, schemadef.ForeignKey{Column: c.Name, RefTable: ref.Name, RefColumn: ref.PKCols[0]})
	}
	return out
}

// resolveRefTable maps a `<x>_id` column stem onto a real table, matching
// internal/scaffold.resolveFKTable (the resolver that wrote the commented
// FK suggestion): progressively drop leading `_`-segments, longest first, so
// a role prefix resolves to the same entity (`assigned_provider_id` →
// providers). Only single-column-PK tables qualify — a composite key has no
// single value to reference.
func (fx *crudTestFixtures) resolveRefTable(stem, self string) (schemadef.Table, bool) {
	if stem == "" {
		return schemadef.Table{}, false
	}
	parts := strings.Split(stem, "_")
	for i := range parts {
		name := naming.Pluralize(strings.Join(parts[i:], "_"))
		if name == self {
			continue // self-reference: no seedable parent
		}
		if t, ok := fx.tables[name]; ok && len(t.PKCols) == 1 {
			return t, true
		}
	}
	return schemadef.Table{}, false
}

// seedSQLFor returns the rendered parent-closure INSERTs for an entity
// table ("" when there is nothing to seed or no model).
func (fx *crudTestFixtures) seedSQLFor(tableName string) string {
	if fx == nil {
		return ""
	}
	if esp := fx.plans[tableName]; esp != nil {
		return esp.seedSQL
	}
	return ""
}

// columnWriteConstrained reports whether writing an arbitrary literal to
// the column can violate schema constraints — the column is a foreign key,
// or any CHECK constraint references it. The lifecycle test's mutation
// targets (`row.X = "lifecycle-updated"`) must be unconstrained columns or
// the update leg fails exactly the way create used to.
func (fx *crudTestFixtures) columnWriteConstrained(ent EntityDef, column string) bool {
	if fx == nil {
		return false
	}
	t, ok := fx.tables[ent.TableName]
	if !ok {
		return false
	}
	for _, fk := range fx.foreignKeys(t) {
		if fk.Column == column {
			return true
		}
	}
	for _, ck := range t.Checks {
		for _, c := range ck.Columns {
			if c == column {
				return true
			}
		}
	}
	return false
}

// fieldFixture returns the Go literals the lifecycle test's create #1 and
// create #2 requests carry for one create-request field, derived from the
// applied schema. ok=false means "no schema-informed value" — the caller
// falls back to the legacy type-blind literal for both creates.
//
// The two creates carry DISTINCT values wherever the type admits one. They
// used to differ only where the schema already demanded it (a single-column
// UNIQUE index), and be identical everywhere else "keeping the scaffold easy
// to read". But handlers_crud_test.go is scaffold-once — the author is told
// not to rewrite it — while the schema keeps evolving, and forge itself
// tells authors to "add relationships, indexes, and constraints with
// hand-written migrations". The first UNIQUE index anyone added therefore
// broke a file they had been told to leave alone:
//
//	--- FAIL: TestCRUD_Product_Lifecycle
//	    create #2: already_exists: create product: a record with the same unique value already exists
//
// Two identical rows buy nothing: the test needs two rows to prove
// list/get/update work across distinct records, not the same row twice.
// It also RECORDS what it hands out (see record), so the guard verifies the
// values the generated file actually carries rather than a re-derivation of
// them. Recording happens here, at the single exit every caller goes through,
// because a fixture the generator emits without recording is exactly the case
// the guard exists to catch.
func (fx *crudTestFixtures) fieldFixture(svc ServiceDef, ent EntityDef, inputType, fieldName, goType string, kind FieldKind) (v1, v2 string, ok bool) {
	v1, v2, ok = fx.deriveFieldFixture(svc, ent, inputType, fieldName, goType, kind)
	if ok {
		// Both creates are written to the same table under the same
		// constraints, so both are verified.
		fx.record(ent.TableName, fieldName, v1, goType)
		fx.record(ent.TableName, fieldName, v2, goType)
	}
	return v1, v2, ok
}

// deriveFieldFixture is fieldFixture's derivation, separated from the
// recording so the two cannot be confused for each other.
func (fx *crudTestFixtures) deriveFieldFixture(svc ServiceDef, ent EntityDef, inputType, fieldName, goType string, kind FieldKind) (v1, v2 string, ok bool) {
	if fx == nil {
		return "", "", false
	}
	t, haveTable := fx.tables[ent.TableName]
	if !haveTable {
		return "", "", false
	}
	col, haveCol := tableColumn(t, fieldName)
	if !haveCol {
		return "", "", false
	}

	// Foreign key → reference the seeded parents: row 0 and row 1, which
	// the plan always seeds. Self-references have no seedable parent.
	for _, fk := range fx.foreignKeys(t) {
		if fk.Column != fieldName {
			continue
		}
		if fk.RefTable == ent.TableName {
			return "", "", false
		}
		esp := fx.plans[ent.TableName]
		if esp == nil || esp.plan == nil {
			return "", "", false
		}
		raw1, ok1 := esp.plan.SeedValue(fk.RefTable, fk.RefColumn, 0)
		raw2, ok2 := esp.plan.SeedValue(fk.RefTable, fk.RefColumn, 1)
		if !ok1 || !ok2 {
			return "", "", false
		}
		lit1, lit2 := goScalarLiteral(goType, raw1), goScalarLiteral(goType, raw2)
		if lit1 == "" || lit2 == "" {
			return "", "", false
		}
		return lit1, lit2, true
	}

	// A discriminated-union CHECK places several columns of one row TOGETHER
	// (seedplan/union.go): a discriminator pinned to one branch's literal, its
	// siblings narrowed or required absent. Consulted before every per-column
	// branch below, because a value chosen for one column on its own is
	// exactly what such a constraint rejects.
	if v1, v2, ok := fx.unionFixture(svc, ent, inputType, t, col, goType, kind); ok {
		return v1, v2, true
	}

	if kind == FieldKindEnum {
		return fx.enumFixture(svc, ent, inputType, fieldName)
	}

	// JSON/JSONB columns store a JSON document (a proto `json` field projects
	// to Go string + a JSONB column), so they must be handled before the
	// type-blind string path: the legacy "test-value" bare word is invalid
	// JSON input and postgres rejects the INSERT with SQLSTATE 22P02 at
	// create #1. Emit a valid-JSON literal instead.
	if col.Type == schemadef.TypeJSON && !col.IsArray {
		return fx.jsonFixture()
	}

	// A column the schema requires to sit ABOVE another column of the same
	// row takes its value from the ordering chain, not from an independent
	// per-column heuristic.
	if v1, v2, ok := fx.orderedFixture(t, col, goType); ok {
		return v1, v2, true
	}

	switch goType {
	case "string":
		return fx.stringFixture(t, col)
	case "int32", "int64", "uint32", "uint64":
		return fx.intFixture(t.Name, fieldName)
	case "float32", "float64":
		b, _ := fx.boundFor(t.Name, fieldName)
		f1, f2 := spreadFloat(b)
		return strconv.FormatFloat(f1, 'g', -1, 64),
			strconv.FormatFloat(f2, 'g', -1, 64), true
	}
	return "", "", false
}

// orderedFixture places a create-request field the schema requires to sit
// above another column of the same row: CHECK (expires_at > issued_at), and
// the NULL-guarded spelling optional timestamps invite.
//
// This is the fixture half of the seeder's ordering pass
// (seeddata/ordering.go), and it exists for the same reason — a value chosen
// per column cannot satisfy a constraint that spans two. Here it was worse
// than a collision: the lifecycle test did not set timestamp fields AT ALL,
// so both ends of a NOT NULL window took Go's zero time and landed on the
// same 0001-01-01 instant:
//
//	--- FAIL: TestCRUD_Prescription_Lifecycle
//	    create #1: invalid_argument: create prescription: a field value violates a constraint
//
// On a NULLABLE pair the same omission is quieter and no better: both ends
// insert NULL, the CHECK passes vacuously, and the born test proves nothing
// about the window it exists to exercise.
//
// Rank r sits r steps above its chain's root — the SAME placement
// seedplan.OrderRanks resolves for the seeded rows, so a born fixture and
// the dev dataset can never disagree about one schema. create #2 shifts
// every placed column by one further step, which preserves the ordering (all
// of them move together) and keeps the two creates distinct.
func (fx *crudTestFixtures) orderedFixture(t schemadef.Table, col schemadef.Column, goType string) (string, string, bool) {
	rank, ok := seedplan.OrderRanks(t)[col.Name]
	if !ok {
		return "", "", false
	}
	switch goType {
	case "*timestamppb.Timestamp":
		days := seedplan.OrderStepDays * rank
		return timestampAfter(days), timestampAfter(days + 1), true
	case "int32", "int64", "uint32", "uint64":
		b, _ := fx.boundFor(t.Name, col.Name)
		v1 := clampInt(int64(1+rank), b)
		return strconv.FormatInt(v1, 10), strconv.FormatInt(clampInt(v1+1, b), 10), true
	case "float32", "float64":
		b, _ := fx.boundFor(t.Name, col.Name)
		f1 := clampFloat(float64(1+rank), b)
		return strconv.FormatFloat(f1, 'g', -1, 64),
			strconv.FormatFloat(clampFloat(f1+1, b), 'g', -1, 64), true
	}
	return "", "", false
}

// timestampAfter spells the Go literal for "now, plus days days". Day 0 is
// `timestamppb.Now()` — the value the field would have carried anyway — so a
// chain's ROOT reads exactly as it did before any ordering existed.
func timestampAfter(days int) string {
	if days == 0 {
		return "timestamppb.Now()"
	}
	return fmt.Sprintf("timestamppb.New(time.Now().AddDate(0, 0, %d))", days)
}

// clampInt / clampFloat pin a value into a column's range-CHECK bounds. A
// nil end is unbounded on that side.
func clampInt(v int64, b seedplan.NumBound) int64 {
	if b.Min != nil && v < *b.Min {
		v = *b.Min
	}
	if b.Max != nil && v > *b.Max {
		v = *b.Max
	}
	return v
}

// spreadFloat picks the two float fixtures the lifecycle test creates with.
//
// The bound IS the input. An earlier shape picked a type-blind 1 and then
// clamped it into range, which is backwards twice over: clamping only the
// first of the pair put a 2 into a `CHECK (col <= 1)` column (a rate, a
// ratio, a probability), and clamping both collapsed them onto one endpoint,
// so the born test asserted "two rows differ" against two identical rows and
// passed while proving nothing.
//
// Deriving from the interval removes both failures and picks better values
// besides: the endpoints are where an off-by-one in a CHECK or a validator
// actually lives, so a fixture pair of (lo, hi) exercises more than two
// arbitrary interior points would. Unbounded ends fall back to a small
// concrete pair — there is no boundary to aim at.
func spreadFloat(b seedplan.NumBound) (float64, float64) {
	switch {
	case b.Min == nil && b.Max == nil:
		return 1, 2
	case b.Min == nil:
		hi := float64(*b.Max)
		return hi - 1, hi
	case b.Max == nil:
		lo := float64(*b.Min)
		return lo, lo + 1
	}

	lo, hi := float64(*b.Min), float64(*b.Max)
	if lo > hi {
		// Contradictory CHECKs — nothing satisfies them. Emit the lower
		// bound and let the guard report the real conflict against
		// postgres rather than inventing a value that hides it.
		return lo, lo
	}
	// lo == hi is the one-legal-value column: the same value twice is the
	// only honest answer, and the create-#2 assertion is what will say so.
	return lo, hi
}

// spreadInt is spreadFloat for integer columns, and exists for the same
// reason: `clampInt(1, b)` followed by `clampInt(v1+1, b)` silently collapses
// on a narrow bound — `CHECK (col >= 0 AND col <= 1)` yielded 1 and 1 — so
// the generated test compared a row against itself.
func spreadInt(b seedplan.NumBound) (int64, int64) {
	switch {
	case b.Min == nil && b.Max == nil:
		return 1, 2
	case b.Min == nil:
		return *b.Max - 1, *b.Max
	case b.Max == nil:
		return *b.Min, *b.Min + 1
	}
	if *b.Min > *b.Max {
		return *b.Min, *b.Min
	}
	return *b.Min, *b.Max
}

func clampFloat(f float64, b seedplan.NumBound) float64 {
	if b.Min != nil && f < float64(*b.Min) {
		f = float64(*b.Min)
	}
	if b.Max != nil && f > float64(*b.Max) {
		f = float64(*b.Max)
	}
	return f
}

// unionPlacement returns what the table's discriminated-union CHECKs require of
// the rows the create requests mint, memoized. A table with no placeable union
// memoizes an empty map, so the parse runs once per table rather than once per
// field.
//
// The branch is the MINIMAL row's (seedplan.MinimalUnionPlacement), and it is
// the SAME branch for create #1 and create #2. Two reasons, both load-bearing:
//
//   - The two creates share ONE field list and differ only in the values in
//     it, and which columns a discriminated union requires to be ABSENT is a
//     property of the list, not of the values. Two creates on two branches
//     cannot be spelled by one list.
//   - A create does not write every column. The ones it leaves unset take
//     their DEFAULT (an unset enum stores the column DEFAULT; a read-only
//     status is not on the request at all), so the branch a create can
//     actually satisfy is the one that keeps those columns at their DEFAULT
//     — a status guard's initial state — not whichever branch the dev
//     dataset's rotation reaches first. That is exactly the branch a minimal
//     factory row takes, so the create requests and New<Entity> agree about
//     one schema.
func (fx *crudTestFixtures) unionPlacement(t schemadef.Table) map[string]seedplan.UnionCell {
	if got, ok := fx.unions[t.Name]; ok {
		return got
	}
	got := seedplan.MinimalUnionPlacement(t, fx.pools)
	if got == nil {
		got = map[string]seedplan.UnionCell{}
	}
	if fx.unions == nil {
		// Hand-built models (unit tests) carry no memo; the parse is a pure
		// function of the table either way.
		fx.unions = map[string]map[string]seedplan.UnionCell{}
	}
	fx.unions[t.Name] = got
	return got
}

// unionOmitsField reports whether a discriminated-union CHECK requires the
// column to hold NO value on the rows the lifecycle test creates. The create
// request then does not set the field at all — an explicit-presence field left
// unset writes NULL, which is what the branch asks for.
//
// It is a separate question from fieldFixture's because its answer is not a
// value: there is no literal that means "absent", and emitting the type's zero
// value is exactly the write the constraint rejects.
func (fx *crudTestFixtures) unionOmitsField(ent EntityDef, fieldName string) bool {
	if fx == nil {
		return false
	}
	t, ok := fx.tables[ent.TableName]
	if !ok {
		return false
	}
	return fx.unionPlacement(t)[fieldName].Null
}

// unionFixture derives a create-request field placed by a discriminated-union
// CHECK: the branch's pinned literal for the discriminator, and its narrowed
// range for a sibling the branch bounds.
//
// This is the union half of what orderedFixture is for orderings, and it exists
// for the same reason: the value is not a property of the column. Drawn per
// column, a discriminator lands on one branch's literal while its siblings take
// values another branch requires, and the row satisfies neither —
//
//	--- FAIL: TestCRUD_Coupon_Lifecycle
//	    create #1: invalid_argument: create coupon: a field value violates a constraint
//
// — in a scaffold-once file the author is told not to rewrite. The placement is
// seedplan's own (UnionPlacement), not a second reading of the CHECK: one
// matcher, one branch choice, one model for both halves of forge's generated
// data.
//
// ok is false for a column the union does not place, and for one it places in a
// way this fixture cannot spell — a pin whose Go type it cannot write. The
// caller then falls back to the per-column derivation, and the generate-time
// guard reports the conflict against postgres rather than forge writing a value
// it cannot justify.
func (fx *crudTestFixtures) unionFixture(svc ServiceDef, ent EntityDef, inputType string, t schemadef.Table, col schemadef.Column, goType string, kind FieldKind) (string, string, bool) {
	cell, placed := fx.unionPlacement(t)[col.Name]
	if !placed || cell.Null {
		// A NULL cell is not a value; the field is dropped from the request
		// entirely (see unionOmitsField), and reaching here for one means the
		// caller emits nothing for it either way.
		return "", "", false
	}

	if cell.Value != "" {
		// A pin is not negotiable: both creates carry it, and the two rows
		// stay distinct on the unconstrained columns.
		if kind == FieldKindEnum {
			lit, ok := fx.enumConstant(svc, ent, inputType, col.Name, cell.Value)
			return lit, lit, ok
		}
		switch goType {
		case "string":
			lit := strconv.Quote(cell.Value)
			return lit, lit, true
		case "bool":
			return cell.Value, cell.Value, true
		case "int32", "int64", "uint32", "uint64", "float32", "float64":
			if _, err := strconv.ParseFloat(cell.Value, 64); err != nil {
				return "", "", false
			}
			return cell.Value, cell.Value, true
		}
		return "", "", false
	}

	if !cell.HasBound {
		// The branch only requires the column to be PRESENT, which every
		// fixture already satisfies. Nothing to place.
		return "", "", false
	}
	b, _ := fx.boundFor(t.Name, col.Name)
	b = narrowBound(b, cell.Bound)
	switch goType {
	case "int32", "int64", "uint32", "uint64":
		v1, v2 := spreadInt(b)
		return strconv.FormatInt(v1, 10), strconv.FormatInt(v2, 10), true
	case "float32", "float64":
		f1, f2 := spreadFloat(b)
		return strconv.FormatFloat(f1, 'g', -1, 64), strconv.FormatFloat(f2, 'g', -1, 64), true
	}
	return "", "", false
}

// narrowBound returns the tightest range satisfying both. A branch's range is
// an ADDITIONAL requirement on top of the column's own range CHECK, not a
// replacement for it: `amount_cents > 0` inside a branch and
// `amount_cents <= 100000` on the column are both true of the row.
func narrowBound(a, b seedplan.NumBound) seedplan.NumBound {
	if b.Min != nil && (a.Min == nil || *b.Min > *a.Min) {
		a.Min = b.Min
	}
	if b.Max != nil && (a.Max == nil || *b.Max < *a.Max) {
		a.Max = b.Max
	}
	return a
}

// enumFixture picks a real (non-UNSPECIFIED) enum value and spells it as
// the generated pb constant. Enum columns store proto VALUE NAMES as TEXT
// under a CHECK vocabulary, so the proto zero value — the old `0` fixture —
// is rejected whenever the CHECK excludes the sentinel. When the column has
// a CHECK pool, the choice is additionally restricted to it.
func (fx *crudTestFixtures) enumFixture(svc ServiceDef, ent EntityDef, inputType, fieldName string) (string, string, bool) {
	fq := ""
	if defs, okS := svc.Schemas[svc.Package+"."+inputType]; okS {
		for _, d := range defs {
			if d.Name == fieldName && d.Kind == "enum" {
				fq = d.TypeName
				break
			}
		}
	}
	if fq == "" {
		// Older descriptors: the entity wire field carries the enum's name.
		for _, ef := range ent.Fields {
			if ef.Name == fieldName && ef.Kind == FieldKindEnum {
				fq = ef.MessageType
				break
			}
		}
	}
	if _, _, okN := fx.enumQualifier(svc, fq); !okN {
		return "", "", false // unresolvable — legacy fixture
	}
	choices := seedplan.SeedEnumChoices(svc.Enums[fq])
	if pool, okP := fx.pools[ent.TableName]; okP {
		if vocab := pool[fieldName]; len(vocab) > 0 {
			allowed := map[string]bool{}
			for _, v := range vocab {
				allowed[v] = true
			}
			var kept []string
			for _, c := range choices {
				if allowed[c] {
					kept = append(kept, c)
				}
			}
			if len(kept) > 0 {
				choices = kept
			}
		}
	}
	if len(choices) == 0 {
		return "", "", false
	}
	// Two different values where the vocabulary has two; a single-valued
	// enum column cannot satisfy a UNIQUE index anyway.
	c1, c2 := choices[0], choices[0]
	if len(choices) > 1 {
		c2 = choices[1]
	}
	l1, ok1 := fx.enumConstant(svc, ent, inputType, fieldName, c1)
	l2, ok2 := fx.enumConstant(svc, ent, inputType, fieldName, c2)
	if !ok1 || !ok2 {
		return "", "", false
	}
	return l1, l2, true
}

// enumConstant spells one enum VALUE NAME as the generated pb constant for a
// create-request field. Split out of enumFixture so that a value chosen by
// something other than the vocabulary walk — a discriminated union pinning the
// discriminator, see unionFixture — is spelled by the same rule rather than by
// a second copy of it.
//
// protoc-gen-go value constants are prefixed by the ENCLOSING scope's Go name:
// `OrderStatus_ORDER_STATUS_ACTIVE` for a top-level enum, `Order_KIND_A` for
// `Order.Kind` (the enum's own last name segment is dropped from the prefix for
// nested declarations).
func (fx *crudTestFixtures) enumConstant(svc ServiceDef, ent EntityDef, inputType, fieldName, value string) (string, bool) {
	fq := ""
	if defs, okS := svc.Schemas[svc.Package+"."+inputType]; okS {
		for _, d := range defs {
			if d.Name == fieldName && d.Kind == "enum" {
				fq = d.TypeName
				break
			}
		}
	}
	if fq == "" {
		for _, ef := range ent.Fields {
			if ef.Name == fieldName && ef.Kind == FieldKindEnum {
				fq = ef.MessageType
				break
			}
		}
	}
	qual, goName, okN := fx.enumQualifier(svc, fq)
	if !okN || value == "" {
		return "", false
	}
	prefix := goName
	if idx := strings.LastIndex(goName, "_"); idx >= 0 {
		prefix = goName[:idx]
	}
	return qual + "." + prefix + "_" + value, true
}

// enumQualifier spells how the generated factory file refers to an enum's Go
// package: `pb` for the service's own package, otherwise a deterministic alias
// whose import is recorded in fx.enumImports for the renderer. A real constant
// from the enum's own package beats a generic literal — a typo in a value name
// stays a compile error instead of a runtime CHECK failure. ok=false only when
// the enum cannot be located at all (a legacy descriptor naming a foreign
// proto package).
func (fx *crudTestFixtures) enumQualifier(svc ServiceDef, fq string) (qual, goName string, ok bool) {
	ref, known := svc.EnumGoRefs[fq]
	if !known {
		name, okN := enumWireGoName(svc.Package, fq)
		return "pb", name, okN
	}
	if ref.ImportPath == svc.GoPackage {
		return "pb", ref.GoName, true
	}
	if ref.ImportPath == "" {
		return "", "", false
	}
	alias := mockImportAlias(ref.ImportPath, 0)
	if alias == "pb" || alias == "db" {
		alias += "enum"
	}
	if fx.enumImports == nil {
		fx.enumImports = map[string]string{}
	}
	fx.enumImports[alias] = ref.ImportPath
	return alias, ref.GoName, true
}

// stringFixture derives a string column's create values: CHECK vocabulary
// pool first, then seeddata's constraint-derived synthesis (a value built from
// the column's declared pattern CHECK, else the synthetic placeholder) fitted
// to any char_length CHECK / declared varchar cap, and the legacy "test-value"
// when the column is genuinely unconstrained.
func (fx *crudTestFixtures) stringFixture(t schemadef.Table, col schemadef.Column) (string, string, bool) {
	if pool, okP := fx.pools[t.Name]; okP {
		if vocab := seedplan.SeedEnumChoices(pool[col.Name]); len(vocab) > 0 {
			c1, c2 := vocab[0], vocab[0]
			if len(vocab) > 1 {
				c2 = vocab[1]
			}
			return strconv.Quote(c1), strconv.Quote(c2), true
		}
	}
	minLen, maxLen := lengthBoundsFor(t, col)
	if columnHasCheck(t, col.Name) || minLen > 0 || maxLen > 0 {
		// Constrained beyond a vocabulary (regex CHECK, char_length, varchar
		// cap): seedplan.SynthString builds a value from the constraints the
		// column DECLARES, so the fixture satisfies the same CHECKs the seeded
		// rows do. Two rows can coincide when the pattern admits no per-row
		// variation — walk on until they differ, keeping every candidate a
		// value the pattern still accepts (mangling one would break it).
		v1 := fitLength(seedplan.SynthString(t, col, 0), minLen, maxLen, 0)
		v2 := v1
		for row := 1; row < 8 && v2 == v1; row++ {
			v2 = fitLength(seedplan.SynthString(t, col, row), minLen, maxLen, row)
		}
		return strconv.Quote(v1), strconv.Quote(v2), true
	}
	return `"test-value"`, `"test-value-2"`, true
}

// jsonFixture derives a JSON/JSONB column's create values. The column
// parses its input as a JSON document, so the legacy "test-value" bare word
// is rejected as invalid JSON (SQLSTATE 22P02) at create #1 — emit
// valid-JSON Go string literals, two distinct documents.
func (fx *crudTestFixtures) jsonFixture() (string, string, bool) {
	return strconv.Quote("{}"), strconv.Quote(`{"k":"v"}`), true
}

// intFixture derives a numeric column's create values: a CHECK VOCABULARY
// first, then the column's range-CHECK bounds.
//
// The vocabulary branch is not an optimization — a set of admitted values is
// not a range, and no clamp can satisfy one. `CHECK (level IN (10, 20, 30))`
// admits neither the legacy `1` nor any bound-derived neighbour of it; only a
// MEMBER satisfies it, so a column carrying such a CHECK must draw from the
// members or not claim a fixture at all.
func (fx *crudTestFixtures) intFixture(table, column string) (string, string, bool) {
	if pool, okP := fx.pools[table]; okP {
		if vocab := seedplan.SeedEnumChoices(pool[column]); len(vocab) > 0 {
			c1, c2 := vocab[0], vocab[0]
			if len(vocab) > 1 {
				c2 = vocab[1]
			}
			return c1, c2, true
		}
	}
	b, has := fx.boundFor(table, column)
	if !has {
		return "", "", false // unconstrained — legacy literal
	}
	v1, v2 := spreadInt(b)
	return strconv.FormatInt(v1, 10), strconv.FormatInt(v2, 10), true
}

func (fx *crudTestFixtures) boundFor(table, column string) (seedplan.NumBound, bool) {
	cols, ok := fx.bounds[table]
	if !ok {
		return seedplan.NumBound{}, false
	}
	b, ok := cols[column]
	return b, ok
}

// tableColumn finds a column by name.
func tableColumn(t schemadef.Table, name string) (schemadef.Column, bool) {
	for _, c := range t.Columns {
		if c.Name == name {
			return c, true
		}
	}
	return schemadef.Column{}, false
}

// columnHasCheck reports whether any CHECK constraint references the column.
func columnHasCheck(t schemadef.Table, column string) bool {
	for _, ck := range t.Checks {
		for _, c := range ck.Columns {
			if c == column {
				return true
			}
		}
	}
	return false
}

// lengthBoundsFor is seedplan.LengthBounds — the char_length CHECK +
// varchar-cap merge lives with the rest of the constraint introspection.
func lengthBoundsFor(t schemadef.Table, col schemadef.Column) (minLen, maxLen int) {
	return seedplan.LengthBounds(t, col)
}

// fitLength forces s into [minLen, maxLen] (0 = unbounded) via the shared
// seedplan.FitLength — one implementation of the length model, so a fixture and
// the seeded row it mirrors can never disagree. row differentiates create #2's
// value for UNIQUE columns whose value TRUNCATED, where fitting alone would
// collapse both creates onto the same prefix: the last character becomes the
// row digit. Untruncated values are left exactly as synthesized (mangling one
// would break the format CHECKs — email, URL — the heuristics satisfy).
func fitLength(s string, minLen, maxLen, row int) string {
	truncated := maxLen > 0 && utf8.RuneCountInString(s) > maxLen
	s = seedplan.FitLength(s, minLen, maxLen)
	if truncated && row > 0 {
		if r := []rune(s); len(r) > 0 {
			r[len(r)-1] = rune('0' + row%10)
			s = string(r)
		}
	}
	return s
}

// goScalarLiteral spells a raw seeded value as a Go literal for the given
// scalar Go type ("" when the value doesn't fit the type — the caller then
// falls back to the legacy literal).
func goScalarLiteral(goType, raw string) string {
	if goType == "string" {
		return strconv.Quote(raw)
	}
	if _, err := strconv.ParseFloat(raw, 64); err == nil {
		return raw
	}
	return ""
}
