package cli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestClusterCreateFlags_NoCIDRsDeclared is the byte-identical guarantee on
// the Go side: a cluster that declares neither CIDR must produce the exact
// flag list it produced before the capability existed (plus the service-account
// issuer flags every forge-created cluster carries).
//
// This is the assertion that would catch a "helpfully" defaulted CIDR — a
// default here would silently move every existing cluster off k3s's own
// allocator, which is a change to a live cluster's network on the next
// recreate.
func TestClusterCreateFlags_NoCIDRsDeclared(t *testing.T) {
	flags := clusterCreateFlags(ClusterEntity{
		Name: "cp", Image: "rancher/k3s:v1.36.3-k3s1", Servers: 1,
	})
	for _, f := range flags {
		if strings.Contains(f, "cidr") {
			t.Fatalf("undeclared CIDRs produced %q in flags %v; an existing cluster must "+
				"create with the flags it created with before", f, flags)
		}
	}
	want := append([]string{"--image", "rancher/k3s:v1.36.3-k3s1", "--servers", "1"}, k3sIssuerFlags()...)
	if strings.Join(flags, " ") != strings.Join(want, " ") {
		t.Fatalf("flags = %v; want %v", flags, want)
	}
}

// TestClusterCreateFlags_CIDRsReachK3sArgs pins that a declared CIDR actually
// arrives at `k3d cluster create` as the k3s server arg that carries it.
//
// The failure this guards is the one that looks like success everywhere else:
// the field parses, validates, projects into the entity, and then never
// reaches k3d — so the cluster is created on the default block and the author
// has no signal at all. That is precisely what `api_port` did for config-file
// clusters before spliceK3dAPIPort.
func TestClusterCreateFlags_CIDRsReachK3sArgs(t *testing.T) {
	flags := clusterCreateFlags(ClusterEntity{
		Name: "cp", Image: "img", Servers: 1,
		ClusterCIDR: "10.52.0.0/16", ServiceCIDR: "10.53.0.0/16",
	})
	joined := strings.Join(flags, " ")
	for _, want := range []string{
		"--k3s-arg --cluster-cidr=10.52.0.0/16@server:*",
		"--k3s-arg --service-cidr=10.53.0.0/16@server:*",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("flags %v do not carry %q — the declared CIDR never reaches k3s", flags, want)
		}
	}
}

// TestClusterCreateFlags_CIDRsIndependentlyOptional: declaring one must not
// synthesize the other. A fabricated service_cidr would be forge inventing a
// network range the author never chose.
func TestClusterCreateFlags_CIDRsIndependentlyOptional(t *testing.T) {
	flags := strings.Join(clusterCreateFlags(ClusterEntity{
		Name: "cp", Image: "img", Servers: 1, ClusterCIDR: "10.52.0.0/16",
	}), " ")
	if !strings.Contains(flags, "--cluster-cidr=10.52.0.0/16") {
		t.Fatalf("flags %q missing the declared cluster-cidr", flags)
	}
	if strings.Contains(flags, "service-cidr") {
		t.Fatalf("flags %q synthesized a service-cidr that was never declared", flags)
	}
}

// TestEnsureDeclaredCluster_CIDRsReachTheCreateCall follows the CIDRs all the
// way to the argv `k3d cluster create` is invoked with, rather than stopping
// at the flag builder. clusterCreateFlags returning the right strings proves
// nothing if ensureDeclaredCluster never calls it on this path.
func TestEnsureDeclaredCluster_CIDRsReachTheCreateCall(t *testing.T) {
	origState := clusterRuntimeStateFn
	origCreate := createDeclaredClusterFn
	origHostDNS := ensureClusterHostGatewayDNSFn
	t.Cleanup(func() {
		clusterRuntimeStateFn = origState
		createDeclaredClusterFn = origCreate
		ensureClusterHostGatewayDNSFn = origHostDNS
	})

	clusterRuntimeStateFn = func(_ context.Context, _ string) (k3dClusterRuntimeState, error) {
		return k3dClusterRuntimeState{}, nil
	}
	var gotArgs []string
	createDeclaredClusterFn = func(_ context.Context, _ string, args []string) error {
		gotArgs = append([]string(nil), args...)
		return nil
	}
	ensureClusterHostGatewayDNSFn = func(_ context.Context, _ string) error { return nil }

	if err := ensureDeclaredCluster(t.Context(), ClusterEntity{
		Name: "cp-daemon", Image: "img", Servers: 1,
		ClusterCIDR: "10.62.0.0/16", ServiceCIDR: "10.63.0.0/16",
	}, nil, "", "dev"); err != nil {
		t.Fatalf("ensureDeclaredCluster: %v", err)
	}

	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{
		"--cluster-cidr=10.62.0.0/16@server:*",
		"--service-cidr=10.63.0.0/16@server:*",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("k3d create argv %v does not carry %q", gotArgs, want)
		}
	}
}

// TestSpliceK3dCIDRs_ConfigClusterGetsK3sArgs covers the OTHER create path.
// `k3d cluster create --config` accepts none of the per-flag settings, so a
// config-file cluster (control-plane's, and every project that outgrows the
// flag form) needs its declared CIDRs projected into options.k3s.extraArgs or
// the declaration is inert.
func TestSpliceK3dCIDRs_ConfigClusterGetsK3sArgs(t *testing.T) {
	const cfg = `apiVersion: k3d.io/v1alpha5
kind: Simple
metadata:
  name: control-plane
options:
  k3s:
    extraArgs:
      - arg: --disable=traefik,metrics-server
        nodeFilters:
          - server:*
`
	out, err := spliceK3dCIDRs([]byte(cfg), "10.52.0.0/16", "10.53.0.0/16")
	if err != nil {
		t.Fatalf("spliceK3dCIDRs: %v", err)
	}
	args := extraArgStrings(t, out)
	// The file's own args survive — this APPENDS, it does not replace. A
	// replacement here would silently re-enable k3s's bundled Traefik and
	// collide with the project's own Gateway controller.
	if !containsArg(args, "--disable=traefik,metrics-server") {
		t.Errorf("splice dropped the config's existing extraArgs: %v", args)
	}
	for _, want := range []string{"--cluster-cidr=10.52.0.0/16", "--service-cidr=10.53.0.0/16"} {
		if !containsArg(args, want) {
			t.Errorf("extraArgs %v missing %q", args, want)
		}
	}
}

// TestSpliceK3dCIDRs_NothingDeclaredIsUntouched pins that the config-file path
// also honours the byte-identical guarantee: with no CIDRs declared the user's
// own bytes come back, so no temp file is written and their comments and
// formatting survive.
func TestSpliceK3dCIDRs_NothingDeclaredIsUntouched(t *testing.T) {
	const cfg = "apiVersion: k3d.io/v1alpha5\nkind: Simple\n# a comment that must survive\n"
	out, err := spliceK3dCIDRs([]byte(cfg), "", "")
	if err != nil {
		t.Fatalf("spliceK3dCIDRs: %v", err)
	}
	if string(out) != cfg {
		t.Fatalf("undeclared CIDRs rewrote the config:\n%s", out)
	}
}

// TestSpliceK3dCIDRs_RefusesDisagreement: when the config file already states
// a CIDR, it is authoritative. forge accepts a declaration that AGREES and
// refuses one that does not, rather than picking a winner between two things
// that both claim to be the source of truth — the same policy spliceK3dAPIPort
// applies to kubeAPI.hostPort.
func TestSpliceK3dCIDRs_RefusesDisagreement(t *testing.T) {
	const cfg = `apiVersion: k3d.io/v1alpha5
kind: Simple
options:
  k3s:
    extraArgs:
      - arg: --cluster-cidr=10.99.0.0/16
        nodeFilters:
          - server:*
`
	_, err := spliceK3dCIDRs([]byte(cfg), "10.52.0.0/16", "")
	if err == nil {
		t.Fatal("expected a refusal when the config and the declaration disagree")
	}
	if !strings.Contains(err.Error(), "10.99.0.0/16") || !strings.Contains(err.Error(), "10.52.0.0/16") {
		t.Errorf("error should name BOTH values so the reader can see the conflict: %v", err)
	}

	// Agreement is not a conflict: the same value declared twice returns the
	// file unchanged rather than duplicating the arg.
	out, err := spliceK3dCIDRs([]byte(cfg), "10.99.0.0/16", "")
	if err != nil {
		t.Fatalf("agreeing values should be accepted: %v", err)
	}
	if string(out) != cfg {
		t.Errorf("an agreeing value rewrote the config:\n%s", out)
	}
}

// TestMergeK3dConfig_ProjectsCIDRsOntoTheConfigItHandsK3d closes the gap that
// spliceK3dCIDRs's own test leaves open: that test proves the splice works,
// not that mergeK3dConfig still CALLS it. Deleting the call left every direct
// splice test green while a config-file cluster silently created on k3s's
// default block — which is exactly the api_port failure this path already had
// once, and the reason the projection exists at all.
//
// So this asserts against the file mergeK3dConfig actually hands to
// `k3d cluster create`, which is the only artifact k3d ever sees.
func TestMergeK3dConfig_ProjectsCIDRsOntoTheConfigItHandsK3d(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "k3d.yaml")
	const cfg = `apiVersion: k3d.io/v1alpha5
kind: Simple
metadata:
  name: control-plane
options:
  k3s:
    extraArgs:
      - arg: --disable=traefik,metrics-server
        nodeFilters:
          - server:*
`
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	got, cleanup, err := mergeK3dConfig(path, k3dConfigOverlay{
		ClusterCIDR: "10.52.0.0/16",
		ServiceCIDR: "10.53.0.0/16",
	})
	if err != nil {
		t.Fatalf("mergeK3dConfig: %v", err)
	}
	defer cleanup()
	if got.path == path {
		t.Fatal("mergeK3dConfig returned the user's own file unchanged; the declared CIDRs " +
			"were dropped and the cluster would be created on k3s's defaults")
	}

	handed, err := os.ReadFile(got.path)
	if err != nil {
		t.Fatalf("read merged config: %v", err)
	}
	args := extraArgStrings(t, handed)
	for _, want := range []string{
		"--cluster-cidr=10.52.0.0/16",
		"--service-cidr=10.53.0.0/16",
		"--disable=traefik,metrics-server", // the file's own args must survive
	} {
		if !containsArg(args, want) {
			t.Errorf("the config handed to k3d has extraArgs %v, missing %q", args, want)
		}
	}
}

// TestMergeK3dConfig_OverlayEmptyPassesThrough pins the pass-through: an
// overlay with nothing in it hands back the user's own path, so no temp file
// is created for a cluster that projects nothing.
func TestMergeK3dConfig_OverlayEmptyPassesThrough(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "k3d.yaml")
	if err := os.WriteFile(path, []byte("apiVersion: k3d.io/v1alpha5\nkind: Simple\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	got, cleanup, err := mergeK3dConfig(path, k3dConfigOverlay{})
	if err != nil {
		t.Fatalf("mergeK3dConfig: %v", err)
	}
	defer cleanup()
	if got.path != path || got.temporary {
		t.Fatalf("empty overlay produced %+v; want the user's own path, untouched", got)
	}
	if !(k3dConfigOverlay{}).empty() {
		t.Fatal("k3dConfigOverlay{}.empty() is false; the zero overlay projects nothing")
	}
}

// extraArgStrings pulls the `arg` strings out of a rendered k3d config's
// options.k3s.extraArgs so assertions read as a list of flags rather than as
// nested map[string]any indexing.
func extraArgStrings(t *testing.T, cfgYAML []byte) []string {
	t.Helper()
	var doc struct {
		Options struct {
			K3s struct {
				ExtraArgs []struct {
					Arg         string   `json:"arg"`
					NodeFilters []string `json:"nodeFilters"`
				} `json:"extraArgs"`
			} `json:"k3s"`
		} `json:"options"`
	}
	if err := yaml.Unmarshal(cfgYAML, &doc); err != nil {
		t.Fatalf("parse spliced config: %v\n%s", err, cfgYAML)
	}
	var out []string
	for _, e := range doc.Options.K3s.ExtraArgs {
		// The node filter is load-bearing: an arg with no filter is not
		// applied to the server nodes, so the CIDR would silently not take.
		if len(e.NodeFilters) == 0 {
			t.Errorf("extraArgs entry %q has no nodeFilters; k3s would not receive it", e.Arg)
		}
		out = append(out, e.Arg)
	}
	return out
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// A forge-created k3d cluster must sign service-account tokens with an issuer
// the control plane accepts: stock k3s signs with a `.local` host, which
// `forge cluster connect` cannot register. k3s keeps the legacy issuer as a
// second accepted one.
func TestClusterCreateFlags_PublicServiceAccountIssuer(t *testing.T) {
	joined := strings.Join(clusterCreateFlags(ClusterEntity{Name: "cp", Image: "img", Servers: 1}), " ")
	for _, want := range []string{
		"--k3s-arg --kube-apiserver-arg=service-account-issuer=https://kubernetes.default.svc@server:*",
		"--k3s-arg --kube-apiserver-arg=service-account-issuer=https://kubernetes.default.svc.cluster.local@server:*",
		"--k3s-arg --kube-apiserver-arg=api-audiences=https://kubernetes.default.svc,https://kubernetes.default.svc.cluster.local,k3s@server:*",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("flags %q do not carry %q", joined, want)
		}
	}
	if strings.Index(joined, "service-account-issuer=https://kubernetes.default.svc@") >
		strings.Index(joined, "service-account-issuer=https://kubernetes.default.svc.cluster.local@") {
		t.Errorf("the public issuer must come first: the API server signs with the first issuer\n%s", joined)
	}
}

func TestMergeK3dConfig_PublicIssuerProjected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k3d.yaml")
	cfg := "apiVersion: k3d.io/v1alpha5\nkind: Simple\noptions:\n  k3s:\n    extraArgs:\n      - arg: --disable=traefik\n        nodeFilters:\n          - server:*\n"
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	got, cleanup, err := mergeK3dConfig(path, k3dConfigOverlay{PublicIssuer: true})
	if err != nil {
		t.Fatalf("mergeK3dConfig: %v", err)
	}
	defer cleanup()
	if got.path == path {
		t.Fatal("PublicIssuer produced the user's own file; the issuer never reaches k3s")
	}
	handed, err := os.ReadFile(got.path)
	if err != nil {
		t.Fatal(err)
	}
	args := extraArgStrings(t, handed)
	for _, want := range append(k3sIssuerArgs(), "--disable=traefik") {
		if !containsArg(args, want) {
			t.Errorf("extraArgs %v missing %q", args, want)
		}
	}
}

func TestMergeK3dConfig_ConfigOwnIssuerWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k3d.yaml")
	cfg := "apiVersion: k3d.io/v1alpha5\nkind: Simple\noptions:\n  k3s:\n    extraArgs:\n      - arg: --kube-apiserver-arg=service-account-issuer=https://issuer.example.com\n        nodeFilters:\n          - server:*\n"
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	got, cleanup, err := mergeK3dConfig(path, k3dConfigOverlay{PublicIssuer: true})
	if err != nil {
		t.Fatalf("mergeK3dConfig: %v", err)
	}
	defer cleanup()
	if got.path != path || got.temporary {
		t.Fatalf("a config that names its own issuer was rewritten: %+v", got)
	}
}

// Every forge path that creates a cluster must carry the issuer, including the
// legacy bootstrap that reads deploy/k3d.yaml directly.
func TestDevClusterCreateArgs_CarryPublicIssuer(t *testing.T) {
	orig := addClusterStorageArgsFn
	t.Cleanup(func() { addClusterStorageArgsFn = orig })
	addClusterStorageArgsFn = func(args []string) ([]string, error) { return args, nil }

	t.Run("fallback flags", func(t *testing.T) {
		t.Chdir(t.TempDir())
		args, cleanup, err := devClusterCreateArgs()
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if !strings.Contains(strings.Join(args, " "), "--k3s-arg --kube-apiserver-arg=service-account-issuer=https://kubernetes.default.svc@server:*") {
			t.Errorf("fallback argv %v lacks the public issuer", args)
		}
	})
	t.Run("deploy/k3d.yaml", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		if err := os.MkdirAll("deploy", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join("deploy", "k3d.yaml"), []byte("apiVersion: k3d.io/v1alpha5\nkind: Simple\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		args, cleanup, err := devClusterCreateArgs()
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		cfgIdx := slices.Index(args, "--config")
		if cfgIdx < 0 {
			t.Fatalf("argv %v has no --config", args)
		}
		b, err := os.ReadFile(args[cfgIdx+1])
		if err != nil {
			t.Fatal(err)
		}
		if !containsArg(extraArgStrings(t, b), k3sIssuerArgs()[0]) {
			t.Errorf("config handed to k3d lacks the public issuer:\n%s", b)
		}
	})
}
