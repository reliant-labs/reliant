// Package cli — `forge doctor` ingress checks (Phase 4 of the Gateway
// API ingress refactor).
//
// Two new check categories layered on top of the standard doctor
// signal-based checks:
//
//   - GatewayClass present — for each unique gateway_class_name
//     declared across every env's rendered KCL, verify a matching
//     `GatewayClass` resource exists in the active cluster context.
//   - cert-manager ClusterIssuer present — for each Gateway with
//     tls.cert_issuer set in any env's KCL, verify a `ClusterIssuer`
//     resource with that name exists.
//
// Both checks are best-effort observation: they degrade to "skipped"
// with a clear reason when kubectl isn't on PATH, the cluster isn't
// reachable, the CRD itself isn't installed, or KCL can't be
// evaluated. Doctor must never fail-loud on environmental gaps the
// user hasn't fixed yet.
//
// The cross-check logic is factored into a pure helper
// (buildIngressDoctorChecks) that takes already-collected class /
// issuer names plus a kubectl-resource-checker interface, so unit
// tests can exercise every status outcome without a live cluster.
package cli

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/doctor"
)

// resourceLookupStatus is the outcome of looking up a single
// cluster-scoped Gateway API / cert-manager resource by name.
type resourceLookupStatus int

const (
	// resourceFound — kubectl returned the resource name. Healthy.
	resourceFound resourceLookupStatus = iota
	// resourceNotFound — kubectl ran but the named resource is absent.
	resourceNotFound
	// resourceCRDMissing — the resource's CRD itself isn't installed
	// (kubectl returns "no matches for kind"). For ClusterIssuer this
	// means cert-manager isn't installed; for GatewayClass it means
	// the Gateway API CRDs aren't applied yet.
	resourceCRDMissing
	// resourceClusterUnreachable — kubectl couldn't talk to the
	// configured context (connection refused, no current context, etc).
	resourceClusterUnreachable
	// resourceKubectlMissing — `kubectl` isn't on PATH at all.
	resourceKubectlMissing
)

// resourceChecker is the seam between buildIngressDoctorChecks and the
// real kubectl invocation. The interface lets unit tests inject a
// stub map[name]status without forking processes.
type resourceChecker interface {
	// CheckGatewayClass reports whether a cluster-scoped
	// GatewayClass with the given name exists.
	CheckGatewayClass(ctx context.Context, name string) resourceLookupStatus
	// CheckClusterIssuer reports whether a cert-manager ClusterIssuer
	// with the given name exists.
	CheckClusterIssuer(ctx context.Context, name string) resourceLookupStatus
}

// kubectlResourceChecker is the production resourceChecker. It shells
// `kubectl get <kind> <name> -o jsonpath={.metadata.name}` and maps
// kubectl's exit code + stderr into a resourceLookupStatus. The two
// methods share kubectlGet because the only thing that varies is the
// kind ("gatewayclass" vs "clusterissuer").
type kubectlResourceChecker struct{}

// CheckGatewayClass implements resourceChecker via kubectl.
func (kubectlResourceChecker) CheckGatewayClass(ctx context.Context, name string) resourceLookupStatus {
	return kubectlGet(ctx, "gatewayclass", name)
}

// CheckClusterIssuer implements resourceChecker via kubectl.
func (kubectlResourceChecker) CheckClusterIssuer(ctx context.Context, name string) resourceLookupStatus {
	return kubectlGet(ctx, "clusterissuer", name)
}

// kubectlGet runs `kubectl get <kind> <name> -o jsonpath={.metadata.name}`
// and dispatches on the result. We probe LookPath first because exec
// errors don't reliably distinguish "kubectl missing" from "kubectl
// crashed" — explicit detection gives a cleaner skip reason.
func kubectlGet(ctx context.Context, kind, name string) resourceLookupStatus {
	if _, err := exec.LookPath("kubectl"); err != nil {
		return resourceKubectlMissing
	}
	cmd := exec.CommandContext(ctx, "kubectl", "get", kind, name, "-o", "jsonpath={.metadata.name}")
	out, err := cmd.CombinedOutput()
	if err == nil {
		// Empty stdout with exit 0 shouldn't happen for a real resource;
		// treat it as not-found defensively.
		if strings.TrimSpace(string(out)) == "" {
			return resourceNotFound
		}
		return resourceFound
	}
	stderr := strings.ToLower(string(out))
	// Order matters — "no matches for kind" indicates the CRD is
	// missing; we check that before the generic "not found" fallback
	// because the kubectl message text contains both substrings for
	// some kubectl versions.
	switch {
	case strings.Contains(stderr, "no matches for kind"),
		strings.Contains(stderr, "the server doesn't have a resource type"):
		return resourceCRDMissing
	case strings.Contains(stderr, "not found"):
		return resourceNotFound
	case strings.Contains(stderr, "connection refused"),
		strings.Contains(stderr, "no such host"),
		strings.Contains(stderr, "current-context"),
		strings.Contains(stderr, "context was not found"),
		strings.Contains(stderr, "unable to connect to the server"),
		strings.Contains(stderr, "i/o timeout"):
		return resourceClusterUnreachable
	default:
		// Any other failure mode — RBAC denied, malformed kubeconfig,
		// etc. — we surface as cluster-unreachable so the user gets a
		// "skipped" result rather than a confusing pass/fail. Doctor
		// is observation, not diagnosis.
		return resourceClusterUnreachable
	}
}

// collectIngressInputsFromKCL renders every env declared via
// deploy/kcl/<env>/main.k and aggregates the distinct
// gateway_class_name values + tls.cert_issuer values across all of
// them. Production might use a different gateway class than dev, so
// we must walk every env rather than only dev.
//
// Render failures for individual envs are tolerated — we collect
// whatever succeeded and return a list of degraded-env reasons the
// caller can surface as "skipped" details. Total failure (no envs at
// all, or every env render errors) returns empty slices + the reasons
// so the caller emits a "skipped" check rather than a false ok.
func collectIngressInputsFromKCL(ctx context.Context, projectDir string) (classes, issuers []string, reasons []string) {
	envs, err := ListEnvs(projectDir)
	if err != nil {
		return nil, nil, []string{fmt.Sprintf("env discovery failed: %v", err)}
	}
	if len(envs) == 0 {
		return nil, nil, []string{"no environments declared (no deploy/kcl/<env>/main.k)"}
	}
	classSet := map[string]struct{}{}
	issuerSet := map[string]struct{}{}
	for _, env := range envs {
		entities, err := RenderKCL(ctx, projectDir, env)
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("env %s KCL render failed: %v", env, err))
			continue
		}
		for _, gw := range entities.Gateways {
			if c := strings.TrimSpace(gw.GatewayClassName); c != "" {
				classSet[c] = struct{}{}
			}
			if gw.TLS != nil {
				if iss := strings.TrimSpace(gw.TLS.CertIssuer); iss != "" {
					issuerSet[iss] = struct{}{}
				}
			}
		}
	}
	for c := range classSet {
		classes = append(classes, c)
	}
	for i := range issuerSet {
		issuers = append(issuers, i)
	}
	sort.Strings(classes)
	sort.Strings(issuers)
	return classes, issuers, reasons
}

// installHintForGatewayClass returns the install-action hint to surface
// when a GatewayClass is missing. The class name is the source of
// truth: forge ships "eg" (Envoy Gateway) via `forge cluster up` — the
// SINGLE controller every forge env uses (local k3d AND cloud); other
// production classes ("gke-gateway", "aws-gateway", "istio", ...) need an
// out-of-band controller install.
func installHintForGatewayClass(className string) string {
	switch strings.ToLower(strings.TrimSpace(className)) {
	case "eg", "envoy-gateway":
		return "run `forge cluster up` to install Envoy Gateway + the eg GatewayClass"
	case "traefik":
		return "Traefik is no longer the forge dev controller — re-render with gateway_class_name = \"eg\" (the default) and run `forge cluster up`"
	case "gke-gateway", "gke-l7-global-external-managed",
		"gke-l7-regional-external-managed", "gke-l7-cross-regional":
		return "install the GKE Gateway controller (gcloud container clusters update --gateway-api=standard)"
	case "aws-gateway":
		return "install the AWS Gateway API Controller (kubectl apply -k github.com/aws/aws-application-networking-k8s)"
	case "istio":
		return "install Istio with `istioctl install` (Gateway API support requires Istio >= 1.16)"
	default:
		return fmt.Sprintf("install the controller that provides the %q GatewayClass", className)
	}
}

// buildIngressDoctorChecks is the pure decision core. Given a
// resolved set of class names, issuer names, any
// rendering-degraded-env reasons, and a resourceChecker, it returns
// the two doctor.CheckResult values for the ingress section. Pure:
// no I/O, no project-dir access, fully unit-testable from a stub
// resourceChecker.
//
// The caller decides whether to invoke this at all — when
// features.ingress is off, the caller skips both checks entirely so
// they don't show up in the report rather than emitting "skipped"
// noise.
func buildIngressDoctorChecks(ctx context.Context, classes, issuers, renderReasons []string, rc resourceChecker) []doctor.CheckResult {
	gcResult := buildGatewayClassCheck(ctx, classes, renderReasons, rc)
	ciResult := buildClusterIssuerCheck(ctx, issuers, renderReasons, rc)
	return []doctor.CheckResult{gcResult, ciResult}
}

// buildGatewayClassCheck verifies every distinct class name. The
// detail line per check uses the severity-prefix convention so grep
// over evidence stays tidy ("error: GatewayClass \"traefik\" ...").
func buildGatewayClassCheck(ctx context.Context, classes, renderReasons []string, rc resourceChecker) doctor.CheckResult {
	result := doctor.CheckResult{
		Name:   "GatewayClass",
		Status: doctor.StatusPass,
	}
	if len(classes) == 0 {
		result.Status = doctor.StatusSkip
		if len(renderReasons) > 0 {
			result.Message = "no gateway classes to verify"
			result.Evidence = strings.Join(renderReasons, "\n")
		} else {
			result.Message = "no Gateway resources declare gateway_class_name"
		}
		return result
	}

	var lines []string
	var missing, crdMissing int
	skipReason := ""
	for _, name := range classes {
		switch rc.CheckGatewayClass(ctx, name) {
		case resourceFound:
			lines = append(lines, fmt.Sprintf("ok: GatewayClass %q present", name))
		case resourceNotFound:
			missing++
			lines = append(lines, fmt.Sprintf("error: GatewayClass %q not found — %s", name, installHintForGatewayClass(name)))
		case resourceCRDMissing:
			crdMissing++
			lines = append(lines, fmt.Sprintf("error: GatewayClass CRD missing — Gateway API CRDs not installed; %s", installHintForGatewayClass(name)))
		case resourceKubectlMissing:
			skipReason = "kubectl not on PATH"
		case resourceClusterUnreachable:
			skipReason = "kubectl context not reachable"
		}
		if skipReason != "" {
			break
		}
	}

	if skipReason != "" {
		result.Status = doctor.StatusSkip
		result.Message = skipReason
		result.Evidence = strings.Join(append([]string{"skipped: " + skipReason}, renderReasons...), "\n")
		return result
	}
	if missing > 0 || crdMissing > 0 {
		result.Status = doctor.StatusFail
		switch {
		case crdMissing > 0 && missing == 0:
			result.Message = fmt.Sprintf("Gateway API CRDs missing (%d class(es) unverifiable)", crdMissing)
		case crdMissing > 0:
			result.Message = fmt.Sprintf("%d GatewayClass(es) missing, %d CRD-blocked", missing, crdMissing)
		default:
			result.Message = fmt.Sprintf("%d GatewayClass(es) missing", missing)
		}
	} else {
		result.Message = fmt.Sprintf("%d GatewayClass(es) present", len(classes))
	}
	if len(renderReasons) > 0 {
		lines = append(lines, renderReasons...)
	}
	result.Evidence = strings.Join(lines, "\n")
	return result
}

// buildClusterIssuerCheck verifies every distinct cert_issuer name.
// CRD-missing here implies cert-manager itself isn't installed — we
// surface that as a single clear hint rather than per-issuer noise.
func buildClusterIssuerCheck(ctx context.Context, issuers, renderReasons []string, rc resourceChecker) doctor.CheckResult {
	result := doctor.CheckResult{
		Name:   "ClusterIssuer",
		Status: doctor.StatusPass,
	}
	if len(issuers) == 0 {
		result.Status = doctor.StatusSkip
		if len(renderReasons) > 0 {
			result.Message = "no cert issuers to verify"
			result.Evidence = strings.Join(renderReasons, "\n")
		} else {
			result.Message = "no Gateways declare tls.cert_issuer"
		}
		return result
	}

	var lines []string
	var missing int
	crdMissing := false
	skipReason := ""
	for _, name := range issuers {
		switch rc.CheckClusterIssuer(ctx, name) {
		case resourceFound:
			lines = append(lines, fmt.Sprintf("ok: ClusterIssuer %q present", name))
		case resourceNotFound:
			missing++
			lines = append(lines, fmt.Sprintf("error: ClusterIssuer %q not found — create it or fix the Gateway tls.cert_issuer reference", name))
		case resourceCRDMissing:
			// One CRD-missing hit is dispositive for every issuer.
			crdMissing = true
		case resourceKubectlMissing:
			skipReason = "kubectl not on PATH"
		case resourceClusterUnreachable:
			skipReason = "kubectl context not reachable"
		}
		if skipReason != "" || crdMissing {
			break
		}
	}

	if skipReason != "" {
		result.Status = doctor.StatusSkip
		result.Message = skipReason
		result.Evidence = strings.Join(append([]string{"skipped: " + skipReason}, renderReasons...), "\n")
		return result
	}
	if crdMissing {
		result.Status = doctor.StatusFail
		result.Message = "cert-manager not installed (ClusterIssuer CRD missing)"
		lines = append(lines, "error: cert-manager not installed — install cert-manager (kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml)")
		if len(renderReasons) > 0 {
			lines = append(lines, renderReasons...)
		}
		result.Evidence = strings.Join(lines, "\n")
		return result
	}
	if missing > 0 {
		result.Status = doctor.StatusFail
		result.Message = fmt.Sprintf("%d ClusterIssuer(s) missing", missing)
	} else {
		result.Message = fmt.Sprintf("%d ClusterIssuer(s) present", len(issuers))
	}
	if len(renderReasons) > 0 {
		lines = append(lines, renderReasons...)
	}
	result.Evidence = strings.Join(lines, "\n")
	return result
}

// runIngressDoctorChecks is the side-effecting wrapper invoked from
// runDoctor. Returns nil (no checks appended) when features.ingress
// is off — keeps the report tidy for projects that aren't using
// Gateway API ingress at all.
//
// `signal` mirrors the existing doctor signal-filter behaviour: only
// run ingress checks when signal is empty ("all") or equals
// Runs only in the unfiltered pass: unknown signals are rejected by
// doctor.RunFiltered before this is reached, so we only ever see one of
// the values it accepts.
func runIngressDoctorChecks(ctx context.Context, cfg *config.ProjectConfig, projectDir, signal string) []doctor.CheckResult {
	if cfg == nil || !cfg.Features.IngressEnabled() {
		return nil
	}
	// Only the unfiltered pass. `--signal` selects among the values
	// doctor.RunFiltered accepts (metrics/traces/logs/profiles/deploy); it
	// REJECTS anything else before a check runs, so a guard naming its own
	// signal here could never be true and advertised a filter that does not
	// exist.
	if signal != "" {
		return nil
	}
	classes, issuers, reasons := collectIngressInputsFromKCL(ctx, projectDir)
	if len(classes) == 0 && len(issuers) == 0 && len(reasons) > 0 {
		// No data collected and we know why — emit a single skipped
		// check rather than two redundant ones.
		return []doctor.CheckResult{{
			Name:     "ingress",
			Status:   doctor.StatusSkip,
			Message:  "could not evaluate KCL",
			Evidence: strings.Join(reasons, "\n"),
			Duration: 0,
		}}
	}
	start := time.Now()
	results := buildIngressDoctorChecks(ctx, classes, issuers, reasons, kubectlResourceChecker{})
	// Apportion total duration evenly; doctor.runCheck normally sets
	// per-check duration but we're outside that orchestration loop.
	per := time.Since(start) / time.Duration(len(results))
	for i := range results {
		results[i].Duration = per
	}
	return results
}

// appendIngressChecksToReport mutates report to add the ingress
// checks and re-rolls the Overall status. Failure of an ingress
// check escalates StatusPass → StatusFail just like the standard
// checks; warnings escalate to StatusWarn. Skipped never changes
// overall status.
func appendIngressChecksToReport(report *doctor.Report, extras []doctor.CheckResult) {
	if len(extras) == 0 {
		return
	}
	report.Checks = append(report.Checks, extras...)
	for _, r := range extras {
		switch r.Status {
		case doctor.StatusFail:
			report.Overall = doctor.StatusFail
		case doctor.StatusWarn:
			if report.Overall == doctor.StatusPass {
				report.Overall = doctor.StatusWarn
			}
		}
	}
}
