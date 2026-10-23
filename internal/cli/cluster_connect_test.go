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

// The auto-detect table. `auto` is the flag's default, so this is what the
// overwhelming majority of invocations resolve through.
func TestResolveAuth(t *testing.T) {
	for _, tc := range []struct {
		name, flag, kctx, want string
		wantErr                string
	}{
		{name: "auto picks gcp for a GKE context", flag: "auto", kctx: "gke_acme_us-central1_prod", want: authGCP},
		{name: "empty flag behaves as auto", flag: "", kctx: "gke_acme_us-central1_prod", want: authGCP},
		{name: "auto picks token for VKE", flag: "auto", kctx: "vke-prod", want: authToken},
		// EKS and AKS resolve to token, which is the whole reason no aws or
		// azure value exists: these clusters are targets today.
		{name: "auto picks token for an EKS ARN context", flag: "auto",
			kctx: "arn:aws:eks:us-east-1:123456789012:cluster/prod", want: authToken},
		{name: "auto picks token for an AKS context", flag: "auto", kctx: "aks-prod", want: authToken},
		{name: "auto picks token for k3d", flag: "auto", kctx: "k3d-demo", want: authToken},
		{name: "explicit token always wins", flag: "token", kctx: "gke_acme_us-central1_prod", want: authToken},
		{name: "explicit gcp on a GKE context", flag: "gcp", kctx: "gke_acme_us-central1_prod", want: authGCP},

		{name: "explicit gcp on a non-GKE context is refused", flag: "gcp", kctx: "vke-prod",
			wantErr: "not one"},
		// The two dropped clouds must not be silently accepted: a value that
		// looks supported and is not is the failure this refusal prevents.
		{name: "aws is not a value", flag: "aws", kctx: "whatever", wantErr: "not a known value"},
		{name: "azure is not a value", flag: "azure", kctx: "whatever", wantErr: "not a known value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveAuth(tc.flag, tc.kctx)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("resolveAuth(%q, %q) error = %v, want one containing %q",
						tc.flag, tc.kctx, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveAuth(%q, %q): %v", tc.flag, tc.kctx, err)
			}
			if got != tc.want {
				t.Errorf("resolveAuth(%q, %q) = %q, want %q", tc.flag, tc.kctx, got, tc.want)
			}
		})
	}
}

func TestParseGKEContext(t *testing.T) {
	for _, tc := range []struct {
		kctx     string
		ok       bool
		resource string
	}{
		{kctx: "gke_acme-prod_us-central1_prod", ok: true,
			resource: "projects/acme-prod/locations/us-central1/clusters/prod"},
		{kctx: "gke_p_us-central1-a_zonal", ok: true,
			resource: "projects/p/locations/us-central1-a/clusters/zonal"},
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
			if ok && got.CloudCluster() != tc.resource {
				t.Errorf("CloudCluster() = %q, want %q", got.CloudCluster(), tc.resource)
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

func TestReadConnectTarget_RefusesAMissingCA(t *testing.T) {
	restore := kubeconfigClusterOf
	defer func() { kubeconfigClusterOf = restore }()
	kubeconfigClusterOf = func(string) (string, string, error) {
		return "https://34.1.2.3", "", nil
	}
	_, err := readConnectTarget("gke_acme_us-central1_prod", "auto")
	if err == nil || !strings.Contains(err.Error(), "cannot verify") {
		t.Fatalf("error = %v, want one about not being able to verify the API server", err)
	}
}

func TestReadConnectTarget_GKECarriesTheCloudResource(t *testing.T) {
	restore := kubeconfigClusterOf
	defer func() { kubeconfigClusterOf = restore }()
	kubeconfigClusterOf = func(string) (string, string, error) {
		return "https://34.1.2.3", "-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----", nil
	}
	got, err := readConnectTarget("gke_acme_us-central1_prod", "auto")
	if err != nil {
		t.Fatalf("readConnectTarget: %v", err)
	}
	if got.Auth != authGCP {
		t.Errorf("Auth = %q, want %q", got.Auth, authGCP)
	}
	if want := "projects/acme/locations/us-central1/clusters/prod"; got.CloudCluster != want {
		t.Errorf("CloudCluster = %q, want %q", got.CloudCluster, want)
	}
}

func TestReadConnectTarget_GKEPrivateEndpoint(t *testing.T) {
	restoreKC, restoreD := kubeconfigClusterOf, gkeDescribeOf
	defer func() { kubeconfigClusterOf, gkeDescribeOf = restoreKC, restoreD }()
	kubeconfigClusterOf = func(string) (string, string, error) { return "https://34.42.58.220", "ca", nil }

	cases := []struct {
		name, auth, private, want string
	}{
		{"private present", "auto", "172.16.0.34", "https://172.16.0.34"},
		{"private absent", "auto", "", "https://34.42.58.220"},
		{"token path ignores it", "token", "172.16.0.34", "https://34.42.58.220"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gkeDescribeOf = func(gkeContext) (gkeDescription, bool) {
				return gkeDescription{Endpoint: "34.42.58.220", PrivateEndpoint: tc.private, CAPEM: "gke-ca"}, true
			}
			got, err := readConnectTarget("gke_acme_us-central1_prod", tc.auth)
			if err != nil {
				t.Fatalf("readConnectTarget: %v", err)
			}
			if got.Address != tc.want {
				t.Errorf("Address = %q, want %q", got.Address, tc.want)
			}
		})
	}
}

func TestReadConnectTarget_GKEDescribeSuppliesAddressAndCA(t *testing.T) {
	restoreKC, restoreD := kubeconfigClusterOf, gkeDescribeOf
	defer func() { kubeconfigClusterOf, gkeDescribeOf = restoreKC, restoreD }()
	// DNS-endpoint context: public cert, so no certificate-authority-data.
	kubeconfigClusterOf = func(string) (string, string, error) { return "https://gke-abc.us-central1.gke.goog", "", nil }
	gkeDescribeOf = func(gkeContext) (gkeDescription, bool) {
		return gkeDescription{Endpoint: "34.1.2.3", PrivateEndpoint: "172.16.0.2", CAPEM: "gke-ca"}, true
	}
	got, err := readConnectTarget("gke_acme_us-central1_prod", "gcp")
	if err != nil {
		t.Fatalf("readConnectTarget: %v", err)
	}
	if got.Address != "https://172.16.0.2" || got.CAPEM != "gke-ca" {
		t.Errorf("Address, CAPEM = %q, %q; want the private endpoint and GKE's CA", got.Address, got.CAPEM)
	}

	gkeDescribeOf = func(gkeContext) (gkeDescription, bool) {
		return gkeDescription{Endpoint: "34.1.2.3"}, true
	}
	if _, err := readConnectTarget("gke_acme_us-central1_prod", "gcp"); err == nil {
		t.Fatal("want a refusal when GKE's describe has no CA")
	}
}

// The token auth must NOT carry cloudCluster: the control plane REFUSES it
// there, because a cloud resource name on a credential path means the caller
// misunderstands which credential is in play.
func TestReadConnectTarget_TokenCarriesNoCloudResource(t *testing.T) {
	restore := kubeconfigClusterOf
	defer func() { kubeconfigClusterOf = restore }()
	kubeconfigClusterOf = func(string) (string, string, error) {
		return "https://34.1.2.3", "ca", nil
	}
	got, err := readConnectTarget("gke_acme_us-central1_prod", "token")
	if err != nil {
		t.Fatalf("readConnectTarget: %v", err)
	}
	if got.CloudCluster != "" {
		t.Errorf("CloudCluster = %q, want empty on the token auth", got.CloudCluster)
	}
}

// ── The request against a recorded fixture of cp's wire ─────────────────────
//
// The field NAMES are the contract, and protojson's lowerCamelCase is what
// the control plane reads. A typo here is a field the server ignores, so the
// connect appears to succeed and stores nothing — which is why this asserts
// on the exact keys rather than on a round trip.

func TestConnectRequestWire_GCP(t *testing.T) {
	calls := recordConnect(t, "gke_acme_us-central1_prod", "auto")
	got := calls[procConnectCluster]
	for key, want := range map[string]any{
		"name":         "prod-us",
		"auth":         "CLUSTER_AUTH_WORKLOAD_IDENTITY_GCP",
		"address":      "https://34.1.2.3",
		"caPem":        "ca-bundle",
		"cloudCluster": "projects/acme/locations/us-central1/clusters/prod",
	} {
		if got[key] != want {
			t.Errorf("request[%q] = %v, want %v", key, got[key], want)
		}
	}
	if _, ok := got["token"]; ok {
		t.Error("the gcp request carries a token; the control plane refuses one there")
	}
}

func TestConnectRequestWire_TokenIsWriteOnlyAndNeverPrinted(t *testing.T) {
	var out strings.Builder
	calls := recordConnectTo(t, &out, "vke-prod", "auto")
	got := calls[procConnectCluster]
	if got["auth"] != "CLUSTER_AUTH_SERVICE_ACCOUNT_TOKEN" {
		t.Errorf("auth = %v, want the ServiceAccount token auth", got["auth"])
	}
	if got["token"] != fakeMintedToken {
		t.Errorf("token = %v, want the minted token to be sent", got["token"])
	}
	if _, ok := got["cloudCluster"]; ok {
		t.Error("the token request carries cloudCluster; the control plane refuses it there")
	}
	// THE TOKEN IS NEVER PRINTED — not in the summary, not in a grant, not
	// anywhere. A credential in a terminal is a credential in a scrollback
	// buffer and a CI log.
	if strings.Contains(out.String(), fakeMintedToken) {
		t.Fatalf("the minted token appears in the command's output:\n%s", out.String())
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
	// noBindingIdentity makes ConnectCluster return no per-binding identity.
	noBindingIdentity bool
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
			if !f.noBindingIdentity {
				r.BindingIdentity = wireBindingIdentity{GCPServiceAccount: "fc-prod-ab12@acme.iam.gserviceaccount.com"}
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

func recordConnect(t *testing.T, kctx, auth string) map[string]map[string]any {
	t.Helper()
	var discard strings.Builder
	return recordConnectTo(t, &discard, kctx, auth)
}

// recordConnectTo drives the real runClusterConnect against a fake control
// plane and a fake cluster, and returns what it sent.
func recordConnectTo(t *testing.T, out *strings.Builder, kctx, auth string) map[string]map[string]any {
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
		Name: "prod-us", KubeContext: kctx, Auth: auth, Env: "prod", Out: out,
	}); err != nil {
		t.Fatalf("runClusterConnect: %v", err)
	}
	return caller.requests
}

// stubConnectRender makes the env's render a small app on kctx.
func stubConnectRender(kctx string) {
	connectEnvStream = func(context.Context, io.Writer, string) (string, error) {
		return "# cluster: " + kctx + "\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\n  namespace: acme-prod\n---\n" +
			"# cluster: " + kctx + "\napiVersion: v1\nkind: Service\nmetadata:\n  name: api\n  namespace: acme-prod\n", nil
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
