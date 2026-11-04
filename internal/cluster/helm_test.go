package cluster

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- pure helpers -----------------------------------------------------------

func TestStripHelmOCIStatus_Helm4Receipt(t *testing.T) {
	in := `Pulled: docker.io/envoyproxy/gateway-helm:v1.7.2
Digest: sha256:02cfb2d2e9a31386f850a1ef3ab2a7a18a489eb2140a35c700c73b9b72b231f8
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: envoy-gateway`

	got := stripHelmOCIStatus(in)
	if strings.Contains(got, "Pulled:") || strings.Contains(got, "Digest:") {
		t.Fatalf("Helm 4 OCI receipt survived:\n%s", got)
	}
	if !strings.Contains(got, "apiVersion: v1") || !strings.Contains(got, "kind: ServiceAccount") {
		t.Fatalf("Kubernetes manifest was not preserved:\n%s", got)
	}
}

func TestStripHelmOCIStatus_PreservesOrdinaryManifest(t *testing.T) {
	in := `apiVersion: v1
kind: ConfigMap
metadata:
  name: Pulled`
	if got := stripHelmOCIStatus(in); got != in {
		t.Fatalf("ordinary manifest changed:\n%s", got)
	}
}

// TestStampAppLabel_OverridesEveryDoc proves the helm-as-a-RENDERER
// bridge: every manifest a chart renders is FORCED into the chart's group
// (WorkloadLabel) so the SAME exclusive --target axis (SelectManifestsByGroup)
// selects the WHOLE chart as one group — even the chart's sub-components that
// label THEMSELVES (cert-manager's webhook / cainjector), which would
// otherwise be dropped by --target=cert-manager. The sub-component's own
// `app.kubernetes.io/name` is what its Service selector matches, so it must
// come through untouched.
func TestStampAppLabel_OverridesEveryDoc(t *testing.T) {
	in := `apiVersion: v1
kind: ServiceAccount
metadata:
  name: cert-manager
  namespace: cert-manager
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cert-manager-webhook
  namespace: cert-manager
  labels:
    app.kubernetes.io/name: webhook
spec: {}`

	got := stampAppLabel(in, "cert-manager", "")

	// The chart's own sub-component label ("webhook") is what its selectors
	// key on; routing must not rewrite it.
	if !strings.Contains(got, `app.kubernetes.io/name: webhook`) {
		t.Errorf("chart sub-component name label was rewritten; its selectors would stop matching:\n%s", got)
	}
	// A doc with no name label is defaulted to the chart name.
	if !strings.Contains(got, "app.kubernetes.io/name: cert-manager") {
		t.Errorf("unlabelled chart doc was not defaulted to the chart name:\n%s", got)
	}
	if strings.Count(got, WorkloadLabel+": cert-manager") != 2 {
		t.Errorf("every chart doc must carry the chart's group on %s:\n%s", WorkloadLabel, got)
	}

	// Round-trip through SelectManifestsByGroup: a --target=cert-manager
	// deploy must KEEP EVERY doc — the whole chart selects as one group.
	kept := SelectManifestsByGroup(got, []string{"cert-manager"})
	if !strings.Contains(kept, "kind: ServiceAccount") || !strings.Contains(kept, "cert-manager-webhook") {
		t.Errorf("every chart doc must be selected by --target=cert-manager:\n%s", kept)
	}
}

// TestPartitionCRDs_SplitsCRDsFromRest pins the CRD/rest split the
// ordered apply depends on.
func TestPartitionCRDs_SplitsCRDsFromRest(t *testing.T) {
	in := `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: certificates.cert-manager.io
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cert-manager
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: issuers.cert-manager.io`

	crds, _, rest := partitionEarlyBatch(in)
	if strings.Count(crds, "kind: CustomResourceDefinition") != 2 {
		t.Errorf("expected 2 CRDs in crds partition:\n%s", crds)
	}
	if strings.Contains(crds, "kind: Deployment") {
		t.Errorf("Deployment leaked into crds partition:\n%s", crds)
	}
	if !strings.Contains(rest, "kind: Deployment") {
		t.Errorf("Deployment missing from rest partition:\n%s", rest)
	}

	names := crdNames(in)
	want := []string{"certificates.cert-manager.io", "issuers.cert-manager.io"}
	if len(names) != len(want) {
		t.Fatalf("crdNames = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("crdNames[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

// TestSelectHelmChartsByGroup_UniformExclusiveRule proves a chart obeys the
// SAME uniform exclusive --target rule as any manifest (a chart's Name is
// its group): EVERY chart on a bare deploy (no targets — the full
// declarative reconcile), EXACTLY the named chart when targeted, and NO
// chart when the target names something else (its manifests are selected by
// SelectManifestsByGroup instead, not here).
func TestSelectHelmChartsByGroup_UniformExclusiveRule(t *testing.T) {
	charts := []HelmChartSpec{
		{Name: "cert-manager"},
		{Name: "envoy-gateway"},
	}

	// No targets → ALL charts (bare `forge env deploy <env>` reconciles every
	// declared platform dep — the uniform "no target = everything" rule).
	sel := selectHelmChartsByGroup(charts, nil)
	if len(sel) != 2 {
		t.Errorf("empty targets must select EVERY chart, got %v", sel)
	}

	// --target=cert-manager → exactly that chart.
	sel = selectHelmChartsByGroup(charts, []string{"cert-manager"})
	if len(sel) != 1 || sel[0].Name != "cert-manager" {
		t.Errorf("expected only cert-manager selected, got %v", sel)
	}

	// Mixed: chart + app name → only the chart is selected here (the app
	// name selects manifests via SelectManifestsByGroup, not charts).
	sel = selectHelmChartsByGroup(charts, []string{"envoy-gateway", "api"})
	if len(sel) != 1 || sel[0].Name != "envoy-gateway" {
		t.Errorf("expected only envoy-gateway selected, got %v", sel)
	}

	// A target naming no chart → no chart rendered.
	sel = selectHelmChartsByGroup(charts, []string{"api"})
	if len(sel) != 0 {
		t.Errorf("a target naming no chart must select no chart, got %v", sel)
	}
}

// TestNamespaceManifest_EmittedAndStamped proves K1: forge synthesizes the
// chart's target Namespace (helm template never emits it) and stampAppLabel
// rides it on the chart's --target group.
func TestNamespaceManifest_EmittedAndStamped(t *testing.T) {
	ns := namespaceManifest("cert-manager", nil, nil)
	m, ok := parseDoc(ns)
	if !ok || m.Kind != "Namespace" || m.Metadata.Name != "cert-manager" {
		t.Fatalf("namespaceManifest must emit a Namespace named cert-manager; got:\n%s", ns)
	}
	// Empty namespace => no manifest.
	if got := namespaceManifest("", nil, nil); got != "" {
		t.Errorf("empty namespace must emit nothing; got %q", got)
	}

	// Stamped, the Namespace must select under the chart's --target.
	stamped := stampAppLabel(ns, "cert-manager", "")
	kept := SelectManifestsByGroup(stamped, []string{"cert-manager"})
	km, ok := parseDoc(kept)
	if !ok || km.Kind != "Namespace" {
		t.Errorf("Namespace must ride the chart's --target group:\n%s", kept)
	}
}

// TestPartitionEarlyBatch_NamespaceWithCRDs proves the Namespace lands in the
// early batch (alongside CRDs), NOT in rest — so applyCRDsThenRest applies it
// before the chart's namespaced resources.
func TestPartitionEarlyBatch_NamespaceWithCRDs(t *testing.T) {
	in := `apiVersion: v1
kind: Namespace
metadata:
  name: cert-manager
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: certificates.cert-manager.io
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cert-manager
  namespace: cert-manager`

	crds, namespaces, rest := partitionEarlyBatch(in)
	if !strings.Contains(namespaces, "kind: Namespace") {
		t.Errorf("Namespace must be in the namespaces partition:\n%s", namespaces)
	}
	if !strings.Contains(crds, "kind: CustomResourceDefinition") {
		t.Errorf("CRD must be in the crds partition:\n%s", crds)
	}
	if strings.Contains(rest, "kind: Namespace") || strings.Contains(rest, "kind: CustomResourceDefinition") {
		t.Errorf("Namespace/CRD must NOT leak into rest:\n%s", rest)
	}
	if !strings.Contains(rest, "kind: Deployment") {
		t.Errorf("Deployment must be in rest:\n%s", rest)
	}
}

// TestApplyCRDsThenRest_NamespaceBeforeRest proves the cold-cluster ordering
// K1 needs: a stream whose only early-batch doc is a Namespace applies the
// Namespace BEFORE the namespaced resources (no `namespaces not found`).
func TestApplyCRDsThenRest_NamespaceBeforeRest(t *testing.T) {
	logPath := fakeKubectlLog(t)

	manifests := `apiVersion: v1
kind: Namespace
metadata:
  name: cert-manager
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cert-manager
  namespace: cert-manager
spec: {}`

	if err := applyCRDsThenRest(context.Background(), "k3d-test", "", "", manifests); err != nil {
		t.Fatalf("applyCRDsThenRest: %v", err)
	}
	data, _ := os.ReadFile(logPath)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	nsApplyIdx, deployApplyIdx := -1, -1
	for i, l := range lines {
		if !strings.Contains(l, "apply") {
			continue
		}
		// The fake records argv only, not stdin, so distinguish passes by
		// order: the FIRST apply is the early batch (Namespace), the next is
		// the rest. With no CRDs there is no Established wait between them.
		if nsApplyIdx == -1 {
			nsApplyIdx = i
		} else if deployApplyIdx == -1 {
			deployApplyIdx = i
		}
	}
	if nsApplyIdx == -1 || deployApplyIdx == -1 {
		t.Fatalf("expected two applies (early-batch Namespace then rest); log:\n%s", string(data))
	}
	if nsApplyIdx >= deployApplyIdx {
		t.Errorf("Namespace early-batch apply must precede the rest apply; got ns=%d rest=%d", nsApplyIdx, deployApplyIdx)
	}
	if strings.Contains(string(data), "condition=Established") {
		t.Errorf("a Namespace-only early batch must NOT wait on Established:\n%s", string(data))
	}
}

// TestDropPostHelmHooks proves m5 AND its correctness boundary: a POST-phase
// hook (cert-manager's startupapicheck Job) is dropped, but a PRE-phase hook
// (envoy-gateway's certgen Job, which creates a prerequisite Secret) is KEPT,
// and a plain doc survives.
func TestDropPostHelmHooks(t *testing.T) {
	in := `apiVersion: batch/v1
kind: Job
metadata:
  name: cert-manager-startupapicheck
  annotations:
    helm.sh/hook: post-install,post-upgrade
spec: {}
---
apiVersion: batch/v1
kind: Job
metadata:
  name: envoy-gateway-certgen
  annotations:
    helm.sh/hook: pre-install,pre-upgrade
spec: {}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cert-manager
spec: {}`

	out := dropPostHelmHooks(in)
	if strings.Contains(out, "cert-manager-startupapicheck") {
		t.Errorf("post-only hook doc must be dropped:\n%s", out)
	}
	if !strings.Contains(out, "envoy-gateway-certgen") {
		t.Errorf("pre-phase hook doc (prerequisite state) must be KEPT:\n%s", out)
	}
	if !strings.Contains(out, "kind: Deployment") {
		t.Errorf("non-hook doc must survive:\n%s", out)
	}
}

// TestChartOwnCRDsFiltering proves K3's filter: the standard Gateway API
// group is dropped (forge owns it at its pinned version) while the chart's
// own `gateway.envoyproxy.io` CRDs are kept. Exercises the pure filter over a
// canned --include-crds render so it needs no network/helm.
func TestChartOwnCRDsFiltering(t *testing.T) {
	// Mimic the doc shapes chartOwnCRDs scans: two CRD groups + a non-CRD.
	included := `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: gateways.gateway.networking.k8s.io
spec:
  group: gateway.networking.k8s.io
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: envoyproxies.gateway.envoyproxy.io
spec:
  group: gateway.envoyproxy.io
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: envoy-gateway`

	var kept []string
	for _, doc := range splitDocs(included) {
		m, ok := parseDoc(doc)
		if !ok || m.Kind != "CustomResourceDefinition" {
			continue
		}
		if crdGroup(doc) == standardGatewayAPIGroup {
			continue
		}
		kept = append(kept, doc)
	}
	out := strings.Join(kept, docDelimiter)
	if strings.Contains(out, "gateway.networking.k8s.io") {
		t.Errorf("standard Gateway API group must be filtered out (forge owns it):\n%s", out)
	}
	if !strings.Contains(out, "envoyproxies.gateway.envoyproxy.io") {
		t.Errorf("chart's own gateway.envoyproxy.io CRD must be kept:\n%s", out)
	}
	if strings.Contains(out, "kind: Deployment") {
		t.Errorf("non-CRD docs must not appear in the chart-own CRD set:\n%s", out)
	}
}

// TestIsWebhookNotReadyError pins K2's retry predicate: the transient
// "webhook not serving" errors retry; a genuine manifest error does not.
func TestIsWebhookNotReadyError(t *testing.T) {
	retryable := []string{
		`failed calling webhook "webhook.cert-manager.io": ... no endpoints available for service "cert-manager-webhook"`,
		`failed calling webhook "x": Post "https://...": connection refused`,
		`failed calling webhook "x": context deadline exceeded`,
	}
	for _, m := range retryable {
		if !isWebhookNotReadyError(fmtErr(m)) {
			t.Errorf("expected retryable: %q", m)
		}
	}
	notRetryable := []string{
		`ClusterIssuer.cert-manager.io "letsencrypt" is invalid: spec.acme.email: Required value`,
		`error validating data: unknown field "spce"`,
		``,
	}
	for _, m := range notRetryable {
		if isWebhookNotReadyError(fmtErr(m)) {
			t.Errorf("expected NOT retryable: %q", m)
		}
	}
}

func fmtErr(s string) error {
	if s == "" {
		return nil
	}
	return &stringErr{s}
}

type stringErr struct{ s string }

func (e *stringErr) Error() string { return e.s }

// --- apply ordering (fake kubectl) ------------------------------------------

// fakeKubectlLog installs a fake `kubectl` on PATH that appends its argv
// (one space-joined line per invocation) to a log file, and returns the
// log path. It is the seam for asserting the ORDER of kubectl calls
// (apply CRDs, wait Established, apply rest) without a live cluster.
//
// An `apply` invocation echoes a `<kind>/<name> serverside-applied` line
// for every document it reads on stdin, because that is what real kubectl
// does and forge now READS that output to verify the apply was complete
// (see apply_completeness.go). A fake that exits 0 while printing nothing
// is indistinguishable from the silent-partial-apply incident, and would
// fail every test that routes through applyCRDsThenRest.
func fakeKubectlLog(t *testing.T) string {
	t.Helper()
	requirePOSIXFake(t, "kubectl")
	dir := t.TempDir()
	logPath := filepath.Join(dir, "kubectl.log")
	// The fake records argv then exits 0. `kubectl wait` succeeds silently;
	// `apply -f -` drains stdin and confirms each document it saw.
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + logPath + "\n" +
		"case \" $* \" in *' apply '*) " +
		"awk '/^kind:/{k=tolower($2)} /^  name:/{if(k!=\"\"&&n==\"\"){n=$2}} " +
		"/^---$/{if(k!=\"\"&&n!=\"\")print k\"/\"n\" serverside-applied\"; k=\"\"; n=\"\"} " +
		"END{if(k!=\"\"&&n!=\"\")print k\"/\"n\" serverside-applied\"}' ;; " +
		"*) cat > /dev/null 2>/dev/null || true ;; esac\n" +
		"exit 0\n"
	bin := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake kubectl: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// TestApplyCRDsThenRest_OrdersCRDsBeforeRest is the apply-ordering proof:
// a manifest set whose chart resources reference a CRD must apply the CRDs
// FIRST, WAIT until Established, and only THEN apply the rest — so a
// subsequent `forge env deploy` (the app) finds the CRDs present. Asserts the
// kubectl invocation order: apply (CRD) → wait Established → apply (rest).
func TestApplyCRDsThenRest_OrdersCRDsBeforeRest(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	logPath := fakeKubectlLog(t)

	extraCRDs := `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: gateways.gateway.networking.k8s.io`

	// The chart's --skip-crds controllers (a Deployment that needs the CRD).
	rest := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: envoy-gateway
  namespace: envoy-gateway-system
spec: {}`

	if err := applyCRDsThenRest(context.Background(), "k3d-test", "", extraCRDs, rest); err != nil {
		t.Fatalf("applyCRDsThenRest: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read kubectl log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")

	// Find the index of: the wait-Established call, and the apply that
	// carries the Deployment (rest). The CRD apply must precede the wait,
	// and the wait must precede the rest apply.
	waitIdx, restApplyIdx, crdApplyIdx := -1, -1, -1
	for i, l := range lines {
		switch {
		case strings.Contains(l, "wait") && strings.Contains(l, "condition=Established"):
			waitIdx = i
		case strings.Contains(l, "apply"):
			// Distinguish CRD apply from rest apply by which came before/after
			// the wait. The first apply is CRDs; a later apply is rest.
			if crdApplyIdx == -1 {
				crdApplyIdx = i
			} else {
				restApplyIdx = i
			}
		}
	}

	if crdApplyIdx == -1 {
		t.Fatalf("expected a CRD apply; kubectl log:\n%s", string(data))
	}
	if waitIdx == -1 {
		t.Fatalf("expected a `kubectl wait --for=condition=Established`; kubectl log:\n%s", string(data))
	}
	if restApplyIdx == -1 {
		t.Fatalf("expected a second apply for the rest; kubectl log:\n%s", string(data))
	}
	if crdApplyIdx >= waitIdx || waitIdx >= restApplyIdx {
		t.Errorf("apply order must be CRDs → wait Established → rest; got crdApply=%d wait=%d restApply=%d\nlog:\n%s",
			crdApplyIdx, waitIdx, restApplyIdx, string(data))
	}

	// The wait must name the forge-supplied CRD.
	if !strings.Contains(lines[waitIdx], "gateways.gateway.networking.k8s.io") {
		t.Errorf("wait should name the CRD; got %q", lines[waitIdx])
	}
}

// TestWaitChartDeploymentsAvailable_Command pins K2's wait: forge waits for
// the chart's Deployments to be Available before applying the riding
// manifests (the cert-manager webhook gate). Asserts the kubectl invocation.
func TestWaitChartDeploymentsAvailable_Command(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	logPath := fakeKubectlLog(t)
	if err := waitChartDeploymentsAvailable(context.Background(), "k3d-test", "cert-manager", 30*time.Second); err != nil {
		t.Fatalf("waitChartDeploymentsAvailable: %v", err)
	}
	data, _ := os.ReadFile(logPath)
	got := strings.TrimSpace(string(data))
	for _, want := range []string{"wait", "condition=Available", "deploy", "--all", "-n cert-manager"} {
		if !strings.Contains(got, want) {
			t.Errorf("wait command missing %q; got %q", want, got)
		}
	}
}

// TestApplyRidingManifestsWithRetry_SucceedsFirstTry confirms the retry
// wrapper passes through on a clean apply (fake kubectl exits 0).
func TestApplyRidingManifestsWithRetry_SucceedsFirstTry(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	logPath := fakeKubectlLog(t)
	manifests := `apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-prod
spec: {}`
	if err := applyRidingManifestsWithRetry(context.Background(), "k3d-test", "", manifests); err != nil {
		t.Fatalf("applyRidingManifestsWithRetry: %v", err)
	}
	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), "apply") {
		t.Errorf("expected an apply; log:\n%s", string(data))
	}
}

// TestApplyCRDsThenRest_NoCRDsSingleApply confirms the degenerate case: a
// stream with no CRDs (and no forge-supplied CRDs) does a plain apply with
// no Established wait — byte-identical to a normal apply.
func TestApplyCRDsThenRest_NoCRDsSingleApply(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	logPath := fakeKubectlLog(t)

	rest := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: app
spec: {}`

	if err := applyCRDsThenRest(context.Background(), "k3d-test", "", "", rest); err != nil {
		t.Fatalf("applyCRDsThenRest: %v", err)
	}
	data, _ := os.ReadFile(logPath)
	if strings.Contains(string(data), "condition=Established") {
		t.Errorf("no-CRD apply must not wait on Established:\n%s", string(data))
	}
}
