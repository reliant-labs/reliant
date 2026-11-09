package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kclplugin"
)

// --frontends-only must leave nothing that makes the env look like it applies
// to a cluster. A surviving ClusterTarget alone sent a versioned
// `--frontends-only` deploy into the whole-stream cluster apply.
func TestFilterEntitiesToFrontendsOnly_DropsTheClusterDeclaration(t *testing.T) {
	e := &KCLEntities{
		ClusterTarget:       &ClusterTargetEntity{Cluster: "gke_hub", Namespace: "ns"},
		ConnectedClusters:   map[string]string{"gke_hub": "hub"},
		Frontends:           []FrontendEntity{{Name: "web", Runtime: FrontendRuntime{Type: FrontendRuntimeFirebase}}},
		RequiredDNS:         []DNSRecordEntity{{}},
		ControlPlaneSecrets: []ControlPlaneSecretEntity{{Name: "s"}},
		ManifestClusters:    []ManifestClusterEntity{{Cluster: "gke_daemon"}},
		ClusterWork:         []string{"Deployment/x"},
	}
	got := filterEntitiesToFrontendsOnly(e)
	if kclEntitiesHaveK8sCluster(got) {
		t.Fatalf("a frontends-only entity set must not look cluster-bound: %+v", got)
	}
	if got.ClusterTarget != nil || len(got.RequiredDNS) != 0 || len(got.ControlPlaneSecrets) != 0 || len(got.ClusterWork) != 0 {
		t.Errorf("cluster-only declarations survived the filter: %+v", got)
	}
	if e.ClusterTarget == nil {
		t.Error("the filter mutated its input")
	}
}

// A deploy that names a release renders every image reference from the
// release: the tag is the release version, whatever .forge/state says.
func TestRenderImageTag_NamedReleaseIgnoresLocalBuildState(t *testing.T) {
	dir := t.TempDir()
	if err := WriteBuildState(dir, "prod", BuildState{Tag: "older-cut", Image: "app"}); err != nil {
		t.Fatal(err)
	}
	if tag, _ := renderImageTag(context.Background(), dir, "prod", ""); tag != "older-cut" {
		t.Fatalf("precondition: an unnamed render reads the build state, got %q", tag)
	}
	ctx := withHostedPinRelease(context.Background(), "v2")
	if tag, src := renderImageTag(ctx, dir, "prod", ""); tag != "v2" {
		t.Fatalf("renderImageTag = %q (%s), want the named release v2", tag, src)
	}
	if tag, _ := renderImageTag(ctx, dir, "prod", "explicit"); tag != "explicit" {
		t.Errorf("--tag must win, got %q", tag)
	}
}

// End to end through runDeploy: `--frontends-only` on an env that declares a
// cluster must not touch kubectl at all (no context guard, no apply).
func TestRunDeploy_FrontendsOnlyNeverTouchesKubectl(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL and runs the deploy; runs in task test")
	}
	requirePOSIXFake(t, "kubectl")
	kclplugin.Register()
	dir := workloadURLProject(t, "prod", `    project = "acme"
    env = "prod"
    cluster_target = forge.ClusterTarget { cluster = "gke_hub", namespace = "acme-prod" }
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api:v1"
        ports = [fw.Port {name = "http", port = 8080}]
        runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "gke_daemon", namespace = "acme-prod"}}
    }]
    frontends = [forge.Frontend {
        name = "web"
        path = "frontends/web"
        type = "vite"
        runtime = forge.OnFirebase {project = "p", site = "s"}
    }]`)
	if err := os.MkdirAll(filepath.Join(dir, "frontends", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "calls.log")
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\ncat >/dev/null 2>&1\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Chdir(dir)
	useLedgerHome(t, t.TempDir())
	writeProjectName(t, dir, "acme")

	var err error
	out := captureStdout(t, func() {
		err = runDeploy(context.Background(), "prod", deployOptions{dryRun: true, frontendsOnly: true, imageTag: "v1"})
	})
	if data, _ := os.ReadFile(log); len(data) > 0 {
		t.Errorf("--frontends-only invoked kubectl:\n%s", data)
	}
	for _, bad := range []string{"Namespace:", "kubectl context:", "Generating manifests"} {
		if strings.Contains(out, bad) {
			t.Errorf("--frontends-only printed the cluster deploy output %q:\n%s", bad, out)
		}
	}
	if err != nil && strings.Contains(err.Error(), "refusing to apply") {
		t.Errorf("the cluster apply ran: %v", err)
	}
}

// A versioned deploy must reproduce the release's bundle byte for byte from
// any checkout at its commit: the objects it renders cannot depend on the tag
// a stale .forge/state/build-<env>.json happens to hold.
func TestRenderEnvShape_NamedReleaseDoesNotDependOnLocalBuildStateTag(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	kclplugin.Register()
	dir := writeRenderStdoutProject(t)
	main := filepath.Join(dir, "deploy", "kcl", "prod", "main.k")
	src, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	// An env var that spells the env tag by hand (DAEMON_IMAGE-style).
	patched := strings.Replace(string(src), `fw.Workload {name = "api", image = "reg.example.com/rendertest"}`,
		`fw.Workload {name = "api", image = "reg.example.com/rendertest", env = {TAGGED = "reg.example.com/base:${_tagopt}"}}`, 1)
	patched = strings.Replace(patched, "_bundle = forge.Bundle {", "_tagopt = option(\"image_tag\") or \"unset\"\n_bundle = forge.Bundle {", 1)
	if patched == string(src) {
		t.Fatal("fixture patch did not apply")
	}
	if err := os.WriteFile(main, []byte(patched), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := testCutRelease(t, dir, rel("v1.0.0", "2026-01-01T00:00:00Z", "", false,
		map[string]string{"reg.example.com/rendertest": renderReleaseDigest})); err != nil {
		t.Fatalf("cut: %v", err)
	}
	t.Chdir(dir)

	hashOf := func(buildTag string) string {
		t.Helper()
		if err := WriteBuildState(dir, "prod", BuildState{Image: "reg.example.com/rendertest", Tag: buildTag,
			Pushed: true, PushedAt: "2026-09-24T00:00:00Z", Digest: renderBuiltDigest}); err != nil {
			t.Fatal(err)
		}
		doc, err := renderEnvShape(withHostedPinRelease(context.Background(), "v1.0.0"), io.Discard, dir, "prod")
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		return shapeObjectNamed(t, doc.Shape, "Deployment", "api").Hash
	}
	if a, b := hashOf("cut-one"), hashOf("cut-two"); a != b {
		t.Fatalf("the release's Deployment renders differently under two local build tags (%s vs %s): a versioned deploy is not reproducible", a, b)
	}
}
