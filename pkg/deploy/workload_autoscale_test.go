package deploy

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

func autoscaledAPI(a v1alpha1.Autoscale) v1alpha1.Workload {
	return wl("api", v1alpha1.WorkloadSpec{
		Kind: v1alpha1.KindService, Image: "ghcr.io/acme/api:v1",
		Ports: []v1alpha1.Port{httpPort(true)}, Autoscale: &a,
	})
}

// TestAutoscaleRendersHPA pins the HorizontalPodAutoscaler an autoscaled
// workload renders, field by field: named after the workload, targeting its
// Deployment, one CPU utilization metric, and the stated behaviour.
func TestAutoscaleRendersHPA(t *testing.T) {
	objs := render(t, v1alpha1.ProfileFull, autoscaledAPI(v1alpha1.Autoscale{MinReplicas: 2, MaxReplicas: 6}))
	o := objects(t, objs)
	hpa, ok := o["HorizontalPodAutoscaler/api"]
	if !ok {
		t.Fatalf("no HorizontalPodAutoscaler/api in %v", keysOf(o))
	}
	if hpa["apiVersion"] != "autoscaling/v2" {
		t.Errorf("apiVersion = %v, want autoscaling/v2", hpa["apiVersion"])
	}
	if get(hpa, "metadata", "namespace") != ctx.Namespace {
		t.Errorf("namespace = %v", get(hpa, "metadata", "namespace"))
	}
	if !reflect.DeepEqual(get(hpa, "metadata", "labels"), get(o["Deployment/api"], "metadata", "labels")) {
		t.Errorf("HPA labels %v differ from the Deployment's %v", get(hpa, "metadata", "labels"), get(o["Deployment/api"], "metadata", "labels"))
	}
	want := map[string]any{
		"scaleTargetRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "api"},
		"minReplicas":    int64(2),
		"maxReplicas":    int64(6),
		"metrics": []any{map[string]any{
			"type": "Resource",
			"resource": map[string]any{
				"name":   "cpu",
				"target": map[string]any{"type": "Utilization", "averageUtilization": int64(70)},
			},
		}},
		"behavior": map[string]any{
			"scaleUp": map[string]any{
				"stabilizationWindowSeconds": int64(0),
				"selectPolicy":               "Max",
				"policies": []any{
					map[string]any{"type": "Percent", "value": int64(100), "periodSeconds": int64(60)},
					map[string]any{"type": "Pods", "value": int64(4), "periodSeconds": int64(60)},
				},
			},
			"scaleDown": map[string]any{
				"stabilizationWindowSeconds": int64(300),
				"selectPolicy":               "Max",
				"policies": []any{
					map[string]any{"type": "Percent", "value": int64(100), "periodSeconds": int64(60)},
				},
			},
		},
	}
	var spec map[string]any
	for _, u := range objs {
		if u.GetKind() == "HorizontalPodAutoscaler" {
			spec = u.Object["spec"].(map[string]any)
		}
	}
	if !reflect.DeepEqual(spec, want) {
		t.Errorf("HPA spec =\n%#v\nwant\n%#v", spec, want)
	}

	// Emitted right after the Deployment it scales.
	for i, u := range objs {
		if u.GetKind() == "Deployment" {
			if i+1 >= len(objs) || objs[i+1].GetKind() != "HorizontalPodAutoscaler" {
				t.Errorf("the HPA does not follow the Deployment: %v", kindsOf(objs))
			}
		}
	}
}

// TestAutoscaleCPUTarget: a stated target is rendered as stated.
func TestAutoscaleCPUTarget(t *testing.T) {
	o := objects(t, render(t, v1alpha1.ProfileFull, autoscaledAPI(v1alpha1.Autoscale{MinReplicas: 1, MaxReplicas: 3, CPUTargetPercent: 50})))
	if got := get(o["HorizontalPodAutoscaler/api"], "spec", "metrics", 0, "resource", "target", "averageUtilization"); got != float64(50) {
		t.Errorf("averageUtilization = %v, want 50", got)
	}
}

// TestAutoscaleDeploymentStatesNoReplicas: the HPA owns spec.replicas, so the
// Deployment must not state it — a stated count would be re-applied over
// whatever the HPA scaled to.
func TestAutoscaleDeploymentStatesNoReplicas(t *testing.T) {
	o := objects(t, render(t, v1alpha1.ProfileFull, autoscaledAPI(v1alpha1.Autoscale{MinReplicas: 2, MaxReplicas: 6})))
	spec := o["Deployment/api"]["spec"].(map[string]any)
	if v, has := spec["replicas"]; has {
		t.Errorf("an autoscaled Deployment states spec.replicas = %v", v)
	}
}

// TestAutoscaleSpreadAndPDBFollowMax: the spread and the PDB key on
// maxReplicas, so they are present for a range above one even at a minimum
// of one, and absent for a range of exactly one.
func TestAutoscaleSpreadAndPDBFollowMax(t *testing.T) {
	for _, c := range []struct {
		name  string
		a     v1alpha1.Autoscale
		multi bool
	}{
		{"min 1 max 4", v1alpha1.Autoscale{MinReplicas: 1, MaxReplicas: 4}, true},
		{"min 1 max 1", v1alpha1.Autoscale{MinReplicas: 1, MaxReplicas: 1}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := objects(t, render(t, v1alpha1.ProfileFull, autoscaledAPI(c.a)))
			_, pdb := o["PodDisruptionBudget/api-pdb"]
			spread := get(podOf(o["Deployment/api"]), "topologySpreadConstraints") != nil
			if pdb != c.multi || spread != c.multi {
				t.Errorf("pdb=%v spread=%v, want both %v", pdb, spread, c.multi)
			}
		})
	}
}

// TestNoAutoscaleRendersNoHPA: a workload without autoscale states its
// replicas and renders no HPA (the goldens pin the rest byte for byte).
func TestNoAutoscaleRendersNoHPA(t *testing.T) {
	o := objects(t, render(t, v1alpha1.ProfileFull, wl("api", v1alpha1.WorkloadSpec{
		Kind: v1alpha1.KindService, Image: "ghcr.io/acme/api:v1", Replicas: 3, Ports: []v1alpha1.Port{httpPort(true)},
	})))
	if _, has := o["HorizontalPodAutoscaler/api"]; has {
		t.Error("a workload without autoscale rendered a HorizontalPodAutoscaler")
	}
	if got := get(o["Deployment/api"], "spec", "replicas"); got != float64(3) {
		t.Errorf("spec.replicas = %v, want 3", got)
	}
}

// TestAutoscaleRefusedBeforeRender: an invalid autoscale renders nothing,
// under either profile.
func TestAutoscaleRefusedBeforeRender(t *testing.T) {
	for _, p := range []v1alpha1.Profile{v1alpha1.ProfileFull, v1alpha1.ProfileRestricted} {
		objs, err := RenderWorkloads([]v1alpha1.Workload{autoscaledAPI(v1alpha1.Autoscale{MinReplicas: 4, MaxReplicas: 2})}, p, ctx)
		mustErr(t, err, "must not exceed autoscale.maxReplicas")
		if objs != nil {
			t.Errorf("%s: a refused set must emit nothing", p)
		}
	}
}

func kindsOf(objs []*unstructured.Unstructured) []string {
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.GetKind())
	}
	return out
}
