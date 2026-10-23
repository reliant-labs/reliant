// File: internal/codegen/inject_gen.go
//
// The GENERATED injector — the keep-in-sync half of the hybrid DI model
// (FORGE_SHAPE_REDESIGN §2, the Google-Wire shape). GenerateInject emits
// internal/app/inject_gen.go: a forge-owned, regenerated-every-run
// `Build(infra *Infra) (*Services, error)` that constructs every
// registered component (service / worker / operator / internal package)
// in TYPE-topological order and calls each New(Deps) resolving every Deps
// field BY TYPE.
//
// This REPLACES the name-matching wire_gen resolution: where wire_gen
// matched a Deps field to `app.<FieldName>` by exact field name, Build
// matches a Deps field to a producer component or an Infra field by TYPE
// (using the same build_topo core + DepsAssignabilityMatcher the additive
// wave landed). The KEY distinction from the prior aborted pass: this
// injector is GENERATED, not scaffold-once — adding/removing a component
// is a regenerate, never a hand-edit.
//
// Resolution per Deps field, in priority order:
//
//  1. PRODUCER — another already-constructed component whose ServiceTypeKey
//     (package-qualified `<pkg>.Service`, pointer-tolerant) matches the
//     field's declared type. Drawn as a build_topo edge; emitted as the
//     producer's local var.
//  2. INFRA FIELD — a field on the owned *Infra struct (providers.go)
//     assignable to the Deps field type, proven at GENERATE time by the
//     DepsAssignabilityMatcher loaded over (internal/app, component) in
//     one packages.Load universe. A concrete *db.PostgresRepository on
//     Infra fills a narrow audit.Repository field (closes the constraint-3
//     silent-drop). Emitted as `infra.<Field>`.
//  3. CONVENTIONAL — Logger -> infra.Log, Config -> infra.Cfg; and a field
//     typed as a config block the loaded Config composes
//     (`Cfg *config.TraderConfig`) -> infra.Cfg.GetTrader()
//     (compose_config_blocks.go).
//  4. MISSING — a required (non `forge:optional-dep`), non-scalar Deps
//     field that resolves to none of the above is LOUD:
//       (a) GENERATE-TIME: collected into MissingProvider; GenerateInject
//           returns an error naming the missing TYPE + the consuming
//           component + the Deps field, when the matcher could PROVE the
//           Infra struct has no assignable field.
//       (b) COMPILE-TIME backstop: when assignability is merely UNPROVEN
//           (project mid-edit / not type-checking — matcher policy in
//           deps_assignability.go), Build still emits `infra.<Field>` so
//           the Go compiler arbitrates. It NEVER emits a silent typed-zero
//           for a required field.
//
// Scalar Deps fields (string/int/bool/duration) are CONFIGURATION, not
// collaborators — they take the typed-zero with a config-block hint and never
// raise a MissingProvider error. The hint's remedy, a config block, is the
// by-type case in 3.
//
// Each ServiceTypeKey is constructed exactly once (one local var per
// BuildPlan.Order entry) — per-binary singleton by construction.

package codegen

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"regexp"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/templates"
)

// composeField is one field on the Components struct (one row of the typed
// bag): the exported field name + its concrete component type.
type composeField struct {
	FieldName string
	Alias     string
	FieldType string
}

// InjectComponentData is one component's rendered inputs for the NewComponents
// construction body: the import line, the constructor selector, and the
// ordered Deps-literal assignments resolved by type.
type InjectComponentData struct {
	// FieldName is the exported field on *Services (e.g. "Billing",
	// "SvcBilling") — shared with the inventory and bootstrap naming.
	FieldName string
	// VarName is the lower-camel base name (e.g. "billing").
	VarName string
	// LocalVar is the local variable Build binds the constructed instance
	// to — VarName + "Inst" so it never shadows the package import alias.
	LocalVar string
	// Alias is the import alias for the component's package.
	Alias string
	// ImportPath is the module-relative import path (e.g.
	// "internal/handlers/billing").
	ImportPath string
	// Package is the Go package clause (for the constructor selector and
	// doc comments).
	Package string
	// Constructor is the component's entry-point func name — `New`, or the
	// `// forge:constructor`-marked name the author chose instead. Read
	// through [InjectComponentData.ConstructorName] so a literal built
	// without it still emits the canonical shape.
	Constructor string
	// Fallible reports whether the constructor returns (T, error). Build
	// wraps the error with the component name when true; assigns directly
	// otherwise.
	Fallible bool
	// Wrapped is true when the package declares the OWNED observability
	// wrapper (the generated middleware decorator — see observe_wrap.go) and the
	// constructor returns the contract interface. The construction is then
	// emitted as `c.X = pkg.<MiddlewareCtor>(pkg.New(...))` (or the fallible
	// two-step equivalent).
	Wrapped bool
	// MiddlewareCtor is the generated middleware wrapper's exact constructor
	// name (codegen.ResolveMiddlewareWrapper — keyed off the constructor's
	// concrete return type, e.g. "NewServiceWithForgeMiddleware"), so the emitted
	// `<Alias>.<MiddlewareCtor>(...)` call matches the generated declaration.
	// Only read when Wrapped is true.
	MiddlewareCtor string
	// Assignments are the per-Deps-field key/value pairs, in Deps
	// declaration order, for the New(Deps{...}) literal.
	Assignments []InjectAssignment
	// DepsKeys is every key the component's `Deps{…}` literal may legally
	// name, and DepsFound reports whether the Deps struct was positively read
	// (see DepsLiteralKeys). Template-invisible: only the reconciler's
	// stale-key drop reads them, and only when DepsFound is true.
	DepsKeys  map[string]bool
	DepsFound bool
}

// ConstructorName is the constructor selector compose.go must emit for this
// component: the detected name, or the canonical `New` when the field was
// never populated. Templates call `{{.ConstructorName}}` so an unset field can
// never render an empty selector.
func (d InjectComponentData) ConstructorName() string {
	if d.Constructor == "" {
		return DefaultConstructorName
	}
	return d.Constructor
}

// InjectAssignment is one `Field: Expr,` line in a New(Deps{...}) literal.
type InjectAssignment struct {
	Field   string
	Expr    string
	Comment string
}

// MissingProvider records a required Deps field that resolved to no
// producer and no PROVEN-assignable Infra field. GenerateInject returns
// an error built from these (see the file header's two-tier loudness).
type MissingProvider struct {
	// Component is the consuming component's FieldName.
	Component string
	// Field is the Deps field name with no provider.
	Field string
	// Type is the declared field type with no provider.
	Type string
}

// Framework func-provider seam expressions (Fix: Clock/IDGen saga). A Deps
// field typed exactly `func() time.Time` gets the real clock; one typed
// exactly `func() string` gets a ULID-backed id generator. These are wired
// BY TYPE so a service can depend on a deterministic clock / id source in
// its own logic and tests without the author hand-rolling the injection
// (the injection that cost the forge-one-shot build ~138 messages). Both
// are framework-guaranteed non-nil, so the field never needs a
// `//forge:optional-dep` marker or per-use nil-guard. Matched by exact
// pretty-printed type string; the emitted expression is unique enough to
// also drive the `time` / ulid import gating (see the caller).
const (
	frameworkClockType = "func() time.Time"
	frameworkClockExpr = "time.Now"
	frameworkIDGenType = "func() string"
	frameworkIDGenExpr = "func() string { return ulid.Make().String() }"
)

// Framework HTTP-client seam (outbound instrumentation). A required Deps
// field typed exactly `*http.Client` (the adapter/client scaffold shape)
// resolves to `infra.DefaultClient()` — the scaffold-once providers.go
// method returning a fresh client over the SHARED instrumented base
// transport (otelhttp: OTel client spans + W3C propagation + one pool).
// Same override story as Clock/IDGen: an Infra field NAMED after the Deps
// field wins (exact-name path), and compose.go is owned so any wiring is a
// one-line edit. Projects scaffolded before DefaultClient existed get a
// LOUD compile error naming the missing method; the adapter/observability
// skills carry the paste-in (never a silent uninstrumented client).
const (
	frameworkHTTPClientType = "*http.Client"
	frameworkHTTPClientExpr = "infra.DefaultClient()"
)

// Framework STORE seam. `internal/db/store_gen.go` gives every entity's
// generated CRUD an interface shape — `db.<Entity>Store` per entity, plus the
// aggregate `db.Store` — so a service can name persistence as a Deps field
// instead of hand-writing an interface and a passthrough adapter over the ORM
// free functions.
//
// That type is only useful if it RESOLVES. Without this seam the generator
// reports `Pipeline.Deps.Estimates (db.EstimateStore) has no provider` and
// tells the author to add an Infra field and construct "your implementation" —
// which is the hand-written adapter layer the generated store exists to
// delete. Measured: two forge-one-shot runs wrote 723 and 464 lines of exactly
// that, and generating the type without wiring it would have left them writing
// it anyway.
//
// Every store is constructible from ONE thing forge already owns — the ORM
// client on Infra — so there is nothing for the author to supply and no
// decision to defer: db.NewStore(infra.ORM) for the aggregate, and the
// per-entity accessor off it for a narrow dep. Same override story as
// Clock/IDGen/HTTPClient: an Infra field NAMED after the Deps field wins via
// the exact-name path, and compose.go is owned, so any bespoke wiring stays a
// one-line edit.
//
// The store lives in the ORM package (database.orm.package, default
// internal/db), so the seam is spelled with THAT package's name: a consumer
// importing internal/store declares `store.Store`, and a `db.Store` in that
// project is the project's own internal/db, which the seam must not claim.
type storeSeam struct {
	pkg       string            // the ORM package's name, e.g. "db"
	dir       string            // its project-relative directory, e.g. "internal/db"
	accessors map[string]string // qualified per-entity store type → accessor
}

// aggregateType is the qualified aggregate store type, e.g. `db.Store`.
func (s storeSeam) aggregateType() string { return s.pkg + ".Store" }

// aggregateExpr is the compose expression for the aggregate store.
func (s storeSeam) aggregateExpr() string { return s.pkg + ".NewStore(infra.ORM)" }

// source names the generated file, for the compose comment.
func (s storeSeam) source() string { return "generated store (" + s.dir + "/store_gen.go)" }

// owns reports whether depType is a store type the seam provides.
func (s storeSeam) owns(depType string) bool {
	if depType == s.aggregateType() {
		return true
	}
	_, ok := s.accessors[depType]
	return ok
}

// entityExpr returns the compose expression for a per-entity store dep —
// `db.NewStore(infra.ORM).<Plural>()`. It returns "" for any type that is
// not a generated per-entity store, so an unrelated `db.`-qualified
// interface never picks up a spurious provider.
//
// The accessor set is read from the GENERATED file rather than inferred from
// the type name. A name match alone would resolve `db.WidgetStore` in a
// project with no Widget entity and emit code that does not compile; and the
// accessor spelling is the pluralizer's output, which this package must not
// re-derive.
func (s storeSeam) entityExpr(depType string) string {
	plural, ok := s.accessors[depType]
	if !ok {
		return ""
	}
	return s.aggregateExpr() + "." + plural + "()"
}

// storeAccessorRE matches one accessor line in the generated store interface:
//
//	Estimates() EstimateStore
var storeAccessorRE = regexp.MustCompile(`^\t([A-Z]\w*)\(\) ([A-Z]\w*Store)$`)

// parseStoreSeam reads the ORM package's store_gen.go and returns the seam:
// a map of qualified dep type (`db.EstimateStore`) → accessor name
// (`Estimates`), under the ORM package's name.
//
// Reading the generated file is deliberate: it is the only place that knows
// BOTH which entities exist and what the pluralizer named their accessors, so
// a rename or a new entity flows through with no second source to keep in
// sync. An absent file (a project with no entities, or one generated before
// stores existed) yields no accessors, and every store dep then falls through
// to the ordinary missing-provider path.
func parseStoreSeam(projectDir string) storeSeam {
	orm := ProjectORMPackage(projectDir)
	seam := storeSeam{pkg: orm.PackageName(), dir: orm.PackageDir()}
	raw, err := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(seam.dir), "store_gen.go"))
	if err != nil {
		return seam
	}
	seam.accessors = map[string]string{}
	inAggregate := false
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case strings.HasPrefix(line, "type Store interface {"):
			inAggregate = true
			continue
		case inAggregate && strings.HasPrefix(line, "}"):
			inAggregate = false
		}
		if !inAggregate {
			continue
		}
		if m := storeAccessorRE.FindStringSubmatch(line); m != nil {
			seam.accessors[seam.pkg+"."+m[2]] = m[1]
		}
	}
	return seam
}

// InjectGenData is the rendered template input for compose.go.tmpl.
type InjectGenData struct {
	Module            string
	NeedsConfigImport bool
	// NeedsFmt gates the `fmt` import: it is only referenced in the fallible
	// (New returns error) construction branch, so a project with no fallible
	// component (incl. the zero-component case) must not import it or the
	// generated file fails to compile on an unused import.
	NeedsFmt bool
	// NeedsTime / NeedsULID gate the `time` and `github.com/oklog/ulid/v2`
	// imports for the framework Clock / IDGen seam. Only set when at least
	// one component's Deps carries a `func() time.Time` / `func() string`
	// field the seam fills, so a project that uses neither never imports
	// them (unused-import build failure).
	NeedsTime bool
	NeedsULID bool
	// NeedsDBStore gates the ORM package import for a generated-store dep;
	// DBStoreImport is that package's import path.
	NeedsDBStore  bool
	DBStoreImport string
	// Fields is the Components struct field set (one per component, typed as
	// its concrete handler/worker/operator type), in stable FieldName order.
	Fields []composeField
	// Order is the topo-sorted construction sequence (producers first).
	Order []InjectComponentData
	// HasCycle / CycleEdges drive the two-phase setter stub block.
	HasCycle   bool
	CycleEdges []BuildEdge
	// ProjectDir is the project root. Template-invisible: the reconciler
	// reads it to find the Deps struct of a package compose.go still imports
	// but the discovered set no longer holds (composeDropStaleDepsKeys), and
	// to read that package's composition markers (composeStaleConstructions).
	// Empty disables both reads.
	ProjectDir string
	// Graph is set when the composition graph (GenerateGraph) owns
	// compose.go: a construction there is a new<X> method, not a Components
	// field, so composeStaleConstructions names that remedy instead.
	// Template-invisible.
	Graph bool
}

// GenerateCompose emits internal/app/compose.go: the EXPLICIT per-binary
// component construction site (the Components typed bag + NewComponents) that
// REPLACES the retired generated injector (inject_gen.go + app_services_gen.go).
// It constructs every registered component in TYPE-topological order and fills
// each Deps field BY TYPE — from another constructed component, from a field on
// the owned *Infra struct (providers.go), or from the conventional
// Logger/Config sources.
//
// Returns an error listing every MissingProvider when a required collaborator
// field resolves to no producer and the matcher PROVES the Infra struct has no
// assignable field.
//
// This is the live composition path: cmd-server composes OpenInfra →
// NewComponents → mount via the typed Mount<Svc> methods + AllWorkers /
// AllOperators. There is no by-type injector and no *Services god-struct.
func GenerateCompose(in InjectGenInput) error {
	composeRel := filepath.Join("internal", "app", "compose.go")
	composeAbs := filepath.Join(in.ProjectDir, composeRel)
	if disownedFilePresent(in.Checksums, composeRel, composeAbs) {
		return nil
	}

	comps, err := assembleBuildComponents(in)
	if err != nil {
		return err
	}
	comps, notComponents := composedComponents(in.ProjectDir, comps)
	// A not-a-component package is one the server does not have. A server
	// component that requires it by type makes that claim false, and no
	// provider forge could pick is right: refuse before planning, naming the
	// marker, instead of reporting "no provider" and steering the author to
	// put a CLI collaborator on the server's Infra.
	if consumed := notAComponentConsumers(comps, notComponents); len(consumed) > 0 {
		return notAComponentConsumedError(in.ProjectDir, consumed)
	}
	// Never emit a constructor call that cannot compile. A package whose
	// constructor compose.go cannot call is refused here, named, before
	// compose.go is written — not composed anyway and left for the validate
	// build to blame on compose.go (compose_constructible.go).
	if bad := unconstructibleComponents(in.ProjectDir, comps); len(bad) > 0 {
		return unconstructibleError(bad)
	}
	// No len(comps)==0 early-return: cmd/server.go imports internal/app
	// unconditionally, so the package must exist and compile even with zero
	// components. The template renders a valid empty NewComponents over an
	// empty Components bag in that case.

	appDir := filepath.Join(in.ProjectDir, "internal", "app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		return err
	}

	// The owned Infra surface and the generated-store accessors are read
	// BEFORE planning: producer inference defers to an explicit Infra binding
	// and to the framework store seam, so it needs both.
	infraFields, err := parseInfraFields(appDir)
	if err != nil {
		return fmt.Errorf("parse internal/app for Infra fields: %w", err)
	}
	store := parseStoreSeam(in.ProjectDir)

	// Exact-contract-key resolution, layered with go/types inference so a
	// consumer-declared narrow interface (or an alias) resolves to the one
	// component that satisfies it — see producer_assignability.go.
	baseResolver := NewServiceKeyResolver(comps)
	inferred, ambiguous := inferInterfaceProducers(in.ProjectDir, comps, baseResolver, infraFields, store)
	if len(ambiguous) > 0 {
		return ambiguousProviderError(ambiguous)
	}
	resolver := &inferringResolver{base: baseResolver, inferred: inferred}
	plan := ComputeBuildPlan(comps, resolver)

	// Producer lookup: FieldName -> the Go EXPRESSION a consumer's Deps
	// literal should name for that producer.
	//
	// For a plain producer that is the local var, suffixed "Inst" so it never
	// shadows the component's package import alias (var `item` would shadow
	// import `item`, making a later `item.X` in the same function refer to the
	// value, not the package).
	//
	// For an INSTRUMENTED producer it is `c.<FieldName>` — the field holding
	// the DECORATED value. The raw instance is the wrong answer twice over: in
	// the fallible shape it compiles and silently routes every call around the
	// observability chain the package opted into, and in the non-fallible shape
	// (`c.X = pkg.NewSvcWithForgeMiddleware(pkg.New(...))`) no `xInst` local is
	// ever declared, so naming it does not compile. Construction order is
	// type-topological, so the producer's `c.<FieldName> =` always precedes its
	// consumers.
	producerVar := map[string]string{}
	for _, c := range comps {
		if c.compWrapped {
			producerVar[c.FieldName] = "c." + c.FieldName
			continue
		}
		producerVar[c.FieldName] = injectVarName(c.VarName)
	}

	// infraFields (parsed above) is the Infra struct's exported fields — the
	// owned provider set, from internal/app/providers.go (+ any sibling .go
	// that declares `type Infra struct`). Empty when providers.go hasn't been
	// scaffolded yet (first generate) — every collaborator then falls to the
	// compile-time backstop, which is the correct loud state.
	matcher := NewInfraAssignabilityMatcher(in.ProjectDir)

	// Config fields (pkg/config/config.go) — used to resolve a scalar Deps
	// field that names a typed config value from infra.Cfg.<field> instead
	// of a bare typed-zero (FIX: kalshi's WTI EIAKey/FREDKey were being
	// reset to ""+TODO). Empty when config.go hasn't been generated yet.
	configFields := parseConfigFields(in.ProjectDir)
	// The component config blocks the loaded Config composes, so a Deps
	// field typed as one (`Cfg *config.TraderConfig`) is filled from it by
	// type (compose_config_blocks.go).
	blocks := parseConfigBlocks(in.ProjectDir, in.ModulePath)

	var (
		rendered     []InjectComponentData
		missing      []MissingProvider
		needsConfig  bool
		needsFmt     bool
		needsTime    bool
		needsULID    bool
		needsDBStore bool
	)

	for _, c := range plan.Order {
		rc := InjectComponentData{
			FieldName:      c.FieldName,
			VarName:        c.VarName,
			LocalVar:       injectVarName(c.VarName),
			Alias:          c.Alias,
			ImportPath:     c.ImportPath,
			Package:        c.compPackage,
			Constructor:    c.compConstructor,
			Fallible:       c.compFallible,
			Wrapped:        c.compWrapped,
			MiddlewareCtor: c.compMiddlewareCtor,
			DepsKeys:       c.DepsKeys,
			DepsFound:      c.DepsFound,
		}
		if c.compFallible {
			needsFmt = true
		}
		for _, df := range c.Deps {
			needsConfig = needsConfig || df.Name == "Config"
			expr, comment, miss := resolveInjectField(df, c, producerVar, resolver, infraFields, configFields, blocks, matcher, in.RoleRoot(c), store)
			if miss != nil {
				missing = append(missing, *miss)
			}
			// Gate the time / ulid imports off the framework Clock / IDGen
			// seam. The emitted expressions are unique constants, so an exact
			// match uniquely identifies a seam fill (Infra / config / producer
			// exprs are `infra.*` / literals, never these).
			switch {
			case expr == frameworkClockExpr:
				needsTime = true
			case expr == frameworkIDGenExpr:
				needsULID = true
			case strings.HasPrefix(expr, store.aggregateExpr()):
				// Both store forms — the aggregate and a per-entity accessor
				// off it — are spelled <pkg>.NewStore(...), so one prefix
				// gates the ORM package import for either.
				needsDBStore = true
			}
			rc.Assignments = append(rc.Assignments, InjectAssignment{
				Field:   df.Name,
				Expr:    expr,
				Comment: comment,
			})
		}
		rendered = append(rendered, rc)
	}

	if len(missing) > 0 {
		return missingProviderError(missing, unconsumedPackages(comps, plan.Edges))
	}

	// Fields: the Components struct rows, one per component typed as its
	// concrete handler/worker/operator type, in stable FieldName order so the
	// file is byte-stable.
	fieldComps := make([]BuildComponent, len(comps))
	copy(fieldComps, comps)
	sort.Slice(fieldComps, func(i, j int) bool { return fieldComps[i].FieldName < fieldComps[j].FieldName })
	fields := make([]composeField, 0, len(fieldComps))
	for _, c := range fieldComps {
		ft := c.compFieldType
		if ft == "" {
			ft = "*" + c.Alias + ".Service"
		}
		fields = append(fields, composeField{FieldName: c.FieldName, Alias: c.Alias, FieldType: ft})
	}

	data := InjectGenData{
		Module:            in.ModulePath,
		NeedsConfigImport: needsConfig,
		NeedsFmt:          needsFmt,
		NeedsTime:         needsTime,
		NeedsULID:         needsULID,
		NeedsDBStore:      needsDBStore,
		DBStoreImport:     in.ModulePath + "/" + store.dir,
		Fields:            fields,
		Order:             rendered,
		HasCycle:          plan.HasCycle(),
		CycleEdges:        plan.CycleEdges,
		ProjectDir:        in.ProjectDir,
	}

	content, err := templates.ProjectTemplates().Render("compose.go.tmpl", data)
	if err != nil {
		return fmt.Errorf("render compose.go.tmpl: %w", err)
	}
	// SCAFFOLD-ONCE for the INITIAL emit; RECONCILED afterwards. compose.go is
	// the OWNED component-construction site, so forge re-derives only the
	// PROJECTION and never the bytes the author owns. It has to re-derive
	// something: a service added AFTER the initial scaffold is missing from the
	// Components bag while the regenerated mounts_services.go references
	// c.<FieldName> for it → `c.X undefined`, and a Deps field REMOVED after
	// the scaffold leaves a key that names nothing → `unknown field` — both
	// build breaks a write-once file could never heal. So reconcile
	// (reconcileComposeAddComponents) injects a missing component's import +
	// field + construction, fills a Deps assignment a component gained, drops a
	// key whose field the Deps struct no longer declares, and converges
	// wholesale only when nothing in the file is the author's. Every pass is
	// gated on the file still being in forge shape and on the result
	// gofmt-parsing as valid Go; anything else leaves the file byte-for-byte
	// untouched (the historical write-once behavior).
	existing, rerr := os.ReadFile(composeAbs)
	switch {
	case rerr == nil:
		reconciled, changed := reconcileComposeAddComponents(string(existing), string(content), data)
		if changed {
			if werr := writeUserScaffold(composeAbs, []byte(reconciled)); werr != nil {
				return fmt.Errorf("update internal/app/compose.go: %w", werr)
			}
		}
		for _, w := range composeStaleConstructions(reconciled, data) {
			fmt.Fprintf(in.warnings(), "  ⚠️  %s\n", w)
		}
		return nil
	case os.IsNotExist(rerr):
		if _, werr := writeForgeScaffoldOnce(in.ProjectDir, composeRel, content); werr != nil {
			return fmt.Errorf("write internal/app/compose.go: %w", werr)
		}
		return nil
	default:
		return fmt.Errorf("read internal/app/compose.go: %w", rerr)
	}
}

// composeComponentBlock renders exactly ONE component's construction block for
// injection into an existing compose.go — the New(Deps{...}) call (fallible or
// not) and the `c.<FieldName> = …` assignment. It mirrors compose.go.tmpl's
// per-component range body byte-for-byte; the shared gofmt pass in
// reconcileComposeAddComponents normalizes the whitespace, so the two stay in
// lockstep on output even if the raw indentation drifts.
var composeComponentBlock = template.Must(template.New("composeComponentBlock").Parse(
	`{{if .Fallible}}{{.LocalVar}}, err := {{.Alias}}.{{.ConstructorName}}({{.Alias}}.Deps{
{{range .Assignments}}{{.Field}}: {{.Expr}},{{if .Comment}} // {{.Comment}}{{end}}
{{end}}})
if err != nil {
return nil, fmt.Errorf("construct {{.FieldName}}: %w", err)
}
c.{{.FieldName}} = {{if .Wrapped}}{{.Alias}}.{{.MiddlewareCtor}}({{.LocalVar}}){{else}}{{.LocalVar}}{{end}}
{{else}}c.{{.FieldName}} = {{if .Wrapped}}{{.Alias}}.{{.MiddlewareCtor}}({{end}}{{.Alias}}.{{.ConstructorName}}({{.Alias}}.Deps{
{{range .Assignments}}{{.Field}}: {{.Expr}},{{if .Comment}} // {{.Comment}}{{end}}
{{end}}}){{if .Wrapped}}){{end}}
{{end}}`))

// reconcileComposeAddComponents keeps an existing compose.go in sync with the
// discovered component set in two ADDITIVE ways:
//
//   - a component in `data` ABSENT from the file gets its import + Components
//     field + NewComponents construction injected (F3: `forge scaffold service`);
//   - a component ALREADY in the file that has GAINED a Deps assignment (e.g.
//     the `DB orm.Context` field the first entity adds, resolved by type to
//     `infra.ORM`) gets that assignment injected into its New(Deps{…}) literal
//     (F4: a stale compose.go must not block the by-type wiring).
//
// It returns the updated source and whether anything changed. It is
// deliberately conservative: it only edits a file still in recognizable forge
// shape, never rewrites a component that is already fully wired, never
// duplicates an assignment the construction already sets, and ABORTS (returns
// the input unchanged) if the result does not gofmt-parse as valid Go. That
// last guard is the safety net — a bad anchor can only ever no-op, never emit
// broken Go.
func reconcileComposeAddComponents(existing, freshRender string, data InjectGenData) (string, bool) {
	// The stale-key drop runs FIRST and unconditionally, so every path below
	// sees a file whose Deps literals name only fields that exist. It is not
	// one of the additive passes and is not gated by their guards: a dead key
	// breaks the build in the cycle shape and in a hand-customized file too,
	// and those are exactly the files the additive passes decline to touch. Its
	// result also feeds the converge checks — a pristine file whose only
	// divergence from the fresh render WAS the dead key becomes a subset again
	// and converges wholesale.
	existing, dropped := composeDropStaleDepsKeys(existing, data)
	out, changed := reconcileComposeAddComponentsAfterDrop(existing, freshRender, data)
	if !changed {
		out, changed = existing, dropped
	}
	// Last, so it also corrects a construction an additive pass kept as it
	// was: an existing component whose instrument decision changed.
	if synced, wrapChanged := composeSyncWrapping(out, data); wrapChanged {
		return synced, true
	}
	return out, changed
}

func reconcileComposeAddComponentsAfterDrop(existing, freshRender string, data InjectGenData) (string, bool) {
	// Guard: recognizable forge shape only. A disowned / hand-rewritten
	// compose.go (no Components struct, no NewComponents return anchor) is left
	// to the user — the loud `c.X undefined` is the correct signal there.
	structOpen := strings.Index(existing, "type Components struct {")
	if structOpen < 0 || !strings.Contains(existing, "func NewComponents(infra *Infra)") {
		return existing, false
	}
	// A dependency cycle turns the emit into a two-phase setter shape that is
	// too bespoke to append into safely; leave it for the user.
	if data.HasCycle {
		return existing, false
	}

	// ── Identity flip: converge to the fresh render ──────────────────────
	// The additive passes below can only ADD a missing component or a gained
	// Deps assignment; they cannot RENAME a component ALREADY in the file. But
	// collision-prefixing (ResolveCollisionNaming) is retroactive: registering
	// a new sibling package that shares a component's Go package name renames
	// the ALREADY-EMITTED component's import alias AND Components field
	// (handler `widget`/`Widget` → `svcWidget`/`SvcWidget` the moment the
	// `internal/widget` domain package the RPC-vertical sweep creates is
	// discovered). Every OTHER, wholesale-regenerated consumer already moved to
	// the new identity (mounts_services.go's `c.SvcWidget` / `MountSvcWidget`,
	// pkg/app/testing.go's `WithSvcWidgetDeps`), so leaving compose.go on the
	// stale identity is a guaranteed build break — `undefined: svcWidget` here
	// (additive re-adds a block under the flipped alias but skips its import,
	// whose path already exists under the old alias) and `c.SvcWidget
	// undefined` over there. Additive patching also cannot fix construction
	// ORDER when the new sibling is a PRODUCER the existing consumer must be
	// built after. So when an identity flip is present we converge to the
	// freshly-rendered file, which carries the correct aliases, topological
	// order, and by-type wiring in one shot — provably the same bytes a
	// from-scratch generate on the final component set would write. Guarded to
	// fire only when no hand-added component would be lost.
	if flip, safe := composeIdentityFlipped(existing, data); flip {
		if !safe {
			return existing, false
		}
		formatted, err := format.Source([]byte(freshRender))
		if err != nil {
			return existing, false
		}
		return string(formatted), true
	}

	// ── Pristine-stale converge: existing ⊆ freshRender ──────────────────
	// The additive passes below can ADD a MISSING component or, via Pass B,
	// a gained Deps assignment — but Pass B's anchor only finds the Deps
	// literal in the NON-fallible `c.X = pkg.New(pkg.Deps{…})` shape; in the
	// fallible shape (`xInst, err := pkg.New(pkg.Deps{…})` … `c.X = xInst`)
	// the literal PRECEDES the `c.X =` assignment it searches from, so a
	// newly-required by-type assignment (the `DB: infra.ORM` the first entity
	// adds to a fallible handler) is never retrofitted and compose.go
	// constructs the handler with a nil DB — a runtime crash the compiler
	// misses. So when the existing file carries NOTHING the fresh render
	// lacks (no hand-added component, Deps assignment, import, or custom
	// code) yet DIFFERS from it, converge wholesale to the bytes a
	// from-scratch generate writes — provably the same result, with the
	// correct by-type wiring in one shot. Gated on the subset check: a file
	// with ANY line the fresh render lacks is a customization and is left
	// byte-for-byte to the additive/preserve path below.
	if composeExistingSubsetOfFresh(existing, freshRender) {
		formatted, err := format.Source([]byte(freshRender))
		if err != nil {
			return existing, false
		}
		return string(formatted), true
	}
	// The struct's closing brace: the first line that is exactly "}" after the
	// struct opener.
	structClose := indexClosingBrace(existing, structOpen)
	if structClose < 0 {
		return existing, false
	}
	// NewComponents' terminal `return c, nil`.
	retIdx := strings.Index(existing, "\treturn c, nil")
	if retIdx < 0 {
		retIdx = strings.Index(existing, "return c, nil")
	}
	if retIdx < 0 {
		return existing, false
	}

	fieldTypeByName := make(map[string]string, len(data.Fields))
	for _, f := range data.Fields {
		fieldTypeByName[f.FieldName] = f.FieldType
	}

	var (
		fieldLines []string // Components struct rows to add
		ctorBlocks []string // NewComponents construction blocks to add
		imports    []string // component import lines to add
		anyFmt     bool
	)
	for _, c := range data.Order {
		// Already wired? The `c.<FieldName> =` assignment is unique to this
		// component in compose.go (the space+`=` disambiguates prefixes).
		if strings.Contains(existing, "c."+c.FieldName+" =") {
			continue
		}
		ft := fieldTypeByName[c.FieldName]
		if ft == "" {
			ft = "*" + c.Alias + ".Service"
		}
		fieldLines = append(fieldLines, "\t"+c.FieldName+" "+ft)

		var buf bytes.Buffer
		if err := composeComponentBlock.Execute(&buf, c); err != nil {
			return existing, false
		}
		ctorBlocks = append(ctorBlocks, buf.String())
		if c.Fallible {
			anyFmt = true
		}

		imp := c.Alias + ` "` + data.Module + "/" + c.ImportPath + `"`
		if !strings.Contains(existing, `"`+data.Module+"/"+c.ImportPath+`"`) {
			imports = append(imports, "\t"+imp)
		}
	}
	out := existing
	changed := false

	// ── Pass A: inject entirely-missing components ────────────────────────
	if len(fieldLines) > 0 {
		// 1. Component import lines (+ any std/pkg imports the new blocks need).
		var importAdds []string
		importAdds = append(importAdds, imports...)
		if anyFmt && !importsContain(out, `"fmt"`) {
			importAdds = append(importAdds, "\t\"fmt\"")
		}
		if len(importAdds) > 0 {
			out = injectBeforeImportClose(out, strings.Join(importAdds, "\n"))
		}

		// Recompute anchors on the mutated string.
		structOpen = strings.Index(out, "type Components struct {")
		structClose = indexClosingBrace(out, structOpen)
		if structClose < 0 {
			return existing, false
		}
		// 2. Components struct fields — before the struct's closing brace.
		out = out[:structClose] + strings.Join(fieldLines, "\n") + "\n" + out[structClose:]

		// 3. Construction blocks — before `return c, nil`.
		retIdx = strings.Index(out, "\treturn c, nil")
		if retIdx < 0 {
			retIdx = strings.Index(out, "return c, nil")
		}
		if retIdx < 0 {
			return existing, false
		}
		out = out[:retIdx] + strings.Join(ctorBlocks, "\n") + "\n" + out[retIdx:]
		changed = true
	}

	// ── Pass B: inject assignments a PRESENT component has gained ─────────
	// (the DB dep the first entity adds is the motivating case). A component
	// injected in Pass A already carries every assignment, so it is skipped.
	for _, c := range data.Order {
		if strings.Contains(existing, "c."+c.FieldName+" =") { // present before this run
			if updated, did := composeInjectMissingAssignments(out, c); did {
				out = updated
				changed = true
			}
		}
	}

	if !changed {
		return existing, false
	}

	// Framework Clock / IDGen seam imports: an injected assignment may
	// reference `time.Now` or `ulid.Make` (the by-type Clock/IDGen fills).
	// format.Source below is gofmt-only — it never ADDS imports — so add
	// them here, content-driven, when a seam expression is present and the
	// import block doesn't already carry it. injectBeforeImportClose is a
	// no-op when the import is already present or there is no block.
	if strings.Contains(out, frameworkClockExpr) && !importsContain(out, `"time"`) {
		out = injectBeforeImportClose(out, "\t\"time\"")
	}
	if strings.Contains(out, "ulid.Make") && !importsContain(out, "oklog/ulid/v2") {
		out = injectBeforeImportClose(out, "\t\"github.com/oklog/ulid/v2\"")
	}

	// Safety net: only accept the edit if it gofmt-parses as valid Go. A bad
	// anchor can then only no-op, never write broken source.
	formatted, err := format.Source([]byte(out))
	if err != nil {
		return existing, false
	}
	return string(formatted), true
}

// composeDropStaleDepsKeys removes, from each `<alias>.Deps{…}` literal in an
// existing compose.go whose alias names a package of this project — a
// discovered component, or one compose.go still constructs after it left the
// discovered set — every key that is not a field of that package's Deps
// struct: the ONE subtractive edit the reconciler is licensed to make, and the
// argument for why it is licensed.
//
// WHAT COMPOSE.GO OWNS. The banner calls compose.go user-owned and additively
// reconciled, and the additive passes take that literally: they only ever add.
// But the ownership line does not run around the Deps literal, it runs THROUGH
// it. The author owns the EXPRESSION on the right of each key — a hand-wired
// provider, a carved cross-edge — and every statement around the construction.
// The author does NOT own the KEY SET: Go fixes it to the field set of
// `<pkg>.Deps`, which lives in the component's own package. The keys are a
// projection of that struct exactly as the component set is a projection of the
// discovered components, and forge already treats them so in one direction
// (Pass B fills a key the struct GAINED). A projection maintained in one
// direction only is not a projection; it is an accumulation that can never be
// brought back to its source, and that is the whole defect: remove a field from
// a Deps struct and compose.go keeps constructing it, so the project stops
// compiling and every later `forge generate` re-runs the same additive passes
// and leaves the dead key exactly where it was. A generate that cannot heal a
// state generate created is the failure; the observed recovery was deleting the
// file.
//
// WHY THIS IS DERIVED, NOT DESTRUCTIVE. The evidence comes from the Deps struct,
// never from compose.go. A key outside that struct's field set cannot compile
// against ANY providers.go, ANY Infra, ANY hand-edit — there is no program in
// which it means anything. So there is nothing an author could have owned there,
// and the drop is exactly as derivable as Pass B's add. It also means the two
// cases that look different — a field the author REMOVED, and a key hand-added
// for a field that never existed — get the SAME answer: the Deps struct is the
// sole arbiter of the key set, forge holds no record of provenance, and neither
// line has ever compiled.
//
// THE GUARDS, which are what keep this from becoming a licence to delete:
//
//   - KEYS ONLY. Never an expression, never a statement, never an import. A key
//     that still exists is left byte-for-byte, hand-wired value included.
//   - PROVEN key set only. DepsFound must be true: DepsLiteralKeys returns an
//     empty set both for a Deps struct with no fields and for a package that
//     did not parse, and acting on the second would empty every literal in the
//     file. Mid-edit source is skipped, never acted on.
//   - FULL key set. DepsKeys carries the embedded and unexported fields the
//     WIRING set (Assignments) drops, so a key forge does not emit but the
//     author may legally write is never mistaken for a dead one.
//   - PROVEN identity. The alias must import, in this file, the very path the
//     discovered set assigns it. A retroactive collision rename can point an
//     alias at a DIFFERENT component than it named at emit time, and checking
//     one component's literal against another's field set is precisely the
//     destruction this must not do. A package outside the discovered set is
//     read from the path this file's own import names — the path Go types the
//     literal by — never from a name match.
//   - AST-located and gofmt-verified. Only a real KeyValueExpr key at the top
//     level of that literal is a candidate, and the whole edit is discarded
//     unless the result parses.
//
// The key set and the fresh render's assignments come from the same parse of
// the same file in the same run, so the drop can never remove a key the run
// itself wants to add. Positional literals (`pkg.Deps{a, b}`) carry no keys and
// are left alone: forge never emits one and repairing one is not derivable.
func composeDropStaleDepsKeys(existing string, data InjectGenData) (string, bool) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "compose.go", existing, parser.SkipObjectResolution)
	if err != nil {
		return existing, false
	}
	aliasPath := composeImportAliases(file)

	// The literals we may touch, and the key set each is measured against.
	keysByAlias := map[string]map[string]bool{}
	for _, c := range data.Order {
		if !c.DepsFound {
			continue
		}
		if aliasPath[c.Alias] != data.Module+"/"+c.ImportPath {
			continue
		}
		keysByAlias[c.Alias] = c.DepsKeys
	}
	// Any OTHER project package compose.go constructs is measured the same
	// way, against the Deps struct its import path names. A package leaves
	// the discovered set while compose.go — user-owned, and only ever added
	// to — keeps constructing it: marked //forge:external-component or
	// //forge:not-a-component, excluded, its contract.go deleted. Go still
	// fixes that literal's key set to the struct's fields, so a removed field
	// leaves a key that breaks the build exactly as it would for a discovered
	// component, and this drop is the only pass that can heal it (#747).
	// Identity is proven the same way: the alias is resolved through THIS
	// file's own import, which is the path Go types the literal by. Read
	// lazily, so only an alias that keys a Deps literal costs a parse.
	resolved := map[string]map[string]bool{}
	keysFor := func(alias string) (map[string]bool, bool) {
		if keys, ok := keysByAlias[alias]; ok {
			return keys, true
		}
		if keys, ok := resolved[alias]; ok {
			return keys, keys != nil
		}
		var keys map[string]bool
		if dir, ok := projectPackageDir(data, aliasPath[alias]); ok {
			if k, found := DepsLiteralKeys(dir); found {
				keys = k
			}
		}
		resolved[alias] = keys
		return keys, keys != nil
	}

	type cut struct{ start, end int }
	var cuts []cut
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Deps" {
			return true
		}
		alias, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		keys, ok := keysFor(alias.Name)
		if !ok {
			return true
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || keys[key.Name] {
				continue
			}
			start, end := widenDepsElementCut(existing,
				fset.Position(kv.Pos()).Offset, fset.Position(kv.End()).Offset)
			cuts = append(cuts, cut{start: start, end: end})
		}
		return true
	})
	if len(cuts) == 0 {
		return existing, false
	}
	sort.Slice(cuts, func(i, j int) bool { return cuts[i].start > cuts[j].start })
	out := existing
	lastStart := len(existing)
	for _, c := range cuts {
		if c.end > lastStart { // widening overlapped an already-applied cut
			continue
		}
		out = out[:c.start] + out[c.end:]
		lastStart = c.start
	}
	formatted, ferr := format.Source([]byte(out))
	if ferr != nil {
		return existing, false
	}
	return string(formatted), true
}

// composeImportAliases maps each import alias in file to its import path, as
// the file spells it: the explicit name, else the path's last element.
func composeImportAliases(file *ast.File) map[string]string {
	aliasPath := map[string]string{}
	for _, imp := range file.Imports {
		path, uerr := strconv.Unquote(imp.Path.Value)
		if uerr != nil {
			continue
		}
		alias := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			alias = imp.Name.Name
		}
		aliasPath[alias] = path
	}
	return aliasPath
}

// projectPackageDir returns the on-disk directory of importPath when it names
// a package of this project (the module path is its prefix), and false for
// anything else — the standard library, a dependency, an unknown project.
func projectPackageDir(data InjectGenData, importPath string) (string, bool) {
	if data.ProjectDir == "" || data.Module == "" {
		return "", false
	}
	rel, ok := strings.CutPrefix(importPath, data.Module+"/")
	if !ok || rel == "" {
		return "", false
	}
	return filepath.Join(data.ProjectDir, filepath.FromSlash(rel)), true
}

// composeStaleConstructions returns one warning per project package that
// compose.go still constructs (a `<alias>.Deps{…}` literal) although its own
// marker now takes it out of the server's composition:
// //forge:external-component (the server has it, built by hand onto Infra) or
// //forge:not-a-component (the server does not have it).
//
// The construction is the author's code, so it is reported and never deleted:
// removing a Components field, its construction and its import is a judgment
// the reconciler cannot derive — something hand-written may read the field.
// composeDropStaleDepsKeys keeps such a construction compiling as its Deps
// struct changes; this tells the author it is now dead weight, and what to do.
func composeStaleConstructions(existing string, data InjectGenData) []string {
	if data.ProjectDir == "" {
		return nil
	}
	file, err := parser.ParseFile(token.NewFileSet(), "compose.go", existing, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	aliasPath := composeImportAliases(file)
	composed := make(map[string]bool, len(data.Order))
	for _, c := range data.Order {
		composed[data.Module+"/"+c.ImportPath] = true
	}
	seen := map[string]bool{}
	var warnings []string
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Deps" {
			return true
		}
		alias, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		path := aliasPath[alias.Name]
		if path == "" || composed[path] || seen[path] {
			return true
		}
		seen[path] = true
		dir, ok := projectPackageDir(data, path)
		if !ok {
			return true
		}
		rel := strings.TrimPrefix(path, data.Module+"/")
		composition, _ := internalPackageComposition(dir)
		if remedy := staleConstructionRemedy(composition, data.Graph); remedy != "" {
			warnings = append(warnings, fmt.Sprintf("internal/app/compose.go still constructs %s, which is now %s", rel, remedy))
		}
		return true
	})
	sort.Strings(warnings)
	return warnings
}

// staleConstructionRemedy is the marker a package's composition names and
// what to do about the construction compose.go still holds — "" when the
// composition does not take the package out of the server. graph selects the
// composition graph's wording, where a construction is a new<X> method and
// an instance the server has but does not build comes from a provider func.
func staleConstructionRemedy(composition string, graph bool) string {
	switch {
	case composition == CompositionExternal && graph:
		return "//forge:external-component — delete its new<X> method and import, and supply the instance from a New<X>(<X>Deps) provider func in internal/app/providers.go"
	case composition == CompositionExternal:
		return "//forge:external-component — delete its import, Components field and construction, and put the instance on Infra"
	case composition == CompositionNotAComponent && graph:
		return "//forge:not-a-component — delete its new<X> method and import: the server does not have it"
	case composition == CompositionNotAComponent:
		return "//forge:not-a-component — delete its import, Components field and construction: the server does not have it"
	}
	return ""
}

// widenDepsElementCut widens one Deps-literal element's byte range so removing
// it leaves clean source: it swallows the trailing comma, the trailing line
// comment forge emits alongside a resolved field (`Field: expr, // why`), the
// newline when the element ends its line, and the indentation when it starts
// it. Without the widening a drop would strand a comment describing a field
// that is gone.
func widenDepsElementCut(src string, start, end int) (int, int) {
	skipSpace := func(i int) int {
		for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
			i++
		}
		return i
	}
	if i := skipSpace(end); i < len(src) && src[i] == ',' {
		end = i + 1
	}
	if j := skipSpace(end); strings.HasPrefix(src[j:], "//") {
		if nl := strings.IndexByte(src[j:], '\n'); nl >= 0 {
			end = j + nl
		} else {
			end = len(src)
		}
	}
	if end < len(src) && src[end] == '\n' {
		// Take the newline only when the element also OWNS the line start,
		// so a drop never joins two surviving elements onto one line.
		k := start
		for k > 0 && (src[k-1] == ' ' || src[k-1] == '\t') {
			k--
		}
		if k == 0 || src[k-1] == '\n' {
			start, end = k, end+1
		}
	}
	return start, end
}

// composeIdentityFlipped reports whether reconciling `existing` toward the
// discovered set `data` would require CHANGING the identity (import alias +
// Components field) of a component ALREADY emitted in the file — the
// retroactive-collision case the additive passes cannot express (see the
// caller). The second return, safeToRerender, is true only when every
// component import path currently in the file is one the discovered set still
// produces: a stray import (a component the user hand-added to compose.go, or
// one dropped from config but left in the file) means a wholesale re-render
// could lose hand-owned wiring, so we fall back to leave-untouched.
func composeIdentityFlipped(existing string, data InjectGenData) (flip, safeToRerender bool) {
	dataPaths := make(map[string]bool, len(data.Order))
	for _, c := range data.Order {
		full := data.Module + "/" + c.ImportPath
		dataPaths[full] = true
		// Path already in the file, but NOT under the alias the discovered set
		// now assigns → an identity flip on an already-emitted component.
		if strings.Contains(existing, `"`+full+`"`) && !strings.Contains(existing, c.Alias+` "`+full+`"`) {
			flip = true
		}
	}
	if !flip {
		return false, false
	}
	for path := range composeComponentImportPaths(existing, data.Module) {
		if !dataPaths[path] {
			return true, false
		}
	}
	return true, true
}

// composeExistingSubsetOfFresh reports whether the existing compose.go is a
// pristine-but-stale forge render: it carries NOTHING the freshly-rendered
// file lacks, yet is NOT byte-identical to it. When true, converging existing
// to freshRender is provably lossless — it only replaces stale forge output
// with the bytes a from-scratch generate would write for the same discovered
// set. When false, existing holds at least one line the fresh render does not
// (a hand-added component, Deps assignment, import, or custom code) and MUST
// be left to the additive/preserve path so that customization survives.
//
// The comparison is line-set containment after gofmt-normalizing BOTH files
// and reducing each line to its whitespace-collapsed tokens: that absorbs
// indentation, import ordering, and struct-literal re-alignment (a wider
// field shifting a column never reads as a customization) so only a genuine
// difference in CONTENT — a token sequence the fresh render never emits —
// makes a line count as user-owned. A file that fails to gofmt-parse, or that
// already matches the fresh render, is not a converge candidate.
func composeExistingSubsetOfFresh(existing, freshRender string) bool {
	existFmt, err := format.Source([]byte(existing))
	if err != nil {
		return false
	}
	freshFmt, err := format.Source([]byte(freshRender))
	if err != nil {
		return false
	}
	if bytes.Equal(existFmt, freshFmt) {
		return false // already converged — nothing to change
	}
	// The set of content-lines the fresh render emits (whitespace-collapsed).
	freshLines := map[string]bool{}
	for _, ln := range strings.Split(string(freshFmt), "\n") {
		if norm := strings.Join(strings.Fields(ln), " "); norm != "" {
			freshLines[norm] = true
		}
	}
	// Every content-line the existing file carries must be one the fresh
	// render also carries; a single line it lacks is user content.
	for _, ln := range strings.Split(string(existFmt), "\n") {
		norm := strings.Join(strings.Fields(ln), " ")
		if norm == "" {
			continue
		}
		if !freshLines[norm] {
			return false
		}
	}
	return true
}

// composeComponentImportPaths returns the module-internal component import
// paths (handlers / packages / workers / operators) currently present in the
// compose.go source, scanned from its quoted import strings under
// `<module>/internal/`. The framework imports (fmt, pkg/config, …) are ignored
// because they never live under internal/.
func composeComponentImportPaths(existing, module string) map[string]bool {
	needle := `"` + module + "/internal/"
	out := map[string]bool{}
	rest := existing
	for {
		i := strings.Index(rest, needle)
		if i < 0 {
			break
		}
		start := i + 1 // past the opening quote
		end := strings.IndexByte(rest[start:], '"')
		if end < 0 {
			break
		}
		out[rest[start:start+end]] = true
		rest = rest[start+end+1:]
	}
	return out
}

// composeInjectMissingAssignments injects into component c's existing
// New(<alias>.Deps{ … }) literal any assignment from c.Assignments whose field
// key is not already set there. Used to retrofit a Deps field a component
// gained after compose.go was first scaffolded (the DB dep the first entity
// adds). Returns the updated source and whether it injected anything. No-ops
// (returns input, false) when the component's construction can't be located or
// its Deps literal is unbalanced.
func composeInjectMissingAssignments(content string, c InjectComponentData) (string, bool) {
	assignStart := strings.Index(content, "c."+c.FieldName+" =")
	if assignStart < 0 {
		return content, false
	}
	marker := c.Alias + ".Deps{"
	di := strings.Index(content[assignStart:], marker)
	if di < 0 {
		return content, false
	}
	depsOpen := assignStart + di + len(marker) // byte just after the '{'
	// Find the matching close brace of the Deps literal.
	depth := 1
	i := depsOpen
	for ; i < len(content) && depth > 0; i++ {
		switch content[i] {
		case '{':
			depth++
		case '}':
			depth--
		}
	}
	if depth != 0 {
		return content, false
	}
	block := content[depsOpen : i-1] // between the braces

	// Which field keys are already set? Compare against each line's trimmed
	// `Field:` prefix so "DB" never matches "DBPool".
	setKey := func(field string) bool {
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), field+":") {
				return true
			}
		}
		return false
	}
	var adds []string
	for _, a := range c.Assignments {
		if setKey(a.Field) {
			continue
		}
		line := "\t\t" + a.Field + ": " + a.Expr + ","
		if a.Comment != "" {
			line += " // " + a.Comment
		}
		adds = append(adds, line)
	}
	if len(adds) == 0 {
		return content, false
	}
	ins := "\n" + strings.Join(adds, "\n")
	return content[:depsOpen] + ins + content[depsOpen:], true
}

// indexClosingBrace returns the byte index of the first line consisting solely
// of "}" at or after `open` (a `type X struct {` position). Returns -1 when the
// struct is unterminated. Used to locate a struct's closing brace for field
// injection without a full AST parse.
func indexClosingBrace(content string, open int) int {
	if open < 0 {
		return -1
	}
	rest := content[open:]
	if i := strings.Index(rest, "\n}"); i >= 0 {
		return open + i + 1 // point at the '}' byte
	}
	return -1
}

// importsContain reports whether the compose.go import block already imports
// `needle` (a quoted path, or an unquoted path fragment). A plain substring
// test is sufficient here: the import block is small and forge-emitted, and a
// false positive only means we skip re-adding an import that is already there.
func importsContain(content, needle string) bool {
	return strings.Contains(content, needle)
}

// injectBeforeImportClose inserts `lines` just before the closing paren of the
// first `import (` block. No-ops (returns input) when there is no parenthesized
// import block.
func injectBeforeImportClose(content, lines string) string {
	impOpen := strings.Index(content, "import (")
	if impOpen < 0 {
		return content
	}
	closeRel := strings.Index(content[impOpen:], "\n)")
	if closeRel < 0 {
		return content
	}
	pos := impOpen + closeRel // index of the '\n' before ')'
	return content[:pos] + "\n" + lines + content[pos:]
}

// resolveInjectField resolves one Deps field to the Go expression Build
// should emit, following the priority order in the file header. The third
// return is non-nil when the field is a required collaborator with a
// PROVEN-missing provider (generate-time loud error).
func resolveInjectField(df DepsField, c BuildComponent, producerVar map[string]string, resolver TypeResolver, infraFields map[string]InfraField, configFields map[string]InfraField, blocks configBlocks, matcher *InfraAssignabilityMatcher, roleRoot string, store storeSeam) (expr, comment string, miss *MissingProvider) {
	// 1. PRODUCER — another component produces this type (by-type edge).
	if prodField := resolver.Resolve(c, df.Type); prodField != "" && prodField != c.FieldName {
		if v, ok := producerVar[prodField]; ok {
			return v, "in-process " + df.Type, nil
		}
	}

	// 3. CONVENTIONAL — Logger / Config from Infra. (Checked before the
	// generic Infra-field path so the canonical sources stay stable
	// regardless of whether the user renamed the Infra Log/Cfg fields.)
	switch df.Name {
	case "Logger":
		return "infra.Log", "", nil
	case "Config":
		return "infra.Cfg", "", nil
	}

	// 3b. CONFIG BLOCK (by TYPE) — a Deps field typed as a component config
	// block the loaded Config composes (`Cfg *config.TraderConfig`) is that
	// block: infra.Cfg.GetTrader(). This is the shape forge-config-deps asks
	// for in place of naked scalars, so it must resolve rather than fall to
	// a missing provider. Checked before the Infra paths because Cfg, the
	// conventional field name, is also Infra's whole-config field, and an
	// exact-name match there would hand the block field the root
	// *config.Config. An Infra field of the very same type still wins: that
	// is an explicit override.
	if expr, ok := blocks.resolve(df.Type, c.compImports); ok {
		if f, named := infraFields[df.Name]; !named || f.Type != df.Type {
			return expr, "", nil
		}
	}

	// 2.5 FRAMEWORK SEAM (by TYPE) — Clock / IDGen / HTTP client. A required
	// Deps field typed exactly `func() time.Time` gets `time.Now`; one typed
	// `func() string` gets a ULID id generator; one typed `*http.Client`
	// gets `infra.DefaultClient()` (the instrumented outbound default — see
	// the seam const block). These are framework-provided
	// and guaranteed non-nil so a service can inject a deterministic clock /
	// id source for testability without hand-wiring the composition (the
	// injection that caused the Clock/NewID DI saga). Checked BEFORE the
	// generic Infra-by-assignability block so a mid-write Infra surface can
	// never emit a spurious `infra.<FuncField>` backstop for it; an author
	// who wants a custom impl declares an Infra field of the SAME NAME as the
	// Deps field (handled by the exact-name Infra path below) or disowns
	// compose.go. Optional func fields are intentionally left to the
	// nil/optional tail — the marker says "nil is acceptable".
	if _, hasInfraName := infraFields[df.Name]; !hasInfraName && !df.Optional {
		switch df.Type {
		case frameworkClockType:
			return frameworkClockExpr, "framework clock", nil
		case frameworkIDGenType:
			return frameworkIDGenExpr, "framework id generator", nil
		case store.aggregateType():
			// Every entity's store, bound to the ORM client Infra already owns.
			return store.aggregateExpr(), store.source(), nil
		case frameworkHTTPClientType:
			// Instrumented default client (see the seam const block). The
			// generic Infra-by-assignability path below would also find a
			// user-declared *http.Client Infra field — but the canonical
			// provider is the method, and the exact-name gate above already
			// honors an explicit same-name override.
			return frameworkHTTPClientExpr, "instrumented default client (providers.go DefaultClient)", nil
		}
		// A per-entity generated store (db.<Entity>Store). Not a switch case
		// because the set is per-project: it is read from the generated
		// store file, so a type that merely LOOKS like one in a project
		// without that entity still falls through to missing-provider.
		if expr := store.entityExpr(df.Type); expr != "" {
			return expr, store.source(), nil
		}
	}

	// 2. INFRA FIELD — an Infra field assignable to df.Type. Prefer a
	// proven assignable field; fall back to an exact-name Infra field as
	// the compile-time backstop.
	if field, kind := matcher.ResolveInfraField(roleRoot, c.compImportLeaf, df.Name, df.Type, infraFields); field != "" {
		switch kind {
		case MatchAssignable, MatchExactString:
			return "infra." + field, "", nil
		case MatchUnavailable:
			// Compile-time backstop: emit infra.<Field> and let the Go
			// compiler arbitrate (never a silent typed-zero for a required
			// field). Same deterministic fail-loud policy as wire_gen.
			return "infra." + field, "compile-time backstop (assignability unproven)", nil
		case MatchUnprovenBackstop:
			// GENERATE-ORDERING backstop: no Infra field name-matches AND none
			// could be PROVEN assignable because internal/app is mid-write
			// this run (e.g. it references the not-yet-regenerated Build). An
			// Infra field named differently from this Deps field may well be
			// assignable on the next clean load. Emitting infra.<DepsField>
			// (the matcher returns the Deps field name here) defers the
			// decision to the Go compiler: loud if genuinely absent, silently
			// correct once a clean generate proves the assignable match —
			// instead of a spurious generate-time MissingProvider. Crucially
			// this NEVER emits a silent typed-zero for a required field.
			return "infra." + field, "compile-time backstop (generate-ordering: Infra surface mid-write)", nil
		}
	}

	// Scalar fields are configuration, not collaborators. When a scalar
	// Deps field corresponds to a typed field on infra.Cfg (matching name +
	// compatible scalar type), resolve it from infra.Cfg.<field> — config
	// IS the producer for configuration. Only when no config field maps does
	// it fall back to the typed-zero with the config-block hint. This is what
	// keeps a service's `EIAKey string` / `FREDKey string` wired to the
	// config value instead of being silently reset to "" + TODO.
	if zeroValueLiteral(df.Type) != "nil" {
		if field, ok := matchScalarConfigField(df, configFields); ok {
			return "infra.Cfg." + field, "from config", nil
		}
		return zeroValueLiteral(df.Type), scalarConfigHint(df, c), nil
	}

	// Optional collaborator with no provider: typed nil, silent (the user
	// opted into "may be nil"). Required: typed nil + loud MissingProvider.
	if df.Optional {
		return "nil", "optional — no provider", nil
	}
	return "nil", "TODO: no provider for " + df.Type,
		&MissingProvider{Component: c.FieldName, Field: df.Name, Type: df.Type}
}

// parseConfigFields returns the exported fields of the generated Config
// struct (pkg/config/config.go), keyed by field name. Reuses parseInfraFields'
// AST walk by reading the config dir and matching the `Config` struct. Returns
// an empty map when config.go hasn't been generated yet — every scalar then
// falls to the typed-zero, the prior behavior.
func parseConfigFields(projectDir string) map[string]InfraField {
	out, err := parseStructFields(filepath.Join(projectDir, "pkg", "config"), "Config")
	if err != nil {
		return map[string]InfraField{}
	}
	return out
}

// matchScalarConfigField reports the Config field name that fills a scalar
// Deps field, if any. The match is by EXACT field name plus scalar-type
// compatibility (so a `MaxRetries int` Deps field maps to a `MaxRetries int32`
// config field, and a `Timeout time.Duration` maps to a duration config
// field). Returning the config field name lets the caller emit
// `infra.Cfg.<field>`. Conventional bare-Deps names (Logger/Config)
// never reach here — they're resolved earlier.
func matchScalarConfigField(df DepsField, configFields map[string]InfraField) (string, bool) {
	cf, ok := configFields[df.Name]
	if !ok {
		return "", false
	}
	if !scalarTypesCompatible(df.Type, cf.Type) {
		return "", false
	}
	return cf.Name, true
}

// scalarTypesCompatible reports whether a scalar Deps field of type want can
// be filled from a config field of type have. Exact-string equality is the
// common case; the integer family (int / int32 / int64) is treated as
// compatible because proto-derived config ints land as int32 while a service
// Deps field idiomatically declares int. time.Duration only matches itself.
func scalarTypesCompatible(want, have string) bool {
	if want == have {
		return true
	}
	intFamily := map[string]bool{"int": true, "int32": true, "int64": true}
	if intFamily[want] && intFamily[have] {
		return true
	}
	return false
}

// scalarConfigHint mirrors wire_gen's unresolvedDepHint for the scalar
// case: a scalar Deps field is configuration and belongs in a config
// block, not the Infra provider set.
func scalarConfigHint(df DepsField, c BuildComponent) string {
	return fmt.Sprintf("TODO: %s is configuration — declare a config block (see forge architecture skill)", df.Name)
}

// missingProviderError builds the loud generate-time error naming every
// required collaborator field with no provider, with the exact remediation
// (add an assignable field to Infra in internal/app/providers.go).
//
// unconsumed maps the FieldName of every internal package nothing in the
// server consumes to its import path (unconsumedPackages). A missing provider
// on one of those gets a second remedy: the package may not belong to the
// server at all. That is a hint, never a decision — "no server component
// requires it yet" is also how a package looks before its consumer is
// written, so forge names the marker and leaves the call to the author.
func missingProviderError(missing []MissingProvider, unconsumed map[string]string) error {
	sort.Slice(missing, func(i, j int) bool {
		if missing[i].Component != missing[j].Component {
			return missing[i].Component < missing[j].Component
		}
		return missing[i].Field < missing[j].Field
	})
	var b strings.Builder
	b.WriteString("the explicit component construction site (internal/app/compose.go) has Deps fields with no provider.\n\n")
	b.WriteString("Each required collaborator must resolve to either another registered component (by its Service interface type) or a field on the owned *Infra struct (internal/app/providers.go). forge will NOT emit a nil for a custom-interface dep (that passes validateDeps and nil-derefs at runtime), so the following must be hand-wired:\n\n")
	var notServer []string
	seen := map[string]bool{}
	for _, m := range missing {
		writeMissingProviderRemedy(&b, m)
		if path, ok := unconsumed[m.Component]; ok && !seen[path] {
			seen[path] = true
			notServer = append(notServer, path)
		}
	}
	b.WriteString("Then re-run `forge generate` — compose.go's NewComponents will fill the Deps field from the Infra field automatically (any field ASSIGNABLE to the dep type resolves it; the field name need not match).\n")
	if len(notServer) > 0 {
		sort.Strings(notServer)
		fmt.Fprintf(&b, "\nNo service, worker or operator requires %s. If the server does not use it — it is built by the CLI, a daemon or another binary — do not wire its Deps into the server's Infra. Mark the package instead:\n\n", strings.Join(notServer, ", "))
		b.WriteString("      //forge:not-a-component: <who constructs it>\n\n")
		b.WriteString("It keeps contract.go, mock_gen.go and its decorator; compose.go stops constructing it (`forge skill load contracts/package-markers`).\n")
	}
	return fmt.Errorf("%s", b.String())
}

// writeMissingProviderRemedy appends one collaborator's copy-paste-correct
// remediation: the exact Infra struct field to add, the OpenInfra line that
// constructs it, and how compose.go then wires it. Making the forced
// hand-wiring correct-by-construction is the SAFE alternative to silently
// emitting a nil the runtime would deref.
func writeMissingProviderRemedy(b *strings.Builder, m MissingProvider) {
	fmt.Fprintf(b, "  - %s.Deps.%s (%s) has no provider. Wire it by hand in internal/app/providers.go:\n\n", m.Component, m.Field, m.Type)
	// 1. Infra field. Suggest the Deps field name as the Infra field name and
	//    the dep type verbatim. A dep type declared in the consuming service's
	//    OWN package is written unqualified there (e.g. `Repository`); from the
	//    internal/app package it needs that package's selector — the note flags it.
	fmt.Fprintf(b, "      // 1. add a field to the Infra struct (import the package that declares %s):\n", m.Type)
	fmt.Fprintf(b, "      //    %s %s\n", m.Field, m.Type)
	// 2. OpenInfra construction.
	fmt.Fprintf(b, "      // 2. construct it in OpenInfra, assigning onto that field:\n")
	fmt.Fprintf(b, "      //    infra.%s = /* your %s implementation */\n", m.Field, m.Type)
	// 3. compose wiring (automatic).
	fmt.Fprintf(b, "      // 3. compose.go then wires it for you: NewComponents sets %s.Deps.%s = infra.%s\n\n", m.Component, m.Field, m.Field)
}

// injectVarName returns the local variable name Build uses for a
// component's constructed instance: the lower-camel base + "Inst". The
// suffix guarantees the var never shadows the component's package import
// alias (which equals the base name for single-word packages).
func injectVarName(base string) string { return base + "Inst" }

// serviceIfaceOrDefault returns the component's Service-contract interface
// name, defaulting to "Service" when the dir declares neither a `Service`
// type nor a `//forge:service`/`//forge:contract`-marked interface (a
// not-yet-scaffolded component, or a plain handler whose concrete type is
// `*Service`). Centralizes the "empty → Service" fallback so every
// BuildComponent field derives the same name.
func serviceIfaceOrDefault(dir string) string {
	if n := DetectServiceInterfaceName(dir); n != "" {
		return n
	}
	return "Service"
}

// qualifyConstructorType turns the package-local constructor return type
// (e.g. "*Service", "Service", "*Controller") into the alias-qualified form
// the internal/app package must reference (e.g. "*item.Service",
// "item.Service"). It inserts `<alias>.` before the leading type identifier,
// after any leading pointer stars. Empty ctorType (no parseable New, or a
// not-yet-scaffolded component) falls back to "*<alias>.<ifaceName>" — the
// bootstrap default, where ifaceName is the marked/canonical Service
// interface name so a role-named contract still gets a valid field type.
func qualifyConstructorType(ctorType, alias, ifaceName string) string {
	if ifaceName == "" {
		ifaceName = "Service"
	}
	t := strings.TrimSpace(ctorType)
	if t == "" {
		return "*" + alias + "." + ifaceName
	}
	stars := ""
	for strings.HasPrefix(t, "*") {
		stars += "*"
		t = strings.TrimSpace(t[1:])
	}
	// Already qualified (selector form pkg.Name) — leave as-is (rare:
	// a New that returns another package's type). Otherwise qualify.
	if strings.Contains(t, ".") {
		return stars + t
	}
	return stars + alias + "." + t
}

// GenerateProviders writes internal/app/providers.go ONCE — the owned
// Infra + OpenInfra (scaffold-once, never overwritten; os.Stat guard below).
// compose.go (NewComponents) wires each component's Deps INLINE off these
// Infra fields; the user grows this file as NewComponents reports missing
// providers.
func GenerateProviders(modulePath, databaseDriver string, ormEnabled bool, projectDir string) error {
	appDir := filepath.Join(projectDir, "internal", "app")
	path := filepath.Join(appDir, "providers.go")
	rel := filepath.Join("internal", "app", "providers.go")
	if !checksums.ScaffoldOnceDecision(projectDir, rel) {
		// Either the file is present, or the user deleted it deliberately.
		// The ORM retrofit below is an edit to an EXISTING file, so it must
		// only run when one is actually there — a deleted providers.go has
		// nothing to retrofit and must not be re-created to receive it.
		if _, err := os.Stat(path); err != nil {
			return nil // deliberately deleted — stays deleted
		}
		// Scaffold-once file present. One ADDITIVE retrofit: a project
		// scaffolded before its first entity has a providers.go with a
		// `DB *sql.DB` pool but NO `*orm.Client`. When the first entity turns
		// on the ORM (ormEnabled), the service's `DB orm.Context` dep has no
		// assignable Infra field (*sql.DB is not orm.Context) → build break.
		// Inject the ORM field + its OpenInfra construction so the by-type
		// wiring resolves. Guarded + gofmt-checked; any other shape is left
		// untouched (the historical write-once behavior).
		if ormEnabled {
			if err := ensureProvidersORMField(path); err != nil {
				return err
			}
		}
		return nil
	}
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		return err
	}
	data := struct {
		Module      string
		HasDatabase bool
		OrmEnabled  bool
	}{Module: modulePath, HasDatabase: databaseDriver != "", OrmEnabled: ormEnabled}
	content, err := templates.ProjectTemplates().Render("providers.go.tmpl", data)
	if err != nil {
		return fmt.Errorf("render providers.go.tmpl: %w", err)
	}
	if err := writeUserScaffold(path, content); err != nil {
		return err
	}
	checksums.RecordScaffold(projectDir, rel)
	return nil
}

// ensureProvidersORMField retrofits the `ORM *orm.Client` field + its OpenInfra
// construction into an existing (scaffold-once) providers.go that has a
// `DB *sql.DB` pool but no ORM client — the state of a project scaffolded
// before its first entity. It is a no-op unless the file is in recognizable
// forge shape (has the `DB *sql.DB` field, the `infra.DB = db` assignment, and
// the orm-client factory is genuinely absent), and it ABORTS (leaves the file
// byte-for-byte unchanged) if the injected result does not gofmt-parse. The
// orm client satisfies the orm.Context Deps field the generated CRUD needs.
func ensureProvidersORMField(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := string(raw)

	// Already has the ORM client, or isn't the DB-bearing forge shape → leave.
	if strings.Contains(content, "ORM *orm.Client") || strings.Contains(content, "orm.NewClientWithDB") {
		return nil
	}
	if !strings.Contains(content, "DB *sql.DB") || !strings.Contains(content, "infra.DB = db") {
		return nil
	}

	out := content
	// 1. Struct field: after the `DB *sql.DB` line.
	out = strings.Replace(out, "\tDB *sql.DB\n", "\tDB  *sql.DB\n\tORM *orm.Client\n", 1)

	// 2. OpenInfra construction: after `infra.DB = db`.
	ormBlock := "\n\tif db != nil {\n" +
		"\t\tormClient, err := orm.NewClientWithDB(db, \"postgres\")\n" +
		"\t\tif err != nil {\n" +
		"\t\t\treturn nil, fmt.Errorf(\"create ORM client: %w\", err)\n" +
		"\t\t}\n" +
		"\t\tinfra.ORM = ormClient\n" +
		"\t}"
	out = strings.Replace(out, "\tinfra.DB = db", "\tinfra.DB = db\n"+ormBlock, 1)

	// 3. orm import, if absent.
	if !strings.Contains(out, `"github.com/reliant-labs/forge/pkg/orm"`) {
		if impOpen := strings.Index(out, "import ("); impOpen >= 0 {
			if closeRel := strings.Index(out[impOpen:], "\n)"); closeRel >= 0 {
				pos := impOpen + closeRel
				out = out[:pos] + "\n\n\t\"github.com/reliant-labs/forge/pkg/orm\"" + out[pos:]
			}
		}
	}

	if out == content {
		return nil
	}
	formatted, ferr := format.Source([]byte(out))
	if ferr != nil {
		// Injection produced invalid Go — abort quietly, leave the file as-is.
		return nil
	}
	return writeUserScaffold(path, formatted)
}

// disownedFilePresent reports whether the project has taken permanent
// ownership of relPath and the file is still there.
//
// `forge project disown` is a ONE-WAY transfer of the bytes, so the answer is
// checked BEFORE any wiring is computed, not just before the write. compose.go
// resolution can fail the whole generate run with a MissingProvider error, and
// failing a run over a file forge will never write — one whose Deps the owner
// wires by hand precisely because forge could not — is the wrong outcome.
//
// The file must still exist: deleting a disowned file is how a project hands
// the path back, and the caller then re-scaffolds it.
func disownedFilePresent(cs *checksums.FileChecksums, relPath, absPath string) bool {
	if !cs.IsDisowned(filepath.ToSlash(relPath)) {
		return false
	}
	_, err := os.Stat(absPath)
	return err == nil
}

// lifeComp is one supervised component's row in the lifecycle.go projection:
// the stable label, the *Components field it reads, and (operators only) the
// package the AddToScheme installer comes from.
type lifeComp struct {
	Name       string
	FieldName  string
	Alias      string
	ImportPath string
	FieldType  string
}

// lifecycleData is the lifecycle.go.tmpl render input.
type lifecycleData struct {
	Module           string
	LeaderElectionID string
	Workers          []lifeComp
	Operators        []lifeComp
}

// GenerateLifecycle emits internal/app/lifecycle.go: the supervised-
// component surface (typed Worker<X>()/Operator<X>() accessors + AllWorkers /
// AllOperators / HasOperators / RunOperators) over the constructed *Components.
// Where mounts_services.go is the HTTP
// surface, this is the worker/operator surface the cmd layer registers onto
// serverkit.Server. Always written (no len==0 early-return) so cmd/server.go's
// references resolve even with zero supervised components.
func GenerateLifecycle(in InjectGenInput) error {
	lifecycleRel := filepath.Join("internal", "app", "lifecycle.go")
	lifecycleAbs := filepath.Join(in.ProjectDir, lifecycleRel)
	if disownedFilePresent(in.Checksums, lifecycleRel, lifecycleAbs) {
		return nil
	}

	comps, err := assembleBuildComponents(in)
	if err != nil {
		return err
	}
	comps, _ = composedComponents(in.ProjectDir, comps)
	// No len(comps)==0 early-return: cmd/server.go reads app.AllWorkers /
	// app.AllOperators / app.RunOperators over *Components, so lifecycle.go
	// must exist even with zero supervised components (the template emits
	// valid nil-returning AllWorkers/AllOperators and a generic RunOperators in
	// that case).

	var workers, operators []lifeComp
	for _, c := range comps {
		lc := lifeComp{Name: c.Name, FieldName: c.FieldName, Alias: c.Alias, ImportPath: c.ImportPath, FieldType: c.compFieldType}
		switch c.compRoleRoot {
		case "internal/workers":
			workers = append(workers, lc)
		case "internal/operators":
			operators = append(operators, lc)
		}
	}
	sort.Slice(workers, func(i, j int) bool { return workers[i].FieldName < workers[j].FieldName })
	sort.Slice(operators, func(i, j int) bool { return operators[i].FieldName < operators[j].FieldName })

	data := lifecycleData{
		Module:           in.ModulePath,
		LeaderElectionID: leaderElectionID(in.ModulePath),
		Workers:          workers,
		Operators:        operators,
	}

	content, err := templates.ProjectTemplates().Render("lifecycle.go.tmpl", data)
	if err != nil {
		return fmt.Errorf("render lifecycle.go.tmpl: %w", err)
	}
	// SCAFFOLD-ONCE for the INITIAL emit; ADDITIVELY RECONCILED afterwards —
	// the same two-tier treatment compose.go gets, and for the same reason.
	//
	// lifecycle.go is the OWNED supervised surface: RunOperators' body and the
	// OperatorEntry shape are real per-app policy (a downstream project extends
	// the entry with per-operator cache scoping and drops the AllOperators
	// aggregate outright), so forge must never re-render it wholesale. But the
	// per-component ACCESSORS are a pure projection of the discovered component
	// set, and the per-worker/operator subcommand forge scaffolds
	// (cmd/<bin>/cmd/workers/<name>.go) calls c.Worker<X>() directly — so a
	// worker added after the initial scaffold left the tree referencing an
	// accessor nobody ever wrote (`c.WorkerNightly undefined`). Reconciling
	// additively closes that without taking the file over.
	existing, rerr := os.ReadFile(lifecycleAbs)
	switch {
	case rerr == nil:
		reconciled, changed := reconcileLifecycleAddComponents(string(existing), string(content), data)
		if changed {
			if werr := writeUserScaffold(lifecycleAbs, []byte(reconciled)); werr != nil {
				return fmt.Errorf("update internal/app/lifecycle.go: %w", werr)
			}
		}
		return nil
	case os.IsNotExist(rerr):
		if _, werr := writeForgeScaffoldOnce(in.ProjectDir, lifecycleRel, content); werr != nil {
			return fmt.Errorf("write internal/app/lifecycle.go: %w", werr)
		}
		return nil
	default:
		return fmt.Errorf("read internal/app/lifecycle.go: %w", rerr)
	}
}

// lifecycleKitImport is the package every Worker<X>() accessor calls
// (lifecyclekit.WrapWorker) and nothing else in lifecycle.go does.
const lifecycleKitImport = "github.com/reliant-labs/forge/pkg/lifecyclekit"

// lifecycleWorkerAccessor / lifecycleOperatorAccessor render exactly ONE
// accessor for injection into an existing lifecycle.go. They mirror
// lifecycle.go.tmpl's per-component range bodies; the shared gofmt pass in
// reconcileLifecycleAddComponents normalizes whitespace, so the two stay in
// lockstep on output even if the raw indentation drifts.
var (
	lifecycleWorkerAccessor = template.Must(template.New("lifecycleWorkerAccessor").Parse(
		`
// Worker{{.FieldName}} adapts the constructed {{.Name}} worker. Each accessor
// welds the worker's stable label to its typed field via lifecyclekit.WrapWorker
// (a runtime type-switch that gives ctx-aware workers per-worker
// cancel-on-shutdown and leaves legacy Start/Stop workers on the legacy path).
func (c *Components) Worker{{.FieldName}}() serverkit.Worker {
return lifecyclekit.WrapWorker("{{.Name}}", c.{{.FieldName}})
}
`))

	lifecycleOperatorAccessor = template.Must(template.New("lifecycleOperatorAccessor").Parse(
		`
// Operator{{.FieldName}} adapts the constructed {{.Name}} controller: its CRD
// scheme installer ({{.Alias}}.AddToScheme) and the controller's manager hookup
// (c.{{.FieldName}}.SetupWithManager).
func (c *Components) Operator{{.FieldName}}() OperatorEntry {
return OperatorEntry{
Name:             "{{.Name}}",
AddToScheme:      {{.Alias}}.AddToScheme,
SetupWithManager: c.{{.FieldName}}.SetupWithManager,
}
}
`))
)

// reconcileLifecycleAddComponents keeps an existing lifecycle.go in sync with
// the discovered supervised set, ADDITIVELY:
//
//   - a worker/operator with no Worker<X>() / Operator<X>() accessor in the
//     file gets one appended (plus, for operators, the package import the
//     AddToScheme reference needs);
//   - the matching AllWorkers() / AllOperators() aggregate gains the new
//     entry, but ONLY when its body is still one of the two shapes forge
//     emits (the empty `_ = c; return nil` or a plain slice literal of
//     accessor calls). A body the user has rewritten into something else is
//     left exactly as found — the accessor still lands, so the per-component
//     subcommand compiles, and WHICH components the aggregate reports stays
//     the user's decision.
//
// Like the compose reconciler it is deliberately conservative: it only edits a
// file still in recognizable forge shape, never duplicates an accessor, and
// ABORTS (returning the input unchanged) if the result does not gofmt-parse as
// valid Go. A bad anchor can only ever no-op, never emit broken Go.
func reconcileLifecycleAddComponents(existing, freshRender string, data lifecycleData) (string, bool) {
	// Guard: recognizable forge shape only. Both aggregates are part of the
	// scaffold; a file missing them has been rewritten past what an additive
	// pass can reason about.
	if !strings.Contains(existing, "func (c *Components) AllWorkers() []serverkit.Worker") &&
		!strings.Contains(existing, "func (c *Components) AllOperators() []OperatorEntry") {
		return existing, false
	}

	// Pristine-stale converge: when the existing file carries NOTHING the
	// fresh render lacks, converging to the fresh render is provably lossless
	// — it only replaces stale forge output with the bytes a from-scratch
	// generate would write for the same discovered set. This is the fresh-
	// scaffold path (the file is the zero-component render of the same
	// template), and it gets the accessors, the aggregates, and the operator
	// imports in one shot with no anchor arithmetic at all.
	if composeExistingSubsetOfFresh(existing, freshRender) {
		formatted, err := format.Source([]byte(freshRender))
		if err != nil {
			return existing, false
		}
		return string(formatted), true
	}

	out := existing
	changed := false

	var (
		workerCalls   []string
		operatorCalls []string
		imports       []string
	)
	for _, w := range data.Workers {
		call := "c.Worker" + w.FieldName + "()"
		workerCalls = append(workerCalls, call)
		if strings.Contains(out, "func (c *Components) Worker"+w.FieldName+"()") {
			continue
		}
		var buf bytes.Buffer
		if err := lifecycleWorkerAccessor.Execute(&buf, w); err != nil {
			return existing, false
		}
		out = lifecycleInsertAccessor(out, "func (c *Components) AllWorkers() []serverkit.Worker {", buf.String())
		// The template imports lifecyclekit only alongside a worker, so the
		// first accessor added to a worker-less file brings the import too.
		if !strings.Contains(out, `"`+lifecycleKitImport+`"`) && !strings.Contains(strings.Join(imports, "\n"), lifecycleKitImport) {
			imports = append(imports, "\t\""+lifecycleKitImport+"\"")
		}
		changed = true
	}
	for _, o := range data.Operators {
		call := "c.Operator" + o.FieldName + "()"
		operatorCalls = append(operatorCalls, call)
		if strings.Contains(out, "func (c *Components) Operator"+o.FieldName+"()") {
			continue
		}
		var buf bytes.Buffer
		if err := lifecycleOperatorAccessor.Execute(&buf, o); err != nil {
			return existing, false
		}
		out = lifecycleInsertAccessor(out, "func (c *Components) AllOperators() []OperatorEntry {", buf.String())
		full := data.Module + "/" + o.ImportPath
		if !strings.Contains(out, `"`+full+`"`) {
			imports = append(imports, "\t"+o.Alias+` "`+full+`"`)
		}
		changed = true
	}
	if len(imports) > 0 {
		out = injectBeforeImportClose(out, strings.Join(imports, "\n"))
	}

	if updated, did := lifecycleSyncAggregate(out,
		"func (c *Components) AllWorkers() []serverkit.Worker {", "[]serverkit.Worker", workerCalls); did {
		out = updated
		changed = true
	}
	if updated, did := lifecycleSyncAggregate(out,
		"func (c *Components) AllOperators() []OperatorEntry {", "[]OperatorEntry", operatorCalls); did {
		out = updated
		changed = true
	}

	if !changed {
		return existing, false
	}
	// Safety net: only accept the edit if it gofmt-parses as valid Go.
	formatted, err := format.Source([]byte(out))
	if err != nil {
		return existing, false
	}
	return string(formatted), true
}

// lifecycleInsertAccessor splices one rendered accessor into content directly
// above the aggregate function named by `signature` (and above that function's
// doc comment) — the position lifecycle.go.tmpl itself emits accessors at.
//
// Placement matters for IDEMPOTENCE, not aesthetics: appending at EOF instead
// would leave the file one converge away from the template layout, so the next
// `forge generate` would rewrite it a second time and `generate ×2 produces no
// diff` would fail. Falls back to appending when the anchor is missing.
func lifecycleInsertAccessor(content, signature, block string) string {
	idx := strings.Index(content, signature)
	if idx < 0 {
		return content + block
	}
	// Walk back over the function's contiguous `//` doc-comment lines so the
	// accessor lands above the comment, not between it and its function.
	lineStart := strings.LastIndexByte(content[:idx], '\n') + 1
	for lineStart > 0 {
		prevEnd := lineStart - 1
		prevStart := strings.LastIndexByte(content[:prevEnd], '\n') + 1
		if !strings.HasPrefix(strings.TrimSpace(content[prevStart:prevEnd]), "//") {
			break
		}
		lineStart = prevStart
	}
	return content[:lineStart] + strings.TrimPrefix(block, "\n") + "\n" + content[lineStart:]
}

// lifecycleSyncAggregate rewrites one All* aggregate's body so it returns
// exactly `calls`, but only when the body is still one of the two shapes forge
// emits: the empty `_ = c` / `return nil` form, or a slice literal whose
// entries are all accessor calls. Anything else (a filter, a conditional, a
// hand-added cross-edge) is left untouched — the caller's accessors still
// landed, so the tree compiles either way.
func lifecycleSyncAggregate(content, signature, sliceType string, calls []string) (string, bool) {
	open := strings.Index(content, signature)
	if open < 0 {
		return content, false
	}
	bodyStart := open + len(signature)
	bodyEnd := indexClosingBrace(content, open)
	if bodyEnd < 0 || bodyEnd <= bodyStart {
		return content, false
	}
	body := content[bodyStart:bodyEnd]

	if !lifecycleAggregateIsForgeShape(body, sliceType) {
		return content, false
	}
	want := lifecycleAggregateBody(sliceType, calls)
	if strings.Join(strings.Fields(body), " ") == strings.Join(strings.Fields(want), " ") {
		return content, false
	}
	return content[:bodyStart] + want + content[bodyEnd:], true
}

// lifecycleAggregateIsForgeShape reports whether an All* body is still the
// shape forge emits — every non-blank line is either the `_ = c` /
// `return nil` empty form, the slice-literal open/close, or an accessor call
// row. One unrecognized line makes the body user-owned.
func lifecycleAggregateIsForgeShape(body, sliceType string) bool {
	for _, ln := range strings.Split(body, "\n") {
		ln = strings.TrimSpace(ln)
		switch {
		case ln == "", ln == "}", ln == "_ = c", ln == "return nil",
			ln == "return "+sliceType+"{", ln == "},":
			continue
		}
		// An accessor row: `c.WorkerX(),` / `c.OperatorX(),`.
		if strings.HasSuffix(ln, "(),") &&
			(strings.HasPrefix(ln, "c.Worker") || strings.HasPrefix(ln, "c.Operator")) {
			continue
		}
		return false
	}
	return true
}

// lifecycleAggregateBody renders the canonical All* body for `calls` — the
// nil-returning empty form when there are none, a slice literal otherwise.
func lifecycleAggregateBody(sliceType string, calls []string) string {
	if len(calls) == 0 {
		return "\n\t_ = c\n\treturn nil\n"
	}
	var b strings.Builder
	b.WriteString("\n\treturn " + sliceType + "{\n")
	for _, c := range calls {
		b.WriteString("\t\t" + c + ",\n")
	}
	b.WriteString("\t}\n")
	return b.String()
}

// ── BuildComponent assembly ──────────────────────────────────────────

// InjectGenInput carries everything GenerateInject needs to assemble the
// component set. Mirrors the bootstrap inputs so the two derive identical
// FieldName / alias values (one source of truth: ResolveCollisionNaming).
type InjectGenInput struct {
	GenContext
	Services  []ServiceDef
	Packages  []BootstrapPackageData
	Workers   []BootstrapWorkerData
	Operators []BootstrapOperatorData
	// Graph assembles the components for the composition graph, whose
	// generated files name identifiers of their own.
	Graph bool
	// Warnings receives GenerateCompose's advisory lines — a construction
	// compose.go keeps that the project no longer composes. Nil means
	// os.Stderr.
	Warnings io.Writer
}

// warnings returns where advisory lines go: Warnings, else os.Stderr.
func (in InjectGenInput) warnings() io.Writer {
	if in.Warnings != nil {
		return in.Warnings
	}
	return os.Stderr
}

// RoleRoot returns the role-root directory the assignability matcher loads
// the component's package from, keyed by the component's role. The role is
// encoded on the assembled BuildComponent (compRoleRoot).
func (in InjectGenInput) RoleRoot(c BuildComponent) string { return c.compRoleRoot }

// serviceTypeKey builds the FULL import-path-qualified Service key a
// component PRODUCES (e.g. "example.com/proj/internal/billing.Service").
// Keying by the full path (not the bare package clause) gives two
// same-clause packages — a domain `internal/billing` and a handler
// `internal/handlers/billing`, both `package billing` — distinct
// identities, so a consumer's domain dep can never mis-resolve to the
// handler instance. When the module path is unknown (synthetic inputs),
// fall back to the module-relative import path, which is still unique.
//
// ifaceName is the component's Service-contract interface name — "Service"
// for the canonical shape, or a `//forge:service`/`//forge:contract`-marked
// name (Gateway / Provider / …). Keying on the ACTUAL interface name is what
// lets a consumer's `pkg.Gateway` Deps field resolve to a marked producer
// by type; an empty name defaults to "Service" (back-compat).
func serviceTypeKey(modulePath, importPath, ifaceName string) string {
	if ifaceName == "" {
		ifaceName = "Service"
	}
	if modulePath == "" {
		return importPath + "." + ifaceName
	}
	return modulePath + "/" + importPath + "." + ifaceName
}

// assembleBuildComponents parses every component's Deps + disk-resolves
// its package identity into the []BuildComponent build_topo orders. The
// ServiceTypeKey each component PRODUCES is `<pkg>.Service` (the strict
// contract-name convention — one Service per component package), pointer-
// tolerant via the resolver. Conventional leaf workers/operators with no
// Service interface get ServiceTypeKey="" (they produce no edges).
func assembleBuildComponents(in InjectGenInput) ([]BuildComponent, error) {
	// Resolve service packages from disk once (import line + package
	// clause), shared with the collision counts — exactly as wire_gen does.
	svcResolved := make([]ResolvedComponent, 0, len(in.Services))
	for _, svc := range in.Services {
		res, err := ResolveServiceComponent(in.ProjectDir, svc.Name)
		if err != nil {
			return nil, err
		}
		svcResolved = append(svcResolved, res)
	}
	svcComponents := make([]BootstrapServiceData, 0, len(in.Services))
	for _, res := range svcResolved {
		svcComponents = append(svcComponents, BootstrapServiceData{Package: res.PackageName})
	}
	counts := CollisionCounts(svcComponents, in.Packages, in.Workers, in.Operators)
	// The names compose.go and lifecycle.go already use for something else;
	// a component whose import name is one of them is imported as
	// `<role><Package>` instead (component_import_alias.go).
	taken := appPackageNames(in.ProjectDir)
	for name := range appTemplateIdentifiers() {
		taken[name] = true
	}
	if in.Graph {
		for name := range graphTemplateIdentifiers() {
			taken[name] = true
		}
	}

	var comps []BuildComponent

	// Services.
	for i, svc := range in.Services {
		res := svcResolved[i]
		pkg := res.PackageName
		fallbackField := naming.ToPascalCase(strings.TrimSuffix(svc.Name, "Service"))
		if fallbackField == "" {
			fallbackField = naming.ToPascalCase(svc.Name)
		}
		alias, fieldName := ResolveCollisionNaming(pkg, fallbackField, "svc", counts)
		alias = appImportAlias(alias, "svc", taken)
		runtimeName := naming.ToKebabCase(strings.TrimSuffix(svc.Name, "Service"))
		if runtimeName == "" {
			runtimeName = naming.ToKebabCase(svc.Name)
		}
		deps, _ := ParseServiceDeps(res.Dir)
		depsKeys, depsFound := DepsLiteralKeys(res.Dir)
		fallible := false
		ctorType := ""
		ctorName := DefaultConstructorName
		if res.FromDisk {
			fallible, _ = DetectFallibleConstructor(res.Dir)
			ctorType, _ = DetectConstructorType(res.Dir)
			ctorName = DetectConstructorName(res.Dir)
		}
		// The produced Service-contract interface name — "Service" for the
		// canonical shape, or a `//forge:service`/`//forge:contract`-marked
		// name so a role-oriented interface still wires by type.
		ifaceName := serviceIfaceOrDefault(res.Dir)
		imports, _ := collectImports(res.Dir)
		importPath := "internal/handlers/" + res.ImportLeaf
		// Interface-returning constructor + an observe opt-in (the
		// `// forge:constructor` marker or the legacy observe_chain.go seam, and
		// no `// forge:no-observe`) → compose emits
		// pkg.<MiddlewareCtor>(pkg.New(...)), where MiddlewareCtor comes from the
		// SAME resolver the generator uses (keyed off the concrete return type).
		// Handler services return *Service, so ShouldInstrumentComponent keeps
		// them unwrapped (otelconnect owns the RPC edge).
		middlewareCtor, wrapped := composeWrapper(res.Dir, ctorType, ifaceName)
		comps = append(comps, BuildComponent{
			Name:               runtimeName,
			FieldName:          fieldName,
			VarName:            lowerFirst(fieldName),
			Alias:              alias,
			ImportPath:         importPath,
			ServiceTypeKey:     serviceTypeKey(in.ModulePath, importPath, ifaceName),
			Deps:               deps,
			DepsKeys:           depsKeys,
			DepsFound:          depsFound,
			compPackage:        pkg,
			compPackageKey:     pkg + "." + ifaceName,
			compImports:        imports,
			compConstructor:    ctorName,
			compFallible:       fallible,
			compRoleRoot:       "internal/handlers",
			compImportLeaf:     res.ImportLeaf,
			compFieldType:      qualifyConstructorType(ctorType, alias, ifaceName),
			compWrapped:        wrapped,
			compMiddlewareCtor: middlewareCtor,
		})
	}

	// Internal packages, workers, operators share the same shape.
	addRole := func(role string, role4 string, datas []BootstrapComponentData) error {
		for _, c := range datas {
			alias, fieldName := ResolveCollisionNaming(c.Package, c.FieldName, role4, counts)
			alias = appImportAlias(alias, role4, taken)
			compDir := filepath.Join(in.ProjectDir, role, filepath.FromSlash(c.ImportPath))
			deps, _ := ParseServiceDeps(compDir)
			depsKeys, depsFound := DepsLiteralKeys(compDir)
			ctorType, _ := DetectConstructorType(compDir)
			ifaceName := serviceIfaceOrDefault(compDir)
			imports, _ := collectImports(compDir)
			importPath := role + "/" + c.ImportPath
			// Interface-returning constructor + an observe opt-in (marker or
			// legacy seam, and no `// forge:no-observe`) → compose emits
			// pkg.<MiddlewareCtor>(pkg.New(...)), where MiddlewareCtor comes from
			// the SAME resolver the generator uses (keyed off the concrete return
			// type).
			middlewareCtor, wrapped := composeWrapper(compDir, ctorType, ifaceName)
			comps = append(comps, BuildComponent{
				Name:               c.Name,
				FieldName:          fieldName,
				VarName:            lowerFirst(fieldName),
				Alias:              alias,
				ImportPath:         importPath,
				ServiceTypeKey:     serviceTypeKey(in.ModulePath, importPath, ifaceName),
				Deps:               deps,
				DepsKeys:           depsKeys,
				DepsFound:          depsFound,
				compPackage:        c.Package,
				compPackageKey:     c.Package + "." + ifaceName,
				compImports:        imports,
				compConstructor:    DetectConstructorName(compDir),
				compFallible:       c.Fallible,
				compRoleRoot:       role,
				compImportLeaf:     c.ImportPath,
				compFieldType:      qualifyConstructorType(ctorType, alias, ifaceName),
				compWrapped:        wrapped,
				compMiddlewareCtor: middlewareCtor,
			})
		}
		return nil
	}
	if err := addRole("internal", "pkg", in.Packages); err != nil {
		return nil, err
	}
	if err := addRole("internal/workers", "wkr", in.Workers); err != nil {
		return nil, err
	}
	if err := addRole("internal/operators", "op", in.Operators); err != nil {
		return nil, err
	}

	return comps, nil
}

// composeWrapper decides whether compose.go constructs the component at dir
// through its generated decorator, and names the decorator's constructor. The
// component must be instrumented (ShouldInstrumentComponent) AND the decorator
// must already be declared in its middleware_gen.go: the contracts step writes
// it before compose is assembled, so in a normal generate the two agree, and a
// construction is never emitted against a symbol that does not exist yet (a
// compose assembled before its contracts step — a test, a partial run — stays
// unwrapped, and the next generate's composeSyncWrapping wraps it).
func composeWrapper(dir, ctorType, ifaceName string) (ctor string, wrapped bool) {
	if !ShouldInstrumentComponent(dir, ctorType, ifaceName) {
		return "", false
	}
	if ifaceName == "" {
		ifaceName = "Service"
	}
	w, declared := pinnedMiddlewareWrapper(dir, ifaceName)
	if !declared {
		return "", false
	}
	return w.Constructor, true
}
