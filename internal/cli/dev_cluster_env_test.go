package cli

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// testProjectName is the temp project's forge.yaml name. It is deliberately a
// name no real k3d cluster on a developer machine can carry: the legacy
// `cluster down` fallback resolves a cluster FROM this name, so a test project
// named like a real cluster is one mutation away from deleting it.
const testProjectName = "forge-unit-test-no-such-cluster"

// deployEnabledProject chdirs into a temp project whose forge.yaml enables
// deploy. forge's OWN forge.yaml has deploy off (it is a CLI project), and every
// cluster lifecycle path gates on it.
func deployEnabledProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(
		"name: "+testProjectName+"\nmodule_path: example.com/cp\n"), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	markDeployProject(t, dir) // deploy derives from deploy/kcl existing
	t.Chdir(dir)
	return dir
}

// stubEnvClusters swaps every env-scoped seam for the duration of the test and
// returns the ordered side-effect log.
func stubEnvClusters(t *testing.T, clusters []ClusterEntity, existing map[string]bool) *[]string {
	t.Helper()
	origRender, origReconcile, origDelete := renderEnvClustersFn, reconcileEnvClustersFn, deleteK3dClusterFn
	origState, origWait, origNested := lookupClusterStateForUpFn, waitDeclaredClusterReadyFn, nestedSecondariesOfFn
	t.Cleanup(func() {
		renderEnvClustersFn, reconcileEnvClustersFn, deleteK3dClusterFn = origRender, origReconcile, origDelete
		lookupClusterStateForUpFn, waitDeclaredClusterReadyFn, nestedSecondariesOfFn = origState, origWait, origNested
	})
	nestedSecondariesOfFn = func(_ context.Context, _ string) ([]string, error) { return nil, nil }
	var calls []string
	renderEnvClustersFn = func(_ context.Context, env string) ([]ClusterEntity, string, error) {
		calls = append(calls, "render:"+env)
		return clusters, "/proj", nil
	}
	reconcileEnvClustersFn = func(_ context.Context, cs []ClusterEntity, projectDir, env string) error {
		names := make([]string, len(cs))
		for i, c := range cs {
			names[i] = c.Name
		}
		calls = append(calls, "reconcile:"+env+":"+strings.Join(names, ","))
		return nil
	}
	deleteK3dClusterFn = func(_ context.Context, name string) error {
		calls = append(calls, "delete:"+name)
		return nil
	}
	lookupClusterStateForUpFn = func(_ context.Context, name string) (k3dClusterRuntimeState, error) {
		return k3dClusterRuntimeState{Exists: existing[name], Running: existing[name]}, nil
	}
	waitDeclaredClusterReadyFn = func(_ context.Context, name string) error {
		calls = append(calls, "wait:"+name)
		return nil
	}
	return &calls
}

// The pair control-plane declares: an owner with a config file and a
// secondary declared by `owner` alone.
var envPair = []ClusterEntity{
	{Name: "control-plane-v2", Config: "deploy/k3d-v2.yaml", ClusterCIDR: "10.52.0.0/16"},
	{Name: "cp-daemon-v2", Network: "k3d-control-plane-v2", RegistryInherit: true, ClusterCIDR: "10.62.0.0/16"},
}

// `forge cluster up <env>` must hand the env's DECLARED clusters — CIDRs and
// all — to the same reconcile `forge env up` runs, not create from a k3d YAML.
//
// MUTATION VERIFIED RED: making the env RunE branch call
// runDevClusterUp(ctx, configPath, wait) instead → no "reconcile:" call.
func TestClusterUpEnv_ReconcilesTheDeclaredClusters(t *testing.T) {
	deployEnabledProject(t)
	calls := stubEnvClusters(t, envPair, nil)

	cmd := newDevClusterUpCmd()
	cmd.SetArgs([]string{"e2e", "--wait"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cluster up e2e: %v", err)
	}
	want := []string{"render:e2e", "reconcile:e2e:control-plane-v2,cp-daemon-v2", "wait:control-plane-v2", "wait:cp-daemon-v2"}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}
}

// `forge cluster down <env>` deletes the secondary BEFORE its owner — k3d
// cannot remove a network a secondary is still attached to — and skips a
// cluster that does not exist.
//
// MUTATION VERIFIED RED: envClusterDeleteOrder returning the declared order
// (owner first) → delete order [control-plane-v2 cp-daemon-v2].
func TestClusterDownEnv_DeletesSecondariesBeforeOwners(t *testing.T) {
	deployEnabledProject(t)
	calls := stubEnvClusters(t, envPair, map[string]bool{"control-plane-v2": true, "cp-daemon-v2": true})

	cmd := newDevClusterDownCmd()
	cmd.SetArgs([]string{"e2e"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cluster down e2e: %v", err)
	}
	want := []string{"render:e2e", "delete:cp-daemon-v2", "delete:control-plane-v2"}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}
}

// An EXPLICIT --config naming a missing file must refuse. The historical
// behavior fell back to the forge.yaml project name, so for a project named
// like its cluster (control-plane is) a typo'd path deleted a cluster nobody
// named.
//
// MUTATION VERIFIED RED: removing the os.Stat guard in the down RunE → nil
// error, and the (stubbed) delete is reached for the project-name cluster.
func TestClusterDownConfig_MissingExplicitFileRefuses(t *testing.T) {
	deployEnabledProject(t)
	calls := stubEnvClusters(t, nil, map[string]bool{testProjectName: true})
	t.Cleanup(func() {
		for _, c := range *calls {
			if strings.HasPrefix(c, "delete:") {
				t.Errorf("a missing --config reached %s", c)
			}
		}
	})

	cmd := newDevClusterDownCmd()
	cmd.SetArgs([]string{"--config", "deploy/k3d-typo.yaml"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "refusing to guess") {
		t.Fatalf("err = %v, want a refusal naming the missing --config", err)
	}
}

// An environment and --config together are ambiguous; refuse rather than pick one.
func TestClusterUpEnv_RejectsConfigToo(t *testing.T) {
	deployEnabledProject(t)
	stubEnvClusters(t, envPair, nil)

	cmd := newDevClusterUpCmd()
	cmd.SetArgs([]string{"e2e", "--config", "deploy/k3d-v2.yaml"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err = %v, want mutually-exclusive refusal", err)
	}
}

// `forge cluster down --config <owner's file>` deletes the clusters nested on
// the owner's network FIRST. They have no config file, so --config can never
// name them, and deleting the owner alone strands them on a dead network.
//
// MUTATION VERIFIED RED: dropping the dependents loop in runDevClusterDown →
// calls = [delete:<owner>] only.
func TestClusterDownConfig_DeletesNestedSecondariesFirst(t *testing.T) {
	dir := deployEnabledProject(t)
	owner := testProjectName + "-owner"
	cfg := filepath.Join(dir, "k3d.yaml")
	if err := os.WriteFile(cfg, []byte("apiVersion: k3d.io/v1alpha5\nkind: Simple\nmetadata:\n  name: "+owner+"\n"), 0o644); err != nil {
		t.Fatalf("write k3d.yaml: %v", err)
	}
	calls := stubEnvClusters(t, nil, map[string]bool{owner: true})
	nestedSecondariesOfFn = func(_ context.Context, o string) ([]string, error) {
		if o != owner {
			t.Errorf("nested lookup for %q, want %q", o, owner)
		}
		return []string{owner + "-daemon"}, nil
	}

	cmd := newDevClusterDownCmd()
	cmd.SetArgs([]string{"--config", cfg})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cluster down --config: %v", err)
	}
	want := []string{"delete:" + owner + "-daemon", "delete:" + owner}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}
}

// The docker label listing includes the owner's own nodes and the standalone
// registry (label k3d.cluster=""); neither is a secondary.
func TestParseNestedSecondaries(t *testing.T) {
	got := parseNestedSecondaries("cp-daemon\ncp-daemon\n\ncontrol-plane\ncontrol-plane\nother\n", "control-plane")
	if want := []string{"cp-daemon", "other"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
