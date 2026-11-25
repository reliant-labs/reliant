package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/audittype"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// auditIngress / crossCheckIngress / ingressBackendNames stay in package
// cli (audit_ingress_cli.go) because they depend on the KCL render + entity
// structs; the audit command group reaches auditIngress through
// factory.AuditAPI.Ingress. The buildAuditReport-level feature-gate test
// moved to package audit alongside the report assembly.

// TestCrossCheckIngress_UnknownService asserts that routes referencing
// a non-existent backend produce an error-level finding and
// flip the category status to error.
func TestCrossCheckIngress_UnknownService(t *testing.T) {
	services := []config.ComponentConfig{
		{Name: "api", Kind: config.ComponentKindServer},
	}
	routes := []HTTPRouteEntity{
		{Name: "api-route", Service: "api", Port: 8080},
		{Name: "ghost-route", Service: "ghost", Port: 9000},
	}
	cat := crossCheckIngress(services, []string{"api"}, nil, routes, nil)
	if cat.Status != audittype.StatusError {
		t.Fatalf("status = %q, want error", cat.Status)
	}
	findings, ok := cat.Details["findings"].([]string)
	if !ok {
		t.Fatalf("findings missing/wrong type: %T", cat.Details["findings"])
	}
	var sawErr bool
	for _, f := range findings {
		if strings.HasPrefix(f, "error: ") && strings.Contains(f, "ghost-route") && strings.Contains(f, "ghost") {
			sawErr = true
		}
	}
	if !sawErr {
		t.Errorf("expected error finding for ghost-route -> ghost; got %v", findings)
	}
}

// TestCrossCheckIngress_ServiceWithoutRoute asserts that a SERVER with no
// matching route produces an info-level finding and keeps status at ok
// (cluster-internal services are valid). The finding names the port the
// server actually answers on — the binary's single Connect mux.
func TestCrossCheckIngress_ServiceWithoutRoute(t *testing.T) {
	services := []config.ComponentConfig{
		{Name: "api", Kind: config.ComponentKindServer},
		{Name: "internal-only", Kind: config.ComponentKindServer},
	}
	routes := []HTTPRouteEntity{
		{Name: "api-route", Service: "api", Port: 8080},
	}
	cat := crossCheckIngress(services, []string{"api", "internal-only"}, nil, routes, nil)
	if cat.Status != audittype.StatusOK {
		t.Fatalf("status = %q, want ok (info findings shouldn't downgrade)", cat.Status)
	}
	findings, _ := cat.Details["findings"].([]string)
	var sawInfo bool
	want := fmt.Sprintf(":%d", config.DefaultServePort)
	for _, f := range findings {
		if strings.HasPrefix(f, "info: ") && strings.Contains(f, "internal-only") && strings.Contains(f, want) {
			sawInfo = true
		}
	}
	if !sawInfo {
		t.Errorf("expected info finding for internal-only naming %s; got %v", want, findings)
	}
	if got, _ := cat.Details["services_without_route"].(int); got != 1 {
		t.Errorf("services_without_route = %v, want 1", cat.Details["services_without_route"])
	}
}

// TestCrossCheckIngress_NonServersSkipped asserts that components which get
// no k8s Service — workers, crons, operators, binaries — never produce a "no
// route" info line. Nothing can route to them, so their absence from the
// route table says nothing.
//
// The empty-Kind case is deliberate and load-bearing: EffectiveKind defaults
// to "server", so a component that states no kind IS a server and MUST be
// reported.
func TestCrossCheckIngress_NonServersSkipped(t *testing.T) {
	services := []config.ComponentConfig{
		{Name: "sync", Kind: config.ComponentKindWorker},
		{Name: "nightly", Kind: config.ComponentKindCron},
		{Name: "controller", Kind: config.ComponentKindOperator},
		{Name: "proxy", Kind: config.ComponentKindBinary},
	}
	cat := crossCheckIngress(services, []string{"sync", "nightly", "controller", "proxy"}, nil, nil, nil)
	if cat.Status != audittype.StatusOK {
		t.Errorf("status = %q, want ok", cat.Status)
	}
	if _, ok := cat.Details["findings"]; ok {
		t.Errorf("unexpected findings for non-server components: %v", cat.Details["findings"])
	}

	unkinded := []config.ComponentConfig{{Name: "api"}}
	cat = crossCheckIngress(unkinded, []string{"api"}, nil, nil, nil)
	if got, _ := cat.Details["services_without_route"].(int); got != 1 {
		t.Errorf("an unkinded component defaults to server and must be reported; services_without_route = %v",
			cat.Details["services_without_route"])
	}
}

// TestCrossCheckIngress_GRPCRoutesAlsoChecked asserts the GRPCRoute
// branch is wired symmetrically with HTTPRoute — unknown svc refs in
// a GRPCRoute escalate to error too.
func TestCrossCheckIngress_GRPCRoutesAlsoChecked(t *testing.T) {
	services := []config.ComponentConfig{{Name: "api", Kind: config.ComponentKindServer}}
	grpc := []GRPCRouteEntity{
		{Name: "grpc-ghost", Service: "ghost", Port: 9000},
	}
	cat := crossCheckIngress(services, []string{"api"}, nil, nil, grpc)
	if cat.Status != audittype.StatusError {
		t.Errorf("status = %q, want error", cat.Status)
	}
}

// TestAuditIngress_KCLRenderFailureWarn confirms that when RenderKCL
// itself fails (no kcl on PATH / no deploy/kcl/dev), the category
// degrades to warn — audit must keep running in CI environments that
// don't ship the kcl toolchain.
func TestAuditIngress_KCLRenderFailureWarn(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.ProjectConfig{
		Name:     "t",
		Features: config.FeaturesConfig{}.With(config.FeatureIngress, true),
	}
	cat := auditIngress(cfg, dir) // no deploy/kcl/dev → RenderKCL errors
	if cat.Status != audittype.StatusWarn {
		t.Errorf("status = %q, want warn (no dev KCL)", cat.Status)
	}
	if !strings.Contains(cat.Summary, "could not evaluate dev KCL") {
		t.Errorf("summary should mention dev KCL failure; got %q", cat.Summary)
	}
}

// TestCrossCheckIngress_SummaryFormat pins the summary shape so
// downstream consumers (CI dashboards, doctor output) get a stable
// human-readable string format. Three slots: gateway count, route
// count (http+grpc), services-without-route count.
func TestCrossCheckIngress_SummaryFormat(t *testing.T) {
	services := []config.ComponentConfig{
		{Name: "api", Kind: config.ComponentKindServer},
		{Name: "internal", Kind: config.ComponentKindServer},
	}
	gws := []GatewayEntity{{Name: "edge"}}
	http := []HTTPRouteEntity{{Name: "api-route", Service: "api", Port: 8080}}
	grpc := []GRPCRouteEntity{{Name: "api-grpc", Service: "api", Port: 9090}}

	cat := crossCheckIngress(services, []string{"api", "internal"}, gws, http, grpc)
	if !strings.Contains(cat.Summary, "1 gateway") {
		t.Errorf("summary should mention 1 gateway; got %q", cat.Summary)
	}
	if !strings.Contains(cat.Summary, "2 route") {
		t.Errorf("summary should mention 2 routes (http+grpc); got %q", cat.Summary)
	}
	if !strings.Contains(cat.Summary, "1 service") {
		t.Errorf("summary should mention 1 service-without-route; got %q", cat.Summary)
	}
}

// TestCrossCheckIngress_FrontendBackend asserts a route pointing at a
// frontend by name passes the cross-check — frontends are valid K8s
// Service targets too. The "serves but no route" info finding
// stays scoped to the discovered components, so an SSR-only frontend doesn't get
// flagged as a gap.
func TestCrossCheckIngress_FrontendBackend(t *testing.T) {
	services := []config.ComponentConfig{}
	backends := []string{"web"}
	routes := []HTTPRouteEntity{
		{Name: "web-route", Service: "web", Port: 3000},
	}
	cat := crossCheckIngress(services, backends, nil, routes, nil)
	if cat.Status != audittype.StatusOK {
		t.Fatalf("status = %q, want ok (frontend is a valid backend)", cat.Status)
	}
	if findings, ok := cat.Details["findings"].([]string); ok {
		for _, f := range findings {
			if strings.HasPrefix(f, "error: ") {
				t.Errorf("unexpected error finding: %q", f)
			}
		}
	}
}

// TestCrossCheckIngress_WebhookBackend asserts a route pointing at a
// webhook handler name (declared under a service's webhooks: block)
// passes the cross-check — at the k8s layer a webhook handler is just
// another Service in the namespace.
func TestCrossCheckIngress_WebhookBackend(t *testing.T) {
	services := []config.ComponentConfig{{Name: "api", Kind: config.ComponentKindServer}}
	backends := []string{"api", "stripe"}
	routes := []HTTPRouteEntity{
		{Name: "stripe-webhook", Service: "stripe", Port: 8080},
	}
	cat := crossCheckIngress(services, backends, nil, routes, nil)
	if cat.Status != audittype.StatusOK {
		t.Fatalf("status = %q, want ok (webhook is a valid backend)", cat.Status)
	}
	if findings, ok := cat.Details["findings"].([]string); ok {
		for _, f := range findings {
			if strings.HasPrefix(f, "error: ") {
				t.Errorf("unexpected error finding: %q", f)
			}
		}
	}
}

// TestCrossCheckIngress_UnknownNonBackend asserts the negative case:
// a route pointing at a name that's neither service, frontend, nor
// webhook still flips the category to error.
func TestCrossCheckIngress_UnknownNonBackend(t *testing.T) {
	services := []config.ComponentConfig{{Name: "api", Kind: config.ComponentKindServer}}
	backends := []string{"api", "web", "stripe"}
	routes := []HTTPRouteEntity{
		{Name: "ghost-route", Service: "nobody", Port: 8080},
	}
	cat := crossCheckIngress(services, backends, nil, routes, nil)
	if cat.Status != audittype.StatusError {
		t.Fatalf("status = %q, want error (unknown backend)", cat.Status)
	}
}

// TestCrossCheckIngress_RenderedManifestBackend asserts that a route
// targeting a Service that exists ONLY as a KCL-rendered manifest — not
// as a forge.yaml component/frontend/webhook — resolves as a known
// backend and does NOT error. This is the operator-fronting case:
// forge.Operator emits no Service, so a hand-authored raw k8s Service
// manifest (surfaced via KCLEntities.ManifestServiceNames, unioned into
// `backends` by auditIngress) is the only place its name appears.
func TestCrossCheckIngress_RenderedManifestBackend(t *testing.T) {
	// No forge.yaml components at all — the backend is purely a rendered
	// Service (as auditIngress would union in from
	// entities.ManifestServiceNames / entities.Services).
	services := []config.ComponentConfig{}
	backends := []string{"workspace-controller"}
	routes := []HTTPRouteEntity{
		{Name: "wc-route", Service: "workspace-controller", Port: 9191},
	}
	grpc := []GRPCRouteEntity{
		{Name: "wc-grpc", Service: "workspace-controller", Port: 9191},
	}
	cat := crossCheckIngress(services, backends, nil, routes, grpc)
	if cat.Status != audittype.StatusOK {
		t.Fatalf("status = %q, want ok (rendered Service is a valid backend)", cat.Status)
	}
	if findings, ok := cat.Details["findings"].([]string); ok {
		for _, f := range findings {
			if strings.HasPrefix(f, "error: ") {
				t.Errorf("unexpected error finding for rendered-manifest backend: %q", f)
			}
		}
	}
}

// TestManifestServiceNamesFromOuter covers the render-layer extraction of
// raw k8s Service names from the outer `manifests` stream — the seam that
// carries operator-fronting / hand-authored Services (which have no typed
// forge entity) into the ingress known-backend set. Only kind=="Service"
// objects contribute; other kinds and unnamed objects are ignored.
func TestManifestServiceNamesFromOuter(t *testing.T) {
	outer := []byte(`{"output": {
		"manifests": [
			{"kind": "Deployment", "metadata": {"name": "workspace-controller"}},
			{"kind": "Service", "metadata": {"name": "workspace-controller"}},
			{"kind": "Service", "metadata": {"name": "admin-server"}},
			{"kind": "Service", "metadata": {"name": ""}},
			{"kind": "ConfigMap", "metadata": {"name": "cfg"}}
		]
	}}`)
	ents, err := parseKCLEntities(outer)
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	got := ents.ManifestServiceNames
	want := map[string]bool{"workspace-controller": true, "admin-server": true}
	if len(got) != len(want) {
		t.Fatalf("got %d names %v, want %d", len(got), got, len(want))
	}
	for _, n := range got {
		if !want[n] {
			t.Errorf("unexpected Service name %q in %v", n, got)
		}
	}
}

// TestIngressBackendNames covers the union built in auditIngress —
// services, per-service webhook handlers, and frontends all surface in
// the resulting set. Order doesn't matter, just membership.
func TestIngressBackendNames(t *testing.T) {
	dir := t.TempDir()
	// Webhooks are discovered from the webhook_<name>.go files on disk, not a
	// declared config list.
	apiDir := filepath.Join(dir, "internal", "handlers", "api")
	if err := os.MkdirAll(apiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, wh := range []string{"stripe", "github"} {
		if err := os.WriteFile(filepath.Join(apiDir, "webhook_"+wh+".go"), []byte("package api\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	components := codegen.Inventory{{Name: "api"}, {Name: "worker"}}
	frontends := []config.FrontendConfig{{Name: "web"}, {Name: "admin"}}
	got := ingressBackendNames(components, frontends, dir)
	want := map[string]bool{"api": true, "worker": true, "stripe": true, "github": true, "web": true, "admin": true}
	seen := map[string]bool{}
	for _, n := range got {
		seen[n] = true
	}
	for k := range want {
		if !seen[k] {
			t.Errorf("missing backend %q in %v", k, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d backends, want %d: %v", len(got), len(want), got)
	}
}
