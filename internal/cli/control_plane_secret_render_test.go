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

// TestControlPlaneSecret_RealKCLToPreflightVerdict drives the whole chain with
// no hand-written JSON between the stages: a real KCL project, rendered through
// the seam `forge env render` / `deploy` use, decoded into entities, applied
// into the stream kubectl reads, projected into the shape the control plane
// records, and put through the deploy preflight against a target that has none
// of the Secrets yet (a first deploy).
//
// Each stage has its own test elsewhere; this one exists because the defect
// class here is a stage dropping what the previous one produced (a renamed JSON
// key decodes to a zero value; a projection that forgets a field records an
// env that delivers nothing), and only the whole chain sees that.
func TestControlPlaneSecret_RealKCLToPreflightVerdict(t *testing.T) {
	dir := t.TempDir()
	const kclMod = "[package]\nname = \"cpsecret\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n"
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
    observability = forge.Observability {
        destination = forge.ObservabilityDestination {
            endpoint = "https://otlp.reliantapi.com"
            token_from_control_plane = True
        }
    }
    control_plane_secrets = [forge.ControlPlaneSecret {name = "reliant-otel-auth", cluster = _prod}]
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

	// 1. entities
	entities, err := parseKCLEntities(out)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := refuseUnboundClusterTargets("prod", entities); err != nil {
		t.Fatalf("a connected env was refused: %v", err)
	}
	got := map[string]ControlPlaneSecretEntity{}
	for _, c := range entities.ControlPlaneSecrets {
		got[c.Name] = c
	}
	if len(got) != 2 {
		t.Fatalf("want the declared secret and the node agent's derived one, got %+v", entities.ControlPlaneSecrets)
	}
	for _, name := range []string{"reliant-otel-auth", "forge-otlp-token"} {
		c, ok := got[name]
		if !ok || c.ConnectedCluster != "acme-prod" || c.Cluster != "gke_acme_prod" || c.Kind != "telemetry_token" {
			t.Errorf("%s decoded as %+v", name, c)
		}
	}
	if ns := got["reliant-otel-auth"].Namespace; ns != "acme-prod" {
		t.Errorf("declared secret namespace = %q, want the cluster's", ns)
	}
	if ns := got["forge-otlp-token"].Namespace; ns != "forge-observability" {
		t.Errorf("derived secret namespace = %q, want the agent's", ns)
	}
	if d := entities.Observability.Destination; d == nil || !d.TokenFromControlPlane {
		t.Errorf("the destination lost token_from_control_plane: %+v", d)
	}

	// 2. the stream kubectl reads: the workload's optional volume + env, and
	// the agent's token mount, both optional.
	stream, err := cluster.ExtractManifests(out)
	if err != nil {
		t.Fatalf("ExtractManifests: %v", err)
	}
	for _, want := range []string{
		"secretName: reliant-otel-auth",
		"secretName: forge-otlp-token",
		"optional: true",
		"OTEL_EXPORTER_OTLP_HEADERS_FILE",
		"/var/run/secrets/forge/otlp/headers",
	} {
		if !strings.Contains(stream, want) {
			t.Errorf("the applied stream lacks %q", want)
		}
	}

	// 3. the shape: both secrets, each with the connected cluster, and no value.
	shape, err := bundle.ProjectShape(bundle.ShapeInput{
		Kind:                release.EnvSelfManaged,
		Manifests:           stream,
		ControlPlaneSecrets: shapeControlPlaneSecretsOf(entities),
	})
	if err != nil {
		t.Fatalf("ProjectShape: %v", err)
	}
	if len(shape.ControlPlaneSecrets) != 2 {
		t.Fatalf("the shape records %d control-plane secrets, want 2: %+v", len(shape.ControlPlaneSecrets), shape.ControlPlaneSecrets)
	}

	// 4. the preflight verdict on a first deploy: nothing exists on the target.
	res, perr := cluster.PreflightReport(t.Context(), cluster.PreflightOpts{
		Manifests:           stream,
		Namespace:           "acme-prod",
		Context:             "gke_acme_prod",
		Secrets:             emptyTarget{},
		SecretSupply:        secretSupplyForPreflight(entities),
		ProvisionedByDeploy: provisionedByDeployForPreflight(entities),
	})
	for key := range res.MissingSecretKeys {
		if strings.HasSuffix(key, "/reliant-otel-auth") || strings.HasSuffix(key, "/forge-otlp-token") {
			t.Errorf("a first deploy was blocked on a Secret the control plane delivers: %s", key)
		}
	}
	if len(res.UndeclaredSecretMounts) != 0 {
		t.Errorf("a delivered Secret reported as an undeclared mount: %+v", res.UndeclaredSecretMounts)
	}
	_ = perr // images are unreachable here; the Secret verdict above is what this asserts.
}
