package generator

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// GeneratePlanORM writes internal/db/<snake_name>_orm.go for each entity
// defined in the plan. The generated files provide a Bun-tagged entity
// struct (a projection of the applied schema) and standalone CRUD
// functions built on Bun's query builder (reached via orm.Context.Bun()).
// CRUD is emitted as package-level functions that take an orm.Context, so
// the same functions run inside or outside a transaction.
//
// cs is the project's checksum tracker: every output is recorded as a
// Tier-1 manifest entry so `forge project disown` can transfer ownership of an
// ORM file (kalshi fr-4dfef712e9: disown refused on internal/db/*_orm.go
// because nothing registered them) and `forge project audit` doesn't see the
// files as orphans. A nil cs is tolerated (files written, not tracked).
//
// orm places the projection (database.orm.package, default internal/db):
// every file lands in that directory and declares its package.
func GeneratePlanORM(root string, orm config.ORMConfig, entities []config.PlanEntity, cs *FileChecksums) error {
	if len(entities) == 0 {
		return nil
	}

	// Reject an out-of-vocabulary field type BEFORE anything is written:
	// the projection below answers `string` for a type it does not map,
	// which is indistinguishable from the answer for the one type that
	// really is a string. Checked here rather than inside the render so a
	// rejected entity leaves no half-written internal/db behind.
	if err := validatePlanFieldTypes(entities); err != nil {
		return err
	}

	relDir := orm.PackageDir()
	pkgName := orm.PackageName()
	dbDir := filepath.Join(root, filepath.FromSlash(relDir))

	// Render everything first, so a name the project already declares in
	// internal/db is refused — naming its declaration — before a single
	// file is written, rather than surfacing as `X redeclared` from the
	// validate build after the whole generate ran.
	outputs := make([]ormOutput, 0, len(entities)+1)
	for _, ent := range entities {
		outputs = append(outputs, ormOutput{
			file:   naming.ToSnakeCase(ent.Name) + "_orm_gen.go",
			entity: ent.Name,
			src:    renderORMEntity(pkgName, ent, hasRepoExtExtraType(root, orm, ent.Name)),
		})
	}
	outputs = append(outputs, ormOutput{file: storeGenFile, src: renderStoreInterfaces(pkgName, entities)})
	if err := checkORMNameCollisions(dbDir, relDir, entities, outputs); err != nil {
		return err
	}

	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		return fmt.Errorf("create db directory: %w", err)
	}

	// Clean up stale ORM files for entities that no longer exist —
	// but never a file the user owns (Tier-2 / disowned / legacy-forked
	// entries): a disowned ORM file is the user's content now, and the
	// entity disappearing from the projection doesn't change that.
	// Removed forge-owned files are pruned from the manifest too.
	expectedFiles := make(map[string]bool)
	expectedFiles["types.go"] = true
	for _, ent := range entities {
		expectedFiles[naming.ToSnakeCase(ent.Name)+"_orm_gen.go"] = true
	}
	sweepStaleORMFiles(root, relDir, expectedFiles, false, cs)
	// A project that moved its ORM out of internal/db (database.orm.package)
	// leaves forge's previous projection behind there; reclaim it, store
	// file included, so two projections of one schema never coexist. The
	// _repo_ext.go seams are the user's and stay where they are.
	if relDir != config.DefaultORMPackage {
		sweepStaleORMFiles(root, config.DefaultORMPackage, nil, true, cs)
	}

	// internal/db/orm_shared_gen.go is RETIRED. It declared exactly one
	// symbol, `var ormTracer = otel.Tracer("orm")`, left over from the
	// pre-generic per-entity ORM that opened its own spans. pkg/crud owns
	// the tracing now and keeps the same tracer name (crud.repoTracer), so
	// the generated var has had NO referent since the generic Repo landed:
	// a rendered project compiles with the file deleted.
	//
	// It was also byte-identical in every project ever generated — the
	// tierguard differential render is what surfaced it — so it was library
	// code, and dead library code at that, sitting in the user's tree
	// behind a "do not edit" banner.
	codegen.RetireGenerated(root, filepath.Join(filepath.FromSlash(relDir), "orm_shared_gen.go"), cs)

	for _, ent := range entities {
		// The custom-query seam: a scaffold-once, user-owned sibling file
		// documenting where hand-written / raw-SQL queries for this entity
		// go, and carrying the <Entity>Extra computed-field type. It reuses
		// the generated <entity>Repo, the package-level delegates, and db
		// handle. Routed through the write-if-absent scaffold writer so
		// it's NEVER overwritten after first scaffold (sibling files in
		// package db survive the *_orm.go regen sweep below). Written
		// BEFORE the entity struct so a same-run birth is visible to the
		// hasRepoExtExtraType check that follows.
		if err := writeRepoExtSeam(root, orm, ent, cs); err != nil {
			return fmt.Errorf("scaffold repo-ext seam for entity %s: %w", ent.Name, err)
		}

		name := naming.ToSnakeCase(ent.Name) + "_orm_gen.go"
		hasExtra := hasRepoExtExtraType(root, orm, ent.Name)
		if err := writeORMFile(root, orm, name, renderORMEntity(pkgName, ent, hasExtra), cs); err != nil {
			return fmt.Errorf("generate ORM for entity %s: %w", ent.Name, err)
		}
	}

	// The interface shape of everything just written. Emitted from the SAME
	// entity slice as the delegates above, so the two cannot describe
	// different schemas — and carrying compile-time assertions, so a drift
	// between a delegate and its interface fails at generate time rather
	// than in a user's package.
	if err := writeStoreInterfaces(root, orm, entities, cs); err != nil {
		return fmt.Errorf("generate store interfaces: %w", err)
	}

	// internal/db/types.go was the proto-entity era's alias file; a stale
	// alias to a deleted pb type would shadow a generated struct. It is
	// removed only when forge wrote it: `types.go` is the most ordinary
	// name a hand-written data layer has, and this used to delete one
	// unconditionally on the first generate that projected an entity.
	if body, rerr := os.ReadFile(filepath.Join(dbDir, "types.go")); rerr == nil && forgeWroteFile(body) {
		if err := checksums.RemoveJournaled(filepath.Join(dbDir, "types.go")); err != nil {
			return fmt.Errorf("retire %s/types.go: %w", relDir, err)
		}
	}
	return nil
}

// sweepStaleORMFiles removes forge's ORM output in relDir that this run
// does not rewrite — an entity whose table is gone, or the pre-rename
// "_orm.go" spelling — but never a file the user owns (Tier-2 / disowned /
// hand-edited): a disowned ORM file is the user's content now, and the
// entity disappearing from the projection doesn't change that. withStore
// sweeps store_gen.go too, for a directory the ORM has moved out of.
func sweepStaleORMFiles(root, relDir string, expected map[string]bool, withStore bool, cs *FileChecksums) {
	dir := filepath.Join(root, filepath.FromSlash(relDir))
	existing, _ := os.ReadDir(dir)
	for _, e := range existing {
		// Both spellings are swept: "_orm_gen.go" is the current name and
		// "_orm.go" the pre-rename one, so an upgrading project's old copies
		// are reclaimed by the same pass that prunes deleted entities.
		nm := e.Name()
		isORM := strings.HasSuffix(nm, "_orm.go") || strings.HasSuffix(nm, "_orm_gen.go") ||
			nm == "orm_shared.go" || (withStore && nm == storeGenFile)
		if e.IsDir() || !isORM || expected[nm] {
			continue
		}
		rel := filepath.Join(filepath.FromSlash(relDir), nm)
		if cs.IsDisowned(rel) {
			continue // user-owned by recorded intent — not forge's to delete
		}
		full := filepath.Join(dir, nm)
		body, rerr := os.ReadFile(full)
		if rerr != nil {
			continue
		}
		// Ownership is read from the file itself: a verifying marker (or,
		// for pre-marker output, the forge banner) certifies forge bytes;
		// a hand-edited (Modified) file is the user's now.
		// Journaled deletes: an aborted run must restore what it swept.
		switch checksums.Verify(body) {
		case checksums.Pristine:
			_ = checksums.RemoveJournaled(full)
		case checksums.Modified:
			continue
		case checksums.NoMarker:
			if bytes.Contains(body, []byte("Code generated by forge")) {
				_ = checksums.RemoveJournaled(full)
			}
		}
	}
}

// writeRepoExtSeam scaffolds <orm package>/<entity>_repo_ext.go ONCE: a
// discoverable, empty-but-documented home for hand-written queries that
// reuse the generated CRUD machinery. Scaffold-once ("yours") — once on
// disk forge never touches it, so the user's custom methods are safe
// across every future `forge generate`.
func writeRepoExtSeam(root string, orm config.ORMConfig, ent config.PlanEntity, _ *FileChecksums) error {
	name := naming.ToSnakeCase(ent.Name) + "_repo_ext.go"
	rel := filepath.Join(filepath.FromSlash(orm.PackageDir()), name)
	_, err := WriteScaffoldIfMissing(root, rel, renderRepoExtSeam(orm.PackageName(), ent))
	return err
}

// renderRepoExtSeam renders the per-entity custom-query seam. It compiles
// as-is (no unused imports, no undefined references) and shows — in a
// commented example — how to add a method that reuses the generated
// repo/delegates and drops to raw SQL via the Bun handle.
func renderRepoExtSeam(pkgName string, ent config.PlanEntity) []byte {
	msgName := ent.Name
	pkGoType := "string"
	for _, f := range resolveORMFields(ent) {
		if f.isPK {
			pkGoType = f.structGoType()
			break
		}
	}
	repoVar := lowerFirst(msgName) + "Repo"

	var b strings.Builder
	fmt.Fprintf(&b, "// yours: scaffolded once, never touched again — forge will not overwrite this file.\n")
	fmt.Fprintf(&b, "//\n")
	fmt.Fprintf(&b, "// This is the custom-query seam for %s. The generated %s_orm.go is\n", msgName, naming.ToSnakeCase(msgName))
	b.WriteString("// Tier-1 (a live projection of the migrations, regenerated every run);\n")
	b.WriteString("// hand-written queries that outgrow the generated CRUD belong HERE, in a\n")
	b.WriteString("// sibling file forge never rewrites. You have three building blocks:\n")
	b.WriteString("//\n")
	fmt.Fprintf(&b, "//   1. The generated repo + delegates: %s (a *crud.Repo[%s]) and the\n", repoVar, msgName)
	// The seam must name only the delegates that EXIST. An append-only
	// entity has no Update/Delete to reuse, and advertising them sends the
	// reader hunting for a symbol forge deliberately refuses to emit.
	if ent.ReadOnly {
		fmt.Fprintf(&b, "//      package-level Get%sByID (when the view has a key) / List%s / Count%s —\n", msgName, msgName, msgName)
		fmt.Fprintf(&b, "//      %s is a read-only VIEW, so nothing writes through it; the\n", msgName)
	} else if keys := compositeKey(resolveORMFields(ent)); keys != nil {
		fmt.Fprintf(&b, "//      package-level Get%sByPK / List%s / Create%s / Upsert%s, keyed by\n", msgName, msgName, msgName, msgName)
		fmt.Fprintf(&b, "//      the whole composite key %s", keyColumns(keys))
		if ent.AppendOnly {
			b.WriteString(" (append-only: no Update/Delete)\n")
		} else {
			fmt.Fprintf(&b, ", plus Update%s / Delete%sByPK\n", msgName, msgName)
		}
	} else if ent.AppendOnly {
		fmt.Fprintf(&b, "//      package-level Get%sByID / List%s / Create%s — %s is append-only,\n", msgName, msgName, msgName, msgName)
		b.WriteString("//      so there is no Update/Delete delegate to reuse; correct a bad row\n")
		b.WriteString("//      by appending its reversal\n")
	} else {
		fmt.Fprintf(&b, "//      package-level Get%sByID / List%s / Create%s / Update%s / Delete%s\n", msgName, msgName, msgName, msgName, msgName)
	}
	b.WriteString("//      functions — compose orm.QueryOption filters (orm.WhereEq,\n")
	b.WriteString("//      orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.\n")
	b.WriteString("//   2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you\n")
	b.WriteString("//      NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or\n")
	b.WriteString("//      anything the builder can't express. Scan into the generated\n")
	fmt.Fprintf(&b, "//      %s struct (or your own row type).\n", msgName)
	b.WriteString("//   3. orm.Context: every query takes one. Take no transaction parameter:\n")
	b.WriteString("//      called with the ctx that s.deps.DB.RunTx hands its fn, a method\n")
	b.WriteString("//      here (db.Bun() builders and raw SQL included) joins that\n")
	b.WriteString("//      transaction; with any other ctx it runs on the pool.\n")
	b.WriteString("//\n")
	b.WriteString("// Example (uncomment and adapt):\n")
	b.WriteString("//\n")
	fmt.Fprintf(&b, "//\tfunc Find%sBy(ctx context.Context, db orm.Context, /* args */) ([]*%s, error) {\n", msgName, msgName)
	fmt.Fprintf(&b, "//\t\tvar rows []*%s\n", msgName)
	fmt.Fprintf(&b, "//\t\terr := db.Bun().NewSelect().Model(&rows).\n")
	fmt.Fprintf(&b, "//\t\t\tWhere(\"%s = ?\", /* value */).\n", firstNonPKColumn(ent, pkGoType))
	b.WriteString("//\t\t\tScan(ctx)\n")
	b.WriteString("//\t\treturn rows, err\n")
	b.WriteString("//\t}\n")
	b.WriteString("//\n")
	fmt.Fprintf(&b, "// Or reuse the generated repo for a filtered list:\n")
	fmt.Fprintf(&b, "//\n")
	fmt.Fprintf(&b, "//\tfunc Active%s(ctx context.Context, db orm.Context) ([]*%s, error) {\n", naming.Pluralize(msgName), msgName)
	fmt.Fprintf(&b, "//\t\treturn List%s(ctx, db, orm.WhereEq(\"%s\", true))\n", msgName, firstNonPKColumn(ent, pkGoType))
	b.WriteString("//\t}\n")
	fmt.Fprintf(&b, "package %s\n\n", pkgName)

	extraType := entityExtraTypeName(msgName)
	fmt.Fprintf(&b, "// %s carries fields on %s that are NOT columns — computed\n", extraType, msgName)
	fmt.Fprintf(&b, "// values, transient state, anything derived rather than stored. %s is\n", msgName)
	fmt.Fprintf(&b, "// embedded on %s (see %s_orm.go), so a field added here is a field on\n", extraType, naming.ToSnakeCase(msgName))
	fmt.Fprintf(&b, "// every %s value.\n", msgName)
	b.WriteString("//\n")
	b.WriteString("// Every field MUST carry the `bun:\"-\"` tag. Bun otherwise treats an\n")
	b.WriteString("// embedded struct's fields as columns of the parent table and tries to\n")
	b.WriteString("// persist them — `bun:\"-\"` is Bun's own idiom for a field it must never\n")
	b.WriteString("// bind or scan.\n")
	fmt.Fprintf(&b, "type %s struct {\n", extraType)
	b.WriteString("}\n")

	if formatted, err := format.Source([]byte(b.String())); err == nil {
		return formatted
	}
	return []byte(b.String())
}

// entityExtraTypeName is the name of the embedded computed-field carrier
// for an entity — <Entity>Extra, declared once in <entity>_repo_ext.go and
// embedded into the generated <entity>_orm.go struct.
func entityExtraTypeName(msgName string) string { return msgName + "Extra" }

// hasRepoExtExtraType reports whether root's internal/db/<entity>_repo_ext.go
// already declares <Entity>Extra — the signal that gates the embedded field
// on the generated entity struct.
//
// This is the backward-compatibility seam. <entity>_repo_ext.go is
// scaffold-once and user-owned from birth (WriteScaffoldIfMissing never
// rewrites it), so an EXISTING project's seam file predates this type and
// stays exactly as it is — forge does not, and must not, inject a
// declaration into a file it no longer touches. Embedding a reference to
// an undefined type would break that project's build, so the entity
// struct only gains the embedded field once the type is actually present:
// for a brand-new entity, that's this same `forge generate` run (the seam
// is scaffolded with the type just above); for an existing one, it's the
// moment the user hand-adds the type themselves — an ordinary edit to a
// file they already own, needing no forge-side migration.
//
// Parse errors and a missing file both answer false: an unparseable
// seam file is not proof the type exists, and the safe default is never
// to embed a reference to a type we can't confirm.
func hasRepoExtExtraType(root string, orm config.ORMConfig, entityName string) bool {
	path := filepath.Join(root, filepath.FromSlash(orm.PackageDir()), naming.ToSnakeCase(entityName)+"_repo_ext.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return false
	}
	want := entityExtraTypeName(entityName)
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if ok && ts.Name.Name == want {
				return true
			}
		}
	}
	return false
}

// firstNonPKColumn returns a representative non-PK column name for the
// doc example's WHERE clause, falling back to the PK column when the
// entity has only a primary key.
func firstNonPKColumn(ent config.PlanEntity, _ string) string {
	pk := ""
	for _, f := range resolveORMFields(ent) {
		if f.isPK {
			pk = f.columnName
			continue
		}
		return f.columnName
	}
	if pk != "" {
		return pk
	}
	return "id"
}

// writeORMFile writes one internal/db output through the Tier-1
// checksum chokepoint: the render is recorded in the manifest, a
// disowned entry is never overwritten (delete-and-regenerate is the
// documented re-adoption path), and force=true keeps the Tier-1
// regenerated-every-run semantics (hand-edit protection is the
// pre-pipeline drift guard's job, same as every other Tier-1 emitter).
func writeORMFile(root string, orm config.ORMConfig, name string, content []byte, cs *FileChecksums) error {
	rel := filepath.Join(filepath.FromSlash(orm.PackageDir()), name)
	// First-adoption warning: ORM files written by pre-marker forge are
	// on disk without a certification marker, so the drift guard can't
	// yet protect a hand-patch in them (kalshi carried exactly such a
	// patch). The overwrite itself matches the old regenerate-every-run
	// behavior — but now it's LOUD, and the marker stamped by the write
	// below makes every future edit guard-visible (and `forge project disown`
	// possible).
	if prior, rerr := os.ReadFile(filepath.Join(root, rel)); rerr == nil {
		if checksums.Verify(prior) == checksums.NoMarker && checksums.BodyHash(prior) != checksums.BodyHash(content) {
			fmt.Printf("  ⚠️  %s existed without a forge:hash marker and differed from the fresh render — overwriting and stamping it (recover prior content from git; use `forge project disown %s --reason ...` if you need to own it)\n", rel, rel)
		}
	}
	_, err := WriteGeneratedFileTier1(root, rel, content, cs, true)
	return err
}

// ormField holds the resolved metadata for a single entity field used during
// code generation. This mirrors the information in the protoc-based codegen's
// fieldInfo but is derived directly from PlanEntityField.
type ormField struct {
	planField   config.PlanEntityField
	goName      string // Go field name on the db struct: naming.ColumnGoName(column)
	columnName  string // snake_case column name
	goType      string // base Go type (notnull variant), e.g. "time.Time"
	isTimestamp bool
	isArray     bool
	isPK        bool
	notNull     bool
	unique      bool
	references  string
	nullable    bool   // nullable column → pointer struct field
	hasDefault  bool   // column has a DB DEFAULT (server-supplied value)
	columnDef   string // the DEFAULT expression verbatim, when hasDefault
	isIdentity  bool   // GENERATED ... AS IDENTITY (no DEFAULT in the catalog)
	// softDeleteNative is true on the deleted_at field when soft delete is
	// active AND the column projects to a proper time type (TIMESTAMPTZ →
	// time.Time). Bun's built-in ,soft_delete owns it; the generated CRUD
	// relies on Bun. False for a legacy TEXT deleted_at, which stays
	// hand-rolled (Bun's ,soft_delete stamps a time.Time that a TEXT column
	// can't round-trip — kalshi fr-3fba9166ba style).
	softDeleteNative bool
	// softDeleteLegacy is true on a soft-delete deleted_at that does NOT
	// project to a time (a legacy TEXT column): Bun's ,soft_delete cannot
	// own it, so it carries a `forge:"soft-delete"` struct tag and
	// pkg/crud hand-rolls the filter and the stamp. The repo reads soft
	// delete only from a tag — never from the column's name — so a
	// deleted_at declared `forge:no-soft-delete` carries neither and is an
	// ordinary column at runtime too.
	softDeleteLegacy bool
	// isGenerated marks a GENERATED ALWAYS AS (...) STORED column →
	// ,nullzero,skipupdate: postgres rejects the column in an INSERT column
	// list or an UPDATE SET, but it must still be SELECTed. See bunTag for
	// why ,scanonly (which silently drops it from the SELECT projection) is
	// the wrong mechanism.
	isGenerated bool
	// isSecret marks a `// forge:secret` column: preserved on a maskless
	// full-replace Update (the repo's Spec.SecretColumns) so a client
	// round-trip — which reads the secret back as "" — cannot clobber the
	// stored value. Still settable on Create and on an explicit masked Update.
	isSecret bool
	// isImmutable marks a column declared `forge:immutable` in its catalog
	// comment → Bun's ,skipupdate: a full-replace UPDATE leaves the stored
	// value alone, an INSERT still writes it.
	isImmutable bool
	// isVersion marks a column declared `forge:version` in its catalog
	// comment: the entity's optimistic-concurrency column. It gets Bun's
	// ,skipupdate (defense in depth — the generic Repo, not Bun's own
	// enforcement, owns the increment; see bunTag) PLUS a `forge:"version"`
	// struct tag in its own namespace, which is the signal
	// pkg/crud.Repo.ensureMeta reads at runtime to find it (see that
	// method's doc comment for why a second tag namespace rather than
	// a Bun option).
	isVersion bool
	// isFillULID marks a column declared `forge:fill=ulid` in its catalog
	// comment: a non-PK column the generic Repo ULID-generates at Create
	// when left at its Go zero. Like isVersion, this is a fact Bun's own
	// tag vocabulary has no concept for, so it gets a `forge:"fill=ulid"`
	// struct tag in forge's own namespace (see structTag) —
	// pkg/crud.Repo.ensureMeta reads it back the same way it reads version.
	isFillULID bool
	// insertDefault is how the column's DEFAULT applies when the caller
	// leaves the field unset (codegen.ColumnInsertDefault): a pointer field
	// whose nil is the DEFAULT, or ,nullzero on a type whose zero is never
	// a stored value (a json document, an array with a non-empty DEFAULT).
	insertDefault codegen.InsertDefault
	// override is a `forge:go-type` declaration: the struct field's Go type
	// in place of goType's projection, and the import it needs.
	override *codegen.GoTypeOverride
}

// isBunSoftDeleteTimeType reports whether a deleted_at field's projected
// Go type is one Bun's ,soft_delete can stamp/scan: a time. Legacy TEXT
// deleted_at columns project to "string" and fail this — they keep the
// hand-rolled stamp/filter path.
func isBunSoftDeleteTimeType(f ormField) bool {
	return f.goType == "time.Time"
}

// structGoType is the Go type of the field on the generated entity
// struct: the base type, pointered when the column is nullable
// (arrays/bytes are reference types already and stay bare).
func (f ormField) structGoType() string {
	if f.override != nil {
		if f.nullable && !f.override.IsReference() {
			return "*" + f.override.GoType
		}
		return f.override.GoType
	}
	if f.nullable && !f.isArray && f.goType != "[]byte" {
		return "*" + f.goType
	}
	return f.goType
}

// writeORMGeneratedHeader writes the header every fully-generated ORM file
// opens with, ahead of its package clause: Go's generated-code banner (the
// exact `^// Code generated .* DO NOT EDIT\.$` line go/ast.IsGenerated,
// gopls and golangci-lint recognise), the forge-owned note, and the
// "// Source:" line `forge lint` requires of a _gen file. The forge:hash
// marker is not written here — the Tier-1 writer stamps it directly under
// the banner.
//
// One writer for every ORM output, because each emitter hand-writing its
// own header is how store_gen.go shipped with none: linters then treated it
// as hand-written, `forge lint`'s auto-fix pass rewrote it, and forge's own
// drift gate failed on the rewrite. The _repo_ext.go seam is user-owned
// from birth and must never carry it.
func writeORMGeneratedHeader(b *strings.Builder, source string) {
	b.WriteString("// Code generated by forge. DO NOT EDIT.\n")
	b.WriteString("// forge-owned: regenerated every run — do not edit (forge project disown to take ownership)\n")
	fmt.Fprintf(b, "// Source: %s\n", source)
}

func renderORMEntity(pkgName string, ent config.PlanEntity, hasExtra bool) []byte {
	msgName := ent.Name
	tableName := ent.TableName
	if tableName == "" {
		tableName = naming.Pluralize(naming.ToSnakeCase(ent.Name))
	}

	// Build fields list including auto-generated timestamp/soft-delete fields.
	fields := resolveORMFields(ent)

	// Identify special fields.
	var pkField *ormField
	for i := range fields {
		if fields[i].isPK {
			pkField = &fields[i]
		}
	}

	// pkGoType is the Go type of the primary key, used in the delegate
	// signatures (Get/Delete take an id of this type). The column name and
	// other PK details the repo derives from Bun's schema at runtime.
	pkGoType := "string"
	if pkField != nil {
		pkGoType = pkField.goType
	}

	// The time import is needed only for a time.Time / *time.Time struct
	// field (the projected type of a TIMESTAMPTZ column). The managed-
	// timestamp stamping (the time.Now() instant or its RFC3339Nano string
	// for legacy TEXT columns) now lives in forge/pkg/crud, so the
	// generated file no longer references time for stamping — a TEXT
	// timestamp column projects to string and needs no time import (an
	// otherwise-unused "time" import would not compile).
	// A TIMESTAMPTZ[] column projects to []time.Time, so the test is
	// "does any field's type MENTION time.Time", not "is any field
	// time.Time" — the latter left an array-of-instants column referencing
	// an unimported package.
	needsTime, needsJSON := false, false
	var overrideImports []string
	for _, f := range fields {
		if f.override != nil {
			if spec := f.override.ImportSpec(); spec != "" && !slices.Contains(overrideImports, spec) {
				overrideImports = append(overrideImports, spec)
			}
			continue
		}
		needsTime = needsTime || strings.Contains(f.goType, "time.Time")
		needsJSON = needsJSON || strings.Contains(f.goType, "json.RawMessage")
	}

	var b strings.Builder

	writeORMGeneratedHeader(&b, "the APPLIED schema (db/migrations) of table "+tableName+".")
	fmt.Fprintf(&b, "package %s\n\n", pkgName)

	// Imports
	writeORMImports(&b, needsTime, needsJSON, overrideImports)

	// The entity struct: a projection of the APPLIED schema, with Bun tags.
	writeEntityStruct(&b, msgName, tableName, fields, hasExtra)

	// Field name constants. Doc comments start with the declared name so
	// the emitted file satisfies `revive: exported` — see
	// TestE2EScaffoldIsReviveExportedClean.
	fmt.Fprintf(&b, "// %sTableName is the SQL table %s rows live in.\n", msgName, msgName)
	fmt.Fprintf(&b, "const %sTableName = %q\n\n", msgName, tableName)
	fmt.Fprintf(&b, "// %sField* name each declared column of %s, for callers that\n", msgName, msgName)
	fmt.Fprintf(&b, "// build order_by / filter input without hardcoding SQL identifiers.\n")
	b.WriteString("const (\n")
	for _, f := range fields {
		fmt.Fprintf(&b, "\t%sField%s = %q\n", msgName, f.goName, f.columnName)
	}
	b.WriteString(")\n\n")

	// Column list helper. EXPORTED: this is the per-entity column
	// allowlist — handler shims hand it to pkg/crud so user-supplied
	// order_by/filter columns are validated against declared columns
	// (identifier-shape validation alone let `order_by=password_hash`
	// through as a silent no-op).
	colsVar := msgName + "Columns"
	fmt.Fprintf(&b, "// %s is the ordered list of declared columns for %s.\n", colsVar, msgName)
	fmt.Fprintf(&b, "// It doubles as the column allowlist for user-supplied order_by/filter\n")
	fmt.Fprintf(&b, "// input (see forge/pkg/crud).\n")
	fmt.Fprintf(&b, "var %s = []string{\n", colsVar)
	for _, f := range fields {
		fmt.Fprintf(&b, "\t%q,\n", f.columnName)
	}
	b.WriteString("}\n\n")

	// Constraint name constants — the sibling of the column constants
	// above, for the other identifier a service has to spell.
	writeConstraintConstants(&b, msgName, ent.Constraints)

	// CRUD: a single generic crud.Repo[<Entity>] owns the lifecycle
	// (built on Bun, metadata derived from the schema/tags at first use),
	// and thin package-level delegates preserve the standalone-function API
	// the handler ops and any user code call. The repo's Spec carries ONLY
	// the forge conventions Bun can't read off the struct tags: managed
	// timestamps and the legacy-TEXT deleted_at fallback.
	//
	// Per-column write policy is NOT among them: `forge:immutable` becomes a
	// ,skipupdate tag on the column itself (see bunTag), where Bun enforces
	// it. A parallel list of column NAMES in the Spec was the same fact
	// spelled a second way, unchecked against the struct it described.
	if ent.ReadOnly {
		writeReadOnlyDelegates(&b, msgName, pkField, tableName)
	} else if keys := compositeKey(fields); keys != nil {
		writeCompositeKeyDelegates(&b, msgName, keys, ent.SoftDelete, ent.AppendOnly)
	} else {
		writeORMRepoAndDelegates(&b, msgName, pkGoType, ent.SoftDelete, ent.AppendOnly)
	}

	// gofmt the render so struct-tag/const alignment matches what the
	// project's gofmt/CI check (and the drift guard) expect. The writers
	// emit unaligned tabs; format.Source canonicalizes them. On the
	// off chance the render is not valid Go, fall back to the raw bytes so
	// the compile error surfaces at `go build` rather than here.
	src := []byte(b.String())
	if formatted, err := format.Source(src); err == nil {
		return formatted
	}
	return src
}

// writeORMImports emits the import block for a generated ORM file. The
// per-entity CRUD bodies now live in the generic forge/pkg/crud.Repo, so
// the generated file imports only what the thin delegates and the struct
// reference: context (delegate signatures), forge/pkg/crud (the Repo),
// forge/pkg/orm (the Context + QueryOption types), and bun (the embedded
// BaseModel). time is added when any column projects to time.Time. The old
// per-file fmt/strings/ulid/otel imports are gone — the library owns the
// tracing, ULID generation, and error wrapping.
func writeORMImports(b *strings.Builder, needsTime, needsJSON bool, extra []string) {
	// forge:go-type imports join the group they belong to: a standard
	// library path (no dot in its first element) beside context, any
	// other beside bun. format.Source sorts each group and drops a
	// duplicate of a path the file already imports.
	var std, third []string
	for _, spec := range extra {
		p := spec[strings.Index(spec, `"`)+1:]
		if first, _, _ := strings.Cut(p, "/"); strings.Contains(first, ".") {
			third = append(third, spec)
		} else {
			std = append(std, spec)
		}
	}
	b.WriteString("import (\n")
	b.WriteString("\t\"context\"\n")
	for _, spec := range std {
		b.WriteString("\t" + spec + "\n")
	}
	if needsJSON {
		b.WriteString("\t\"encoding/json\"\n")
	}
	if needsTime {
		b.WriteString("\t\"time\"\n")
	}
	b.WriteString("\n")
	b.WriteString("\t\"github.com/reliant-labs/forge/pkg/crud\"\n")
	b.WriteString("\t\"github.com/reliant-labs/forge/pkg/orm\"\n")
	b.WriteString("\t\"github.com/uptrace/bun\"\n")
	for _, spec := range third {
		b.WriteString("\t" + spec + "\n")
	}
	b.WriteString(")\n\n")
}

// writeORMRepoAndDelegates emits the per-entity repo var plus the thin
// standalone delegates that preserve the pre-generic function API. The
// generic crud.Repo[<Entity>] owns every lifecycle concern; these delegates
// are pure forwarding so the handler ops shim (and any hand-written caller)
// keeps compiling unchanged. Spec carries only the conventions Bun's schema
// can't infer: managed timestamps, legacy-TEXT deleted_at, and
// the `// forge:secret` columns preserved on a maskless full-replace Update.
func writeORMRepoAndDelegates(b *strings.Builder, msgName, pkGoType string, softDelete, appendOnly bool) {
	repoVar := lowerFirst(msgName) + "Repo"

	// The per-entity repo, constructed once at package init. Everything it
	// needs — managed timestamps, both soft-delete forms, per-column write
	// policy — is derived from the model's own Bun tags on the first call,
	// so there is nothing to configure here.
	fmt.Fprintf(b, "// %s is the generic CRUD repository for %s. It owns the\n", repoVar, msgName)
	b.WriteString("// lifecycle (soft delete, masked updates, managed\n")
	b.WriteString("// timestamps, pagination via QueryOption) over Bun; the standalone\n")
	b.WriteString("// functions below are thin delegates onto it.\n")
	fmt.Fprintf(b, "var %s = crud.NewRepo[%s]()\n\n", repoVar, msgName)

	// Create
	fmt.Fprintf(b, "// Create%s inserts a new %s row (plain INSERT, never an upsert).\n", msgName, msgName)
	fmt.Fprintf(b, "func Create%s(ctx context.Context, db orm.Context, msg *%s) error {\n", msgName, msgName)
	fmt.Fprintf(b, "\treturn %s.Create(ctx, db, msg)\n", repoVar)
	b.WriteString("}\n\n")

	// Get. A missing row is crud.Repo's not-found error, the one every
	// by-key verb gives (Get, Update, UpdateMasked, Delete): svcerr.NotFound,
	// still errors.Is-true for orm.ErrNoRows. The Repo classifies it, so
	// this delegate — like the others — only forwards.
	entityLower := strings.ToLower(msgName)
	fmt.Fprintf(b, "// Get%sByID retrieves a %s by primary key. A missing row is\n", msgName, msgName)
	fmt.Fprintf(b, "// svcerr.NotFound(%q) — return it as-is from a service or hand it to\n", entityLower)
	b.WriteString("// svcerr.Wrap in a handler — and is also errors.Is(err, orm.ErrNoRows);\n")
	b.WriteString("// Update, UpdateMasked and Delete answer a missing row the same way.\n")
	fmt.Fprintf(b, "func Get%sByID(ctx context.Context, db orm.Context, id %s) (*%s, error) {\n", msgName, pkGoType, msgName)
	fmt.Fprintf(b, "\treturn %s.Get(ctx, db, id)\n", repoVar)
	b.WriteString("}\n\n")

	// List
	fmt.Fprintf(b, "// List%s retrieves %s rows with optional QueryOption filtering.\n", msgName, msgName)
	fmt.Fprintf(b, "func List%s(ctx context.Context, db orm.Context, opts ...orm.QueryOption) ([]*%s, error) {\n", msgName, msgName)
	fmt.Fprintf(b, "\treturn %s.List(ctx, db, opts...)\n", repoVar)
	b.WriteString("}\n\n")

	// Count
	fmt.Fprintf(b, "// Count%s returns the number of matching %s rows.\n", msgName, msgName)
	fmt.Fprintf(b, "func Count%s(ctx context.Context, db orm.Context, opts ...orm.QueryOption) (int64, error) {\n", msgName)
	fmt.Fprintf(b, "\treturn %s.Count(ctx, db, opts...)\n", repoVar)
	b.WriteString("}\n\n")

	// ListAll (only for soft-delete entities, matching the legacy surface)
	if softDelete {
		fmt.Fprintf(b, "// ListAll%s retrieves all %s rows including soft-deleted ones.\n", msgName, msgName)
		fmt.Fprintf(b, "func ListAll%s(ctx context.Context, db orm.Context, opts ...orm.QueryOption) ([]*%s, error) {\n", msgName, msgName)
		fmt.Fprintf(b, "\treturn %s.ListAll(ctx, db, opts...)\n", repoVar)
		b.WriteString("}\n\n")
	}

	// forge:append-only — the SECOND of the two routes to a mutating
	// write, and the one the first cut of this fix missed. Narrowing
	// PaymentStore to omit Update/Delete was defeated by the delegates
	// right here, which are exported: `db.DeletePayment(ctx, tx, id)`
	// still type-checked, so a money ledger's immutability came back as
	// SQLSTATE P0001 at runtime rather than the compile error the db
	// skill promises. An invariant enforced on one of two available
	// routes is a convention, not an invariant.
	//
	// Create, the reads and the repo all stay: append-only means
	// immutable, not invisible.
	if appendOnly {
		return
	}

	// Update. The variadic crud.UpdateOption is the per-CALL write policy:
	// the generated CRUD Update op passes crud.Preserve(<the entity's
	// forge:read-only/forge:computed columns>) so a client cannot write
	// them, while app code calling this with no options keeps writing every
	// updatable column. Variadic so every existing call site still compiles.
	fmt.Fprintf(b, "// Update%s writes the updatable columns of an existing %s by PK\n", msgName, msgName)
	if softDelete {
		b.WriteString("// (created_at/PK/deleted_at excluded; updated_at re-stamped) and reads\n")
	} else {
		b.WriteString("// (created_at/PK excluded; updated_at re-stamped) and reads\n")
	}
	b.WriteString("// back every column it did not write, so msg holds the stored row after.\n")
	b.WriteString("// crud.Preserve(cols...) keeps more columns out of this one write.\n")
	fmt.Fprintf(b, "func Update%s(ctx context.Context, db orm.Context, msg *%s, opts ...crud.UpdateOption) error {\n", msgName, msgName)
	fmt.Fprintf(b, "\treturn %s.Update(ctx, db, msg, opts...)\n", repoVar)
	b.WriteString("}\n\n")

	// UpdateMasked
	fmt.Fprintf(b, "// Update%sMasked writes ONLY the named columns (AIP-134 update_mask;\n", msgName)
	b.WriteString("// unknown/immutable paths return *orm.UnknownFieldError). It writes a\n")
	b.WriteString("// forge:read-only column named here: this is how the code that owns one\n")
	b.WriteString("// sets it, since no client Update can. opts are Update's: crud.NoReadBack()\n")
	b.WriteString("// skips the read-back.\n")
	fmt.Fprintf(b, "func Update%sMasked(ctx context.Context, db orm.Context, msg *%s, fields []string, opts ...crud.UpdateOption) error {\n", msgName, msgName)
	fmt.Fprintf(b, "\treturn %s.UpdateMasked(ctx, db, msg, fields, opts...)\n", repoVar)
	b.WriteString("}\n\n")

	// Delete
	fmt.Fprintf(b, "// Delete%s removes a %s by PK (soft delete stamps deleted_at when the\n", msgName, msgName)
	b.WriteString("// entity opts in; otherwise a hard DELETE).\n")
	fmt.Fprintf(b, "func Delete%s(ctx context.Context, db orm.Context, id %s) error {\n", msgName, pkGoType)
	fmt.Fprintf(b, "\treturn %s.Delete(ctx, db, id)\n", repoVar)
	b.WriteString("}\n")
}

// keyParam is one column of a composite primary key, as a delegate
// parameter.
type keyParam struct {
	param   string // the Go parameter name, e.g. "chatId"
	goType  string
	goField string // the struct field, e.g. "ChatId"
	column  string
}

// compositeKey returns the primary-key columns of a composite-key entity,
// in declaration order, and nil for a single-column (or absent) key.
func compositeKey(fields []ormField) []keyParam {
	var keys []keyParam
	for _, f := range fields {
		if f.isPK {
			keys = append(keys, keyParam{param: keyParamName(f.goName), goType: f.goType, goField: f.goName, column: f.columnName})
		}
	}
	if len(keys) < 2 {
		return nil
	}
	return keys
}

// keyParamName is a key column's delegate parameter: the field name with
// its first letter lowered, kept clear of Go keywords and of the names the
// delegates already bind (ctx, db, msg, row, err).
func keyParamName(goField string) string {
	name := lowerFirst(goField)
	switch name {
	case "ctx", "db", "msg", "row", "err", "opts", "fields":
		return name + "Key"
	}
	if token.Lookup(name).IsKeyword() {
		return name + "Key"
	}
	return name
}

// keyParamList spells the key as a parameter list ("chatId string, kind
// string"), the argument list ("chatId, kind") and the key literal
// ("&Counter{ChatId: chatId, Kind: kind}").
func keyParamList(msgName string, keys []keyParam) (params, args, literal string) {
	var ps, as, ls []string
	for _, k := range keys {
		ps = append(ps, k.param+" "+k.goType)
		as = append(as, k.param)
		ls = append(ls, k.goField+": "+k.param)
	}
	return strings.Join(ps, ", "), strings.Join(as, ", "), "&" + msgName + "{" + strings.Join(ls, ", ") + "}"
}

// keyColumns names the key for a doc comment: "(chat_id, kind)".
func keyColumns(keys []keyParam) string {
	cols := make([]string, len(keys))
	for i, k := range keys {
		cols[i] = k.column
	}
	return "(" + strings.Join(cols, ", ") + ")"
}

// writeCompositeKeyDelegates is writeORMRepoAndDelegates for an entity whose
// primary key spans several columns (reliant P5b, F12.3: counters, join
// tables). Every by-key verb takes the WHOLE key — Get<E>ByPK and
// Delete<E>ByPK, one parameter per key column — and Upsert<E> resolves a
// conflict on all of them. Create, List, Count and Update/UpdateMasked
// (which address the row by msg's key fields) keep the single-key shape.
// There is no ULID fill and no cursor pagination: neither has a single
// column to key on.
func writeCompositeKeyDelegates(b *strings.Builder, msgName string, keys []keyParam, softDelete, appendOnly bool) {
	repoVar := lowerFirst(msgName) + "Repo"
	params, _, literal := keyParamList(msgName, keys)
	keyDoc := keyColumns(keys)
	entityLower := strings.ToLower(msgName)

	fmt.Fprintf(b, "// %s is the generic CRUD repository for %s, whose primary key\n", repoVar, msgName)
	fmt.Fprintf(b, "// is the composite %s. The functions below are thin delegates onto it.\n", keyDoc)
	fmt.Fprintf(b, "var %s = crud.NewRepo[%s]()\n\n", repoVar, msgName)

	fmt.Fprintf(b, "// Create%s inserts a new %s row (plain INSERT, never an upsert).\n", msgName, msgName)
	fmt.Fprintf(b, "func Create%s(ctx context.Context, db orm.Context, msg *%s) error {\n", msgName, msgName)
	fmt.Fprintf(b, "\treturn %s.Create(ctx, db, msg)\n}\n\n", repoVar)

	fmt.Fprintf(b, "// Get%sByPK retrieves the %s whose key %s is the one given. A\n", msgName, msgName, keyDoc)
	fmt.Fprintf(b, "// missing row is svcerr.NotFound(%q), also errors.Is(err, orm.ErrNoRows),\n", entityLower)
	b.WriteString("// as from every by-key verb; every other error is the repository's.\n")
	fmt.Fprintf(b, "func Get%sByPK(ctx context.Context, db orm.Context, %s) (*%s, error) {\n", msgName, params, msgName)
	fmt.Fprintf(b, "\treturn %s.GetByPK(ctx, db, %s)\n}\n\n", repoVar, literal)

	fmt.Fprintf(b, "// List%s retrieves %s rows with optional QueryOption filtering.\n", msgName, msgName)
	fmt.Fprintf(b, "func List%s(ctx context.Context, db orm.Context, opts ...orm.QueryOption) ([]*%s, error) {\n", msgName, msgName)
	fmt.Fprintf(b, "\treturn %s.List(ctx, db, opts...)\n}\n\n", repoVar)

	fmt.Fprintf(b, "// Count%s returns the number of matching %s rows.\n", msgName, msgName)
	fmt.Fprintf(b, "func Count%s(ctx context.Context, db orm.Context, opts ...orm.QueryOption) (int64, error) {\n", msgName)
	fmt.Fprintf(b, "\treturn %s.Count(ctx, db, opts...)\n}\n\n", repoVar)

	if softDelete {
		fmt.Fprintf(b, "// ListAll%s retrieves all %s rows including soft-deleted ones.\n", msgName, msgName)
		fmt.Fprintf(b, "func ListAll%s(ctx context.Context, db orm.Context, opts ...orm.QueryOption) ([]*%s, error) {\n", msgName, msgName)
		fmt.Fprintf(b, "\treturn %s.ListAll(ctx, db, opts...)\n}\n\n", repoVar)
	}

	fmt.Fprintf(b, "// Upsert%s writes msg by its key %s: INSERT, or on a conflict\n", msgName, keyDoc)
	b.WriteString("// with an existing row, UPDATE its updatable columns.\n")
	fmt.Fprintf(b, "func Upsert%s(ctx context.Context, db orm.Context, msg *%s) error {\n", msgName, msgName)
	fmt.Fprintf(b, "\treturn %s.Upsert(ctx, db, msg)\n}\n", repoVar)

	if appendOnly {
		return
	}
	b.WriteString("\n")
	fmt.Fprintf(b, "// Update%s writes the updatable columns of the %s msg's key %s\n", msgName, msgName, keyDoc)
	b.WriteString("// addresses, and reads back every column it did not write.\n")
	b.WriteString("// crud.Preserve(cols...) keeps more columns out of this one write.\n")
	fmt.Fprintf(b, "func Update%s(ctx context.Context, db orm.Context, msg *%s, opts ...crud.UpdateOption) error {\n", msgName, msgName)
	fmt.Fprintf(b, "\treturn %s.Update(ctx, db, msg, opts...)\n}\n\n", repoVar)

	fmt.Fprintf(b, "// Update%sMasked writes ONLY the named columns of the row msg's key\n", msgName)
	b.WriteString("// addresses (unknown/immutable paths return *orm.UnknownFieldError);\n")
	b.WriteString("// opts are Update's (crud.NoReadBack() skips the read-back).\n")
	fmt.Fprintf(b, "func Update%sMasked(ctx context.Context, db orm.Context, msg *%s, fields []string, opts ...crud.UpdateOption) error {\n", msgName, msgName)
	fmt.Fprintf(b, "\treturn %s.UpdateMasked(ctx, db, msg, fields, opts...)\n}\n\n", repoVar)

	fmt.Fprintf(b, "// Delete%sByPK removes the %s whose key %s is the one given (soft\n", msgName, msgName, keyDoc)
	b.WriteString("// delete stamps deleted_at when the entity opts in; otherwise a hard DELETE).\n")
	fmt.Fprintf(b, "func Delete%sByPK(ctx context.Context, db orm.Context, %s) error {\n", msgName, params)
	fmt.Fprintf(b, "\treturn %s.DeleteByPK(ctx, db, %s)\n}\n", repoVar, literal)
}

// writeReadOnlyDelegates emits the store of a VIEW opted in with
// `forge:entity` (reliant P5b, F12.7): List and Count, and Get<E>ByID when
// the view has a key column. Nothing writes — a view is a derived read, and
// the absence of Create/Update/Delete makes trying one a compile error.
func writeReadOnlyDelegates(b *strings.Builder, msgName string, key *ormField, view string) {
	repoVar := lowerFirst(msgName) + "Repo"
	fmt.Fprintf(b, "// %s reads the view %s (forge:entity: read-only — no\n", repoVar, view)
	b.WriteString("// Create, Update or Delete is generated for a view).\n")
	fmt.Fprintf(b, "var %s = crud.NewRepo[%s]()\n\n", repoVar, msgName)
	if key != nil {
		entityLower := strings.ToLower(msgName)
		fmt.Fprintf(b, "// Get%sByID retrieves the %s row whose %s is id. A missing row is\n", msgName, msgName, key.columnName)
		fmt.Fprintf(b, "// svcerr.NotFound(%q), also errors.Is(err, orm.ErrNoRows); every other\n", entityLower)
		b.WriteString("// error is the repository's.\n")
		fmt.Fprintf(b, "func Get%sByID(ctx context.Context, db orm.Context, id %s) (*%s, error) {\n", msgName, key.goType, msgName)
		fmt.Fprintf(b, "\treturn %s.Get(ctx, db, id)\n}\n\n", repoVar)
	}
	fmt.Fprintf(b, "// List%s retrieves %s rows with optional QueryOption filtering.\n", msgName, msgName)
	fmt.Fprintf(b, "func List%s(ctx context.Context, db orm.Context, opts ...orm.QueryOption) ([]*%s, error) {\n", msgName, msgName)
	fmt.Fprintf(b, "\treturn %s.List(ctx, db, opts...)\n}\n\n", repoVar)
	fmt.Fprintf(b, "// Count%s returns the number of matching %s rows.\n", msgName, msgName)
	fmt.Fprintf(b, "func Count%s(ctx context.Context, db orm.Context, opts ...orm.QueryOption) (int64, error) {\n", msgName)
	fmt.Fprintf(b, "\treturn %s.Count(ctx, db, opts...)\n}\n", repoVar)
}

// lowerFirst returns s with its first rune lower-cased (msgName ->
// repo-var prefix).
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// bunTag returns the `bun:"..."` struct tag for a field, projecting the
// introspected schema into Bun's native tag vocabulary so Bun owns the
// behaviour forge used to hand-roll. The column name is always present.
//
//   - PRIMARY KEY                → ,pk; a key the DATABASE allocates also
//     gets ,autoincrement, so a zero key is left to the database and Bun
//     reads the assigned value back via RETURNING. What allocates it is the
//     applied schema, never the key's type (see serverAllocatedKey).
//   - deleted_at soft-delete     → ,soft_delete,nullzero — Bun's BUILT-IN
//     soft delete. NewSelect auto-excludes deleted rows, a plain
//     NewDelete stamps deleted_at, WhereAllWithDeleted/ForceDelete opt
//     out. nullzero maps a zero/NULL deleted_at to "live". (Only emitted
//     for a proper time deleted_at; a legacy TEXT column is NOT marked
//     softDeleteNative and falls through to the plain notnull/default
//     projection below, keeping the hand-rolled path.)
//   - array column               → ,array (native postgres array bind/scan).
//   - NOT NULL column            → ,notnull (skipped for the PK, which
//     ,pk already implies, and for soft_delete, which is nullable).
//   - column DEFAULT             → ,default:<expr> so Bun knows the DB
//     supplies the value (skipped for the PK/autoincrement and
//     soft_delete, whose lifecycle Bun manages).
//   - GENERATED ALWAYS STORED    → ,nullzero,skipupdate — the DB computes
//     it, so it must be absent from both write clauses but PRESENT in the
//     SELECT projection. Notably NOT ,scanonly; see the block below.
func bunTag(f ormField) string {
	parts := []string{f.columnName}

	switch {
	case f.softDeleteNative:
		// Bun's built-in soft delete owns the column entirely.
		parts = append(parts, "soft_delete", "nullzero")
	case f.isPK:
		parts = append(parts, "pk")
		if serverAllocatedKey(f) {
			parts = append(parts, "autoincrement")
		}
	default:
		if f.isArray {
			parts = append(parts, "array")
		}
		if f.notNull {
			parts = append(parts, "notnull")
		}
		if f.hasDefault && (!f.notNull || f.isTimestamp) {
			// Not on a NOT NULL non-timestamp column (e.g. a bool `default:true`):
			// `default:` makes Bun treat the Go zero as "unset" and substitute
			// the default, flipping an intentional false to true. Such a column
			// says "unset" with its TYPE instead — a pointer whose nil Bun sends
			// as DEFAULT, or ,nullzero where the zero is never a stored value
			// (codegen.ColumnInsertDefault). ,default: stays where a Go value can
			// legitimately be absent: nullable columns, or server-stamped
			// timestamps.
			parts = append(parts, "default:"+f.columnDef)
		}
	}

	// A column whose Go zero is never a value it stores — a json document
	// (`""` is not JSON) or an array/bytea with a non-empty DEFAULT — reads
	// that zero as "unset": ,nullzero makes Bun omit the column from the
	// INSERT so its DEFAULT fires, and pkg/crud keeps the stored value on a
	// full-replace Update. (A scalar whose DEFAULT is not its zero is a
	// pointer instead — see resolveORMFields — because its zero IS a value.)
	if f.insertDefault == codegen.InsertDefaultNullZero {
		parts = append(parts, "nullzero")
	}

	// GENERATED ALWAYS AS (...) STORED: the DB computes it, and postgres
	// rejects naming it in an INSERT column list or an UPDATE SET clause.
	// ,nullzero handles the INSERT half: Bun emits the literal DEFAULT
	// keyword for a zero-valued nullzero field (query_insert.go's
	// marshalsToDefault → appendStructValues), which postgres ACCEPTS for a
	// generated column. The UPDATE half is the shared ,skipupdate below,
	// which isGenerated now feeds alongside immutable/secret/version.
	// Reading the computed value back is NOT the tag's job: pkg/crud.Repo
	// excludes the column from the INSERT (so Bun's own DEFAULT-field
	// RETURNING never sees it) and instead RETURNs every generated column
	// explicitly on Create, Upsert, Update and UpdateMasked — keyed off the
	// forge:"generated" tag structTag adds.
	//
	// NOT ,scanonly, which is what this used to emit. ,scanonly does not
	// mean "read but don't write": Bun's schema/table.go early-returns
	// BEFORE `t.Fields = append(...)` for a scanonly field, and
	// Table.Fields is exactly the list query_select.go projects. A scanonly
	// column is therefore never SELECTed at all and reads back as the Go
	// zero forever, with no error anywhere — the DB holds the right value
	// and the app sees 0.
	//
	// One residual hazard, closed on the write side rather than here:
	// ,nullzero keys off the Go zero, so re-INSERTing an entity that was
	// READ (non-zero computed value) would send that value literally and
	// postgres would reject it. pkg/crud.Repo excludes generated columns
	// from the INSERT/UPSERT column set outright, which covers the
	// non-zero case the tag cannot express. Applied after the switch so it
	// stacks on whatever else the column needs (array/notnull for scanning).
	if f.isGenerated {
		parts = append(parts, "nullzero")
	}

	// ,skipupdate omits the column from a full-replace UPDATE's SET clause
	// while leaving it writable on INSERT and on an explicit masked Update.
	// Four independent declarations project onto it. Three describe a column
	// the client could not have sent a value for; the fourth is a column
	// postgres refuses to be sent at all:
	//
	//   - `forge:immutable` in the column's COMMENT — schema truth, a fact
	//     about the column applied by the migration.
	//   - `// forge:secret` on the wire field — the read path never packs it,
	//     so a client Get/List reads it back as "" and a round-tripped entity
	//     would overwrite the stored credential with that "".
	//   - `forge:version` in the column's COMMENT — the entity's optimistic-
	//     concurrency column. pkg/crud.Repo's own SET-clause construction is
	//     what actually enforces this (it must ALSO exclude the column from
	//     UpdateMasked's allowlist, which ,skipupdate alone cannot express —
	//     see meta.versionColumn); the tag here is defense in depth against
	//     a hand-rolled Bun query bypassing the generic Repo entirely.
	//   - GENERATED ALWAYS AS (...) STORED — postgres itself rejects the
	//     column in a SET clause, so this one is not a policy choice but a
	//     hard constraint of the schema (see the isGenerated block above,
	//     which owns the INSERT half via ,nullzero).
	//
	// The secret case is the narrower one and predates the marker; it is
	// folded in here rather than requiring every secret column to ALSO carry
	// a COMMENT, which would make forgetting one a silent credential wipe.
	//
	// Sharing the single append is what keeps a column that is BOTH generated
	// and (say) immutable from emitting ,skipupdate twice.
	if f.isImmutable || f.isSecret || f.isVersion || f.isGenerated {
		parts = append(parts, "skipupdate")
	}

	return fmt.Sprintf("`bun:%q`", strings.Join(parts, ","))
}

// structTag returns the full Go struct tag literal for a field — the
// `bun:"..."` tag bunTag builds, plus a `forge:"version"`,
// `forge:"fill=ulid"` or `forge:"generated"` tag in its OWN namespace for
// the (at most one, per field) forge-specific column marker.
//
// A second tag namespace, not another bun tag option, because Bun has no
// native concept for either declaration and inventing a private option name
// (e.g. an unrecognized bun:"...,version" flag) would hit Bun's own
// unknown-tag-option warning (schema.isKnownFieldOption) on every generated
// struct carrying one. The `forge:"..."` namespace sits where Bun's tag
// parser never inspects it, so it is silent to Bun and readable by anyone:
// pkg/crud.Repo.ensureMeta reads it directly off
// schema.Field.StructField.Tag (Bun always keeps the raw reflect.StructField
// alongside its own parsed Tag), and go vet / reflect.StructTag.Lookup see
// it exactly like any other well-formed struct tag.
func structTag(f ormField) string {
	bunPart := strings.Trim(bunTag(f), "`")
	switch {
	case f.isVersion:
		return fmt.Sprintf("`%s forge:%q`", bunPart, "version")
	case f.isFillULID:
		return fmt.Sprintf("`%s forge:%q`", bunPart, "fill=ulid")
	case f.softDeleteLegacy:
		return fmt.Sprintf("`%s forge:%q`", bunPart, "soft-delete")
	case f.isGenerated:
		// GENERATED ALWAYS AS (...) STORED. The bun tag's ,nullzero handles
		// the common INSERT (a zero value marshals to DEFAULT, which
		// postgres accepts), but ,nullzero keys off the GO zero and so
		// cannot express the case that matters most: re-inserting an entity
		// that was READ back carries a NON-zero computed value, which bun
		// would send literally and postgres would reject. Bun has no tag
		// option for "never name this column in a write", so the fact is
		// declared here, in forge's own namespace, and pkg/crud.Repo reads
		// it to drop the column from the INSERT/UPSERT column set outright.
		//
		// Mutually exclusive with the two above in practice: the database
		// computes a generated column, so it can be neither a ULID fill
		// target nor the repo-incremented version column.
		return fmt.Sprintf("`%s forge:%q`", bunPart, "generated")
	default:
		return bunTag(f)
	}
}

// writeEntityStruct emits the entity row type with Bun struct tags. Field
// types come from the APPLIED schema's columns: time.Time for timestamps
// (never timestamppb — the wire seam converts), pointers for nullable
// columns, native slices for array columns. Bun scans rows straight into
// this struct via the tags.
//
// hasExtra embeds <Entity>Extra — the user-owned computed-field carrier
// declared in <entity>_repo_ext.go — when that type actually exists (see
// hasRepoExtExtraType). An embedded struct whose fields all carry
// `bun:"-"` contributes nothing to Bun's schema introspection (verified
// against uptrace/bun's schema.Table.processFields: a `bun:"-"` field is
// skipped before it ever reaches the embedding walk), so the seam is
// invisible to every generated CRUD path unless and until the user adds
// a field to it.
func writeEntityStruct(b *strings.Builder, msgName, tableName string, fields []ormField, hasExtra bool) {
	fmt.Fprintf(b, "// %s is the %s row type — a projection of the APPLIED schema\n", msgName, tableName)
	b.WriteString("// (db/migrations). Evolve it by writing migrations; `forge generate`\n")
	b.WriteString("// regenerates this struct from the introspected schema. The bun tags\n")
	b.WriteString("// map fields to columns for the Bun query engine; the embedded\n")
	b.WriteString("// bun.BaseModel pins the table name and its alias (alias == table so\n")
	b.WriteString("// Bun's column qualification matches the FROM clause).\n")
	fmt.Fprintf(b, "type %s struct {\n", msgName)
	fmt.Fprintf(b, "\tbun.BaseModel `bun:%q`\n", fmt.Sprintf("table:%s,alias:%s", tableName, tableName))
	for _, f := range fields {
		fmt.Fprintf(b, "\t%s %s %s\n", f.goName, f.structGoType(), structTag(f))
	}
	if hasExtra {
		fmt.Fprintf(b, "\t%s // computed / in-memory-only fields — see %s_repo_ext.go\n",
			entityExtraTypeName(msgName), naming.ToSnakeCase(msgName))
	}
	b.WriteString("}\n\n")
}

// serverAllocatedKey reports whether the database allocates the key column
// f when an INSERT leaves it zero: an identity column, or one with any
// DEFAULT — a serial's nextval(...), gen_random_uuid(), a literal (the same
// rule as schemadef.Column.DatabaseSupplied). The key's type says nothing:
// `integer PRIMARY KEY` with no DEFAULT is a key the caller sets, and
// marking it ,autoincrement made Create drop the caller's id and fail NOT
// NULL (reliant's legacy_credential_copy). It holds per column, so each
// member of a composite key answers for itself.
func serverAllocatedKey(f ormField) bool {
	return f.isIdentity || f.hasDefault
}

// ── managed-timestamp stamping (type-aware) ────────────────────────────
//
// The managed created_at/updated_at stamping (TIMESTAMPTZ → time.Now(),
// legacy TEXT → RFC3339Nano string, plus the nullable pointer variants)
// now lives in forge/pkg/crud, driven by the per-entity repo Spec's
// Timestamps flag. The generator only projects the struct field types and
// records the Spec flag; it no longer emits stamping code, so the former
// isManagedTimestampColumn/stampableTimestamp helpers are gone.

// resolveORMFields resolves an entity's declared fields plus the
// auto-added id/timestamp/soft-delete columns into the ormField list used
// by the code generator.
func resolveORMFields(ent config.PlanEntity) []ormField {
	type entry struct {
		config.PlanEntityField
		autoGenerated bool
	}

	// Check if any field already serves as the primary key.
	hasPK := false
	for _, f := range ent.Fields {
		if f.PrimaryKey || f.Name == "id" {
			hasPK = true
			break
		}
	}

	entries := make([]entry, 0, len(ent.Fields)+4)

	// Auto-add an id primary key field if none exists — never for a view,
	// whose columns are exactly what it selects.
	if !hasPK && !ent.ReadOnly {
		entries = append(entries, entry{PlanEntityField: config.PlanEntityField{
			Name: "id", Type: "string", PrimaryKey: true, NotNull: true,
		}})
	}

	declared := make(map[string]bool, len(ent.Fields))
	for _, f := range ent.Fields {
		declared[f.Name] = true
		entries = append(entries, entry{PlanEntityField: f})
	}

	// Auto-add timestamp/soft-delete columns ONLY when the entity didn't
	// declare them — proto entities routinely declare created_at /
	// updated_at / deleted_at explicitly alongside timestamps:true /
	// soft_delete:true, and a duplicate field is a compile error in the
	// generated code (duplicate consts) and a duplicate column in SQL.
	if ent.Timestamps {
		if !declared["created_at"] {
			entries = append(entries,
				entry{PlanEntityField: config.PlanEntityField{Name: "created_at", Type: "time"}, autoGenerated: true})
		}
		if !declared["updated_at"] {
			entries = append(entries,
				entry{PlanEntityField: config.PlanEntityField{Name: "updated_at", Type: "time"}, autoGenerated: true})
		}
	}
	if ent.SoftDelete && !declared["deleted_at"] {
		entries = append(entries,
			entry{PlanEntityField: config.PlanEntityField{Name: "deleted_at", Type: "time"}, autoGenerated: true},
		)
	}

	fields := make([]ormField, 0, len(entries))
	for _, e := range entries {
		// GeneratePlanORM rejects an unprojectable type before any render
		// runs, so the fallback here is unreachable through the generator.
		goType, _ := planFieldGoType(e.Type)
		f := ormField{
			planField:   e.PlanEntityField,
			goName:      naming.ColumnGoName(e.Name),
			columnName:  e.Name,
			goType:      goType,
			isTimestamp: isTimePlanType(e.Type),
			isArray:     strings.HasPrefix(e.Type, "[]"),
			isPK:        e.PrimaryKey,
			notNull:     e.NotNull,
			unique:      e.Unique,
			references:  e.References,
			hasDefault:  strings.TrimSpace(e.Default) != "",
			columnDef:   strings.TrimSpace(e.Default),
			isIdentity:  e.Identity,
			isGenerated: e.Generated,
			isSecret:    e.Secret,
			isImmutable: e.Immutable,
			isVersion:   e.Version,
			isFillULID:  e.FillStrategy == schemadef.FillStrategyULID,
		}
		base, isArray := strings.CutPrefix(e.Type, "[]")
		f.insertDefault = codegen.ColumnInsertDefault(codegen.InsertDefaultColumn{
			Canonical: base, IsArray: isArray, NotNull: e.NotNull, IsPK: e.PrimaryKey,
			Generated: e.Generated, Version: e.Version, Default: e.Default,
			ZeroRejected: e.ZeroRejected,
		})
		if e.GoType != "" {
			// validatePlanFieldTypes refused an unparseable spec before
			// any render ran.
			if o, err := codegen.ParseGoTypeOverride(e.GoType); err == nil {
				f.override = &o
			}
		}
		// Nullable column → pointer struct field. Arrays and bytes are
		// reference types already (NULL scans to nil). A NOT NULL column
		// whose DEFAULT is not its Go zero is a pointer too: nil is "unset",
		// the INSERT omits it and the DEFAULT fires.
		f.nullable = ((!f.isPK && !f.notNull) || f.insertDefault == codegen.InsertDefaultPointer) && isNullablePlanType(e.Type)
		fields = append(fields, f)
	}

	// Mark the soft-delete column when soft delete is active. A deleted_at
	// that projects to a proper time type is Bun's built-in ,soft_delete; a
	// legacy TEXT deleted_at (Go type string) is tagged for pkg/crud's
	// hand-rolled stamp/filter path. Without soft delete — no deleted_at,
	// or one declared forge:no-soft-delete — neither mark is set and the
	// column is plain.
	if ent.SoftDelete {
		for i := range fields {
			if fields[i].columnName != "deleted_at" {
				continue
			}
			if isBunSoftDeleteTimeType(fields[i]) {
				fields[i].softDeleteNative = true
			} else {
				fields[i].softDeleteLegacy = true
			}
		}
	}
	return fields
}

// entityUsesNativeSoftDelete reports whether the entity's soft delete is
// handled by Bun's built-in ,soft_delete (proper time deleted_at) rather
// than the hand-rolled TEXT fallback. Drives the read/delete codegen: when
// true, Bun auto-excludes deleted rows from NewSelect, a plain NewDelete
// soft-deletes, and WhereAllWithDeleted includes them — the generator emits
// no deleted_at IS NULL filters and no CURRENT_TIMESTAMP stamp.
func entityUsesNativeSoftDelete(fields []ormField) bool {
	for _, f := range fields {
		if f.softDeleteNative {
			return true
		}
	}
	return false
}

// isTimePlanType reports whether the plan type is a timestamp column.
func isTimePlanType(t string) bool { return t == "time" }

// planFieldGoType maps a PlanEntityField.Type to the base Go type used on
// the entity struct. ok=false means the type is not in the vocabulary and
// the "string" answer is a FALLBACK rather than a mapping.
//
// The vocabulary is exactly the canonical schema vocabulary
// (pkg/schemadef.MapDeclaredType: string, int64, float64, bool, time,
// json, bytes) with a "[]" prefix for an array column, and the projection
// is codegen's — the SAME one the CRUD conversion generator pairs a wire
// field against. Two copies of this table is how a BYTEA[] column came to
// be [][]byte on one side and []string on the other, which is not a bug you
// can see in either half alone.
//
// The comment above is a claim about the PRODUCER, and it holds:
// PlanEntityField.Type is written by codegen.planTypeForColumn, which
// passes a column's canonical type through verbatim. That makes an
// out-of-vocabulary type a forge bug rather than user input, so callers
// route through validatePlanFieldTypes and report it by name instead of
// declaring the field as text.
func planFieldGoType(t string) (string, bool) {
	base, isArray := strings.CutPrefix(t, "[]")
	return codegen.CanonicalGoTypeOK(base, isArray)
}

// validatePlanFieldTypes reports the first plan field whose type has no Go
// projection, naming the entity, the field and the type.
//
// It derives its notion of "valid" from the projection itself rather than
// from a list of type names kept here: a canonical type added to schemadef
// without a projection in codegen is caught, and a list would have to be
// remembered into agreement with both.
func validatePlanFieldTypes(entities []config.PlanEntity) error {
	for _, ent := range entities {
		for _, f := range ent.Fields {
			if f.GoType != "" {
				if _, err := codegen.ParseGoTypeOverride(f.GoType); err != nil {
					return fmt.Errorf("entity %s field %s: %w", ent.Name, f.Name, err)
				}
			}
			if _, ok := planFieldGoType(f.Type); !ok {
				return fmt.Errorf(
					"entity %s field %s: no Go projection for plan field type %q — "+
						"the plan vocabulary is the canonical schema vocabulary "+
						"(string, int64, float64, bool, time, json, bytes, each "+
						"optionally []-prefixed), and generating this field would "+
						"declare it on the entity struct as text",
					ent.Name, f.Name, f.Type)
			}
		}
	}
	return nil
}

// isNullablePlanType returns true when a nullable column of this type
// becomes a pointer field on the entity struct. Arrays, bytes and json
// (json.RawMessage) are reference types already: NULL scans to nil without
// one.
func isNullablePlanType(t string) bool {
	return !strings.HasPrefix(t, "[]") && t != "bytes" && t != "json"
}
