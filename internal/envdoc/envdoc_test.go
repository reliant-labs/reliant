package envdoc

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/schemagen"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

func writeDoc(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, Dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func loadOutput(t *testing.T, dir, env string) map[string]any {
	t.Helper()
	out, ok, err := Load(dir, env, Options{})
	if err != nil || !ok {
		t.Fatalf("Load(%s) = ok %v, err %v", env, ok, err)
	}
	var wrapper struct {
		Output map[string]any `json:"output"`
	}
	if err := json.Unmarshal(out, &wrapper); err != nil {
		t.Fatal(err)
	}
	return wrapper.Output
}

const monitoringOnly = `apiVersion: forge.dev/env/v1
project: shop
env: prod
control_plane: {}
observability:
  destination: {token_from_control_plane: true}
`

func TestMonitoringOnlyDocumentTakesTheSchemaDefaults(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "prod.yaml", monitoringOnly)
	out := loadOutput(t, dir, "prod")
	cp := out["control_plane"].(map[string]any)
	if cp["endpoint"] != cloud.DefaultEndpoint || cp["token_env"] != controlPlaneTokenEnv || cp["type"] != controlPlaneType {
		t.Errorf("control_plane = %v, want the ControlPlane schema defaults", cp)
	}
	o := out["observability"].(map[string]any)
	if o["enabled"] != true {
		t.Errorf("observability.enabled = %v, want true (the schema default)", o["enabled"])
	}
	if sig := o["signals"].(map[string]any); sig["traces"] != true || sig["replay"] != false {
		t.Errorf("observability.signals = %v", sig)
	}
}

func TestNoDocumentIsNotOK(t *testing.T) {
	if _, ok, err := Load(t.TempDir(), "prod", Options{}); ok || err != nil {
		t.Fatalf("Load with no document = ok %v, err %v", ok, err)
	}
}

func TestKCLAndDocumentForOneEnvIsALoadError(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "prod.yaml", monitoringOnly)
	if err := os.MkdirAll(filepath.Join(dir, "deploy", "kcl", "prod"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(KCLMain(dir, "prod"), []byte("output = {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(dir, "prod", Options{})
	if !errors.Is(err, ErrDeclaredTwice) || !strings.Contains(err.Error(), "deploy/kcl/prod/main.k") || !strings.Contains(err.Error(), "deploy/envs/prod.yaml") {
		t.Fatalf("err = %v, want ErrDeclaredTwice naming both files", err)
	}
}

func TestTwoDocumentsForOneEnvIsAnError(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "prod.yaml", monitoringOnly)
	writeDoc(t, dir, "prod.json", `{}`)
	if _, err := Path(dir, "prod"); err == nil || !strings.Contains(err.Error(), "prod.json") {
		t.Fatalf("Path = %v, want an error naming both", err)
	}
	if envs, _ := List(dir); !reflect.DeepEqual(envs, []string{"prod"}) {
		t.Fatalf("List = %v", envs)
	}
}

func TestValidationErrorsAreReadable(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "prod.yaml", `project: shop
env: staging
obserability: {enabled: true}
workloads:
  - name: api
    runtime: {type: kubernetes}
  - runtime: {type: external, stauts: ["./status.sh"]}
`)
	_, _, err := Load(dir, "prod", Options{})
	if err == nil {
		t.Fatal("an invalid document loaded")
	}
	for _, want := range []string{
		"deploy/envs/prod.yaml is not a valid env document",
		"apiVersion: required: an env document declares `apiVersion: forge.dev/env/v1`",
		`env: "staging", but the file declares env "prod"`,
		`obserability: unknown field (did you mean "observability"?)`,
		`workloads[0].runtime.type: "kubernetes" is not one of: build-only, cluster, compose, external, host, hosted`,
		"workloads[1].name: required, and missing",
		`workloads[1].runtime.stauts: unknown field (did you mean "status"?)`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}

func TestRecordDeploysDocumentLoads(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "prod.yaml", monitoringOnly+`workloads:
  - name: api
    runtime: {type: external}
  - name: worker
    kind: worker
    runtime: {type: external, status: ["./scripts/status.sh"]}
`)
	out := loadOutput(t, dir, "prod")
	ws := out["workloads"].([]any)
	worker := ws[1].(map[string]any)["spec"].(map[string]any)
	if worker["kind"] != "worker" {
		t.Errorf("the workload's kind did not reach its spec: %v", worker)
	}
	env := map[string]any{}
	for _, e := range worker["env"].([]any) {
		m := e.(map[string]any)
		env[m["name"].(string)] = m["value"]
	}
	if env["OTEL_SERVICE_NAME"] != "worker" || env["OTEL_RESOURCE_ATTRIBUTES"] != "deployment.environment.name=prod,service.namespace=shop" {
		t.Errorf("derived telemetry identity = %v", env)
	}
}

func TestClusterBuiltWorkloadIsRefused(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "prod.yaml", `apiVersion: forge.dev/env/v1
project: shop
env: prod
workloads:
  - name: api
    build: {type: go, cmd: ./cmd/api}
    runtime: {type: cluster, cluster: k3d-shop, namespace: shop}
`)
	_, _, err := Load(dir, "prod", Options{})
	if err == nil || !strings.Contains(err.Error(), "needs KCL in env schema v1") {
		t.Fatalf("err = %v", err)
	}
}

func TestPrebuiltClusterWorkloadGetsItsRecord(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "prod.yaml", `apiVersion: forge.dev/env/v1
project: shop
env: prod
workloads:
  - name: web
    runtime: {type: cluster, cluster: k3d-shop, namespace: shop}
    spec: {image: "nginx:1.27", ports: [{name: http, port: 80}]}
`)
	out := loadOutput(t, dir, "prod")
	ms := out["manifests"].([]any)
	if len(ms) != 1 {
		t.Fatalf("manifests = %v", ms)
	}
	rec := ms[0].(map[string]any)
	md := rec["metadata"].(map[string]any)
	labels := md["labels"].(map[string]any)
	if rec["kind"] != "Workload" || md["namespace"] != "shop" || labels["forge.dev/cluster"] != "k3d-shop" || labels["app.kubernetes.io/part-of"] != "shop" {
		t.Fatalf("record = %v", rec)
	}
}

func TestRuntimeMaskRefusesWhatTheRuntimeCannotHonour(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "dev.yaml", `apiVersion: forge.dev/env/v1
project: shop
env: dev
workloads:
  - name: api
    build: {type: go, cmd: ./cmd/api}
    runtime: {type: host}
    spec: {replicas: 2, storageGiB: 5}
  - name: redis
    runtime: {type: compose}
    spec: {ports: [{name: tcp, port: 6379}]}
`)
	_, _, err := Load(dir, "dev", Options{})
	for _, want := range []string{
		`workload "api" on the host runtime: storageGiB:`,
		`workload "api" on the host runtime: replicas: a host workload is one local process`,
		`workload "redis" on the compose runtime: ports: the compose file owns the published ports`,
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err lacks %q: %v", want, err)
		}
	}
}

func TestDanglingRequiresIsRefused(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "prod.yaml", monitoringOnly+`workloads:
  - name: api
    runtime: {type: external}
    requires: [db]
`)
	if _, _, err := Load(dir, "prod", Options{}); err == nil || !strings.Contains(err.Error(), `requires names "db"`) {
		t.Fatalf("err = %v", err)
	}
}

// TestMaskMatchesKCL reads render.k's three runtime-mask tables and holds the
// Go copies to the same field sets: KCL stays the source.
func TestMaskMatchesKCL(t *testing.T) {
	src, err := os.ReadFile("../../kcl/render.k")
	if err != nil {
		t.Fatal(err)
	}
	keysOf := func(table string) []string {
		re := regexp.MustCompile(`(?ms)^` + table + `: \{str:str\} = [^{]*\{\n(.*?)\n\}`)
		m := re.FindSubmatch(src)
		if m == nil {
			t.Fatalf("table %s not found in kcl/render.k", table)
		}
		var keys []string
		for _, km := range regexp.MustCompile(`(?m)^\s+"([A-Za-z]+)":`).FindAllSubmatch(m[1], -1) {
			keys = append(keys, string(km[1]))
		}
		sort.Strings(keys)
		return keys
	}
	goKeys := func(m map[string]string) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	if got, want := goKeys(localRefused), keysOf("_LOCAL_REFUSED"); !reflect.DeepEqual(got, want) {
		t.Errorf("localRefused = %v, KCL _LOCAL_REFUSED = %v", got, want)
	}
	composeOwn := map[string]string{}
	for k, v := range composeRefused {
		if _, local := localRefused[k]; !local {
			composeOwn[k] = v
		}
	}
	if got, want := goKeys(composeOwn), keysOf("_COMPOSE_REFUSED"); !reflect.DeepEqual(got, want) {
		t.Errorf("composeRefused (own) = %v, KCL _COMPOSE_REFUSED = %v", got, want)
	}
	if got, want := goKeys(buildOnlyRefused), keysOf("_BUILD_ONLY_REFUSED"); !reflect.DeepEqual(got, want) {
		t.Errorf("buildOnlyRefused = %v, KCL _BUILD_ONLY_REFUSED = %v", got, want)
	}
}

// TestLoweringRederivesEveryRenderContract strips what the Go lowering
// derives from every render-contract golden — the derived telemetry env, the
// default probes, the Workload records — re-derives it, and requires the KCL
// render's own values back. This is what keeps KCL the single source of each
// ported default.
func TestLoweringRederivesEveryRenderContract(t *testing.T) {
	files, _ := filepath.Glob("../cli/testdata/render_contract/*.json")
	if len(files) == 0 {
		t.Fatal("no render-contract goldens")
	}
	derived := map[string]bool{"OTEL_SERVICE_NAME": true, "OTEL_RESOURCE_ATTRIBUTES": true, "GOMEMLIMIT": true}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var wrapper struct {
				Output json.RawMessage `json:"output"`
			}
			if err := json.Unmarshal(raw, &wrapper); err != nil {
				t.Fatal(err)
			}
			v, err := schemagen.DecodeJSON(wrapper.Output)
			if err != nil {
				t.Fatal(err)
			}
			doc := v.(map[string]any)
			project, _ := doc["project"].(string)
			env, _ := doc["env"].(string)
			records := map[string]map[string]any{}
			for _, raw := range asList(doc["manifests"]) {
				m := raw.(map[string]any)
				if m["kind"] == "Workload" {
					records[m["metadata"].(map[string]any)["name"].(string)] = m
				}
			}
			for _, raw := range asList(doc["workloads"]) {
				w := raw.(map[string]any)
				name := w["name"].(string)
				spec := specOf(w)
				if spec == nil {
					continue
				}
				want := cloneJSON(t, spec)
				stripped := cloneJSON(t, spec)
				var kept []any
				for _, e := range asList(stripped["env"]) {
					if !derived[e.(map[string]any)["name"].(string)] {
						kept = append(kept, e)
					}
				}
				stripped["env"] = kept
				delete(stripped, "probes")
				kind := kindOf(w, stripped)
				deriveEnv(w, stripped, runtimeType(w), kind, project, env, "")
				defaultProbes(w, stripped, kind)
				if g, e := envByName(stripped), envByName(want); !reflect.DeepEqual(g, e) {
					t.Errorf("%s: re-derived env\n got %v\nwant %v", name, g, e)
				}
				if g, e := probesOf(t, stripped), probesOf(t, want); !reflect.DeepEqual(g, e) {
					t.Errorf("%s: re-derived probes\n got %+v\nwant %+v", name, g, e)
				}
				if rec, ok := records[name]; ok {
					got := workloadRecord(w, want, project, env, "")
					if g, e := jsonOf(t, got), jsonOf(t, rec); g != e {
						t.Errorf("%s: re-derived Workload record\n got %s\nwant %s", name, g, e)
					}
				}
			}
		})
	}
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func cloneJSON(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	v, err := schemagen.DecodeJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	return v.(map[string]any)
}

func envByName(spec map[string]any) map[string]string {
	out := map[string]string{}
	for _, e := range asList(spec["env"]) {
		m := e.(map[string]any)
		b, _ := json.Marshal(m)
		out[m["name"].(string)] = string(b)
	}
	return out
}

func probesOf(t *testing.T, spec map[string]any) *deployv1alpha1.Probes {
	t.Helper()
	p, ok := spec["probes"]
	if !ok || p == nil {
		return nil
	}
	b, _ := json.Marshal(p)
	var out deployv1alpha1.Probes
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return &out
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
