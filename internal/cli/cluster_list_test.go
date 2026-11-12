package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// fakeClusterWire is a control plane that answers ClusterService/ListClusters
// and GetCluster, and DeployService/ListEnvironments, with canned proto3-JSON —
// so the tests exercise forge's real JSON tags, not shared Go structs.
type fakeClusterWire struct {
	clusters []map[string]any
	envs     []map[string]any
	envsErr  error
	calls    []string
}

func (f *fakeClusterWire) Call(_ context.Context, procedure string, req, out any) error {
	f.calls = append(f.calls, procedure)
	var doc any
	switch procedure {
	case procListClusters:
		doc = map[string]any{"clusters": f.clusters}
	case procGetCluster:
		id, _ := req.(map[string]any)["id"].(string)
		for _, c := range f.clusters {
			if c["id"] == id {
				doc = map[string]any{"cluster": c}
			}
		}
		if doc == nil {
			return fmt.Errorf("not_found: cluster %s", id)
		}
	case "controlplane.v1.DeployService/ListEnvironments":
		if f.envsErr != nil {
			return f.envsErr
		}
		doc = map[string]any{"environments": f.envs}
	default:
		return fmt.Errorf("unexpected procedure %s", procedure)
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (f *fakeClusterWire) called(procedure string) int {
	n := 0
	for _, c := range f.calls {
		if c == procedure {
			n++
		}
	}
	return n
}

// The "token" and "caPem" members are what a buggy or future control plane
// might return. Nothing forge prints may carry them.
const leakedToken = "eyJhbGciOiJSUzI1NiJ9.SECRET-SA-TOKEN"

func fixtureClusters() []map[string]any {
	return []map[string]any{
		{
			"id": "cl_1", "name": "prod-us", "provider": "CLUSTER_PROVIDER_BYO",
			"connection":     "CLUSTER_CONNECTION_CONNECTED",
			"address":        "https://203.0.113.10:6443",
			"caFingerprint":  "sha256:ab12cd34",
			"environmentIds": []string{"env_prod", "env_gone"},
			"createdAt":      "2026-10-01T10:00:00Z",
			"updatedAt":      "2026-10-20T12:30:00Z",
			"issuer": map[string]any{
				"storeClusterId": "prod-us", "issuer": "https://container.googleapis.com/v1/projects/p/locations/l/clusters/c",
				"discover": true, "updatedAt": "2026-10-20T12:30:01Z",
			},
			"token": leakedToken, "caPem": "-----BEGIN CERTIFICATE-----" + leakedToken,
		},
		{
			"id": "cl_2", "name": "k3d-dev", "provider": "CLUSTER_PROVIDER_BYO",
			"connection": "CLUSTER_CONNECTION_WAITING",
			"address":    "https://198.51.100.7:6443",
			"createdAt":  "2026-10-02T08:00:00Z",
			"issuer": map[string]any{
				"storeClusterId": "k3d-dev", "issuer": "https://kubernetes.default.svc", "keyCount": 2,
			},
		},
	}
}

func fixtureEnvs() []map[string]any {
	return []map[string]any{{"id": "env_prod", "name": "prod", "project": "acme"}}
}

func useFakeClusterWire(t *testing.T, f *fakeClusterWire) {
	t.Helper()
	restore := clusterConnectClient
	t.Cleanup(func() { clusterConnectClient = restore })
	clusterConnectClient = func(_ context.Context, env, _ string) (cloudCaller, string, error) {
		if env == "" {
			return nil, "", fmt.Errorf("--env is required")
		}
		return f, "acme", nil
	}
}

func runClusterCmd(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := newClusterCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

func TestClusterList_TextShowsEveryField(t *testing.T) {
	f := &fakeClusterWire{clusters: fixtureClusters(), envs: fixtureEnvs()}
	useFakeClusterWire(t, f)

	out, _, err := runClusterCmd(t, "list", "--env", "prod")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, want := range []string{
		"prod-us", "cl_1", "CONNECTED", "token", "https://203.0.113.10:6443", "sha256:ab12cd34",
		"registered", "discover", "prod (acme)", "env_gone",
		"2026-10-01T10:00:00Z", "2026-10-20T12:30:00Z",
		"k3d-dev", "cl_2", "WAITING", "2 key(s)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "prod-us") > strings.Index(out, "k3d-dev") {
		t.Errorf("clusters must print in the order the control plane returned (sorted by name):\n%s", out)
	}
}

func TestClusterList_NeverPrintsTokenMaterial(t *testing.T) {
	f := &fakeClusterWire{clusters: fixtureClusters(), envs: fixtureEnvs()}
	useFakeClusterWire(t, f)

	for _, args := range [][]string{
		{"list", "--env", "prod"}, {"list", "--env", "prod", "--json"},
		{"show", "prod-us", "--env", "prod"}, {"show", "prod-us", "--env", "prod", "--json"},
	} {
		out, errOut, err := runClusterCmd(t, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if strings.Contains(out+errOut, "SECRET-SA-TOKEN") || strings.Contains(out+errOut, "BEGIN CERTIFICATE") {
			t.Errorf("%v leaked credential material:\n%s%s", args, out, errOut)
		}
	}
}

func TestClusterList_JSONDocumentIsStable(t *testing.T) {
	f := &fakeClusterWire{clusters: fixtureClusters(), envs: fixtureEnvs()}
	useFakeClusterWire(t, f)

	out, _, err := runClusterCmd(t, "list", "--env", "prod", "--json")
	if err != nil {
		t.Fatalf("list --json: %v", err)
	}
	var doc struct {
		Schema   string `json:"schema"`
		Env      string `json:"env"`
		Clusters []struct {
			Name          string  `json:"name"`
			ID            string  `json:"id"`
			State         string  `json:"state"`
			Auth          string  `json:"auth"`
			Address       string  `json:"address"`
			CAFingerprint *string `json:"ca_fingerprint"`
			Issuer        struct {
				Registered     bool   `json:"registered"`
				StoreClusterID string `json:"store_cluster_id"`
				Mode           string `json:"mode"`
				KeyCount       int    `json:"key_count"`
			} `json:"issuer"`
			Environments []struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Project string `json:"project"`
			} `json:"environments"`
			CreatedAt string  `json:"created_at"`
			UpdatedAt *string `json:"updated_at"`
		} `json:"clusters"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not one JSON document: %v\n%s", err, out)
	}
	if doc.Schema != "forge.cluster.list/v1" || doc.Env != "prod" || len(doc.Clusters) != 2 {
		t.Fatalf("head = %q env %q, %d clusters", doc.Schema, doc.Env, len(doc.Clusters))
	}
	a, b := doc.Clusters[0], doc.Clusters[1]
	if a.Name != "prod-us" || a.ID != "cl_1" || a.State != "CONNECTED" || a.Auth != "token" ||
		a.Address != "https://203.0.113.10:6443" || a.CAFingerprint == nil || *a.CAFingerprint != "sha256:ab12cd34" ||
		a.CreatedAt != "2026-10-01T10:00:00Z" || a.UpdatedAt == nil || *a.UpdatedAt != "2026-10-20T12:30:00Z" {
		t.Errorf("cluster 0 = %+v", a)
	}
	if !a.Issuer.Registered || a.Issuer.Mode != "discover" || a.Issuer.StoreClusterID != "prod-us" {
		t.Errorf("issuer 0 = %+v", a.Issuer)
	}
	// Sorted by id, so the document does not depend on the server's order.
	if len(a.Environments) != 2 || a.Environments[0].ID != "env_gone" || a.Environments[0].Name != "" ||
		a.Environments[1].Name != "prod" || a.Environments[1].Project != "acme" {
		t.Errorf("environments 0 = %+v (an id the control plane cannot name is kept, unnamed)", a.Environments)
	}
	if b.State != "WAITING" || b.CAFingerprint != nil || b.UpdatedAt != nil || b.Issuer.Mode != "upload" || b.Issuer.KeyCount != 2 {
		t.Errorf("cluster 1 = %+v (fields the control plane did not send must be null, not empty strings)", b)
	}

	// Absent members are present-and-null, and environments is [] — a stable
	// shape a script can index without checking for existence.
	var raw struct {
		Clusters []map[string]json.RawMessage `json:"clusters"`
	}
	_ = json.Unmarshal([]byte(out), &raw)
	if string(raw.Clusters[1]["ca_fingerprint"]) != "null" || string(raw.Clusters[1]["environments"]) != "[]" {
		t.Errorf("unstable shape: ca_fingerprint=%s environments=%s", raw.Clusters[1]["ca_fingerprint"], raw.Clusters[1]["environments"])
	}
}

func TestClusterList_NoIssuerIsReportedNotRegistered(t *testing.T) {
	c := fixtureClusters()[1]
	delete(c, "issuer")
	f := &fakeClusterWire{clusters: []map[string]any{c}}
	useFakeClusterWire(t, f)

	out, _, err := runClusterCmd(t, "list", "--env", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "not registered") {
		t.Errorf("a cluster with no issuer must say so:\n%s", out)
	}
	if f.called("controlplane.v1.DeployService/ListEnvironments") != 0 {
		t.Error("no cluster has environments, so there is nothing to resolve")
	}
}

func TestClusterList_EmptyOrg(t *testing.T) {
	useFakeClusterWire(t, &fakeClusterWire{})

	out, _, err := runClusterCmd(t, "list", "--env", "prod")
	if err != nil || !strings.Contains(out, "no connected clusters") {
		t.Errorf("text: err=%v out=%q", err, out)
	}
	out, _, err = runClusterCmd(t, "list", "--env", "prod", "--json")
	if err != nil || !strings.Contains(out, `"clusters": []`) {
		t.Errorf("json must carry an empty array, not null: err=%v out=%q", err, out)
	}
}

func TestClusterList_EnvironmentNamesBestEffort(t *testing.T) {
	f := &fakeClusterWire{clusters: fixtureClusters(), envsErr: fmt.Errorf("permission_denied")}
	useFakeClusterWire(t, f)

	out, errOut, err := runClusterCmd(t, "list", "--env", "prod")
	if err != nil {
		t.Fatalf("an unresolvable environment name must not fail the listing: %v", err)
	}
	if !strings.Contains(out, "env_prod") || !strings.Contains(errOut, "environment names") {
		t.Errorf("ids are shown and the gap is said on stderr:\nout=%s\nerr=%s", out, errOut)
	}
}

func TestClusterShow_ByNameResolvesThroughGetCluster(t *testing.T) {
	f := &fakeClusterWire{clusters: fixtureClusters(), envs: fixtureEnvs()}
	useFakeClusterWire(t, f)

	out, _, err := runClusterCmd(t, "show", "prod-us", "--env", "prod")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if f.called(procGetCluster) != 1 {
		t.Errorf("show must read the cluster through GetCluster; calls = %v", f.calls)
	}
	for _, want := range []string{"prod-us", "cl_1", "CONNECTED", "sha256:ab12cd34", "prod (acme)"} {
		if !strings.Contains(out, want) {
			t.Errorf("show output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "k3d-dev") {
		t.Errorf("show prints one cluster:\n%s", out)
	}
}

func TestClusterShow_JSONSchema(t *testing.T) {
	f := &fakeClusterWire{clusters: fixtureClusters(), envs: fixtureEnvs()}
	useFakeClusterWire(t, f)

	out, _, err := runClusterCmd(t, "show", "prod-us", "--env", "prod", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema  string `json:"schema"`
		Cluster struct {
			Name string `json:"name"`
		} `json:"cluster"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc.Schema != "forge.cluster.show/v1" || doc.Cluster.Name != "prod-us" {
		t.Errorf("show --json = %+v err=%v\n%s", doc, err, out)
	}
}

func TestClusterShow_UnknownNameListsWhatExists(t *testing.T) {
	useFakeClusterWire(t, &fakeClusterWire{clusters: fixtureClusters()})

	_, _, err := runClusterCmd(t, "show", "nope", "--env", "prod")
	if err == nil || !strings.Contains(err.Error(), `no cluster named "nope"`) || !strings.Contains(err.Error(), "prod-us") {
		t.Errorf("err = %v", err)
	}
}

func TestClusterListShow_RequireEnv(t *testing.T) {
	useFakeClusterWire(t, &fakeClusterWire{clusters: fixtureClusters()})

	for _, args := range [][]string{{"list"}, {"show", "prod-us"}} {
		if _, _, err := runClusterCmd(t, args...); err == nil || !strings.Contains(err.Error(), "--env is required") {
			t.Errorf("%v: err = %v", args, err)
		}
	}
}
