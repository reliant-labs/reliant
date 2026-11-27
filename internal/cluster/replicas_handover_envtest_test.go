//go:build !windows

// envtest's process package does not compile for windows (see
// eso_vap_envtest_test.go); the same tag applies here.

package cluster

import (
	"context"
	"strings"
	"testing"
)

// The replicas handover and the metrics-API refusal against a REAL
// kube-apiserver, through the real kubectl and the real apply path
// (KubectlApplyNamespaced). envtest runs no controllers: no pods, no HPA
// controller. What it proves is the API server's server-side-apply
// bookkeeping, which is all the handover relies on. Shares
// field_ownership_envtest_test.go's harness, and is skipped without
// KUBEBUILDER_ASSETS and under -short.

// handoverDeploy is a Deployment as forge renders it, with replicas stated
// (replicas != "") or omitted for an autoscaler. kindLabel adds forge.dev/kind,
// which marks a forge workload.
func handoverDeploy(name, replicas string, kindLabel bool) string {
	labels := "app.kubernetes.io/name: " + name
	if kindLabel {
		labels += ", forge.dev/kind: service"
	}
	rep := ""
	if replicas != "" {
		rep = "\n  replicas: " + replicas
	}
	return `apiVersion: apps/v1
kind: Deployment
metadata:
  name: ` + name + `
  namespace: prod
  labels: {` + labels + `}
spec:` + rep + `
  selector: {matchLabels: {app.kubernetes.io/name: ` + name + `}}
  template:
    metadata: {labels: {app.kubernetes.io/name: ` + name + `}}
    spec: {containers: [{name: app, image: registry.example/app:v1}]}
`
}

func (e *ownershipEnv) replicas(name string) any {
	e.t.Helper()
	return e.get("deployment", name)["spec"].(map[string]any)["replicas"]
}

// ownsReplicas reports whether manager's main-resource entry owns spec.replicas.
func (e *ownershipEnv) ownsReplicas(name, manager string) bool {
	e.t.Helper()
	for _, en := range e.get("deployment", name)["metadata"].(map[string]any)["managedFields"].([]any) {
		m := en.(map[string]any)
		if m["manager"] != manager || m["subresource"] != nil {
			continue
		}
		spec, _ := m["fieldsV1"].(map[string]any)["f:spec"].(map[string]any)
		if _, ok := spec["f:replicas"]; ok {
			return true
		}
	}
	return false
}

func TestReplicasHandover_KeepsTheLiveCountWhenAnAutoscalerTakesOver(t *testing.T) {
	e := startOwnershipEnvtest(t)

	// The problem, measured: forge's manager stated 4 and then stops stating
	// it, with nobody else owning it. The server resets it to the default.
	e.kubectl(handoverDeploy("bare", "4", false), "apply", "--server-side", "-f", "-")
	e.kubectl(handoverDeploy("bare", "", false)+"---\n"+hpaDoc("bare", "prod", "bare", 2, 10), "apply", "--server-side", "-f", "-")
	if r := e.replicas("bare"); r != float64(1) {
		t.Fatalf("setup: an SSA apply that drops replicas should reset them to 1, got %v", r)
	}

	// forge's apply path: the same transition keeps the live count.
	e.forgeApply(handoverDeploy("api", "4", false))
	e.forgeApply(handoverDeploy("api", "", false) + "---\n" + hpaDoc("api", "prod", "api", 2, 10))
	if r := e.replicas("api"); r != float64(4) {
		t.Errorf("replicas = %v after the first autoscaled deploy, want the live 4 kept", r)
	}
	if !e.ownsReplicas("api", replicasHandoverManager) {
		t.Errorf("%s should own spec.replicas until the HPA writes it", replicasHandoverManager)
	}
	if e.ownsReplicas("api", forgeFieldManager) {
		t.Errorf("forge's manager still owns spec.replicas; it must have given it up")
	}

	// Idempotent: the next deploy of the same render writes nothing.
	autoscaled := handoverDeploy("api", "", false) + "---\n" + hpaDoc("api", "prod", "api", 2, 10)
	rv := e.get("deployment", "api")["metadata"].(map[string]any)["resourceVersion"]
	e.forgeApply(autoscaled)
	if again := e.get("deployment", "api")["metadata"].(map[string]any)["resourceVersion"]; again != rv {
		t.Errorf("a second autoscaled deploy wrote the Deployment again (resourceVersion %v -> %v)", rv, again)
	}

	// Turning autoscale off takes the field straight back.
	e.forgeApply(handoverDeploy("api", "5", false))
	if r := e.replicas("api"); r != float64(5) || !e.ownsReplicas("api", forgeFieldManager) {
		t.Errorf("replicas = %v (forge owns: %v) after autoscale was removed, want 5 owned by forge", r, e.ownsReplicas("api", forgeFieldManager))
	}

	// A live count above the HPA's maximum is clamped into it.
	e.forgeApply(handoverDeploy("big", "12", false))
	e.forgeApply(handoverDeploy("big", "", false) + "---\n" + hpaDoc("big", "prod", "big", 2, 10))
	if r := e.replicas("big"); r != float64(10) {
		t.Errorf("replicas = %v, want 12 clamped to maxReplicas 10", r)
	}
}

// envtest's API server serves no metrics.k8s.io, so a forge workload declaring
// autoscale is refused before anything is written, in the words the unit test
// pins; a registered but unreachable metrics APIService is not a refusal.
func TestReplicasHandover_RefusesAForgeAutoscaledWorkloadWithoutMetrics(t *testing.T) {
	e := startOwnershipEnvtest(t)
	s := handoverDeploy("api", "", true) + "---\n" + hpaDoc("api", "prod", "api", 2, 10)
	err := KubectlApplyNamespaced(context.Background(), e.kctx, ownershipNS, s)
	if err == nil || err.Error() != metricsMissingMessage([]string{"api"}, e.kctx) {
		t.Fatalf("want the metrics-server refusal, got %v", err)
	}
	if out := e.kubectl("", "get", "deployments", "-n", ownershipNS, "-o", "name"); strings.TrimSpace(out) != "" {
		t.Errorf("a refused stream wrote objects: %s", out)
	}

	// metrics-server registered, its backend absent: the API is there, just
	// not answering yet, as right after a cluster boots.
	e.kubectl(`apiVersion: apiregistration.k8s.io/v1
kind: APIService
metadata: {name: v1beta1.metrics.k8s.io}
spec:
  group: metrics.k8s.io
  version: v1beta1
  service: {name: metrics-server, namespace: kube-system}
  insecureSkipTLSVerify: true
  groupPriorityMinimum: 100
  versionPriority: 100
`, "apply", "-f", "-")
	if err := KubectlApplyNamespaced(context.Background(), e.kctx, ownershipNS, s); err != nil {
		t.Errorf("a registered metrics API must not be refused: %v", err)
	}
}
