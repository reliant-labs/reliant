package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/release"
)

// Bring-your-own YAML, end to end: KCL declaration -> extraction -> the
// bundle's manifest layer. Driving bundle.Build over the extracted stream is
// the point: a source that reached `env render` but not the bundle would be
// exactly the "recorded as shipped, never applied" failure.

const byoMainK = `
import forge
import forge.workloads as fw

_target = forge.ClusterTarget {cluster = "k3d-byo", namespace = "acme"}

output = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    cluster_target = _target
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api:v1"
        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        runtime = forge.OnCluster {target = _target}
    }]
    helm_charts = [
        forge.HelmChart {
            name = "shop"
            chart = "shop"
            repo = "https://charts.example"
            version = "1.0.0"
            namespace = "acme"
            delivery = "bundle"
        }
        forge.HelmChart {
            name = "cert-manager"
            chart = "cert-manager"
            repo = "https://charts.example"
            version = "1.0.0"
            namespace = "cert-manager"
        }
    ]
    generated = [forge.Generated {
        name = "edge"
        command = ["cat", "GEN_YAML"]
    }]
    overrides = {"Deployment/edge-web" = {spec.replicas = 5}}
})
`

const byoGenYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: edge-web
spec:
  replicas: 1
`

const byoChartYAML = `apiVersion: v1
kind: Service
metadata:
  name: shop-svc
`

func byoFakeHelm(t *testing.T, stream string) {
	t.Helper()
	requirePOSIXFake(t, "helm")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "chart.yaml"), []byte(stream), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "helm"), []byte("#!/bin/sh\ncat "+filepath.Join(dir, "chart.yaml")+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func byoTree(t *testing.T, mainK string) (stream string, tree map[string]string) {
	t.Helper()
	gen := filepath.Join(t.TempDir(), "gen.yaml")
	if err := os.WriteFile(gen, []byte(byoGenYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	byoFakeHelm(t, byoChartYAML)
	// ToSlash: the path lands in a KCL string literal, where a Windows
	// backslash would be read as an escape.
	out := renderKCLProject(t, writeKCLProject(t, strings.ReplaceAll(mainK, "GEN_YAML", filepath.ToSlash(gen))))
	stream, err := cluster.ExtractManifests(out)
	if err != nil {
		t.Fatalf("ExtractManifests: %v", err)
	}
	objects, _ := attributeRenderedObjects(stream, nil, &KCLEntities{})
	for i := range objects {
		objects[i].Clusters = []string{"k3d-byo"}
	}
	b, err := bundle.Build(context.Background(), bundle.BuildInput{
		Project: "acme", Env: "dev",
		Shape:     bundle.ShapeInput{Kind: release.EnvSelfManaged, Clusters: []string{"k3d-byo"}, Manifests: renderShapeStream(objects)},
		CreatedAt: time.Unix(1, 0),
	})
	if err != nil {
		t.Fatalf("bundle.Build: %v", err)
	}
	layer, ok := b.Layer(release.BundleManifestsLayer)
	if !ok {
		t.Fatal("bundle has no manifests layer")
	}
	gz, err := gzip.NewReader(bytes.NewReader(layer))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	tree = map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		tree[h.Name] = string(body)
	}
	return stream, tree
}

func TestBYO_GeneratedAndBundledChartLandInTheBundleTree(t *testing.T) {
	_, tree := byoTree(t, byoMainK)
	prefix := release.BundleClusterPath("k3d-byo") + "/"
	var inTree []string
	for name, body := range tree {
		if strings.HasPrefix(name, prefix) || strings.HasPrefix("./"+name, prefix) {
			inTree = append(inTree, body)
		}
	}
	all := strings.Join(inTree, "\n---\n")
	if len(inTree) == 0 {
		t.Fatalf("nothing under %q in the bundle: %v", prefix, keys(tree))
	}
	for _, want := range []string{
		"name: edge-web", "forge.dev/workload: edge", "replicas: 5", // generated, labelled, overridden
		"name: shop-svc", "forge.dev/workload: shop", // delivery = "bundle" chart
	} {
		if !strings.Contains(all, want) {
			t.Errorf("cluster tree missing %q", want)
		}
	}
	if strings.Contains(all, "cert-manager") {
		t.Errorf("a default (bootstrap) chart leaked into the bundle:\n%s", all)
	}
}

func TestBYO_TargetSelectsASource(t *testing.T) {
	stream, _ := byoTree(t, byoMainK)
	got := cluster.ManifestGroups(stream)
	has := map[string]bool{}
	for _, g := range got {
		has[g] = true
	}
	if !has["edge"] || !has["shop"] {
		t.Fatalf("--target groups = %v, want edge and shop selectable", got)
	}
	sel := cluster.SelectManifestsByGroup(stream, []string{"edge"})
	if !strings.Contains(sel, "edge-web") || strings.Contains(sel, "shop-svc") {
		t.Errorf("--target edge selected the wrong objects:\n%s", sel)
	}
}

func TestBYO_HostedEnvRefusesBothSources(t *testing.T) {
	hosted := strings.Replace(byoMainK, `runtime = forge.OnCluster {target = _target}`, `runtime = forge.OnHosted {}`, 1)
	hosted = strings.Replace(hosted, `    lifecycle = "local"
`, `    control_plane = forge.ControlPlane {}
`, 1)
	gen := filepath.Join(t.TempDir(), "gen.yaml")
	_ = os.WriteFile(gen, []byte(byoGenYAML), 0o644)
	out := renderKCLProject(t, writeKCLProject(t, strings.ReplaceAll(hosted, "GEN_YAML", filepath.ToSlash(gen))))
	_, err := cluster.ExtractManifests(out)
	if err == nil || !strings.Contains(err.Error(), "hosted") {
		t.Fatalf("a hosted env using BYO sources must be refused at render, got %v", err)
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
