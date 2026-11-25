package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/internal/obsbackend"
)

// writeEnvDocument writes deploy/envs/<name> under dir.
func writeEnvDocument(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, "deploy", "envs", name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const minimalMonitoringDocument = `apiVersion: forge.dev/env/v1
project: shop
env: prod
control_plane: {}
observability:
  destination: {token_from_control_plane: true}
`

// TestEnvDocument_MonitoringOnlyIsTheSameTargetAsKCL: the minimal monitoring
// document renders through the one render seam into the obs target a KCL
// env declaring the same would: monitoring on, telemetry to Reliant's control
// plane, read back from it.
func TestEnvDocument_MonitoringOnlyIsTheSameTargetAsKCL(t *testing.T) {
	dir := t.TempDir()
	writeEnvDocument(t, dir, "prod.yaml", minimalMonitoringDocument)
	e, err := RenderKCL(context.Background(), dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if e.Project != "shop" || e.Env != "prod" || e.ControlPlane == nil || e.ControlPlane.TokenEnv != "FORGE_CONTROL_PLANE_TOKEN" {
		t.Fatalf("entities = %+v / control plane %+v", e, e.ControlPlane)
	}
	tgt := obsTargetFromEntities("prod", e)
	f := tgt.Facts
	if !f.Declared || !f.Enabled || f.Destination == nil || !f.Destination.TokenFromControlPlane || f.ControlPlane != cloud.DefaultEndpoint {
		t.Fatalf("facts = %+v", f)
	}
	if dec, err := obsbackend.Resolve(f, ""); err != nil || dec.Backend != obsbackend.BackendCloud {
		t.Fatalf("resolve = %+v, %v; want the cloud backend", dec, err)
	}
	envs, err := ListEnvs(dir)
	if err != nil || !reflect.DeepEqual(envs, []string{"prod"}) {
		t.Fatalf("ListEnvs = %v, %v", envs, err)
	}
	if ok, err := EnvExists(dir, "prod"); !ok || err != nil {
		t.Fatalf("EnvExists = %v, %v", ok, err)
	}
}

// TestEnvDocument_RenderContractLoadsLikeItsRender: a render-contract golden,
// saved as an env document, loads into exactly the entities its KCL render
// parses into. Lowering a document that already carries the lowered form is
// the identity — including a cluster workload forge builds, whose saved
// spec.image is the unbound derivation and is re-derived to itself.
func TestEnvDocument_RenderContractLoadsLikeItsRender(t *testing.T) {
	files, _ := filepath.Glob("testdata/render_contract/*.json")
	if len(files) == 0 {
		t.Fatal("no render-contract goldens")
	}
	loaded := 0
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var wrapper struct {
				Output map[string]any `json:"output"`
			}
			if err := json.Unmarshal(raw, &wrapper); err != nil {
				t.Fatal(err)
			}
			want, err := parseKCLEntities(raw)
			if err != nil {
				t.Fatal(err)
			}
			doc := wrapper.Output
			doc["apiVersion"] = "forge.dev/env/v1"
			body, _ := json.MarshalIndent(doc, "", "  ")
			dir := t.TempDir()
			writeEnvDocument(t, dir, want.Env+".json", string(body))

			out, ok, err := loadEnvDocument(context.Background(), dir, want.Env)
			if err != nil || !ok {
				t.Fatalf("load: ok %v, %v", ok, err)
			}
			got, err := parseKCLEntities(out)
			if err != nil {
				t.Fatal(err)
			}
			if g, w := mustJSON(t, got), mustJSON(t, want); g != w {
				t.Errorf("entities differ\n got %s\nwant %s", g, w)
			}
			if g, w := mustJSON(t, got.Workloads), mustJSON(t, want.Workloads); g != w {
				t.Errorf("workloads differ\n got %s\nwant %s", g, w)
			}
			gs, gerr := cluster.ExtractManifests(out)
			ws, werr := cluster.ExtractManifests(raw)
			if gerr != nil || werr != nil || gs != ws {
				t.Errorf("apply streams differ (err %v / %v)", gerr, werr)
			}
			loaded++
		})
	}
	if loaded == 0 {
		t.Fatal("no golden loaded as a document: the identity check ran on nothing")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestEnvDocument_ManifestRenderReadsTheDocument: the manifest render seam
// (cluster.RenderManifests, every cluster apply) reads a document env's
// stream from the document, with the Workload record the lowering adds
// expanded into its Deployment.
func TestEnvDocument_ManifestRenderReadsTheDocument(t *testing.T) {
	dir := t.TempDir()
	writeEnvDocument(t, dir, "prod.yaml", `apiVersion: forge.dev/env/v1
project: shop
env: prod
workloads:
  - name: web
    runtime: {type: cluster, cluster: k3d-shop, namespace: shop}
    spec: {image: "registry.example.com/web:1.0", ports: [{name: http, port: 80}]}
`)
	mainK := filepath.Join(dir, "deploy", "kcl", "prod", "main.k")
	stream, err := cluster.RenderManifests(context.Background(), mainK, "1.0", "shop", "prod", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"kind: Deployment", "name: web", "registry.example.com/web:1.0"} {
		if !strings.Contains(stream, want) {
			t.Errorf("stream lacks %q:\n%s", want, stream)
		}
	}
}

// TestEnvDocumentDefaults_KCLMatchesGo: a document's `control_plane: {}` and
// `observability: {}` take the defaults the empty KCL schemas render, so the
// Go copies of those defaults (internal/envdoc lowerControlPlane,
// lowerObservability) cannot drift from kcl/schema.k.
func TestEnvDocumentDefaults_KCLMatchesGo(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; runs in task test")
	}
	main := `import forge

_bundle = forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    observability = forge.Observability {}
}

output = forge.render(_bundle)
`
	kclplugin.Register()
	kdir := t.TempDir()
	kclMod := "[package]\nname = \"envdocdefaults\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\n"
	for f, c := range map[string]string{"kcl.mod": kclMod, "main.k": main} {
		if err := os.WriteFile(filepath.Join(kdir, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := kclrender.Run(kdir, kdir, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	fromKCL, err := parseKCLEntities(out)
	if err != nil {
		t.Fatal(err)
	}

	ddir := t.TempDir()
	writeEnvDocument(t, ddir, "prod.yaml", "apiVersion: forge.dev/env/v1\nproject: acme\nenv: prod\ncontrol_plane: {}\nobservability: {}\n")
	doc, _, err := loadEnvDocument(context.Background(), ddir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	fromDoc, err := parseKCLEntities(doc)
	if err != nil {
		t.Fatal(err)
	}
	k, d := fromKCL.ControlPlane, fromDoc.ControlPlane
	if k == nil || d == nil || k.Type != d.Type || k.Endpoint != d.Endpoint || k.TokenEnv != d.TokenEnv || k.RegistryHost != d.RegistryHost {
		t.Errorf("control_plane defaults: KCL %+v, document %+v", k, d)
	}
	ko, do := fromKCL.Observability, fromDoc.Observability
	if ko == nil || do == nil {
		t.Fatalf("observability: KCL %+v, document %+v", ko, do)
	}
	if ko.Enabled != do.Enabled || ko.Signals != do.Signals || ko.Sampling.TracesRatio != do.Sampling.TracesRatio {
		t.Errorf("observability defaults: KCL enabled=%v signals=%+v ratio=%v; document enabled=%v signals=%+v ratio=%v",
			ko.Enabled, ko.Signals, ko.Sampling.TracesRatio, do.Enabled, do.Signals, do.Sampling.TracesRatio)
	}
}

func TestDeclareEnv_DocumentIsASource(t *testing.T) {
	dir, cfg := monitoringProject(t, "name: shop\n")
	writeEnvDocument(t, dir, "prod.yaml", minimalMonitoringDocument)
	d, err := declareEnv(dir, cfg, "prod")
	if err != nil || d.Source != envDeclDocument {
		t.Fatalf("declaration = %+v, %v; want the document", d, err)
	}
	// The obs seam leaves it to the render, which reads the document.
	if _, ok, err := declaredObsTarget("prod"); ok || err != nil {
		t.Fatalf("declaredObsTarget = ok %v, %v", ok, err)
	}
}

func TestDeclareEnv_DocumentAndKCLIsALoadError(t *testing.T) {
	dir, cfg := monitoringProject(t, "name: shop\n", "prod")
	writeEnvDocument(t, dir, "prod.yaml", minimalMonitoringDocument)
	_, err := declareEnv(dir, cfg, "prod")
	if err == nil || !strings.Contains(err.Error(), "declared by both") {
		t.Fatalf("err = %v", err)
	}
	if _, err := RenderKCL(context.Background(), dir, "prod"); err == nil || !strings.Contains(err.Error(), "declared by both") {
		t.Fatalf("render err = %v", err)
	}
}

func TestDeclareEnv_DocumentAndForgeYAMLEnvsIsALoadError(t *testing.T) {
	dir, cfg := monitoringProject(t, "name: shop\nenvs: [prod]\nmonitoring: true\n")
	writeEnvDocument(t, dir, "prod.yaml", minimalMonitoringDocument)
	_, err := declareEnv(dir, cfg, "prod")
	if !errors.Is(err, errEnvDeclaredTwice) || !strings.Contains(err.Error(), "deploy/envs/prod.yaml") {
		t.Fatalf("err = %v", err)
	}
}

// Envs that send telemetry to different places cannot share one init
// (review B1 on #2216), in either argument order; agreeing ones can. A
// self-hosted endpoint is the destination whenever one is named, so one
// endpoint agrees with itself whatever control planes the declarations name
// (review B2 on #2217): a legacy forge.yaml monitoring block with an endpoint
// and a custom control plane, beside the endpoint-only document init writes
// for a fresh env.
func TestInitMonitoringNeedsOneDestination(t *testing.T) {
	cloudA := &config.MonitoringConfig{Enabled: true}
	cloudA2 := &config.MonitoringConfig{Enabled: true, ControlPlane: " "}
	cloudB := &config.MonitoringConfig{Enabled: true, ControlPlane: "https://cp.example"}
	self := &config.MonitoringConfig{Enabled: true, Endpoint: "https://otel.example"}
	selfOnCP := &config.MonitoringConfig{Enabled: true, Endpoint: "https://otel.example/", ControlPlane: "https://cp.example"}
	selfOther := &config.MonitoringConfig{Enabled: true, Endpoint: "https://otel2.example"}
	docs := func(a, b *config.MonitoringConfig) []initDecl {
		return []initDecl{
			{env: "prod", source: envDeclDocument, document: envDocumentDeclaration{Project: "x", Monitoring: a}},
			{env: "staging", source: envDeclDocument, document: envDocumentDeclaration{Project: "x", Monitoring: b}},
		}
	}
	for _, pair := range [][2]*config.MonitoringConfig{{cloudA, cloudA2}, {self, selfOnCP}, {selfOnCP, self}} {
		if _, err := initMonitoring(nil, docs(pair[0], pair[1])); err != nil {
			t.Errorf("%+v / %+v: one destination refused: %v", pair[0], pair[1], err)
		}
	}
	for _, pair := range [][2]*config.MonitoringConfig{{self, cloudA}, {cloudA, self}, {cloudA, cloudB}, {cloudB, cloudA}, {self, selfOther}, {selfOnCP, cloudB}} {
		_, err := initMonitoring(nil, docs(pair[0], pair[1]))
		if err == nil || !strings.Contains(err.Error(), "init them separately") || !strings.Contains(err.Error(), "staging") {
			t.Errorf("%+v / %+v: err = %v, want a refusal naming staging", pair[0], pair[1], err)
		}
	}

	// The legacy env reads forge.yaml's monitoring; the fresh env, the
	// endpoint-only document init wrote from it.
	legacy := &config.ProjectConfig{Monitoring: selfOnCP}
	mixed := []initDecl{
		{env: "prod", source: envDeclYAML},
		{env: "staging", source: envDeclDocument, document: envDocumentDeclaration{Project: "x", Monitoring: self}},
	}
	mon, err := initMonitoring(legacy, mixed)
	if err != nil {
		t.Fatalf("legacy endpoint + control plane beside a fresh endpoint-only document: %v", err)
	}
	if monitoringEndpoint(mon) != "https://otel.example" {
		t.Errorf("endpoint = %q", monitoringEndpoint(mon))
	}
}
