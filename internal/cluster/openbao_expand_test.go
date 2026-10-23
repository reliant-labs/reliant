package cluster

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/pkg/deploy/openbao"
)

// The KCL render and the Go renderer meet at the OpenBao record. This reads the
// record the real KCL render golden for the secrets components contains, decodes
// it STRICTLY (a key Go does not know is an error, exactly as in production),
// and expands it, so a field renamed on one side fails here rather than on a
// customer's deploy.
func TestExpandTierDeclarations_OpenBaoRecordFromTheRenderGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "cli", "testdata", "render_contract", "secrets-"+"components"+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Output struct {
			Manifests []any `json:"manifests"`
		} `json:"output"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var records int
	for _, m := range doc.Output.Manifests {
		if mm, _ := m.(map[string]any); mm["kind"] == openbao.Kind {
			records++
		}
	}
	if records != 1 {
		t.Fatalf("the golden carries %d OpenBao records, want 1", records)
	}

	out, err := expandTierDeclarations(doc.Output.Manifests, nil, nil)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	kinds := map[string]int{}
	for _, it := range out {
		m := it.(map[string]any)
		if m["kind"] == openbao.Kind {
			t.Fatal("the OpenBao record must be replaced by what it renders")
		}
		kinds[m["kind"].(string)]++
		md := m["metadata"].(map[string]any)
		labels, _ := md["labels"].(map[string]any)
		if md["namespace"] == "openbao" && m["kind"] != "Namespace" {
			if labels[ClusterRoutingLabel] != "gke_acme_us-central1_prod" {
				t.Errorf("%v/%v lost its cluster route: %v", m["kind"], md["name"], labels)
			}
			if labels["forge.dev/env"] != "staging" {
				t.Errorf("%v/%v lost its env stamp: %v", m["kind"], md["name"], labels)
			}
		}
	}
	for kind, n := range map[string]int{"StatefulSet": 1, "Job": 2, "Certificate": 2, "Issuer": 2, "NetworkPolicy": 2, "PodDisruptionBudget": 1} {
		if kinds[kind] != n {
			t.Errorf("%s: %d rendered, want %d (all: %v)", kind, kinds[kind], n, kinds)
		}
	}
}

func TestExpandOpenBao_RefusesAnUnknownKeyAndNamesTheRecord(t *testing.T) {
	_, err := expandTierDeclarations([]any{map[string]any{
		"apiVersion": "forge.dev/v1alpha1", "kind": "OpenBao",
		"metadata": map[string]any{"name": "openbao", "namespace": "openbao"},
		"spec":     map[string]any{"name": "openbao", "frobnicate": true},
	}}, nil, nil)
	if err == nil {
		t.Fatal("an unknown spec key must be refused: a dropped field is the drift this seam exists to prevent")
	}
}

// A bootstrap source delivers the seal key and the Postgres DSN through ESO, so
// the Secrets the store's pods mount exist only after the operator has synced
// them. This expands a record carrying a Secret Manager bootstrap through the
// strict decode and asks the render-time supply gate (CheckSecretSupply) whether
// anything in the stream provides those Secrets. The ExternalSecrets are the
// supply: the gate recognises the ESO objects by name.
func TestExpandOpenBao_BootstrapRecordDecodesAndItsSecretsAreSupplied(t *testing.T) {
	record := func(withBootstrap bool) map[string]any {
		spec := map[string]any{
			"name": "openbao", "namespace": "openbao", "image": "quay.io/openbao/openbao:2.6.2", "replicas": 2,
			"owner":          "acme.prod",
			"storage":        map[string]any{"type": "postgres", "postgres": map[string]any{"cloudSQLInstance": "acme:us-central1:pg", "gcpServiceAccount": "pg@acme.iam.gserviceaccount.com", "port": 5432, "database": "openbao", "role": "openbao", "maxParallel": 4}},
			"listeners":      []any{map[string]any{"port": 8200, "tls": true}},
			"tls":            map[string]any{"caConfigMap": "openbao-ca", "duration": "8760h"},
			"seal":           map[string]any{"keyID": "prod-1"},
			"recoverySecret": "openbao-recovery",
			"kv":             map[string]any{"mount": "secret", "maxVersions": 10},
			"ha":             map[string]any{"pdb": true, "zoneSpread": true},
			"audit":          map[string]any{"stdout": true, "file": true, "filePath": "/openbao/audit/audit.log"},
			"writer":         map[string]any{"type": "service_account", "serviceAccount": "forge-secret-writer"},
			"access":         map[string]any{"org": "acme", "env": "prod", "selfCluster": "hub", "clusters": []any{map[string]any{"id": "hub", "audience": "https://openbao.openbao.svc:8200/c/hub"}}},
		}
		if withBootstrap {
			spec["bootstrap"] = map[string]any{
				"project": "acme-prod", "clusterProject": "acme-prod", "clusterLocation": "us-central1", "clusterName": "prod",
				"readerServiceAccount": "openbao-bootstrap-reader", "readerGSA": "openbao-bootstrap-reader@acme-prod.iam.gserviceaccount.com",
				"escrowGSA": "openbao-escrow-writer@acme-prod.iam.gserviceaccount.com", "esoNamespace": "external-secrets",
				"sealSecretID": "openbao-unseal-prod", "passwordSecretID": "openbao-pg-password-prod", "escrowSecretID": "openbao-recovery-prod",
			}
		}
		return map[string]any{
			"apiVersion": "forge.dev/v1alpha1", "kind": openbao.Kind,
			"metadata": map[string]any{"name": "openbao", "namespace": "openbao", "labels": map[string]any{ClusterRoutingLabel: "hub"}},
			"spec":     spec,
		}
	}

	stream := func(withBootstrap bool) string {
		out, err := expandTierDeclarations([]any{record(withBootstrap)}, nil, nil)
		if err != nil {
			t.Fatalf("expand (bootstrap=%v): %v", withBootstrap, err)
		}
		var docs []string
		for _, it := range out {
			b, err := yaml.Marshal(it)
			if err != nil {
				t.Fatal(err)
			}
			docs = append(docs, string(b))
		}
		return strings.Join(docs, "---\n")
	}

	// Without a bootstrap source the pods mount two Secrets nothing in the
	// stream provides: they are the out-of-band prerequisites the render lists.
	var missing []string
	for _, m := range CheckSecretSupply(stream(false), nil) {
		missing = append(missing, m.Secret)
	}
	sort.Strings(missing)
	if want := []string{"openbao-storage", "openbao-unseal"}; !slices.Equal(missing, want) {
		t.Fatalf("without a bootstrap source the gate should flag %v, got %v", want, missing)
	}

	// With one, the ExternalSecrets that deliver them are in the stream.
	s := stream(true)
	for _, m := range CheckSecretSupply(s, nil) {
		t.Errorf("with a bootstrap source %s is still undeclared (mounted by %v)", m.Secret, m.Workloads)
	}
	for _, want := range []string{"kind: SecretStore", "kind: ExternalSecret", "name: openbao-unseal", "name: openbao-storage", "iam.gke.io/gcp-service-account: openbao-escrow-writer@acme-prod.iam.gserviceaccount.com", "clusterName: prod"} {
		if !strings.Contains(s, want) {
			t.Errorf("the expanded stream lacks %q", want)
		}
	}
}
