package cluster

import (
	"context"
	"errors"
	"testing"
)

func esoCRD(owner string) map[string]any {
	labels := map[string]any{}
	if owner != "" {
		labels[componentOwnerLabel] = owner
	}
	return map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
		"metadata": map[string]any{"name": esoOwnerProbeCRD, "labels": labels},
	}
}

func TestCheckComponentOwner(t *testing.T) {
	spec := HelmChartSpec{Name: "external-secrets", CRDBundle: "external-secrets", Owner: "proj.prod"}
	cases := []struct {
		name    string
		seed    map[string]any
		spec    HelmChartSpec
		wantErr bool
	}{
		{"no CRD yet", nil, spec, false},
		{"same owner re-applies", esoCRD("proj.prod"), spec, false},
		{"unlabelled CRD is adopted", esoCRD(""), spec, false},
		{"another env's install is refused", esoCRD("other.dev"), spec, true},
		{"another chart is not judged", esoCRD("other.dev"), HelmChartSpec{Name: "cert-manager", CRDBundle: "cert-manager", Owner: "proj.prod"}, false},
		{"no owner, no judgement", esoCRD("other.dev"), HelmChartSpec{Name: "external-secrets", CRDBundle: "external-secrets"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newFakeCluster(t)
			if tc.seed != nil {
				c.seed(tc.seed)
			}
			err := checkComponentOwner(context.Background(), "ctx", tc.spec)
			var oe *ComponentOwnerError
			if tc.wantErr != errors.As(err, &oe) {
				t.Fatalf("err = %v, want refusal %v", err, tc.wantErr)
			}
			if tc.wantErr && (oe.Holder != "other.dev" || oe.Wanted != "proj.prod") {
				t.Errorf("refusal = %+v", oe)
			}
		})
	}
}
