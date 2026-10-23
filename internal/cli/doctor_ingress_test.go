package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/doctor"
)

// stubResourceChecker is a deterministic resourceChecker that
// returns canned statuses keyed by name. Lets tests assert every
// outcome (found / not-found / CRD-missing / kubectl-missing /
// cluster-unreachable) without shelling kubectl.
type stubResourceChecker struct {
	classes map[string]resourceLookupStatus
	issuers map[string]resourceLookupStatus
	// fallback used when a name isn't in the map. Defaults to
	// resourceNotFound so unconfigured names fail loud.
	fallback resourceLookupStatus
}

func (s stubResourceChecker) CheckGatewayClass(_ context.Context, name string) resourceLookupStatus {
	if v, ok := s.classes[name]; ok {
		return v
	}
	return s.fallback
}

func (s stubResourceChecker) CheckClusterIssuer(_ context.Context, name string) resourceLookupStatus {
	if v, ok := s.issuers[name]; ok {
		return v
	}
	return s.fallback
}

// TestBuildIngressDoctorChecks_AllPresent confirms the happy path:
// every class + issuer resolves, both checks return pass.
func TestBuildIngressDoctorChecks_AllPresent(t *testing.T) {
	rc := stubResourceChecker{
		classes: map[string]resourceLookupStatus{"traefik": resourceFound},
		issuers: map[string]resourceLookupStatus{"letsencrypt-prod": resourceFound},
	}
	results := buildIngressDoctorChecks(context.Background(),
		[]string{"traefik"}, []string{"letsencrypt-prod"}, nil, rc)
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	for _, r := range results {
		if r.Status != doctor.StatusPass {
			t.Errorf("check %q status = %q, want pass; evidence=%q", r.Name, r.Status, r.Evidence)
		}
	}
}

// TestBuildIngressDoctorChecks_GatewayClassMissing — missing class
// becomes status=fail with the install-hint baked into the evidence.
func TestBuildIngressDoctorChecks_GatewayClassMissing(t *testing.T) {
	rc := stubResourceChecker{
		classes: map[string]resourceLookupStatus{"traefik": resourceNotFound},
	}
	results := buildIngressDoctorChecks(context.Background(),
		[]string{"traefik"}, nil, nil, rc)
	var gc doctor.CheckResult
	for _, r := range results {
		if r.Name == "GatewayClass" {
			gc = r
		}
	}
	if gc.Status != doctor.StatusFail {
		t.Fatalf("GatewayClass status = %q, want fail", gc.Status)
	}
	if !strings.Contains(gc.Evidence, "forge cluster up") {
		t.Errorf("evidence should hint at `forge cluster up` for traefik; got: %s", gc.Evidence)
	}
	if !strings.Contains(gc.Evidence, "error:") {
		t.Errorf("evidence should carry severity-prefixed line; got: %s", gc.Evidence)
	}
}

// TestBuildIngressDoctorChecks_GKEGatewayClassHint pins the
// per-provider install hint dispatch — gke-gateway must not get the
// traefik hint.
func TestBuildIngressDoctorChecks_GKEGatewayClassHint(t *testing.T) {
	rc := stubResourceChecker{
		classes: map[string]resourceLookupStatus{"gke-gateway": resourceNotFound},
	}
	results := buildIngressDoctorChecks(context.Background(),
		[]string{"gke-gateway"}, nil, nil, rc)
	gc := results[0]
	if !strings.Contains(gc.Evidence, "GKE Gateway controller") {
		t.Errorf("expected GKE hint; got: %s", gc.Evidence)
	}
	if strings.Contains(gc.Evidence, "forge cluster up") {
		t.Errorf("must not suggest forge cluster up for gke-gateway; got: %s", gc.Evidence)
	}
}

// TestBuildIngressDoctorChecks_AWSGatewayClassHint pins the
// aws-gateway hint shape.
func TestBuildIngressDoctorChecks_AWSGatewayClassHint(t *testing.T) {
	rc := stubResourceChecker{
		classes: map[string]resourceLookupStatus{"aws-gateway": resourceNotFound},
	}
	results := buildIngressDoctorChecks(context.Background(),
		[]string{"aws-gateway"}, nil, nil, rc)
	if !strings.Contains(results[0].Evidence, "AWS Gateway API Controller") {
		t.Errorf("expected AWS Gateway API Controller hint; got: %s", results[0].Evidence)
	}
}

// TestBuildIngressDoctorChecks_ClusterIssuerMissing — missing issuer
// becomes fail with a useful hint.
func TestBuildIngressDoctorChecks_ClusterIssuerMissing(t *testing.T) {
	rc := stubResourceChecker{
		issuers: map[string]resourceLookupStatus{"letsencrypt-prod": resourceNotFound},
	}
	results := buildIngressDoctorChecks(context.Background(),
		nil, []string{"letsencrypt-prod"}, nil, rc)
	var ci doctor.CheckResult
	for _, r := range results {
		if r.Name == "ClusterIssuer" {
			ci = r
		}
	}
	if ci.Status != doctor.StatusFail {
		t.Fatalf("ClusterIssuer status = %q, want fail", ci.Status)
	}
	if !strings.Contains(ci.Evidence, "letsencrypt-prod") {
		t.Errorf("evidence should name the missing issuer; got: %s", ci.Evidence)
	}
	if !strings.Contains(ci.Evidence, "error:") {
		t.Errorf("evidence should carry severity prefix; got: %s", ci.Evidence)
	}
}

// TestBuildIngressDoctorChecks_CertManagerCRDMissing — CRD-missing
// surfaces a clear "install cert-manager" hint, distinct from
// "issuer not found".
func TestBuildIngressDoctorChecks_CertManagerCRDMissing(t *testing.T) {
	rc := stubResourceChecker{
		issuers: map[string]resourceLookupStatus{"letsencrypt-prod": resourceCRDMissing},
	}
	results := buildIngressDoctorChecks(context.Background(),
		nil, []string{"letsencrypt-prod"}, nil, rc)
	var ci doctor.CheckResult
	for _, r := range results {
		if r.Name == "ClusterIssuer" {
			ci = r
		}
	}
	if ci.Status != doctor.StatusFail {
		t.Fatalf("ClusterIssuer status = %q, want fail", ci.Status)
	}
	if !strings.Contains(strings.ToLower(ci.Message), "cert-manager") {
		t.Errorf("message should mention cert-manager; got: %s", ci.Message)
	}
	if !strings.Contains(ci.Evidence, "install cert-manager") {
		t.Errorf("evidence should hint at installing cert-manager; got: %s", ci.Evidence)
	}
}

// TestBuildIngressDoctorChecks_GatewayAPICRDMissing — when the
// GatewayClass CRD itself is missing, the user needs to install the
// Gateway API CRDs (typically via forge cluster up).
func TestBuildIngressDoctorChecks_GatewayAPICRDMissing(t *testing.T) {
	rc := stubResourceChecker{
		classes: map[string]resourceLookupStatus{"traefik": resourceCRDMissing},
	}
	results := buildIngressDoctorChecks(context.Background(),
		[]string{"traefik"}, nil, nil, rc)
	gc := results[0]
	if gc.Status != doctor.StatusFail {
		t.Fatalf("GatewayClass status = %q, want fail", gc.Status)
	}
	if !strings.Contains(gc.Evidence, "Gateway API CRDs not installed") {
		t.Errorf("evidence should mention missing Gateway API CRDs; got: %s", gc.Evidence)
	}
}

// TestBuildIngressDoctorChecks_KubectlMissing — when kubectl isn't on
// PATH, both checks degrade to skipped with a clear reason.
func TestBuildIngressDoctorChecks_KubectlMissing(t *testing.T) {
	rc := stubResourceChecker{
		classes: map[string]resourceLookupStatus{"traefik": resourceKubectlMissing},
		issuers: map[string]resourceLookupStatus{"letsencrypt-prod": resourceKubectlMissing},
	}
	results := buildIngressDoctorChecks(context.Background(),
		[]string{"traefik"}, []string{"letsencrypt-prod"}, nil, rc)
	for _, r := range results {
		if r.Status != doctor.StatusSkip {
			t.Errorf("check %q status = %q, want skip", r.Name, r.Status)
		}
		if !strings.Contains(r.Message, "kubectl") {
			t.Errorf("check %q skip message should mention kubectl; got: %s", r.Name, r.Message)
		}
	}
}

// TestBuildIngressDoctorChecks_ClusterUnreachable — the cluster
// connection error path also yields skip, not fail. Doctor must not
// fail-loud just because no kubeconfig is configured.
func TestBuildIngressDoctorChecks_ClusterUnreachable(t *testing.T) {
	rc := stubResourceChecker{
		classes: map[string]resourceLookupStatus{"traefik": resourceClusterUnreachable},
	}
	results := buildIngressDoctorChecks(context.Background(),
		[]string{"traefik"}, nil, nil, rc)
	if results[0].Status != doctor.StatusSkip {
		t.Errorf("status = %q, want skip on cluster-unreachable", results[0].Status)
	}
}

// TestBuildIngressDoctorChecks_NoInputsSkips — empty inputs skip the
// individual check rather than fail or pass-with-zero.
func TestBuildIngressDoctorChecks_NoInputsSkips(t *testing.T) {
	results := buildIngressDoctorChecks(context.Background(), nil, nil, nil, stubResourceChecker{})
	for _, r := range results {
		if r.Status != doctor.StatusSkip {
			t.Errorf("check %q status = %q on empty inputs, want skip", r.Name, r.Status)
		}
	}
}

// TestRunIngressDoctorChecks_FeatureOffReturnsNil — when
// features.ingress=false, the wrapper returns no checks at all (not
// even skipped placeholders). Doctor output stays tidy for projects
// that aren't using Gateway API ingress.
func TestRunIngressDoctorChecks_FeatureOffReturnsNil(t *testing.T) {
	// Ingress is experimental — default-off, no explicit opt-out
	// needed to exercise the "ingress off" path.
	cfg := &config.ProjectConfig{Name: "t"}
	results := runIngressDoctorChecks(context.Background(), cfg, t.TempDir(), "")
	if results != nil {
		t.Errorf("want nil results when ingress is off; got %d", len(results))
	}
}

// TestRunIngressDoctorChecks_SignalFilter — non-empty, non-"ingress"
// signal also suppresses the ingress checks (the user asked for
// metrics-only output; ingress is irrelevant).
func TestRunIngressDoctorChecks_SignalFilter(t *testing.T) {
	cfg := &config.ProjectConfig{
		Name:     "t",
		Features: config.FeaturesConfig{}.With(config.FeatureIngress, true),
	}
	results := runIngressDoctorChecks(context.Background(), cfg, t.TempDir(), "metrics")
	if results != nil {
		t.Errorf("want nil results when signal=metrics; got %d", len(results))
	}
}

// TestRunIngressDoctorChecks_KCLFailureSurfacedAsSkip — when no
// envs are configured (no deploy/kcl/<env>/main.k), we emit a single
// skipped ingress check with the reason rather than failing.
func TestRunIngressDoctorChecks_KCLFailureSurfacedAsSkip(t *testing.T) {
	cfg := &config.ProjectConfig{
		Name:     "t",
		Features: config.FeaturesConfig{}.With(config.FeatureIngress, true),
	}
	results := runIngressDoctorChecks(context.Background(), cfg, t.TempDir(), "")
	if len(results) != 1 {
		t.Fatalf("want 1 result (consolidated skip); got %d", len(results))
	}
	if results[0].Status != doctor.StatusSkip {
		t.Errorf("status = %q, want skip", results[0].Status)
	}
}

// TestCollectIngressInputsFromKCL_AggregatesAcrossEnvs uses a
// FORGE_KCL_RENDER_FIXTURE to drive RenderKCL deterministically for
// two distinct envs. Confirms class + issuer aggregation is
// dedup'd and sorted.
func TestCollectIngressInputsFromKCL_AggregatesAcrossEnvs(t *testing.T) {
	// Build a project tree with two envs (dev, prod). Their main.k
	// content is irrelevant because we override RenderKCL via the
	// fixture env var — but the files must exist for ListEnvs.
	dir := t.TempDir()
	for _, env := range []string{"dev", "prod"} {
		envDir := filepath.Join(dir, "deploy", "kcl", env)
		if err := os.MkdirAll(envDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(envDir, "main.k"), []byte("// stub\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Same fixture for both envs — the aggregator should dedup the
	// shared "traefik" class while still keeping the per-env issuer
	// distinction (here both envs reference the same issuer; dedup'd).
	fixture := filepath.Join(dir, "fixture.json")
	body := `{
		"gateways": [
			{"name": "edge", "gateway_class_name": "traefik",
			 "tls": {"cert_issuer": "letsencrypt-prod", "secret_name": "tls"}},
			{"name": "internal", "gateway_class_name": "traefik"}
		]
	}`
	if err := os.WriteFile(fixture, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", fixture)

	classes, issuers, reasons := collectIngressInputsFromKCL(context.Background(), dir)
	if len(reasons) != 0 {
		t.Errorf("want no render reasons; got %v", reasons)
	}
	if len(classes) != 1 || classes[0] != "traefik" {
		t.Errorf("classes = %v, want [traefik]", classes)
	}
	if len(issuers) != 1 || issuers[0] != "letsencrypt-prod" {
		t.Errorf("issuers = %v, want [letsencrypt-prod]", issuers)
	}
}

// TestCollectIngressInputsFromKCL_DistinctClassesAcrossEnvs — when
// dev and prod render different KCL bundles we'd see two classes;
// here we exercise the multi-class branch with a single fixture that
// declares both. (The fixture mechanism doesn't vary per env, so we
// stuff both classes into the same render — sufficient to confirm
// dedup leaves distinct entries alone.)
func TestCollectIngressInputsFromKCL_DistinctClassesAcrossEnvs(t *testing.T) {
	dir := t.TempDir()
	envDir := filepath.Join(dir, "deploy", "kcl", "dev")
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envDir, "main.k"), []byte("// stub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(dir, "fixture.json")
	body := `{
		"gateways": [
			{"name": "edge",     "gateway_class_name": "traefik"},
			{"name": "edge-gke", "gateway_class_name": "gke-gateway"}
		]
	}`
	if err := os.WriteFile(fixture, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", fixture)

	classes, _, _ := collectIngressInputsFromKCL(context.Background(), dir)
	if len(classes) != 2 {
		t.Fatalf("want 2 distinct classes; got %v", classes)
	}
	// sort.Strings → ["gke-gateway", "traefik"]
	if classes[0] != "gke-gateway" || classes[1] != "traefik" {
		t.Errorf("classes not sorted as expected; got %v", classes)
	}
}

// TestAppendIngressChecksToReport_FailEscalatesOverall — a failed
// ingress check must escalate report.Overall from Pass → Fail.
func TestAppendIngressChecksToReport_FailEscalatesOverall(t *testing.T) {
	report := &doctor.Report{Overall: doctor.StatusPass}
	appendIngressChecksToReport(report, []doctor.CheckResult{
		{Name: "GatewayClass", Status: doctor.StatusFail},
	})
	if report.Overall != doctor.StatusFail {
		t.Errorf("overall = %q, want fail", report.Overall)
	}
}

// TestAppendIngressChecksToReport_SkipDoesNotEscalate — skipped
// checks are observational and must not change overall status.
func TestAppendIngressChecksToReport_SkipDoesNotEscalate(t *testing.T) {
	report := &doctor.Report{Overall: doctor.StatusPass}
	appendIngressChecksToReport(report, []doctor.CheckResult{
		{Name: "GatewayClass", Status: doctor.StatusSkip},
		{Name: "ClusterIssuer", Status: doctor.StatusSkip},
	})
	if report.Overall != doctor.StatusPass {
		t.Errorf("overall = %q, want pass (skips must not escalate)", report.Overall)
	}
	if len(report.Checks) != 2 {
		t.Errorf("expected 2 checks appended; got %d", len(report.Checks))
	}
}

// TestInstallHintForGatewayClass_DispatchTable pins the lookup table
// so adding a new provider keeps the existing matches intact.
func TestInstallHintForGatewayClass_DispatchTable(t *testing.T) {
	cases := map[string]string{
		"traefik":     "forge cluster up",
		"gke-gateway": "GKE Gateway",
		"aws-gateway": "AWS Gateway API Controller",
		"unknown-xyz": "unknown-xyz", // fallback path quotes the name
	}
	for in, want := range cases {
		got := installHintForGatewayClass(in)
		if !strings.Contains(got, want) {
			t.Errorf("hint for %q = %q, want substring %q", in, got, want)
		}
	}
}
