package cli

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/pkg/release"
)

// Every named forge.Manifests group is a declared placement owner, with
// release_inputs or without: its objects carry its owner label either way
// (kcl/render.k _manifests_owner), so the inventory declares it either way. A
// hosted frontend with absent release_dependencies waits for it like any server.
func TestPlacementDeclaresEveryManifestOwner(t *testing.T) {
	e := &KCLEntities{
		Workloads:                []WorkloadEntity{orderClusterWorkload("api", nil)},
		Frontends:                []FrontendEntity{orderFrontend("web", FrontendRuntimeHosted, nil)},
		ReleaseManifestConsumers: []releaseManifestConsumer{{Name: "prepull", Cluster: "daemon", ReleaseInputs: map[string]string{"container:prepull": "registry.example/prepull"}}},
		// prepull is both; cluster-rbac (no controllers) and side-jobs (has
		// controllers) declare no release_inputs; api names a workload too.
		ManifestOwners: []manifestOwner{{Name: "prepull", Cluster: "daemon"}, {Name: "cluster-rbac", Cluster: "daemon"}, {Name: "side-jobs", Cluster: "hub"}, {Name: "api", Cluster: "hub"}},
	}
	decl, err := bundlePlacementDeclarations(e)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]bundle.PlacementDeclaration{}
	for _, d := range decl {
		if _, dup := byName[d.Workload]; dup {
			t.Fatalf("%s declared twice: %+v", d.Workload, decl)
		}
		byName[d.Workload] = d
	}
	for _, want := range []string{"api", "prepull", "cluster-rbac", "side-jobs"} {
		d, ok := byName[want]
		if !ok || d.Runtime != "cluster" || d.Actuator != "forge" {
			t.Fatalf("%s is not a declared cluster placement: %+v", want, decl)
		}
	}
	web := byName["web"]
	for _, server := range []string{"api", "prepull", "cluster-rbac", "side-jobs"} {
		if !slices.Contains(web.Dependencies, server) {
			t.Fatalf("web (absent release_dependencies) does not wait for %s: %v", server, web.Dependencies)
		}
	}
	// A pure cluster env (no hosted part) still has no placement inventory.
	e.Frontends = nil
	if decl, err := bundlePlacementDeclarations(e); err != nil || decl != nil {
		t.Fatalf("pure cluster env got placements %+v (%v)", decl, err)
	}
}

// prod's shape in miniature, through the real KCL render and the real seal: a
// cluster workload, a hosted SPA with absent release_dependencies, and two
// forge.Manifests groups with NO release_inputs: one that renders only RBAC and
// a PriorityClass (prod's prod-daemon-cluster), and one that renders a
// controller. Before the owner rule was one definition, the first group's
// ClusterRole carried forge.dev/workload=daemon-cluster while the inventory
// never declared it, and sealing refused the bundle ("undeclared placement
// owner"). Now it seals: the controller group is an item the SPA waits for,
// the RBAC group is inventory only, and every owner label on every sealed
// object names a declared placement.
func TestMixedBundleSealsManifestGroupsWithoutReleaseInputs(t *testing.T) {
	if testing.Short() {
		t.Skip("temp git project and native KCL render; runs in task test")
	}
	dir := t.TempDir()
	main := `import forge
import forge.workloads as fw
_hub = forge.ClusterTarget {cluster = "k3d-prod-hub", namespace = "app", platform = "amd64"}
_daemon = forge.ClusterTarget {cluster = "k3d-prod-daemon", namespace = "app", platform = "amd64"}
_deploy = lambda name: str -> any {
    {
        apiVersion = "apps/v1"
        kind = "Deployment"
        metadata = {name = name}
        spec = {
            selector = {matchLabels = {app = name}}
            template = {metadata = {labels = {app = name}}, spec = {
                securityContext = {runAsNonRoot = True, seccompProfile = {type = "RuntimeDefault"}}
                containers = [{name = name, image = "registry.test/side@sha256:` + strings.Repeat("c", 64) + `", securityContext = {allowPrivilegeEscalation = False, capabilities = {drop = ["ALL"]}}}]
            }}
        }
    }
}
output = forge.render(forge.Bundle {
 project = "owners"
 env = "prod"
 control_plane = forge.ControlPlane {endpoint = "https://cp.invalid", token_env = "FORGE_OWNERS_TEST_TOKEN"}
 workloads = [
  fw.Workload {name = "api", image = "registry.test/api:stable", ports = [fw.Port {name = "http", port = 8080, expose = True}], runtime = forge.OnCluster {target = _hub}}
 ]
 manifests = [
  forge.Manifests {name = "daemon-cluster", cluster = "k3d-prod-daemon", objects = [
   {apiVersion = "rbac.authorization.k8s.io/v1", kind = "ClusterRole", metadata = {name = "scheduler"}, rules = []}
   {apiVersion = "scheduling.k8s.io/v1", kind = "PriorityClass", metadata = {name = "tenant"}, value = 1000}
  ]}
  forge.Manifests {name = "side-jobs", cluster = "k3d-prod-hub", namespace = "app", objects = [_deploy("side")]}
 ]
 frontends = [forge.Frontend {
  name = "web"
  image = "registry.test/acme/web"
  path = "frontends/web"
  type = "vite"
  public_dir = "dist"
  runtime = forge.OnHosted {}
 }]
})
`
	initGitRepo(t, dir, map[string]string{
		"forge.yaml":                 "name: owners\nmodule_path: github.com/acme/owners\n",
		"go.mod":                     "module github.com/acme/owners\n\ngo 1.23\n",
		"deploy/kcl/kcl.mod":         "[package]\nname = \"owners\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\n",
		"deploy/kcl/prod/main.k":     main,
		"frontends/web/package.json": "{\"name\":\"web\",\"private\":true}\n",
		".gitignore":                 ".forge/\nbin/\n**/kcl.mod.lock\n",
	})
	t.Chdir(dir)
	useLedgerHome(t, t.TempDir())
	t.Setenv("FORGE_OWNERS_TEST_TOKEN", "test-token")

	ctx := context.Background()
	doc, err := projectEnvShape(ctx, &strings.Builder{}, "prod")
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	owners := map[string]bool{}
	for _, o := range doc.entities.ManifestOwners {
		owners[o.Name] = true
	}
	if !owners["daemon-cluster"] || !owners["side-jobs"] {
		t.Fatalf("output.manifest_owners = %+v, want both named groups", doc.entities.ManifestOwners)
	}
	if len(doc.entities.ReleaseManifestConsumers) != 0 {
		t.Fatalf("a group with no release_inputs became a composition consumer: %+v", doc.entities.ReleaseManifestConsumers)
	}
	placements, err := bundlePlacementDeclarations(doc.entities)
	if err != nil {
		t.Fatal(err)
	}
	built, err := bundle.Build(ctx, bundle.BuildInput{
		Placements: placements, Project: doc.Project, Env: "prod", Release: "v1",
		Provenance: doc.Provenance, Shape: bundleShapeInputOf(doc),
		CreatedAt: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), Units: doc.units,
		Reconciled: doc.reconciled(), ReconciledUnits: doc.reconciledUnits(),
	})
	if err != nil {
		t.Fatalf("the mixed bundle was refused: %v", err)
	}
	p := built.Doc.PlacementPrerequisites
	if p == nil || p.Version != release.PlacementPrerequisitesVersion {
		t.Fatalf("sealed without placement prerequisites v%d: %+v", release.PlacementPrerequisitesVersion, p)
	}
	waitsFor := map[string]bool{}
	for _, e := range p.Edges {
		if e.Dependent.Runtime == "hosted" && e.Dependent.Name == "web" {
			waitsFor[e.Predecessor.Kind+"/"+e.Predecessor.Name] = true
		}
	}
	if !waitsFor["Deployment/side"] || !waitsFor["Deployment/api"] {
		t.Fatalf("the SPA does not wait for every server controller: %v", waitsFor)
	}
	for _, it := range p.Items {
		if it.Workload == "daemon-cluster" {
			t.Fatalf("a group with no controller became an item: %+v", it)
		}
	}
	requireEveryOwnerDeclared(t, doc.manifests, placements)
}

// requireEveryOwnerDeclared: every owner a sealed object names (its
// forge.dev/workload label, or its forge.dev/release-owners annotation, which
// wins) is a declared placement. Hosted records are their own component.
func requireEveryOwnerDeclared(t *testing.T, manifests string, placements []bundle.PlacementDeclaration) {
	t.Helper()
	declared := map[string]bool{}
	for _, d := range placements {
		declared[d.Workload] = true
	}
	checked := 0
	for _, raw := range strings.Split(manifests, "\n---") {
		if strings.Contains(raw, "# cluster: hosted") {
			continue
		}
		var obj struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name        string            `json:"name"`
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(raw), &obj); err != nil || obj.Kind == "" {
			continue
		}
		var names []string
		if a := obj.Metadata.Annotations["forge.dev/release-owners"]; a != "" {
			if err := json.Unmarshal([]byte(a), &names); err != nil {
				t.Fatalf("%s/%s: release-owners %q: %v", obj.Kind, obj.Metadata.Name, a, err)
			}
		} else if w := obj.Metadata.Labels["forge.dev/workload"]; w != "" {
			names = []string{w}
		}
		for _, n := range names {
			checked++
			if !declared[n] {
				t.Errorf("sealed object %s/%s names owner %q, which no placement declares", obj.Kind, obj.Metadata.Name, n)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no sealed object named an owner: the check proved nothing")
	}
}
