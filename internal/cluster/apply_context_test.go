package cluster

import (
	"context"
	"strings"
	"testing"
)

// TestKubectlApply_RefusesEmptyContext is the footgun guard: a k8s WRITE must
// never fall back to kubectl's current/default context. The target cluster is
// declarative (forge.K8sCluster.cluster), so an empty context means a group
// failed to carry its declared cluster — and applying to whatever context is
// active is how a flipped current-context (e.g. after `k3d cluster create`)
// lands a deploy in the wrong cluster. The guard returns BEFORE exec'ing
// kubectl, so this needs no cluster.
func TestKubectlApply_RefusesEmptyContext(t *testing.T) {
	for _, kctx := range []string{"", "   ", "\t"} {
		err := KubectlApply(context.Background(), kctx, "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: x\n")
		if err == nil {
			t.Fatalf("KubectlApply(kctx=%q) = nil, want error (must not fall back to current-context)", kctx)
		}
		if !strings.Contains(err.Error(), "without an explicit kubectl context") {
			t.Errorf("KubectlApply(kctx=%q) error = %v, want the declarative-context refusal", kctx, err)
		}
	}
}
