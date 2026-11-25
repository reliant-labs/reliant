// Package cli — `forge project audit` ingress category (KCL-entity-typed half).
//
// auditIngress / ingressBackendNames / crossCheckIngress stay in package
// cli because they depend on the KCL render + entity structs (GatewayEntity
// / HTTPRouteEntity / GRPCRouteEntity) shared by ~12 cli files
// (build/deploy/dev/doctor). The audit command group (internal/cli/audit)
// reaches auditIngress through factory.AuditAPI.Ingress, so it never touches
// these entity types. They return the neutral audittype.Category the group
// consumes.
package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/reliant-labs/forge/internal/cli/audittype"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"
)

// auditIngress cross-checks the project's backends against the dev-env
// KCL-declared Gateway API ingress (Gateways + HTTPRoutes + GRPCRoutes).
// The components are DISCOVERED from the project tree, not read off the
// config — the audit reports on what the project actually contains.
// Two failure modes:
//
//   - A route's `Service` doesn't match any known backend name —
//     components, frontends, or webhook handlers (error — the route is
//     dead at deploy time).
//   - A SERVER component nothing routes to (info — internal-only
//     services are valid; we just surface the gap so the operator
//     notices when they meant to ingress it). Only servers get a k8s
//     Service, so only servers can be the target of a route.
//
// Frontends and webhook services are valid route backends too — at the
// k8s layer a route's `backendRefs[].name` resolves to any Service in
// the env namespace regardless of what scaffolded it. We only emit the
// "port but no route" info for components: frontends own their own
// scaffold and may legitimately be cluster-internal-only (SSR-only) so
// flagging them would be noisy.
//
// We render the dev env because that's the only env every project is
// guaranteed to have. If `kcl` isn't on PATH or the dev dir is missing
// (CI environments without the toolchain), the category degrades to
// warn rather than failing the whole audit.
func auditIngress(cfg *config.ProjectConfig, projectDir string) audittype.Category {
	if cfg == nil {
		return audittype.Category{Status: audittype.StatusError, Summary: "no forge.yaml"}
	}
	entities, err := RenderKCL(context.Background(), projectDir, "dev")
	if err != nil {
		return audittype.Category{
			Status:  audittype.StatusWarn,
			Summary: fmt.Sprintf("could not evaluate dev KCL: %v", err),
		}
	}
	components := codegen.DiscoverProjectComponents(projectDir, cfg.Name)
	backends := ingressBackendNames(components, cfg.Frontends, projectDir)
	// A route may legally target any Service in the env namespace — not
	// just the ones forge scaffolds. Union the KCL-RENDERED
	// Service names so hand-authored Services resolve as known backends:
	//   - entities.Workloads — every declared workload.
	//   - entities.ManifestServiceNames — raw k8s Service manifests injected
	//     via `additional_manifests` (e.g. the Service fronting a
	//     forge.Operator, which forge itself emits no Service for).
	// Without this the cross-check false-errors "route X references unknown
	// service X" for any operator-fronting or hand-authored Service.
	backends = append(backends, entities.WorkloadNames("")...)
	backends = append(backends, entities.ManifestServiceNames...)
	return crossCheckIngress(components, backends, entities.Gateways, entities.HTTPRoutes, entities.GRPCRoutes)
}

// ingressBackendNames returns the union of every name that can legitimately
// appear as a route backend: components, frontends, and per-service webhook
// handlers. K8s only sees a Service in the env namespace by that name — what
// scaffolded it is irrelevant at route-resolution time.
func ingressBackendNames(components codegen.Inventory, frontends []config.FrontendConfig, projectDir string) []string {
	names := make([]string, 0, len(components)+len(frontends))
	for _, s := range components {
		names = append(names, s.Name)
		// Webhook backends are discovered from the webhook_<name>.go files in
		// the service's handler dir — the file IS the declaration.
		handlerDir := filepath.Join(projectDir, "internal", "handlers", naming.ServicePackage(s.Name))
		names = append(names, codegen.WebhookNamesForService(handlerDir)...)
	}
	for _, f := range frontends {
		names = append(names, f.Name)
	}
	return names
}

// crossCheckIngress is the pure decision core of auditIngress: takes
// the resolved services / known-backend set / gateways / routes and
// returns the audittype.Category. Split out so unit tests can exercise
// the cross-check without shelling kcl. `backends` is the union of names
// (forge.yaml services + frontends + webhook handlers, PLUS the
// KCL-rendered Service objects — typed forge.Service and raw k8s Service
// manifests) any route may legally point at; `services` is kept separate
// because only it drives the "serves but no route" info finding.
func crossCheckIngress(services codegen.Inventory, backends []string, gateways []GatewayEntity, httpRoutes []HTTPRouteEntity, grpcRoutes []GRPCRouteEntity) audittype.Category {
	knownBackend := make(map[string]struct{}, len(backends))
	for _, b := range backends {
		knownBackend[b] = struct{}{}
	}

	routedService := map[string]struct{}{}
	var findings []string
	hasError := false

	check := func(routeKind, name, svcRef string) {
		if svcRef == "" {
			return
		}
		routedService[svcRef] = struct{}{}
		if _, ok := knownBackend[svcRef]; !ok {
			findings = append(findings, fmt.Sprintf("error: %s %s references unknown service %s", routeKind, name, svcRef))
			hasError = true
		}
	}
	for _, r := range httpRoutes {
		check("route", r.Name, r.Service)
	}
	for _, r := range grpcRoutes {
		check("route", r.Name, r.Service)
	}

	// Only SERVER components are addressable: their expansion is the one
	// that emits a k8s Service. Workers, crons, operators and binaries
	// have nothing for a route to point at, so "no route" says nothing
	// about them. Every server answers on the same port — the binary's
	// single Connect mux (config.DefaultServePort) unless the env's KCL
	// overlay says otherwise, which this audit does not read.
	servicesWithoutRoute := 0
	for _, s := range services {
		if !s.IsServer() {
			continue
		}
		if _, ok := routedService[s.Name]; ok {
			continue
		}
		findings = append(findings, fmt.Sprintf("info: service %s serves :%d but no ingress route targets it — cluster-internal only", s.Name, config.DefaultServePort))
		servicesWithoutRoute++
	}

	sort.Strings(findings)

	status := audittype.StatusOK
	if hasError {
		status = audittype.StatusError
	}
	summary := fmt.Sprintf("%d gateway(s), %d route(s); %d service(s) without route",
		len(gateways), len(httpRoutes)+len(grpcRoutes), servicesWithoutRoute)
	details := map[string]any{
		"gateways":               len(gateways),
		"http_routes":            len(httpRoutes),
		"grpc_routes":            len(grpcRoutes),
		"services_without_route": servicesWithoutRoute,
	}
	if len(findings) > 0 {
		details["findings"] = findings
	}
	return audittype.Category{Status: status, Summary: summary, Details: details}
}
