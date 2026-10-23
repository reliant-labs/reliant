package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/deploytarget"
)

// The dev database must exist BEFORE the first cluster is applied, not only
// before the host services start.
//
// Incident: control-plane's dev env runs workspace-controller IN-CLUSTER
// against a per-worktree database on the docker-compose postgres. forge's
// ensureDevDatabase — which already creates every declared dev DSN, the
// host.k3d.internal ones included — ran in `forge env up`'s HOST phase, and
// `forge env up` only reaches that phase after the cluster deploy's rollout
// succeeds. On a fresh worktree the controller crash-looped on
// `database "control_plane_control_plane_dev_<key>" does not exist`, the
// rollout never converged, and the step that would have created the database
// never ran. The fix is a barrier at the one point where the infra exists and
// no cluster workload has been sent: between the infra groups and the first
// cluster group.

// TestDispatchDeployGroups_BeforeClustersRunsAtTheInfraBoundary pins the
// placement: after every infra group, before the first cluster apply, once.
//
// Mutation that fails it: call the hook before the loop (infra not up yet) or
// per cluster group (twice).
func TestDispatchDeployGroups_BeforeClustersRunsAtTheInfraBoundary(t *testing.T) {
	var log []string
	reg := &deploytarget.Registry{}
	reg.Register(&fakeRolloutApplier{fakeProvider: fakeProvider{id: "k8s-cluster"}, log: &log})
	reg.Register(&orderedProvider{fakeProvider: fakeProvider{id: "compose"}, log: &log})
	reg.Register(&orderedProvider{fakeProvider: fakeProvider{id: "host-infra"}, log: &log})
	groups := []deploytarget.ServiceGroup{
		{ProviderID: "compose"},
		{ProviderID: "host-infra"},
		{ProviderID: "k8s-cluster", Cluster: "k3d-hub", Namespace: "ns"},
		{ProviderID: "k8s-cluster", Cluster: "k3d-edge", Namespace: "ns"},
	}
	ensure := func(context.Context) error {
		log = append(log, "ensure databases")
		return nil
	}
	if err := dispatchDeployGroupsBeforeClusters(context.Background(), reg, groups, ensure); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	want := "deploy compose, deploy host-infra, ensure databases, apply k3d-hub, apply k3d-edge"
	if got := strings.Join(log, ", "); got != want {
		t.Errorf("dispatch order =\n  %s\nwant\n  %s", got, want)
	}
}

// TestDispatchDeployGroups_BeforeClustersFailureAppliesNoCluster: a database
// that cannot be ensured stops the deploy before any workload is sent — the
// same fail-closed rule as the pre-rollout Job gate.
func TestDispatchDeployGroups_BeforeClustersFailureAppliesNoCluster(t *testing.T) {
	var log []string
	reg := &deploytarget.Registry{}
	reg.Register(&fakeRolloutApplier{fakeProvider: fakeProvider{id: "k8s-cluster"}, log: &log})
	groups := []deploytarget.ServiceGroup{{ProviderID: "k8s-cluster", Cluster: "k3d-hub", Namespace: "ns"}}
	err := dispatchDeployGroupsBeforeClusters(context.Background(), reg, groups, func(context.Context) error {
		return errors.New("connection refused")
	})
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("want the hook's error, got %v", err)
	}
	if len(log) != 0 {
		t.Errorf("no cluster may be applied after the hook fails, got %v", log)
	}
}

// TestDispatchDeployGroups_BeforeClustersSkippedWithoutClusters: an env with
// no cluster group never runs the hook — a compose-only deploy has no
// in-cluster consumer to prepare for.
func TestDispatchDeployGroups_BeforeClustersSkippedWithoutClusters(t *testing.T) {
	var log []string
	reg := &deploytarget.Registry{}
	reg.Register(&orderedProvider{fakeProvider: fakeProvider{id: "compose"}, log: &log})
	called := false
	err := dispatchDeployGroupsBeforeClusters(context.Background(), reg,
		[]deploytarget.ServiceGroup{{ProviderID: "compose"}},
		func(context.Context) error { called = true; return nil })
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if called {
		t.Error("the hook must not run for a deploy with no cluster group")
	}
}
