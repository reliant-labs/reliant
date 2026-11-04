package codegen

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/jinzhu/inflection"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/schemadef"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// Typed entity factories: the TYPED counterpart to seedplan.SeedGraph.
//
// SeedGraph seeds an FK spine keyed by a raw TABLE name and hands back a
// string-keyed handle — the right primitive for a flow test that drives its
// own RPCs. But a handler/unit test that just needs "one valid <Entity> row to
// read/derive from" was left reverse-engineering the entity's `_orm.go` — which
// columns are NOT NULL, which are foreign keys, what a CHECK vocabulary allows —
// to hand-build a `db.Order{...}` literal and INSERT it. That reverse-engineering
// (nine `_orm.go` views for one dashboard test in the dogfood run) is exactly
// what a typed factory removes.
//
// New<Entity>(t, db, overrides…) inserts ONE valid row — every NOT-NULL column
// and FK parent satisfied from forge's seed planner — and returns the typed
// *db.<Entity>. Each call gets a fresh primary key, so it is repeatable where
// a SeedGraph root is a single fixed spine; the FK parent spine is seeded
// idempotently (ON CONFLICT DO NOTHING), and column overrides let a test set
// just the fields it asserts on:
//
//	o := NewOrder(t, database, func(o *db.Order) { o.Status = "shipped" })
//
// WHERE THEY LAND, AND WHY IT IS A `_test.go` FILE. Each entity's factory is
// emitted into the handler package that owns its CRUD RPCs, as
// internal/handlers/<svc>/factories_gen_test.go — beside that service's
// helpers_gen_test.go and in the same package clause. The `_test.go` suffix is
// load-bearing: the toolchain compiles such a file only into that package's
// test binary, so `testing` is never linked into cmd/. The factories used to
// live in their own non-test package (internal/testfactory) for importability,
// which meant a non-test .go file importing `testing` sat in the tree — safe
// only for as long as nothing in the production import graph reached it, a
// property guarded by a `go list -deps` test rather than by structure. Landing
// them beside their consumer makes the leak unrepresentable instead.
//
// THE VISIBILITY THIS BUYS AND COSTS. Go compiles a package's in-package
// _test.go files INTO the package under test, and the external `<svc>_test`
// package then imports that augmented package — so these exported factories are
// visible to BOTH `package <svc>` and `package <svc>_test` files in this
// directory, and to nothing else. That is the correct scope for a test factory
// and it is what the scaffolded CRUD lifecycle test (package <svc>_test, same
// directory) needs. A test in a DIFFERENT package cannot call them; such a test
// drives the service through its RPCs, which is the seam it should be using.
//
// Emission degrades gracefully: no migrations, an
// unreachable shadow DB, or an unsatisfiable FK closure just means the factory
// for that entity (or the whole file) isn't emitted — never a failed generate.

// entityFactorySpec is one entity's baked factory: the parent-closure seed SQL
// (idempotent), the single-row root INSERT with the PK column bound to $1 (so
// each call inserts a fresh row), and the identifiers the emitted Go references.
type entityFactorySpec struct {
	goName    string // "Order" — the db.<GoName> struct / db.Get<GoName>ByID / db.Update<GoName>
	lower     string // "order" — const/var name stem (camelCase for multi-word)
	parentSQL string // FK-ancestor INSERTs (root excluded), each ON CONFLICT DO NOTHING
	rootSQL   string // single root INSERT with the PK literal replaced by $1
	// appendOnly suppresses the override seam. Overrides are applied by
	// writing the loaded row back with db.Update<Entity>, which an
	// append-only table's trigger rejects and whose store method no longer
	// exists — so for these entities the factory inserts and returns, and a
	// test wanting specific values inserts them itself.
	appendOnly bool
	// failure, when set, is postgres's reason for rejecting every row forge
	// could plan for this entity. The emitted factory then fails the calling
	// test with it rather than inserting SQL forge has already seen refused.
	failure string
}

// dbEntity is the table↔Go-name mapping parsed from the generated ORM package.
// The seed planner works in SQL (table names); the emitted factory works in Go
// (db.<GoName>, db.Get<GoName>ByID). The `_orm.go` structs are the authoritative
// join between the two, so we read them rather than re-deriving a Go name from a
// table name (pluralization isn't reliably invertible).
type dbEntity struct {
	goName   string
	table    string
	pkColumn string
	pkString bool // single string PK → factory-eligible (Get/Create take an id string)
}

// buildEntityFactorySpecs joins the fixture model's applied schema with the ORM
// structs' table↔Go-name mapping, and bakes a factory spec for every
// single-string-PK entity whose FK closure the seed planner can satisfy.
func buildEntityFactorySpecs(ctx context.Context, projectDir string, fx *crudTestFixtures) []entityFactorySpec {
	dbEntities := parseDBEntities(filepath.Join(projectDir, filepath.FromSlash(ProjectORMPackage(projectDir).PackageDir())))
	if len(dbEntities) == 0 {
		return nil
	}
	roots := make([]string, 0, len(dbEntities))
	for table := range dbEntities {
		roots = append(roots, table)
	}
	sort.Strings(roots)

	var specs []entityFactorySpec
	for _, root := range roots {
		ent := dbEntities[root]
		if !ent.pkString {
			continue // Get/Create take a string id — non-string PKs are out of scope
		}
		tbl, ok := fx.tables[root]
		if !ok || len(tbl.PKCols) != 1 {
			continue
		}
		if spec, ok := bakeVerifiedEntityFactory(ctx, fx, root, ent); ok {
			specs = append(specs, spec)
		}
	}
	return specs
}

// bakeVerifiedEntityFactory bakes an entity's factory from a MINIMAL plan —
// the smallest row its schema accepts, at its initial lifecycle state (see
// seedplan.Config.Minimal) — and checks it against the live shadow schema by
// executing it in a rolled-back transaction.
//
// A minimal row trusts the column DEFAULTs, and a DEFAULT can contradict a
// constraint forge cannot read. When postgres rejects the minimal row, the
// FULL plan's row (every column synthesized, the shape factories had before
// minimal rows existed) is tried before giving up, so no schema that had a
// working factory loses it. When both are rejected the spec carries the
// minimal row's error, and the emitted factory reports it.
func bakeVerifiedEntityFactory(ctx context.Context, fx *crudTestFixtures, root string, ent dbEntity) (entityFactorySpec, bool) {
	minimal, ok := bakeEntityFactory(fx.tables, root, ent, fx.pools, fx.bounds, fx.vocab, true)
	if !ok {
		return entityFactorySpec{}, false
	}
	minimalErr := verifyEntityFactory(ctx, fx, minimal)
	if minimalErr == nil {
		return minimal, true
	}
	if full, okFull := bakeEntityFactory(fx.tables, root, ent, fx.pools, fx.bounds, fx.vocab, false); okFull {
		if verifyEntityFactory(ctx, fx, full) == nil {
			return full, true
		}
	}
	fx.ensurePlan(root)
	minimal.failure = minimalErr.Error() + fx.unplacedConstraintNote(root, minimalErr.Error())
	return minimal, true
}

// verifyEntityFactory executes a baked factory's SQL against the fixture
// model's shadow schema in a transaction that is rolled back. nil when it
// runs, or when there is no live schema to ask (a hand-built model).
func verifyEntityFactory(ctx context.Context, fx *crudTestFixtures, spec entityFactorySpec) error {
	if fx == nil || fx.shadow == nil {
		return nil
	}
	return execRolledBack(ctx, fx.shadow.DB(),
		sqlStep{query: spec.parentSQL},
		sqlStep{query: spec.rootSQL, args: []any{factoryVerifyID}},
	)
}

// factoryVerifyID is the primary key bound to $1 when a factory's root INSERT
// is verified. ULID-shaped, like the ids the emitted factory mints, so a key
// CHECK that accepts those accepts this.
const factoryVerifyID = "01FORGEVERIFY0000000000000"

// bakeEntityFactory renders one entity's parent + root SQL from a Rows:1 plan
// over its FK closure (root included) — a minimal plan when minimal is set,
// else the full one. Returns ok=false when the closure is unsatisfiable or the
// root statement/PK can't be resolved — the caller then skips that entity,
// exactly as the seed-graph builder skips an unseedable root.
func bakeEntityFactory(byName map[string]schemadef.Table, root string, ent dbEntity, pools seedplan.EnumPools, bounds seedplan.CheckBounds, vocab *seedplan.Vocab, minimal bool) (entityFactorySpec, bool) {
	all := make([]schemadef.Table, 0, len(byName))
	for _, t := range byName {
		all = append(all, t)
	}
	closure := seedplan.FKClosure(all, root)
	closureTables := make([]schemadef.Table, 0, len(closure))
	for _, n := range closure {
		closureTables = append(closureTables, byName[n])
	}
	plan, err := seedplan.BuildPlan(closureTables, pools, seedplan.Config{Rows: 1, Salt: 0, Minimal: minimal})
	if err != nil {
		return entityFactorySpec{}, false
	}
	plan.SetBounds(bounds)
	plan.ApplyVocab(vocab)

	rootStmt := ""
	var parentStmts []string
	rootPrefix := "INSERT INTO " + seedplan.QualifiedTable(byName[root]) + " ("
	for _, stmt := range plan.Statements() {
		if strings.HasPrefix(stmt, rootPrefix) {
			rootStmt = stmt
		} else {
			parentStmts = append(parentStmts, strings.TrimSpace(stmt))
		}
	}
	if rootStmt == "" {
		return entityFactorySpec{}, false
	}

	// Bind the seeded PK literal to $1 so each factory call inserts a fresh row
	// (the Go side passes a new ULID). The PK is a synthesized unique value, so
	// its quoted literal occurs exactly once in the root statement.
	pkRaw, ok := plan.SeedValue(root, ent.pkColumn, 0)
	if !ok {
		return entityFactorySpec{}, false
	}
	pkLit := "'" + strings.ReplaceAll(pkRaw, "'", "''") + "'"
	if !strings.Contains(rootStmt, pkLit) {
		return entityFactorySpec{}, false
	}
	rootParamSQL := strings.Replace(strings.TrimSpace(rootStmt), pkLit, "$1", 1)

	return entityFactorySpec{
		goName:     ent.goName,
		lower:      lowerFirst(ent.goName),
		parentSQL:  strings.Join(parentStmts, "\n"),
		rootSQL:    rootParamSQL,
		appendOnly: byName[root].AppendOnly(),
	}, true
}

// parseDBEntities reads the generated internal/db/*.go ORM structs and returns
// the table↔Go-name mapping (plus the PK column and whether it is a single
// string PK). A struct is an entity when its embedded bun.BaseModel field
// carries a `bun:"table:<name>,…"` tag; the PK column is the field tagged `,pk`.
func parseDBEntities(dbDir string) map[string]dbEntity {
	out := map[string]dbEntity{}
	entries, err := os.ReadDir(dbDir)
	if err != nil {
		return out
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, filepath.Join(dbDir, name), nil, parser.SkipObjectResolution)
		if perr != nil {
			continue
		}
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					continue
				}
				if ent, ok := entityFromStruct(ts.Name.Name, st); ok {
					out[ent.table] = ent
				}
			}
		}
	}
	return out
}

// entityFromStruct extracts the table name and PK metadata from one struct's
// bun tags, or ok=false when the struct isn't a bun entity (no table tag).
func entityFromStruct(goName string, st *ast.StructType) (dbEntity, bool) {
	ent := dbEntity{goName: goName}
	found := false
	for _, f := range st.Fields.List {
		if f.Tag == nil {
			continue
		}
		tag, terr := strconv.Unquote(f.Tag.Value)
		if terr != nil {
			continue
		}
		bunTag := reflect.StructTag(tag).Get("bun")
		if bunTag == "" {
			continue
		}
		parts := strings.Split(bunTag, ",")
		for _, p := range parts {
			if strings.HasPrefix(p, "table:") {
				ent.table = strings.TrimPrefix(p, "table:")
				found = true
			}
		}
		if hasTagOption(parts, "pk") {
			ent.pkColumn = parts[0]
			ent.pkString = isStringFieldType(f.Type)
		}
	}
	if !found || ent.table == "" || ent.pkColumn == "" {
		return dbEntity{}, false
	}
	return ent, true
}

func hasTagOption(parts []string, opt string) bool {
	for _, p := range parts {
		if p == opt {
			return true
		}
	}
	return false
}

// isStringFieldType reports whether a struct field's type is `string` (the PK
// shape db.Get<Entity>ByID / a ULID insert require). Pointer/other types are
// out of scope for the factory.
func isStringFieldType(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == "string"
}

// renderEntityFactoryFile renders one handler package's forge-owned
// factories_gen_test.go: the typed New<Entity> row factories and the
// New<CreateRequest> request factories the scaffolded lifecycle test calls.
// Baked SQL is a raw string literal where it can be, strconv.Quote otherwise,
// so any character stays a valid Go string.
//
// dbImportPath is the ORM package (database.orm.package), imported as `db`
// whatever its name, since every row factory spells its rows `db.<Entity>`.
//
// g.pkg is the handler package's own clause (`order`), so the file compiles
// INTO that package's test binary and its exported factories are visible to the
// directory's internal and external (`order_test`) test files alike.
func renderEntityFactoryFile(dbImportPath string, g factoryGroup) []byte {
	pkgName := g.pkg
	var b strings.Builder
	b.WriteString("// Code generated by forge. DO NOT EDIT.\n")
	b.WriteString("// forge-owned: regenerated every run — do not edit (forge project disown to take ownership)\n")
	b.WriteString("// Source: the APPLIED schema (db/migrations), baked through forge's seed planner.\n")
	b.WriteString("//\n")
	b.WriteString("// Typed test factories, derived from your schema on every `forge generate`:\n")
	b.WriteString("//\n")
	b.WriteString("//   - New<Entity>(t, db, overrides…) inserts one MINIMAL valid row and returns\n")
	b.WriteString("//     the typed *db.<Entity>: every NOT NULL column without a DEFAULT and every\n")
	b.WriteString("//     FK parent satisfied, every other column left to its DEFAULT or NULL, and\n")
	b.WriteString("//     every CHECK honoured — so the row sits at its initial lifecycle state.\n")
	b.WriteString("//     Each call gets a fresh primary key: `item := NewItem(t, database)`.\n")
	b.WriteString("//   - New<CreateRequest>(t, db, variant) seeds a create RPC's FK parents and\n")
	b.WriteString("//     returns a request the schema accepts; variants 0 and 1 are distinct rows.\n")
	b.WriteString("//     The scaffolded handlers_crud_test.go builds its rows from these, so a\n")
	b.WriteString("//     migration edit flows in here instead of breaking a file forge never\n")
	b.WriteString("//     rewrites.\n")
	b.WriteString("//\n")
	b.WriteString("// WHY THIS IS A `_test.go` FILE. The toolchain compiles it only into this\n")
	b.WriteString("// package's test binary, so the `testing` import below is never linked into\n")
	b.WriteString("// cmd/. These factories used to live in their own non-test package\n")
	b.WriteString("// (internal/testfactory) to be importable from anywhere; that made a non-test\n")
	b.WriteString("// .go file importing `testing` part of the tree, safe only while nothing in\n")
	b.WriteString("// the production import graph happened to reach it.\n")
	b.WriteString("//\n")
	b.WriteString("// The package clause is `" + pkgName + "`, NOT `" + pkgName + "_test`. Go compiles a package's\n")
	b.WriteString("// in-package _test.go files INTO the package under test, and the external\n")
	b.WriteString("// `" + pkgName + "_test` package then imports that augmented package — so these\n")
	b.WriteString("// factories are visible to BOTH the internal and the external test files in\n")
	b.WriteString("// this directory (including the scaffolded CRUD lifecycle test), and to no\n")
	b.WriteString("// other package. See the forge `testing` skill.\n")
	b.WriteString("package " + pkgName + "\n\n")

	needTimestamppb, needTime := createSpecsNeed(g.creates)
	b.WriteString("import (\n")
	b.WriteString("\t\"context\"\n")
	b.WriteString("\t\"testing\"\n")
	if needTime {
		b.WriteString("\t\"time\"\n")
	}
	b.WriteString("\n")
	if len(g.specs) > 0 {
		b.WriteString("\t\"github.com/oklog/ulid/v2\"\n")
	}
	b.WriteString("\t\"github.com/reliant-labs/forge/pkg/orm\"\n")
	if needTimestamppb {
		b.WriteString("\t\"google.golang.org/protobuf/types/known/timestamppb\"\n")
	}
	if len(g.specs) > 0 || len(g.creates) > 0 {
		b.WriteString("\n")
	}
	if len(g.creates) > 0 {
		fmt.Fprintf(&b, "\tpb %s\n", strconv.Quote(g.pbImport))
		for _, alias := range sortedEnumImportAliases(g.creates) {
			fmt.Fprintf(&b, "\t%s %s\n", alias, strconv.Quote(createSpecsEnumImports(g.creates)[alias]))
		}
	}
	if len(g.specs) > 0 {
		fmt.Fprintf(&b, "\tdb %s\n", strconv.Quote(dbImportPath))
	}
	b.WriteString(")\n\n")

	b.WriteString(entityFactoryHelperSource)

	for _, s := range g.specs {
		renderRowFactory(&b, s)
	}
	for _, s := range g.creates {
		renderCreateRequestFactory(&b, s)
	}
	// gofmt here rather than trusting a later pass: request literals carry
	// fields of uneven width, and the bytes this returns are what the
	// checksum ledger and the idempotency check compare.
	if formatted, err := format.Source([]byte(b.String())); err == nil {
		return formatted
	}
	return []byte(b.String())
}

// renderRowFactory writes one entity's New<Entity> row factory into b.
func renderRowFactory(b *strings.Builder, s entityFactorySpec) {
	fmt.Fprintf(b, "\n// --- %s ---\n\n", s.goName)
	if !s.appendOnly {
		fmt.Fprintf(b, "// %sOverride mutates the *db.%s New%s is about to insert.\n", s.goName, s.goName, s.goName)
		fmt.Fprintf(b, "type %sOverride func(*db.%s)\n\n", s.goName, s.goName)
	}

	if s.failure == "" {
		if s.parentSQL != "" {
			fmt.Fprintf(b, "const %sFactoryParentSQL = %s\n\n", s.lower, backquoteOrQuote(s.parentSQL))
		}
		fmt.Fprintf(b, "const %sFactoryRootSQL = %s\n\n", s.lower, backquoteOrQuote(s.rootSQL))
	}

	fmt.Fprintf(b, "// New%s inserts one MINIMAL %s row and returns it: every NOT NULL\n", s.goName, s.goName)
	b.WriteString("// column without a DEFAULT and every FK parent satisfied (forge's seed\n")
	b.WriteString("// planner), every other column left to its DEFAULT or NULL, every CHECK\n")
	b.WriteString("// honoured. Each call gets a fresh primary key, so call it once per row.\n")
	if s.appendOnly {
		// No override seam: applying one means writing the loaded row
		// back, and this table refuses UPDATE at the database.
		b.WriteString("//\n")
		fmt.Fprintf(b, "// %s is APPEND-ONLY, so this factory takes no overrides — applying\n", s.goName)
		b.WriteString("// one would mean UPDATEing the row it just inserted, which the table's\n")
		b.WriteString("// guard rejects. A test needing particular values inserts the row itself.\n")
		fmt.Fprintf(b, "func New%s(t testing.TB, database orm.Context) *db.%s {\n", s.goName, s.goName)
	} else {
		b.WriteString("// Override the columns your test asserts on; leave the rest to the schema:\n")
		b.WriteString("//\n")
		fmt.Fprintf(b, "//\t%s := New%s(t, database, func(x *db.%s) { /* x.Field = … */ })\n", s.lower, s.goName, s.goName)
		b.WriteString("//\n")
		b.WriteString("// A single-column-unique NOT-NULL field other than the primary key keeps its\n")
		b.WriteString("// seeded value across calls — override such a field yourself to insert more\n")
		b.WriteString("// than one row.\n")
		fmt.Fprintf(b, "func New%s(t testing.TB, database orm.Context, overrides ...%sOverride) *db.%s {\n", s.goName, s.goName, s.goName)
	}
	b.WriteString("\tt.Helper()\n")
	if s.failure != "" {
		// The signature stays, so callers compile; there is no row to insert.
		fmt.Fprintf(b, "\tt.Fatalf(\"New%s: postgres rejects every row forge could plan for this table (seen at `forge generate`): %%s\", %s)\n",
			s.goName, backquoteOrQuote(s.failure))
		b.WriteString("\treturn nil\n")
		b.WriteString("}\n")
		return
	}
	if s.parentSQL != "" {
		fmt.Fprintf(b, "\tseedFactoryParents(t, database, %sFactoryParentSQL)\n", s.lower)
	}
	b.WriteString("\tid := ulid.Make().String()\n")
	fmt.Fprintf(b, "\tif _, err := database.Exec(context.Background(), %sFactoryRootSQL, id); err != nil {\n", s.lower)
	fmt.Fprintf(b, "\t\tt.Fatalf(\"New%s: insert row: %%v\", err)\n", s.goName)
	b.WriteString("\t}\n")
	fmt.Fprintf(b, "\trow, err := db.Get%sByID(context.Background(), database, id)\n", s.goName)
	b.WriteString("\tif err != nil {\n")
	fmt.Fprintf(b, "\t\tt.Fatalf(\"New%s: load inserted row: %%v\", err)\n", s.goName)
	b.WriteString("\t}\n")
	if !s.appendOnly {
		b.WriteString("\tif len(overrides) > 0 {\n")
		b.WriteString("\t\tfor _, o := range overrides {\n")
		b.WriteString("\t\t\to(row)\n")
		b.WriteString("\t\t}\n")
		fmt.Fprintf(b, "\t\tif err := db.Update%s(context.Background(), database, row); err != nil {\n", s.goName)
		fmt.Fprintf(b, "\t\t\tt.Fatalf(\"New%s: apply overrides: %%v\", err)\n", s.goName)
		b.WriteString("\t\t}\n")
		b.WriteString("\t}\n")
	}
	b.WriteString("\treturn row\n")
	b.WriteString("}\n")
}

// entityFactoryHelperSource is the schema-independent shared helper.
const entityFactoryHelperSource = `// seedFactoryParents seeds an entity's FK-ancestor spine. Every INSERT is
// ON CONFLICT DO NOTHING, so repeated factory calls (and factories for sibling
// entities that share ancestors) converge on one shared parent spine.
func seedFactoryParents(t testing.TB, database orm.Context, sql string) {
	t.Helper()
	if sql == "" {
		return
	}
	if _, err := database.Exec(context.Background(), sql); err != nil {
		t.Fatalf("factory: seed FK parents: %v", err)
	}
}
`

// backquoteOrQuote renders s as a raw (backtick) Go string literal when it
// contains no backtick, else falls back to strconv.Quote. Baked seed SQL is
// multi-line, so a raw literal keeps it readable in the generated file.
func backquoteOrQuote(s string) string {
	if !strings.Contains(s, "`") {
		return "`" + s + "`"
	}
	return strconv.Quote(s)
}

// GenerateEntityFactories writes one forge-owned factories_gen_test.go per
// handler package, holding the typed New<Entity> row factories for the entities
// that package's CRUD RPCs own, and the New<CreateRequest> request factories
// for its create RPCs (create_request_factory.go).
//
// Forge-owned + checksum-tracked, and regenerated on every run from the
// APPLIED schema. A project with no schema to model writes nothing. A schema
// that cannot be READ (an unreachable shadow server, a migration that does not
// apply) also writes nothing — the file on disk is left as it was rather than
// rewritten from no information — and only an embedded-postgres fetch failure
// is returned, so the caller can say why. A factory forge cannot derive a valid
// row or request for is still EMITTED, with a body that fails the calling test
// naming postgres's reason, and that reason is printed here as a warning: the
// scaffold-once lifecycle test calls these by name, so leaving one out would
// break compilation of every test beside it.
//
// An entity whose CRUD RPCs no service declares is SKIPPED rather than parked
// in a shared package. The factory's whole value is being callable from the
// test that exercises that entity's handlers, and Go's in-package-_test.go
// visibility rule means it can only be called from the directory it lands in —
// so an entity with no owning handler package has no directory that could use
// it. Emitting one anyway would reintroduce exactly the importable-from-
// anywhere non-test package this move removed.
func GenerateEntityFactories(projectDir, modulePath string, services []ServiceDef, cs *checksums.FileChecksums) error {
	fx, err := loadCRUDTestFixtures(projectDir, nil)
	if err != nil {
		var fetchErr *pgtest.FetchError
		if errors.As(err, &fetchErr) {
			return err
		}
		return nil
	}
	if fx == nil {
		return nil
	}
	defer fx.close()
	ctx := context.Background()

	byService := groupFactorySpecsByService(projectDir, services, buildEntityFactorySpecs(ctx, projectDir, fx))

	// The create-request factories. Each create RPC's entity gets its minimal
	// seed plan (parents + the entity's own row shape) before its request is
	// derived from it.
	entities := entitiesForTables(fx.tableList(), services)
	for _, svc := range services {
		methods := MatchCRUDMethods(svc, entities)
		var creates []CRUDMethod
		for _, cm := range methods {
			if cm.Operation == "create" {
				creates = append(creates, cm)
				fx.ensurePlan(cm.Entity.TableName)
			}
		}
		if len(creates) == 0 || svc.GoPackage == "" {
			continue
		}
		res, err := ResolveServiceComponent(projectDir, svc.Name)
		if err != nil {
			continue // not scaffolded yet; no lifecycle test calls these
		}
		g := byService[res.Dir]
		if g.pbImport != "" && g.pbImport != svc.GoPackage {
			// Two services' wire packages in one handler directory: the
			// request literals all spell their enums against `pb`, so only
			// the first service's requests can be rendered here.
			fmt.Fprintf(os.Stderr, "Warning: %s shares handler directory %s with another service; its create-request factories were not emitted\n", svc.Name, res.ImportLeaf)
			continue
		}
		g.pkg = res.PackageName
		g.pbImport = svc.GoPackage
		g.creates = append(g.creates, buildCreateRequestSpecs(ctx, svc, creates, fx)...)
		byService[res.Dir] = g
	}

	for _, dir := range sortedFactoryDirs(byService) {
		group := byService[dir]
		rel, err := filepath.Rel(projectDir, filepath.Join(dir, entityFactoryFile))
		if err != nil {
			return fmt.Errorf("resolve factory path for %s: %w", group.pkg, err)
		}
		for _, s := range group.specs {
			if s.failure != "" {
				fmt.Fprintf(os.Stderr, "Warning: %s: New%s cannot insert a row its schema accepts; calling it fails the test:\n  %s\n", rel, s.goName, s.failure)
			}
		}
		for _, s := range group.creates {
			if s.failure != "" {
				fmt.Fprintf(os.Stderr, "Warning: %s: %s cannot build a request its schema accepts; calling it fails the test:\n  %s\n", rel, s.funcName, strings.ReplaceAll(s.failure, "\n", "\n  "))
			}
		}
		content := renderEntityFactoryFile(modulePath+"/"+ProjectORMPackage(projectDir).PackageDir(), group)
		if err := writeForgeOwned(projectDir, rel, content, cs); err != nil {
			return fmt.Errorf("write %s: %w", rel, err)
		}
	}
	return nil
}

// entityFactoryFile is the file name emitted into each handler package. The
// `_test.go` suffix is load-bearing — see the file header.
const entityFactoryFile = "factories_gen_test.go"

// factoryGroup is one handler package's share of the factories: the package
// clause to render under, the entity row factories it owns, and its create
// RPCs' request factories with the wire package they build.
type factoryGroup struct {
	pkg      string
	specs    []entityFactorySpec
	creates  []createRequestSpec
	pbImport string // the service's Go proto package, imported as pb
}

// groupFactorySpecsByService routes each entity spec to the handler directory
// whose service declares that entity's CRUD RPCs, keyed by absolute dir.
//
// The routing question — "which service owns entity E?" — is the same one
// MatchCRUDMethods answers when it pairs a CRUD method with an entity, and it
// is answered from the same evidence: a method named Create<E>/Get<E>/
// Update<E>/Delete<E>/List<Es> on that service. Re-using ParseCRUDOperation
// keeps the two from drifting into disagreeing about ownership.
//
// An entity claimed by two services lands in both. That is deliberate: each
// package's factory is private to its own test binary, so there is no
// redeclaration between them, and a test in either directory can seed the row
// it needs.
func groupFactorySpecsByService(projectDir string, services []ServiceDef, specs []entityFactorySpec) map[string]factoryGroup {
	out := map[string]factoryGroup{}
	for _, svc := range services {
		owned := ownedFactorySpecs(svc, specs)
		if len(owned) == 0 {
			continue
		}
		res, err := ResolveServiceComponent(projectDir, svc.Name)
		if err != nil {
			// No handler dir on disk (declared but not yet scaffolded).
			// Skipping matches renderComponentTestHelpers rather than
			// conjuring a stray directory.
			continue
		}
		g := out[res.Dir]
		g.pkg = res.PackageName
		g.specs = append(g.specs, owned...)
		out[res.Dir] = g
	}
	return out
}

// ownedFactorySpecs returns the specs for entities svc declares CRUD RPCs for.
func ownedFactorySpecs(svc ServiceDef, specs []entityFactorySpec) []entityFactorySpec {
	claimed := map[string]bool{}
	for _, m := range svc.Methods {
		op, entityName := CRUDShape(m) // unary, not forge:custom — same as MatchCRUDMethods
		if op == "" {
			continue
		}
		claimed[strings.ToLower(entityName)] = true
		// `ListOrders` names the entity in the plural; the spec is keyed
		// by the singular Go type name.
		if op == "list" {
			claimed[strings.ToLower(inflection.Singular(entityName))] = true
		}
	}
	var owned []entityFactorySpec
	for _, s := range specs {
		if claimed[strings.ToLower(s.goName)] {
			owned = append(owned, s)
		}
	}
	return owned
}

// sortedFactoryDirs orders the emission targets so a run's writes are
// deterministic (the generate pipeline's idempotency check compares bytes).
func sortedFactoryDirs(byService map[string]factoryGroup) []string {
	dirs := make([]string, 0, len(byService))
	for dir := range byService {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	return dirs
}

// RetiredEntityFactoryRelPath is the pre-move location of the entity factories:
// their own non-test package, which every project scaffolded before this change
// still carries. GenerateEntityFactories' caller retires it so an upgrading
// project does not end up with both copies — the old one being a non-test .go
// file that imports `testing`, which is the shape this move exists to remove.
func RetiredEntityFactoryRelPath() string {
	return filepath.Join("internal", "testfactory", "factory_gen.go")
}

// createSpecsEnumImports merges the foreign enum packages every request
// factory in a group references.
func createSpecsEnumImports(specs []createRequestSpec) map[string]string {
	merged := map[string]string{}
	for _, s := range specs {
		if s.failure != "" {
			continue
		}
		for alias, path := range s.imports {
			merged[alias] = path
		}
	}
	return merged
}

func sortedEnumImportAliases(specs []createRequestSpec) []string {
	merged := createSpecsEnumImports(specs)
	aliases := make([]string, 0, len(merged))
	for alias := range merged {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	return aliases
}
