package cli

import (
	"context"
	"strings"
	"testing"
)

// TestLookupClusterProvider_DefaultsToK3d pins that an entity with no
// declared Provider (every hand-built ClusterEntity{} literal in this
// package's existing tests, and the KCL schema's own default) resolves to
// the k3d provider — the behavior-preserving default M0.4 requires.
func TestLookupClusterProvider_DefaultsToK3d(t *testing.T) {
	p, err := lookupClusterProvider("")
	if err != nil {
		t.Fatalf("lookupClusterProvider(\"\"): %v", err)
	}
	if _, ok := p.(k3dClusterProvider); !ok {
		t.Fatalf("lookupClusterProvider(\"\") = %T; want k3dClusterProvider", p)
	}
}

// TestLookupClusterProvider_K3dExplicit pins that an explicit "k3d"
// resolves the same as the empty-string default.
func TestLookupClusterProvider_K3dExplicit(t *testing.T) {
	p, err := lookupClusterProvider("k3d")
	if err != nil {
		t.Fatalf("lookupClusterProvider(\"k3d\"): %v", err)
	}
	if _, ok := p.(k3dClusterProvider); !ok {
		t.Fatalf("lookupClusterProvider(\"k3d\") = %T; want k3dClusterProvider", p)
	}
}

// TestLookupClusterProvider_VClusterAndGKEAreDeclaredButRejected pins that
// "vcluster" and "gke" are REGISTERED (a project may declare them) but
// their Ensure refuses with a clear not-yet-implemented error rather than
// silently doing nothing or falling back to k3d.
func TestLookupClusterProvider_VClusterAndGKEAreDeclaredButRejected(t *testing.T) {
	for _, providerID := range []string{"vcluster", "gke"} {
		p, err := lookupClusterProvider(providerID)
		if err != nil {
			t.Fatalf("lookupClusterProvider(%q): %v", providerID, err)
		}
		err = p.Ensure(t.Context(), ClusterEntity{Name: "x", Provider: providerID}, nil, "", "dev")
		if err == nil {
			t.Fatalf("provider %q Ensure succeeded; want a not-yet-implemented refusal", providerID)
		}
		if !strings.Contains(err.Error(), "not implement") {
			t.Fatalf("provider %q Ensure error = %q; want it to say forge does not implement it yet", providerID, err)
		}
	}
}

// TestLookupClusterProvider_UnknownProviderErrors pins that an
// unregistered provider string is a clear error, not a nil provider a
// caller might dereference.
func TestLookupClusterProvider_UnknownProviderErrors(t *testing.T) {
	if _, err := lookupClusterProvider("openshift"); err == nil {
		t.Fatal("lookupClusterProvider(\"openshift\") succeeded; want an error naming it unregistered")
	}
}

// TestReconcileDeclaredClusters_RejectsUnimplementedProviderBeforeAnyK3dWork
// asserts the registry dispatch fires BEFORE any k3d work runs: a
// mixed-provider env whose first cluster is unimplemented must fail before
// ever shelling out for a later k3d one. The k3d seams are left untouched
// (nil) so a call into any of them panics/fails the test — the assertion
// IS that reconcile never reaches them.
func TestReconcileDeclaredClusters_RejectsUnimplementedProviderBeforeAnyK3dWork(t *testing.T) {
	withReachableDocker(t)
	origState := clusterRuntimeStateFn
	t.Cleanup(func() { clusterRuntimeStateFn = origState })
	clusterRuntimeStateFn = func(context.Context, string) (k3dClusterRuntimeState, error) {
		t.Fatal("k3d runtime-state lookup ran for an unimplemented-provider cluster")
		return k3dClusterRuntimeState{}, nil
	}

	clusters := []ClusterEntity{
		{Name: "vc", Provider: "vcluster"},
		{Name: "cp"}, // k3d default — must never be reached
	}
	err := reconcileDeclaredClusters(t.Context(), clusters, "", "dev")
	if err == nil {
		t.Fatal("reconcileDeclaredClusters succeeded; want the unimplemented provider to refuse")
	}
	if !strings.Contains(err.Error(), "vc") || !strings.Contains(err.Error(), "not implement") {
		t.Fatalf("error = %q; want it to name cluster %q and say not implemented", err, "vc")
	}
}
