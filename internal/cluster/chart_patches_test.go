package cluster

import (
	"strings"
	"testing"
)

const patchesStream = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: kustomize-controller
  namespace: flux-system
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: manager
        image: ghcr.io/fluxcd/kustomize-controller:v1.9.5
      - name: sidecar
        image: example.com/sidecar:1
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: source-controller
  namespace: flux-system
spec:
  template:
    spec:
      containers:
      - name: manager
        image: ghcr.io/fluxcd/source-controller:v1.9.5
---
apiVersion: v1
kind: Service
metadata:
  name: source-controller
  namespace: flux-system`

func gvisorPatches() map[string]any {
	rc := map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"runtimeClassName": "gvisor"}}}}
	return map[string]any{
		"Deployment/kustomize-controller": rc,
		"Deployment/source-controller":    rc,
	}
}

func TestApplyChartPatches_NoPatchesIsByteForByte(t *testing.T) {
	got, err := applyChartPatches("flux", patchesStream, nil)
	if err != nil || got != patchesStream {
		t.Fatalf("no patches must return the stream untouched; err=%v changed=%v", err, got != patchesStream)
	}
}

func TestApplyChartPatches_MergesOntoTheNamedObjectsOnly(t *testing.T) {
	got, err := applyChartPatches("flux", patchesStream, gvisorPatches())
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(got, "runtimeClassName: gvisor"); n != 2 {
		t.Fatalf("runtimeClassName must land on exactly the two Deployments, got %d:\n%s", n, got)
	}
	// A strategic merge keeps what the patch did not mention: both containers,
	// the replicas, and the Service (a same-named object of another kind).
	for _, want := range []string{"name: sidecar", "name: manager", "replicas: 1", "kind: Service"} {
		if !strings.Contains(got, want) {
			t.Errorf("patch dropped %q:\n%s", want, got)
		}
	}
	svc := got[strings.Index(got, "kind: Service"):]
	if strings.Contains(svc, "runtimeClassName") {
		t.Errorf("the Service must be untouched:\n%s", svc)
	}
}

func TestApplyChartPatches_Refusals(t *testing.T) {
	rc := map[string]any{"spec": map[string]any{"replicas": 2}}
	cases := []struct {
		name    string
		patches map[string]any
		want    string
	}{
		{"matches nothing", map[string]any{"Deployment/renamed-controller": rc}, "matches no rendered object"},
		{"wrong kind", map[string]any{"StatefulSet/source-controller": rc}, "matches no rendered object"},
		{"not a map", map[string]any{"Deployment/source-controller": "gvisor"}, "must be a map"},
		{"renames the object", map[string]any{"Deployment/source-controller": map[string]any{"metadata": map[string]any{"name": "x"}}}, "never its identity"},
		{"malformed key", map[string]any{"source-controller": rc}, "is not an override key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := applyChartPatches("flux", patchesStream, tc.patches)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error containing %q, got %v", tc.want, err)
			}
			if !strings.Contains(err.Error(), `"flux"`) {
				t.Errorf("the error must name the chart: %v", err)
			}
		})
	}
}

func TestApplyChartPatches_AmbiguousKeyIsRefused(t *testing.T) {
	two := patchesStream + "\n---\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: kustomize-controller\n  namespace: other\n"
	_, err := applyChartPatches("flux", two, map[string]any{"Deployment/kustomize-controller": map[string]any{"spec": map[string]any{"replicas": 2}}})
	if err == nil || !strings.Contains(err.Error(), "matches 2 objects") {
		t.Fatalf("a key matching two objects must be refused, got %v", err)
	}
}
