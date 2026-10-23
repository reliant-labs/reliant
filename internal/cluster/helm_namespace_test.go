package cluster

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Chart-namespace threading: the `default`-namespace bug.
//
// THE BUG. `helm template -n <ns>` renders a chart FOR a namespace, but a
// chart is not obliged to stamp `metadata.namespace` onto its output, and
// many do not — Helm's own install path supplies the namespace from the
// client at apply time. forge rendered such a chart correctly and then
// applied it with `kubectl apply` carrying NO namespace flag, so every
// namespace-less object went to kubectl's default namespace, `default`,
// whatever the chart declared.
//
// Measured against the two charts control-plane declares:
//
//	fluxcd-community/flux2 2.19.1   40 objects, 0 carry metadata.namespace
//	envoyproxy/gateway-helm v1.7.2  18 objects, 12 carry it; the other 6 are
//	                                all cluster-scoped and need none
//
// So Flux's six controllers landed in `default` while `flux-system` sat
// empty, and Envoy — declared identically, in the same bundle, for months —
// was never affected, because its templates stamp the field. Any test that
// only exercised Envoy would have passed throughout. These tests therefore
// use a FLUX-SHAPED render (namespace-less docs) as the primary fixture.
//
// WHAT THESE TESTS ASSERT. The real `kubectl` argv, and which manifests rode
// each invocation — not a helper in isolation. The fake kubectl below records
// argv AND stdin per call, so "the namespace reached the apply that carried
// the Deployment" is checked directly. A helper proven correct while nothing
// proves it is called is the gap this file exists to close.

// kubectlCall is one recorded invocation of the fake kubectl: its argv and
// the manifest stream it received on stdin.
type kubectlCall struct {
	Args  string
	Stdin string
}

// Namespace returns the value of the `-n` flag in this call's argv, or ""
// when the call passed none.
func (c kubectlCall) Namespace() string {
	fields := strings.Fields(c.Args)
	for i, f := range fields {
		if (f == "-n" || f == "--namespace") && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

func (c kubectlCall) IsApply() bool {
	for _, f := range strings.Fields(c.Args) {
		if f == "apply" {
			return true
		}
	}
	return false
}

// fakeKubectlRecorder installs a fake `kubectl` on PATH that records both
// argv and stdin for every invocation, and returns a reader for the calls.
//
// Recording STDIN is the point: asserting that some kubectl call carried
// `-n flux-system` proves nothing on its own, because the objects are split
// across several apply passes by kind. Pairing argv with the documents that
// rode it is what proves the Deployment specifically was applied into the
// chart's namespace.
//
// Like fakeKubectlLog, an `apply` echoes `<kind>/<name> serverside-applied`
// per document so the apply-completeness check (apply_completeness.go) is
// satisfied — a fake that exits 0 printing nothing is indistinguishable from
// the silent-partial-apply incident and fails every apply path.
func fakeKubectlRecorder(t *testing.T) func() []kubectlCall {
	t.Helper()
	requirePOSIXFake(t, "kubectl")
	dir := t.TempDir()
	logPath := filepath.Join(dir, "kubectl-calls.log")

	// Each invocation appends a record delimited by sentinel lines:
	//   <<<ARGS>>> <argv>
	//   <<<STDIN>>>
	//   <the stream>
	//   <<<END>>>
	// stdin is captured to a temp file, replayed to the confirming awk, then
	// appended to the log, so the fake both records and behaves.
	script := "#!/bin/sh\n" +
		"tmp=$(mktemp)\n" +
		"cat > \"$tmp\"\n" +
		"{ printf '<<<ARGS>>> %s\\n' \"$*\"; printf '<<<STDIN>>>\\n'; cat \"$tmp\"; printf '\\n<<<END>>>\\n'; } >> " + logPath + "\n" +
		"case \" $* \" in *' apply '*) " +
		"awk '/^kind:/{k=tolower($2)} /^  name:/{if(k!=\"\"&&n==\"\"){n=$2}} " +
		"/^---$/{if(k!=\"\"&&n!=\"\")print k\"/\"n\" serverside-applied\"; k=\"\"; n=\"\"} " +
		"END{if(k!=\"\"&&n!=\"\")print k\"/\"n\" serverside-applied\"}' \"$tmp\" ;; \n" +
		"esac\n" +
		"rm -f \"$tmp\"\n" +
		"exit 0\n"

	bin := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake kubectl: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return func() []kubectlCall {
		data, err := os.ReadFile(logPath)
		if err != nil {
			return nil
		}
		var calls []kubectlCall
		for _, rec := range strings.Split(string(data), "<<<ARGS>>> ") {
			if strings.TrimSpace(rec) == "" {
				continue
			}
			argsPart, rest, found := strings.Cut(rec, "\n<<<STDIN>>>\n")
			if !found {
				continue
			}
			stdin, _, _ := strings.Cut(rest, "\n<<<END>>>")
			calls = append(calls, kubectlCall{
				Args:  strings.TrimSpace(argsPart),
				Stdin: stdin,
			})
		}
		return calls
	}
}

// fluxShapedRender is a flux2-shaped chart render: NOT ONE document carries
// `metadata.namespace`. It spans every pass of the apply pipeline — a CRD and
// the synthesized Namespace (early batch), a ServiceAccount and Service
// (rest/workloads), a ConfigMap (config pass), a Deployment (the object that
// actually landed in `default`), and cluster-scoped RBAC that must NOT
// acquire a namespace.
const fluxShapedRender = `apiVersion: v1
kind: Namespace
metadata:
  name: flux-system
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: kustomizations.kustomize.toolkit.fluxcd.io
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: source-controller
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: flux-config
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: crd-controller
rules: []
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: cluster-reconciler
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: crd-controller}
---
apiVersion: v1
kind: Service
metadata:
  name: source-controller
spec: {}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: source-controller
spec: {}`

// findApplyCarrying returns the apply call whose stdin contains marker.
func findApplyCarrying(t *testing.T, calls []kubectlCall, marker string) kubectlCall {
	t.Helper()
	for _, c := range calls {
		if c.IsApply() && strings.Contains(c.Stdin, marker) {
			return c
		}
	}
	t.Fatalf("no apply invocation carried %q; calls:\n%s", marker, renderCalls(calls))
	return kubectlCall{}
}

func renderCalls(calls []kubectlCall) string {
	var b strings.Builder
	for i, c := range calls {
		b.WriteString("  [")
		b.WriteString(strings.TrimSpace(c.Args))
		b.WriteString("] stdin-kinds=")
		for _, line := range strings.Split(c.Stdin, "\n") {
			if strings.HasPrefix(line, "kind:") {
				b.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "kind:")))
				b.WriteString(",")
			}
		}
		if i < len(calls)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// TestApplyCRDsThenRest_ThreadsChartNamespaceToEveryApplyPass is THE
// regression test for the bug: every apply pass of a flux-shaped render must
// carry `-n flux-system`, so its namespace-less objects land in the chart's
// namespace rather than in `default`.
//
// It asserts on the ACTUAL kubectl argv, correlated with the documents that
// rode each call — so removing the namespace threading from ANY pass fails
// it. The Deployment assertion is the one that maps directly to the observed
// symptom (`kubectl get deploy -A` showed source-controller in `default`).
func TestApplyCRDsThenRest_ThreadsChartNamespaceToEveryApplyPass(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	readCalls := fakeKubectlRecorder(t)

	if err := applyCRDsThenRest(context.Background(), "k3d-test", "flux-system", "", fluxShapedRender); err != nil {
		t.Fatalf("applyCRDsThenRest: %v", err)
	}
	calls := readCalls()

	applies := 0
	for _, c := range calls {
		if c.IsApply() {
			applies++
		}
	}
	if applies == 0 {
		t.Fatalf("expected at least one apply; calls:\n%s", renderCalls(calls))
	}

	// EVERY apply pass must carry the chart's namespace. The objects are
	// split across passes by kind (early CRD/Namespace batch, then
	// config-then-rest), so a fix applied to one pass and not another
	// reproduces the bug for whichever half was missed.
	for _, c := range calls {
		if !c.IsApply() {
			continue
		}
		if got := c.Namespace(); got != "flux-system" {
			t.Errorf("apply pass ran with namespace %q, want \"flux-system\"; argv: %s\nstdin:\n%s",
				got, c.Args, c.Stdin)
		}
	}

	// The specific objects that were observed in `default`, each proven to
	// have ridden an apply scoped to flux-system.
	for _, marker := range []string{
		"kind: Deployment",     // source-controller — the observed symptom
		"kind: ServiceAccount", // rest pass
		"kind: ConfigMap",      // config pass
		"kind: Service",        // rest pass
	} {
		c := findApplyCarrying(t, calls, marker)
		if c.Namespace() != "flux-system" {
			t.Errorf("%s was applied with namespace %q, want \"flux-system\"; argv: %s",
				marker, c.Namespace(), c.Args)
		}
	}

	// The early batch (CRD + Namespace) is a separate apply and must be
	// scoped too — a Namespace object is cluster-scoped so the flag is inert
	// for it, but the CRD pass and the Namespace pass are the same
	// invocation, and leaving it unscoped is the half-fix this pins against.
	crdCall := findApplyCarrying(t, calls, "kind: CustomResourceDefinition")
	if crdCall.Namespace() != "flux-system" {
		t.Errorf("early CRD/Namespace batch applied with namespace %q, want \"flux-system\"; argv: %s",
			crdCall.Namespace(), crdCall.Args)
	}
}

// TestKubectlApplyArgs_NamespaceFlag pins the argv construction itself: the
// `-n` flag appears exactly when a namespace is supplied, and the rest of the
// server-side apply invocation is unchanged.
//
// The empty case is the compatibility guarantee for every non-chart caller:
// forge's own KCL render stamps metadata.namespace on everything it emits, so
// those applies must stay byte-identical to before this fix.
func TestKubectlApplyArgs_NamespaceFlag(t *testing.T) {
	withNS := strings.Join(kubectlApplyArgs("flux-system"), " ")
	if want := "apply --server-side --force-conflicts -n flux-system -f -"; withNS != want {
		t.Errorf("kubectlApplyArgs(%q) = %q, want %q", "flux-system", withNS, want)
	}

	for _, empty := range []string{"", "   ", "\t"} {
		got := strings.Join(kubectlApplyArgs(empty), " ")
		if want := "apply --server-side --force-conflicts -f -"; got != want {
			t.Errorf("kubectlApplyArgs(%q) = %q, want %q (no flag)", empty, got, want)
		}
	}
}

// TestKubectlApplyNamespaced_ClusterScopedObjectsKeepNoNamespace is the
// cluster-scoped guarantee. The chart emits ClusterRoles,
// ClusterRoleBindings and CRDs; none may acquire a namespace.
//
// `-n` is the mechanism precisely BECAUSE it is safe here: measured against a
// live apiserver, a namespace-less ClusterRole applied with `-n <ns>` comes
// back with `metadata.namespace` empty — kubectl asks the apiserver for the
// resource's scope and ignores the flag for cluster-scoped kinds. The test
// therefore asserts the manifests are passed through UNMODIFIED: forge must
// not stamp `metadata.namespace` into them, which is the failure mode the
// alternative (render-time stamping) would have introduced. Scope cannot be
// derived from a static kind list anyway — a chart's own CRDs define new
// cluster-scoped kinds (GatewayClass, XMesh), which forge cannot know.
func TestKubectlApplyNamespaced_ClusterScopedObjectsKeepNoNamespace(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	readCalls := fakeKubectlRecorder(t)

	if err := applyCRDsThenRest(context.Background(), "k3d-test", "flux-system", "", fluxShapedRender); err != nil {
		t.Fatalf("applyCRDsThenRest: %v", err)
	}

	for _, c := range readCalls() {
		if !c.IsApply() {
			continue
		}
		// No document in any pass may have gained a metadata.namespace:
		// forge threads the namespace through the FLAG, never by rewriting
		// the manifest, so a cluster-scoped object cannot be corrupted.
		for _, line := range strings.Split(c.Stdin, "\n") {
			if strings.HasPrefix(line, "  namespace:") {
				t.Errorf("forge must not stamp metadata.namespace into chart manifests "+
					"(cluster-scoped kinds would become invalid); found %q in:\n%s",
					strings.TrimSpace(line), c.Stdin)
			}
		}
	}
}

// TestKubectlApplyNamespaced_ObjectDeclaringOwnNamespaceKeepsIt is the
// "do not silently relocate" guarantee: a chart that deliberately places an
// object in another namespace must keep it.
//
// This is not merely a preference — it is a correctness requirement, because
// kubectl REFUSES the mismatch and fails the WHOLE stream:
//
//	error: the namespace from the provided object "kube-system" does not
//	match the namespace "flux-system". You must pass '--namespace=kube-system'
//
// Measured: in a two-document stream, the first document applied and the
// second produced that error, so a naive `-n` on everything would break the
// deploy for the objects applied alongside it. Such docs are applied in their
// own pass with no flag.
func TestKubectlApplyNamespaced_ObjectDeclaringOwnNamespaceKeepsIt(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	readCalls := fakeKubectlRecorder(t)

	const manifests = `apiVersion: v1
kind: ConfigMap
metadata:
  name: in-chart-namespace
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: deliberately-elsewhere
  namespace: kube-system
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: explicitly-in-target
  namespace: flux-system`

	if err := KubectlApplyNamespaced(context.Background(), "k3d-test", "flux-system", manifests); err != nil {
		t.Fatalf("KubectlApplyNamespaced: %v", err)
	}
	calls := readCalls()

	// The foreign-namespace doc must NOT ride the -n flag, or kubectl
	// rejects the entire stream.
	elsewhere := findApplyCarrying(t, calls, "deliberately-elsewhere")
	if ns := elsewhere.Namespace(); ns != "" {
		t.Errorf("a doc declaring namespace kube-system must not be applied with -n %q "+
			"(kubectl rejects the mismatch and fails the whole stream); argv: %s", ns, elsewhere.Args)
	}
	if !strings.Contains(elsewhere.Stdin, "namespace: kube-system") {
		t.Errorf("the doc must keep its declared namespace; stdin:\n%s", elsewhere.Stdin)
	}
	// It must not be dragged along with the flagged docs.
	if strings.Contains(elsewhere.Stdin, "in-chart-namespace") {
		t.Errorf("foreign-namespace doc must apply in its OWN pass, separate from the "+
			"flagged docs; stdin:\n%s", elsewhere.Stdin)
	}

	// The namespace-less doc still gets the flag — that is the actual fix.
	inChart := findApplyCarrying(t, calls, "in-chart-namespace")
	if inChart.Namespace() != "flux-system" {
		t.Errorf("namespace-less doc applied with namespace %q, want \"flux-system\"; argv: %s",
			inChart.Namespace(), inChart.Args)
	}

	// A doc that already declares the TARGET namespace rides the flag
	// harmlessly (kubectl accepts a matching namespace), so it must not be
	// split into a needless extra pass.
	if !strings.Contains(inChart.Stdin, "explicitly-in-target") {
		t.Errorf("a doc already declaring the target namespace should ride the same "+
			"flagged pass; stdin:\n%s", inChart.Stdin)
	}
}

// TestSplitByTargetNamespace pins the split rule directly, including the
// pass-through when no target namespace is supplied (every non-chart caller).
func TestSplitByTargetNamespace(t *testing.T) {
	const in = `apiVersion: v1
kind: ConfigMap
metadata:
  name: none
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: same
  namespace: target
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: other
  namespace: elsewhere`

	onTarget, elsewhere := splitByTargetNamespace(in, "target")
	if !strings.Contains(onTarget, "name: none") || !strings.Contains(onTarget, "name: same") {
		t.Errorf("namespace-less and matching-namespace docs belong onTarget:\n%s", onTarget)
	}
	if strings.Contains(onTarget, "name: other") {
		t.Errorf("foreign-namespace doc leaked into onTarget:\n%s", onTarget)
	}
	if !strings.Contains(elsewhere, "name: other") {
		t.Errorf("foreign-namespace doc missing from elsewhere:\n%s", elsewhere)
	}

	// No target → no flag will be passed → nothing to separate. This keeps
	// every pre-existing caller byte-identical.
	on, other := splitByTargetNamespace(in, "")
	if on != in || other != "" {
		t.Errorf("an empty target must pass the stream through unchanged; got onTarget/elsewhere:\n%s\n---\n%s", on, other)
	}
}

// TestApplyCRDsThenRest_NoNamespacePassesNoFlag is the regression guard for
// the non-chart callers: forge's own KCL-rendered manifests stamp
// metadata.namespace themselves, so their applies must carry no `-n` at all.
func TestApplyCRDsThenRest_NoNamespacePassesNoFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	readCalls := fakeKubectlRecorder(t)

	const rest = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: app
spec: {}`

	if err := applyCRDsThenRest(context.Background(), "k3d-test", "", "", rest); err != nil {
		t.Fatalf("applyCRDsThenRest: %v", err)
	}
	for _, c := range readCalls() {
		if c.IsApply() && c.Namespace() != "" {
			t.Errorf("an apply with no chart namespace must pass no -n flag; argv: %s", c.Args)
		}
	}
}

// TestApplyRenderedCharts_ReadsNamespaceFromTheChartSpec closes the
// "helper proven correct while nothing proves it is CALLED" gap.
//
// Every other test here passes a namespace INTO applyCRDsThenRest, so it
// cannot see the failure mode that actually shipped: a namespace that is
// present in HelmChartSpec and never reaches the apply. Severing exactly that
// wiring — `applyCRDsThenRest(ctx, kctx, "", ...)` instead of
// `rc.spec.Namespace` — leaves kubectlApplyArgs and splitByTargetNamespace
// perfect and every other test in this file green, while reproducing the bug
// in full.
//
// So this test starts where the deploy does, from the declared spec, and
// asserts the namespace arrives at the real kubectl argv. It is the one that
// goes red when the wiring is cut.
func TestApplyRenderedCharts_ReadsNamespaceFromTheChartSpec(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	readCalls := fakeKubectlRecorder(t)

	// Exactly what renderSelectedCharts produces for the flux chart: the
	// spec carries the namespace, the render does not.
	charts := []renderedChart{{
		spec:      HelmChartSpec{Name: "flux", Namespace: "flux-system"},
		manifests: fluxShapedRender,
	}}

	if err := applyRenderedCharts(context.Background(), "k3d-test", charts, true); err != nil {
		t.Fatalf("applyRenderedCharts: %v", err)
	}
	calls := readCalls()

	applies := 0
	for _, c := range calls {
		if !c.IsApply() {
			continue
		}
		applies++
		if got := c.Namespace(); got != "flux-system" {
			t.Errorf("the chart spec's namespace must reach the apply argv; got %q, want "+
				"\"flux-system\"; argv: %s", got, c.Args)
		}
	}
	if applies == 0 {
		t.Fatalf("expected applies; calls:\n%s", renderCalls(calls))
	}

	// The observed symptom, asserted at the argv that carried it: flux's
	// source-controller Deployment went to `default`.
	deploy := findApplyCarrying(t, calls, "kind: Deployment")
	if deploy.Namespace() != "flux-system" {
		t.Errorf("flux's controller Deployment must be applied into flux-system, not the "+
			"kubectl default namespace; argv: %s", deploy.Args)
	}
}

// TestApplyRenderedCharts_RidingManifestsGetTheChartNamespace pins the
// second apply path through the chart loop: the consumer-declared manifests
// riding the chart's --target (GatewayClass / ClusterIssuers) go through
// applyRidingManifestsWithRetry, a DIFFERENT call site, which must be scoped
// the same way.
func TestApplyRenderedCharts_RidingManifestsGetTheChartNamespace(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	readCalls := fakeKubectlRecorder(t)

	charts := []renderedChart{{
		spec: HelmChartSpec{Name: "flux", Namespace: "flux-system"},
		// No Deployments in manifests, so the Available wait returns
		// immediately against the fake.
		manifests: `apiVersion: v1
kind: ServiceAccount
metadata:
  name: source-controller`,
		extra: `apiVersion: v1
kind: ConfigMap
metadata:
  name: riding-config`,
	}}

	if err := applyRenderedCharts(context.Background(), "k3d-test", charts, true); err != nil {
		t.Fatalf("applyRenderedCharts: %v", err)
	}

	riding := findApplyCarrying(t, readCalls(), "riding-config")
	if riding.Namespace() != "flux-system" {
		t.Errorf("a chart's riding manifests must be applied into the chart's namespace; "+
			"got %q, argv: %s", riding.Namespace(), riding.Args)
	}
}

// TestWithDefaultNamespace pins the immutable-recovery scoping: the delete /
// get that heal an immutable-field conflict must be scoped the same way the
// apply was, or they look for the object in `default` and recover nothing.
// flux renders a namespace-less Job (`flux-flux-check`), so this is the same
// class of bug one layer down.
func TestWithDefaultNamespace(t *testing.T) {
	// No namespace in the manifest → inherit the apply's namespace.
	got := withDefaultNamespace(immutableTarget{Kind: "Job", Name: "flux-flux-check"}, "flux-system")
	if got.Namespace != "flux-system" {
		t.Errorf("a namespace-less target must inherit the apply namespace; got %q", got.Namespace)
	}

	// An explicit namespace wins — never relocated.
	got = withDefaultNamespace(immutableTarget{Kind: "Job", Name: "j", Namespace: "kube-system"}, "flux-system")
	if got.Namespace != "kube-system" {
		t.Errorf("an explicit namespace must be preserved; got %q", got.Namespace)
	}

	// No apply namespace → stays empty, so kubectl omits -n (correct for a
	// cluster-scoped object, ignored for a namespaced one).
	got = withDefaultNamespace(immutableTarget{Kind: "ClusterRole", Name: "cr"}, "")
	if got.Namespace != "" {
		t.Errorf("an empty apply namespace must leave the target unscoped; got %q", got.Namespace)
	}
}
