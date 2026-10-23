package cluster

import (
	"strings"
	"testing"
)

func pdbStream(replicas int, pdbSpec string) string {
	return `apiVersion: apps/v1
kind: Deployment
metadata:
  name: bao
  namespace: ns
  labels: {app.kubernetes.io/name: bao}
spec:
  replicas: ` + string(rune('0'+replicas)) + `
  template:
    metadata:
      labels: {app.kubernetes.io/name: bao}
---
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: bao-pdb
  namespace: ns
  labels: {app.kubernetes.io/name: bao, app.kubernetes.io/managed-by: forge}
spec:
  ` + pdbSpec + `
  selector:
    matchLabels: {app.kubernetes.io/name: bao}
`
}

func TestCheckPDBs(t *testing.T) {
	cases := []struct {
		name     string
		replicas int
		spec     string
		blocks   bool
	}{
		{"minAvailable 1 on 1 replica", 1, "minAvailable: 1", true},
		{"minAvailable 3 on 3 replicas", 3, "minAvailable: 3", true},
		{"maxUnavailable 0", 3, "maxUnavailable: 0", true},
		{"minAvailable 100%", 3, `minAvailable: "100%"`, true},
		{"maxUnavailable 1 on 3 replicas", 3, "maxUnavailable: 1", false},
		{"minAvailable 1 on 3 replicas", 3, "minAvailable: 1", false},
		{"minAvailable 50%", 3, `minAvailable: "50%"`, false},
	}
	for _, c := range cases {
		got := CheckPDBs(pdbStream(c.replicas, c.spec))
		if (len(got) > 0) != c.blocks {
			t.Errorf("%s: findings=%v, want blocks=%v", c.name, got, c.blocks)
		}
		if c.blocks && len(got) == 1 && !strings.Contains(got[0].String(), "bao-pdb") {
			t.Errorf("%s: finding does not name the object: %s", c.name, got[0])
		}
	}
}

func TestStalePDBs(t *testing.T) {
	render := pdbStream(1, "") // workload bao present, no PDB doc content matters
	render = strings.Split(render, "---")[0]
	live := []livePDB{
		mk("bao-pdb", map[string]string{"app.kubernetes.io/managed-by": "forge", "app.kubernetes.io/name": "bao"}),
		mk("other-pdb", map[string]string{"app.kubernetes.io/managed-by": "forge", "app.kubernetes.io/name": "gone"}),
		mk("user-pdb", map[string]string{"app.kubernetes.io/name": "bao"}),
	}
	got := stalePDBs(live, render)
	if len(got) != 1 || got[0] != "bao-pdb" {
		t.Fatalf("stale = %v, want [bao-pdb] (not the non-forge PDB, not an absent workload's)", got)
	}
	if got := stalePDBs(live, pdbStream(3, "maxUnavailable: 1")); len(got) != 0 {
		t.Fatalf("PDB still rendered must be kept, got %v", got)
	}
}

func mk(name string, labels map[string]string) livePDB {
	var p livePDB
	p.Metadata.Name = name
	p.Metadata.Labels = labels
	return p
}
