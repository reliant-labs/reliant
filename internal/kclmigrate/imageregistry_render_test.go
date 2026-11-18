package kclmigrate_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kclmigrate"
	"github.com/reliant-labs/forge/internal/kclrender"
)

// The migration's output has to RENDER. That is the property the houndersclub
// failure actually violated: the rewrite "succeeded", reported two removals,
// and left a tree that no forge command could evaluate — every env render died
// on a cluster workload whose image named no registry host. Asserting on the
// rewritten text alone would have passed on exactly that tree, because the
// text was well-formed; it was only wrong.
//
// So this renders through kclrender — forge's OWN evaluation seam, with the
// module supplied from this binary and the kcl_plugin.forge namespace
// registered — which is the only way any forge command reads these files.

// houndersPreMigration is the pre-#322 shape reduced to what a render needs:
// two workloads sharing one binary and declaring NO image, a dev env whose
// ClusterTarget carries a local registry while every workload runs OnHost, and
// a hosted prod env with a ControlPlane registry and a hosted static frontend
// that declares no image either.
var houndersPreMigration = map[string]string{
	// ONE kcl.mod, at deploy/kcl/, exactly as forge scaffolds it: the envs are
	// sub-packages of it, which is what makes `import ..workloads` resolve.
	// No `forge` dependency — the module is supplied from this binary.
	"deploy/kcl/kcl.mod": "[package]\nname = \"hounders-deploy\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n",
	"deploy/kcl/workloads.k": `import forge
import forge.workloads as fw

membership = fw.Workload {
    name = "membership"
    kind = "service"
    build = forge.GoBuild {cmd = "./cmd/hounders", output_name = "hounders"}
    args = ["membership"]
    ports = [fw.Port {name = "http", port = 8080, expose = True}]
}

migrate = fw.Workload {
    name = "migrate"
    kind = "job"
    build = forge.GoBuild {cmd = "./cmd/hounders", output_name = "hounders"}
    args = ["db", "migrate", "up"]
}

ALL: [fw.Workload] = [migrate, membership]
`,
	"deploy/kcl/dev/main.k": `import forge
import forge.workloads as fw
import ..workloads as wl

_k3d = forge.ClusterTarget {
    cluster = "k3d-hounders"
    namespace = "hounders-dev"
    registry = "localhost:5050"
}

_on_host = lambda w: fw.Workload -> fw.Workload {
    w | {runtime = forge.OnHost {runner = "go-run"}}
}
_on_host_job = lambda w: fw.Workload -> fw.Workload {
    w | {runtime = forge.OnHost {runner = "go-run"}}
}

_workloads = [
    _on_host_job(wl.migrate)
    _on_host(wl.membership)
]

output = forge.render(forge.Bundle {
    project = "hounders"
    cluster_target = _k3d
    workloads = _workloads
})
`,
	"deploy/kcl/prod/main.k": `import forge
import forge.workloads as fw
import ..workloads as wl

_hosted = lambda w: fw.Workload -> fw.Workload {
    w | {runtime = forge.OnHosted {}}
}

_membership = wl.membership | {
    env = wl.membership.env | {CORS_ORIGINS = "https://hounders.club"}
}

_workloads = [
    _hosted(wl.migrate)
    _hosted(_membership)
]

output = forge.render(forge.Bundle {
    project = "hounders"
    control_plane = forge.ControlPlane {
        endpoint = "http://127.0.0.1:8090"
        registry = "ghcr.io/reliant-01"
    }
    secret_provider = forge.HostedSecrets {}
    workloads = _workloads
    frontends = [forge.Frontend {
        name = "web"
        path = "frontends/web"
        public_dir = "out"
        runtime = forge.OnHosted {}
    }]
})
`,
}

func writeProject(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// renderEnv evaluates one env the way forge does, returning the `output`
// contract.
func renderEnv(t *testing.T, root, env string) (map[string]any, error) {
	t.Helper()
	out, err := kclrender.Run(root, filepath.Join(root, "deploy", "kcl", env), []string{"env=" + env})
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal %s render: %v\n%s", env, err, out)
	}
	contract, ok := doc["output"].(map[string]any)
	if !ok {
		t.Fatalf("%s render has no `output` contract:\n%s", env, out)
	}
	return contract, nil
}

// TestMigratedHoundersShapeRenders is the end-to-end property: the pre-#322
// tree does not render (the env registries are closed-schema errors now), the
// migration rewrites it, and the result renders — with the references the
// migration wrote reaching the rendered spec.
func TestMigratedHoundersShapeRenders(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	root := writeProject(t, houndersPreMigration)

	// BEFORE: the tree cannot be evaluated at all. `registry` is not a field
	// on either schema now, so every render fails — which is why the migration
	// runs in generate, and why a migration that removes the registries
	// without placing them leaves a project with no way forward.
	if _, err := renderEnv(t, root, "prod"); err == nil {
		t.Fatal("the pre-#322 tree rendered, so this fixture is not the shape being migrated")
	} else if !strings.Contains(err.Error(), "registry") {
		t.Logf("pre-migration render failed (expected), on: %v", err)
	}

	res, err := kclmigrate.ImageRegistry(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Refused() {
		t.Fatalf("migration refused a tree it has everything it needs for:\n  ambiguous: %+v\n  unaccounted: %+v",
			res.Ambiguous, res.Unaccounted)
	}

	// AFTER: both envs render. This is the assertion the original migration
	// would have failed while reporting success.
	for _, env := range []string{"dev", "prod"} {
		if _, err := renderEnv(t, root, env); err != nil {
			t.Fatalf("the MIGRATED tree does not render %s — the migration left the project unusable:\n%v", env, err)
		}
	}

	// The reference the migration wrote is the one the render resolves: both
	// workloads ride the one image the build produces, under prod's registry.
	prod, err := renderEnv(t, root, "prod")
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(prod)
	if !strings.Contains(string(blob), "ghcr.io/reliant-01/hounders") {
		t.Errorf("prod's rendered spec names no ghcr.io/reliant-01/hounders image:\n%s", blob)
	}
	if !strings.Contains(string(blob), "ghcr.io/reliant-01/web") {
		t.Errorf("prod's rendered spec names no ghcr.io/reliant-01/web site release:\n%s", blob)
	}
}

// TestMigrationDropsADeclaredOrganization: the org is the credential's, and
// ControlPlane is a closed schema, so a tree still declaring one would fail to
// load. The migration removes it — in either spelling — and seeds nothing.
func TestMigrationDropsADeclaredOrganization(t *testing.T) {
	files := map[string]string{}
	for k, v := range houndersPreMigration {
		files[k] = v
	}
	main := files["deploy/kcl/prod/main.k"]
	if !strings.Contains(main, "forge.ControlPlane") {
		t.Skip("the fixture's prod env declares no control plane")
	}
	files["deploy/kcl/prod/main.k"] = strings.Replace(main, "forge.ControlPlane {",
		"forge.ControlPlane {\n        # REPLACE THIS with your organization's id.\n        organization = \"REPLACE_ME_ORG_ID\"", 1)
	root := writeProject(t, files)
	if _, err := kclmigrate.ImageRegistry(root, true); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "deploy", "kcl", "prod", "main.k"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "organization") || strings.Contains(string(got), "REPLACE") {
		t.Fatalf("the migrated prod/main.k still carries an organization:\n%s", got)
	}
}
