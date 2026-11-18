package cluster

import (
	"context"
	"strings"
	"testing"
)

// A chart's riding manifests wait for its Deployments to be Available. The
// objects those Deployments themselves need — a network allow, an identity, a
// grant, a priority class — must not: in flux-system, whose egress is already
// default-deny on a hub, the Flux Operator cannot reach the API server and so
// never becomes Available until its egress allow exists.
func TestPartitionChartPrerequisites(t *testing.T) {
	stream := strings.Join([]string{
		"apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata:\n  name: operator-egress",
		"apiVersion: fluxcd.controlplane.io/v1\nkind: FluxInstance\nmetadata:\n  name: flux",
		"apiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: shard",
		"apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRoleBinding\nmetadata:\n  name: crd-controller",
		"apiVersion: admissionregistration.k8s.io/v1\nkind: ValidatingAdmissionPolicy\nmetadata:\n  name: guard",
		"apiVersion: scheduling.k8s.io/v1\nkind: PriorityClass\nmetadata:\n  name: forge-delivery",
		"apiVersion: v1\nkind: Namespace\nmetadata:\n  name: forge-delivery-acme-prod",
	}, docDelimiter)
	pre, rest := PartitionChartPrerequisites(stream)
	for _, want := range []string{"operator-egress", "name: shard", "name: crd-controller", "name: forge-delivery\n", "forge-delivery-acme-prod"} {
		if !strings.Contains(pre, want) {
			t.Errorf("prerequisites lack %q:\n%s", want, pre)
		}
	}
	for _, want := range []string{"kind: FluxInstance", "kind: ValidatingAdmissionPolicy"} {
		if !strings.Contains(rest, want) || strings.Contains(pre, want) {
			t.Errorf("%q must ride after the Available wait; prereqs:\n%s\nrest:\n%s", want, pre, rest)
		}
	}
	if strings.Index(pre, "operator-egress") > strings.Index(pre, "name: shard") {
		t.Errorf("order within the prerequisites must be kept:\n%s", pre)
	}
	if p, r := PartitionChartPrerequisites(""); p != "" || r != "" {
		t.Errorf("an empty stream splits into nothing, got %q / %q", p, r)
	}
}

func TestApplyRenderedCharts_PrerequisitesLandBeforeTheAvailableWait(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	readCalls := fakeKubectlRecorder(t)
	charts := []renderedChart{{
		spec: HelmChartSpec{Name: "flux-operator", Namespace: "flux-system"},
		manifests: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: flux-operator`,
		extra: `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: riding-allow
---
apiVersion: fluxcd.controlplane.io/v1
kind: FluxInstance
metadata:
  name: riding-instance`,
	}}
	if err := applyRenderedCharts(context.Background(), "k3d-test", charts, true); err != nil {
		t.Fatalf("applyRenderedCharts: %v", err)
	}
	calls := readCalls()
	index := func(match func(kubectlCall) bool, what string) int {
		for i, c := range calls {
			if match(c) {
				return i
			}
		}
		t.Fatalf("no kubectl call %s; calls:\n%s", what, renderCalls(calls))
		return -1
	}
	allow := index(func(c kubectlCall) bool { return c.IsApply() && strings.Contains(c.Stdin, "riding-allow") }, "carrying the riding NetworkPolicy")
	wait := index(func(c kubectlCall) bool { return c.IsWaitFor("Available") }, "waiting for Available")
	instance := index(func(c kubectlCall) bool { return c.IsApply() && strings.Contains(c.Stdin, "riding-instance") }, "carrying the FluxInstance")
	if allow >= wait || wait >= instance {
		t.Errorf("want NetworkPolicy apply (#%d) < Available wait (#%d) < FluxInstance apply (#%d); calls:\n%s", allow, wait, instance, renderCalls(calls))
	}
	if c := calls[allow]; c.Namespace() != "flux-system" {
		t.Errorf("a riding prerequisite must be applied into the chart's namespace; argv: %s", c.Args)
	}
}
