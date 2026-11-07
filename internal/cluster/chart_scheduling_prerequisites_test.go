package cluster

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

func TestChartSchedulingPrerequisitesScope(t *testing.T) {
	stream := `apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: shared
---
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
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
		got := ChartSchedulingPrerequisites(ScopeManifestsToGroup(stream, GroupScope{Cluster: kctx}))
		if !strings.Contains(got, "name: shared") || strings.Contains(got, "name: custom") || strings.Contains(got, "kind: Deployment") || (strings.Contains(got, "name: tenant") != (kctx == "tenant")) {
			t.Fatalf("%s: %s", kctx, got)
		}
	}
}

func TestApplyRenderedSchedulingPrerequisitesBeforeChartWait(t *testing.T) {
	testkit.Slow(t, "records fake helm/kubectl subprocesses")
	for _, kctx := range []string{"hub", "tenant"} {
		t.Run(kctx, func(t *testing.T) {
			readCalls := fakeKubectlRecorder(t)
			fakeHelm(t, "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: controller\nspec:\n  template:\n    spec:\n      priorityClassName: platform")
			stream := "apiVersion: scheduling.k8s.io/v1\nkind: PriorityClass\nmetadata:\n  name: platform\n  labels:\n    forge.dev/cluster: " + kctx + "\nvalue: 1000"
			opts := ApplyOpts{Context: "hub", ClusterScope: &GroupScope{Cluster: "hub"}, Namespace: "platform", Rollout: RolloutPolicy{Mode: RolloutSkip}, HelmCharts: []HelmChartSpec{{Name: "controller", Cluster: kctx, Manifests: "apiVersion: example.test/v1\nkind: Widget\nmetadata:\n  name: riding", Chart: "controller", Repo: "https://example.test/charts", Version: "1", Namespace: "platform"}}}
			if err := applyRendered(context.Background(), opts, stream); err != nil {
				t.Fatal(err)
			}
			calls := readCalls()
			pre := firstIndex(calls, appliesKind("PriorityClass"))
			wait := firstIndex(calls, func(c kubectlCall) bool { return c.IsWaitFor("Available") })
			if pre < 0 || wait < 0 || pre >= wait {
				t.Fatalf("prerequisite %d must precede wait %d:\n%s", pre, wait, renderCalls(calls))
			}
			if !strings.Contains(calls[pre].Args, "--context "+kctx) {
				t.Fatalf("wrong context: %s", calls[pre].Args)
			}
		})
	}
}
