package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubLiveCIDRs points the live-cluster read at a fixed answer for the
// duration of one test.
func stubLiveCIDRs(t *testing.T, fn func(context.Context, string) (liveClusterCIDRs, error)) {
	t.Helper()
	orig := liveClusterCIDRsFn
	t.Cleanup(func() { liveClusterCIDRsFn = orig })
	liveClusterCIDRsFn = fn
}

// k3s's out-of-the-box allocation, verified against both live k3d clusters on
// this machine: one node slice 10.42.0.0/24, kubernetes ClusterIP 10.43.0.1.
var k3sDefaultLiveCIDRs = liveClusterCIDRs{
	PodCIDRs:  []string{"10.42.0.0/24"},
	ServiceIP: "10.43.0.1",
}

// TestClusterCIDRDriftDetectsDeclarationThatNeverTookEffect is the bug this
// guard exists for: an operator declares disjoint CIDRs to fix cross-cluster
// pod routing against a cluster that already exists, k3s ignores it entirely,
// and nothing says so.
func TestClusterCIDRDriftDetectsDeclarationThatNeverTookEffect(t *testing.T) {
	c := ClusterEntity{Name: "cp-daemon", ClusterCIDR: "10.52.0.0/16", ServiceCIDR: "10.53.0.0/16"}
	drift := clusterCIDRDrift(c, k3sDefaultLiveCIDRs)
	if len(drift) != 2 {
		t.Fatalf("drift = %v; want one line per declared CIDR", drift)
	}
	if !strings.Contains(drift[0], "10.52.0.0/16") || !strings.Contains(drift[0], "10.42.0.0/24") {
		t.Fatalf("pod-CIDR drift line names neither side: %q", drift[0])
	}
	if !strings.Contains(drift[1], "10.53.0.0/16") || !strings.Contains(drift[1], "10.43.0.1") {
		t.Fatalf("service-CIDR drift line names neither side: %q", drift[1])
	}
}

// TestClusterCIDRDriftEmptyDeclarationMatchesAnything pins the "I don't care"
// contract: a cluster that does not use this capability must behave exactly
// as it did before the capability existed.
func TestClusterCIDRDriftEmptyDeclarationMatchesAnything(t *testing.T) {
	if drift := clusterCIDRDrift(ClusterEntity{Name: "control-plane"}, k3sDefaultLiveCIDRs); len(drift) != 0 {
		t.Fatalf("undeclared CIDRs reported drift: %v", drift)
	}
	// Half-declared: only the declared half is checked.
	onlyPod := ClusterEntity{Name: "cp", ClusterCIDR: "10.52.0.0/16"}
	if drift := clusterCIDRDrift(onlyPod, k3sDefaultLiveCIDRs); len(drift) != 1 ||
		!strings.Contains(drift[0], "cluster_cidr") {
		t.Fatalf("drift = %v; want only the pod-CIDR line", drift)
	}
}

// TestClusterCIDRDriftAcceptsNodeSlicesInsideTheDeclaredBlock is why the
// comparison is containment and not string equality: when the declaration DID
// take effect, the live values still never equal it — the controller manager
// carves a /24 per node out of the cluster CIDR, and the Service CIDR shows up
// as a single ClusterIP.
func TestClusterCIDRDriftAcceptsNodeSlicesInsideTheDeclaredBlock(t *testing.T) {
	c := ClusterEntity{Name: "cp-daemon", ClusterCIDR: "10.52.0.0/16", ServiceCIDR: "10.53.0.0/16"}
	live := liveClusterCIDRs{
		PodCIDRs:  []string{"10.52.0.0/24", "10.52.1.0/24"},
		ServiceIP: "10.53.0.1",
	}
	if drift := clusterCIDRDrift(c, live); len(drift) != 0 {
		t.Fatalf("an honoured declaration reported drift: %v", drift)
	}
}

// TestClusterCIDRDriftRejectsBlockWiderThanDeclared guards the containment
// check's second half. A live /8 whose base address happens to fall inside the
// declared /16 is NOT the declared allocation — pods would land outside it.
func TestClusterCIDRDriftRejectsBlockWiderThanDeclared(t *testing.T) {
	c := ClusterEntity{Name: "cp", ClusterCIDR: "10.52.0.0/16"}
	live := liveClusterCIDRs{PodCIDRs: []string{"10.52.0.0/8"}}
	if drift := clusterCIDRDrift(c, live); len(drift) != 1 {
		t.Fatalf("drift = %v; want the wider live block reported", drift)
	}
}

// TestClusterCIDRDriftIgnoresUnparseableValues keeps false positives out. This
// guard refuses to start a cluster, so it must only report what it can prove.
func TestClusterCIDRDriftIgnoresUnparseableValues(t *testing.T) {
	c := ClusterEntity{Name: "cp", ClusterCIDR: "not-a-cidr", ServiceCIDR: "10.53.0.0/16"}
	live := liveClusterCIDRs{PodCIDRs: []string{"", "garbage"}, ServiceIP: ""}
	if drift := clusterCIDRDrift(c, live); len(drift) != 0 {
		t.Fatalf("unparseable values produced drift: %v", drift)
	}
}

// TestCheckClusterCIDRDriftErrorNamesRemediation pins the message contract:
// what differs, why the live cluster cannot be changed in place, and the
// literal command — via recreateClusterCommand, so a cluster that owns nested
// secondaries names them rather than handing over a half-working delete.
func TestCheckClusterCIDRDriftErrorNamesRemediation(t *testing.T) {
	stubLiveCIDRs(t, func(_ context.Context, kctx string) (liveClusterCIDRs, error) {
		if kctx != "k3d-control-plane" {
			t.Fatalf("read kubectl context = %q; want k3d-control-plane", kctx)
		}
		return k3sDefaultLiveCIDRs, nil
	})

	owner := ClusterEntity{Name: "control-plane", ClusterCIDR: "10.52.0.0/16"}
	declared := []ClusterEntity{
		owner,
		{Name: "cp-daemon", Network: "k3d-control-plane", RegistryInherit: true},
	}
	err := checkClusterCIDRDrift(t.Context(), owner, declared, "dev")
	if err == nil {
		t.Fatal("inert CIDR declaration was accepted silently")
	}
	msg := err.Error()
	for _, want := range []string{
		"10.52.0.0/16",
		"10.42.0.0/24",
		"k3s fixes both at cluster-create time",
		"k3d cluster delete cp-daemon control-plane && forge env up dev",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error missing %q:\n%s", want, msg)
		}
	}
}

// TestCheckClusterCIDRDriftUndeclaredSkipsTheRead proves the fast path costs
// nothing: a cluster declaring no CIDRs must not even shell out to kubectl.
func TestCheckClusterCIDRDriftUndeclaredSkipsTheRead(t *testing.T) {
	stubLiveCIDRs(t, func(context.Context, string) (liveClusterCIDRs, error) {
		t.Fatal("read the live cluster for a cluster that declares no CIDRs")
		return liveClusterCIDRs{}, nil
	})
	if err := checkClusterCIDRDrift(t.Context(), ClusterEntity{Name: "control-plane"}, nil, "dev"); err != nil {
		t.Fatalf("undeclared CIDRs failed reconcile: %v", err)
	}
}

// TestCheckClusterCIDRDriftReadFailureIsAWarning mirrors checkClusterPortDrift:
// an unreadable live cluster is a warning and a no-op, never a hard failure —
// it must not block the warm-run fast path.
func TestCheckClusterCIDRDriftReadFailureIsAWarning(t *testing.T) {
	stubLiveCIDRs(t, func(context.Context, string) (liveClusterCIDRs, error) {
		return liveClusterCIDRs{}, errors.New("kubectl: connection refused")
	})
	c := ClusterEntity{Name: "control-plane", ClusterCIDR: "10.52.0.0/16"}
	if err := checkClusterCIDRDrift(t.Context(), c, nil, "dev"); err != nil {
		t.Fatalf("a live-read failure blocked reconcile: %v", err)
	}
}

// TestReconcileExistingClusterRefusesOnCIDRDrift is the call-site test, and
// the one that matters most: a drift check nothing invokes is a helper that
// passes its own tests while `forge env up` stays silent. This drives the real
// reconcileExistingCluster and requires it to refuse.
func TestReconcileExistingClusterRefusesOnCIDRDrift(t *testing.T) {
	origLB, origHealthy, origDNS := ensureClusterLBFreshFn, ensureRunningClusterHealthyFn, ensureClusterHostGatewayDNSFn
	t.Cleanup(func() {
		ensureClusterLBFreshFn, ensureRunningClusterHealthyFn, ensureClusterHostGatewayDNSFn = origLB, origHealthy, origDNS
	})
	ensureClusterLBFreshFn = func(context.Context, string) error { return nil }
	ensureRunningClusterHealthyFn = func(context.Context, ClusterEntity) error { return nil }
	ensureClusterHostGatewayDNSFn = func(context.Context, string) error { return nil }
	stubLiveCIDRs(t, func(context.Context, string) (liveClusterCIDRs, error) {
		return k3sDefaultLiveCIDRs, nil
	})

	c := ClusterEntity{Name: "cp-daemon", ClusterCIDR: "10.52.0.0/16", ServiceCIDR: "10.53.0.0/16"}
	err := reconcileExistingCluster(t.Context(), c,
		k3dClusterRuntimeState{Exists: true, Running: true}, []ClusterEntity{c}, "", "dev")
	if err == nil {
		t.Fatal("reconcile reported success for a cluster whose declared CIDRs never took effect")
	}
	if !strings.Contains(err.Error(), "k3d cluster delete cp-daemon && forge env up dev") {
		t.Fatalf("reconcile error did not carry the recreate command:\n%s", err)
	}
}

// TestReconcileExistingClusterUnchangedWithoutCIDRs is the regression half: a
// cluster that declares no CIDRs must reconcile exactly as it did before.
func TestReconcileExistingClusterUnchangedWithoutCIDRs(t *testing.T) {
	origLB, origHealthy, origDNS := ensureClusterLBFreshFn, ensureRunningClusterHealthyFn, ensureClusterHostGatewayDNSFn
	t.Cleanup(func() {
		ensureClusterLBFreshFn, ensureRunningClusterHealthyFn, ensureClusterHostGatewayDNSFn = origLB, origHealthy, origDNS
	})
	ensureClusterLBFreshFn = func(context.Context, string) error { return nil }
	ensureRunningClusterHealthyFn = func(context.Context, ClusterEntity) error { return nil }
	ensureClusterHostGatewayDNSFn = func(context.Context, string) error { return nil }
	stubLiveCIDRs(t, func(context.Context, string) (liveClusterCIDRs, error) {
		t.Fatal("reconcile read live CIDRs for a cluster that declares none")
		return liveClusterCIDRs{}, nil
	})

	err := reconcileExistingCluster(t.Context(), ClusterEntity{Name: "control-plane"},
		k3dClusterRuntimeState{Exists: true, Running: true}, nil, "", "dev")
	if err != nil {
		t.Fatalf("reconcileExistingCluster: %v", err)
	}
}
