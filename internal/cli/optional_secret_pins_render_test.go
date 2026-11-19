package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/pkg/release"
)

// renderOptionalPinProject renders a real KCL project through the seam
// `forge env render`, `forge env deploy` and the bundle share (kclrender.Run,
// then cluster.ExtractManifests): a store-backed ExternalSecret pinned to
// version `pin`, read by one workload through an OPTIONAL forge.SecretRef and
// by another through a required one, and a third workload reading a Secret
// nothing provides, optionally.
func renderOptionalPinProject(t *testing.T, pin string) map[string][]map[string]any {
	t.Helper()
	dir := t.TempDir()
	kclMod := "[package]\nname = \"optpins\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n"
	main := `import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {
    cluster = "gke_acme_prod"
    namespace = "acme-prod"
    connected_cluster = "acme-prod"
    platform = "amd64"
}

_hosted = forge.HostedStore {org = "55949942-3ee9-4cfc-bb7c-817bdba75186", env_id = "13b10699-40f1-4b19-b273-4b36a9977cc9"}
_on = forge.OnCluster {target = _prod}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    external_secrets_operators = [forge.ExternalSecretsOperator {cluster = _prod}]
    required_secrets = [forge.ExternalSecret {
        name = "vault"
        namespace = "acme-prod"
        keys = ["vault_key"]
        source = forge.StoreSource {
            store = _hosted
            data = {"vault_key" = "VAULT_KEY"}
            pin = {"VAULT_KEY" = ` + pin + `}
        }
    }]
    workloads = [
        fw.Workload {name = "optional-reader", image = "ghcr.io/acme/api", runtime = _on, env = {
            VAULT_KEY = forge.SecretRef {name = "vault", key = "vault_key", optional = True}
        }}
        fw.Workload {name = "required-reader", image = "ghcr.io/acme/api", runtime = _on, env = {
            VAULT_KEY = forge.SecretRef {name = "vault", key = "vault_key"}
        }}
        fw.Workload {name = "absent-reader", image = "ghcr.io/acme/api", runtime = _on, env = {
            BREAK_GLASS = forge.SecretRef {name = "break-glass", key = "token", optional = True}
        }}
    ]
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
	return appliedObjects(t, out)
}

// podTemplatePins returns each Deployment's forge.dev/secret-pins annotation
// by name ("" when it carries none).
func podTemplatePins(t *testing.T, objs map[string][]map[string]any) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, d := range objs["Deployment"] {
		name, _ := d["metadata"].(map[string]any)["name"].(string)
		tmpl, _ := d["spec"].(map[string]any)["template"].(map[string]any)
		md, _ := tmpl["metadata"].(map[string]any)
		ann, _ := md["annotations"].(map[string]any)
		pin, _ := ann[release.AnnotationSecretPins].(string)
		out[name] = pin
	}
	return out
}

// TestRender_OptionalSecretRefIsPinned: the render a deploy applies pins an
// optional reader of a version-pinned store Secret exactly like a required
// one, a pin bump rolls both, and an optional reader of a Secret nothing
// provides carries no pin. The optional reference itself still renders as
// `optional: true`.
func TestRender_OptionalSecretRefIsPinned(t *testing.T) {
	v1 := renderOptionalPinProject(t, "1")
	pins := podTemplatePins(t, v1)
	for _, w := range []string{"optional-reader", "required-reader"} {
		if pins[w] != "vault/VAULT_KEY=1" {
			t.Errorf("%s carries pin %q, want vault/VAULT_KEY=1 (all: %v)", w, pins[w], pins)
		}
	}
	if pins["absent-reader"] != "" {
		t.Errorf("an optional reader of a Secret nothing provides must carry no pin, got %q", pins["absent-reader"])
	}
	for _, d := range v1["Deployment"] {
		if d["metadata"].(map[string]any)["name"] != "optional-reader" {
			continue
		}
		var ref map[string]any
		for _, e := range firstContainer(t, d)["env"].([]any) {
			if ev := e.(map[string]any); ev["name"] == "VAULT_KEY" {
				vf, _ := ev["valueFrom"].(map[string]any)
				ref, _ = vf["secretKeyRef"].(map[string]any)
			}
		}
		if ref["optional"] != true || ref["name"] != "vault" {
			t.Errorf("the optional reference must render as optional: %v", ref)
		}
	}

	v2 := podTemplatePins(t, renderOptionalPinProject(t, "2"))
	if v2["optional-reader"] != "vault/VAULT_KEY=2" || v2["required-reader"] != "vault/VAULT_KEY=2" {
		t.Errorf("a pin bump must roll the optional reader like the required one: %v", v2)
	}
}
