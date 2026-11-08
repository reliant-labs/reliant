package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/pkg/release"
)

// TestControlPlaneSecret_StoreDeliveryRealKCLToShape drives a store-delivered
// ControlPlaneSecret through the real chain: render, entities, the applied
// stream (a floating ExternalSecret, no required Secret), and the shape — whose
// store binding must read exactly the one name the control plane writes, with no
// pin. The Go constant the control plane compiles against is pinned to the
// string the KCL render writes into the ExternalSecret.
func TestControlPlaneSecret_StoreDeliveryRealKCLToShape(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; runs in task test")
	}
	dir := t.TempDir()
	const kclMod = "[package]\nname = \"cpsstore\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n"
	const main = `import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {
    cluster = "gke_acme_prod"
    namespace = "acme-prod"
    connected_cluster = "acme-prod"
    platform = "amd64"
}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    external_secrets_operators = [forge.ExternalSecretsOperator {cluster = _prod}]
    control_plane_secrets = [forge.ControlPlaneSecret {
        name = "reliant-otel-auth"
        cluster = _prod
        delivery = "store"
        store = forge.HostedStore {org = "org-1", env_id = "env-1"}
    }]
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api"
        ports = [fw.Port {name = "http", port = 8080}]
        runtime = forge.OnCluster {target = _prod}
    }]
})
`
	for f, c := range map[string]string{"kcl.mod": kclMod, "main.k": main} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	kclplugin.Register()
	out, err := kclrender.Run(dir, dir, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	entities, err := parseKCLEntities(out)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(entities.ControlPlaneSecrets) != 1 {
		t.Fatalf("control-plane secrets = %+v", entities.ControlPlaneSecrets)
	}
	c := entities.ControlPlaneSecrets[0]
	if c.Delivery != release.ControlPlaneSecretDeliveryStore || c.Source == nil {
		t.Fatalf("the record lost its store delivery: %+v", c)
	}
	if got := c.Source.Data["OTEL_EXPORTER_OTLP_HEADERS"]; got != release.ControlPlaneSecretStoreName {
		t.Errorf("the render reads store name %q, but release.ControlPlaneSecretStoreName = %q: the control plane writes one and forge reads the other", got, release.ControlPlaneSecretStoreName)
	}
	if len(c.Source.Pin) != 0 {
		t.Errorf("a platform-issued token must float, got pins %v", c.Source.Pin)
	}
	if len(entities.RequiredSecrets) != 0 {
		t.Errorf("a store-delivered Secret became a deploy prerequisite: %+v", entities.RequiredSecrets)
	}

	stream, err := cluster.ExtractManifests(out)
	if err != nil {
		t.Fatalf("ExtractManifests: %v", err)
	}
	if !strings.Contains(stream, "kind: ExternalSecret") || !strings.Contains(stream, "/org-1/env-1/"+release.ControlPlaneSecretStoreName) {
		t.Errorf("the stream should carry the ExternalSecret reading the store name:\n%s", stream)
	}

	proj, err := shapeStoreSecretsOf(entities)
	if err != nil {
		t.Fatalf("shapeStoreSecretsOf: %v", err)
	}
	shape, err := bundle.ProjectShape(bundle.ShapeInput{
		Kind:                release.EnvSelfManaged,
		Manifests:           stream,
		ControlPlaneSecrets: shapeControlPlaneSecretsOf(entities),
		Store:               proj,
	})
	if err != nil {
		t.Fatalf("ProjectShape: %v", err)
	}
	if got := shape.ControlPlaneSecrets[0].Delivery; got != release.ControlPlaneSecretDeliveryStore {
		t.Errorf("shape delivery = %q", got)
	}
	bindings := shape.StoreBindings(release.StoreHosted)
	if len(bindings) != 1 || bindings[0].Cluster != "acme-prod" || bindings[0].Namespace != "acme-prod" || strings.Join(bindings[0].Names, ",") != release.ControlPlaneSecretStoreName {
		t.Errorf("the namespace's read policy must be the one name the control plane writes: %+v", bindings)
	}
	if len(shape.StoreSecrets[0].Pins) != 0 {
		t.Errorf("pins = %+v, want none (floating)", shape.StoreSecrets[0].Pins)
	}
}

func TestShapeStoreSecretsOf_PushControlPlaneSecretProjectsNothing(t *testing.T) {
	e := &KCLEntities{ControlPlaneSecrets: []ControlPlaneSecretEntity{{Name: "reliant-otel-auth", Namespace: "ns", Cluster: "ctx", ConnectedCluster: "c", Kind: "telemetry_token"}}}
	got, err := shapeStoreSecretsOf(e)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.StoreSecrets) != 0 {
		t.Errorf("a pushed Secret reads nothing from the store: %+v", got.StoreSecrets)
	}
}
