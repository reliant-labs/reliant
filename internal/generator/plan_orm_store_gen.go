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

// storeGenFile is the single generated file holding every entity's store
// interface plus the aggregate. One file, not one per entity: the
// interfaces are pure signatures derived from the same schema pass, and
// splitting them across N files would multiply the regen churn without
// giving anyone a smaller thing to read.
const storeGenFile = "store_gen.go"

// writeStoreInterfaces renders internal/db/store_gen.go for the given
// entities. Callers pass the same entity slice the ORM render used, so the
// interfaces cannot describe a different schema than the delegates they
// abstract.
func writeStoreInterfaces(root string, orm config.ORMConfig, entities []config.PlanEntity, cs *FileChecksums) error {
	if len(entities) == 0 {
		return nil
	}
	return writeORMFile(root, orm, storeGenFile, renderStoreInterfaces(orm.PackageName(), entities), cs)
}

// renderStoreInterfaces builds the store file: one interface per entity,
// mirroring that entity's generated delegates exactly, then an aggregate
// embedding them all.
func renderStoreInterfaces(pkgName string, entities []config.PlanEntity) []byte {
	var b strings.Builder

	writeORMGeneratedHeader(&b, "the APPLIED schema (db/migrations) — the CRUD delegates of every <entity>_orm_gen.go beside it.")
	fmt.Fprintf(&b, "package %s\n\n", pkgName)
	// crud is imported for UpdateOption, which only a store with an
	// Update names: a project whose every entity is append-only or a view
	// would otherwise fail to build on an unused import.
	if storesWrite(entities) {
		b.WriteString("import (\n\t\"context\"\n\n\t\"github.com/reliant-labs/forge/pkg/crud\"\n\t\"github.com/reliant-labs/forge/pkg/orm\"\n)\n\n")
	} else {
		b.WriteString("import (\n\t\"context\"\n\n\t\"github.com/reliant-labs/forge/pkg/orm\"\n)\n\n")
	}

	b.WriteString("// This file gives the generated CRUD delegates an INTERFACE shape, so a\n")
	b.WriteString("// domain package can depend on persistence the same way it depends on any\n")
	b.WriteString("// other component: a Deps field forge resolves by type.\n")
	b.WriteString("//\n")
	b.WriteString("// Depend on the narrowest thing that works — one entity's store when a\n")
	b.WriteString("// package touches one entity, the aggregate Store when it genuinely spans\n")
	b.WriteString("// many. The aggregate embeds the per-entity interfaces, so the two views\n")
	b.WriteString("// never disagree.\n")
	b.WriteString("//\n")
	b.WriteString("// These cover the GENERATED CRUD surface only. A hand-written or raw-SQL\n")
	b.WriteString("// query belongs in the entity's <entity>_repo_ext.go seam; declare it on\n")
	b.WriteString("// your own interface in the consuming package, because this file is\n")
	b.WriteString("// regenerated and would go stale against it.\n\n")

	for _, ent := range entities {
		writeEntityStore(&b, ent)
	}

	writeAggregateStore(&b, entities)

	src := []byte(b.String())
	if formatted, err := format.Source(src); err == nil {
		return formatted
	}
	return src
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

// writeAggregateStore emits the all-entity interface and its adapter.
func writeAggregateStore(b *strings.Builder, entities []config.PlanEntity) {
	b.WriteString("// Store is every entity's store in one interface, for a service that\n")
	b.WriteString("// genuinely spans several tables (a reporting or analytics service, a\n")
	b.WriteString("// use-case orchestrator). Prefer a per-entity store when one is enough —\n")
	b.WriteString("// a narrower dep is a smaller fake in a test.\n")
	// ACCESSORS, not embedded interfaces. Each per-entity store declares its
	// own WithTx, so embedding N of them makes the selector ambiguous and the
	// aggregate cannot satisfy its own interface — measured: `forge generate`
	// rejected exactly that render with "ambiguous selector store.WithTx".
	// Accessors also read better at the call site: `s.DB.Estimates().Get...`
	// says which table it is touching.
	b.WriteString("type Store interface {\n")
	for _, ent := range entities {
		fmt.Fprintf(b, "\t// %s returns the %s store.\n", naming.Pluralize(ent.Name), ent.Name)
		fmt.Fprintf(b, "\t%s() %sStore\n", naming.Pluralize(ent.Name), ent.Name)
	}
	b.WriteString("\n\t// WithTx returns every store bound to one transaction handle, so a\n")
	b.WriteString("\t// use case spanning tables commits or rolls back as a unit.\n")
	b.WriteString("\tWithTx(tx orm.Context) Store\n")
	b.WriteString("}\n\n")

	// The aggregate embeds the per-entity ADAPTERS, so it satisfies Store
	// without restating a method. Each embedded adapter carries its own
	// handle; NewStore gives them all the same one.
	b.WriteString("// store is the aggregate adapter: one handle, one accessor per entity.\n")
	b.WriteString("type store struct{ db orm.Context }\n\n")
	for _, ent := range entities {
		fmt.Fprintf(b, "func (s store) %s() %sStore { return %s{db: s.db} }\n",
			naming.Pluralize(ent.Name), ent.Name, lowerFirst(ent.Name)+"Store")
	}
	b.WriteString("\n")

	// Each per-entity WithTx is promoted from its embedded adapter and returns
	// only THAT entity's store, so the aggregate needs its own differently
	// named rebinder. Naming it WithTx too would be ambiguous across the
	// embedded set and would not compile.
	b.WriteString("// WithTx rebinds every accessor to tx.\n")
	b.WriteString("func (store) WithTx(tx orm.Context) Store { return NewStore(tx) }\n\n")

	b.WriteString("// NewStore returns the aggregate store bound to db. Pass the *orm.Client\n")
	b.WriteString("// for ordinary use, or a transaction handle to scope every call to it.\n")
	b.WriteString("func NewStore(db orm.Context) Store { return store{db: db} }\n\n")
	b.WriteString("var _ Store = store{}\n")
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
