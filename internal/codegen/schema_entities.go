package codegen

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/jinzhu/inflection"

	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/shadowdb"
	"github.com/reliant-labs/forge/pkg/schemadef"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// ownerMarkerTokenRE matches a `forge:owner` declaration as a WHOLE
// token, so a future or unrelated marker whose name merely begins with
// this one (`forge:owner-hint`) does not read as an ownership
// declaration. Column.HasMarker is a plain substring test, which is right
// for the markers that have no near-neighbours and wrong here.
//
// The audit applies the same discipline to the same marker
// (internal/cli/audit's ownerMarkerTokenRE), and it matters that the two
// agree: this one decides whether the shim scaffolds a scoping reminder,
// that one decides whether shipping without it fails the build. A
// scaffold that fired where the gate did not — or a gate that fired with
// no scaffold to answer it — is worse than either alone.
var ownerMarkerTokenRE = regexp.MustCompile(regexp.QuoteMeta(schemadef.ColumnMarkerOwner) + `(?:[^\w:-]|$)`)

// declaresOwnerMarker reports whether a catalog comment declares
// schemadef.ColumnMarkerOwner.
func declaresOwnerMarker(comment string) bool {
	return ownerMarkerTokenRE.MatchString(comment)
}

// BuildSchemaEntities is the entity source of truth: it joins the
// APPLIED schema (db/migrations shadow-applied and introspected) with
// the service protos' CRUD method shapes.
//
// An entity exists when BOTH halves exist:
//
//   - a service declares CRUD RPCs for it (Create<X>/Get<X>/List<Xs>/...),
//     giving the wire message shape, and
//   - the applied schema has the matching table (pluralized snake_case
//     of the entity name), giving columns/PK/conventions.
//
// CRUD RPCs without a table generate nothing (the honest-routes
// contract: no pages, no ORM, no nav for entities that don't exist).
// Tables without CRUD RPCs are plain schema — owned by hand-written
// code, invisible to the CRUD/frontend projections.
func BuildSchemaEntities(projectDir string, services []ServiceDef) ([]EntityDef, error) {
	tables, err := shadowdb.Tables(projectDir, filepath.Join(projectDir, "db", "migrations"))
	if err != nil {
		return nil, err
	}
	if len(tables) == 0 {
		return nil, nil
	}
	if err := schemadef.CheckMarkers(tables); err != nil {
		return nil, err
	}
	return entitiesForTables(tables, services), nil
}

// entitiesForTables is the pure half of BuildSchemaEntities: the join
// itself, with the schema already introspected. Split out so the join's
// rules can be tested without a shadow database.
func entitiesForTables(tables []schemadef.Table, services []ServiceDef) []EntityDef {
	idx := indexEntityTables(tables)

	seen := map[string]bool{}
	var entities []EntityDef
	for _, svc := range services {
		for _, m := range svc.Methods {
			// A `// forge:custom` rpc claims no CRUD entity: when every
			// CRUD-named rpc of a table carries it, the table gets no CRUD
			// wiring, page or seed root (its store is BuildORMEntities').
			op, name := CRUDShape(m)
			if op == "" {
				continue
			}
			if op == "list" {
				name = inflection.Singular(name)
			}
			key := strings.ToLower(name)
			if seen[key] {
				continue
			}
			table, ok := idx.lookup(name)
			if !ok {
				continue
			}
			// The RPC NAME is not evidence that the wire message exists.
			// `ListSecrets`/`DeleteSecret` parse to the entity "Secret"
			// whether or not the service declares a Secret message — and
			// control-plane's SecretStoreService declares SecretSummary and
			// SecretVersion instead. Projecting an entity anyway emits a
			// CRUD page and mock fixtures that import `Secret` and
			// `SecretSchema` from a _pb module exporting neither, so the
			// frontend stops compiling. Require the message.
			if !declaresWireMessage(svc, name) {
				continue
			}
			seen[key] = true
			// A composite PK has no single column the CRUD projection's
			// Get/Update/Delete (which take ONE id) can key on. Fabricating
			// "id" here used to silently name a column that does not exist
			// on the table, producing broken generated code with no signal
			// to the user. Skip the entity entirely instead — its RPCs fall
			// through to the ordinary stub path, exactly as if no matching
			// table existed at all — and say so once, by name, so the user
			// knows to reach it via the repo_ext seam rather than wonder why
			// CRUD never showed up.
			if len(table.PKCols) > 1 {
				fmt.Printf("ℹ️  %s has a composite primary key (%s) — CRUD projection and ULID id-generation are skipped; the table is plain schema (query it via the repo_ext seam).\n",
					table.Name, strings.Join(table.PKCols, ", "))
				continue
			}
			entities = append(entities, buildEntityDef(name, table, svc))
		}
	}
	sort.Slice(entities, func(i, j int) bool { return entities[i].Name < entities[j].Name })
	return entities
}

// BuildORMEntities is the DATA LAYER's entity set: every applied BASE
// TABLE, projected whether or not a service declares CRUD rpcs for it.
//
// It is deliberately wider than BuildSchemaEntities. That join decides
// what gets CRUD wiring, pages and seed roots — wire-facing projections
// that need a wire message. The store needs nothing from the wire: its
// struct is the table's columns, its delegates are keyed by the table's
// primary key. Gating it on CRUD rpcs made a project whose rpcs are all
// custom (`// forge:custom`), or whose tables have no rpc at all, get no
// data layer whatsoever — the schema was the truth and nothing projected
// it.
//
// A table opts out with `COMMENT ON TABLE t IS 'forge:no-entity'`
// (schemadef.TableMarkerNoEntity); the project opts out with
// `//forge:no-orm`.
func BuildORMEntities(projectDir string, services []ServiceDef) ([]EntityDef, error) {
	tables, err := shadowdb.Tables(projectDir, filepath.Join(projectDir, "db", "migrations"))
	if err != nil {
		return nil, err
	}
	if len(tables) == 0 {
		return nil, nil
	}
	views, err := shadowdb.Views(projectDir, filepath.Join(projectDir, "db", "migrations"))
	if err != nil {
		return nil, err
	}
	if err := schemadef.CheckMarkers(append(tables, views...)); err != nil {
		return nil, err
	}
	entities, notes, err := ormEntitiesForTables(append(tables, views...), services)
	for _, n := range notes {
		fmt.Println("ℹ️  " + n)
	}
	return entities, err
}

// ormEntitiesForTables is the pure half of BuildORMEntities. A table the
// CRUD join claims keeps that entity verbatim (its name is the wire
// message's, and its wire fields carry `forge:secret`), so the ORM and the
// CRUD wiring that compiles against it agree on every name. Every other
// table is named from the table itself (TableEntityName).
//
// notes are one-line reasons for tables left out of the projection; err
// names two tables that would project to the same Go name, which cannot
// both be declared in one package.
func ormEntitiesForTables(tables []schemadef.Table, services []ServiceDef) (entities []EntityDef, notes []string, err error) {
	var baseTables []schemadef.Table
	for _, t := range tables {
		if !t.IsView { // a view is never a CRUD entity
			baseTables = append(baseTables, t)
		}
	}
	byTable := map[string]EntityDef{}
	for _, e := range entitiesForTables(baseTables, services) {
		byTable[e.TableName] = e
	}

	owner := map[string]string{} // entity name (lowercased) → table
	sorted := append([]schemadef.Table(nil), tables...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, t := range sorted {
		if t.NoEntity() {
			continue
		}
		if t.IsView {
			e, ok := viewEntityDef(t)
			if !ok {
				continue
			}
			key := strings.ToLower(e.Name)
			if prev, dup := owner[key]; dup {
				return nil, notes, fmt.Errorf("%s and the view %s both project to the entity %s, and one package cannot declare it twice: rename one",
					prev, t.Name, e.Name)
			}
			owner[key] = t.Name
			entities = append(entities, e)
			continue
		}
		e, joined := byTable[t.Name]
		if !joined {
			// A composite key projects too: its store addresses a row by
			// the whole key (Get<E>ByPK / Delete<E>ByPK / Upsert<E>). Only
			// the CRUD join, whose rpcs take ONE id, leaves it out.
			if len(t.PKCols) == 0 {
				notes = append(notes, fmt.Sprintf("%s has no primary key — no entity is projected; query it via db.Bun().", t.Name))
				continue
			}
			e = buildEntityDef(TableEntityName(t), t, ServiceDef{})
		}
		key := strings.ToLower(e.Name)
		if prev, dup := owner[key]; dup {
			return nil, notes, fmt.Errorf("tables %s and %s both project to the entity %s, and one package cannot declare it twice: "+
				"opt one out with `COMMENT ON TABLE %s IS '%s'`, or rename it",
				prev, t.Name, e.Name, t.Name, schemadef.TableMarkerNoEntity)
		}
		owner[key] = t.Name
		entities = append(entities, e)
	}
	sort.Slice(entities, func(i, j int) bool { return entities[i].Name < entities[j].Name })
	return entities, notes, nil
}

// viewEntityDef projects a VIEW opted in with `forge:entity` as a read-only
// entity, ok=false for one that did not opt in. Its key is the column
// `forge:entity key=<column>` names, else `id` when the view has one, else
// none (List/Count only). The catalog cannot say whether a view column is
// nullable, so every column but the key is — its Go field a pointer.
func viewEntityDef(v schemadef.Table) (EntityDef, bool) {
	declared, key := v.DeclaresEntity()
	if !declared {
		return EntityDef{}, false
	}
	if key == "" {
		for _, c := range v.Columns {
			if c.Name == "id" {
				key = "id"
			}
		}
	}
	for i := range v.Columns {
		if v.Columns[i].Name == key {
			v.Columns[i].IsPK, v.Columns[i].NotNull = true, true
			v.PKCols = []string{key}
		}
	}
	e := buildEntityDef(TableEntityName(v), v, ServiceDef{})
	e.ReadOnly = true
	// A view is a derived read: no managed timestamps to stamp, no soft
	// delete to scope by (a deleted_at the view exposes is just a column).
	e.SoftDelete, e.Timestamps, e.AppendOnly = false, false, false
	e.Constraints = nil
	if len(v.PKCols) == 0 {
		e.PkField, e.PkGoType = "", ""
	}
	return e, true
}

// TableEntityName is the Go name of the entity a table projects to when no
// CRUD rpc names it: the singular of the table, camel-cased the way
// protoc-gen-go camel-cases a message name (`message_content_blocks` →
// MessageContentBlock, `api_keys` → ApiKey, `provider_backoff` →
// ProviderBackoff). Spelled like a wire message on purpose: the day a
// service adds `Get<E>` over the table, the CRUD join names the entity
// after that message, and the two spellings must already agree.
//
// A `forge:entity-name=<Name>` declaration in the table's COMMENT wins
// (schemadef.TableMarkerEntityName): it is how a table whose name derives
// to the wrong thing says what it holds.
func TableEntityName(t schemadef.Table) string {
	if declared := t.DeclaredEntityName(); declared != "" {
		return declared
	}
	return naming.GoCamelCase(inflection.Singular(t.Name))
}

// entityTables resolves a CRUD rpc's entity name to the table behind it.
type entityTables struct {
	byTable map[string]schemadef.Table
	byName  map[string]schemadef.Table // lower-cased TableEntityName → table
}

// indexEntityTables indexes every table that can be an entity — all but
// `forge:no-entity` ones, which are plain schema on both sides: no store,
// and no CRUD wiring over a store that does not exist.
func indexEntityTables(tables []schemadef.Table) entityTables {
	idx := entityTables{byTable: map[string]schemadef.Table{}, byName: map[string]schemadef.Table{}}
	sorted := append([]schemadef.Table(nil), tables...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, t := range sorted {
		if t.NoEntity() {
			continue
		}
		idx.byTable[t.Name] = t
		key := strings.ToLower(TableEntityName(t))
		if _, dup := idx.byName[key]; !dup || t.DeclaredEntityName() != "" {
			idx.byName[key] = t
		}
	}
	return idx
}

// lookup finds the table an entity name means. The table's own entity name
// decides first — a `forge:entity-name` declaration, else the singular of
// the table, which is what matches a singular-named table such as
// `daemon_attachment` to DaemonAttachment. The conventional plural and the
// literal singular of the name follow, for an entity whose spelling differs
// from its table's derivation only by initialism case (`APIKey` over
// `api_keys`).
func (idx entityTables) lookup(entity string) (schemadef.Table, bool) {
	if t, ok := idx.byName[strings.ToLower(entity)]; ok {
		return t, true
	}
	for _, cand := range EntityTableCandidates(entity) {
		if t, ok := idx.byTable[cand]; ok {
			return t, true
		}
	}
	return schemadef.Table{}, false
}

// EntityTableCandidates are the table names an entity name maps to by
// convention, in order: the plural (`Bookmark` → `bookmarks`, what forge
// births) and the singular (`daemon_attachment`). A consumer that holds
// table names but not the catalog — the seed scope, the ownership audit —
// tries these; the join itself also honors `forge:entity-name`.
func EntityTableCandidates(entity string) []string {
	snake := naming.ToSnakeCase(entity)
	plural := naming.Pluralize(snake)
	if plural == snake {
		return []string{snake}
	}
	return []string{plural, snake}
}

// UnmatchedCRUDRPC is a CRUD-named rpc that names a wire message but no
// table: forge would stub it, and nothing in the tree would say why.
type UnmatchedCRUDRPC struct {
	Service, RPC, Entity string
	GoName               string   // the rpc's *Service method name
	Looked               []string // the table names it looked for
}

// UnmatchedCRUDRPCs lists every unary, unmarked CRUD-named rpc on a managed
// service whose entity has a wire message but matches no table.
//
// It used to be silence: the rpc fell through to a pb-through stub and
// served Unimplemented, which is how six of reliant's `Get<E>` rpcs over
// singular-named tables (`daemon_attachment`, `provider_backoff`) became
// stubs nobody asked for. An rpc whose entity has no wire message is not
// listed (the name alone is no evidence of an entity — see
// declaresWireMessage), nor is one on a descriptor with no message
// inventory, which cannot tell.
func UnmatchedCRUDRPCs(tables []schemadef.Table, services []ServiceDef) []UnmatchedCRUDRPC {
	idx := indexEntityTables(tables)
	var out []UnmatchedCRUDRPC
	for _, svc := range services {
		if svc.Unmanaged || (len(svc.SchemaFiles) == 0 && len(svc.Schemas) == 0 && len(svc.Messages) == 0) {
			continue
		}
		for _, m := range svc.Methods {
			op, name := CRUDShape(m)
			if op == "" {
				continue
			}
			if op == "list" {
				name = inflection.Singular(name)
			}
			if _, ok := idx.lookup(name); ok || !declaresWireMessage(svc, name) {
				continue
			}
			out = append(out, UnmatchedCRUDRPC{Service: svc.Name, RPC: m.Name, Entity: name, GoName: m.GoName(), Looked: EntityTableCandidates(name)})
		}
	}
	return out
}

// WithoutHandImplemented drops the rpcs projectDir's handler packages
// implement by hand: a CRUD-named rpc over no table that the author wrote
// (a computed ListArchivedChats, a GetRun over a workflow engine) is
// custom in fact, and nothing about it is silent. What stays is an rpc
// with no method at all, or only forge's `forge:gen unwired-stub` — the
// one that would serve Unimplemented with nothing saying why.
func WithoutHandImplemented(projectDir string, unmatched []UnmatchedCRUDRPC) []UnmatchedCRUDRPC {
	type scan struct{ existing, stubs map[string]bool }
	dirs := map[string]scan{}
	var out []UnmatchedCRUDRPC
	for _, u := range unmatched {
		sc, ok := dirs[u.Service]
		if !ok {
			if res, err := ResolveServiceComponent(projectDir, u.Service); err == nil {
				if existing, err := ScanExistingMethods(res.Dir, false); err == nil {
					for name := range ScanHandlersCrudMethods(res.Dir) {
						existing[name] = true
					}
					sc = scan{existing: existing, stubs: ScanUnwiredStubMethods(res.Dir)}
				}
			}
			dirs[u.Service] = sc
		}
		if sc.existing[u.GoName] && !sc.stubs[u.GoName] {
			continue
		}
		out = append(out, u)
	}
	return out
}

// UnmatchedCRUDRPCsError renders UnmatchedCRUDRPCs as one refusal, nil for
// none.
func UnmatchedCRUDRPCsError(unmatched []UnmatchedCRUDRPC) error {
	if len(unmatched) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("CRUD-named rpcs whose entity has a wire message but no table, and no implementation but forge's Unimplemented stub — nothing would say why:\n")
	for _, u := range unmatched {
		fmt.Fprintf(&b, "  - %s.%s: entity %s, no table %s\n", u.Service, u.RPC, u.Entity, strings.Join(u.Looked, " or "))
	}
	b.WriteString("Resolve each one: write the table's migration (or birth it with `forge scaffold`); if the table exists under another name, " +
		"declare what it holds with `COMMENT ON TABLE <table> IS 'forge:entity-name=<Entity>'`; or, if the rpc is not CRUD, put `// forge:custom` above it.")
	return errors.New(b.String())
}

// FrontendEntities is the subset of entities the frontend projections (CRUD
// pages, nav, mock fixtures, mock transport) can render coherently: those whose
// wire message carries a real key (EntityWireKey). The pages key rows, build
// detail/edit links and the mock store by that field, so an entity with no key on
// the wire (a surrogate `id` the message never exposes, served by a custom
// List+Update pair) would emit `item.id` against a message with no `id` — code
// that cannot typecheck. Such an entity stays schema and
// server CRUD; it just gets no generated UI. An entity with no wire fields at
// all (a legacy descriptor with no deep inventory) proves nothing and is kept.
// The second return names each dropped entity and why, for one-line reporting.
func FrontendEntities(entities []EntityDef) (kept []EntityDef, dropped []string) {
	for _, e := range entities {
		if reason := entityWirePKGap(e); reason != "" {
			dropped = append(dropped, reason)
			continue
		}
		kept = append(kept, e)
	}
	return kept, dropped
}

func entityWirePKGap(e EntityDef) string {
	if len(e.Fields) == 0 {
		return ""
	}
	if _, ok := EntityWireKey(e); ok {
		return ""
	}
	return fmt.Sprintf("%s: neither its primary key %q nor a conventional %q field is on its wire message — no CRUD pages or mocks generated (the entity stays schema and server CRUD)",
		e.Name, e.PkField, inflection.Singular(e.TableName)+"_id")
}

// EntityWireKey is the field the frontend keys an entity's rows by, and ok=false
// when the wire message offers no real key. In order: the DB primary key when the
// message carries it, else the conventional `<singular table>_id` domain key (a
// table can hold a surrogate `id` while the published proto exposes
// `usage_event_id` instead). Picking "the first field" or a literal "id" is a
// guess, not a key, and is deliberately not offered here.
func EntityWireKey(e EntityDef) (field string, ok bool) {
	wire := make(map[string]bool, len(e.Fields))
	for _, f := range e.Fields {
		wire[f.Name] = true
	}
	if e.PkField != "" && wire[e.PkField] {
		return e.PkField, true
	}
	if conventional := inflection.Singular(e.TableName) + "_id"; wire[conventional] {
		return conventional, true
	}
	return "", false
}

// declaresWireMessage reports whether svc's descriptor actually contains a
// message named pkg.name — the entity's wire shape.
//
// A descriptor produced before SchemaFiles/Schemas existed carries no
// message inventory, and there the answer is yes: a gate with no evidence
// must not drop every entity in the project. Only a descriptor that DOES
// enumerate messages, and does not list this one, is a refusal.
func declaresWireMessage(svc ServiceDef, name string) bool {
	fq := svc.Package + "." + name
	if len(svc.SchemaFiles) > 0 {
		_, ok := svc.SchemaFiles[fq]
		return ok
	}
	if len(svc.Schemas) > 0 {
		_, ok := svc.Schemas[fq]
		return ok
	}
	if len(svc.Messages) > 0 {
		_, ok := svc.Messages[name]
		return ok
	}
	return true
}

func buildEntityDef(name string, table schemadef.Table, svc ServiceDef) EntityDef {
	conv := schemadef.DetectConventions(table)

	// ProtoFile must name the file that DECLARES the entity wire message,
	// not necessarily the service's own proto file. When protos are split
	// (domain entity messages in a shared file, CRUD services in per-service
	// files) the two differ, and downstream codegen (mock data, CRUD pages)
	// imports the entity's `*Schema` / type from this file's generated _pb
	// module. SchemaFiles carries that provenance, keyed by FQ message name;
	// fall back to the service file for older descriptors / same-file cases.
	protoFile := svc.ProtoFile
	if svc.SchemaFiles != nil {
		if f, ok := svc.SchemaFiles[svc.Package+"."+name]; ok && f != "" {
			protoFile = f
		}
	}

	e := EntityDef{
		Name:          name,
		TableName:     table.Name,
		ProtoFile:     protoFile,
		SoftDelete:    conv.SoftDelete,
		Timestamps:    conv.Timestamps,
		AppendOnly:    conv.AppendOnly,
		SearchColumns: conv.SearchColumns,
		Constraints:   EntityConstraintsFromTable(table),
	}

	// Columns: the applied schema, verbatim.
	pools := seedplan.PoolsFromTables([]schemadef.Table{table})[table.Name]
	bounds := seedplan.BoundsFromTables([]schemadef.Table{table})[table.Name]
	for _, c := range table.Columns {
		fillStrategy, _ := c.FillStrategy()
		e.Columns = append(e.Columns, EntityColumn{
			Name:         c.Name,
			Type:         string(c.Type),
			IsArray:      c.IsArray,
			NotNull:      c.NotNull,
			IsPK:         c.IsPK,
			DeclType:     c.DeclType,
			Default:      c.Default,
			IsGenerated:  c.IsGenerated,
			IsIdentity:   c.IsIdentity,
			Immutable:    c.HasMarker(schemadef.ColumnMarkerImmutable),
			Version:      c.HasMarker(schemadef.ColumnMarkerVersion),
			FillStrategy: fillStrategy,
			Owner:        declaresOwnerMarker(c.Comment),
			GoType:       c.GoTypeOverride(),
			ZeroRejected: checkRejectsZero(c, pools[c.Name], bounds[c.Name]),
		})
	}

	// PK: the CRUD projection keys on ONE column. BuildSchemaEntities
	// filters composite-PK tables out before calling this function; the
	// ORM set keeps them (their store takes the whole key, read off the
	// columns' IsPK), and for those PkField stays empty rather than naming
	// one key column as if it were the key. The "id" fallback covers only a
	// table with no declared PK at all.
	e.PkField = "id"
	e.PkGoType = "string"
	if len(table.PKCols) > 1 {
		e.PkField, e.PkGoType = "", ""
	}
	if len(table.PKCols) == 1 {
		e.PkField = table.PKCols[0]
		for _, c := range table.Columns {
			if c.Name == e.PkField {
				e.PkGoType = ColumnGoType(string(c.Type), c.DeclType, c.IsArray)
			}
		}
	}

	// Wire fields from the service proto's entity message.
	e.Fields = WireEntityFields(svc, name)

	// forge:secret — a secret column is stripped from read responses
	// (crud_convert's toProto skip); it must also stay OUT of the generated
	// list `search` filter, or an ILIKE over it becomes an oracle for probing
	// the secret value. Drop any secret field's column from SearchColumns.
	e.SearchColumns = dropSecretSearchColumns(e.SearchColumns, e.Fields)
	return e
}

// checkRejectsZero reports whether the table's single-column CHECKs keep c
// from ever holding its Go zero: an enum vocabulary (`col IN (...)`) that
// lacks the empty string or 0, or a numeric bound that excludes 0. Only scalar columns
// are judged; anything the CHECK parsers do not read answers false — the
// zero is then presumed storable, which is the safe side (a pointer field).
func checkRejectsZero(c schemadef.Column, pool []string, bound seedplan.NumBound) bool {
	if c.IsArray {
		return false
	}
	switch c.Type {
	case schemadef.TypeString:
		return len(pool) > 0 && !slices.Contains(pool, "")
	case schemadef.TypeInt, schemadef.TypeFloat:
		if len(pool) > 0 {
			for _, v := range pool {
				if f, err := strconv.ParseFloat(v, 64); err != nil || f == 0 {
					return false
				}
			}
			return true
		}
		return (bound.Min != nil && *bound.Min > 0) || (bound.Max != nil && *bound.Max < 0)
	}
	return false
}

// dropSecretSearchColumns removes columns backed by a `// forge:secret` wire
// field from the list-search span. Search columns are text columns by
// convention (schemadef), which cannot see the proto-level marker — this is
// the join point where the marker prunes the search set.
func dropSecretSearchColumns(cols []string, fields []EntityField) []string {
	secret := map[string]bool{}
	for _, f := range fields {
		if f.Secret {
			secret[f.Name] = true
		}
	}
	if len(secret) == 0 {
		return cols
	}
	out := cols[:0:0]
	for _, c := range cols {
		if !secret[c] {
			out = append(out, c)
		}
	}
	return out
}

// CanonicalGoTypeOK maps a canonical schema type (the closed vocabulary
// schemadef.MapDeclaredType produces) to the Go type a column projects to
// on the generated entity struct — the NOT NULL variant; a nullable column
// adds a pointer, and an array column never does because a slice already
// carries nil.
//
// This is the SINGLE definition. There used to be three: this one, the CRUD
// conversion generator's dbBaseGoType, and internal/generator's
// planFieldGoType. They disagreed exactly where it mattered — dbBaseGoType
// collapsed every array column that was not BIGINT[] to []string, so a
// BYTEA[] column projected to []string in the half that decides whether a
// wire field pairs with it and to something else in the half that declares
// the struct field. Two projections of one column is one projection too
// many; the other two now route here.
//
// A json ARRAY column (jsonb[]) projects to []string: each element is a
// document, the same way a scalar json column projects to the document
// text.
//
// ok=false means the string is not a canonical schema type and the "string"
// answer is a FALLBACK rather than a mapping — the same distinction, for
// the same reason, that schemadef.MapDeclaredType draws with its `known`
// return. Every caller holds a type that is supposed to be canonical, so
// ok=false is a forge bug and each one reports it by name rather than
// declaring the field as text.
//
// The `default:` arm this used to end with covered "string, json, unknown"
// in one breath, which is the defect in miniature: `string` and `json` are
// MAPPED to Go string, and "unknown" is a type forge has no projection for.
// Collapsing the three meant a canonical type added upstream without a
// projection here became a Go `string` field in silence.
func CanonicalGoTypeOK(canonical string, isArray bool) (goType string, ok bool) {
	var base string
	switch canonical {
	case "string":
		base = "string"
	case "json":
		// A json column holds a document: json.RawMessage, the bytes of
		// one, which Bun binds and scans as JSON and encoding/json embeds
		// verbatim — lossless, with no text round trip. A json ARRAY column
		// (jsonb[]) stays []string: each element is one document's text.
		if isArray {
			return "[]string", true
		}
		return "json.RawMessage", true
	case "int64":
		base = "int64"
	case "int32", "int16":
		// The width-refined integers (GoIntType): `integer` / `smallint`
		// columns, which hold no more than these.
		base = canonical
	case "float64":
		base = "float64"
	case "bool":
		base = "bool"
	case "time":
		base = "time.Time"
	case "bytes":
		base = "[]byte"
	default:
		base, ok = "string", false
		if isArray {
			return "[]" + base, ok
		}
		return base, ok
	}
	if isArray {
		return "[]" + base, true
	}
	return base, true
}

// GoIntType refines the canonical int64 of an integer column to the
// narrowest Go type that holds every value of its declared width:
// `integer`/`int4`/`serial` → int32, `smallint`/`int2`/`smallserial` →
// int16, anything else (bigint) → int64. Lossless and idiomatic: a 32-bit
// column read into int64 claims a range it can never return, and every
// write needs a guard the column itself already enforces.
func GoIntType(declType string) string {
	d := strings.ToUpper(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(declType), "[]")))
	switch d {
	case "INTEGER", "INT", "INT4", "SERIAL", "SERIAL4":
		return "int32"
	case "SMALLINT", "INT2", "SMALLSERIAL", "SERIAL2":
		return "int16"
	}
	return "int64"
}

// ColumnGoType is the Go type a column projects to on the entity struct
// (the NOT NULL variant): CanonicalGoTypeOK of its canonical type, with an
// integer narrowed to its declared width (GoIntType). It is the one
// projection the ORM struct (via EntityDefToPlanEntity's plan type) and the
// CRUD conversions read.
func ColumnGoType(canonical, declType string, isArray bool) string {
	if canonical == "int64" {
		canonical = GoIntType(declType)
	}
	return canonicalGoTypeMust(canonical, isArray)
}

// canonicalGoTypeMust projects a column type that is known to be canonical
// — it came out of schemadef — so an unmapped one is a forge bug and stops
// generation by name rather than declaring the field as text.
func canonicalGoTypeMust(canonical string, isArray bool) string {
	goType, ok := CanonicalGoTypeOK(canonical, isArray)
	if !ok {
		panic("codegen: no Go projection for canonical schema type " + canonical)
	}
	return goType
}

// WireEntityFields extracts the entity wire-message fields from the
// service descriptor: the deep Schemas map when present, else the
// shallow Messages map (older descriptors). It is the WIRE half of an
// entity, the half BuildEntityConv pairs against the applied schema's
// columns — exported so the birth↔conversion contract can be asserted from
// internal/scaffold, which owns the other half.
func WireEntityFields(svc ServiceDef, entityName string) []EntityField {
	if defs, ok := svc.Schemas[svc.Package+"."+entityName]; ok {
		fields := make([]EntityField, 0, len(defs))
		for _, d := range defs {
			fields = append(fields, schemaFieldToEntityField(d))
		}
		return withMessageGoNames(fields)
	}
	defs, ok := svc.Messages[entityName]
	if !ok {
		return nil
	}
	fields := make([]EntityField, 0, len(defs))
	for _, d := range defs {
		fields = append(fields, messageFieldToEntityField(d))
	}
	return withMessageGoNames(fields)
}

// withMessageGoNames sets each field's GoName to the struct field
// protoc-gen-go generates for it, given that fields is the WHOLE message
// in declaration order. The per-field converters can only camel-case one
// name; protoc-gen-go also renames a field that collides with a generated
// method or an earlier field's getter, which needs the siblings.
func withMessageGoNames(fields []EntityField) []EntityField {
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.Name
	}
	for i, goName := range naming.GoFieldNames(names) {
		fields[i].GoName = goName
	}
	return fields
}

// messageFieldGoName is the Go struct field protoc-gen-go generates for the
// field called name on a message whose fields are fields (declaration
// order) — e.g. the entity-carrying field of an update request.
func messageFieldGoName(fields []MessageFieldDef, name string) string {
	siblings := make([]string, len(fields))
	for i, f := range fields {
		siblings[i] = f.Name
	}
	return naming.GoFieldName(name, siblings)
}

func schemaFieldToEntityField(d SchemaFieldDef) EntityField {
	f := EntityField{
		Name: d.Name,
		// Exact unless the field collides with a sibling or a generated
		// method; list builders correct that with withMessageGoNames.
		GoName:   naming.GoCamelCase(d.Name),
		Optional: d.Optional,
		Secret:   d.Secret,
		ReadOnly: d.ReadOnly,
	}
	switch d.Kind {
	case "message":
		f.ProtoType = "message"
		f.MessageType = d.TypeName
		f.GoType = wellKnownGoType(d.TypeName)
		if d.Repeated {
			f.GoType = "[]*" + shortName(d.TypeName)
		}
	case "enum":
		f.ProtoType = "enum"
		f.MessageType = d.TypeName
		f.GoType = shortName(d.TypeName)
		// The message and scalar arms both record `repeated` in GoType; the
		// enum arm did not, and the descriptor's repeated bit was simply
		// dropped. Every consumer then read a `repeated Status` field as a
		// singular one: the frontend mocked the number 1 into a Status[]
		// and rendered <StatusBadge value={item.tags}> against a prop typed
		// `string | number`. Kind deliberately stays FieldKindEnum — the
		// conversion generator branches on it, and a repeated enum is still
		// an enum.
		if d.Repeated {
			f.GoType = "[]" + f.GoType
		}
	case "map":
		f.ProtoType = "message"
		f.GoType = "map[string]string" // marker; the real key/value types live on the descriptor
		f.MapValueKind = d.MapValueKind
	default: // scalar
		f.ProtoType = d.Kind
		f.GoType = ProtoTypeToGoType(d.Kind)
		if d.Repeated {
			f.GoType = "[]" + f.GoType
		}
	}
	f.Kind = DetermineFieldKind(f.ProtoType, f.GoType)
	if d.Kind == "map" {
		f.Kind = FieldKindMap
	}
	return f
}

func messageFieldToEntityField(d MessageFieldDef) EntityField {
	f := EntityField{
		Name:      d.Name,
		GoName:    naming.GoCamelCase(d.Name), // see schemaFieldToEntityField
		ProtoType: d.ProtoType,
		Optional:  d.IsOptional,
	}
	if d.ProtoType == "message" {
		f.MessageType = d.MessageType
		f.GoType = wellKnownGoType(d.MessageType)
	} else {
		f.GoType = ProtoTypeToGoType(d.ProtoType)
	}
	f.Kind = DetermineFieldKind(f.ProtoType, f.GoType)
	return f
}

// wellKnownGoType maps well-known proto message types to their Go
// representation; other messages map to a pointer to their short name.
func wellKnownGoType(typeName string) string {
	switch typeName {
	case "google.protobuf.Timestamp":
		return "*timestamppb.Timestamp"
	case "google.protobuf.StringValue":
		return "*string"
	case "google.protobuf.Int32Value":
		return "*int32"
	case "google.protobuf.Int64Value":
		return "*int64"
	case "google.protobuf.UInt32Value":
		return "*uint32"
	case "google.protobuf.UInt64Value":
		return "*uint64"
	case "google.protobuf.BoolValue":
		return "*bool"
	case "google.protobuf.FloatValue":
		return "*float32"
	case "google.protobuf.DoubleValue":
		return "*float64"
	}
	return "*" + shortName(typeName)
}

func shortName(fq string) string {
	if i := strings.LastIndexByte(fq, '.'); i >= 0 {
		return fq[i+1:]
	}
	return fq
}
