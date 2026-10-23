//go:build e2e

package cluster

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func k3dPDBStream(replicas int) string {
	s := `apiVersion: v1
kind: Namespace
metadata: {name: pdbtest}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: pdbtest
  labels: {app.kubernetes.io/name: web, app.kubernetes.io/managed-by: forge}
spec:
  replicas: %d
  selector: {matchLabels: {app.kubernetes.io/name: web}}
  template:
    metadata: {labels: {app.kubernetes.io/name: web}}
    spec:
      containers: [{name: c, image: "registry.k8s.io/pause:3.9"}]
`
	s = fmt.Sprintf(s, replicas)
	if replicas > 1 {
		s += `---
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: web-pdb
  namespace: pdbtest
  labels: {app.kubernetes.io/name: web, app.kubernetes.io/managed-by: forge}
spec:
  maxUnavailable: 1
  selector: {matchLabels: {app.kubernetes.io/name: web}}
`
	}
	return s
}

// TestPrunePDBs_K3d: replicas 3 -> PDB exists; replicas 1 -> forge's PDB is
// deleted while a non-forge PDB in the same namespace survives.
func TestPrunePDBs_K3d(t *testing.T) {
	if _, err := exec.LookPath("k3d"); err != nil {
		t.Skip("k3d not installed")
	}
	name := fmt.Sprintf("forge-pdbtest-%d", time.Now().UnixNano()%1e8)
	ctx := context.Background()
	if out, err := exec.CommandContext(ctx, "k3d", "cluster", "create", name, "--no-lb", "--wait", "--timeout", "180s").CombinedOutput(); err != nil {
		t.Fatalf("k3d create: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("k3d", "cluster", "delete", name).Run() })
	kctx := "k3d-" + name

	pdbs := func() string {
		out, _ := exec.CommandContext(ctx, "kubectl", "--context", kctx, "get", "pdb", "-n", "pdbtest", "-o", "name").CombinedOutput()
		return string(out)
	}
	apply := func(replicas int) {
		m := k3dPDBStream(replicas)
		if err := KubectlApply(ctx, kctx, m); err != nil {
			t.Fatalf("apply: %v", err)
		}
		if err := PrunePDBs(ctx, kctx, m, "pdbtest"); err != nil {
			t.Fatalf("PrunePDBs: %v", err)
		}
	}

	apply(3)
	user := exec.CommandContext(ctx, "kubectl", "--context", kctx, "apply", "-f", "-")
	user.Stdin = strings.NewReader(`apiVersion: policy/v1
kind: PodDisruptionBudget
metadata: {name: user-pdb, namespace: pdbtest, labels: {app.kubernetes.io/name: web}}
spec:
  maxUnavailable: 1
  selector: {matchLabels: {app.kubernetes.io/name: web}}
`)
	if out, err := user.CombinedOutput(); err != nil {
		t.Fatalf("user pdb: %v\n%s", err, out)
	}
	if got := pdbs(); !strings.Contains(got, "web-pdb") {
		t.Fatalf("replicas 3: web-pdb missing: %q", got)
	}

	apply(1)
	got := pdbs()
	if strings.Contains(got, "web-pdb") {
		t.Errorf("replicas 1: stale forge PDB not deleted: %q", got)
	}
	if !strings.Contains(got, "user-pdb") {
		t.Errorf("non-forge PDB was deleted: %q", got)
	}
}
