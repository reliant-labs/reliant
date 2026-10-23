package cluster

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const byoDeployment = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  replicas: 1
---
apiVersion: v1
kind: Service
metadata:
  name: web
`

// byoOutput builds the KCL output extraction reads: output.<key> plus a
// primary cluster target, as forge.render emits.
func byoOutput(t *testing.T, extra map[string]any) []byte {
	t.Helper()
	out := map[string]any{
		"manifests":      []any{},
		"cluster_target": map[string]any{"cluster": "k3d-dev", "namespace": "acme"},
	}
	for k, v := range extra {
		out[k] = v
	}
	b, err := json.Marshal(map[string]any{"output": out})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeHelm puts a `helm` on PATH that prints stream for `helm template`.
func fakeHelm(t *testing.T, stream string) {
	t.Helper()
	requirePOSIXFake(t, "helm")
	dir := t.TempDir()
	body := writeFile(t, dir, "chart.yaml", stream)
	writeFile(t, dir, "helm", "#!/bin/sh\ncat "+body+"\n")
	if err := os.Chmod(filepath.Join(dir, "helm"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func extract(t *testing.T, projectDir string, raw []byte) (string, error) {
	t.Helper()
	stream, _, err := extractManifestsIn(context.Background(), projectDir, raw)
	return stream, err
}

func TestGenerated_ObjectsArePlacedAndStamped(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "out.yaml", byoDeployment)
	raw := byoOutput(t, map[string]any{"generated": []any{map[string]any{
		"name": "edge", "command": []string{"cat", "out.yaml"},
	}}})
	stream, err := extract(t, dir, raw)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	for _, want := range []string{"kind: Deployment", "kind: Service", "forge.dev/workload: edge", "forge.dev/cluster: k3d-dev", "namespace: acme"} {
		if !strings.Contains(stream, want) {
			t.Errorf("stream missing %q:\n%s", want, stream)
		}
	}
	if got := ManifestGroups(stream); len(got) != 1 || got[0] != "edge" {
		t.Errorf("ManifestGroups = %v, want [edge] (so --target edge selects it)", got)
	}
}

func TestGenerated_OverrideAppliesToItsObject(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "out.yaml", byoDeployment)
	raw := byoOutput(t, map[string]any{
		"generated": []any{map[string]any{"name": "edge", "command": []string{"cat", "out.yaml"}}},
		"overrides": map[string]any{"Deployment/web": map[string]any{"spec": map[string]any{"replicas": 7}}},
	})
	stream, err := extract(t, dir, raw)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !strings.Contains(stream, "replicas: 7") {
		t.Errorf("override did not land on the generated Deployment:\n%s", stream)
	}
}

func TestGenerated_FailuresNameTheGenerator(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "bad.yaml", "a: [unclosed\n")
	writeFile(t, dir, "nokind.yaml", "metadata:\n  name: x\n")
	cases := map[string][]string{
		"non-zero exit":   {"sh", "-c", "echo boom >&2; exit 3"},
		"unparsable yaml": {"cat", "bad.yaml"},
		"no kind":         {"cat", "nokind.yaml"},
	}
	for name, argv := range cases {
		t.Run(name, func(t *testing.T) {
			raw := byoOutput(t, map[string]any{"generated": []any{map[string]any{"name": "mygen", "command": argv}}})
			_, err := extract(t, dir, raw)
			if err == nil || !strings.Contains(err.Error(), `generator "mygen"`) {
				t.Fatalf("want an error naming generator mygen, got %v", err)
			}
		})
	}
}

func TestBundledChart_ObjectsJoinTheStream(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL and drives a fake helm through shell subprocesses; runs in task test")
	}
	fakeHelm(t, byoDeployment)
	raw := byoOutput(t, map[string]any{"bundled_charts": []any{map[string]any{
		"name": "shop", "chart": "shop", "repo": "https://charts.example", "version": "1.2.3", "namespace": "acme",
	}}})
	stream, err := extract(t, "", raw)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !strings.Contains(stream, "forge.dev/workload: shop") || !strings.Contains(stream, "kind: Deployment") {
		t.Errorf("chart objects missing or unstamped:\n%s", stream)
	}
}

func TestBYO_RefusedOnAHostedEnv(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "out.yaml", byoDeployment)
	raw := byoOutput(t, map[string]any{
		"workloads": []any{map[string]any{"name": "api", "runtime": map[string]any{"type": "hosted"}}},
		"generated": []any{map[string]any{"name": "edge", "command": []string{"cat", "out.yaml"}}},
	})
	_, err := extract(t, dir, raw)
	if err == nil || !strings.Contains(err.Error(), "hosted") || !strings.Contains(err.Error(), "Generated edge") {
		t.Fatalf("want a hosted refusal naming the source, got %v", err)
	}
}
