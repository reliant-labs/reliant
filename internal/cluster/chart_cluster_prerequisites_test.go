package cluster

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// Every cluster-scoped definition a chart can name is promoted ahead of the
// charts, by API group as well as kind; anything that needs a running backend
// or controller (webhooks, APIServices, custom resources) and anything nothing
// is admitted against (RBAC) stays in the normal apply.
func TestChartClusterPrerequisitesSelectsClusterScopedDefinitions(t *testing.T) {
	doc := func(apiVersion, kind, name string) string {
		return "apiVersion: " + apiVersion + "\nkind: " + kind + "\nmetadata:\n  name: " + name
	}
	stream := strings.Join([]string{
		doc("scheduling.k8s.io/v1", "PriorityClass", "priority"),
		doc("node.k8s.io/v1", "RuntimeClass", "runtime"),
		doc("storage.k8s.io/v1", "StorageClass", "storage"),
		doc("networking.k8s.io/v1", "IngressClass", "ingress"),
		doc("v1", "Namespace", "namespace"),
		doc("apiextensions.k8s.io/v1", "CustomResourceDefinition", "widgets.example.test"),
		doc("example.test/v1", "PriorityClass", "custom-priority"),
		doc("example.test/v1", "Namespace", "custom-namespace"),
		doc("admissionregistration.k8s.io/v1", "ValidatingWebhookConfiguration", "webhook"),
		doc("apiregistration.k8s.io/v1", "APIService", "apiservice"),
		doc("rbac.authorization.k8s.io/v1", "ClusterRole", "role"),
		doc("gateway.networking.k8s.io/v1", "GatewayClass", "gateway"),
		doc("cert-manager.io/v1", "ClusterIssuer", "issuer"),
		doc("apps/v1", "Deployment", "app"),
		"not: [yaml",
	}, "\n---\n")
	var got []string
	for _, d := range splitDocs(ChartClusterPrerequisites(stream)) {
		m, ok := parseDoc(d)
		if !ok {
			t.Fatalf("unparseable prerequisite: %q", d)
		}
		got = append(got, m.Metadata.Name)
	}
	want := []string{"priority", "runtime", "storage", "ingress", "namespace", "widgets.example.test"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prerequisites = %v, want %v", got, want)
	}
}

func TestChartClusterPrerequisitesScope(t *testing.T) {
	stream := `apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: shared
---
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: tenant
  labels:
    forge.dev/cluster: tenant
---
apiVersion: example.test/v1
kind: PriorityClass
metadata:
  name: custom
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: app`
	for _, kctx := range []string{"hub", "tenant"} {
		got := ChartClusterPrerequisites(ScopeManifestsToGroup(stream, GroupScope{Cluster: kctx}))
		if !strings.Contains(got, "name: shared") || strings.Contains(got, "name: custom") || strings.Contains(got, "kind: Deployment") || (strings.Contains(got, "name: tenant") != (kctx == "tenant")) {
			t.Fatalf("%s: %s", kctx, got)
		}
	}
}

// The deploy path: the env's cluster-scoped objects reach each chart's context
// before that chart's controller wait, and a CRD the chart's own Manifests
// instantiate is Established before the chart is applied.
func TestApplyRenderedClusterPrerequisitesBeforeChartWait(t *testing.T) {
	testkit.Slow(t, "records fake helm/kubectl subprocesses")
	for _, kctx := range []string{"hub", "tenant"} {
		t.Run(kctx, func(t *testing.T) {
			readCalls := fakeKubectlRecorder(t)
			fakeHelm(t, "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: controller\nspec:\n  template:\n    spec:\n      priorityClassName: platform")
			route := "\n  labels:\n    forge.dev/cluster: " + kctx
			stream := strings.Join([]string{
				"apiVersion: scheduling.k8s.io/v1\nkind: PriorityClass\nmetadata:\n  name: platform" + route + "\nvalue: 1000",
				"apiVersion: storage.k8s.io/v1\nkind: StorageClass\nmetadata:\n  name: fast" + route + "\nprovisioner: rancher.io/local-path",
				"apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: widgets.example.test" + route,
			}, "\n---\n")
			opts := ApplyOpts{Context: "hub", ClusterScope: &GroupScope{Cluster: "hub"}, Namespace: "platform", Rollout: RolloutPolicy{Mode: RolloutSkip}, HelmCharts: []HelmChartSpec{{Name: "controller", Cluster: kctx, Manifests: "apiVersion: example.test/v1\nkind: Widget\nmetadata:\n  name: riding", Chart: "controller", Repo: "https://example.test/charts", Version: "1", Namespace: "platform"}}}
			if err := applyRendered(context.Background(), opts, stream); err != nil {
				t.Fatal(err)
			}
			calls := readCalls()
			wait := firstIndex(calls, func(c kubectlCall) bool { return c.IsWaitFor("Available") })
			if wait < 0 {
				t.Fatalf("no controller wait:\n%s", renderCalls(calls))
			}
			for _, kind := range []string{"PriorityClass", "StorageClass", "CustomResourceDefinition"} {
				pre := firstIndex(calls, appliesKind(kind))
				if pre < 0 || pre >= wait {
					t.Fatalf("%s prerequisite %d must precede wait %d:\n%s", kind, pre, wait, renderCalls(calls))
				}
				if calls[pre].Context() != kctx {
					t.Fatalf("%s applied to %q, want %q", kind, calls[pre].Context(), kctx)
				}
			}
			established := firstIndex(calls, func(c kubectlCall) bool {
				return c.IsWaitFor("Established") && strings.Contains(c.Args, "widgets.example.test") && c.Context() == kctx
			})
			widget := firstIndex(calls, appliesKind("Widget"))
			if established < 0 || widget < 0 || established >= widget {
				t.Fatalf("CRD Established wait %d must precede the chart's Widget apply %d:\n%s", established, widget, renderCalls(calls))
			}
		})
	}
}
