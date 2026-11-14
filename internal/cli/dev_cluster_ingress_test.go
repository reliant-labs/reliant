package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestSpliceK3dPorts_AppendsToExisting covers the canonical case —
// scaffolded deploy/k3d.yaml has its own ports[] block and the
// generated fragment carries one or more listener mappings. Output
// must contain BOTH the user entries and the fragment entries.
func TestSpliceK3dPorts_AppendsToExisting(t *testing.T) {
	user := []byte(`apiVersion: k3d.io/v1alpha5
kind: Simple
metadata:
  name: example
ports:
  - port: 18080:80
    nodeFilters:
      - loadbalancer
`)
	fragment := []byte(`ports:
  - port: 19190:19190
    nodeFilters:
      - loadbalancer
`)
	out, err := spliceK3dPorts(user, fragment)
	if err != nil {
		t.Fatalf("splice: %v", err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("parse merged: %v\n%s", err, out)
	}
	ports, ok := parsed["ports"].([]any)
	if !ok {
		t.Fatalf("merged ports[] not a list: %T\n%s", parsed["ports"], out)
	}
	if len(ports) != 2 {
		t.Errorf("merged ports[] count = %d, want 2\n%s", len(ports), out)
	}
	// Metadata + apiVersion pass through.
	if !strings.Contains(string(out), "name: example") {
		t.Errorf("merged YAML lost metadata.name:\n%s", out)
	}
}

// TestSpliceK3dPorts_NoUserPortsBlock covers the case where the user
// removed the ports[] block from deploy/k3d.yaml (relying entirely
// on the generated fragment). Output must contain just the fragment
// entries.
func TestSpliceK3dPorts_NoUserPortsBlock(t *testing.T) {
	user := []byte(`apiVersion: k3d.io/v1alpha5
kind: Simple
metadata:
  name: noports
`)
	fragment := []byte(`ports:
  - port: 18080:18080
    nodeFilters:
      - loadbalancer
  - port: 19190:19190
    nodeFilters:
      - loadbalancer
`)
	out, err := spliceK3dPorts(user, fragment)
	if err != nil {
		t.Fatalf("splice: %v", err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("parse merged: %v\n%s", err, out)
	}
	ports := parsed["ports"].([]any)
	if len(ports) != 2 {
		t.Errorf("merged ports[] count = %d, want 2\n%s", len(ports), out)
	}
}

// TestSpliceK3dPorts_EmptyFragmentNoOp covers the case where the
// fragment has no ports entries (e.g. the dev env has no gateways).
// Output should be the user YAML verbatim — preserve user comments
// and formatting by skipping the marshal round-trip.
func TestSpliceK3dPorts_EmptyFragmentNoOp(t *testing.T) {
	user := []byte(`# user comment preserved
apiVersion: k3d.io/v1alpha5
kind: Simple
metadata:
  name: empty-frag
ports:
  - port: 18080:80
    nodeFilters: [loadbalancer]
`)
	fragment := []byte(`# fragment with no ports
`)
	out, err := spliceK3dPorts(user, fragment)
	if err != nil {
		t.Fatalf("splice: %v", err)
	}
	if string(out) != string(user) {
		t.Errorf("expected pass-through; got divergent output\n--- got ---\n%s\n--- want ---\n%s", out, user)
	}
}

// TestSpliceK3dPorts_FragmentWinsOnHostCollision pins the dedupe
// policy: when the scaffolded YAML and the fragment both claim host
// port 18080 with different cluster-side targets, the fragment wins.
// This is the canonical bug — scaffolded `18080:80` (pre-Gateway-API
// artifact) collided with fragment `18080:18080` (from the new KCL
// listener), and k3d refused the config.
func TestSpliceK3dPorts_FragmentWinsOnHostCollision(t *testing.T) {
	user := []byte(`apiVersion: k3d.io/v1alpha5
kind: Simple
metadata:
  name: collision
ports:
  - port: 18080:80
    nodeFilters:
      - loadbalancer
`)
	fragment := []byte(`ports:
  - port: 18080:18080
    nodeFilters:
      - loadbalancer
`)
	out, err := spliceK3dPorts(user, fragment)
	if err != nil {
		t.Fatalf("splice: %v", err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("parse merged: %v\n%s", err, out)
	}
	ports, ok := parsed["ports"].([]any)
	if !ok {
		t.Fatalf("merged ports[] not a list: %T\n%s", parsed["ports"], out)
	}
	if len(ports) != 1 {
		t.Fatalf("merged ports[] count = %d, want 1 (dedupe should drop scaffold entry)\n%s", len(ports), out)
	}
	entry, ok := ports[0].(map[string]any)
	if !ok {
		t.Fatalf("ports[0] not a map: %T", ports[0])
	}
	if got, want := entry["port"], "18080:18080"; got != want {
		t.Errorf("ports[0].port = %q, want %q (fragment must win)\n%s", got, want, out)
	}
}

// TestSpliceK3dPorts_DisjointEntriesAllSurvive covers the no-collision
// case: scaffolded YAML declares `18080:80` and `18443:443`, fragment
// declares a disjoint `19190:19190`. The merged output should preserve
// all three entries (nothing to dedupe).
func TestSpliceK3dPorts_DisjointEntriesAllSurvive(t *testing.T) {
	user := []byte(`apiVersion: k3d.io/v1alpha5
kind: Simple
metadata:
  name: disjoint
ports:
  - port: 18080:80
    nodeFilters:
      - loadbalancer
  - port: 18443:443
    nodeFilters:
      - loadbalancer
`)
	fragment := []byte(`ports:
  - port: 19190:19190
    nodeFilters:
      - loadbalancer
`)
	out, err := spliceK3dPorts(user, fragment)
	if err != nil {
		t.Fatalf("splice: %v", err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("parse merged: %v\n%s", err, out)
	}
	ports := parsed["ports"].([]any)
	if len(ports) != 3 {
		t.Errorf("merged ports[] count = %d, want 3 (disjoint hosts)\n%s", len(ports), out)
	}
	// Spot-check that all three host ports are present.
	got := map[string]bool{}
	for _, p := range ports {
		if m, ok := p.(map[string]any); ok {
			if s, ok := m["port"].(string); ok {
				got[s] = true
			}
		}
	}
	for _, want := range []string{"18080:80", "18443:443", "19190:19190"} {
		if !got[want] {
			t.Errorf("merged ports[] missing %q; got = %v\n%s", want, got, out)
		}
	}
}

// TestSpliceK3dPorts_BothEmpty covers the degenerate case — empty
// user YAML and empty fragment. Splice must succeed and return the
// user YAML verbatim (empty-fragment fast path).
func TestSpliceK3dPorts_BothEmpty(t *testing.T) {
	out, err := spliceK3dPorts(nil, nil)
	if err != nil {
		t.Fatalf("splice: %v", err)
	}
	// nil userYAML in -> empty bytes out (the empty-fragment fast path
	// returns userYAML verbatim, which is nil).
	if len(out) != 0 {
		t.Errorf("expected empty output for empty inputs; got %q", out)
	}
}

// TestIngressPinnedVersions parses the embedded VERSION file and
// asserts both keys (envoy_gateway, gateway_api) are present + look
// like vX.Y.Z tags.
func TestIngressPinnedVersions(t *testing.T) {
	envoyVer, gatewayAPIVer, err := ingressPinnedVersions()
	if err != nil {
		t.Fatalf("read versions: %v", err)
	}
	for _, ver := range []string{envoyVer, gatewayAPIVer} {
		if !strings.HasPrefix(ver, "v") || strings.Count(ver, ".") < 2 {
			t.Errorf("version %q doesn't look like a vX.Y.Z tag", ver)
		}
	}
}

// TestGatewayAPICRDsURL pins the URL shape — the release download
// URL is contract: if upstream relocates the file, forge breaks at
// cluster up. Catching the URL drift in tests gives us a flag
// instead of a runtime 404.
func TestGatewayAPICRDsURL(t *testing.T) {
	got := gatewayAPICRDsURL("v1.5.1")
	want := "https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.5.1/standard-install.yaml"
	if got != want {
		t.Errorf("URL = %q, want %q", got, want)
	}
}

// TestClusterPortDrift_FlagsMissingControllerPort is the drift scenario:
// the env's RENDERED Gateway listeners require the controller listener port
// (28090) but the live cluster only published http/grpc (28080/29190) at
// create time. The drift check must surface 28090 as missing so the caller
// can tell the user to recreate — never leave the controller route silently
// unreachable.
func TestClusterPortDrift_FlagsMissingControllerPort(t *testing.T) {
	orig := runningClusterHostPortsFn
	t.Cleanup(func() { runningClusterHostPortsFn = orig })
	// Live cluster predates the controller listener — only http/grpc mapped.
	runningClusterHostPortsFn = func(_ context.Context, _ string) (map[int]bool, error) {
		return map[int]bool{28080: true, 29190: true}, nil
	}
	// The env's rendered listeners require the controller port too.
	required := map[int]bool{28080: true, 28090: true, 29190: true}

	missing, err := clusterPortDrift(context.Background(), "control-plane", required)
	if err != nil {
		t.Fatalf("drift: %v", err)
	}
	if len(missing) != 1 || missing[0] != 28090 {
		t.Fatalf("expected missing=[28090], got %v", missing)
	}
}

// TestClusterPortDrift_NoDriftWhenComplete: a live cluster that maps every
// required listener port reports no drift (extra live ports are fine).
func TestClusterPortDrift_NoDriftWhenComplete(t *testing.T) {
	orig := runningClusterHostPortsFn
	t.Cleanup(func() { runningClusterHostPortsFn = orig })
	runningClusterHostPortsFn = func(_ context.Context, _ string) (map[int]bool, error) {
		return map[int]bool{28080: true, 28090: true, 99999: true}, nil // extra live ports are fine
	}
	required := map[int]bool{28080: true, 28090: true}
	missing, err := clusterPortDrift(context.Background(), "control-plane", required)
	if err != nil {
		t.Fatalf("drift: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("expected no drift, got %v", missing)
	}
}

// TestClusterPortDrift_OnlyRenderedListenersFlagged proves the drift check
// reasons about the RENDERED listeners only — NOT statically pre-mapped
// parallel-worktree blocks. A live cluster missing such a future-block port
// (28190) but mapping every CURRENT listener reports NO drift, so a routine
// `forge env up` doesn't force a recreate for ports no current route needs.
func TestClusterPortDrift_OnlyRenderedListenersFlagged(t *testing.T) {
	orig := runningClusterHostPortsFn
	t.Cleanup(func() { runningClusterHostPortsFn = orig })
	runningClusterHostPortsFn = func(_ context.Context, _ string) (map[int]bool, error) {
		// Live cluster maps the current listeners but not the unused
		// worktree-block 1 ports (28180/28190/29290).
		return map[int]bool{28080: true, 28090: true, 29190: true}, nil
	}
	required := map[int]bool{28080: true, 28090: true, 29190: true} // rendered listeners only
	missing, err := clusterPortDrift(context.Background(), "control-plane", required)
	if err != nil {
		t.Fatalf("drift: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("expected no drift (future-block ports are not required), got %v", missing)
	}
}

// TestClusterPortDrift_UnreadableServerlbIsNoOp: an empty running set
// (serverlb unreadable) must NOT flag every port as missing.
func TestClusterPortDrift_UnreadableServerlbIsNoOp(t *testing.T) {
	orig := runningClusterHostPortsFn
	t.Cleanup(func() { runningClusterHostPortsFn = orig })
	runningClusterHostPortsFn = func(_ context.Context, _ string) (map[int]bool, error) {
		return map[int]bool{}, nil
	}
	required := map[int]bool{28090: true}
	missing, err := clusterPortDrift(context.Background(), "control-plane", required)
	if err != nil {
		t.Fatalf("drift: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("expected no-op on unreadable serverlb, got %v", missing)
	}
}

// TestSpliceK3dAPIPortProjectsDeclaredPort pins that a declared api_port
// reaches a config-file cluster. `k3d cluster create --config` ignores the
// per-flag settings, so without this projection the field parsed, validated
// and then did nothing while k3d picked a random host port.
func TestSpliceK3dAPIPortProjectsDeclaredPort(t *testing.T) {
	const cfg = "apiVersion: k3d.io/v1alpha5\nkind: Simple\nmetadata:\n  name: control-plane\nservers: 1\n"
	out, err := spliceK3dAPIPort([]byte(cfg), 6443)
	if err != nil {
		t.Fatalf("spliceK3dAPIPort: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	kubeAPI, ok := doc["kubeAPI"].(map[string]any)
	if !ok {
		t.Fatalf("no kubeAPI block in:\n%s", out)
	}
	if got := fmt.Sprintf("%v", kubeAPI["hostPort"]); got != "6443" {
		t.Fatalf("hostPort = %q; want \"6443\"", got)
	}
	// The rest of the config must survive the round-trip.
	if doc["servers"] != 1 {
		t.Fatalf("servers = %v; want 1", doc["servers"])
	}
	if md, _ := doc["metadata"].(map[string]any); md == nil || md["name"] != "control-plane" {
		t.Fatalf("metadata lost: %v", doc["metadata"])
	}
}

// TestSpliceK3dAPIPortLeavesConfigAloneWhenUndeclared keeps today's behavior
// for the common case: no api_port declared, k3d keeps choosing.
func TestSpliceK3dAPIPortLeavesConfigAloneWhenUndeclared(t *testing.T) {
	const cfg = "kind: Simple\nservers: 1\n"
	out, err := spliceK3dAPIPort([]byte(cfg), 0)
	if err != nil {
		t.Fatalf("spliceK3dAPIPort: %v", err)
	}
	if string(out) != cfg {
		t.Fatalf("config was rewritten when nothing was declared:\n%s", out)
	}
}

// TestSpliceK3dAPIPortAcceptsAgreement lets the config file keep its own
// formatting when it already says the same thing.
func TestSpliceK3dAPIPortAcceptsAgreement(t *testing.T) {
	const cfg = "kind: Simple\nkubeAPI:\n  hostPort: \"6443\"\n"
	out, err := spliceK3dAPIPort([]byte(cfg), 6443)
	if err != nil {
		t.Fatalf("spliceK3dAPIPort: %v", err)
	}
	if string(out) != cfg {
		t.Fatalf("agreeing config was rewritten:\n%s", out)
	}
}

// TestSpliceK3dAPIPortRefusesConflict is the reason this is a projection and
// not an override: two sources of truth that disagree is an authoring bug, and
// forge must not silently pick a winner.
func TestSpliceK3dAPIPortRefusesConflict(t *testing.T) {
	const cfg = "kind: Simple\nkubeAPI:\n  hostPort: \"7443\"\n"
	_, err := spliceK3dAPIPort([]byte(cfg), 6443)
	if err == nil {
		t.Fatal("conflicting api_port and kubeAPI.hostPort were accepted")
	}
	if !strings.Contains(err.Error(), "7443") || !strings.Contains(err.Error(), "6443") {
		t.Fatalf("error names neither value: %v", err)
	}
}
