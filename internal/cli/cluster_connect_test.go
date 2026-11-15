package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/deploytarget"
)

func TestParseGKEContext(t *testing.T) {
	for _, tc := range []struct {
		kctx    string
		ok      bool
		cluster string
	}{
		{kctx: "gke_acme-prod_us-central1_prod", ok: true, cluster: "prod"},
		{kctx: "gke_p_us-central1-a_zonal", ok: true, cluster: "zonal"},
		// Not GKE contexts. The five-field case is the one worth pinning: a
		// project id, a location and an RFC-1123 name all forbid '_', so this
		// is something else whose shape must not be guessed at.
		{kctx: "gke_acme_us-central1_prod_extra"},
		{kctx: "gke_acme_us-central1"},
		{kctx: "gke__us-central1_prod"},
		{kctx: "vke-prod"},
		{kctx: "k3d-demo"},
		{kctx: ""},
	} {
		t.Run(tc.kctx, func(t *testing.T) {
			got, ok := parseGKEContext(tc.kctx)
			if ok != tc.ok {
				t.Fatalf("parseGKEContext(%q) ok = %v, want %v", tc.kctx, ok, tc.ok)
			}
			if ok && got.Cluster != tc.cluster {
				t.Errorf("Cluster = %q, want %q", got.Cluster, tc.cluster)
			}
		})
	}
}

// The addresses that fail AGAINST A HEALTHY CLUSTER. The hub dials from a pod,
// where a loopback host is the pod — so these produce "connection refused"
// against a target that is fine, which is why they are refused up front.
func TestRefuseUnusableAddress(t *testing.T) {
	for _, tc := range []struct {
		address string
		wantErr string
	}{
		{address: "https://34.1.2.3"},
		{address: "https://my-cluster.example.com:6443"},

		{address: "http://34.1.2.3", wantErr: "https only"},
		{address: "https://127.0.0.1:6443", wantErr: "loopback or bind address"},
		{address: "https://0.0.0.0:6443", wantErr: "loopback or bind address"},
		{address: "https://[::1]:6443", wantErr: "loopback or bind address"},
		{address: "https://localhost:6443", wantErr: "localhost"},
		{address: "", wantErr: "not a URL"},
	} {
		t.Run(tc.address, func(t *testing.T) {
			err := refuseUnusableAddress(tc.address)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("refuseUnusableAddress(%q) = %v, want nil", tc.address, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("refuseUnusableAddress(%q) = %v, want one containing %q", tc.address, err, tc.wantErr)
			}
		})
	}
}

func gkeCtxFixtures(t *testing.T, server, ca string, d gkeDescription, described bool) {
	t.Helper()
	restoreKC, restoreD := kubeconfigClusterOf, gkeDescribeOf
	t.Cleanup(func() { kubeconfigClusterOf, gkeDescribeOf = restoreKC, restoreD })
	kubeconfigClusterOf = func(string) (string, string, error) { return server, ca, nil }
	gkeDescribeOf = func(gkeContext) (gkeDescription, bool) { return d, described }
}

func TestReadConnectTarget_RefusesAMissingCA(t *testing.T) {
	gkeCtxFixtures(t, "https://34.1.2.3", "", gkeDescription{}, false)
	_, err := readConnectTarget("gke_acme_us-central1_prod", "", "")
	if err == nil || !strings.Contains(err.Error(), "cannot verify") || !strings.Contains(err.Error(), "--ca-file") {
		t.Fatalf("error = %v, want one about not being able to verify the API server, naming --ca-file", err)
	}
}

// A GKE context: the PUBLIC endpoint and GKE's CA, never the private endpoint.
func TestReadConnectTarget_GKEUsesThePublicEndpointAndDescribeCA(t *testing.T) {
	// The kubeconfig holds a DNS endpoint with no CA, as ours does.
	gkeCtxFixtures(t, "https://gke-abc.us-central1.gke.goog", "",
		gkeDescription{Endpoint: "34.1.2.3", CAPEM: "gke-ca"}, true)
	got, err := readConnectTarget("gke_acme_us-central1_prod", "", "")
	if err != nil {
		t.Fatalf("readConnectTarget: %v", err)
	}
	if got.Address != "https://34.1.2.3" || got.CAPEM != "gke-ca" {
		t.Errorf("Address, CAPEM = %q, %q; want the public endpoint and GKE's CA", got.Address, got.CAPEM)
	}
	if !strings.Contains(got.AddressNote, "public endpoint") {
		t.Errorf("AddressNote = %q", got.AddressNote)
	}
}

func TestReadConnectTarget_GKEDescribeWithNoCAIsRefusedWhenTheKubeconfigHasNone(t *testing.T) {
	gkeCtxFixtures(t, "https://gke-abc.us-central1.gke.goog", "", gkeDescription{Endpoint: "34.1.2.3"}, true)
	if _, err := readConnectTarget("gke_acme_us-central1_prod", "", ""); err == nil {
		t.Fatal("want a refusal when neither GKE nor the kubeconfig has a CA")
	}
}

// A GKE DNS endpoint is IAM-gated: a ServiceAccount token cannot pass it.
func TestReadConnectTarget_RefusesAGKEDNSEndpointWithoutAddress(t *testing.T) {
	// describe unavailable: only the DNS endpoint is known.
	gkeCtxFixtures(t, "https://gke-a838.us-central1.gke.goog", "kubeconfig-ca", gkeDescription{}, false)
	_, err := readConnectTarget("gke_acme_us-central1_prod", "", "")
	if err == nil || !strings.Contains(err.Error(), "gke.goog") || !strings.Contains(err.Error(), "--address") {
		t.Fatalf("error = %v, want a refusal of the DNS endpoint naming --address", err)
	}
	// --address is the way out.
	got, err := readConnectTarget("gke_acme_us-central1_prod", "https://34.1.2.3", "")
	if err != nil || got.Address != "https://34.1.2.3" {
		t.Fatalf("with --address: %v %+v", err, got)
	}
	// ... but it cannot name the DNS endpoint itself.
	if _, err := readConnectTarget("gke_acme_us-central1_prod", "https://gke-a838.us-central1.gke.goog", ""); err == nil {
		t.Error("--address naming a *.gke.goog endpoint was accepted")
	}
}

// --address is honoured over the context's server AND over GKE's describe, and
// the private endpoint is never chosen for the operator.
func TestReadConnectTarget_AddressOverrideWins(t *testing.T) {
	gkeCtxFixtures(t, "https://34.42.58.220", "ca", gkeDescription{Endpoint: "34.42.58.220", CAPEM: "gke-ca"}, true)
	got, err := readConnectTarget("gke_acme_us-central1_prod", "https://172.16.0.34", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Address != "https://172.16.0.34" {
		t.Errorf("Address = %q, want the --address override", got.Address)
	}
	if got.CAPEM != "gke-ca" {
		t.Errorf("CAPEM = %q, want GKE's CA (the override changes the address, not the CA)", got.CAPEM)
	}
	// 6443 and a trailing slash.
	got, err = readConnectTarget("gke_acme_us-central1_prod", "https://203.0.113.10:6443/", "")
	if err != nil || got.Address != "https://203.0.113.10:6443" {
		t.Errorf("6443: %v %q", err, got.Address)
	}
	// 8443 is refused.
	if _, err := readConnectTarget("gke_acme_us-central1_prod", "https://203.0.113.10:8443", ""); err == nil ||
		!strings.Contains(err.Error(), "port 8443") {
		t.Errorf("port 8443: err = %v", err)
	}
}

func TestReadConnectTarget_NonGKEUsesTheKubeconfig(t *testing.T) {
	gkeCtxFixtures(t, "https://34.1.2.3:6443", "ca", gkeDescription{Endpoint: "9.9.9.9", CAPEM: "other"}, true)
	got, err := readConnectTarget("vke-prod", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Address != "https://34.1.2.3:6443" || got.CAPEM != "ca" || got.AddressNote != "" {
		t.Errorf("target = %+v, want the kubeconfig's server and CA untouched", got)
	}
}

func TestReadConnectTarget_CAFileOverridesTheCA(t *testing.T) {
	gkeCtxFixtures(t, "https://34.1.2.3", "kubeconfig-ca", gkeDescription{}, false)
	good := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(good, []byte("-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readConnectTarget("vke-prod", "", good)
	if err != nil || !strings.HasPrefix(got.CAPEM, "-----BEGIN CERTIFICATE-----") {
		t.Fatalf("--ca-file: %v %q", err, got.CAPEM)
	}
	// A kubeconfig with NO CA is rescued by --ca-file.
	gkeCtxFixtures(t, "https://34.1.2.3", "", gkeDescription{}, false)
	if _, err := readConnectTarget("vke-prod", "", good); err != nil {
		t.Errorf("--ca-file did not supply the missing CA: %v", err)
	}
	// A file that is not a certificate, or is missing, is refused.
	bad := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(bad, []byte("not a cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readConnectTarget("vke-prod", "", bad); err == nil || !strings.Contains(err.Error(), "no PEM certificate") {
		t.Errorf("bad --ca-file: %v", err)
	}
	if _, err := readConnectTarget("vke-prod", "", filepath.Join(t.TempDir(), "missing.pem")); err == nil {
		t.Error("a missing --ca-file was accepted")
	}
}

// ── The request against a recorded fixture of cp's wire ─────────────────────
//
// The field NAMES are the contract, and protojson's lowerCamelCase is what
// the control plane reads. A typo here is a field the server ignores, so the
// connect appears to succeed and stores nothing — which is why this asserts
// on the exact keys rather than on a round trip.

func TestConnectRequestWire_IsAlwaysTheServiceAccountTokenAuth(t *testing.T) {
	for _, kctx := range []string{"gke_acme_us-central1_prod", "vke-prod"} {
		var out strings.Builder
		calls := recordConnectTo(t, &out, kctx)
		got := calls[procConnectCluster]
		for key, want := range map[string]any{
			"name":    "prod-us",
			"address": "https://34.1.2.3",
			"caPem":   "ca-bundle",
			"token":   fakeMintedToken,
		} {
			if got[key] != want {
				t.Errorf("%s: request[%q] = %v, want %v", kctx, key, got[key], want)
			}
		}
		// No cloud identity ever crosses the wire.
		if _, ok := got["cloudCluster"]; ok {
			t.Errorf("%s: the request carries cloudCluster", kctx)
		}
		// Nor an auth selector: the control plane deleted the enum and
		// reserved the field (CC-C1), so a token is the only credential and
		// there is nothing to choose between.
		if v, ok := got["auth"]; ok {
			t.Errorf("%s: the request carries auth = %v; the control plane reserved that field", kctx, v)
		}
		raw, _ := json.Marshal(got)
		for _, banned := range []string{"cloudCluster", "WORKLOAD_IDENTITY", "gcpServiceAccount", "bindingIdentity"} {
			if strings.Contains(string(raw), banned) {
				t.Errorf("%s: the request JSON contains %q: %s", kctx, banned, raw)
			}
		}
		// THE TOKEN IS NEVER PRINTED — not in the summary, not in a grant,
		// not anywhere. A credential in a terminal is a credential in a
		// scrollback buffer and a CI log.
		if strings.Contains(out.String(), fakeMintedToken) {
			t.Fatalf("%s: the minted token appears in the command's output:\n%s", kctx, out.String())
		}
	}
}

// ── The KCL binding and its refusal ─────────────────────────────────────────

func TestClusterBindingsOf(t *testing.T) {
	e := &KCLEntities{
		ClusterTarget: &ClusterTargetEntity{Cluster: "gke_a_b_prod", ConnectedCluster: "prod-us"},
		Workloads: []WorkloadEntity{
			{Runtime: RuntimeEntity{Type: RuntimeCluster,
				Cluster: &ClusterRuntime{Cluster: "gke_a_b_prod", ConnectedCluster: "prod-us"}}},
			{Runtime: RuntimeEntity{Type: RuntimeCluster,
				Cluster: &ClusterRuntime{Cluster: "vke-eu", ConnectedCluster: "eu-west"}}},
			// A host workload contributes no binding.
			{Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{}}},
		},
	}
	got := clusterBindingsOf(e)
	want := []clusterBinding{
		{Cluster: "gke_a_b_prod", ConnectedCluster: "prod-us"},
		{Cluster: "vke-eu", ConnectedCluster: "eu-west"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d bindings, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("binding %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestRefuseUnboundClusterTargets(t *testing.T) {
	cp := &ControlPlaneEntity{Endpoint: "https://cp.example"}

	// A cloud cluster with no connected name: refused, because the control
	// plane would record the deploy with nowhere to send it.
	err := refuseUnboundClusterTargets("prod", &KCLEntities{
		ControlPlane:  cp,
		ClusterTarget: &ClusterTargetEntity{Cluster: "gke_a_b_prod"},
	})
	if err == nil {
		t.Fatal("a control-plane env targeting an unbound cloud cluster was accepted")
	}
	for _, want := range []string{"forge cluster connect", "connected_cluster", `"gke_a_b_prod"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal omits %q: %v", want, err)
		}
	}

	// Bound: fine.
	if err := refuseUnboundClusterTargets("prod", &KCLEntities{
		ControlPlane:  cp,
		ClusterTarget: &ClusterTargetEntity{Cluster: "gke_a_b_prod", ConnectedCluster: "prod-us"},
	}); err != nil {
		t.Errorf("a bound target was refused: %v", err)
	}

	// A k3d cluster needs no registration — forge applies to it directly,
	// and requiring one would mean connecting a cluster that exists on one
	// laptop.
	if err := refuseUnboundClusterTargets("dev", &KCLEntities{
		ControlPlane:  cp,
		ClusterTarget: &ClusterTargetEntity{Cluster: "k3d-demo"},
	}); err != nil {
		t.Errorf("a k3d target was refused: %v", err)
	}

	// An env with no control plane declares nothing to ship, so there is
	// nothing to bind.
	if err := refuseUnboundClusterTargets("local", &KCLEntities{
		ClusterTarget: &ClusterTargetEntity{Cluster: "gke_a_b_prod"},
	}); err != nil {
		t.Errorf("an env with no control plane was refused: %v", err)
	}
}

// The refusal fires at the RENDER, which is the one place every path that
// could ship the env passes through. A version of this that only tested the
// predicate passed with the call site deleted from RenderKCLWith — exactly the
// regression it is meant to catch.
func TestRenderRefusesAnUnboundCloudTarget(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "entities.json")
	if err := os.WriteFile(fixture, []byte(`{
      "control_plane": {"type": "control_plane", "endpoint": "https://cp.example"},
      "cluster_target": {"cluster": "gke_acme_us-central1_prod", "namespace": "acme-prod"},
      "workloads": []
    }`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", fixture)

	_, err := RenderKCLWith(context.Background(), t.TempDir(), "prod", nil)
	if err == nil {
		t.Fatal("the render accepted a control-plane env whose cloud target names no connected cluster")
	}
	if !strings.Contains(err.Error(), "forge cluster connect") {
		t.Errorf("the refusal does not name the fix: %v", err)
	}
}

// The same render, with the binding declared, renders.
func TestRenderAcceptsABoundCloudTarget(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "entities.json")
	if err := os.WriteFile(fixture, []byte(`{
      "control_plane": {"type": "control_plane", "endpoint": "https://cp.example"},
      "cluster_target": {"cluster": "gke_acme_us-central1_prod", "namespace": "acme-prod",
                         "connected_cluster": "prod-us"},
      "workloads": []
    }`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", fixture)

	got, err := RenderKCLWith(context.Background(), t.TempDir(), "prod", nil)
	if err != nil {
		t.Fatalf("RenderKCLWith: %v", err)
	}
	if got.ClusterTarget.ConnectedCluster != "prod-us" {
		t.Errorf("connected_cluster = %q, want prod-us (the field must survive the render)",
			got.ClusterTarget.ConnectedCluster)
	}
}

func TestResolveClusterBindings_ResolvesNamesToIDs(t *testing.T) {
	c := &fakeConnectCaller{clusters: []wireConnectedCluster{
		{ID: "cl_1", Name: "prod-us"}, {ID: "cl_2", Name: "eu-west"},
	}}
	got, err := resolveClusterBindings(context.Background(), c, &KCLEntities{
		ClusterTarget: &ClusterTargetEntity{Cluster: "gke_a_b_prod", ConnectedCluster: "prod-us"},
	})
	if err != nil {
		t.Fatalf("resolveClusterBindings: %v", err)
	}
	want := []deploytarget.ClusterBinding{{Cluster: "gke_a_b_prod", ClusterID: "cl_1"}}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("bindings = %+v, want %+v", got, want)
	}
}

// A name the org has not connected is an error, never a dropped binding:
// omitting it would record the env with no target and report success.
func TestResolveClusterBindings_RefusesAnUnknownName(t *testing.T) {
	c := &fakeConnectCaller{clusters: []wireConnectedCluster{{ID: "cl_1", Name: "prod-us"}}}
	_, err := resolveClusterBindings(context.Background(), c, &KCLEntities{
		ClusterTarget: &ClusterTargetEntity{Cluster: "gke_a_b_prod", ConnectedCluster: "typo"},
	})
	if err == nil || !strings.Contains(err.Error(), "has not connected") {
		t.Fatalf("error = %v, want one naming the unconnected cluster", err)
	}
}

// The EnsureEnvironment request carries the bindings under the key the control
// plane reads (DeployEnvironmentSpec.clusterBindings, tag 14).
func TestEnsureEnvironmentCarriesClusterBindings(t *testing.T) {
	c := &fakeConnectCaller{}
	_, _, err := deploytarget.EnsureHostedEnvironment(context.Background(), c, deploytarget.HostedEnvRef{
		Name: "prod", Kind: deploytarget.HostedEnvSelfManaged,
		ClusterBindings: []deploytarget.ClusterBinding{{Cluster: "gke_a_b_prod", ClusterID: "cl_1"}},
	})
	if err != nil {
		t.Fatalf("EnsureHostedEnvironment: %v", err)
	}
	spec, _ := c.requests["controlplane.v1.DeployService/EnsureEnvironment"]["spec"].(map[string]any)
	bindings, ok := spec["clusterBindings"].([]map[string]any)
	if !ok || len(bindings) != 1 {
		t.Fatalf("spec.clusterBindings = %#v, want one binding", spec["clusterBindings"])
	}
	if bindings[0]["cluster"] != "gke_a_b_prod" || bindings[0]["clusterId"] != "cl_1" {
		t.Errorf("binding = %#v, want {cluster: gke_a_b_prod, clusterId: cl_1}", bindings[0])
	}
}

// ── Disconnect ──────────────────────────────────────────────────────────────

func TestLookupConnectedCluster(t *testing.T) {
	c := &fakeConnectCaller{clusters: []wireConnectedCluster{
		{ID: "cl_1", Name: "prod-us"}, {ID: "cl_2", Name: "prod-us-2"},
	}}
	// EXACT name, never a prefix: a fuzzy match here is a deletion that hits
	// the wrong cluster.
	got, err := lookupConnectedCluster(context.Background(), c, "prod-us")
	if err != nil {
		t.Fatalf("lookupConnectedCluster: %v", err)
	}
	if got != "cl_1" {
		t.Errorf("id = %q, want cl_1", got)
	}
	if _, err := lookupConnectedCluster(context.Background(), c, "prod"); err == nil {
		t.Error("a partial name resolved; it must not")
	}
}

// ── Helpers ─────────────────────────────────────────────────────────────────

const fakeMintedToken = "eyJhbGciOi-FAKE-TOKEN"

// fakeConnectCaller records every request and answers the two reads the
// connect/disconnect paths make.
type fakeConnectCaller struct {
	clusters []wireConnectedCluster
	requests map[string]map[string]any
	// storeClusterID is the id the store assigns an uploaded issuer; the cluster
	// name when empty.
	storeClusterID string
	// refuseIssuer makes ConnectCluster refuse a request that carries an issuer,
	// the way the control plane does (it checks before it connects anything).
	refuseIssuer error
	// connects counts ConnectCluster calls; issuers records each request's issuer.
	connects int
	issuers  []any
}

func (f *fakeConnectCaller) Call(_ context.Context, procedure string, req, out any) error {
	if f.requests == nil {
		f.requests = map[string]map[string]any{}
	}
	if m, ok := req.(map[string]any); ok {
		f.requests[procedure] = m
	}
	switch procedure {
	case procListClusters:
		if r, ok := out.(*struct {
			Clusters []wireConnectedCluster `json:"clusters"`
		}); ok {
			r.Clusters = f.clusters
		}
	case procConnectCluster:
		f.connects++
		m, _ := req.(map[string]any)
		iss, hasIssuer := m["issuer"]
		f.issuers = append(f.issuers, iss)
		if hasIssuer && f.refuseIssuer != nil {
			return f.refuseIssuer
		}
		if r, ok := out.(*wireConnectClusterResponse); ok {
			name, _ := f.requests[procedure]["name"].(string)
			r.Cluster = wireConnectedCluster{ID: "cl_new", Name: name}
			if reg, ok := iss.(map[string]any); ok && reg["issuer"] != nil {
				id := f.storeClusterID
				if id == "" {
					id = name
				}
				w := &wireClusterIssuer{StoreClusterID: id, Issuer: reg["issuer"].(string), Discover: reg["discover"] == true}
				if j, ok := reg["jwks"].(string); ok {
					var set struct {
						Keys []json.RawMessage `json:"keys"`
					}
					_ = json.Unmarshal([]byte(j), &set)
					w.KeyCount = int32(len(set.Keys))
				}
				r.Cluster.Issuer = w
			}
		}
	default:
		// Every other procedure decodes an empty object, which is what the
		// ensure and remove paths expect.
		b, _ := json.Marshal(map[string]any{"environment": map[string]any{"id": "env_1"}})
		_ = json.Unmarshal(b, out)
	}
	return nil
}

// recordConnectTo drives the real runClusterConnect against a fake control
// plane and a fake cluster, and returns what it sent.
func recordConnectTo(t *testing.T, out *strings.Builder, kctx string) map[string]map[string]any {
	t.Helper()

	restoreKube := kubeconfigClusterOf
	restoreClient := clusterConnectClient
	restoreApply := connectApply
	restoreToken := connectReadToken
	restoreStream := connectEnvStream
	restoreLive, restoreKubectl := connectReadLive, connectKubectl
	t.Cleanup(func() {
		connectEnvStream, connectReadLive, connectKubectl = restoreStream, restoreLive, restoreKubectl
		kubeconfigClusterOf = restoreKube
		clusterConnectClient = restoreClient
		connectApply = restoreApply
		connectReadToken = restoreToken
	})

	kubeconfigClusterOf = func(string) (string, string, error) {
		return "https://34.1.2.3", "ca-bundle", nil
	}
	caller := &fakeConnectCaller{}
	clusterConnectClient = func(context.Context, string, string) (cloudCaller, string, error) {
		return caller, "acme", nil
	}
	connectApply = func(context.Context, string, string) error { return nil }
	connectReadToken = func(context.Context, string, connectRBAC) (string, error) {
		return fakeMintedToken, nil
	}
	stubConnectRender(kctx)
	connectReadLive = func(context.Context, string, connectRBAC) ([]liveObject, error) { return nil, nil }
	connectKubectl = func(context.Context, string, ...string) (string, error) { return "", nil }

	if err := runClusterConnect(context.Background(), clusterConnectOptions{
		Name: "prod-us", KubeContext: kctx, Env: "prod", Out: out,
	}); err != nil {
		t.Fatalf("runClusterConnect: %v", err)
	}
	return caller.requests
}

// stubConnectRender makes the env's render a small app on kctx.
func stubConnectRender(kctx string) {
	connectEnvStream = func(context.Context, io.Writer, string) (connectRender, error) {
		return connectRender{HubUnits: []string{"base"}, Stream: "# cluster: " + kctx + "\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\n  namespace: acme-prod\n---\n" +
			"# cluster: " + kctx + "\napiVersion: v1\nkind: Service\nmetadata:\n  name: api\n  namespace: acme-prod\n"}, nil
	}
}

// A cluster placed only by a forge.Manifests group (no ClusterTarget names it)
// still gets a bundle tree, so it must be bound too.
func TestRefuseUnboundClusterTargets_EveryRenderedCluster(t *testing.T) {
	cp := &ControlPlaneEntity{Endpoint: "https://cp.example"}
	e := &KCLEntities{
		ControlPlane:     cp,
		ClusterTarget:    &ClusterTargetEntity{Cluster: "gke_a_b_prod", ConnectedCluster: "prod-us"},
		ManifestClusters: []ManifestClusterEntity{{Cluster: "gke_a_b_prod"}, {Cluster: "gke_a_b_daemon"}},
	}
	err := refuseUnboundClusterTargets("prod", e)
	if err == nil {
		t.Fatal("an unbound manifests-only cluster was accepted")
	}
	if !strings.Contains(err.Error(), `"gke_a_b_daemon"`) || strings.Contains(err.Error(), `"gke_a_b_prod"`) {
		t.Errorf("refusal must name only the unbound cluster: %v", err)
	}
	e.HelmCharts = []HelmChartEntity{{Cluster: "gke_a_b_helm"}}
	if err := refuseUnboundClusterTargets("prod", e); err == nil || !strings.Contains(err.Error(), `"gke_a_b_helm"`) {
		t.Errorf("helm-placed cluster not named: %v", err)
	}
}

// prod-daemon's shape: a second cluster placed ONLY by several forge.Manifests
// groups (no workload, no ClusterTarget). One cluster-level binding in
// ConnectedClusters must bind every group on it; without it the render is
// refused, and the binding list the control plane receives must carry it.
func TestConnectedClustersBindEveryGroupOnACluster(t *testing.T) {
	cp := &ControlPlaneEntity{Endpoint: "https://cp.example"}
	e := &KCLEntities{
		ControlPlane:  cp,
		ClusterTarget: &ClusterTargetEntity{Cluster: "gke_a_b_prod", ConnectedCluster: "prod-control-plane"},
		ManifestClusters: []ManifestClusterEntity{
			{Cluster: "gke_a_b_prod"}, {Cluster: "gke_a_b_daemon"}, {Cluster: "gke_a_b_daemon"}, {Cluster: "gke_a_b_daemon"},
		},
		HelmCharts: []HelmChartEntity{{Cluster: "gke_a_b_daemon"}},
	}
	if err := refuseUnboundClusterTargets("prod", e); err == nil {
		t.Fatal("precondition: an unbound manifests-only cluster must be refused")
	}

	e.ConnectedClusters = map[string]string{"gke_a_b_daemon": "prod-daemon"}
	if err := refuseUnboundClusterTargets("prod", e); err != nil {
		t.Fatalf("a cluster bound once at cluster level was refused: %v", err)
	}
	got := clusterBindingsOf(e)
	want := []clusterBinding{
		{Cluster: "gke_a_b_daemon", ConnectedCluster: "prod-daemon"},
		{Cluster: "gke_a_b_prod", ConnectedCluster: "prod-control-plane"},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("bindings = %+v, want %+v", got, want)
	}

	// Two declarations that disagree for one context are drift, not a merge.
	e.ConnectedClusters["gke_a_b_prod"] = "someone-else"
	if err := refuseUnboundClusterTargets("prod", e); err == nil || !strings.Contains(err.Error(), "someone-else") {
		t.Fatalf("conflicting bindings accepted: %v", err)
	}
}
