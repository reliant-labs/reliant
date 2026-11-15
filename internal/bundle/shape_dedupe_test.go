package bundle

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// The stream control-plane's render actually produced, reduced to the two
// documents that collide: the env Namespace emitted UNSTAMPED (which the
// deploy router replicates to every cluster of the env) and the SAME
// namespace emitted again stamped to one cluster. Both land on cluster `a`,
// under one ObjectKey, with different bodies.
const collidingNamespaceStream = `# cluster: a, b
apiVersion: v1
kind: Namespace
metadata:
  name: shop-prod
  labels:
    pod-security.kubernetes.io/enforce: restricted
---
# cluster: a
apiVersion: v1
kind: Namespace
metadata:
  name: shop-prod
  labels:
    forge.dev/cluster: a
    pod-security.kubernetes.io/enforce: baseline
`

// TestProjectShapeRefusesTwoBodiesUnderOneKey is the F-SHAPE regression.
//
// Two Namespace documents reach cluster `a` under the same ObjectKey with
// DIFFERENT bodies — one enforcing restricted PSA, one baseline. `kubectl
// apply` is last-wins, so which one survives depends on document order:
// exactly the "two writers, one object" conflict a shape exists to expose.
//
// The refusal must NAME the key, because the reason a reader needs is which
// object has two writers, and a bare "invalid" sends them back to the render.
func TestProjectShapeRefusesTwoBodiesUnderOneKey(t *testing.T) {
	_, err := ProjectShape(ShapeInput{
		Kind:      release.EnvPersistent,
		Clusters:  []string{"a", "b"},
		Manifests: collidingNamespaceStream,
	})
	if err == nil {
		t.Fatal("ProjectShape accepted two different bodies under one ObjectKey")
	}
	if !strings.Contains(err.Error(), "shop-prod") {
		t.Errorf("error does not name the colliding object: %v", err)
	}
}

// TestProjectShapeCollapsesIdenticalDuplicates: two declarations can each
// legitimately require the same object to exist, and forge's own render
// replicates an unattributed env-level object to every cluster. Applying the
// same bytes twice leaves the same object, so the shape names it ONCE —
// matching what a deploy actually produces.
func TestProjectShapeCollapsesIdenticalDuplicates(t *testing.T) {
	const identical = `# cluster: a
apiVersion: v1
kind: Namespace
metadata:
  name: shop-prod
  labels:
    pod-security.kubernetes.io/enforce: restricted
---
# cluster: a
apiVersion: v1
kind: Namespace
metadata:
  name: shop-prod
  labels:
    pod-security.kubernetes.io/enforce: restricted
`
	shape, err := ProjectShape(ShapeInput{
		Kind:      release.EnvPersistent,
		Clusters:  []string{"a"},
		Manifests: identical,
	})
	if err != nil {
		t.Fatalf("ProjectShape refused two identical documents: %v", err)
	}
	if len(shape.Objects) != 1 {
		t.Fatalf("objects: got %d, want 1", len(shape.Objects))
	}
	if o := shape.Objects[0]; o.Cluster != "a" || o.Name != "shop-prod" {
		t.Errorf("got cluster %q name %q, want a shop-prod", o.Cluster, o.Name)
	}
}

// TestProjectShapeKeepsOneObjectPerClusterItLandsOn pins the half of the
// expansion that is CORRECT and must survive the fix: a document genuinely
// routed to two clusters is two objects, because drift is a per-cluster fact.
// Only a SECOND document under the same key is a conflict.
func TestProjectShapeKeepsOneObjectPerClusterItLandsOn(t *testing.T) {
	shape, err := ProjectShape(ShapeInput{
		Kind:     release.EnvPersistent,
		Clusters: []string{"a", "b"},
		Manifests: `# cluster: a, b
apiVersion: v1
kind: Namespace
metadata:
  name: shop-prod
`,
	})
	if err != nil {
		t.Fatalf("ProjectShape: %v", err)
	}
	var clusters []string
	for _, o := range shape.Objects {
		clusters = append(clusters, o.Cluster)
	}
	if got := strings.Join(clusters, ","); got != "a,b" {
		t.Errorf("Namespace lands on %q, want a,b", got)
	}
}
