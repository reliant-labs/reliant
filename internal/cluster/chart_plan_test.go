package cluster

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

type fakeChartReader struct {
	live   map[string]map[string]any // resource|ns|name
	prune  []liveChartObject
	pruneR bool
}

func (f fakeChartReader) Get(_ context.Context, _, resource, ns string, names []string) ([]map[string]any, error) {
	var out []map[string]any
	for _, n := range names {
		if o, ok := f.live[resource+"|"+ns+"|"+n]; ok {
			out = append(out, o)
		}
	}
	return out, nil
}

func (f fakeChartReader) PruneCandidates(context.Context, string, renderedChart) ([]liveChartObject, bool, error) {
	return f.prune, f.pruneR, nil
}

func certManagerChart(configYAML string) renderedChart {
	return renderedChart{
		spec: HelmChartSpec{Name: "cert-manager", Namespace: "cert-manager", Owner: "cp.prod", Prune: true},
		manifests: `apiVersion: v1
kind: ConfigMap
metadata:
  name: cert-manager
  namespace: cert-manager
  labels: {forge.dev/workload: cert-manager}
data:
  config.yaml: |
` + configYAML + `
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: cert-manager
  namespace: cert-manager
`,
	}
}

func liveCertManager(configYAML string) map[string]map[string]any {
	return map[string]map[string]any{
		"configmap|cert-manager|cert-manager": {
			"kind": "ConfigMap",
			"metadata": map[string]any{"name": "cert-manager", "namespace": "cert-manager", "uid": "x", "resourceVersion": "9",
				"labels": map[string]any{"forge.dev/workload": "cert-manager", "extra": "set-by-someone-else"}},
			"data": map[string]any{"config.yaml": strings.TrimSpace(configYAML)},
		},
		"serviceaccount|cert-manager|cert-manager": {
			"kind": "ServiceAccount", "secrets": []any{map[string]any{"name": "tok"}},
			"metadata": map[string]any{"name": "cert-manager", "namespace": "cert-manager", "uid": "y"},
		},
	}
}

// The 2026-10-16 defect: a chart value changed the controller ConfigMap and the
// plan said nothing.
func TestPlanChart_ValueChangeIsAChangedObject(t *testing.T) {
	rc := certManagerChart("    acmeDNS01Config: {x: 1}")
	changes, err := planChart(context.Background(), "prod", rc, fakeChartReader{live: liveCertManager("    apiVersion: x")})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("want exactly the ConfigMap changed (the ServiceAccount is current, live-only fields are ignored): %+v", changes)
	}
	c := changes[0]
	if c.Op != release.ChartChanged || c.Chart != "cert-manager" || c.Key.Kind != "ConfigMap" ||
		c.Key.Cluster != "prod" || c.Key.Namespace != "cert-manager" || !reflect.DeepEqual(c.Fields, []string{"data.config.yaml"}) || c.Fingerprint == "" {
		t.Errorf("unexpected change %+v", c)
	}
}

func TestPlanChart_CurrentChartIsQuiet(t *testing.T) {
	same := "    acmeDNS01Config: {x: 1}"
	changes, err := planChart(context.Background(), "prod", certManagerChart(same), fakeChartReader{live: liveCertManager(same)})
	if err != nil || len(changes) != 0 {
		t.Fatalf("a chart that matches the cluster must plan nothing: %+v %v", changes, err)
	}
}

func TestPlanChart_MissingObjectIsAdded(t *testing.T) {
	changes, err := planChart(context.Background(), "prod", certManagerChart("    a: 1"), fakeChartReader{})
	if err != nil || len(changes) != 2 {
		t.Fatalf("both objects are new: %+v %v", changes, err)
	}
	for _, c := range changes {
		if c.Op != release.ChartAdded {
			t.Errorf("%+v", c)
		}
	}
}

func TestPlanChart_PruneIsARemoval(t *testing.T) {
	var pc liveChartObject
	pc.Kind = "PriorityClass"
	pc.Metadata.Name = "forge-delivery"
	var lb liveChartObject
	lb.Kind, lb.Metadata.Namespace, lb.Metadata.Name = "Service", "cert-manager", "edge"
	lb.raw = map[string]any{"spec": map[string]any{"type": "LoadBalancer"}}
	same := "    a: 1"
	changes, err := planChart(context.Background(), "prod", certManagerChart(same),
		fakeChartReader{live: liveCertManager(same), prune: []liveChartObject{pc, lb}, pruneR: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("want the two prunes: %+v", changes)
	}
	var gotPC, gotLB release.ChartChange
	for _, c := range changes {
		if c.Key.Kind == "PriorityClass" {
			gotPC = c
		} else {
			gotLB = c
		}
	}
	if gotPC.Op != release.ChartRemoved || gotPC.Key.Name != "forge-delivery" || gotPC.Key.Namespace != "" || gotPC.LoadBalancer {
		t.Errorf("%+v", gotPC)
	}
	if gotLB.Op != release.ChartRemoved || !gotLB.LoadBalancer {
		t.Errorf("a pruned LoadBalancer must be marked: %+v", gotLB)
	}
}

func TestChartChangedFields(t *testing.T) {
	desired := map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "x", "labels": map[string]any{"a": "1"}},
		"spec": map[string]any{
			"replicas": 2,
			"template": map[string]any{"spec": map[string]any{"containers": []any{
				map[string]any{"name": "c", "image": "img:2", "args": []any{"--v=2"}, "env": []any{}},
			}}},
		},
	}
	live := map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment", "status": map[string]any{"ready": 1},
		"metadata": map[string]any{"name": "x", "uid": "u", "labels": map[string]any{"a": "1", "b": "2"}},
		"spec": map[string]any{
			"replicas": float64(2), "revisionHistoryLimit": float64(10),
			"template": map[string]any{"spec": map[string]any{"containers": []any{
				map[string]any{"name": "c", "image": "img:1", "args": []any{"--v=2"}, "terminationMessagePath": "/x"},
			}}},
		},
	}
	got := chartChangedFields(desired, live)
	want := []string{"spec.template.spec.containers[c].image"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fields = %v, want %v", got, want)
	}
}

// The apiserver canonicalises quantities: 1024Mi comes back as 1Gi. That is the
// same amount and must not read as a change; a real difference still must.
func TestChartChangedFields_QuantitiesCompareByAmount(t *testing.T) {
	mk := func(mem string) map[string]any {
		return map[string]any{"spec": map[string]any{"containers": []any{
			map[string]any{"name": "c", "resources": map[string]any{"limits": map[string]any{"memory": mem}}},
		}}}
	}
	if got := chartChangedFields(mk("1024Mi"), mk("1Gi")); len(got) != 0 {
		t.Errorf("1024Mi vs 1Gi reported as a change: %v", got)
	}
	if got := chartChangedFields(mk("2Gi"), mk("1Gi")); len(got) != 1 {
		t.Errorf("2Gi vs 1Gi must be a change: %v", got)
	}
	env := func(v string) map[string]any {
		return map[string]any{"env": []any{map[string]any{"name": "N", "value": v}}}
	}
	if got := chartChangedFields(env("1k"), env("1000")); len(got) != 1 {
		t.Errorf("an env value is text, never a quantity: %v", got)
	}
}
