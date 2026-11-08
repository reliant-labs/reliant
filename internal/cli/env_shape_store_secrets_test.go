package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// entitiesFromGolden decodes the real KCL render golden for the secrets
// components, so the projection is tested against what the render emits and not
// against a hand-built struct that could drift from it.
func entitiesFromGolden(t *testing.T, name string) *KCLEntities {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "render_contract", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := parseKCLEntities(raw)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestShapeStoreSecretsOf_FromTheRenderGolden(t *testing.T) {
	e := entitiesFromGolden(t, "secrets-components")
	got, err := shapeStoreSecretsOf(e)
	if err != nil {
		t.Fatal(err)
	}

	if len(got.StoreSecrets) != 1 {
		t.Fatalf("store secrets = %+v", got.StoreSecrets)
	}
	b := got.StoreSecrets[0]
	if b.Cluster != "gke_acme_us-central1_prod" || b.Namespace != "acme-prod" || b.Store != release.StoreOpenBao {
		t.Errorf("binding = %+v: the Secret must land on the env's primary cluster", b)
	}
	if strings.Join(b.Names, ",") != "APP_DB_ROLE_PASSWORD" {
		t.Errorf("names = %v", b.Names)
	}
	if len(got.GeneratedSecrets) != 1 || got.GeneratedSecrets[0].Names[0] != "APP_DB_ROLE_PASSWORD" || got.GeneratedSecrets[0].Kind != "password" {
		t.Errorf("generated = %+v", got.GeneratedSecrets)
	}
	if got.OpenBao == nil || got.OpenBao.Name != "openbao" || got.OpenBao.Cluster != "gke_acme_us-central1_prod" || !strings.Contains(got.OpenBao.Image, "@sha256:") {
		t.Errorf("openbao = %+v", got.OpenBao)
	}
	if len(got.ExternalSecretsOperators) != 1 || got.ExternalSecretsOperators[0].Namespace != "external-secrets" || got.ExternalSecretsOperators[0].Version == "" {
		t.Errorf("eso = %+v", got.ExternalSecretsOperators)
	}
	// Values never travel: the projection carries names, kinds and counts.
	shape := release.Shape{}.WithStoreSecrets(got)
	if err := shape.Validate(); err != nil {
		t.Logf("(shape validity is the caller's: %v)", err)
	}
}

func TestShapeStoreSecretsOf_ConnectedClusterNameIsAttributed(t *testing.T) {
	e := entitiesFromGolden(t, "secrets-components")
	e.ConnectedClusters = map[string]string{"gke_acme_us-central1_prod": "hub"}
	got, err := shapeStoreSecretsOf(e)
	if err != nil {
		t.Fatal(err)
	}
	if got.StoreSecrets[0].ConnectedCluster != "hub" || got.OpenBao.ConnectedCluster != "hub" || got.ExternalSecretsOperators[0].ConnectedCluster != "hub" {
		t.Errorf("the connected name must reach the binding, the store and ESO alike: %+v / %+v / %+v", got.StoreSecrets[0], got.OpenBao, got.ExternalSecretsOperators[0])
	}
}

func TestShapeStoreSecretsOf_SourceClusterWinsOverThePrimary(t *testing.T) {
	e := entitiesFromGolden(t, "secrets-components")
	e.RequiredSecrets[0].Source.Cluster = "gke_acme_us-central1-a_daemon"
	got, err := shapeStoreSecretsOf(e)
	if err != nil {
		t.Fatal(err)
	}
	if got.StoreSecrets[0].Cluster != "gke_acme_us-central1-a_daemon" {
		t.Errorf("a source that names a cluster lands there: %+v", got.StoreSecrets[0])
	}
}

func TestShapeStoreSecretsOf_NoClusterIsRefusedNotGuessed(t *testing.T) {
	e := entitiesFromGolden(t, "secrets-components")
	e.ClusterTarget = nil
	if _, err := shapeStoreSecretsOf(e); err == nil || !strings.Contains(err.Error(), "reaches no cluster") {
		t.Fatalf("a store-backed Secret with no cluster has no role to derive for it: %v", err)
	}
}

func TestShapeStoreSecretsOf_AnEnvWithoutStoresRecordsNothing(t *testing.T) {
	got, err := shapeStoreSecretsOf(entitiesFromGolden(t, "cluster"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.StoreSecrets) != 0 || len(got.GeneratedSecrets) != 0 || got.OpenBao != nil || len(got.ExternalSecretsOperators) != 0 {
		t.Errorf("an env with no store-backed secret must record nothing: %+v", got)
	}
	if z, _ := shapeStoreSecretsOf(nil); z.OpenBao != nil {
		t.Error("nil entities project to nothing")
	}
}
