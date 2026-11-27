package cluster

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/deploy"
	"github.com/reliant-labs/forge/pkg/release"
)

func workloadRecord(name, unit string) map[string]any {
	labels := map[string]any{
		deploy.LabelName: name, deploy.LabelManagedBy: "forge", deploy.LabelPartOf: "shop",
		ClusterRoutingLabel: "gke-prod",
	}
	if unit != "" {
		labels[release.LabelDeliveryUnit] = unit
	}
	return map[string]any{
		"apiVersion": "forge.dev/v1alpha1",
		"kind":       "Workload",
		"metadata":   map[string]any{"name": name, "namespace": "shop-prod", "labels": labels},
		"spec": map[string]any{
			"kind": "service", "image": "registry.example.com/" + name, "replicas": 1,
			"ports": []any{map[string]any{"name": "http", "port": 8080}},
		},
	}
}

// A workload's delivery unit reaches every object it renders, so the bundle
// can place the Deployment, its Service and its ServiceAccount in one tree. An
// object no workload owns (the env's NetworkPolicies) stays unplaced.
func TestExpandTierDeclarationsCarriesTheDeliveryUnit(t *testing.T) {
	out, err := expandTierDeclarations(
		[]any{workloadRecord("admin", "control-plane"), workloadRecord("plain", "")},
		&deploy.EnvNetworkPolicy{EgressPorts: []int32{443}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	unitOf := func(kind, name string) (string, bool) {
		for _, it := range out {
			m := it.(map[string]any)
			md := m["metadata"].(map[string]any)
			if m["kind"] == kind && md["name"] == name {
				labels, _ := md["labels"].(map[string]any)
				u, _ := labels[release.LabelDeliveryUnit].(string)
				return u, true
			}
		}
		return "", false
	}
	for _, kind := range []string{"Deployment", "Service", "ServiceAccount"} {
		u, ok := unitOf(kind, "admin")
		if !ok {
			t.Fatalf("no %s/admin rendered", kind)
		}
		if u != "control-plane" {
			t.Errorf("%s/admin is in unit %q, want control-plane", kind, u)
		}
		if u, _ := unitOf(kind, "plain"); u != "" {
			t.Errorf("%s/plain was placed in unit %q though its workload declares none", kind, u)
		}
	}
	for _, it := range out {
		m := it.(map[string]any)
		if m["kind"] != "NetworkPolicy" {
			continue
		}
		md := m["metadata"].(map[string]any)
		labels, _ := md["labels"].(map[string]any)
		if _, placed := labels[release.LabelDeliveryUnit]; placed && md["name"] == "default-deny-ingress" {
			t.Errorf("the env-wide NetworkPolicy %v belongs to no workload but was placed in a unit", md["name"])
		}
	}
}

// A Workload record's app.kubernetes.io/version label (Bundle.release) reaches
// the pod template, so a file-scraping node agent can report service.version.
func TestExpandTierDeclarationsStampsTheReleaseOnPodTemplates(t *testing.T) {
	rec := workloadRecord("api", "")
	rec["metadata"].(map[string]any)["labels"].(map[string]any)[deploy.LabelVersion] = "20261025"
	out, err := expandTierDeclarations([]any{rec}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range out {
		m := it.(map[string]any)
		if m["kind"] != "Deployment" {
			continue
		}
		tmpl := m["spec"].(map[string]any)["template"].(map[string]any)["metadata"].(map[string]any)["labels"].(map[string]any)
		if tmpl[deploy.LabelVersion] != "20261025" {
			t.Errorf("pod template labels = %v, want %s=20261025", tmpl, deploy.LabelVersion)
		}
		sel := m["spec"].(map[string]any)["selector"].(map[string]any)["matchLabels"].(map[string]any)
		if _, ok := sel[deploy.LabelVersion]; ok {
			t.Errorf("selector carries the version: %v", sel)
		}
		return
	}
	t.Fatal("no Deployment rendered")
}

func databaseRecord(name, namespace string) map[string]any {
	return map[string]any{
		"apiVersion": "forge.dev/v1alpha1",
		"kind":       "ManagedDatabase",
		"metadata": map[string]any{"name": name, "namespace": namespace, "labels": map[string]any{
			deploy.LabelName: name, deploy.LabelManagedBy: "forge", deploy.LabelPartOf: "shop",
			ClusterRoutingLabel: "gke-prod",
		}},
		"spec": map[string]any{},
	}
}

// A Workload and a ManagedDatabase of one name in one namespace would run as
// one ServiceAccount (deploy.CheckDatabaseIdentityNames). The same name in
// another namespace is a different ServiceAccount, and is fine.
func TestExpandTierDeclarationsRefusesAWorkloadNamedLikeADatabase(t *testing.T) {
	_, err := expandTierDeclarations([]any{workloadRecord("orders", ""), databaseRecord("orders", "shop-prod")}, nil, nil)
	if err == nil {
		t.Fatal("expanded workload \"orders\" beside database \"orders\" in shop-prod: the workload would run as the database's ServiceAccount")
	}
	if _, err := expandTierDeclarations([]any{workloadRecord("orders", ""), databaseRecord("orders", "shop-data")}, nil, nil); err != nil {
		t.Errorf("a database in another namespace was refused: %v", err)
	}
}
