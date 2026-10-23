package cluster

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// Two contexts shaped like control-plane prod's: a regional hub and a zonal
// daemon cluster. The zonal one sorts FIRST, which is why nothing about the
// guard may depend on order.
const (
	hubCtx    = "gke_acme_us-central1_prod"
	daemonCtx = "gke_acme_us-central1-a_prod-daemon"
)

// twoClusterStream is the incident's shape: ONE render carrying both
// clusters' objects, each stamped with the context it belongs on. The daemon
// half is an operator with its CRD, Namespace and Deployment, plus a
// cluster-scoped StorageClass — what landed on prod's main cluster on
// 2026-10-09.
var twoClusterStream = strings.Join([]string{
	`apiVersion: v1
kind: Namespace
metadata:
  name: app
  labels:
    forge.dev/cluster: ` + hubCtx,
	`apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: app
  labels:
    forge.dev/cluster: ` + hubCtx + `
spec: {}`,
	`apiVersion: v1
kind: ConfigMap
metadata:
  name: shared
  namespace: app
data: {}`,
	`apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: clusters.postgresql.cnpg.io
  labels:
    forge.dev/cluster: ` + daemonCtx + `
spec:
  group: postgresql.cnpg.io`,
	`apiVersion: v1
kind: Namespace
metadata:
  name: cnpg-system
  labels:
    forge.dev/cluster: ` + daemonCtx,
	`apiVersion: apps/v1
kind: Deployment
metadata:
  name: cloudnative-pg
  namespace: cnpg-system
  labels:
    forge.dev/cluster: ` + daemonCtx + `
spec: {}`,
	`apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: workspace-ssd
  labels:
    forge.dev/cluster: ` + daemonCtx + `
provisioner: pd.csi.storage.gke.io`,
}, "\n---\n")

func TestRefuseForeignClusterDocs(t *testing.T) {
	t.Parallel()
	unlabeled := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n  namespace: app\n"
	own := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n  labels:\n    forge.dev/cluster: " + hubCtx + "\n"
	cases := []struct {
		name      string
		kctx      string
		manifests string
		refuse    bool
	}{
		{"unlabeled objects make no claim", hubCtx, unlabeled, false},
		{"objects stamped for this context", hubCtx, own, false},
		{"an unparseable doc makes no claim", hubCtx, "kind: [unterminated\n", false},
		{"no context is the empty-context guard's call", "", twoClusterStream, false},
		{"another cluster's objects are refused", hubCtx, twoClusterStream, true},
		{"from either side", daemonCtx, twoClusterStream, true},
		{"surrounding whitespace on the context does not matter", "  " + hubCtx + " ", twoClusterStream, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := refuseForeignClusterDocs(tc.kctx, tc.manifests)
			if got := err != nil; got != tc.refuse {
				t.Fatalf("refuseForeignClusterDocs(%q) err = %v, want refuse=%v", tc.kctx, err, tc.refuse)
			}
		})
	}
}

// TestRefuseForeignClusterDocs_NamesEveryMisroutedObject: the refusal is the
// operator's only clue, so it must say which objects, where they belong, and
// how many — and never name an object that IS on the right cluster.
func TestRefuseForeignClusterDocs_NamesEveryMisroutedObject(t *testing.T) {
	t.Parallel()
	err := refuseForeignClusterDocs(hubCtx, twoClusterStream)
	if err == nil {
		t.Fatal("want a refusal")
	}
	msg := err.Error()
	for _, want := range []string{
		`kubectl context "` + hubCtx + `"`,
		"4 object(s)",
		"nothing was applied",
		"CustomResourceDefinition clusters.postgresql.cnpg.io → " + daemonCtx,
		"Namespace cnpg-system → " + daemonCtx,
		"Deployment cnpg-system/cloudnative-pg → " + daemonCtx,
		"StorageClass workspace-ssd → " + daemonCtx,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal does not mention %q:\n%s", want, msg)
		}
	}
	for _, notWant := range []string{"app/api", "Namespace app ", "shared"} {
		if strings.Contains(msg, notWant) {
			t.Errorf("refusal names %q, which belongs on this cluster:\n%s", notWant, msg)
		}
	}
}

func TestRefuseForeignClusterDocs_CapsTheListButNotTheCount(t *testing.T) {
	t.Parallel()
	var docs []string
	for i := range foreignClusterListCap + 5 {
		docs = append(docs, fmt.Sprintf("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cm-%02d\n  namespace: x\n  labels:\n    forge.dev/cluster: %s\n", i, daemonCtx))
	}
	err := refuseForeignClusterDocs(hubCtx, strings.Join(docs, "---\n"))
	if err == nil {
		t.Fatal("want a refusal")
	}
	msg := err.Error()
	if !strings.Contains(msg, fmt.Sprintf("%d object(s)", foreignClusterListCap+5)) || !strings.Contains(msg, "… and 5 more") {
		t.Errorf("want the exact count and a capped list:\n%s", msg)
	}
	if got := strings.Count(msg, "→ "+daemonCtx); got != foreignClusterListCap {
		t.Errorf("listed %d objects, want the cap %d", got, foreignClusterListCap)
	}
}

// TestApplyRendered_UnscopedStreamNeverWritesAnotherClustersObjects is the
// 2026-10-09 regression. A deploy that reached the apply with NO cluster scope
// — there the env-wide direct apply a failed entity read fell through to —
// handed the whole two-cluster stream to the hub's context, and kubectl wrote
// the daemon cluster's operator, CRDs and StorageClass onto the hub.
//
// The apply must refuse before the FIRST kubectl call: not one pass may run,
// or the hub's half lands and the deploy is left partial. A dry run refuses
// identically, so a preview never shows a stream the real apply would refuse.
func TestApplyRendered_UnscopedStreamNeverWritesAnotherClustersObjects(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("dryRun=%v", dryRun), func(t *testing.T) {
			readCalls := fakeKubectlRecorder(t)
			opts := ApplyOpts{
				Namespace: "app",
				Context:   hubCtx,
				DryRun:    dryRun,
				Rollout:   RolloutPolicy{Mode: RolloutSkip},
			}
			err := applyRendered(context.Background(), opts, twoClusterStream)
			if err == nil {
				t.Fatal("applyRendered of an unscoped two-cluster stream = nil, want a refusal")
			}
			if !strings.Contains(err.Error(), "Deployment cnpg-system/cloudnative-pg → "+daemonCtx) {
				t.Errorf("refusal does not name the misrouted operator: %v", err)
			}
			if calls := readCalls(); len(calls) != 0 {
				t.Errorf("kubectl ran %d time(s) before the refusal; want none:\n%v", len(calls), calls)
			}
		})
	}
}

// TestKubectlApply_RefusesObjectsStampedForAnotherCluster: the write
// chokepoint holds the same line for every caller (charts, Secrets, minted
// kubeconfigs, the pre-rollout passes), not only for the env stream.
func TestKubectlApply_RefusesObjectsStampedForAnotherCluster(t *testing.T) {
	readCalls := fakeKubectlRecorder(t)
	err := KubectlApply(context.Background(), hubCtx, twoClusterStream)
	if err == nil {
		t.Fatal("KubectlApply of another cluster's objects = nil, want a refusal")
	}
	if calls := readCalls(); len(calls) != 0 {
		t.Errorf("kubectl ran %d time(s); a refused write must exec nothing:\n%v", len(calls), calls)
	}
}

// TestApplyRendered_ScopedStreamAppliesOnlyItsOwnCluster is the other half:
// the guard must not get in the way of a correctly scoped multi-cluster apply.
// Each side applies, and nothing stamped for the other side reaches kubectl.
func TestApplyRendered_ScopedStreamAppliesOnlyItsOwnCluster(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through several shell subprocesses; runs in task test")
	}
	for _, side := range []struct{ kctx, other string }{{hubCtx, daemonCtx}, {daemonCtx, hubCtx}} {
		t.Run(side.kctx, func(t *testing.T) {
			readCalls := fakeKubectlRecorder(t)
			opts := ApplyOpts{
				Namespace:    "app",
				Context:      side.kctx,
				Rollout:      RolloutPolicy{Mode: RolloutSkip},
				ClusterScope: &GroupScope{Cluster: side.kctx},
			}
			if err := applyRendered(context.Background(), opts, twoClusterStream); err != nil {
				t.Fatalf("scoped applyRendered: %v", err)
			}
			calls := readCalls()
			applied := 0
			for _, c := range calls {
				if !strings.Contains(c.Args, " apply ") {
					continue
				}
				applied++
				if !strings.Contains(c.Args, "--context "+side.kctx) {
					t.Errorf("apply ran against the wrong context: %s", c.Args)
				}
				if strings.Contains(c.Stdin, "forge.dev/cluster: "+side.other) {
					t.Errorf("an object stamped for %s reached %s's apply:\n%s", side.other, side.kctx, c.Stdin)
				}
			}
			if applied == 0 {
				t.Fatalf("no apply ran for %s:\n%v", side.kctx, calls)
			}
		})
	}
}
