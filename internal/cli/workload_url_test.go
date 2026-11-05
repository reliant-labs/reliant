package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/deploytarget"
)

// workloadURLProject writes a project whose `env` renders the given Bundle
// body through the real forge KCL module.
func workloadURLProject(t *testing.T, env, bundleBody string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"forge.yaml":         "name: acme\nmodule_path: github.com/example/acme\n\n",
		"deploy/kcl/kcl.mod": "[package]\nname = \"acme_deploy\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n",
		"deploy/kcl/" + env + "/main.k": "import forge\nimport forge.tiers\nimport forge.workloads as fw\n\n_bundle = forge.Bundle {\n" + bundleBody + "\n}\n\n" +
			"output = forge.render(_bundle)\n",
	}
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const hostedWorkloadURLBundle = `    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane { endpoint = "https://cp.example"}
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api:v1"
        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        env = {CORS_ORIGINS = forge.WorkloadURL {workload = "web"}}
        runtime = forge.OnHosted {}
    }]
    frontends = [forge.Frontend {
        name = "web"
        image = "ghcr.io/acme/web"
        path = "frontends/web"
        type = "vite"
        runtime_config = {
            API_URL = forge.WorkloadURL { workload = "api" }
            APP_NAME = "acme"
        }
        public_dir = "dist"
        base_path = "/app"
        runtime = forge.OnHosted {}
    }]`

// TestWorkloadURL_HostedLowersToSpecReferences: a HOSTED referrer (an
// OnHosted workload, an OnHosted frontend) leaves every forge.WorkloadURL a
// REFERENCE, and the hosted group publishes it verbatim — the StaticSite
// spec's runtimeConfig and the Workload CR's env workloadURL — for the
// control plane to resolve. Nothing resolved means nothing for forge to write
// into a config.js.
func TestWorkloadURL_HostedLowersToSpecReferences(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	dir := workloadURLProject(t, "prod", hostedWorkloadURLBundle)
	entities, err := RenderKCL(context.Background(), dir, "prod")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if o := frontendRuntimeOverlays(entities); len(o) != 0 {
		t.Fatalf("hosted frontend resolved runtime config %v; the control plane resolves it", o)
	}
	groups, err := buildDeployGroups("prod", entities, "")
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups = %+v err = %v (hosted workload + hosted site = one hosted group)", groups, err)
	}
	var static, workload *deploytarget.HostedWorkload
	for _, s := range groups[0].Services {
		switch s.Name {
		case "web":
			static = s.Hosted
		case "api":
			workload = s.Hosted
		}
	}
	if static == nil || static.Static == nil || workload == nil || workload.Workload == nil {
		t.Fatalf("hosted workloads missing: %+v", groups[0].Services)
	}
	rc := static.Static.RuntimeConfig
	if ref := rc["API_URL"].WorkloadURL; ref == nil || ref.Name != "api" || rc["API_URL"].Value != nil {
		t.Errorf("runtimeConfig.API_URL = %+v, want the reference {workloadURL: {name: api}}", rc["API_URL"])
	}
	if v := rc["APP_NAME"].Value; v == nil || *v != "acme" {
		t.Errorf("runtimeConfig.APP_NAME = %+v, want the literal acme", rc["APP_NAME"])
	}
	if err := static.Static.Validate(); err != nil {
		t.Errorf("published StaticSite spec does not validate: %v", err)
	}
	// The declared reference first, then the OTEL_SERVICE_NAME and
	// OTEL_RESOURCE_ATTRIBUTES forge.render derives for every process-running
	// workload.
	env := workload.Workload.Env
	if len(env) != 3 || env[0].WorkloadURL == nil || env[0].WorkloadURL.Name != "web" || env[0].Value != "" ||
		env[1].Name != "OTEL_SERVICE_NAME" || env[1].Value != "api" || env[2].Name != "OTEL_RESOURCE_ATTRIBUTES" {
		t.Errorf("workload env = %+v, want CORS_ORIGINS carrying the reference to web, then the derived OTEL_SERVICE_NAME=api and OTEL_RESOURCE_ATTRIBUTES", env)
	}
}

// TestWorkloadURL_LocalEnvResolvesAtRender: a host/cluster referrer's
// references are lowered at render time — the frontend's runtime document
// gets the host workload's URL, and the cluster workload's env var reaches
// the Kubernetes Deployment as a VALUE (pkg/deploy.RenderWorkloads refuses a
// reference on a cluster record, so this also proves none survives to
// extraction).
func TestWorkloadURL_LocalEnvResolvesAtRender(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	dir := workloadURLProject(t, "dev", `    project = "acme"
    env = "dev"
    cluster_target = forge.ClusterTarget { cluster = "k3d-acme", namespace = "acme-dev" }
    workloads = [
        fw.Workload {name = "api", build = forge.GoBuild {cmd = "./cmd/acme"}, args = ["api"], runtime = forge.OnHost {listen_ports = [8085]}}
        fw.Workload {
            name = "admin-api"
            image = "ghcr.io/acme/admin:v1"
            ports = [fw.Port {name = "http", port = 8080}]
            env = {CORS_ORIGINS = forge.WorkloadURL {workload = "web"}}
            runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}}
        }
    ]
    frontends = [forge.Frontend {
        name = "web"
        path = "frontends/web"
        type = "vite"
        port = 5173
        runtime_config = { API_URL = forge.WorkloadURL { workload = "api" }, APP_NAME = "acme" }
        runtime = forge.OnHost {}
    }]`)
	entities, err := RenderKCL(context.Background(), dir, "dev")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	overlays := frontendRuntimeOverlays(entities)
	if got := overlays["web"]["API_URL"]; got != "http://localhost:8085" {
		t.Errorf("web API_URL = %q, want the host service's declared port", got)
	}
	docs, err := renderFrontendRuntimeDocsWith(dir, "dev", overlays)
	if err != nil {
		t.Fatal(err)
	}
	if doc := docs["web"]; !strings.Contains(doc, `"API_URL": "http://localhost:8085"`) || !strings.Contains(doc, `"APP_NAME": "acme"`) ||
		!strings.Contains(doc, "window.__FORGE_CONFIG__ = ") {
		t.Errorf("config.js for a frontend with runtime_config and no typed config =\n%s", doc)
	}

	manifests, err := cluster.RenderManifests(context.Background(), filepath.Join(dir, "deploy", "kcl", "dev", "main.k"), "t", "acme-dev", "dev", nil, nil)
	if err != nil {
		t.Fatalf("render manifests (an unresolved reference would be refused here): %v", err)
	}
	// A CLUSTER referrer reaches a host target through its ClusterTarget's
	// host_gateway (host.k3d.internal by default): inside the pod,
	// localhost is the pod itself (kcl/lib/workload_url.k, #287).
	if !strings.Contains(manifests, "name: CORS_ORIGINS\n") || !strings.Contains(manifests, "value: http://host.k3d.internal:5173") {
		t.Errorf("Deployment env does not carry the frontend URL resolved through the cluster's host_gateway:\n%s", manifests)
	}
	if strings.Contains(manifests, "workloadURL") {
		t.Errorf("a reference survived into the applied manifests:\n%s", manifests)
	}
}

// TestHostedStaticBuildShipsNoRuntimeConfig: a hosted release is promoted by
// digest, so its artifact must be environment-agnostic. The build hands the
// packer a frontend that WRITES no config.js and STRIPS the dev copy that
// travels in the built bundle.
func TestHostedStaticBuildShipsNoRuntimeConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	dir := workloadURLProject(t, "prod", hostedWorkloadURLBundle)
	t.Chdir(dir)
	entities, err := RenderKCL(context.Background(), dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	var got []deploytarget.StaticSiteFrontend
	prev := hostedStaticPusher
	hostedStaticPusher = func(_ context.Context, _, _ string, fe deploytarget.StaticSiteFrontend) (string, error) {
		got = append(got, fe)
		return hostedStaticDigest, nil
	}
	t.Cleanup(func() { hostedStaticPusher = prev })
	t.Setenv("FORGE_HOME", t.TempDir())
	if err := buildHostedStaticSites(context.Background(), dir, entities, buildOptions{env: "prod", pushPlan: pushPlan{push: true, env: "prod"}}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("pushed %d sites, want 1", len(got))
	}
	if got[0].RuntimeConfigJS != "" || !got[0].StripRuntimeConfig {
		t.Errorf("hosted artifact runtime config: RuntimeConfigJS=%q Strip=%v; want none written and the dev copy stripped",
			got[0].RuntimeConfigJS, got[0].StripRuntimeConfig)
	}
}
