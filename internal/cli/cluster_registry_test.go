package cli

import (
	"context"
	"os"
	"testing"
)

// k3dUseConfig is a k3d Simple-config that references a STANDALONE registry
// via `registries.use`, with the inline containerd mirror carrying the host
// push port on its `localhost:<port>` key — the shape control-plane's
// deploy/k3d.yaml uses.
const k3dUseConfig = `apiVersion: k3d.io/v1alpha5
kind: Simple
metadata:
  name: control-plane
registries:
  use:
    - k3d-control-plane-registry:5000
  config: |
    mirrors:
      "registry.localhost:5000":
        endpoint:
          - http://k3d-control-plane-registry:5000
      "localhost:5051":
        endpoint:
          - http://k3d-control-plane-registry:5000
`

// k3dCreateConfig uses `registries.create` (cluster-owned) — forge must NOT
// ensure a standalone registry for it (k3d creates that registry itself).
const k3dCreateConfig = `apiVersion: k3d.io/v1alpha5
kind: Simple
metadata:
  name: legacy
registries:
  create:
    name: legacy-registry
    hostPort: "5051"
`

// TestParseUseRegistries pins that a `registries.use` config yields the
// registry name (internal :5000 stripped) with the host port read from the
// `localhost:<port>` mirror key.
func TestParseUseRegistries(t *testing.T) {
	refs, err := parseUseRegistries([]byte(k3dUseConfig))
	if err != nil {
		t.Fatalf("parseUseRegistries: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs: got %d want 1", len(refs))
	}
	if refs[0].Name != "k3d-control-plane-registry" {
		t.Errorf("Name = %q want k3d-control-plane-registry (internal :5000 stripped)", refs[0].Name)
	}
	if refs[0].HostPort != 5051 {
		t.Errorf("HostPort = %d want 5051 (from the localhost:5051 mirror key)", refs[0].HostPort)
	}
}

// TestParseUseRegistries_CreateIsNoop confirms a `registries.create` config
// yields no refs — forge leaves cluster-owned registries to k3d.
func TestParseUseRegistries_CreateIsNoop(t *testing.T) {
	refs, err := parseUseRegistries([]byte(k3dCreateConfig))
	if err != nil {
		t.Fatalf("parseUseRegistries: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("refs: got %d want 0 (create is k3d's job, not a standalone ensure)", len(refs))
	}
}

// TestHostPortFromMirrorConfig checks the host port is taken from the
// canonical `localhost:<port>` mirror key (not an arbitrary registry.localhost
// alias), and is 0 when no host:port mirror key is present.
func TestHostPortFromMirrorConfig(t *testing.T) {
	const mirror = `mirrors:
  "registry.localhost:5000":
    endpoint:
      - http://k3d-control-plane-registry:5000
  "localhost:5051":
    endpoint:
      - http://k3d-control-plane-registry:5000
`
	if got := hostPortFromMirrorConfig(mirror, "k3d-control-plane-registry"); got != 5051 {
		t.Errorf("hostPortFromMirrorConfig = %d want 5051 (localhost key)", got)
	}
	if got := hostPortFromMirrorConfig("", ""); got != 0 {
		t.Errorf("hostPortFromMirrorConfig(empty) = %d want 0", got)
	}
	// No localhost key, only a registry.localhost alias — fall back to the
	// max parseable host:port (5000 here).
	const aliasOnly = `mirrors:
  "registry.localhost:5000":
    endpoint:
      - http://k3d-control-plane-registry:5000
`
	if got := hostPortFromMirrorConfig(aliasOnly, "k3d-control-plane-registry"); got != 5000 {
		t.Errorf("hostPortFromMirrorConfig(alias-only) = %d want 5000 (fallback)", got)
	}
}

// TestEnsureStandaloneRegistry_Idempotent asserts a present registry is a
// no-op (no create) and an absent one is created exactly once. Both k3d
// shell-out seams are stubbed so the test never touches k3d.
func TestEnsureStandaloneRegistry_Idempotent(t *testing.T) {
	origExists := registryExistsFn
	origCreate := registryCreateFn
	t.Cleanup(func() {
		registryExistsFn = origExists
		registryCreateFn = origCreate
	})

	// Present -> no create.
	var created []k3dRegistryRef
	registryExistsFn = func(_ context.Context, _ string) (bool, error) { return true, nil }
	registryCreateFn = func(_ context.Context, ref k3dRegistryRef) error {
		created = append(created, ref)
		return nil
	}
	if err := ensureStandaloneRegistry(t.Context(), k3dRegistryRef{Name: "k3d-cp-registry", HostPort: 5051}); err != nil {
		t.Fatalf("ensureStandaloneRegistry(present): %v", err)
	}
	if len(created) != 0 {
		t.Fatalf("present registry triggered %d create(s); want 0", len(created))
	}

	// Absent -> create once, with the parsed name + host port.
	created = nil
	registryExistsFn = func(_ context.Context, _ string) (bool, error) { return false, nil }
	if err := ensureStandaloneRegistry(t.Context(), k3dRegistryRef{Name: "k3d-cp-registry", HostPort: 5051}); err != nil {
		t.Fatalf("ensureStandaloneRegistry(absent): %v", err)
	}
	if len(created) != 1 || created[0].Name != "k3d-cp-registry" || created[0].HostPort != 5051 {
		t.Fatalf("absent registry created %v; want exactly [{k3d-cp-registry 5051}]", created)
	}
}

// TestRunDevClusterUp_EnsuresStandaloneRegistry pins that `forge cluster up`
// ensures a `registries.use` standalone registry BEFORE `k3d cluster create`,
// exactly as the declarative `forge env up` path does.
//
// Without the ensure, k3d fails Cluster Preparation ("Didn't find container
// for node 'k3d-control-plane-registry'") and rolls the create back, so the
// same deploy/k3d.yaml worked under `forge env up` and failed under
// `forge cluster up`. Ordering is asserted, not just the call: a registry
// created after the cluster is too late to matter.
func TestRunDevClusterUp_EnsuresStandaloneRegistry(t *testing.T) {
	origExists := registryExistsFn
	origCreate := registryCreateFn
	origCreateCmd := runK3dClusterCreateCommandFn
	origStateAfter := lookupK3dStateAfterCreateFn
	origCleanup := cleanupK3dCreateToolsFn
	origMergeKubeconfig := mergeK3dKubeconfigFn
	origStateForUp := lookupClusterStateForUpFn
	t.Cleanup(func() {
		registryExistsFn = origExists
		registryCreateFn = origCreate
		runK3dClusterCreateCommandFn = origCreateCmd
		lookupK3dStateAfterCreateFn = origStateAfter
		cleanupK3dCreateToolsFn = origCleanup
		mergeK3dKubeconfigFn = origMergeKubeconfig
		lookupClusterStateForUpFn = origStateForUp
	})

	// runDevClusterUp gates on features.deploy read from the forge.yaml it
	// finds by walking up from cwd. forge's OWN forge.yaml has deploy off
	// (it is a CLI project), so the test needs a deploy-enabled project to
	// stand in for a downstream one.
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/forge.yaml", []byte(
		"name: testproj\nmodule_path: example.com/testproj\n"), 0o644); err != nil {
		t.Fatalf("write temp forge.yaml: %v", err)
	}
	markDeployProject(t, dir) // deploy derives from deploy/kcl existing
	t.Chdir(dir)

	configPath := dir + "/k3d.yaml"
	if err := os.WriteFile(configPath, []byte(k3dUseConfig), 0o644); err != nil {
		t.Fatalf("write temp k3d.yaml: %v", err)
	}

	// One ordered log, so "registry before cluster" is a real assertion
	// rather than two independent call counts.
	var calls []string
	// The cluster does not exist yet — this is the create path. Stubbed so
	// the test does not depend on whatever k3d clusters the host happens to
	// be running.
	lookupClusterStateForUpFn = func(_ context.Context, _ string) (k3dClusterRuntimeState, error) {
		return k3dClusterRuntimeState{}, nil
	}
	registryExistsFn = func(_ context.Context, _ string) (bool, error) { return false, nil }
	registryCreateFn = func(_ context.Context, ref k3dRegistryRef) error {
		calls = append(calls, "registry:"+ref.Name)
		return nil
	}
	runK3dClusterCreateCommandFn = func(_ context.Context, _ []string) error {
		calls = append(calls, "cluster-create")
		return nil
	}
	lookupK3dStateAfterCreateFn = func(_ context.Context, _ string) (k3dClusterRuntimeState, error) {
		return k3dClusterRuntimeState{Exists: true, Running: true}, nil
	}
	cleanupK3dCreateToolsFn = func(_ context.Context, _ string) error { return nil }
	mergeK3dKubeconfigFn = func(_ context.Context, _ string) error { return nil }
	// The real pin runs `kubectl config use-context` against the
	// developer's kubeconfig — shared with every process on the machine —
	// so it is recorded here rather than run. This used to be left to
	// the real kubectl with its error ignored, which switched the host's
	// context to k3d-control-plane on every run of this test.
	origPin := pinKubectlContextFn
	t.Cleanup(func() { pinKubectlContextFn = origPin })
	pinKubectlContextFn = func(_ context.Context, clusterName string) error {
		calls = append(calls, "pin:"+clusterName)
		return nil
	}

	// What follows the pin (mkcert provisioning) is out of scope here.
	_ = runDevClusterUp(t.Context(), configPath, false)

	want := []string{"registry:k3d-control-plane-registry", "cluster-create", "pin:control-plane"}
	if len(calls) < len(want) || calls[0] != want[0] || calls[1] != want[1] || calls[2] != want[2] {
		t.Fatalf("call order was %v; want the standalone registry ensured BEFORE cluster create, "+
			"and the context pinned after it (%v)", calls, want)
	}
}

// TestEnsureConfigRegistries_FromFile drives the full file->ensure path
// against a temp k3d.yaml, asserting the standalone registry is ensured with
// the name + host port parsed from the config.
func TestEnsureConfigRegistries_FromFile(t *testing.T) {
	origExists := registryExistsFn
	origCreate := registryCreateFn
	t.Cleanup(func() {
		registryExistsFn = origExists
		registryCreateFn = origCreate
	})

	path := t.TempDir() + "/k3d.yaml"
	if err := os.WriteFile(path, []byte(k3dUseConfig), 0o644); err != nil {
		t.Fatalf("write temp k3d.yaml: %v", err)
	}

	var created []k3dRegistryRef
	registryExistsFn = func(_ context.Context, _ string) (bool, error) { return false, nil }
	registryCreateFn = func(_ context.Context, ref k3dRegistryRef) error {
		created = append(created, ref)
		return nil
	}
	if err := ensureConfigRegistries(t.Context(), path); err != nil {
		t.Fatalf("ensureConfigRegistries: %v", err)
	}
	if len(created) != 1 || created[0].Name != "k3d-control-plane-registry" || created[0].HostPort != 5051 {
		t.Fatalf("ensured %v; want exactly [{k3d-control-plane-registry 5051}]", created)
	}

	// Empty path is a no-op.
	created = nil
	if err := ensureConfigRegistries(t.Context(), ""); err != nil {
		t.Fatalf("ensureConfigRegistries(empty): %v", err)
	}
	if len(created) != 0 {
		t.Fatalf("empty path ensured %d registr(ies); want 0", len(created))
	}
}

// control-plane's k3d-v2.yaml carries a mirror for the declared registry
// (localhost:5051) AND for the hosted registry's serverlb (localhost:38086).
// The host port must be the declared registry's, every time — map iteration
// order used to pick 38086 in ~12% of runs.
func TestParseUseRegistries_TwoMirrorsDeterministic(t *testing.T) {
	const cfg = `apiVersion: k3d.io/v1alpha5
kind: Simple
registries:
  use:
    - k3d-control-plane-registry:5000
  config: |
    mirrors:
      "registry.localhost:5000":
        endpoint:
          - http://k3d-control-plane-registry:5000
      "registry.localhost:5051":
        endpoint:
          - http://k3d-control-plane-registry:5000
      "localhost:5051":
        endpoint:
          - http://k3d-control-plane-registry:5000
      "localhost:38086":
        endpoint:
          - http://k3d-control-plane-v2-serverlb:38086
`
	for i := 0; i < 1000; i++ {
		refs, err := parseUseRegistries([]byte(cfg))
		if err != nil || len(refs) != 1 {
			t.Fatalf("parse: refs=%v err=%v", refs, err)
		}
		if refs[0].HostPort != 5051 {
			t.Fatalf("iteration %d: HostPort = %d want 5051", i, refs[0].HostPort)
		}
	}
}
