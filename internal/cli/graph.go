package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// graph.go — `forge project graph`.
//
// Emits a single JSON document describing every resource the project
// declares and the explicit dependency edges between them. The goal is
// "one tool call answers what-depends-on-what" — sub-agents shouldn't
// have to grep forge.yaml + render KCL + walk contract.go themselves.
//
// Sources (no re-derivation; we call existing parsers):
//
//   - forge.yaml       — loadProjectConfig (project, services list,
//                        packages list, frontends list, binaries list).
//   - deploy/kcl/<env> — RenderKCL (deploy type, env vars, gateways,
//                        routes, frontend ports).
//   - contract.go      — codegen.ParseServiceDeps (per-service deps;
//                        each Deps field becomes a service→package edge
//                        when the field's type references a known
//                        internal package).
//   - gen/forge_descriptor.json — loadForgeDescriptor (RPCs per service,
//                        AuthRequired per RPC).
//
// All sub-readers are tolerant: a missing or malformed input degrades
// to a warning in the top-level `warnings` array rather than failing
// the whole command. The model can read the warnings.

// graphDoc is the JSON shape emitted by `forge project graph`. All slice fields
// are omitempty so a CLI-shaped project with no services produces a
// compact document. Edges always present (possibly empty).
type graphDoc struct {
	Project   graphProject    `json:"project"`
	Services  []graphService  `json:"services,omitempty"`
	Frontends []graphFrontend `json:"frontends,omitempty"`
	Packages  []graphPackage  `json:"packages,omitempty"`
	Binaries  []graphBinary   `json:"binaries,omitempty"`
	Gateways  []graphGateway  `json:"gateways,omitempty"`
	Routes    []graphRoute    `json:"routes,omitempty"`
	Edges     []graphEdge     `json:"edges"`
	Warnings  []string        `json:"warnings,omitempty"`
}

// graphProject is the top-level project identity. ModulePath / Kind
// come from forge.yaml; when forge.yaml is missing entirely we still
// emit the section with empty strings so consumers don't have to
// null-check.
type graphProject struct {
	Name       string `json:"name"`
	ModulePath string `json:"module_path"`
	Kind       string `json:"kind"`
}

// graphService is one Connect-RPC service. DeployType is the KCL-level
// placement discriminator ("host"/"cluster"/"external"/"compose"/
// "build-only") and is empty when KCL didn't render the service.
//
// Package is the conventional Go package name for the service —
// handlers/<name> is the canonical layout, mirroring how
// ParseServiceDeps + bootstrap codegen address services.
type graphService struct {
	Name       string           `json:"name"`
	Package    string           `json:"package,omitempty"`
	DeployType string           `json:"deploy_type,omitempty"`
	EnvVars    []graphEnvVar    `json:"env_vars,omitempty"`
	Deps       []graphDepsField `json:"deps,omitempty"`
	RPCs       []graphRPC       `json:"rpcs,omitempty"`
	// Served is the additive types-only marker: present (and false)
	// only when the user-owned pkg/app/services.go has no serviceRow
	// for the service — the types/client generate here but this binary
	// does not serve the API. Absent means served (the default; a
	// missing registration file serves everything).
	Served *bool `json:"served,omitempty"`
}

// graphEnvVar names one env var read by the service. Source is the
// origin within the KCL render — today we surface "kcl" for everything
// rendered out of KCL, leaving room for future discriminators
// (env_file, secret_ref, config_map_ref) without breaking consumers.
type graphEnvVar struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

// graphDepsField is one field on a service's `Deps` struct.
// Package is best-effort resolved by scanning the field's Type string
// for a known internal package name. When no match is found Package
// is empty — the edge is still emitted as a service→type relationship
// the model can act on.
type graphDepsField struct {
	Field   string `json:"field"`
	Type    string `json:"type"`
	Package string `json:"package,omitempty"`
}

// graphRPC is one method on the service. AuthRequired mirrors the
// fail-closed default the proto descriptor extractor applies.
type graphRPC struct {
	Name         string `json:"name"`
	AuthRequired bool   `json:"auth_required"`
}

// graphFrontend is one Next.js / Vite / RN app. Port comes from KCL
// when present; otherwise from forge.yaml as the fallback.
type graphFrontend struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
	Port int    `json:"port,omitempty"`
}

// graphPackage is one internal Go package. Every field comes from the
// package's own source: it exists because internal/<name>/contract.go
// exists, and its Type is "outbound-io" when it carries the
// `//forge:outbound-io` marker, "service" when it does not. Composition says
// whether the server builds it: "composed" (compose.go), "external"
// (`//forge:external-component`, hand-built on Infra) or "not-a-component"
// (`//forge:not-a-component`, not in the server; Reason says who builds it).
type graphPackage struct {
	Name        string `json:"name"`
	Type        string `json:"type,omitempty"`
	Composition string `json:"composition,omitempty"`
	Reason      string `json:"composition_reason,omitempty"`
}

// graphBinary is one non-server long-running binary.
type graphBinary struct {
	Name string `json:"name"`
}

// graphGateway is one Gateway API gateway from KCL. Listeners are
// inlined.
type graphGateway struct {
	Name      string                 `json:"name"`
	Host      string                 `json:"host,omitempty"`
	Listeners []graphGatewayListener `json:"listeners,omitempty"`
}

type graphGatewayListener struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol,omitempty"`
}

// graphRoute is one HTTPRoute / GRPCRoute from KCL. Kind is "http" or
// "grpc". Service names the backend; the edge route→service makes the
// relationship explicit in the edges array.
type graphRoute struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Gateway string `json:"gateway,omitempty"`
	Service string `json:"service,omitempty"`
	Host    string `json:"host,omitempty"`
	Path    string `json:"path,omitempty"`
}

// graphEdge is one explicit dependency relationship. From/To are
// prefixed with the node kind ("service:tasks", "package:repo",
// "route:api", "gateway:web") so consumers don't have to disambiguate
// by name alone.
type graphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

// newGraphCmd is the cobra entry point. `--env` (default "dev")
// selects the KCL env to render — KCL is per-env so the choice is
// required even when the caller doesn't care.
func newGraphCmd() *cobra.Command {
	var env string
	cmd := &cobra.Command{
		Use:   "graph",
		Short: "Emit a JSON dependency graph of the project's declared resources",
		Long: `Emit a single JSON document describing every resource the
project declares (services, packages, frontends, binaries, gateways,
routes) and the explicit dependency edges between them.

Sources are existing forge parsers: forge.yaml, the KCL render for
the selected env, contract.go's Deps struct, and gen/forge_descriptor.json.
Partial-data conditions (missing forge.yaml, KCL fails to render) are
reported via the top-level "warnings" array rather than aborting the
command — consumers should always read warnings before trusting the
graph as complete.

Output goes to stdout; warnings and errors to stderr.

Examples:
  forge project graph                 # dev env
  forge project graph --env=staging   # staging KCL render`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGraph(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), ".", env)
		},
	}
	cmd.Flags().StringVar(&env, "env", "dev", "KCL environment to render")
	return cmd
}

// runGraph builds the graph document and writes it as indented JSON to
// out. Warnings are echoed to errOut so a piped `forge project graph | jq`
// surfaces them without polluting the JSON.
func runGraph(ctx context.Context, out, errOut io.Writer, projectDir, env string) error {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return fmt.Errorf("resolve project dir: %w", err)
	}
	doc := buildGraphDoc(ctx, abs, env)
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("encode graph: %w", err)
	}
	for _, w := range doc.Warnings {
		// Best-effort warning echo to stderr. Failure to write a
		// warning isn't fatal — the JSON already carried it.
		_, _ = fmt.Fprintln(errOut, "warning:", w)
	}
	return nil
}

// buildGraphDoc is the testable core. Pure-ish: side effects are
// confined to the loadProjectConfigFrom + RenderKCL + ParseServiceDeps
// + loadForgeDescriptor calls. Each call's failure is folded into the
// warnings slice rather than propagated, so the caller always gets a
// usable document.
func buildGraphDoc(ctx context.Context, projectDir, env string) graphDoc {
	doc := graphDoc{
		Edges: []graphEdge{}, // always present even when empty
	}

	store, cfgErr := loadProjectStoreFrom(filepath.Join(projectDir, defaultProjectConfigFile))
	var cfg *config.ProjectConfig
	if store != nil {
		cfg = store.Config()
	}
	switch {
	case cfgErr == nil && store != nil:
		meta := store.Meta()
		doc.Project = graphProject{
			Name:       meta.Name,
			ModulePath: meta.ModulePath,
			Kind:       meta.EffectiveKind(),
		}
	case errors.Is(cfgErr, ErrProjectConfigNotFound):
		doc.Warnings = append(doc.Warnings, "forge.yaml not found")
	default:
		doc.Warnings = append(doc.Warnings, fmt.Sprintf("load forge.yaml: %v", cfgErr))
	}

	// KCL render is best-effort: a CLI-kind project with no deploy/kcl
	// directory will fail here, and that's fine — we just emit a
	// warning and continue with whatever forge.yaml gave us.
	var kcl *KCLEntities
	if env != "" {
		k, err := RenderKCL(ctx, projectDir, env)
		if err != nil {
			doc.Warnings = append(doc.Warnings, fmt.Sprintf("render kcl env=%s: %v", env, err))
		} else {
			kcl = k
		}
	}

	// Build a lookup of internal package names so service Deps fields can be
	// matched to a producing package. The set is DISCOVERED from the code
	// (internal/<pkg>/contract.go), which is the same source the bootstrap
	// codegen wires from — so the graph can never omit a package the binary
	// constructs, and each package's Composition (read off the same markers
	// compose.go obeys) says whether the server builds it at all. An unknown
	// type leaves the package field empty and emits a typeless dep edge.
	pkgByName := map[string]graphPackage{}
	discovered, pkgErr := codegen.DiscoverInternalPackages(projectDir)
	if pkgErr != nil {
		doc.Warnings = append(doc.Warnings, fmt.Sprintf("discover internal packages: %v", pkgErr))
	}
	for _, p := range discovered {
		gp := graphPackage{Name: p.Name, Type: p.Type, Composition: p.Composition, Reason: p.CompositionReason}
		pkgByName[p.Name] = gp
		doc.Packages = append(doc.Packages, gp)
	}

	// Descriptor gives us RPCs + AuthRequired per service. Best-effort:
	// projects without `forge generate` having run won't have it yet.
	desc, descErr := loadForgeDescriptor(projectDir)
	if descErr != nil {
		doc.Warnings = append(doc.Warnings, fmt.Sprintf("load forge descriptor: %v", descErr))
	}
	rpcsByService := map[string][]graphRPC{}
	if desc != nil {
		for _, s := range desc.Services {
			// ServiceDef.Name is the proto service name ("TasksService");
			// forge.yaml's service name is typically "tasks". We match
			// on the proto package's last segment too so the lookup
			// works for both shapes.
			for _, m := range s.Methods {
				rpcsByService[s.Name] = append(rpcsByService[s.Name], graphRPC{
					Name:         m.Name,
					AuthRequired: m.AuthRequired,
				})
			}
		}
	}

	// Index KCL services by name so we can attach deploy_type + env_vars.
	kclSvcByName := map[string]*WorkloadEntity{}
	if kcl != nil {
		for i := range kcl.Workloads {
			kclSvcByName[kcl.Workloads[i].Name] = &kcl.Workloads[i]
		}
	}

	// Emit services. The forge.yaml services slice is the canonical
	// inventory; KCL fills in deploy type + env vars; descriptor fills
	// in RPCs; contract.go fills in Deps.
	graphAppendServices(&doc, cfg, projectDir, kclSvcByName, rpcsByService, pkgByName)

	// Frontends: forge.yaml is the inventory; KCL contributes nothing
	// new today beyond what forge.yaml already declares (port + type).
	if cfg != nil {
		kclFrontends := map[string]FrontendEntity{}
		if kcl != nil {
			for _, fe := range kcl.Frontends {
				kclFrontends[fe.Name] = fe
			}
		}
		for _, fe := range cfg.Frontends {
			gf := graphFrontend{
				Name: fe.Name,
				Type: fe.Type,
				Port: fe.Port,
			}
			if k, ok := kclFrontends[fe.Name]; ok && k.Port != 0 {
				gf.Port = k.Port
			}
			doc.Frontends = append(doc.Frontends, gf)
		}
	}

	// Binaries (kind=binary components), enumerated from cmd/ entrypoints.
	if cfg != nil {
		for _, b := range codegen.IntrospectComponents(projectDir) {
			if !b.IsBinary() {
				continue
			}
			doc.Binaries = append(doc.Binaries, graphBinary{Name: b.Name})
		}
	}

	// Gateways + routes from KCL. Routes always produce a route→service
	// edge so the model can answer "what's the request path to <svc>?".
	graphAppendGatewaysAndRoutes(&doc, kcl)

	// Deterministic edge order so test fixtures (and diff-based review
	// of the JSON output) are stable.
	sort.SliceStable(doc.Edges, func(i, j int) bool {
		if doc.Edges[i].From != doc.Edges[j].From {
			return doc.Edges[i].From < doc.Edges[j].From
		}
		if doc.Edges[i].To != doc.Edges[j].To {
			return doc.Edges[i].To < doc.Edges[j].To
		}
		return doc.Edges[i].Kind < doc.Edges[j].Kind
	})

	return doc
}

// graphAppendServices emits the graph's services (and their deps edges) from
// the forge.yaml component inventory, enriched with KCL deploy type + env vars,
// descriptor RPCs, and contract.go Deps. Best-effort throughout: a service
// registry parse failure falls open to "everything served" (graph is a
// read-only inspection surface and must not die mid-migration), and a Deps
// parse error is recorded as a warning rather than aborting. No-op when cfg is
// nil.
func graphAppendServices(doc *graphDoc, cfg *config.ProjectConfig, projectDir string, kclSvcByName map[string]*WorkloadEntity, rpcsByService map[string][]graphRPC, pkgByName map[string]graphPackage) {
	if cfg == nil {
		return
	}
	// Registration view (pkg/app/services.go). Best-effort: a parse
	// failure falls open to "everything served".
	reg, regErr := loadServiceRegistry(projectDir)
	if regErr != nil {
		reg = &serviceRegistry{Exists: false}
	}
	// Inventory is enumerated from the REAL sources (proto descriptor +
	// owned worker/operator files + cmd/ binaries) — see
	// codegen.IntrospectComponents.
	for _, s := range codegen.IntrospectComponents(projectDir) {
		// Binary components are inventoried in the Binaries section, not
		// as graph services.
		if s.IsBinary() {
			continue
		}
		gs := graphService{
			Name:    s.Name,
			Package: servicePackageDir(s),
		}
		if isConnectServiceConfig(s) && !reg.registered(s.Name) {
			notServed := false
			gs.Served = &notServed
		}
		if k, ok := kclSvcByName[s.Name]; ok {
			gs.DeployType = k.Runtime.Type
			for _, ev := range k.EnvVars() {
				gs.EnvVars = append(gs.EnvVars, graphEnvVar{
					Name:   ev.Name,
					Source: "kcl",
				})
			}
		}
		// Match RPCs by proto-service-name suffix. "TasksService"
		// matches forge.yaml's "tasks"; "tasks" matches as-is.
		if rpcs, ok := matchRPCs(rpcsByService, s.Name); ok {
			gs.RPCs = rpcs
		}

		// Parse Deps from the service's package directory.
		pkgDir := filepath.Join(projectDir, s.Path)
		deps, depsErr := codegen.ParseServiceDeps(pkgDir)
		if depsErr != nil && !errors.Is(depsErr, fs.ErrNotExist) {
			doc.Warnings = append(doc.Warnings, fmt.Sprintf("parse deps for service %s: %v", s.Name, depsErr))
		}
		for _, d := range deps {
			pkgName := resolveDepsPackage(d.Type, pkgByName)
			gs.Deps = append(gs.Deps, graphDepsField{
				Field:   d.Name,
				Type:    d.Type,
				Package: pkgName,
			})
			if pkgName != "" {
				doc.Edges = append(doc.Edges, graphEdge{
					From: "service:" + s.Name,
					To:   "package:" + pkgName,
					Kind: "deps",
				})
			}
		}
		doc.Services = append(doc.Services, gs)
	}
}

// graphAppendGatewaysAndRoutes emits the KCL gateways and HTTP/gRPC routes.
// Every route with a service produces a route→service edge (and, with a
// gateway, a route→gateway edge) so the model can answer "what's the request
// path to <svc>?". No-op when kcl is nil.
func graphAppendGatewaysAndRoutes(doc *graphDoc, kcl *KCLEntities) {
	if kcl == nil {
		return
	}
	for _, g := range kcl.Gateways {
		gg := graphGateway{
			Name: g.Name,
			Host: g.Host,
		}
		for _, l := range g.Listeners {
			gg.Listeners = append(gg.Listeners, graphGatewayListener{
				Port:     l.Port,
				Protocol: l.Protocol,
			})
		}
		doc.Gateways = append(doc.Gateways, gg)
	}
	for _, r := range kcl.HTTPRoutes {
		graphAppendRoute(doc, r.Name, "http", r.Gateway, r.Service, r.Host, r.Path)
	}
	for _, r := range kcl.GRPCRoutes {
		graphAppendRoute(doc, r.Name, "grpc", r.Gateway, r.Service, r.Host, r.Path)
	}
}

// graphAppendRoute appends one route (of the given kind) plus its route→service
// and route→gateway edges. The HTTP and gRPC route entities are distinct named
// types with an identical Name/Gateway/Service/Host/Path shape, so both kinds
// flow through here as scalar fields.
func graphAppendRoute(doc *graphDoc, name, kind, gateway, service, host, path string) {
	doc.Routes = append(doc.Routes, graphRoute{
		Name:    name,
		Kind:    kind,
		Gateway: gateway,
		Service: service,
		Host:    host,
		Path:    path,
	})
	if service != "" {
		doc.Edges = append(doc.Edges, graphEdge{
			From: "route:" + name,
			To:   "service:" + service,
			Kind: "routes-to",
		})
	}
	if gateway != "" {
		doc.Edges = append(doc.Edges, graphEdge{
			From: "route:" + name,
			To:   "gateway:" + gateway,
			Kind: "attached-to",
		})
	}
}

// servicePackageDir returns the on-disk package dir for a component —
// either the explicit Path (set by loadProjectConfig's default-fill)
// or the canonical "internal/handlers/<name>" fallback for safety.
func servicePackageDir(s config.ComponentConfig) string {
	if s.Path != "" {
		return s.Path
	}
	return "internal/handlers/" + s.Name
}

// matchRPCs returns the rpcs for a forge.yaml service name. The
// descriptor keys are proto service names ("TasksService"); forge.yaml
// is conventionally lowercase ("tasks"). We try an exact match first,
// then a case-insensitive suffix/prefix scan so both shapes work.
func matchRPCs(byProtoName map[string][]graphRPC, svcName string) ([]graphRPC, bool) {
	if rpcs, ok := byProtoName[svcName]; ok {
		return rpcs, true
	}
	want := strings.ToLower(svcName)
	for proto, rpcs := range byProtoName {
		lp := strings.ToLower(proto)
		if lp == want || lp == want+"service" || strings.HasPrefix(lp, want) {
			return rpcs, true
		}
	}
	return nil, false
}

// resolveDepsPackage scans a Deps field's printed Go type for the name
// of a known internal package. The match is intentionally simple — a
// substring check against each declared package name — because Deps
// types are written either as bare idents ("Repository") or
// selector expressions ("repo.Storer"), and forge.yaml's package
// names are the canonical token in both forms.
//
// Returns "" when no package matches; callers treat that as "edge has
// a type but no producing package known to forge.yaml".
func resolveDepsPackage(typeExpr string, pkgByName map[string]graphPackage) string {
	if typeExpr == "" || len(pkgByName) == 0 {
		return ""
	}
	// Strip leading "*" pointer indicators so "*repo.Storer" matches
	// the same way "repo.Storer" does.
	t := strings.TrimLeft(typeExpr, "*&")
	// Try selector-prefix match first: "repo.Storer" → "repo".
	if idx := strings.Index(t, "."); idx > 0 {
		head := t[:idx]
		if _, ok := pkgByName[head]; ok {
			return head
		}
	}
	// Fall back to substring scan. Deterministic order so the same
	// type expression always resolves to the same package across runs.
	names := make([]string, 0, len(pkgByName))
	for n := range pkgByName {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if n != "" && strings.Contains(t, n) {
			return n
		}
	}
	return ""
}
