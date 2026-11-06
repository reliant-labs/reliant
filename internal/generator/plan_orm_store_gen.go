package generator

// Store interfaces — internal/db/store_gen.go.
//
// ── The gap this closes ───────────────────────────────────────────────────
//
// A domain package's Deps are INTERFACES: `forge generate` resolves each
// Deps field BY TYPE in the generated compose.go, and a package that
// depends on a concrete type cannot be faked in a test or swapped at
// composition. That is the architecture forge promotes everywhere.
//
// The generated ORM, though, is a package of FREE FUNCTIONS —
// GetEstimateByID(ctx, db, id), ListEstimate(ctx, db, opts...). A free
// function is not a type, so a domain package could not name it as a Deps
// field. Following forge's own rule therefore forced every project to
// hand-write the missing interface AND an adapter whose methods are
// one-line passthroughs:
//
//	func (s *PipelineStore) GetInspection(ctx context.Context, id string) (*db.Inspection, error) {
//	    return db.GetInspectionByID(ctx, s.db, id)
//	}
//
// Measured across two forge-one-shot runs: 723 lines across four packages
// in one, 464 across two in the other — and ~16 minutes, almost none of it
// typing. The cost was DISCOVERY: reading the ORM surface, inventing a
// helper that did not exist, finding orm.Asc and bun.In by trial, then
// aligning adapter signatures with contract signatures so composition
// would resolve, then moving the files twice to stop fan-out units
// colliding on them.
//
// Every one of those lines is derivable from the schema forge already
// introspected, so forge generates them here.
//
// ── Shape: per-entity, plus an aggregate ─────────────────────────────────
//
// Two real cases, so two shapes:
//
//   - A package touching ONE entity declares `Deps{ Estimates db.EstimateStore }`
//     — a narrow interface, trivially faked in a unit test.
//   - A package spanning many (an analytics/reporting service — the measured
//     one touched six tables) declares `Deps{ DB db.Store }` and gets every
//     entity store embedded, rather than eight separate fields.
//
// Neither is a default forced on the other: the aggregate EMBEDS the
// per-entity interfaces, so the two views never drift and a package can
// migrate from one to the other without changing a call site.
//
// ── The escape hatch is the existing seam, not a new one ─────────────────
//
// These interfaces cover the generated CRUD surface ONLY. Anything the
// builder cannot express — joins, aggregates, CTEs, raw SQL — belongs in
// the per-entity <entity>_repo_ext.go seam forge already scaffolds
// (scaffold-once, user-owned, never regenerated). A hand-written query
// there is reached by declaring it on your own interface in the consuming
// package: the generated store deliberately does NOT try to enumerate
// user queries, because it is regenerated and would either clobber them or
// go stale against them.

import (
	"fmt"
	"go/format"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"
)

// storeGenFile holds the aggregate Store: a constant struct with no
// per-entity line in it. Each entity's store — its interface, its adapter and
// the aggregate's accessor for it — is its own <entity>_store_gen.go.
//
// ONE FILE PER ENTITY, NOT ONE FILE. The stores were one file, and the
// aggregate interface listed one accessor per entity. Two branches that each
// add an entity sorting between the same two neighbours then insert at one
// point of that file, which no line merge can order (TestE2EMergeProperty
// measured it), and every change rewrote its one forge:hash line. A Go
// interface's method set cannot span files, so the aggregate is a concrete
// struct and each entity file adds its accessor as a method. Depend on a
// per-entity store (an interface, mocked by forge) to fake persistence in a
// test; the aggregate is for code that runs against a database.
const storeGenFile = "store_gen.go"

// entityStoreFile is ent's store file.
func entityStoreFile(ent config.PlanEntity) string {
	return naming.ToSnakeCase(ent.Name) + "_store_gen.go"
}

// writeStoreInterfaces renders the aggregate (store_gen.go) and one store
// file per entity. Callers pass the same entity slice the ORM render used, so
// the interfaces cannot describe a different schema than the delegates they
// abstract.
func writeStoreInterfaces(root string, orm config.ORMConfig, entities []config.PlanEntity, cs *FileChecksums) error {
	if len(entities) == 0 {
		return nil
	}
	for _, ent := range entities {
		if err := writeORMFile(root, orm, entityStoreFile(ent), renderEntityStoreFile(orm.PackageName(), ent), cs); err != nil {
			return err
		}
	}
	return writeORMFile(root, orm, storeGenFile, renderStoreAggregate(orm.PackageName()), cs)
}

// renderStoreInterfaces is every store file's source concatenated, for tests
// that read the stores as one text.
func renderStoreInterfaces(pkgName string, entities []config.PlanEntity) []byte {
	out := renderStoreAggregate(pkgName)
	for _, ent := range entities {
		out = append(out, renderEntityStoreFile(pkgName, ent)...)
	}
	return out
}

// renderStoreAggregate is store_gen.go: the aggregate Store, which names no
// entity, so no schema change rewrites it.
func renderStoreAggregate(pkgName string) []byte {
	var b strings.Builder
	writeORMGeneratedHeader(&b, "forge — the aggregate of the <entity>_store_gen.go stores beside it.")
	fmt.Fprintf(&b, "package %s\n\n", pkgName)
	b.WriteString("import \"github.com/reliant-labs/forge/pkg/orm\"\n\n")
	b.WriteString("// The generated CRUD delegates have an INTERFACE shape per entity — the\n")
	b.WriteString("// <Entity>Store in each <entity>_store_gen.go — so a domain package can\n")
	b.WriteString("// depend on persistence the same way it depends on any other component: a\n")
	b.WriteString("// Deps field forge resolves by type, and forge generates its mock.\n")
	b.WriteString("//\n")
	b.WriteString("// These cover the GENERATED CRUD surface only. A hand-written or raw-SQL\n")
	b.WriteString("// query belongs in the entity's <entity>_repo_ext.go seam; declare it on\n")
	b.WriteString("// your own interface in the consuming package, because these files are\n")
	b.WriteString("// regenerated and would go stale against it.\n\n")
	b.WriteString("// Store is every entity's store behind one handle, for code that genuinely\n")
	b.WriteString("// spans several tables against a real database (a reporting job, a\n")
	b.WriteString("// migration of data). Each entity's accessor (<Entities>()) is declared in\n")
	b.WriteString("// that entity's _store_gen.go, so entities born on different branches never\n")
	b.WriteString("// edit one shared list. It is a struct, not an interface: to fake persistence\n")
	b.WriteString("// in a test, depend on the per-entity stores, which forge mocks.\n")
	b.WriteString("type Store struct{ db orm.Context }\n\n")
	b.WriteString("// NewStore returns the aggregate store bound to db. Pass the *orm.Client\n")
	b.WriteString("// for ordinary use, or a transaction handle to scope every call to it.\n")
	b.WriteString("func NewStore(db orm.Context) Store { return Store{db: db} }\n\n")
	b.WriteString("// WithTx returns every store bound to one transaction handle, so a use\n")
	b.WriteString("// case spanning tables commits or rolls back as a unit.\n")
	b.WriteString("func (s Store) WithTx(tx orm.Context) Store { return Store{db: tx} }\n")
	return formatStoreSource(b.String())
}

// renderEntityStoreFile is ent's <entity>_store_gen.go: its store interface,
// the adapter over its delegates, and the aggregate's accessor for it.
func renderEntityStoreFile(pkgName string, ent config.PlanEntity) []byte {
	var b strings.Builder
	writeORMGeneratedHeader(&b, "the APPLIED schema (db/migrations) — the CRUD delegates of "+naming.ToSnakeCase(ent.Name)+"_orm_gen.go beside it.")
	fmt.Fprintf(&b, "package %s\n\n", pkgName)
	// crud is imported for UpdateOption, which only a store with an Update
	// names; context only by a store with a method.
	if storesWrite([]config.PlanEntity{ent}) {
		b.WriteString("import (\n\t\"context\"\n\n\t\"github.com/reliant-labs/forge/pkg/crud\"\n\t\"github.com/reliant-labs/forge/pkg/orm\"\n)\n\n")
	} else {
		b.WriteString("import (\n\t\"context\"\n\n\t\"github.com/reliant-labs/forge/pkg/orm\"\n)\n\n")
	}
	writeEntityStore(&b, ent)
	plural := naming.Pluralize(ent.Name)
	fmt.Fprintf(&b, "// %s returns the %s store, bound to the aggregate's handle.\n", plural, ent.Name)
	fmt.Fprintf(&b, "func (s Store) %s() %sStore { return %s{db: s.db} }\n", plural, ent.Name, lowerFirst(ent.Name)+"Store")
	return formatStoreSource(b.String())
}

func formatStoreSource(src string) []byte {
	if formatted, err := format.Source([]byte(src)); err == nil {
		return formatted
	}
	return []byte(src)
}

// writeEntityStore emits one entity's store interface and the compile-time
// assertion that the generated delegates satisfy it.
// storesWrite reports whether any entity's store carries an Update — the
// only store methods that name a pkg/crud type.
func storesWrite(entities []config.PlanEntity) bool {
	for _, ent := range entities {
		if !ent.AppendOnly && !ent.ReadOnly {
			return true
		}
	}
	return false
}

func writeEntityStore(b *strings.Builder, ent config.PlanEntity) {
	if ent.ReadOnly {
		writeReadOnlyStore(b, ent)
		return
	}
	if keys := compositeKey(resolveORMFields(ent)); keys != nil {
		writeCompositeKeyStore(b, ent, keys)
		return
	}
	msgName := ent.Name
	pkGoType := storePKGoType(ent)
	iface := msgName + "Store"
	lower := strings.ToLower(msgName)

	// The interface BINDS the handle: no orm.Context in any signature.
	//
	// The first cut of this generator mirrored the delegates exactly, handle
	// and all. Dogfooding it against a real service proved that useless — a
	// consumer still had to carry an orm.Context beside the store and still
	// wrote `s.store.GetX(ctx, s.db, id)` wrappers, which is the passthrough
	// layer this file exists to delete. A dependency you cannot call without
	// a second dependency is not a dependency.
	//
	// The transaction seam survives intact via WithTx below; see its comment.
	fmt.Fprintf(b, "// %s is the generated CRUD surface for %s, shaped as an\n", iface, msgName)
	b.WriteString("// interface so a service can declare it as a Deps field and call it with\n")
	b.WriteString("// nothing but its own arguments.\n")
	if ent.AppendOnly {
		b.WriteString("//\n")
		fmt.Fprintf(b, "// %s is an APPEND-ONLY ledger (forge:append-only on the table), so\n", msgName)
		b.WriteString("// this store has no Update, UpdateMasked or Delete: rows are inserted and\n")
		b.WriteString("// read, never rewritten or erased. The database rejects both operations\n")
		b.WriteString("// too — omitting them here is what makes that refusal a compile error\n")
		b.WriteString("// instead of a runtime one.\n")
		b.WriteString("//\n")
		b.WriteString("// Correcting a bad row means appending its reversal, not editing it.\n")
	}
	fmt.Fprintf(b, "type %s interface {\n", iface)
	fmt.Fprintf(b, "\t// Create%s inserts a new %s row (plain INSERT, never an upsert).\n", msgName, msgName)
	fmt.Fprintf(b, "\tCreate%s(ctx context.Context, msg *%s) error\n\n", msgName, msgName)
	fmt.Fprintf(b, "\t// Get%sByID retrieves a %s by primary key. A missing row is\n", msgName, msgName)
	fmt.Fprintf(b, "\t// svcerr.NotFound(%q), also errors.Is(err, orm.ErrNoRows): the answer\n", lower)
	b.WriteString("\t// every by-key method gives a missing row.\n")
	fmt.Fprintf(b, "\tGet%sByID(ctx context.Context, id %s) (*%s, error)\n\n", msgName, pkGoType, msgName)
	fmt.Fprintf(b, "\t// List%s retrieves %s rows with optional QueryOption filtering.\n", msgName, msgName)
	fmt.Fprintf(b, "\tList%s(ctx context.Context, opts ...orm.QueryOption) ([]*%s, error)\n\n", msgName, msgName)
	fmt.Fprintf(b, "\t// Count%s counts %s rows matching the given options.\n", msgName, msgName)
	fmt.Fprintf(b, "\tCount%s(ctx context.Context, opts ...orm.QueryOption) (int64, error)\n\n", msgName)

	// forge:append-only — the mutators are OMITTED, not merely documented
	// away. The marker already removed the Update/Delete RPCs and installed
	// a trigger that rejects both at the database; emitting them here
	// anyway left the ledger's immutability defended only at runtime, so
	// `DeletePayment(ctx, id)` type-checked fine and could only fail as a
	// 500. A generator that knows an invariant and still emits the call
	// that breaks it has made the invariant the user's problem: the audited
	// build ended up hand-writing `panic("payments are append-only")` into
	// a test fake to defend it.
	//
	// Reads and Create stay: append-only means immutable, not invisible.
	if !ent.AppendOnly {
		fmt.Fprintf(b, "\t// Update%s writes every non-skipupdate column of msg.\n", msgName)
		fmt.Fprintf(b, "\tUpdate%s(ctx context.Context, msg *%s, opts ...crud.UpdateOption) error\n\n", msgName, msgName)
		fmt.Fprintf(b, "\t// Update%sMasked writes only the named fields.\n", msgName)
		fmt.Fprintf(b, "\tUpdate%sMasked(ctx context.Context, msg *%s, fields []string, opts ...crud.UpdateOption) error\n\n", msgName, msgName)
		fmt.Fprintf(b, "\t// Delete%s removes a %s by primary key.\n", msgName, msgName)
		fmt.Fprintf(b, "\tDelete%s(ctx context.Context, id %s) error\n\n", msgName, pkGoType)
	}
	b.WriteString("\t// WithTx returns the same store bound to an explicit transaction handle\n")
	b.WriteString("\t// (from orm RunTransaction). Code using RunTx does not need it: called\n")
	b.WriteString("\t// with the ctx RunTx hands its fn, the store joins that transaction.\n")
	fmt.Fprintf(b, "\tWithTx(tx orm.Context) %s\n", iface)
	b.WriteString("}\n\n")

	adapter := lowerFirst(msgName) + "Store"
	fmt.Fprintf(b, "// %s adapts the package-level %s delegates to %s,\n", adapter, msgName, iface)
	b.WriteString("// holding the handle those delegates take per call.\n")
	fmt.Fprintf(b, "type %s struct{ db orm.Context }\n\n", adapter)

	fmt.Fprintf(b, "func (s %s) Create%s(ctx context.Context, msg *%s) error {\n", adapter, msgName, msgName)
	fmt.Fprintf(b, "\treturn Create%s(ctx, s.db, msg)\n}\n\n", msgName)
	fmt.Fprintf(b, "func (s %s) Get%sByID(ctx context.Context, id %s) (*%s, error) {\n", adapter, msgName, pkGoType, msgName)
	fmt.Fprintf(b, "\treturn Get%sByID(ctx, s.db, id)\n}\n\n", msgName)
	fmt.Fprintf(b, "func (s %s) List%s(ctx context.Context, opts ...orm.QueryOption) ([]*%s, error) {\n", adapter, msgName, msgName)
	fmt.Fprintf(b, "\treturn List%s(ctx, s.db, opts...)\n}\n\n", msgName)
	fmt.Fprintf(b, "func (s %s) Count%s(ctx context.Context, opts ...orm.QueryOption) (int64, error) {\n", adapter, msgName)
	fmt.Fprintf(b, "\treturn Count%s(ctx, s.db, opts...)\n}\n\n", msgName)
	// The adapter loses the same three. An invariant the interface hides
	// while the concrete type still offers it is not an invariant — a
	// caller holding the adapter directly would compile straight through
	// the guard.
	if !ent.AppendOnly {
		fmt.Fprintf(b, "func (s %s) Update%s(ctx context.Context, msg *%s, opts ...crud.UpdateOption) error {\n", adapter, msgName, msgName)
		fmt.Fprintf(b, "\treturn Update%s(ctx, s.db, msg, opts...)\n}\n\n", msgName)
		fmt.Fprintf(b, "func (s %s) Update%sMasked(ctx context.Context, msg *%s, fields []string, opts ...crud.UpdateOption) error {\n", adapter, msgName, msgName)
		fmt.Fprintf(b, "\treturn Update%sMasked(ctx, s.db, msg, fields, opts...)\n}\n\n", msgName)
		fmt.Fprintf(b, "func (s %s) Delete%s(ctx context.Context, id %s) error {\n", adapter, msgName, pkGoType)
		fmt.Fprintf(b, "\treturn Delete%s(ctx, s.db, id)\n}\n\n", msgName)
	}

	// WithTx preserves the per-call-handle property the delegates were
	// designed around, for the explicit-handle API: a store bound to the
	// client at construction can be rebound at the site that owns a
	// RunTransaction:
	//
	//	err := client.RunTransaction(ctx, orm.ReadCommitted, func(tx orm.Context) error {
	//	    return svc.deps.Estimates.WithTx(tx).UpdateEstimate(ctx, e)
	//	})
	//
	// RunTx makes the rebinding unnecessary — the transaction rides in ctx,
	// and the client's connection resolver hands it to every query the
	// store makes with that ctx — so WithTx stays for existing callers.
	fmt.Fprintf(b, "func (s %s) WithTx(tx orm.Context) %s { return %s{db: tx} }\n\n", adapter, iface, adapter)

	// Compile-time proof. If a delegate's signature ever drifts from the
	// interface, THIS line fails to build — in generated code, at generate
	// time — instead of the mismatch surfacing in a user's package.
	fmt.Fprintf(b, "var _ %s = %s{}\n\n", iface, adapter)
}

// writeCompositeKeyStore is writeEntityStore for a composite-key entity:
// the same shape, with every by-key method taking the whole key and an
// Upsert, mirroring writeCompositeKeyDelegates exactly.
func writeCompositeKeyStore(b *strings.Builder, ent config.PlanEntity, keys []keyParam) {
	msgName := ent.Name
	iface := msgName + "Store"
	adapter := lowerFirst(msgName) + "Store"
	params, args, _ := keyParamList(msgName, keys)

	type method struct{ doc, sig, call string }
	methods := []method{
		{"Create" + msgName + " inserts a new " + msgName + " row (plain INSERT, never an upsert).",
			"Create" + msgName + "(ctx context.Context, msg *" + msgName + ") error", "Create" + msgName + "(ctx, s.db, msg)"},
		{"Get" + msgName + "ByPK retrieves a " + msgName + " by its whole key " + keyColumns(keys) + ".",
			"Get" + msgName + "ByPK(ctx context.Context, " + params + ") (*" + msgName + ", error)", "Get" + msgName + "ByPK(ctx, s.db, " + args + ")"},
		{"List" + msgName + " retrieves " + msgName + " rows with optional QueryOption filtering.",
			"List" + msgName + "(ctx context.Context, opts ...orm.QueryOption) ([]*" + msgName + ", error)", "List" + msgName + "(ctx, s.db, opts...)"},
		{"Count" + msgName + " counts " + msgName + " rows matching the given options.",
			"Count" + msgName + "(ctx context.Context, opts ...orm.QueryOption) (int64, error)", "Count" + msgName + "(ctx, s.db, opts...)"},
		{"Upsert" + msgName + " inserts msg, or updates the row with its key.",
			"Upsert" + msgName + "(ctx context.Context, msg *" + msgName + ") error", "Upsert" + msgName + "(ctx, s.db, msg)"},
	}
	if !ent.AppendOnly {
		methods = append(methods,
			method{"Update" + msgName + " writes every non-skipupdate column of msg.",
				"Update" + msgName + "(ctx context.Context, msg *" + msgName + ", opts ...crud.UpdateOption) error", "Update" + msgName + "(ctx, s.db, msg, opts...)"},
			method{"Update" + msgName + "Masked writes only the named fields.",
				"Update" + msgName + "Masked(ctx context.Context, msg *" + msgName + ", fields []string, opts ...crud.UpdateOption) error", "Update" + msgName + "Masked(ctx, s.db, msg, fields, opts...)"},
			method{"Delete" + msgName + "ByPK removes a " + msgName + " by its whole key.",
				"Delete" + msgName + "ByPK(ctx context.Context, " + params + ") error", "Delete" + msgName + "ByPK(ctx, s.db, " + args + ")"},
		)
	}

	fmt.Fprintf(b, "// %s is the generated CRUD surface for %s, whose primary key is\n", iface, msgName)
	fmt.Fprintf(b, "// the composite %s, shaped as an interface so a service can declare it\n", keyColumns(keys))
	b.WriteString("// as a Deps field and call it with nothing but its own arguments.\n")
	fmt.Fprintf(b, "type %s interface {\n", iface)
	for _, m := range methods {
		fmt.Fprintf(b, "\t// %s\n\t%s\n\n", m.doc, m.sig)
	}
	b.WriteString("\t// WithTx returns the same store bound to an explicit transaction handle.\n")
	fmt.Fprintf(b, "\tWithTx(tx orm.Context) %s\n}\n\n", iface)

	fmt.Fprintf(b, "// %s adapts the package-level %s delegates to %s,\n", adapter, msgName, iface)
	b.WriteString("// holding the handle those delegates take per call.\n")
	fmt.Fprintf(b, "type %s struct{ db orm.Context }\n\n", adapter)
	for _, m := range methods {
		fmt.Fprintf(b, "func (s %s) %s {\n\treturn %s\n}\n\n", adapter, m.sig, m.call)
	}
	fmt.Fprintf(b, "func (s %s) WithTx(tx orm.Context) %s { return %s{db: tx} }\n\n", adapter, iface, adapter)
	fmt.Fprintf(b, "var _ %s = %s{}\n\n", iface, adapter)
}

// writeReadOnlyStore is writeEntityStore for a view entity: the reads the
// delegates offer (writeReadOnlyDelegates) and nothing that writes.
func writeReadOnlyStore(b *strings.Builder, ent config.PlanEntity) {
	msgName := ent.Name
	iface := msgName + "Store"
	adapter := lowerFirst(msgName) + "Store"
	var key *ormField
	for _, f := range resolveORMFields(ent) {
		if f.isPK {
			key = &f
		}
	}
	type method struct{ doc, sig, call string }
	var methods []method
	if key != nil {
		methods = append(methods, method{"Get" + msgName + "ByID retrieves a " + msgName + " row by its " + key.columnName + ".",
			"Get" + msgName + "ByID(ctx context.Context, id " + key.goType + ") (*" + msgName + ", error)", "Get" + msgName + "ByID(ctx, s.db, id)"})
	}
	methods = append(methods,
		method{"List" + msgName + " retrieves " + msgName + " rows with optional QueryOption filtering.",
			"List" + msgName + "(ctx context.Context, opts ...orm.QueryOption) ([]*" + msgName + ", error)", "List" + msgName + "(ctx, s.db, opts...)"},
		method{"Count" + msgName + " counts " + msgName + " rows matching the given options.",
			"Count" + msgName + "(ctx context.Context, opts ...orm.QueryOption) (int64, error)", "Count" + msgName + "(ctx, s.db, opts...)"},
	)
	fmt.Fprintf(b, "// %s reads the %s view (read-only: a view is a derived\n", iface, msgName)
	b.WriteString("// read, so no write is generated).\n")
	fmt.Fprintf(b, "type %s interface {\n", iface)
	for _, m := range methods {
		fmt.Fprintf(b, "\t// %s\n\t%s\n\n", m.doc, m.sig)
	}
	b.WriteString("\t// WithTx returns the same store bound to an explicit transaction handle.\n")
	fmt.Fprintf(b, "\tWithTx(tx orm.Context) %s\n}\n\n", iface)
	fmt.Fprintf(b, "// %s adapts the package-level %s delegates to %s.\n", adapter, msgName, iface)
	fmt.Fprintf(b, "type %s struct{ db orm.Context }\n\n", adapter)
	for _, m := range methods {
		fmt.Fprintf(b, "func (s %s) %s {\n\treturn %s\n}\n\n", adapter, m.sig, m.call)
	}
	fmt.Fprintf(b, "func (s %s) WithTx(tx orm.Context) %s { return %s{db: tx} }\n\n", adapter, iface, adapter)
	fmt.Fprintf(b, "var _ %s = %s{}\n\n", iface, adapter)
}

// storePKGoType returns the Go type of an entity's primary key, matching
// what the ORM delegates use in their Get/Delete signatures. It mirrors
// renderORMEntity's own derivation — same fields, same default — so the
// interface cannot disagree with the delegate it abstracts.
func storePKGoType(ent config.PlanEntity) string {
	for _, f := range resolveORMFields(ent) {
		if f.isPK {
			return f.goType
		}
	}
	return "string"
}
