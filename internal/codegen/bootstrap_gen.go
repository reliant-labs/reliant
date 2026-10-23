package codegen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/templates"
)

// BootstrapComponentData represents a bootstrappable component (service, package, worker, operator).
//
// For nested internal packages (e.g. internal/mcp/database/contract.go), Package
// is the leaf Go-identifier ("database"), while ImportPath carries the full
// path under internal/ ("mcp/database") used to construct the import line. For
// top-level packages, ImportPath is the same as Package. FieldName must remain
// unique across all packages, so for nested entries it should encode the full
// path (e.g. "McpDatabase") to avoid collisions with sibling leaves. VarName is
// the lowerCamel form of FieldName and is used in the bootstrap template as a
// unique prefix for local Go variables (e.g. "mcpDatabaseImpl"); using FieldName
// avoids collisions when two nested packages share a leaf name.
type BootstrapComponentData struct {
	Name       string // e.g. "api", "cache", "email_sender"
	Package    string // e.g. "api" (Go package identifier — leaf of ImportPath for nested entries)
	ImportPath string // e.g. "api" or "mcp/database" (path under internal/, workers/, etc.)
	FieldName  string // e.g. "API" or "McpDatabase" (exported struct field, must be unique)
	VarName    string // e.g. "api" or "mcpDatabase" (unique lowerCamel local-var prefix in bootstrap)
	Fallible   bool   // true if the constructor returns (T, error)
	// Constructor is the component's entry-point func name — `New` unless the
	// package moved the `// forge:constructor` marker onto a name of its own
	// (Open / Connect / NewReadOnly). Read through [BootstrapComponentData.ConstructorName]
	// so a literal built without it still emits the canonical shape.
	Constructor string
	// Alias is the import alias used in bootstrap.go for this component's
	// Go package. Defaults to Package when there are no cross-role
	// collisions; gets a role-prefixed value (e.g. "svcBilling",
	// "pkgBilling") when a service Package matches an internal package
	// Package (or other cross-role pair). All bootstrap.go references
	// to the package's exported symbols must use Alias rather than
	// Package so the alias-rewrite is observed at every call site.
	Alias string
	// HasWebhooks is true when this service has webhooks declared in
	// forge.yaml. The bootstrap template uses this to emit a
	// `RegisterWebhookRoutes(mux, stack)` call after `RegisterHTTP(...)`,
	// so generated webhook routes get mounted on the mux without the user
	// having to hand-edit the user-owned `RegisterHTTP` body in
	// handlers/<svc>/service.go. Only populated for services; ignored
	// for packages, workers, and operators.
	HasWebhooks bool
	// HasLogger / HasConfig record whether this component's Deps struct
	// declares a Logger / Config field. The bootstrap template gates the
	// emission of those Deps-literal fields on these flags so a package
	// that doesn't consume them isn't forced to carry vestigial Logger /
	// Config fields just to keep the generated New(Deps{...}) call site
	// type-checking. Populated by inspectComponentDepsShape before
	// rendering; defaults to false (skip) when the source dir can't be
	// parsed (e.g. just-scaffolded component with no Deps yet).
	HasLogger bool
	HasConfig bool
	// HasDB records whether this component's Deps struct declares a
	// `DB orm.Context` field. The test-harness template gates `DB: cfg.db`
	// on it, exactly as HasLogger/HasConfig gate their fields. Without it
	// NewTest<Package> built the interactor with a nil DB while the
	// sibling NewTest<Service> in the same generated file merged one in —
	// so an interactor test that touched the database nil-panicked inside
	// a DO NOT EDIT file the author cannot fix.
	HasDB bool
	// AppFieldRefs lists Deps fields (other than Logger/Config) whose
	// names AND types match an AppExtras field. Bootstrap emits one
	// assignment per entry like `<DepsField>: app.<DepsField>`. Without
	// this, audit.New got only {Logger} even when audit.Deps.Repo and
	// app.Repo both existed — the package silently degraded (Log warn-
	// and-drops) until the next forge generate cycle. wire_gen has had
	// this logic for services; this brings packages to parity.
	AppFieldRefs []AppFieldRef
	// CanonicalAppField names the single App/AppExtras field whose
	// declared type is this internal package's Service interface (e.g.
	// AppExtras.DaemonService of type svcdaemon.Service). Populated by
	// inspectComponentDepsShape ONLY when the package's Deps struct has
	// at least one non-optional collaborator field that bootstrap cannot
	// auto-wire from App/AppExtras (no name match, or proven type
	// mismatch) — i.e. the construction is unexpressible from app
	// fields. When set, the bootstrap template emits
	//
	//	app.Packages.<FieldName> = app.<CanonicalAppField>
	//
	// instead of `<pkg>.New(<pkg>.Deps{...})`: user-owned setup.go
	// constructs the canonical, fully-wired instance (with deps that
	// have no AppExtras representation — inline URL builders,
	// env-derived strings, cross-package collaborators), and appkit
	// runs Setup BEFORE the package table, so the alias always observes
	// the setup.go assignment. Constructing a second instance here
	// instead would produce a half-built duplicate that panics in
	// validateDeps or silently no-ops — the cp-forge svcdaemon
	// hand-edit class (Deps.DaemonRepo/URLBuilder unwireable, boot
	// panic "Deps.DaemonRepo is required").
	//
	// Empty when: every Deps field auto-wires (keep constructing — the
	// enforcement/Checker shape), the only gaps are config scalars
	// (zero value is the documented degraded mode — the billing/APIKey
	// shape), the gap is `forge:optional-dep`-marked, no App/AppExtras
	// field has the package's Service type, or more than one does
	// (ambiguous — no deterministic canonical instance).
	CanonicalAppField string
	// ConnectPkg is the import alias of the generated Connect package for
	// this service (e.g. "echov1connect") — used by the bootstrap template
	// to reference the `<X>ServiceName` constant when building the vanguard
	// REST routes. Only populated for services and only when
	// `api.rest: true`; empty for non-services or when REST is off.
	ConnectPkg string
	// ProtoServiceName is the PascalCase proto service identifier (e.g.
	// "EchoService") used to look up the `<ProtoServiceName>Name` constant
	// in the connect-generated package. Combined with ConnectPkg, the
	// bootstrap template emits `<connectPkg>.<ProtoServiceName>Name` as
	// the Connect service name handed to serverkit.RESTHandler.
	ProtoServiceName string
}

// AppFieldRef pairs a package Deps field name with the app.<name>
// expression bootstrap should emit for it. Only emitted when the
// AppExtras field type EXACTLY matches the Deps field type (otherwise
// the compile fails with funding.Repository vs *db.PostgresRepository
// style mismatches).
type AppFieldRef struct {
	DepsField string // e.g. "Repo"
}

// ConstructorName is the constructor selector templates must emit for this
// component: the detected name, or the canonical `New` when the field was
// never populated (hand-built literals in tests, pre-disk-resolution paths).
// Templates call `{{.ConstructorName}}` so an unset field can never render an
// empty selector.
func (d BootstrapComponentData) ConstructorName() string {
	if d.Constructor == "" {
		return DefaultConstructorName
	}
	return d.Constructor
}

// Type aliases for backward compatibility and readability.
type BootstrapServiceData = BootstrapComponentData
type BootstrapPackageData = BootstrapComponentData
type BootstrapWorkerData = BootstrapComponentData

// inspectComponentDepsShape walks each component's source directory under
// roleRoot (e.g. "internal", "workers", "operators") and populates
// HasLogger / HasConfig / AppFieldRefs from the parsed Deps struct +
// AppExtras AST. A missing source dir or unparseable file falls through
// to defaults: best-effort, errors-as-default.
//
// Mutates each component in place so callers don't have to thread a
// result slice through bootstrap data assembly.
func inspectComponentDepsShape(components []BootstrapComponentData, projectDir, roleRoot string) {
	// Resolve AppExtras field types once for the whole batch so each
	// component can name-and-type-match without re-parsing pkg/app.
	appFields, _ := ParseAppFields(filepath.Join(projectDir, "pkg", "app"))
	appFieldTypes := map[string]string{}
	for _, f := range appFields {
		appFieldTypes[f.Name] = f.Type
	}

	// Type-aware backstop for the legacy string-compare matcher.
	// When the pretty-printed type strings differ but go/types says
	// the AppExtras field is assignable to the Deps field (narrow-
	// interface implemented by wide concrete), we wire anyway. When
	// the matcher reports NameMismatch, we deliberately DO NOT wire —
	// the post-generate bootstrap-deps-coverage lint surfaces the
	// gap loudly rather than silently dropping (this was the audit-
	// no-op bug class).
	matcher := NewDepsAssignabilityMatcher(projectDir)

	// Module path feeds canonicalAppServiceField's exact import-path
	// compare; empty (no go.mod — synthetic fixtures) falls back to a
	// suffix match there.
	modulePath, _ := GetModulePath(projectDir)

	for i := range components {
		dir := filepath.Join(projectDir, roleRoot, components[i].ImportPath)
		fields, err := ParseServiceDeps(dir)
		if err != nil || len(fields) == 0 {
			continue
		}
		// unwiredCollaborators counts non-optional, non-scalar Deps
		// fields that bootstrap cannot supply from App/AppExtras. When
		// non-zero, the generated New(Deps{...}) is a half-built
		// instance — see CanonicalAppField for the alias escape hatch.
		unwiredCollaborators := 0
		for _, f := range fields {
			switch f.Name {
			case "Logger":
				// Logger is the project's *slog.Logger. Gate emission
				// on the declared type matching to avoid stomping on a
				// package-local Logger type.
				if f.Type == "" || f.Type == "*slog.Logger" {
					components[i].HasLogger = true
				} else if !f.Optional && !isConfigScalarType(f.Type) {
					// Domain-local logger type (e.g. logr.Logger) that
					// bootstrap cannot supply.
					unwiredCollaborators++
				}
			case "Config":
				// HasConfig gates emission of `Config: cfg` in the
				// bootstrap template — but `cfg` is the project's
				// `*config.Config`. If the package declares a
				// domain-local Config (e.g. enforcement.Config) the
				// emit would produce a hard type-mismatch at codegen
				// time. Gate on the type-string matching the project
				// config so a domain Config field gets the typed-zero
				// default and the user wires it manually in setup.go
				// (or via an AppExtras field that matches the type).
				// FRICTION 2026-06-02: cp-forge layer-2 enforcement.
				if f.Type == "" || f.Type == "*config.Config" {
					components[i].HasConfig = true
				} else {
					// Domain-local Config — defer to the name+type
					// matcher (with assignability) like any other field.
					if matchedAppField(matcher, roleRoot, components[i].ImportPath, f.Name, f.Type, appFieldTypes) {
						components[i].AppFieldRefs = append(components[i].AppFieldRefs, AppFieldRef{DepsField: f.Name})
					} else if !f.Optional && !isConfigScalarType(f.Type) {
						unwiredCollaborators++
					}
				}
			case "DB":
				// The ORM handle. forge's own CRUD generator injects
				// `DB orm.Context` into a service's Deps, and an
				// interactor that reaches the database declares the
				// same field — so the test harness must supply it for
				// both. Gate on the declared type matching orm.Context
				// so a domain-local DB field is left to the name+type
				// matcher below, the same way Logger/Config do.
				if f.Type == "" || f.Type == "orm.Context" {
					components[i].HasDB = true
				} else if matchedAppField(matcher, roleRoot, components[i].ImportPath, f.Name, f.Type, appFieldTypes) {
					components[i].AppFieldRefs = append(components[i].AppFieldRefs, AppFieldRef{DepsField: f.Name})
				} else if !f.Optional && !isConfigScalarType(f.Type) {
					unwiredCollaborators++
				}
			default:
				// Name-match + assignability check. The legacy version
				// required byte-equal type strings, which silently
				// dropped the wire when funding.Deps.Repo was
				// funding.Repository (narrow interface) and
				// AppExtras.Repo was *db.PostgresRepository (concrete
				// impl). Using go/types via the matcher closes the
				// silent-drop class without losing the safety of the
				// no-name-match → no-emit invariant (the existing
				// optional-dep mechanism).
				if matchedAppField(matcher, roleRoot, components[i].ImportPath, f.Name, f.Type, appFieldTypes) {
					components[i].AppFieldRefs = append(components[i].AppFieldRefs, AppFieldRef{DepsField: f.Name})
				} else if !f.Optional && !isConfigScalarType(f.Type) {
					unwiredCollaborators++
				}
			}
		}
		// The construction is unexpressible from App/AppExtras fields:
		// at least one live collaborator stays unwired, so the emitted
		// New(Deps{...}) would be a half-built duplicate of whatever
		// the user constructs in setup.go (panicking in validateDeps or
		// silently no-opping). If the user maintains the canonical
		// instance on exactly one App/AppExtras field of this package's
		// Service type, alias the Packages slot to it instead — Setup
		// runs before the package table, so the assignment is always
		// observed. Packages-only: workers/operators have no
		// canonical-instance convention.
		if unwiredCollaborators > 0 && roleRoot == "internal" {
			components[i].CanonicalAppField = canonicalAppServiceField(
				filepath.Join(projectDir, "pkg", "app"),
				modulePath, roleRoot, components[i].ImportPath, components[i].Package)
		}
	}
}

// isConfigScalarType reports whether a Deps field type is a plain
// configuration scalar (string, bool, numeric, or a slice of those —
// e.g. APIKey string, PlansData []byte, AllowedHosts []string).
// Scalars are never auto-wired from App/AppExtras by name today, and
// their zero value is the package's documented degraded mode rather
// than a nil collaborator — so they don't count toward the
// "construction is unexpressible" trigger for CanonicalAppField.
func isConfigScalarType(t string) bool {
	t = strings.TrimPrefix(strings.TrimSpace(t), "[]")
	switch t {
	case "string", "bool", "byte", "rune",
		"int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"float32", "float64", "complex64", "complex128",
		"time.Duration":
		return true
	}
	return false
}

// canonicalAppServiceField finds the single exported App/AppExtras
// field whose declared type is `<pkg>.Service` for the internal
// package at <module>/<roleRoot>/<importPath>. Returns "" unless
// exactly one such field exists (zero → nothing to alias; two or more
// → ambiguous, no deterministic canonical instance).
//
// Resolution follows each pkg/app file's own import table so renamed
// imports work (cp-forge declares `internaluser "…/internal/user"` and
// types the field `internaluser.Service`). A plain import's qualifier
// is the package's real package-clause name (pkgName, disk-resolved by
// the caller), NOT the directory leaf — the two may legally differ.
// When modulePath is empty (no go.mod — synthetic fixtures), import
// paths match by "/<roleRoot>/<importPath>" suffix instead.
func canonicalAppServiceField(appDir, modulePath, roleRoot, importPath, pkgName string) string {
	entries, err := os.ReadDir(appDir)
	if err != nil {
		return ""
	}
	want := ""
	if modulePath != "" {
		want = modulePath + "/" + roleRoot + "/" + importPath
	}
	suffix := "/" + roleRoot + "/" + importPath

	fset := token.NewFileSet()
	seen := map[string]struct{}{}
	var fieldNames []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(appDir, entry.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		// Qualifiers in THIS file that refer to the target package.
		quals := map[string]struct{}{}
		for _, imp := range file.Imports {
			if imp.Path == nil {
				continue
			}
			p := strings.Trim(imp.Path.Value, `"`)
			matched := p == want
			if want == "" {
				matched = strings.HasSuffix(p, suffix)
			}
			if !matched {
				continue
			}
			if imp.Name != nil {
				if imp.Name.Name == "_" || imp.Name.Name == "." {
					continue
				}
				quals[imp.Name.Name] = struct{}{}
			} else {
				quals[pkgName] = struct{}{}
			}
		}
		if len(quals) == 0 {
			continue
		}
		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.TYPE {
				continue
			}
			for _, spec := range genDecl.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || (typeSpec.Name.Name != "App" && typeSpec.Name.Name != "AppExtras") {
					continue
				}
				structType, ok := typeSpec.Type.(*ast.StructType)
				if !ok || structType.Fields == nil {
					continue
				}
				for _, field := range structType.Fields.List {
					sel, ok := field.Type.(*ast.SelectorExpr)
					if !ok || sel.Sel == nil || sel.Sel.Name != "Service" {
						continue
					}
					qual, ok := sel.X.(*ast.Ident)
					if !ok {
						continue
					}
					if _, ok := quals[qual.Name]; !ok {
						continue
					}
					for _, name := range field.Names {
						if !name.IsExported() {
							continue
						}
						if _, dup := seen[name.Name]; dup {
							continue
						}
						seen[name.Name] = struct{}{}
						fieldNames = append(fieldNames, name.Name)
					}
				}
			}
		}
	}
	if len(fieldNames) == 1 {
		return fieldNames[0]
	}
	return ""
}

// matchedAppField is the central decision for "should bootstrap emit
// `<Field>: app.<Field>` for this Deps field?". It consults the
// type-aware matcher with a string-compare pre-check.
//
// Returns true when:
//   - the matcher confirms assignability (narrow-interface case), or
//   - the matcher confirms exact-string match (legacy fast path), or
//   - the matcher is unavailable (assignability unprovable: load
//     failure, project mid-edit). Wiring the name match here is the
//     deterministic fail-loud policy (deps_assignability.go header):
//     the compiler arbitrates a wrong wire loudly, while dropping it
//     silently un-wires a live collaborator with no signal. It also
//     matches what wire_gen's consumer does for Unavailable, so
//     bootstrap and wire_gen can't diverge on the same project state.
//     (The previous Unavailable fallback — wire only on byte-equal
//     type strings — made bootstrap output depend on whether the
//     project type-checked mid-pipeline: kalshi FORGE_BACKLOG #13's
//     nondeterminism class.)
//
// Returns false when:
//   - no AppExtras field of the same name exists (the optional-dep
//     invariant), or
//   - name matches but types are PROVEN not assignable (NameMismatch
//     in a single shared type universe — the lint surfaces the gap).
func matchedAppField(m *DepsAssignabilityMatcher, roleRoot, pkgDir, depsName, depsType string, appByName map[string]string) bool {
	appType, hasName := appByName[depsName]
	if !hasName {
		return false
	}
	kind := m.Match(roleRoot, pkgDir, depsName, depsType, appType, true)
	switch kind {
	case MatchExactString, MatchAssignable:
		return true
	case MatchNameMismatch:
		// Intentional silence here — the lint reports loudly. We don't
		// emit a wire that won't compile, AND we don't pretend the
		// field doesn't exist (which would have skipped validateDeps).
		return false
	case MatchUnavailable:
		// Unproven ≠ mismatched. Wire the name match; see the policy
		// note in the function doc.
		return true
	default:
		return false
	}
}

// leaderElectionID derives a Kubernetes-valid leader-election lease name
// from the project's module path. Lease names are k8s resource names and
// must satisfy DNS-1123 label rules (lowercase alphanumerics and '-');
// the raw module path ("github.com/acme/control-plane") contains slashes
// and dots, which the API server rejects with "may not contain '/'". Use
// the module's final path element, lowercased with every invalid rune
// squashed to '-', suffixed with "-leader" (e.g. "control-plane-leader").
// LeaderElectionID is the controller manager's leader election ID for a
// module — the one the generated operators node (and the legacy
// lifecycle.go before it) elects under.
func LeaderElectionID(modulePath string) string { return leaderElectionID(modulePath) }

func leaderElectionID(modulePath string) string {
	base := modulePath
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	base = strings.ToLower(base)
	var b strings.Builder
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "forge"
	}
	return slug + "-leader"
}

// BootstrapTestServiceData holds data for a single service in the bootstrap testing template.
//
// ProtoConnectImportPath and ProtoConnectPkg are derived from the proto file's
// declared `go_package` rather than from the service name. This makes the
// generated testing.go correct even when the proto file's go_package doesn't
// follow the convention `<module>/gen/services/<svc>/v1/<svc>v1` (for example
// when multiple proto services live in a single proto file and share one go
// package, or when the package name has a custom alias).
type BootstrapTestServiceData struct {
	Name    string // e.g. "api"
	Package string // e.g. "api" (Go package CLAUSE — may differ from the dir name)
	// ImportPath is the handler directory leaf under handlers/ as it exists
	// ON DISK (e.g. "engine_shadow"), resolved by ResolveServiceComponent.
	// The testing.go template builds its import line from this, never from
	// Package — a dir may legally declare a package name that differs from
	// its directory name, and only the directory name belongs in the path.
	ImportPath string
	FieldName  string // e.g. "API" (exported struct field)
	// ConnectServiceGoName is protoc-gen-connect-go's base for the service
	// (naming.ConnectServiceGoName): the test client is <base>Client.
	// "Alphav1connectService" → Alphav1ConnectService.
	ConnectServiceGoName   string
	ProtoConnectImportPath string // e.g. "github.com/foo/bar/gen/services/api/v1/apiv1connect"
	ProtoConnectPkg        string // e.g. "apiv1connect" (Go identifier used at call sites)
	// MountMethod is the handler method that mounts the service — "Register"
	// normally, "Mount" when the service declares an RPC of its own called
	// Register (one type cannot carry both, and the RPC wins).
	MountMethod string
	Fallible    bool // true if the constructor returns (T, error)
	HasDB       bool // true if Deps struct has a DB orm.Context field
	// Constructor is the handler's entry-point func name — `New` unless the
	// package moved the `// forge:constructor` marker onto a name of its own.
	// Read through [BootstrapTestServiceData.ConstructorName].
	Constructor string
	// Alias mirrors BootstrapComponentData.Alias — when an internal package
	// shares its leaf-name with this service's package, both get role-prefixed
	// aliases ("svcBilling" vs "pkgBilling") so the generated testing.go imports
	// don't collide.
	Alias string
	// VarName is the lowerCamel form of FieldName, used as the testConfig
	// field name (e.g. `c.billingDeps`). Defaults to lowerFirst(Package);
	// becomes "svcBilling" when there's a cross-role collision so the
	// services-range and packages-range testConfig fields stay distinct.
	VarName string
	// AutoStubs lists the per-Deps-field synthesized interface stubs the
	// template should emit and inject as defaults inside NewTest<Svc>.
	// Each entry corresponds to a Deps field whose Go type is an interface
	// declared locally in the handler package (so the testing.go file can
	// reference the interface via the imported handler package alias).
	// Optional-dep fields are excluded — those stay nil to preserve the
	// "graceful dev-mode degrade" semantics. See ParseLocalInterfaces +
	// HasOptionalDepMarker for the detection rules.
	AutoStubs []DepsAutoStub
	// AutoMocks lists the Deps fields whose interface forge already mocks
	// in a mock_gen.go: NewTest<Svc> installs that mock, and no stub is
	// synthesized for them (see harness_mocks.go).
	AutoMocks []DepsAutoMock
	// UnresolvedStubs lists Deps fields whose type is a cross-package
	// selector forge couldn't resolve (alias not in imports, package
	// can't load, type isn't an interface). The template emits a
	// `// TODO: stub <type>` line next to NewTest<Svc> so the user
	// sees a visible reminder to hand-roll an override via
	// With<Svc>Deps(...). Empty when every selector resolved cleanly.
	UnresolvedStubs []UnresolvedAutoStub
	// FuncDefaults lists required func-typed Deps fields the harness fills
	// with a real framework default (Clock -> time.Now, IDGen -> ulid). A
	// func value can't be synthesized by the interface auto-stubber, so
	// without this a required `Now func() time.Time` / `NewID func() string`
	// would leave NewTest<Svc> constructing with a nil func — validateDeps
	// (or the first call) then fails. Optional func fields are excluded
	// (they stay nil by design — the `//forge:optional-dep` contract).
	FuncDefaults []DepsFuncDefault
	// FuncTodos lists required func-typed Deps fields the harness has NO
	// sensible default for (e.g. `Send func(string) error`). Emitted as a
	// TODO next to NewTest<Svc> so the author overrides via With<Svc>Deps
	// instead of hitting a silent nil-func validateDeps failure — the
	// func-typed analog of UnresolvedStubs.
	FuncTodos []UnresolvedAutoStub
	// UnfilledDeps lists every required Deps field the harness leaves nil
	// because it cannot construct one (see computeUnfilledDeps). Each gets
	// a WithPlaceholderDeps fill and a line in the construction failure.
	UnfilledDeps []UnresolvedAutoStub
}

// ConstructorName is the constructor selector the testing.go template must
// emit for this service — the detected name, or the canonical `New` when the
// field was never populated.
func (d BootstrapTestServiceData) ConstructorName() string {
	if d.Constructor == "" {
		return DefaultConstructorName
	}
	return d.Constructor
}

// UnresolvedAutoStub is one Deps field whose cross-package selector
// type couldn't be turned into a synthesized stub. Surfaces in the
// generated testing.go as a TODO comment so the user knows to
// hand-roll an override.
type UnresolvedAutoStub struct {
	// FieldName is the Deps field name as declared.
	FieldName string
	// TypeExpr is the unresolved type expression as written in the
	// Deps struct (e.g. "external.Client").
	TypeExpr string
	// Reason says why, when it is not the default (the package did not
	// load, or the type is not an interface).
	Reason string
}

// DepsFuncDefault describes one required func-typed Deps field the test
// harness fills with a real framework default, so NewTest<Svc> constructs
// the service without the author hand-rolling a func value (the Clock /
// IDGen friction). The template emits `if deps.<F> == nil { deps.<F> =
// <Expr> }` — matching the by-type Clock/IDGen fill compose.go emits on
// the live path, so tests and production share the same default.
type DepsFuncDefault struct {
	// FieldName is the Deps field name as declared (e.g. "Now", "NewID").
	FieldName string
	// Expr is the Go expression the harness assigns (e.g. "time.Now" or
	// the ULID id-generator closure).
	Expr string
	// NeedsTime / NeedsULID flag the import the Expr references so the
	// assembler can gate testing.go's `time` / ulid imports.
	NeedsTime bool
	NeedsULID bool
}

// DepsAutoStub describes one synthesized interface implementation
// emitted into a service's generated helpers_gen_test.go for a
// service-owned Deps field whose interface no forge mock_gen.go covers
// (one that does gets its DepsAutoMock instead, so a collaborator never
// has two doubles). It exists so NewTest<Svc>(t) can construct
// the Service even when the field is required by validateDeps, and it is
// a programmable double shaped like a contract mock: a <Method>Func field
// per method, an embedded contractkit.Recorder, and a loud
// testkit.StubNotConfigured error from every error-returning method the
// test did not program (see programStubMethods).
type DepsAutoStub struct {
	// FieldName is the Deps field name as declared (e.g. "Repo").
	FieldName string
	// StubType is the exported Go identifier of the synthesized stub
	// struct: "Stub" + the Deps field name (StubRepo). It is exported so
	// the external <pkg>_test package — where the scaffolded tests live —
	// can construct one with its Func fields set. Field names are unique
	// within one Deps, and each helper file is its own component's, so no
	// package prefix is needed to keep stubs apart.
	StubType string
	// InterfaceQualified is the package-qualified type expression used
	// when injecting the stub into Deps in NewTest<Svc>.
	//
	// For locally-declared interfaces this carries the literal "<alias>."
	// placeholder so the caller can substitute the post-collision
	// service alias ("svcBilling" vs "billing"). For cross-package
	// interfaces (CrossPackage = true) the prefix is already the
	// declaring package's alias (e.g. "repo.Repository") and must NOT
	// be re-aliased — the service alias is irrelevant to it.
	InterfaceQualified string
	// Methods are the interface's flattened method set rendered for
	// the template's stub-emit loop.
	Methods []InterfaceMethod
	// CrossPackage flags stubs whose interface lives in a package
	// other than the handler's. The caller uses this to skip the
	// "<alias>." rewrite and to fold the stub's ExtraImports into
	// the file's import block.
	CrossPackage bool
	// ExtraImports lists every package the stub's method signatures
	// reference. For a CrossPackage stub that includes the interface's
	// own package; for a LOCAL interface the interface type needs no
	// import but its parameter / result types still may, so this is
	// populated independently of CrossPackage. The bootstrap_testing assembler
	// deduplicates these across stubs into the top-level
	// ExtraImports field on the template data.
	ExtraImports []ExtraImport
	// Optional marks a `// forge:optional-dep` field. Its stub type is
	// emitted so a test can pass one through With<Svc>Deps, but
	// NewTest<Svc> does not install it: nil is the field's declared
	// state, and the code's nil path is what a test that says nothing
	// about the field should exercise.
	Optional bool
}

// GenerateBootstrapTesting generates the per-component test harness: one
// helpers_gen_test.go per service / internal package, rendered from
// component_test_helpers.go.tmpl into that component's own directory.
//
// cs is the project's checksum tracker — passing it keeps pkg/app/testing.go
// recorded so `forge project audit` doesn't flag stale state on it. A nil cs is tolerated.
//
// BootstrapTestingGenInput embeds GenContext (ProjectDir / ModulePath /
// Checksums) and adds the component inventory.
// Replaces the prior 8-positional-parameter signature; field names map
// 1:1 to the old params.
type BootstrapTestingGenInput struct {
	GenContext

	Services  []ServiceDef
	Packages  []BootstrapPackageData
	Workers   []BootstrapWorkerData
	Operators []BootstrapOperatorData
}

func GenerateBootstrapTesting(in BootstrapTestingGenInput) error {
	// Destructure into the long-standing local names so the body below
	// (disk resolution + auto-stub assembly) stays untouched.
	services := in.Services
	packages := in.Packages
	// in.Workers / in.Operators are accepted for inventory symmetry with
	// BootstrapGenInput but testing.go is service+package scoped only, so
	// the body never reads them (it didn't before this struct conversion
	// either — they were unused positional params).
	modulePath := in.ModulePath
	projectDir := in.ProjectDir
	cs := in.Checksums

	// Nothing is written under pkg/app/ any more — each helper lands beside
	// its own component — so pkg/app/ is not created here. It used to be,
	// unconditionally, and an empty pkg/app/ is service evidence to
	// config.deriveProjectKindFromSources: a project derived as a CLI on one
	// `forge generate` became a service on the next (more CI workflows
	// scaffolded, observability and hot reload switched on), so generate
	// was not idempotent.

	// Disk-first: same handler-dir + package-clause resolution as
	// GenerateBootstrap so testing.go's import lines / aliases can never
	// disagree with bootstrap.go's (see disk_resolver.go). Resolved up front
	// so every cross-package interface the services' Deps name is
	// type-checked in one load (typesResolver), not one per field.
	resolved := make([]ResolvedComponent, len(services))
	handlerDirs := make([]string, len(services))
	for i, svc := range services {
		res, resErr := ResolveServiceComponent(projectDir, svc.Name)
		if resErr != nil {
			return resErr
		}
		resolved[i] = res
		handlerDirs[i] = res.Dir
	}
	depsTypes := newTypesResolver(projectDir, depsSelectorImports(handlerDirs))

	var testSvcs []BootstrapTestServiceData
	anyServiceHasDB := false
	for i, svc := range services {
		res := resolved[i]
		pkg := res.PackageName
		handlerDir := res.Dir
		fallible, _ := DetectFallibleConstructor(handlerDir)
		hasDB, _ := DetectDepsDBField(handlerDir)
		if hasDB {
			anyServiceHasDB = true
		}
		// Auto-stub: walk the service's Deps fields, find any interface-
		// typed required fields and queue a synthesized stub. Handles
		// both locally-declared interfaces AND cross-package selector
		// types (e.g. repo.Repository) — the selector path loads the
		// imported package via go/types and walks its method set. The
		// bare-Deps pair (Logger / Config) and the DB orm.Context field
		// are handled by the existing default fill so they're excluded
		// from auto-stubbing here.
		//
		// unresolvedStubs surfaces selector types we couldn't resolve
		// (package failed to load, alias not in imports, type isn't
		// an interface) so the template can emit a TODO comment.
		autoStubs, autoMocks, unresolvedStubs := computeDepsDoubles(handlerDir, depsTypes)
		// Func-typed required deps: fill Clock/IDGen with real defaults
		// (time.Now / ulid), TODO the rest — the harness half of the
		// Clock/IDGen fix. A func value can't be interface-auto-stubbed.
		funcDefaults, funcTodos := computeFuncDefaults(handlerDir)
		// Derive Connect package path/name from the proto's declared
		// go_package + PkgName instead of guessing from the service name.
		// This is what lets a service whose proto moved (e.g. from
		// services/daemon_token/v1 to reliant/v1) regenerate testing.go
		// with the correct *v1connect import — the convention path
		// would still point at the old gen/services/<svc>/v1 location.
		//
		// Falls back to the convention path only when BOTH descriptor
		// fields are empty (synthetic test fixtures, pre-descriptor
		// scaffolds). Falling back on GoPackage alone left connectPkg as
		// the literal "connect" when PkgName happened to be empty.
		connectPkg := svc.PkgName + "connect"
		connectImport := svc.GoPackage + "/" + connectPkg
		if svc.GoPackage == "" || svc.PkgName == "" {
			// Synthesize from svc.Name (NOT the disk-resolved pkg): the
			// gen/ path mirrors the proto layout, not the handler dir's
			// possibly-legacy package clause.
			synth := naming.ServicePackage(svc.Name)
			connectImport = modulePath + "/gen/services/" + synth + "/v1/" + synth + "v1connect"
			connectPkg = synth + "v1connect"
		}
		testSvcs = append(testSvcs, BootstrapTestServiceData{
			Name:                   pkg,
			Package:                pkg,
			ImportPath:             res.ImportLeaf,
			FieldName:              naming.ToPascalCase(pkg),
			ConnectServiceGoName:   svc.GoName(),
			ProtoConnectImportPath: connectImport,
			ProtoConnectPkg:        connectPkg,
			MountMethod:            mountMethodFor(svc),
			Fallible:               fallible,
			Constructor:            DetectConstructorName(handlerDir),
			HasDB:                  hasDB,
			Alias:                  pkg, // overwritten below if there's a cross-role collision
			VarName:                pkg, // overwritten below if there's a cross-role collision
			AutoStubs:              autoStubs,
			AutoMocks:              autoMocks,
			UnresolvedStubs:        unresolvedStubs,
			FuncDefaults:           funcDefaults,
			FuncTodos:              funcTodos,
			UnfilledDeps:           computeUnfilledDeps(handlerDir),
		})
	}

	// Drop packages forge does not construct from the test-factory namespace.
	// An `//forge:external-component` domain pkg is HAND-CONSTRUCTED in
	// providers.go / OpenInfra, and a `//forge:not-a-component` one is not in
	// the server at all — neither is a forge-wired component, so neither gets
	// a NewTest<Pkg> factory (mirroring composedComponents on the Build
	// side). This also removes them from the cross-role collision count:
	// a domain `internal/billing` and a handler `internal/handlers/billing`
	// both `package billing` would otherwise drive a spurious Svc-prefix
	// rename on the HANDLER service's factory (NewTestSvcBilling) that no
	// scaffold test references (ComputeTestHelperName is now external-aware
	// too), and could duplicate-declare the same factory identifier.
	packages = filterWiredComponentPackages(projectDir, packages)

	// Resolve cross-role import-alias collisions. Build a count over the
	// services + packages namespace (workers/operators don't appear in
	// testing.go imports, but we still pass them so the alias derivation
	// matches bootstrap.go's exactly). When colliding, also rewrite
	// FieldName so the generated `NewTest<FieldName>` and `With<FieldName>Deps`
	// helpers don't collide on the same Go identifier.
	pkgCount := map[string]int{}
	for _, s := range testSvcs {
		pkgCount[s.Package]++
	}
	for _, p := range packages {
		pkgCount[p.Package]++
	}
	for i := range testSvcs {
		// Route the collision-prefix rule through the single source of
		// truth so testing.go's svc-aliases/field names can never drift
		// from bootstrap.go's (see ResolveCollisionNaming). Only override
		// FieldName/VarName on collision — the no-collision branch keeps
		// the caller's ToPascalCase FieldName and the default Package alias.
		alias, fieldName := ResolveCollisionNaming(testSvcs[i].Package, testSvcs[i].FieldName, "svc", pkgCount)
		testSvcs[i].Alias = alias
		if pkgCount[testSvcs[i].Package] > 1 {
			testSvcs[i].FieldName = fieldName
			testSvcs[i].VarName = lowerFirst(testSvcs[i].FieldName)
		}
		// Rewrite the AutoStubs' qualified interface refs to use the
		// post-collision alias. The unqualified stub-type identifier
		// already carries an UpperCamel(Package) prefix so collisions
		// across services are impossible by construction.
		//
		// CrossPackage stubs skip this rewrite — their interface lives
		// in a different package whose alias has nothing to do with
		// this service's alias. Their InterfaceQualified is already
		// the resolved "<pkg>.<TypeName>" form from
		// ResolveCrossPkgInterface.
		//
		// alias here is the post-collision Alias resolved above.
		for j, stub := range testSvcs[i].AutoStubs {
			if stub.CrossPackage {
				continue
			}
			// Replace the placeholder "<alias>." prefix with the resolved
			// alias. computeAutoStubs writes "<alias>." literally so the
			// rewrite is a single string-replace per field.
			testSvcs[i].AutoStubs[j].InterfaceQualified = strings.Replace(stub.InterfaceQualified,
				"<alias>.", alias+".", 1)
		}
	}
	// Apply the same collision rule to packages so the testing.go import
	// alias for a colliding internal package matches the bootstrap.go alias.
	// Take a defensive copy so we don't mutate the caller's slice.
	pkgsCopy := append([]BootstrapPackageData(nil), packages...)
	for i := range pkgsCopy {
		if pkgCount[pkgsCopy[i].Package] > 1 {
			// Same single-source-of-truth rule as the services loop above,
			// so a colliding internal package's testing.go alias/field name
			// matches bootstrap.go's exactly (see ResolveCollisionNaming).
			alias, fieldName := ResolveCollisionNaming(pkgsCopy[i].Package, pkgsCopy[i].FieldName, "pkg", pkgCount)
			pkgsCopy[i].Alias = alias
			pkgsCopy[i].FieldName = fieldName
			pkgsCopy[i].VarName = lowerFirst(pkgsCopy[i].FieldName)
		} else if pkgsCopy[i].Alias == "" {
			pkgsCopy[i].Alias = pkgsCopy[i].Package
		}
	}
	// Probe each package's Deps AST so testing.go emits the same Deps
	// shape as bootstrap.go. Without this call, removing Logger from a
	// package's Deps regenerates bootstrap.go cleanly but breaks
	// testing.go (the v2 migration of control-plane reproduced this).
	inspectComponentDepsShape(pkgsCopy, projectDir, "internal")
	packages = pkgsCopy

	// Reconcile cross-package auto-stub import aliases against the service /
	// package imports the file ALREADY carries. A cross-package stub imports
	// its interface's package under that package's DECLARED name (e.g. a
	// gateway Deps field `Billing billing.Service` whose interface lives in
	// the external-component domain `internal/billing`, declared `package
	// billing`). When that bare alias collides with a DIFFERENT path the file
	// already imports — the handler service `internal/handlers/billing`, also
	// `package billing` — Go rejects the duplicate declaration. Re-alias the
	// stub's import to a path-unique form and rewrite every reference in the
	// stub (InterfaceQualified + method signatures) so the rendered stub still
	// compiles. This is the same same-clause collision FIX #1 closes, applied
	// to the test harness's stub imports.
	reconcileAutoStubAliases(testSvcs, packages)

	// Dedupe Connect package imports: when multiple proto services share one
	// proto file (and thus one go_package), they share one connect import.
	// Without dedupe the generated testing.go would contain duplicate imports
	// and fail to compile.
	connectImportSet := make(map[string]struct{}, len(testSvcs))
	var connectImports []string
	for _, s := range testSvcs {
		if _, seen := connectImportSet[s.ProtoConnectImportPath]; seen {
			continue
		}
		connectImportSet[s.ProtoConnectImportPath] = struct{}{}
		connectImports = append(connectImports, s.ProtoConnectImportPath)
	}

	// Collect the union of every cross-package import any auto-stub
	// needs. The bootstrap_testing template emits these in a dedicated
	// import block so the rendered file can reference cross-package
	// interface types and their method-signature dependencies without
	// the user wiring up the import by hand. Deterministic ordering is
	// preserved by SortedNeededImports — the union is then re-sorted
	// here so the final file stays diff-stable.
	extraImports := mergeExtraImports(testSvcs)

	// Gate the framework Clock/IDGen imports on whether any service actually
	// carries a func default of that shape — mirrors AnyServiceHasDB.
	anyServiceNeedsTime := false
	anyServiceNeedsULID := false
	for _, s := range testSvcs {
		for _, fd := range s.FuncDefaults {
			if fd.NeedsTime {
				anyServiceNeedsTime = true
			}
			if fd.NeedsULID {
				anyServiceNeedsULID = true
			}
		}
	}

	data := bootstrapTestingTemplateData{
		Module:              modulePath,
		Services:            testSvcs,
		ConnectImports:      connectImports,
		Packages:            packages,
		AnyServiceHasDB:     anyServiceHasDB,
		AnyServiceNeedsTime: anyServiceNeedsTime,
		AnyServiceNeedsULID: anyServiceNeedsULID,
		// Same predicate GenerateMigrate uses for db/embed.go: when the
		// project carries SQL migrations, emit the opt-in NewMigratedTestDB
		// helper (testkit.NewMigratedPostgresDB over forgedb.MigrationsFS)
		// so generated CRUD/handler tests can start with the real schema
		// instead of the bare default database.
		HasMigrationsFS: ProjectHasSQLMigrations(projectDir),
		ExtraImports:    extraImports,
	}

	// Emit ONE helpers_gen_test.go per component, beside the component it
	// constructs, instead of a single pkg/app/testing.go.
	//
	// pkg/app/testing.go was a NON-test .go file that imported "testing",
	// and cmd/<proj>/cmd imports pkg/app — so `go list -deps ./cmd/<proj>`
	// contained `testing` and every scaffolded project linked the testing
	// package (and the flags it registers in init()) into its production
	// binary. The `_test.go` suffix is what fixes that: the toolchain
	// compiles these files only into their package's test binary.
	if err := writeComponentTestHelpers(data, projectDir, cs); err != nil {
		return err
	}

	// Retire the pre-split file. An existing project regenerating against
	// this forge must LOSE pkg/app/testing.go, not end up with both it and
	// the new per-component files: leaving it behind would keep the leak
	// alive (the whole point of the split) and duplicate-declare every
	// helper's name in a package that still compiles into cmd/.
	if err := removeRetiredBootstrapTesting(projectDir, cs); err != nil {
		return err
	}

	// Emit the typed New<Entity> test factories, one
	// internal/handlers/<svc>/factories_gen_test.go per handler package that
	// owns entities. Best-effort contract: a missing/unreachable shadow DB or
	// an unseedable entity just means the factory isn't emitted this run,
	// never a failed generate.
	if err := GenerateEntityFactories(projectDir, modulePath, services, cs); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: skipped entity factories: %v\n", err)
	}

	// Retire internal/testfactory, where the factories lived before they
	// moved beside their consumers. Leaving it behind would keep a non-test
	// .go file that imports `testing` in the tree — the exact shape this move
	// removes — and duplicate-declare every New<Entity> the new files emit
	// (harmlessly, being in another package, but confusingly).
	removeRetiredEntityFactories(projectDir, cs)
	return nil
}

// removeRetiredEntityFactories deletes the pre-move
// internal/testfactory/factory_gen.go and, when that leaves the directory
// empty, the directory itself.
//
// Only a copy that still self-verifies as forge's own render is reclaimed
// (removeRetiredForgeFile's contract): a hand-edited or disowned one is the
// user's now, and deleting it would destroy work forge did not write. Such a
// project keeps the old package — and its `testing` import — until the user
// removes it, which the drift hint for this path says outright.
func removeRetiredEntityFactories(projectDir string, cs *checksums.FileChecksums) {
	rel := RetiredEntityFactoryRelPath()
	removeRetiredForgeFile(projectDir, rel, cs)
	// os.Remove on a directory succeeds only when it is empty, which is
	// exactly the condition for reclaiming it: a user file parked alongside
	// keeps the directory alive.
	_ = os.Remove(filepath.Join(projectDir, filepath.Dir(rel)))
}

// bootstrapTestingTemplateData is the assembled component inventory
// GenerateBootstrapTesting builds before fanning it out into one
// per-component helper file. It was an anonymous struct back when the whole
// inventory rendered into a single pkg/app/testing.go; naming it lets the
// per-component writer take it as a parameter.
type bootstrapTestingTemplateData struct {
	Module              string
	Services            []BootstrapTestServiceData
	ConnectImports      []string
	Packages            []BootstrapPackageData
	AnyServiceHasDB     bool
	AnyServiceNeedsTime bool
	AnyServiceNeedsULID bool
	HasMigrationsFS     bool
	ExtraImports        []ExtraImport
}

// componentTestHelperData is the per-component view the
// component_test_helpers.go.tmpl template renders. One value per service /
// internal package; each renders into that component's own directory in that
// component's own package clause, so two components can never collide on an
// identifier and a new service never rewrites an existing service's file.
type componentTestHelperData struct {
	Module    string
	Package   string // package CLAUSE of the component's dir
	Name      string
	FieldName string
	IsService bool

	ConstructorName string
	Fallible        bool
	HasDB           bool
	HasLogger       bool
	HasMigrationsFS bool
	NeedsTime       bool

	// ConfigImport is the import path of the config the component's Deps
	// takes (`Config *config.Config`), read from its source by
	// DetectDepsConfigImport. Empty means it takes none, and the harness
	// then has no config to default, no WithConfig option, and no config
	// import: it imports only what the component itself does.
	ConfigImport string
	// ConfigImportAlias is "config" when ConfigImport's last segment is not
	// already config, so the harness can always spell the type config.Config.
	ConfigImportAlias string
	// ConfigDefaults is true when ConfigImport's package declares
	// `func Defaults()` — the generated pkg/config does — and the harness
	// then defaults to config.Defaults(): the config Load yields with no
	// file, env or flag, nested blocks allocated and proto defaults applied.
	// A config package without it (a hand-written one, a CLI's stub) keeps
	// the zero &config.Config{}, the only value the harness can build there.
	ConfigDefaults bool
	NeedsULID      bool

	ConnectServiceGoName   string
	ProtoConnectImportPath string
	ProtoConnectPkg        string
	MountMethod            string

	// ConstructorType is what the component's New actually returns, so the
	// helper's signature matches it: "Service" for the usual internal
	// package, but "KeyWrapper" for one whose contract interface is named
	// after what it does.
	//
	// IT IS READ FROM THE SOURCE, NOT ASSUMED. The template used to emit a
	// literal `Service`, which made a package naming its interface anything
	// else generate a helper referring to a type that does not exist — and
	// because the file is forge-owned, the author could not fix it. That
	// failed the whole `forge generate` at its own validate step, so ONE
	// package's naming broke codegen for the entire repo.
	//
	// Empty falls back to Service, which is right for a just-scaffolded
	// package whose constructor cannot be parsed yet.
	ConstructorType string

	AutoStubs       []DepsAutoStub
	AutoMocks       []DepsAutoMock
	UnresolvedStubs []UnresolvedAutoStub
	FuncDefaults    []DepsFuncDefault
	FuncTodos       []UnresolvedAutoStub
	UnfilledDeps    []UnresolvedAutoStub
	ExtraImports    []ExtraImport
}

// componentTestHelperFile is the file name emitted into each component dir.
// The `_test.go` suffix is load-bearing — see writeComponentTestHelpers.
const componentTestHelperFile = "helpers_gen_test.go"

// writeComponentTestHelpers renders one helpers_gen_test.go per service and
// per internal package, into that component's own directory.
//
// THE PACKAGE CLAUSE IS THE COMPONENT'S OWN (`package order`), NOT the
// external test package (`package order_test`). Go compiles a package's
// in-package _test.go files INTO the package under test, and an external
// `order_test` package then imports that augmented package — so exported
// helpers declared here are visible to BOTH the internal and the external
// test files of this directory, while remaining invisible to every other
// package. That is exactly the scope a test factory should have.
func writeComponentTestHelpers(data bootstrapTestingTemplateData, projectDir string, cs *checksums.FileChecksums) error {
	for _, svc := range data.Services {
		d := componentTestHelperData{
			Module:                 data.Module,
			Package:                svc.Package,
			Name:                   svc.Name,
			FieldName:              svc.FieldName,
			IsService:              true,
			ConstructorName:        svc.ConstructorName(),
			Fallible:               svc.Fallible,
			HasDB:                  svc.HasDB,
			HasMigrationsFS:        data.HasMigrationsFS,
			ConnectServiceGoName:   svc.ConnectServiceGoName,
			MountMethod:            svc.MountMethod,
			ProtoConnectImportPath: svc.ProtoConnectImportPath,
			ProtoConnectPkg:        svc.ProtoConnectPkg,
			AutoStubs:              svc.AutoStubs,
			AutoMocks:              svc.AutoMocks,
			UnresolvedStubs:        svc.UnresolvedStubs,
			FuncDefaults:           svc.FuncDefaults,
			FuncTodos:              svc.FuncTodos,
			UnfilledDeps:           svc.UnfilledDeps,
		}
		dir := filepath.Join(projectDir, "internal", "handlers", filepath.FromSlash(svc.ImportPath))
		d.setConfigImport(dir, projectDir)
		for _, fd := range svc.FuncDefaults {
			if fd.NeedsTime {
				d.NeedsTime = true
			}
			if fd.NeedsULID {
				d.NeedsULID = true
			}
		}
		// The helpers now live INSIDE the component's package, so the
		// component's own types (Deps, Service) are unqualified and the
		// service's own import must not be re-emitted. Cross-package stub
		// imports still apply.
		d.ExtraImports = extraImportsExcluding(mergeExtraImports(
			[]BootstrapTestServiceData{svc}), svc.Package)
		if err := renderComponentTestHelpers(d, dir, projectDir, cs); err != nil {
			return err
		}
	}
	for _, pkg := range data.Packages {
		d := componentTestHelperData{
			Module:          data.Module,
			Package:         pkg.Package,
			Name:            pkg.Name,
			FieldName:       pkg.FieldName,
			IsService:       false,
			ConstructorName: pkg.ConstructorName(),
			Fallible:        pkg.Fallible,
			HasDB:           pkg.HasDB,
			HasLogger:       pkg.HasLogger,
			HasMigrationsFS: data.HasMigrationsFS,
		}
		dir := filepath.Join(projectDir, "internal", filepath.FromSlash(pkg.ImportPath))
		d.setConfigImport(dir, projectDir)
		// The helper returns what New returns. A package whose contract
		// interface is not called Service (keywrap's KeyWrapper) would
		// otherwise get a helper naming a type that does not exist.
		if t, err := DetectConstructorType(dir); err == nil {
			d.ConstructorType = t
		}
		if err := renderComponentTestHelpers(d, dir, projectDir, cs); err != nil {
			return err
		}
	}
	return nil
}

// setConfigImport records the config the component in dir takes, if any (see
// componentTestHelperData.ConfigImport).
func (d *componentTestHelperData) setConfigImport(dir, projectDir string) {
	d.ConfigImport = DetectDepsConfigImport(dir)
	d.ConfigImportAlias = ""
	if d.ConfigImport != "" && path.Base(d.ConfigImport) != "config" {
		d.ConfigImportAlias = "config"
	}
	d.ConfigDefaults = d.ConfigImport != "" && ConfigPackageDeclaresDefaults(projectDir, d.Module, d.ConfigImport)
}

// renderComponentTestHelpers renders one component's helper file into dir.
// A component whose directory does not exist yet (declared in forge.yaml but
// not yet scaffolded on disk) is skipped rather than conjuring a stray dir.
func renderComponentTestHelpers(d componentTestHelperData, dir, projectDir string, cs *checksums.FileChecksums) error {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil
	}
	// Filter ExtraImports against what the template ALREADY imports.
	// Cross-package stub method signatures routinely reference packages the
	// static import block carries unconditionally (`context` is the canonical
	// case: the template emits `"context"` for every service, and any stub
	// method taking a ctx adds `context "context"` to ExtraImports — two
	// declarations of the same name, which fails `go build`). Rather than
	// hand-mirroring the template's conditional import logic here (and
	// drifting the moment the template changes), render a baseline WITHOUT
	// extras, parse its import block, and drop every extra already present.
	// An extra whose alias collides with a DIFFERENT path is kept: that's a
	// genuine package-name collision the compile error should surface.
	if len(d.ExtraImports) > 0 {
		bare := d
		bare.ExtraImports = nil
		if baseline, berr := templates.ProjectTemplates().Render("component_test_helpers.go.tmpl", bare); berr == nil {
			d.ExtraImports = filterAlreadyImported(d.ExtraImports, baseline)
		}
	}
	content, err := templates.ProjectTemplates().Render("component_test_helpers.go.tmpl", d)
	if err != nil {
		return fmt.Errorf("render component_test_helpers.go.tmpl for %s: %w", d.Name, err)
	}
	rel, err := filepath.Rel(projectDir, filepath.Join(dir, componentTestHelperFile))
	if err != nil {
		return fmt.Errorf("resolve helper path for %s: %w", d.Name, err)
	}
	if err := writeSourceDerived(projectDir, rel, content, cs); err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	return nil
}

// extraImportsExcluding drops any import whose alias is the component's own
// package — now that the helpers live in that package, importing it would be
// a self-import.
func extraImportsExcluding(in []ExtraImport, ownPkg string) []ExtraImport {
	out := make([]ExtraImport, 0, len(in))
	for _, imp := range in {
		if imp.Alias == ownPkg {
			continue
		}
		out = append(out, imp)
	}
	return out
}

// removeRetiredBootstrapTesting deletes a pre-split pkg/app/testing.go and
// drops its checksum entry, so an upgrading project stops linking `testing`
// into cmd/ instead of carrying the leaking file alongside its replacement.
//
// A file the user has DISOWNED (or otherwise hand-edited away from the
// forge-owned render) is left alone: forge does not delete files it no longer
// owns. Such a project keeps the leak until the user removes the file, which
// the drift hint for this path now says outright.
func removeRetiredBootstrapTesting(projectDir string, cs *checksums.FileChecksums) error {
	rel := filepath.Join("pkg", "app", "testing.go")
	// The shared retire rule: a pristine forge render is reclaimed, a
	// hand-edited one is kept, and a PRE-MARKER file (no forge:hash) is
	// reclaimed only when it self-identifies as forge output.
	//
	// This used to reclaim Pristine only, which silently kept every
	// pre-marker testing.go forever. That file imports every service's
	// handler package, so deleting one proto service left it referencing a
	// package that no longer exists; generate's own build validation then
	// failed on it and rolled back — and the file that needed retiring was
	// never retired. See retired_bootstrap_testing_test.go.
	removeRetiredForgeFileAt(projectDir, rel, cs)
	return nil
}

// filterWiredComponentPackages narrows pkgs to the packages that actually
// occupy a test-factory namespace slot: the forge-WIRED components. It drops
// anything hand-built in providers.go / OpenInfra
// (`//forge:external-component`), anything the server does not have
// (`//forge:not-a-component`) and anything with no component to construct
// at all — a types-only package carrying `//forge:exclude-contract`, or a
// directory with no contract.go. None of those get a NewTest<Pkg> factory, so
// none of them participate in the cross-role collision count.
//
// The membership test is IsWiredComponentDir, the SAME predicate
// ComputeTestHelperName uses to decide whether the handler service needs its
// "Svc" prefix. Sharing it is the point: when these two derived the answer
// from different sources, a types-only internal/deploy put NewTestDeploy in
// testing.go and NewTestSvcDeploy in the scaffold tests. The on-disk dir is
// internal/<ImportPath> (PackageDataFromNames preserves the nested import
// path). Mirrors composedComponents on the Build side.
func filterWiredComponentPackages(projectDir string, pkgs []BootstrapPackageData) []BootstrapPackageData {
	out := make([]BootstrapPackageData, 0, len(pkgs))
	for _, p := range pkgs {
		dir := filepath.Join(projectDir, "internal", filepath.FromSlash(p.ImportPath))
		if !IsWiredComponentDir(dir) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// ProjectHasSQLMigrations reports whether db/migrations/ contains at
// least one .sql file — the ONE predicate every consumer of "does this
// project ship migrations" reads, so they cannot disagree:
//
//   - GenerateMigrate embeds db/embed.go's MigrationsFS
//   - component_test_helpers.go.tmpl imports forgedb (which only exists when
//     GenerateMigrate saw migrations)
//   - cmd-tree-db.go.tmpl compiles `db migrate` against the EMBEDDED set
//   - the scaffolded per-env `migrate` argv, which the env render turns
//     into a migration initContainer
func ProjectHasSQLMigrations(projectDir string) bool {
	entries, err := os.ReadDir(filepath.Join(projectDir, "db", "migrations"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			return true
		}
	}
	return false
}

// reconcileAutoStubAliases rewrites cross-package auto-stub import aliases
// that collide with a DIFFERENT path the testing.go file already imports
// (a service handler import or an internal package import). For each
// colliding stub it picks a path-unique alias and rewrites the stub's
// InterfaceQualified, every method's Params/Results, and its ExtraImports
// entries so the rendered stub references the renamed import. Same-clause
// collisions (a domain pkg + a handler pkg both `package billing`) are the
// case this closes; without it the file fails to compile with
// "<clause> redeclared in this block".
//
// The used-alias set is keyed by alias -> path. A stub import whose alias
// is absent, or maps to the SAME path, is left untouched (no collision).
func reconcileAutoStubAliases(services []BootstrapTestServiceData, packages []BootstrapPackageData) {
	// used maps an in-use import alias to its path. Service handler imports
	// and internal package imports are the two alias sources already in the
	// file's static import block.
	used := map[string]string{}
	for _, s := range services {
		used[s.Alias] = "internal/handlers/" + s.ImportPath
	}
	for _, p := range packages {
		used[p.Alias] = "internal/" + p.ImportPath
	}

	for i := range services {
		for j := range services[i].AutoStubs {
			stub := &services[i].AutoStubs[j]
			// Local-interface stubs are reconciled too: their signature
			// imports can collide with a static import exactly like a
			// cross-package stub's can. Only the InterfaceQualified rewrite
			// is cross-package-specific (see rewriteStubAlias).
			for k := range stub.ExtraImports {
				imp := &stub.ExtraImports[k]
				existing, taken := used[imp.Alias]
				if !taken || pathHasSuffix(existing, imp.Path) {
					// Free alias, or already-ours (same package) — keep it,
					// and reserve it so a later stub doesn't reuse it for a
					// different path.
					used[imp.Alias] = imp.Path
					continue
				}
				// Collision with a different path. Pick a path-unique alias
				// and rewrite every reference to the old one in this stub.
				newAlias := uniqueStubAlias(imp.Alias, imp.Path, used)
				rewriteStubAlias(stub, imp.Alias, newAlias)
				imp.Alias = newAlias
				used[newAlias] = imp.Path
			}
		}
		// A foreign mock's import collides the same way; its one
		// reference is the qualified MockType.
		for j := range services[i].AutoMocks {
			mock := &services[i].AutoMocks[j]
			for k := range mock.ExtraImports {
				imp := &mock.ExtraImports[k]
				existing, taken := used[imp.Alias]
				if !taken || pathHasSuffix(existing, imp.Path) {
					used[imp.Alias] = imp.Path
					continue
				}
				newAlias := uniqueStubAlias(imp.Alias, imp.Path, used)
				mock.MockType = strings.Replace(mock.MockType, imp.Alias+".", newAlias+".", 1)
				imp.Alias = newAlias
				used[newAlias] = imp.Path
			}
		}
	}
}

// pathHasSuffix reports whether a and b denote the same import (exact
// match). The used map stores module-relative paths for the static imports
// and full paths for stub ExtraImports, so a same-package check compares on
// the trailing module-relative segment.
func pathHasSuffix(a, b string) bool {
	if a == b {
		return true
	}
	return strings.HasSuffix(b, "/"+a) || strings.HasSuffix(a, "/"+b)
}

// uniqueStubAlias derives a deterministic, collision-free alias for a stub
// import. It prefixes "stub" to the upper-cased base alias (stubBilling);
// if that is also taken for a different path, it appends the path's parent
// segment and finally a numeric suffix. Determinism keeps codegen stable.
func uniqueStubAlias(base, path string, used map[string]string) string {
	cand := "stub" + upperFirst(base)
	if p, taken := used[cand]; !taken || p == path {
		return cand
	}
	// Disambiguate with the path's parent segment (e.g. handlers/billing ->
	// stubHandlersBilling), then a numeric suffix as the last resort.
	segs := strings.Split(strings.Trim(path, "/"), "/")
	if len(segs) >= 2 {
		alt := "stub" + upperFirst(segs[len(segs)-2]) + upperFirst(base)
		if p, taken := used[alt]; !taken || p == path {
			return alt
		}
	}
	for n := 2; ; n++ {
		alt := fmt.Sprintf("%s%d", cand, n)
		if p, taken := used[alt]; !taken || p == path {
			return alt
		}
	}
}

// rewriteStubAlias replaces every `<oldAlias>.` selector prefix in a stub's
// InterfaceQualified and method signatures with `<newAlias>.`. The cross-
// package qualifier renders all of the stub's type references through the
// package's declared name, so a single prefix rewrite covers them all.
func rewriteStubAlias(stub *DepsAutoStub, oldAlias, newAlias string) {
	old := oldAlias + "."
	repl := newAlias + "."
	stub.InterfaceQualified = strings.ReplaceAll(stub.InterfaceQualified, old, repl)
	for m := range stub.Methods {
		meth := &stub.Methods[m]
		for _, s := range []*string{&meth.Params, &meth.Results, &meth.ReturnStatement, &meth.NamedParams, &meth.FuncType} {
			*s = strings.ReplaceAll(*s, old, repl)
		}
		for _, list := range [][]string{meth.ParamTypes, meth.ResultTypes, meth.ResultZeros} {
			for i := range list {
				list[i] = strings.ReplaceAll(list[i], old, repl)
			}
		}
	}
}

// mergeExtraImports folds every cross-package stub's ExtraImports
// into one deterministic deduplicated slice. Path is the dedupe key —
// two stubs that both depend on "x/y/z" produce a single import line.
// Conflict on the alias (same path with different aliases across two
// stubs) is resolved first-wins, matching the order computeAutoStubs
// returns and the import-collection inside ResolveCrossPkgInterface
// (which uses the imported package's declared name, so a conflict
// would already be a real package-rename collision the user would
// have to resolve regardless).
func mergeExtraImports(services []BootstrapTestServiceData) []ExtraImport {
	seen := map[string]string{}
	for _, s := range services {
		for _, stub := range s.AutoStubs {
			// Every stub's ExtraImports count, CrossPackage or not. A LOCAL
			// interface needs no import for the interface type itself, but its
			// method signatures may still reference other packages — and the
			// stub renders those signatures verbatim, so the imports are just
			// as load-bearing. Filtering on CrossPackage here is what produced
			// `undefined: proxyauthz` in a generated helpers_gen_test.go.
			for _, imp := range stub.ExtraImports {
				if _, ok := seen[imp.Path]; ok {
					continue
				}
				seen[imp.Path] = imp.Alias
			}
		}
		// A mock from the declaring package is referenced through that
		// package's import.
		for _, mock := range s.AutoMocks {
			for _, imp := range mock.ExtraImports {
				if _, ok := seen[imp.Path]; ok {
					continue
				}
				seen[imp.Path] = imp.Alias
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]ExtraImport, 0, len(seen))
	for path, alias := range seen {
		out = append(out, ExtraImport{Path: path, Alias: alias})
	}
	// Deterministic order — codegen must be reproducible for the
	// checksums-based "no spurious diff" guarantee. SortedNeededImports
	// sorts by Path; we do the same here so the merged slice stays
	// in lockstep with the per-stub view.
	sortExtraImports(out)
	return out
}

// filterAlreadyImported drops every ExtraImport whose declared name AND
// path already appear in src's import block (src is a rendered Go file —
// the bootstrap_testing baseline rendered without extras). Matching on
// the (name, path) pair rather than path alone is deliberate: Go permits
// the same path under two different aliases (the cross-role collision
// case where a package import is aliased `pkgLedger` while a stub
// references the package's declared name), so a path-only filter would
// remove an import the stub's method signatures still need. When src
// doesn't parse, the extras are returned unchanged — the subsequent
// `go build` validation step owns the failure.
func filterAlreadyImported(extras []ExtraImport, src interface{}) []ExtraImport {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "testing.go", src, parser.ImportsOnly|parser.SkipObjectResolution)
	if err != nil {
		return extras
	}
	declared := map[string]string{}
	for _, imp := range f.Imports {
		path, perr := importPathFromSpec(imp)
		if perr != nil {
			continue
		}
		name := aliasForImport(imp, path)
		if _, ok := declared[name]; !ok {
			declared[name] = path
		}
	}
	var out []ExtraImport
	for _, e := range extras {
		if p, ok := declared[e.Alias]; ok && p == e.Path {
			continue
		}
		out = append(out, e)
	}
	return out
}

// sortExtraImports puts an ExtraImport slice in canonical Path order.
// Factored out of mergeExtraImports so the tests can reuse it on
// hand-built slices without re-running the merge.
func sortExtraImports(s []ExtraImport) {
	// import "sort" once; the codegen package already imports it.
	if len(s) < 2 {
		return
	}
	// Trivial insertion sort: codegen runs are dominated by I/O and
	// template execution, the import list is typically <10 elements,
	// and we want to avoid pulling another import for this one call.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1].Path > s[j].Path; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// bareDepsFieldNames is the set of Deps field names the bootstrap_testing
// template fills in by default — these never need an auto-stub. Keep
// the list narrow: anything else (Repo, Audit, Stripe, Email, ...)
// flows through the auto-stub path when its type is a local interface.
var bareDepsFieldNames = map[string]bool{
	"Logger": true,
	"Config": true,
	"DB":     true,
}

// computeAutoStubs walks a service's handler directory, parses its
// Deps struct + locally-declared interfaces, and returns one
// DepsAutoStub for every Deps field whose type is an interface forge
// mocks nowhere but can satisfy with a synthesized stub (an interface
// forge DOES mock gets that mock instead — computeDepsDoubles). The bare-Deps
// pair (Logger / Config) is excluded — the template's default-merge
// step fills it. A `// forge:optional-dep` field gets a stub too, marked
// Optional: the type exists for a test to pass, and NewTest<Svc> leaves
// the field nil (see DepsAutoStub.Optional). An optional field forge
// cannot stub is no TODO: nothing requires it.
//
// Two type shapes are handled:
//
//  1. Bare-identifier types ("Repository") that resolve to an
//     interface declared in the handler package. InterfaceQualified
//     is "<alias>." + name so the caller can substitute the
//     post-collision service alias.
//
//  2. Selector types ("repo.Repository") that resolve to an
//     interface in an imported package. ResolveCrossPkgInterface
//     does the heavy lifting (alias → import path → go/packages
//     load → types.Interface walk). On success, the stub carries
//     CrossPackage = true and ExtraImports listing every package
//     its method signatures reference.
//
// Unresolvable selector types (alias mismatch, package can't load,
// type isn't an interface) fall through to the existing
// "field stays nil" behavior. This is the soft-fail design: tests
// that hit a nil dependency see the usual nil-deref, the user
// either overrides the field via With<Svc>Deps or hand-rolls a
// stub — both are existing escape valves.
func computeAutoStubs(handlerDir, _ string) ([]DepsAutoStub, []UnresolvedAutoStub) {
	stubs, _, unresolved := computeDepsDoubles(handlerDir, newTypesResolver(handlerDir, nil))
	return stubs, unresolved
}

// computeDepsDoubles decides the double NewTest<Svc> installs for each
// interface-typed Deps field of the handler at handlerDir, reading
// cross-package interfaces through resolver, which GenerateBootstrapTesting
// shares across every service so their packages load together.
//
// A field whose interface forge already mocks gets that mock (see
// harness_mocks.go) and no stub; the rest go through computeAutoStubs'
// rules above.
func computeDepsDoubles(handlerDir string, resolver *typesResolver) ([]DepsAutoStub, []DepsAutoMock, []UnresolvedAutoStub) {
	fields, err := ParseServiceDeps(handlerDir)
	if err != nil || len(fields) == 0 {
		return nil, nil, nil
	}
	locals, _ := ParseLocalInterfaces(handlerDir)
	imports, _ := collectImports(handlerDir)
	var stubs []DepsAutoStub
	var mocks []DepsAutoMock
	var unresolved []UnresolvedAutoStub
	for _, f := range fields {
		if bareDepsFieldNames[f.Name] {
			continue
		}
		t := strings.TrimSpace(f.Type)

		// (0) An interface forge already mocks: the harness installs the
		// mock, so a collaborator has one double under one name.
		if mock, ok := mockForField(handlerDir, f, t, imports, resolver); ok {
			mocks = append(mocks, mock)
			continue
		}

		// (1) Bare-identifier interface declared in this package.
		if iface, ok := locals[t]; ok {
			if len(iface.Unresolved) > 0 {
				if f.Optional {
					continue // nothing requires it; no TODO
				}
				// A stub missing the methods of an embed it could not
				// read would not compile; leave the field to the test.
				unresolved = append(unresolved, UnresolvedAutoStub{
					FieldName: f.Name,
					TypeExpr:  t,
					Reason: "forge couldn't read the methods of its embedded " +
						strings.Join(iface.Unresolved, ", "),
				})
				continue
			}
			stubs = append(stubs, DepsAutoStub{
				FieldName:          f.Name,
				StubType:           "", // resolved below from handlerDir's package
				InterfaceQualified: "<alias>." + iface.Name,
				Methods:            iface.Methods,
				// A local interface's SIGNATURES can still be cross-package
				// (`Authorize(ctx context.Context, in proxyauthz.Input)`), and
				// the stub renders them verbatim. CrossPackage stays false —
				// see the note on that field: it describes where the INTERFACE
				// lives, and it gates the "<alias>." rewrite this stub needs.
				ExtraImports: iface.ExtraImports,
				Optional:     f.Optional,
			})
			continue
		}

		// (2) Selector type — resolve across the import boundary.
		// We only handle the simple `<pkg>.<TypeName>` shape; pointer
		// (`*pkg.T`), slice, map, and chan decorations on an interface
		// are not idiomatic and stay on the hand-roll path.
		if dot := strings.IndexByte(t, '.'); dot > 0 && !strings.ContainsAny(t, "*[]<>(){}") {
			pkgAlias := t[:dot]
			typeName := t[dot+1:]
			res, ok := resolveCrossPkgInterface(handlerDir, pkgAlias, typeName, resolver)
			if !ok {
				if f.Optional {
					continue // nothing requires it; no TODO
				}
				// Soft-fail: record the selector so the template can
				// surface a TODO comment. The field still stays nil
				// at construction time — the comment exists purely to
				// nudge the user toward With<Svc>Deps overrides or
				// a hand-rolled stub.
				unresolved = append(unresolved, UnresolvedAutoStub{
					FieldName: f.Name,
					TypeExpr:  t,
				})
				continue
			}
			stubs = append(stubs, DepsAutoStub{
				FieldName:          f.Name,
				StubType:           "", // resolved below
				InterfaceQualified: res.PackageName + "." + typeName,
				Methods:            res.Methods,
				CrossPackage:       true,
				ExtraImports:       SortedNeededImports(res.NeededImports),
				Optional:           f.Optional,
			})
		}
	}
	// Stub<FieldName>: see DepsAutoStub.StubType. The CrossPackage flag
	// does not affect the identifier — the stub lives in the handler's
	// own helper file, never in the interface's package.
	for i := range stubs {
		stubs[i].StubType = "Stub" + stubs[i].FieldName
		programStubMethods(&stubs[i])
	}
	return stubs, mocks, unresolved
}

// programStubMethods renders each of a stub's methods as a programmable
// double: it records the call, delegates to the test's <Method>Func when
// one is set, and otherwise returns what an unconfigured double honestly
// can.
//
// # Every error-returning method refuses when unprogrammed
//
// A method whose last result is `error` returns its other results' zero
// values plus testkit.StubNotConfigured — the same default forge's
// contract mocks make with contractkit.MockNotSet, one generator over.
//
// It used to apply only to methods returning nothing BUT error, on the
// theory that (T, error) has a legible zero: nil, nil as "nothing found".
// Handlers do not read it that way. Go's convention is that a nil error
// means the lookup SUCCEEDED, so `chat, err := repo.GetChat(...)` followed
// by `chat.UserID` dereferences nil — and a test could not reach a
// handler's not-found or failure branch through the stub at all, because
// the stub could only ever answer "found, and it is nil". Every P3b lane in
// reliant hand-wrote a fake to get past it. An error-only method had the
// same flaw in its strongest form (nil from a policy check means ALLOW);
// the rule is now the general one, and stated on the signature rather
// than on names: a stub that did not do the work cannot claim it did.
//
// Methods with no error result return their zero values, as contract mocks
// do; there is no channel to refuse on.
//
// # Programmable
//
// Construction never calls a method, so NewTest<Svc>(t) still builds with
// no overrides. A test that drives a handler through a dependency programs
// exactly the methods it needs, and an unprogrammed one names itself:
//
//	repo := &StubRepo{GetChatFunc: func(context.Context, string) (*db.Chat, error) {
//	    return nil, db.ErrNotFound
//	}}
//	svc := NewTestQuestion(t, WithQuestionDeps(Deps{Repo: repo}))
//
// The embedded contractkit.Recorder lets the same test assert what was
// called (repo.CallCount("ResolveQuestion")).
func programStubMethods(stub *DepsAutoStub) {
	for i := range stub.Methods {
		programStubMethod(stub.StubType, &stub.Methods[i])
	}
}

// programStubMethod fills one method's programmable rendering. The
// receiver and parameter names must not shadow anything the body spells:
// the result types' package qualifiers (in `*new(pkg.T)` zeroes),
// testkit, and new.
func programStubMethod(stubType string, m *InterfaceMethod) {
	taken := map[string]bool{"testkit": true, "new": true, "nil": true}
	for _, z := range m.ResultZeros {
		for q := range qualifiersIn(z) {
			taken[q] = true
		}
	}
	for _, r := range m.ResultTypes {
		for q := range qualifiersIn(r) {
			taken[q] = true
		}
	}
	m.Receiver = freeIdent("s", taken)
	taken[m.Receiver] = true

	var named, call, record []string
	for i, t := range m.ParamTypes {
		name := freeIdent(fmt.Sprintf("a%d", i), taken)
		named = append(named, name+" "+t)
		record = append(record, name)
		if strings.HasPrefix(t, "...") {
			call = append(call, name+"...")
		} else {
			call = append(call, name)
		}
	}
	m.NamedParams = strings.Join(named, ", ")
	m.CallArgs = strings.Join(call, ", ")
	m.RecordArgs = strings.Join(record, ", ")
	m.FuncType = "func(" + m.Params + ")"
	if m.Results != "" {
		m.FuncType += " " + m.Results
	}

	if n := len(m.ResultTypes); n > 0 && strings.TrimSpace(m.ResultTypes[n-1]) == "error" {
		zeros := append([]string(nil), m.ResultZeros...)
		zeros[n-1] = fmt.Sprintf("testkit.StubNotConfigured(%q, %q)", stubType, m.Name)
		m.ReturnStatement = "return " + strings.Join(zeros, ", ")
	}
}

// freeIdent returns base, or base with trailing underscores until it is
// not in taken.
func freeIdent(base string, taken map[string]bool) string {
	for taken[base] {
		base += "_"
	}
	return base
}

// computeFuncDefaults walks a service's Deps and classifies each REQUIRED
// (non-`forge:optional-dep`) func-typed field into either a framework
// default the harness can fill, or a TODO the author must satisfy:
//
//   - `func() time.Time` -> `time.Now`   (the Clock seam)
//   - `func() string`    -> ULID id gen  (the IDGen seam)
//   - any other `func(...)...`           -> FuncTodos (no sensible default)
//
// This is the harness half of the Clock/IDGen fix: it mirrors the by-type
// fill compose.go emits on the live path, so a service that depends on a
// deterministic clock / id source constructs in NewTest<Svc> with a real
// default instead of a nil func. Optional func fields are skipped — the
// `//forge:optional-dep` contract says nil is acceptable, and the test
// harness leaves them nil like every other optional dep. The bare-Deps
// trio + DB are never func-typed, so bareDepsFieldNames membership is
// checked only for symmetry with computeAutoStubs.
func computeFuncDefaults(handlerDir string) ([]DepsFuncDefault, []UnresolvedAutoStub) {
	fields, err := ParseServiceDeps(handlerDir)
	if err != nil || len(fields) == 0 {
		return nil, nil
	}
	var defaults []DepsFuncDefault
	var todos []UnresolvedAutoStub
	for _, f := range fields {
		if bareDepsFieldNames[f.Name] || f.Optional {
			continue
		}
		t := strings.TrimSpace(f.Type)
		if !strings.HasPrefix(t, "func(") {
			continue
		}
		switch t {
		case frameworkClockType:
			defaults = append(defaults, DepsFuncDefault{
				FieldName: f.Name, Expr: frameworkClockExpr, NeedsTime: true,
			})
		case frameworkIDGenType:
			defaults = append(defaults, DepsFuncDefault{
				FieldName: f.Name, Expr: frameworkIDGenExpr, NeedsULID: true,
			})
		default:
			todos = append(todos, UnresolvedAutoStub{FieldName: f.Name, TypeExpr: t})
		}
	}
	return defaults, todos
}

// computeUnfilledDeps returns every REQUIRED Deps field of the handler in
// handlerDir that the test harness leaves nil because it cannot construct
// one: a pointer, slice, map or channel to something concrete (account's
// `SQL *sql.DB`), and a func with no framework default.
//
// The harness fills Logger, Config, an orm.Context DB, Clock and IDGen
// funcs, and auto-stubs interfaces. Everything here is a value only the
// test can supply, so a bare NewTest<Svc>(t) used to die in validateDeps
// with "Deps.SQL is required" and no word on what to do — including in the
// scaffolded test forge itself wrote, which therefore could never pass
// without an edit. Each field listed here gets:
//
//   - a fill under WithPlaceholderDeps(): a never-initialised stand-in
//     (testkit.Placeholder) for a test whose rows never reach it, which is
//     what the scaffolded tests pass — inert for a handle, but a valid empty
//     config for a generated config message;
//   - a line in NewTest<Svc>'s construction failure naming the field, its
//     type, and both ways to supply it.
//
// Only types that are nil-able BY SYNTAX qualify: the harness compares each
// against nil, and a bare named type (a local struct, `sql.DB` by value)
// may be a struct that does not compare — and that validateDeps cannot
// reject as nil anyway. Unresolved interface selectors stay on the
// UnresolvedStubs path for the same reason.
func computeUnfilledDeps(handlerDir string) []UnresolvedAutoStub {
	fields, err := ParseServiceDeps(handlerDir)
	if err != nil || len(fields) == 0 {
		return nil
	}
	var out []UnresolvedAutoStub
	for _, f := range fields {
		if f.Optional {
			continue
		}
		t := strings.TrimSpace(f.Type)
		switch f.Name {
		case "Logger", "Config":
			continue // the harness's own defaults
		case "DB":
			if strings.HasSuffix(t, "orm.Context") {
				continue // the harness's ephemeral postgres
			}
		}
		if t == frameworkClockType || t == frameworkIDGenType {
			continue // computeFuncDefaults fills these
		}
		if !nilableBySyntax(t) {
			continue
		}
		out = append(out, UnresolvedAutoStub{FieldName: f.Name, TypeExpr: t})
	}
	return out
}

// nilableBySyntax reports whether a rendered Deps field type is a pointer,
// slice, map, channel or func type — nil-able whatever it points at.
func nilableBySyntax(t string) bool {
	for _, prefix := range []string{"*", "[]", "map[", "chan ", "chan<-", "<-chan", "func("} {
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}

// UnfilledDepsFor describes each required Deps field of the handler in
// handlerDir that the test harness cannot construct, as "Deps.SQL
// (*sql.DB)". The scaffolded handler tests read it: a service with any
// such field gets tests that pass WithPlaceholderDeps(), so the file forge
// writes passes as written.
func UnfilledDepsFor(handlerDir string) []string {
	var out []string
	for _, d := range computeUnfilledDeps(handlerDir) {
		out = append(out, "Deps."+d.FieldName+" ("+d.TypeExpr+")")
	}
	return out
}

// ComponentTestHelperRelPath is the project-relative path of a service's
// generated test-helper file. Exposed so tests (and callers reasoning about
// generated output) name the location in one place rather than rebuilding the
// "internal/handlers/<leaf>/helpers_gen_test.go" string each time.
func ComponentTestHelperRelPath(handlerLeaf string) string {
	return filepath.Join("internal", "handlers", handlerLeaf, componentTestHelperFile)
}

// mountMethodFor names the handler method that mounts svc.
//
// A service may declare an RPC called Register — the obvious name for a
// sign-up endpoint — which collides with the scaffolded Register(mux,
// opts...) mount helper. The handler renames its helper to Mount in that
// case, so every generated call site has to follow.
func mountMethodFor(svc ServiceDef) string {
	for _, m := range svc.Methods {
		if m.GoName() == "Register" {
			return "Mount"
		}
	}
	return "Register"
}
