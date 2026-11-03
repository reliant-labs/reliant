// File: internal/codegen/inventory_gen.go
//
// The DATA-ONLY inventory — the introspection + selection half of the
// hybrid model (FORGE_SHAPE_REDESIGN §2). GenerateInventory emits
// internal/app/inventory_gen.go: a `var Inventory = []ComponentInfo{...}`
// where each ComponentInfo is a pure descriptor (Name, ConnectPath, Kind)
// plus a typed Mount closure over the assembled *Services.
//
// This SPLITS appkit.Def's dual role. appkit.ServiceDef.Construct was both
// the inventory row AND the string-keyed constructor table (appkit.Run
// walked it constructing everything by name). Construction now lives
// entirely in the generated Build (inject_gen.go); the inventory is a pure
// descriptor. Names live HERE only — for display (`forge project map`/`audit`, CLI
// listing) and for choosing which subset to MOUNT per-subcommand — NEVER as
// a construction key.
//
// The Mount closure is a typed function over the constructed *Services: it
// registers one service's Connect + HTTP routes on a mux. The cmd layer
// (PASS 2) selects which Mount funcs to call per subcommand and composes
// them onto the cmd-owned mux, preserving the interceptor ordering. In
// PASS 1 the inventory is additive (data + closures that compile) — the
// cmd flip to mount-via-inventory lands in PASS 2.
//
// Only services produce a mountable inventory row (workers/operators are
// not mounted on the HTTP mux; they are supervised by serverkit). Their
// presence is still discoverable via the build plan; the inventory is the
// HTTP-mount surface.

package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/templates"
)

// InventoryServiceData is one service's rendered inventory row + Mount
// closure inputs.
type InventoryServiceData struct {
	// Name is the runtime kebab name — DISPLAY + selection only.
	Name string
	// FieldName is the exported field on *Services holding the instance.
	FieldName string
	// MountMethod is the method this file calls to mount the service —
	// "Register" normally, "Mount" when the service declares an RPC of its
	// own called Register. One type cannot carry both a Register(mux,
	// opts...) helper and a Register(ctx, req) RPC, and the RPC wins.
	MountMethod string
	// Package is the Go package clause.
	Package string
	// ConnectPkg / ProtoServiceName drive the ConnectPath descriptor and,
	// when REST is on, the connect import. Mirrors the bootstrap fields.
	// ProtoServiceName is the RAW proto service name: protoc-gen-connect-go
	// spells the `<Service>Name` constant from it rather than from the
	// GoName (naming.ConnectServiceNameConst).
	ConnectPkg       string
	ProtoServiceName string
	// BaseService and Version carry the proto identity SPLIT into its
	// version-independent logical name and its proto API version (e.g.
	// proto package "billing.v1" -> BaseService "billing", Version "v1").
	// VERSION-AWARE SEAM (FORGE_SHAPE_REDESIGN — version-aware registry):
	// today identity fuses the version (the v1 rides in ConnectPath/ConnectPkg
	// and the import path), so a future `billing.v2` would register as a
	// SEPARATE service. Recording the version as EXPLICIT METADATA here — a
	// field, not an opaque part of identity — makes v2 an ADDITIVE change
	// later (a second Version on the same BaseService) rather than a breaking
	// redesign. It does NOT change today's behavior: ConnectPath, the mount
	// path, and the field keying are byte-identical for v1 projects; this is
	// pure additive metadata. Version is "" for an unversioned proto package.
	//
	// DEFERRED (NOT in this seam): per-version handler generation /
	// per-version mount adapters. When multi-version lands, the cmd layer
	// will group Inventory rows by BaseService and mount each Version's
	// ConnectPath on its own route; the Mount closure and a per-version
	// Services field are the extension points. Until then a project has at
	// most one Version per BaseService and the grouping is a no-op.
	BaseService string
	Version     string
	// HasWebhooks gates the webhook-route registration in the Mount body.
	HasWebhooks bool
	// Audience is the trust domain the service declares (`(forge.v1.service)
	// .audience`), "user" when it declares none: which route set mounts it.
	Audience string
}

// serviceAudienceOrUser is a service's audience, defaulting to "user" the way
// serverkit.ServiceAudience does at boot.
func serviceAudienceOrUser(a string) string {
	if a == "" {
		return "user"
	}
	return a
}

// InventoryGenData is the rendered template input for mounts_services_gen.go.tmpl.
type InventoryGenData struct {
	Module      string
	RESTEnabled bool
	Services    []InventoryServiceData
	// ConnectImports are the *v1connect import lines needed for the
	// ConnectPath descriptor constants (and REST). Deduped + sorted.
	ConnectImports []string
}

// GenerateInventory emits internal/app/mounts_services.go: the typed
// per-service Mount<Svc> methods over *Components, the typed MountByName map,
// MountAll, and the data-only `var Inventory = []ComponentInfo{...}` that
// introspection (forge project map / audit / services listing) reads. It is ALWAYS
// written when internal/app is emitted (no len(Services)==0 early-return):
// cmd/server.go references app.Inventory / the typed mounts unconditionally,
// so the symbols must exist even with no Connect services.
func GenerateInventory(in InventoryGenInput) error {
	appDir := filepath.Join(in.ProjectDir, "internal", "app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		return err
	}

	restEnabled := projectAPIRESTEnabled(in.ProjectDir)

	rows, imports, err := inventoryRows(in)
	if err != nil {
		return err
	}

	data := InventoryGenData{
		Module:         in.ModulePath,
		RESTEnabled:    restEnabled,
		Services:       rows,
		ConnectImports: imports,
	}

	content, err := templates.ProjectTemplates().Render("mounts_services_gen.go.tmpl", data)
	if err != nil {
		return fmt.Errorf("render mounts_services_gen.go.tmpl: %w", err)
	}
	// Renamed to _gen so the name states the tier: this file is a pure
	// projection of the discovered service set, rewritten on every run, and a
	// reader could not tell that from the old spelling without opening it.
	RetireRenamedGenerated(in.ProjectDir, filepath.Join("internal", "app", "mounts_services.go"), in.Checksums)
	if err := writeForgeOwned(in.ProjectDir, filepath.Join("internal", "app", "mounts_services_gen.go"), content, in.Checksums); err != nil {
		return fmt.Errorf("write internal/app/mounts_services_gen.go: %w", err)
	}
	return nil
}

// inventoryRows derives one row per service — the collision-aware field
// name, its mount method, its Connect identity and audience — plus the
// *connect imports those rows name. Shared by the legacy mount surface and
// the composition graph's route sets, so both name every service alike.
func inventoryRows(in InventoryGenInput) ([]InventoryServiceData, []string, error) {
	// Service field naming must agree with inject_gen / bootstrap, so reuse
	// the same collision counts (services + packages + workers + operators).
	svcResolved := make([]ResolvedComponent, 0, len(in.Services))
	for _, svc := range in.Services {
		res, err := ResolveServiceComponent(in.ProjectDir, svc.Name)
		if err != nil {
			return nil, nil, err
		}
		svcResolved = append(svcResolved, res)
	}
	svcComponents := make([]BootstrapServiceData, 0, len(in.Services))
	for _, res := range svcResolved {
		svcComponents = append(svcComponents, BootstrapServiceData{Package: res.PackageName})
	}
	counts := CollisionCounts(svcComponents, in.Packages, in.Workers, in.Operators)

	var (
		rows           []InventoryServiceData
		connectImports = map[string]bool{}
	)
	for i, svc := range in.Services {
		res := svcResolved[i]
		pkg := res.PackageName
		fallbackField := naming.ToPascalCase(strings.TrimSuffix(svc.Name, "Service"))
		if fallbackField == "" {
			fallbackField = naming.ToPascalCase(svc.Name)
		}
		// The alias half is discarded: this file no longer imports the handler
		// packages (see mounts_services_gen.go.tmpl). FieldName still has to
		// come from the same resolver so it keys the *Components fields
		// identically to compose.go / inject_gen.
		_, fieldName := ResolveCollisionNaming(pkg, fallbackField, "svc", counts)
		runtimeName := naming.ToKebabCase(strings.TrimSuffix(svc.Name, "Service"))
		if runtimeName == "" {
			runtimeName = naming.ToKebabCase(svc.Name)
		}

		var connectPkg, connectImport string
		// A descriptor-backed service names its proto service exactly; a
		// synthesized one (no descriptor yet) is spelled the way the proto
		// template will declare it.
		protoServiceName := fallbackField + "Service"
		if svc.GoPackage != "" && svc.PkgName != "" {
			connectPkg = naming.ConnectPackage(svc.PkgName)
			connectImport = svc.GoPackage + "/" + connectPkg
			protoServiceName = svc.Name
		} else {
			synth := naming.ServicePackage(svc.Name)
			connectPkg = synth + "v1connect"
			connectImport = in.ModulePath + "/gen/services/" + synth + "/v1/" + connectPkg
		}
		connectImports[connectImport] = true

		// Version-aware seam: split the proto identity into its
		// version-independent base + the proto API version. The version flows
		// from the descriptor's proto package (svc.Package, e.g. "billing.v1").
		// runtimeName is already the version-INDEPENDENT kebab service name, so
		// it is the BaseService; Version is purely additive metadata (see the
		// InventoryServiceData doc). Empty Version for an unversioned package.
		protoVersion := naming.ProtoPackageVersion(svc.Package)

		// A service may declare an RPC called Register — the obvious name for
		// a sign-up endpoint — which collides with the scaffolded mount
		// helper. The handler renames its helper to Mount in that case, so
		// this call site has to follow.
		mountMethod := mountMethodFor(svc)

		rows = append(rows, InventoryServiceData{
			Name:             runtimeName,
			FieldName:        fieldName,
			MountMethod:      mountMethod,
			Package:          pkg,
			ConnectPkg:       connectPkg,
			ProtoServiceName: protoServiceName,
			BaseService:      runtimeName,
			Version:          protoVersion,
			HasWebhooks:      in.WebhookServices[naming.ServicePackage(svc.Name)],
			Audience:         serviceAudienceOrUser(svc.Audience),
		})
	}

	imports := make([]string, 0, len(connectImports))
	for imp := range connectImports {
		imports = append(imports, imp)
	}
	sort.Strings(imports)
	return rows, imports, nil
}

// InventoryGenInput carries everything GenerateInventory needs. Mirrors the
// bootstrap/inject inputs so naming stays in lockstep.
type InventoryGenInput struct {
	GenContext
	Services        []ServiceDef
	Packages        []BootstrapPackageData
	Workers         []BootstrapWorkerData
	Operators       []BootstrapOperatorData
	WebhookServices map[string]bool
}

// compile-time guard: keep checksums import used even if the writer call
// shape changes during the staged rollout.
var _ = checksums.WriteGeneratedFile
