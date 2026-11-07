package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/release"
)

// control_plane_secret_test.go pins forge's Go half of forge.ControlPlaneSecret:
// the render contract it decodes, the shape it records, what it means to the
// deploy preflight, and when it is refused. The KCL half is the fixtures
// kcl/tests/*control_plane_secret*.k.

func prodCPS() ControlPlaneSecretEntity {
	return ControlPlaneSecretEntity{
		Name: "reliant-otel-auth", Namespace: "control-plane-prod", Cluster: "gke_p_prod",
		ConnectedCluster: "prod-control-plane", Kind: "telemetry_token",
		Keys: []string{"OTEL_EXPORTER_OTLP_HEADERS", "token"},
	}
}

// The bucket KCL renders decodes into the entity, key for key.
func TestControlPlaneSecretEntity_DecodesTheRenderContract(t *testing.T) {
	doc := `{"output": {"control_plane_secrets": [{
		"type": "control_plane_secret", "name": "reliant-otel-auth", "namespace": "control-plane-prod",
		"cluster": "gke_p_prod", "connected_cluster": "prod-control-plane", "kind": "telemetry_token",
		"keys": ["OTEL_EXPORTER_OTLP_HEADERS", "token"], "reason": null}]}}`
	e, err := parseKCLEntities([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(e.ControlPlaneSecrets) != 1 {
		t.Fatalf("decoded %d secrets, want 1", len(e.ControlPlaneSecrets))
	}
	got := e.ControlPlaneSecrets[0]
	if got.Name != "reliant-otel-auth" || got.Namespace != "control-plane-prod" || got.Cluster != "gke_p_prod" ||
		got.ConnectedCluster != "prod-control-plane" || got.Kind != "telemetry_token" || len(got.Keys) != 2 {
		t.Errorf("decoded %+v", got)
	}
}

// The destination flag decodes, so `forge env up` and the plan can tell a
// control-plane destination from one whose token the author provides.
func TestObservabilityDest_DecodesTokenFromControlPlane(t *testing.T) {
	doc := `{"output": {"observability": {"enabled": true, "signals": {}, "destination": {
		"endpoint": "https://otlp.reliantapi.com", "protocol": "http/protobuf",
		"token_secret": {"name": "forge-otlp-token", "key": "token"}, "token_from_control_plane": true}}}}`
	e, err := parseKCLEntities([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	d := e.Observability.Destination
	if d == nil || !d.TokenFromControlPlane || d.TokenSecret == nil || d.TokenSecret.Name != "forge-otlp-token" {
		t.Errorf("decoded %+v", d)
	}
}

// It lands in the recorded shape, and in the Secret list a person fills in it
// does NOT: a Secret the control plane writes is not one nobody has set.
func TestShapeControlPlaneSecretsOf(t *testing.T) {
	e := &KCLEntities{ControlPlaneSecrets: []ControlPlaneSecretEntity{prodCPS()}}
	got := shapeControlPlaneSecretsOf(e)
	want := release.ShapeControlPlaneSecret{
		Name: "reliant-otel-auth", Namespace: "control-plane-prod", Cluster: "gke_p_prod",
		ConnectedCluster: "prod-control-plane", Kind: release.ControlPlaneSecretTelemetryToken,
		Keys: []string{"OTEL_EXPORTER_OTLP_HEADERS", "token"},
	}
	if len(got) != 1 || got[0].Name != want.Name || got[0].Namespace != want.Namespace || got[0].Cluster != want.Cluster ||
		got[0].ConnectedCluster != want.ConnectedCluster || got[0].Kind != want.Kind {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if secrets := shapeSecretsOf(e); len(secrets) != 0 {
		t.Errorf("a control-plane secret leaked into the shape's person-provided secrets: %+v", secrets)
	}
	if shapeControlPlaneSecretsOf(&KCLEntities{}) != nil || shapeControlPlaneSecretsOf(nil) != nil {
		t.Error("an env declaring none must project none")
	}
}

// The shape a deploy RECORDS carries the secrets: the same projection `forge
// env build`/`deploy` send on every ensure.
func TestRecordedShapeCarriesControlPlaneSecrets(t *testing.T) {
	e := &KCLEntities{ControlPlaneSecrets: []ControlPlaneSecretEntity{prodCPS()}}
	shape := release.Shape{Kind: release.EnvSelfManaged, ControlPlaneSecrets: shapeControlPlaneSecretsOf(e)}
	if err := shape.Validate(); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(shape.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"control_plane_secrets":[{"name":"reliant-otel-auth"`) {
		t.Errorf("the recorded shape lacks the secret: %s", b)
	}
	// The bundle path builds its shape from the same input, so the bundle a
	// reconciler reads cannot disagree with the declaration.
	doc := envShapeDoc{Shape: shape}
	if in := bundleShapeInputOf(doc); len(in.ControlPlaneSecrets) != 1 {
		t.Errorf("bundleShapeInputOf dropped the secrets: %+v", in)
	}
}

// Preflight projection: delivered, never required.
func TestControlPlaneSecretPreflightProjection(t *testing.T) {
	e := &KCLEntities{ControlPlaneSecrets: []ControlPlaneSecretEntity{prodCPS()}}

	if got := requiredSecretsForPreflight(e); len(got) != 0 {
		t.Errorf("a control-plane secret became a REQUIRED secret, so a first deploy would block on it: %+v", got)
	}
	prov := provisionedByDeployForPreflight(e)
	if len(prov) != 1 || prov[0].Name != "reliant-otel-auth" || prov[0].By != cluster.SupplyControlPlaneSecret {
		t.Errorf("not exempt from the live existence check: %+v", prov)
	}
	supply := secretSupplyForPreflight(e)
	if len(supply) != 1 || supply[0].Kind != cluster.SupplyControlPlaneSecret {
		t.Errorf("not supply for the render-time gate: %+v", supply)
	}
}

// A first deploy: the live target has nothing, the workload mounts the Secret
// non-optionally, and the whole path from entities to the preflight verdict
// lets it through. With the declaration removed the same deploy is refused.
func TestFirstDeployIsNotBlockedByAControlPlaneSecret(t *testing.T) {
	manifests := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  template:
    spec:
      containers:
        - name: api
          image: ghcr.io/acme/api:v1
          env:
            - name: TOKEN
              valueFrom:
                secretKeyRef: {name: reliant-otel-auth, key: token}
`
	run := func(e *KCLEntities) cluster.PreflightResult {
		t.Helper()
		res, err := cluster.PreflightReport(t.Context(), cluster.PreflightOpts{
			Manifests:           manifests,
			Namespace:           "control-plane-prod",
			Context:             "gke_p_prod",
			Secrets:             emptyTarget{},
			SecretSupply:        secretSupplyForPreflight(e),
			ProvisionedByDeploy: provisionedByDeployForPreflight(e),
		})
		if err != nil && res.OK() {
			t.Fatal(err)
		}
		return res
	}
	if res := run(&KCLEntities{ControlPlaneSecrets: []ControlPlaneSecretEntity{prodCPS()}}); !res.OK() {
		t.Errorf("a first deploy was blocked:\n%s", cluster.FormatPreflightReport(res))
	}
	if res := run(&KCLEntities{}); res.OK() {
		t.Error("control: without the declaration the absent Secret must block")
	}
}

type emptyTarget struct{}

func (emptyTarget) GetSecretKeys(_ context.Context, _, _, _ string) (map[string]struct{}, bool, error) {
	return nil, false, nil
}

// REFUSED without a connected-cluster binding, and without a control plane.
func TestRefuseUndeliverableControlPlaneSecrets(t *testing.T) {
	cp := &ControlPlaneEntity{Endpoint: "https://cp.example"}
	bound := prodCPS()
	unbound := prodCPS()
	unbound.ConnectedCluster = ""

	if err := refuseUnboundClusterTargets("prod", &KCLEntities{ControlPlane: cp, ControlPlaneSecrets: []ControlPlaneSecretEntity{bound}}); err != nil {
		t.Errorf("a bound secret was refused: %v", err)
	}

	err := refuseUnboundClusterTargets("prod", &KCLEntities{ControlPlane: cp, ControlPlaneSecrets: []ControlPlaneSecretEntity{unbound}})
	if err == nil {
		t.Fatal("a secret on an unconnected cluster was accepted")
	}
	for _, want := range []string{"reliant-otel-auth", "gke_p_prod", "forge cluster connect", "connected_clusters"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal omits %q: %v", want, err)
		}
	}

	// No k3d exemption: delivery is the control plane dialing the cluster.
	k3d := unbound
	k3d.Cluster = "k3d-demo"
	if err := refuseUnboundClusterTargets("dev", &KCLEntities{ControlPlane: cp, ControlPlaneSecrets: []ControlPlaneSecretEntity{k3d}}); err == nil {
		t.Error("an unconnected k3d cluster was exempted: the control plane could not deliver to it")
	}

	err = refuseUnboundClusterTargets("prod", &KCLEntities{ControlPlaneSecrets: []ControlPlaneSecretEntity{bound}})
	if err == nil || !strings.Contains(err.Error(), "no control plane") {
		t.Errorf("a secret with no control plane to deliver it was accepted: %v", err)
	}
	if err := refuseUnboundClusterTargets("prod", &KCLEntities{}); err != nil {
		t.Errorf("an env that declares none was refused: %v", err)
	}
}

// The refusal fires at the render, not only in the predicate: the call site is
// what every ship path passes through.
func TestRenderRefusesAnUnconnectedControlPlaneSecret(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "entities.json")
	if err := os.WriteFile(fixture, []byte(`{"output": {
		"control_plane": {"endpoint": "https://cp.example"},
		"control_plane_secrets": [{"name": "reliant-otel-auth", "namespace": "control-plane-prod",
			"cluster": "gke_p_prod", "kind": "telemetry_token"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", fixture)
	_, err := RenderKCL(t.Context(), t.TempDir(), "prod")
	if err == nil || !strings.Contains(err.Error(), "forge cluster connect") {
		t.Fatalf("RenderKCL accepted an undeliverable secret: %v", err)
	}
}

// A cluster ONLY a control-plane secret reaches is still bound and recorded, so
// the control plane learns its connected cluster id.
func TestControlPlaneSecretCountsAsAClusterBinding(t *testing.T) {
	got := clusterBindingsOf(&KCLEntities{ControlPlaneSecrets: []ControlPlaneSecretEntity{prodCPS()}})
	if len(got) != 1 || got[0].Cluster != "gke_p_prod" || got[0].ConnectedCluster != "prod-control-plane" {
		t.Errorf("bindings = %+v", got)
	}
}

// The local forwarder cannot read a mounted token, so a destination that
// authenticates with one is refused for it, rather than starting a collector
// that sends no credential and is refused by the backend with nothing saying why.
func TestForwarderConfigRefusesATokenDestination(t *testing.T) {
	o := &ObservabilityEntity{
		Enabled: true,
		Destination: &ObservabilityDestEntity{
			Endpoint: "https://otlp.reliantapi.com", Protocol: "http/protobuf", TokenFromControlPlane: true,
			TokenSecret: &struct {
				Name string `json:"name"`
				Key  string `json:"key"`
			}{Name: "forge-otlp-token", Key: "token"},
		},
	}
	_, err := forwarderConfig(o)
	if err == nil || !strings.Contains(err.Error(), "headers_secret") {
		t.Fatalf("the forwarder accepted a token destination it cannot authenticate: %v", err)
	}
}
