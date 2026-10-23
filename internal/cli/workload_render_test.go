package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/pkg/deploy"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// writeKCLProject writes a single-file KCL project (no forge dependency in
// kcl.mod: `import forge` is supplied from the binary, #277) and returns its
// directory. The main.k must end with `output = forge.render(bundle)`.
func writeKCLProject(t *testing.T, main string) string {
	t.Helper()
	dir := t.TempDir()
	kclMod := "[package]\nname = \"workload_render\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n"
	for f, c := range map[string]string{"kcl.mod": kclMod, "main.k": main} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// renderKCLProject renders dir through forge's KCL seam (kclrender.Run: the
// module from the binary, the kcl_plugin.forge namespace), exactly as every
// forge command does. The module cache is content-addressed and immutable
// once written, so parallel tests share it safely.
func renderKCLProject(t *testing.T, dir string, dArgs ...string) []byte {
	t.Helper()
	kclplugin.Register()
	out, err := kclrender.Run(dir, dir, dArgs)
	if err != nil {
		t.Fatalf("render %s: %v", dir, err)
	}
	return out
}

// appliedObjects runs KCL output through cluster.ExtractManifests — exactly
// the stream kubectl receives, with every forge.dev Workload record expanded
// by pkg/deploy.RenderWorkloads — and returns the objects grouped by kind.
func appliedObjects(t *testing.T, kclOut []byte) map[string][]map[string]any {
	t.Helper()
	stream, err := cluster.ExtractManifests(kclOut)
	if err != nil {
		t.Fatalf("ExtractManifests: %v", err)
	}
	byKind := map[string][]map[string]any{}
	for _, doc := range strings.Split(stream, "\n---\n") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatalf("parse applied object: %v\n%s", err, doc)
		}
		kind, _ := obj["kind"].(string)
		byKind[kind] = append(byKind[kind], obj)
	}
	return byKind
}

// firstContainer digs the primary container out of a rendered Deployment.
func firstContainer(t *testing.T, deployment map[string]any) map[string]any {
	t.Helper()
	spec, _ := deployment["spec"].(map[string]any)
	tmpl, _ := spec["template"].(map[string]any)
	podSpec, _ := tmpl["spec"].(map[string]any)
	containers, _ := podSpec["containers"].([]any)
	if len(containers) == 0 {
		t.Fatal("Deployment has no containers")
	}
	c, _ := containers[0].(map[string]any)
	return c
}

func objectName(o map[string]any) string {
	md, _ := o["metadata"].(map[string]any)
	n, _ := md["name"].(string)
	return n
}

const acmeAPIWorkload = `import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {
    cluster = "gke_acme_us-central1_prod"
    namespace = "acme-prod"
    platform = "amd64"
}

_bundle = forge.Bundle {
    project = "acme"
    env = "prod"
    cluster_target = _prod
    workloads = [fw.Workload {
        name = "acme-api"
        # A third-party pinned image: the env registry and image_tag must
        # not rewrite a reference forge did not build.
        image = "ghcr.io/acme/api:v1.4.2"
        args = ["serve"]
        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        storageGiB = 20
        resources = fw.Resources {cpuRequestMillicores = 500, cpuLimitMillicores = 1000, memoryRequestBytes = 2147483648, memoryLimitBytes = 2147483648}
        env = {
            LOG_LEVEL = "info"
            DB_PASSWORD = forge.SecretRef {name = "acme-prod-db", key = "password"}
            DATABASE_URL = forge.DatabaseRef {name = "orders"}
        }
        runtime = forge.OnCluster {target = _prod}
    }]
}

output = forge.render(_bundle)
`

// TestWorkload_RoundTripsKCLToAppliedObjects is the ANTI-DRIFT test for the
// Cluster runtime. One test spans every layer rather than one test per layer
// each constructing its own input: a Go test that hand-builds a
// WorkloadEntity asserts about a struct, not about anything KCL produces,
// and stays green while the schema moves.
//
// So it renders REAL KCL through the real render seam, decodes the entity
// contract through the real parseKCLEntities, groups through the real
// buildDeployGroups, and expands output.manifests through the real
// cluster.ExtractManifests — the path `forge env deploy` takes to kubectl,
// where pkg/deploy.RenderWorkloads turns each Workload record into objects.
func TestWorkload_RoundTripsKCLToAppliedObjects(t *testing.T) {
	out := renderKCLProject(t, writeKCLProject(t, acmeAPIWorkload), `image_tag="v9-should-not-appear"`)

	// ── layer 1: KCL -> the Go entity contract ────────────────────────
	entities, err := parseKCLEntities(out)
	if err != nil {
		t.Fatalf("parse entities: %v\n%s", err, out)
	}
	if len(entities.Workloads) != 1 {
		t.Fatalf("want 1 workload, got %d", len(entities.Workloads))
	}
	w := entities.Workloads[0]
	if w.Runtime.Type != RuntimeCluster || w.Runtime.Cluster == nil {
		t.Fatalf("runtime = %+v, want cluster: the discriminator is what routes it to the k8s provider", w.Runtime)
	}
	if w.Runtime.Cluster.Cluster != "gke_acme_us-central1_prod" || w.Runtime.Cluster.Namespace != "acme-prod" {
		t.Errorf("target coordinates = %q/%q", w.Runtime.Cluster.Cluster, w.Runtime.Cluster.Namespace)
	}
	// The spec decoded strictly into forge's Go type and is VALID under the
	// profile a cluster the author operates runs: what an author writes in
	// KCL is an admissible spec.
	if err := w.Spec.Validate(deployv1alpha1.ProfileFull); err != nil {
		t.Errorf("entity spec fails WorkloadSpec.Validate(Full): %v", err)
	}
	if w.Spec.Image != "ghcr.io/acme/api:v1.4.2" || w.Spec.StorageGiB != 20 || w.Spec.Resources.MemoryRequestBytes != 2<<30 {
		t.Errorf("entity spec = %+v", w.Spec)
	}
	if w.Build.Type != "" {
		t.Errorf("Build.Type = %q, want empty: an image-only workload is not built by forge (no synthesized ./cmd default)", w.Build.Type)
	}
	// The secret pre-flight sees the secretRef (the channels it can check);
	// databaseRef reads CNPG's own Secret, which no store renders.
	// OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES are the keys forge
	// derives for this workload (an image forge does not build gets no
	// GOMEMLIMIT), after the declared ones.
	envs := w.EnvVars()
	if len(envs) != 4 || envs[1].SecretRef != "acme-prod-db" || envs[1].SecretKey != "password" ||
		envs[2].Name != "OTEL_SERVICE_NAME" || envs[2].Value != "acme-api" ||
		envs[3].Name != "OTEL_RESOURCE_ATTRIBUTES" {
		t.Errorf("EnvVars() = %+v, want LOG_LEVEL, the DB_PASSWORD secret ref, then the derived OTEL_SERVICE_NAME=acme-api and OTEL_RESOURCE_ATTRIBUTES", envs)
	}

	// ── layer 2: the entity contract -> deploy groups ─────────────────
	groups, err := buildDeployGroups("prod", entities, "fallback-ns")
	if err != nil {
		t.Fatalf("buildDeployGroups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("want 1 group, got %d: %+v", len(groups), groups)
	}
	g := groups[0]
	if g.ProviderID != "k8s-cluster" {
		t.Errorf("ProviderID = %q, want k8s-cluster", g.ProviderID)
	}
	if g.Cluster != "gke_acme_us-central1_prod" || g.Namespace != "acme-prod" {
		t.Errorf("group target = %q/%q (a workload in the fallback namespace lands in someone else's)", g.Cluster, g.Namespace)
	}
	if len(g.Services) != 1 || g.Services[0].K8sCluster == nil {
		t.Fatalf("group services = %+v", g.Services)
	}
	if p := deploytarget.NewRegistry().Lookup(g.ProviderID); p == nil {
		t.Fatalf("no registered provider for %q", g.ProviderID)
	}

	// ── layer 3: what kubectl actually receives ───────────────────────
	byKind := appliedObjects(t, out)
	if n := len(byKind["Workload"]); n != 0 {
		t.Fatalf("%d forge.dev Workload record(s) reached the applied stream: a self-hosted cluster has no CRD for them", n)
	}
	for kind, want := range map[string]int{"Deployment": 1, "Service": 1, "ServiceAccount": 1, "PersistentVolumeClaim": 1} {
		if got := len(byKind[kind]); got != want {
			t.Errorf("%s objects = %d, want %d", kind, got, want)
		}
	}
	// The env stamp on the record must survive expansion onto every object,
	// or env-scoped prune misses them.
	for kind, objs := range byKind {
		for _, o := range objs {
			md, _ := o["metadata"].(map[string]any)
			labels, _ := md["labels"].(map[string]any)
			if kind != "Namespace" && labels["forge.dev/env"] != "prod" {
				t.Errorf("%s %v lost the forge.dev/env stamp: %v", kind, md["name"], labels)
			}
		}
	}
	// The OBSERVER's half of the PVC: OwnedClaims must name the claim the
	// render actually emitted, or forge reports the workload green while its
	// storage is unbound.
	pvc := objectName(byKind["PersistentVolumeClaim"][0])
	if observed := g.Services[0].K8sCluster.OwnedClaims; len(observed) != 1 || observed[0] != pvc || pvc != deploy.PVCName("acme-api") {
		t.Errorf("OwnedClaims = %v, but the render emitted a PVC named %v", observed, pvc)
	}
	container := firstContainer(t, byKind["Deployment"][0])
	if got, _ := container["image"].(string); got != w.Spec.Image {
		t.Errorf("container image = %q, want the declared %q (the env registry or image_tag leaked in)", got, w.Spec.Image)
	}
	if args, _ := json.Marshal(container["args"]); string(args) != `["serve"]` {
		t.Errorf("container args = %s, want the declared [serve]", args)
	}
	env, _ := container["env"].([]any)
	var dbURL map[string]any
	for _, e := range env {
		if m, _ := e.(map[string]any); m["name"] == "DATABASE_URL" {
			dbURL = m
		}
	}
	if ref, _ := json.Marshal(dbURL["valueFrom"]); !strings.Contains(string(ref), `"name":"orders-app"`) || !strings.Contains(string(ref), `"key":"uri"`) {
		t.Errorf("DATABASE_URL = %s, want CNPG's orders-app/uri", ref)
	}
}

// TestWorkload_ForgeBuiltServiceIsHTTPProbed pins ADR 0002 §5 end to end: a
// service forge BUILDS gets split HTTP probes (/readyz readiness, /healthz
// liveness) on its `http` port after expansion, and a third-party image gets
// TCP on its first port. This is the Hounders defect — a service shipped
// with no probes because one render path had no default.
func TestWorkload_ForgeBuiltServiceIsHTTPProbed(t *testing.T) {
	out := renderKCLProject(t, writeKCLProject(t, `import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [_w | {runtime = forge.OnCluster {target = _k3d}} if not _w.runtime else _w for _w in [
        fw.Workload {name = "api", build = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}, image = "localhost:5050/demo", args = ["api"], ports = [fw.Port {name = "http", port = 8080}]}
        fw.Workload {name = "redis", image = "docker.io/library/redis:7.2", ports = [fw.Port {name = "redis", port = 6379}]}
    ]]
})
`))
	deployments := map[string]map[string]any{}
	for _, d := range appliedObjects(t, out)["Deployment"] {
		deployments[objectName(d)] = d
	}
	api := firstContainer(t, deployments["api"])
	rp, _ := json.Marshal(api["readinessProbe"])
	lp, _ := json.Marshal(api["livenessProbe"])
	if !strings.Contains(string(rp), `"path":"/readyz"`) || !strings.Contains(string(rp), `"port":8080`) {
		t.Errorf("api readinessProbe = %s, want HTTP /readyz on 8080", rp)
	}
	if !strings.Contains(string(lp), `"path":"/healthz"`) {
		t.Errorf("api livenessProbe = %s, want HTTP /healthz", lp)
	}
	redis := firstContainer(t, deployments["redis"])
	if rp, _ := json.Marshal(redis["readinessProbe"]); !strings.Contains(string(rp), `"tcpSocket"`) || strings.Contains(string(rp), "httpGet") {
		t.Errorf("redis readinessProbe = %s, want TCP (forge cannot know a third-party image serves /readyz)", rp)
	}
}

// TestWorkload_RequestOnlyLimitsEqualRequests: "set a request, leave the
// limit" through the real KCL render and expansion. The generated Workload
// leaves the limits unset, so the record reaches Validate and the rendered
// Deployment with each limit equal to its request — never a static 250m /
// 1 GiB limit below the request that gets the spec refused.
func TestWorkload_RequestOnlyLimitsEqualRequests(t *testing.T) {
	out := renderKCLProject(t, writeKCLProject(t, `import forge
import forge.workloads as fw

_t = forge.ClusterTarget {cluster = "c", namespace = "acme-prod"}

output = forge.render(forge.Bundle {
    project = "acme"
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api:v2"
        ports = [fw.Port {name = "http", port = 8080}]
        resources = fw.Resources {cpuRequestMillicores = 500, memoryRequestBytes = 2147483648}
        runtime = forge.OnCluster {target = _t}
    }]
})
`))
	entities, err := parseKCLEntities(out)
	if err != nil {
		t.Fatalf("parse entities: %v\n%s", err, out)
	}
	if err := entities.Workloads[0].Spec.Validate(deployv1alpha1.ProfileFull); err != nil {
		t.Fatalf("request-only spec refused: %v", err)
	}
	res, _ := json.Marshal(firstContainer(t, appliedObjects(t, out)["Deployment"][0])["resources"])
	want := `{"limits":{"cpu":"500m","memory":"2Gi"},"requests":{"cpu":"500m","memory":"2Gi"}}`
	if string(res) != want {
		t.Errorf("container resources = %s, want %s", res, want)
	}
}

// TestWorkload_WorkerHasNoService: a worker is dialled by nothing, so it
// renders a Deployment and no Service, and declares no container port it
// does not listen on.
func TestWorkload_WorkerHasNoService(t *testing.T) {
	out := renderKCLProject(t, writeKCLProject(t, `import forge
import forge.workloads as fw

_t = forge.ClusterTarget {cluster = "c", namespace = "acme-prod"}

output = forge.render(forge.Bundle {
    project = "acme"
    workloads = [fw.Workload {name = "worker", kind = "worker", image = "ghcr.io/acme/worker:v2", args = ["work"], runtime = forge.OnCluster {target = _t}}]
})
`))
	byKind := appliedObjects(t, out)
	if n := len(byKind["Deployment"]); n != 1 {
		t.Errorf("Deployments = %d, want 1", n)
	}
	if n := len(byKind["Service"]); n != 0 {
		t.Errorf("Services = %d, want 0 for a worker", n)
	}
	if ports, ok := firstContainer(t, byKind["Deployment"][0])["ports"]; ok {
		t.Errorf("a worker declares container ports %v; it listens on nothing", ports)
	}
}

// TestWorkload_NonClusterRuntimesApplyNothing: a mixed env renders records
// only for its cluster workloads; host, compose, hosted and build-only
// workloads reach the applied stream as nothing at all.
func TestWorkload_NonClusterRuntimesApplyNothing(t *testing.T) {
	out := renderKCLProject(t, writeKCLProject(t, `import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
_pinned = "ghcr.io/acme/billing@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

output = forge.render(forge.Bundle {
    project = "demo"
    control_plane = forge.ControlPlane {endpoint = "http://127.0.0.1:8090"}
    cluster_target = _k3d
    workloads = [
        fw.Workload {name = "local", build = forge.GoBuild {cmd = "./cmd/demo"}, args = ["local"], runtime = forge.OnHost {}}
        fw.Workload {name = "postgres", runtime = forge.OnCompose {service = "postgres"}}
        fw.Workload {name = "search", image = "localhost:5050/demo", ports = [fw.Port {name = "http", port = 8080}], runtime = forge.OnCluster {target = _k3d}}
        fw.Workload {name = "billing", image = _pinned, ports = [fw.Port {name = "http", port = 8080, expose = True}], runtime = forge.OnHosted {}}
        fw.Workload {name = "cli", kind = "tool", build = forge.GoBuild {cmd = "./cmd/cli"}, runtime = forge.BuildOnly {}}
    ]
})
`))
	var names []string
	for _, d := range appliedObjects(t, out)["Deployment"] {
		names = append(names, objectName(d))
	}
	if strings.Join(names, ",") != "search" {
		t.Errorf("applied Deployments = %v, want only the cluster workload [search]", names)
	}
}

// TestManagedDatabase_WorkloadReachesDatabase is the three-tier shape in one
// declaration: a cluster ManagedDatabase plus a workload whose DATABASE_URL
// is a databaseRef to it, rendered through real KCL and expanded exactly as
// `forge env deploy` applies it.
//
// It pins the CONTRACT between the two renderers, which must agree without
// either being told: the CNPG Cluster is named for the database, it lands in
// the workload's namespace, and CloudNativePG publishes "<cluster>-app"
// there. The workload's secretKeyRef names exactly that Secret, key uri.
func TestManagedDatabase_WorkloadReachesDatabase(t *testing.T) {
	out := renderKCLProject(t, writeKCLProject(t, `import forge
import forge.workloads as fw

_t = forge.ClusterTarget {cluster = "c", namespace = "shop-prod"}

output = forge.render(forge.Bundle {
    project = "shop"
    env = "prod"
    cluster_target = _t
    databases = [forge.ManagedDatabase {name = "orders", runtime = forge.OnCluster {target = _t}}]
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api:v1"
        ports = [fw.Port {name = "http", port = 8080}]
        env = {DATABASE_URL = forge.DatabaseRef {name = "orders"}}
        runtime = forge.OnCluster {target = _t}
    }]
})
`))
	byKind := appliedObjects(t, out)
	if n := len(byKind["ManagedDatabase"]); n != 0 {
		t.Fatalf("%d forge.dev declaration(s) reached the applied stream", n)
	}
	if n := len(byKind["Cluster"]); n != 1 {
		t.Fatalf("CNPG Clusters = %d, want 1", n)
	}
	cl := byKind["Cluster"][0]
	md, _ := cl["metadata"].(map[string]any)
	if cl["apiVersion"] != "postgresql.cnpg.io/v1" || md["name"] != "orders" || md["namespace"] != "shop-prod" {
		t.Fatalf("Cluster = %v %v/%v", cl["apiVersion"], md["namespace"], md["name"])
	}
	annotations, _ := md["annotations"].(map[string]any)
	if annotations["forge.dev/deletion-policy"] != "retain" {
		t.Errorf("deletion-policy annotation = %v, want retain (the default)", annotations["forge.dev/deletion-policy"])
	}
	spec, _ := json.Marshal(cl["spec"])
	for _, want := range []string{`"enableSuperuserAccess":false`, `"database":"orders"`, `"owner":"orders_app"`, `"size":"10Gi"`} {
		if !strings.Contains(string(spec), want) {
			t.Errorf("Cluster spec %s missing %s", spec, want)
		}
	}
	env, _ := firstContainer(t, byKind["Deployment"][0])["env"].([]any)
	ref, _ := json.Marshal(env[0].(map[string]any)["valueFrom"])
	if !strings.Contains(string(ref), `"name":"orders-app"`) || !strings.Contains(string(ref), `"key":"uri"`) {
		t.Errorf("DATABASE_URL valueFrom = %s, want the Secret CNPG publishes for the Cluster (orders-app, key uri)", ref)
	}
	entities, err := parseKCLEntities(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entities.Databases) != 1 || entities.Databases[0].Name != "orders" || entities.Databases[0].Spec.WithDefaults().DeletionPolicy != "retain" {
		t.Errorf("entity databases = %+v", entities.Databases)
	}
}
