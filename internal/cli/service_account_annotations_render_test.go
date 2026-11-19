package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
)

// TestServiceAccountAnnotations_ReachTheAppliedStream renders a real KCL
// project through the same seam `forge env render` / `forge env deploy` use
// (kclrender.Run, then cluster.ExtractManifests — the stream kubectl reads)
// and asserts the workload-identity annotation declared on
// fw.Workload.serviceAccountAnnotations arrives on the generated
// ServiceAccount, on that object only.
//
// The KCL fixture (kcl/tests/positive_workload_service_account_annotations.k)
// pins the authoring side; this pins that nothing between it and kubectl —
// the Workload record, ExtractManifests' RenderWorkloads expansion, the
// env-label stamp — drops or relocates the annotation. GKE
// Workload Identity fails silently when it is missing: the pod starts, and
// every GCP call is made as the node's identity instead.
func TestServiceAccountAnnotations_ReachTheAppliedStream(t *testing.T) {
	const gsaKey = "iam.gke.io/gcp-service-account"
	const gsa = "deploy-publisher@reliant-labs-475814.iam.gserviceaccount.com"

	dir := t.TempDir()
	kclMod := "[package]\nname = \"saannotations\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n"
	main := `import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {
    cluster = "gke_reliant-labs-475814_us-central1_prod"
    namespace = "control-plane-prod"
    platform = "amd64"
}

_bundle = forge.Bundle {
    project = "control-plane"
    env = "prod"
    cluster_target = _prod
    workloads = [_w | {runtime = forge.OnCluster {target = _prod}} if not _w.runtime else _w for _w in [
        fw.Workload {
            name = "admin-server"
            image = "us-docker.pkg.dev/reliant-labs-475814/reliant-prod/control-plane"
            ports = [fw.Port {name = "http", port = 8090}]
            serviceAccountAnnotations = {"` + gsaKey + `" = "` + gsa + `"}
        }
        fw.Workload {
            name = "plain"
            image = "us-docker.pkg.dev/reliant-labs-475814/reliant-prod/control-plane"
            ports = [fw.Port {name = "http", port = 8080}]
        }
    ]]
}

output = forge.render(_bundle)
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
	objs := appliedObjects(t, out)

	annotationsOf := func(obj map[string]any) map[string]any {
		meta, _ := obj["metadata"].(map[string]any)
		ann, _ := meta["annotations"].(map[string]any)
		return ann
	}
	nameOf := func(obj map[string]any) string {
		meta, _ := obj["metadata"].(map[string]any)
		n, _ := meta["name"].(string)
		return n
	}

	var sawAnnotated, sawPlain bool
	for _, sa := range objs["ServiceAccount"] {
		switch nameOf(sa) {
		case "admin-server":
			sawAnnotated = true
			if got := annotationsOf(sa)[gsaKey]; got != gsa {
				t.Errorf("admin-server ServiceAccount %s = %v, want %q", gsaKey, got, gsa)
			}
		case "plain":
			sawPlain = true
			if ann := annotationsOf(sa); ann != nil {
				t.Errorf("plain ServiceAccount declared no annotations but carries %v", ann)
			}
		}
	}
	if !sawAnnotated || !sawPlain {
		t.Fatalf("expected ServiceAccounts admin-server and plain in the applied stream, got %d SA(s)", len(objs["ServiceAccount"]))
	}

	// Nowhere else: not on any other object, and not on the pod template.
	for kind, list := range objs {
		for _, obj := range list {
			if kind == "ServiceAccount" && nameOf(obj) == "admin-server" {
				continue
			}
			if _, ok := annotationsOf(obj)[gsaKey]; ok {
				t.Errorf("%s/%s carries %s; it belongs on the ServiceAccount only", kind, nameOf(obj), gsaKey)
			}
			if kind != "Deployment" {
				continue
			}
			spec, _ := obj["spec"].(map[string]any)
			tmpl, _ := spec["template"].(map[string]any)
			if _, ok := annotationsOf(tmpl)[gsaKey]; ok {
				t.Errorf("Deployment/%s pod template carries %s; it belongs on the ServiceAccount only", nameOf(obj), gsaKey)
			}
		}
	}
}
