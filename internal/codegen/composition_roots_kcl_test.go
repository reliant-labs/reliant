package codegen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kcltest"
)

// twoRootFixture: AppConfig {port, database_url (required, sensitive),
// tick {interval}, stripe {secret_key, site_url}} and two roots — `worker`
// reaching the tick block, `server` reaching the database and stripe.
func twoRootFixture() ([]ConfigField, []GraphRoot) {
	messages := []ConfigMessage{
		{Name: "AppConfig", Fields: []ConfigField{
			{Name: "port", GoName: "Port", GoType: "int32", ProtoType: "int32", EnvVar: "PORT", DefaultValue: "8080"},
			{Name: "database_url", GoName: "DatabaseUrl", GoType: "string", ProtoType: "string", EnvVar: "DATABASE_URL", Sensitive: true, Required: true},
			{Name: "tick", GoName: "Tick", ProtoType: "message", MessageType: "TickConfig"},
			{Name: "stripe", GoName: "Stripe", ProtoType: "message", MessageType: "StripeConfig"},
		}},
		{Name: "TickConfig", Fields: []ConfigField{
			{Name: "interval", GoName: "Interval", GoType: "string", ProtoType: "string", EnvVar: "TICK_INTERVAL", DefaultValue: "1m"},
		}},
		{Name: "StripeConfig", Fields: []ConfigField{
			{Name: "secret_key", GoName: "SecretKey", GoType: "string", ProtoType: "string", EnvVar: "STRIPE_SECRET_KEY", Sensitive: true, Optional: true},
			{Name: "site_url", GoName: "SiteUrl", GoType: "string", ProtoType: "string", EnvVar: "STRIPE_SITE_URL", DefaultValue: "http://localhost:3000"},
		}},
	}
	port := ConfigLeaf{Path: "port", EnvVar: "PORT"}
	roots := []GraphRoot{
		{Name: "worker", Args: []string{"worker"}, Config: []ConfigLeaf{port, {Path: "tick.interval", EnvVar: "TICK_INTERVAL"}}},
		{Name: "server", Args: []string{"server"}, Config: []ConfigLeaf{
			port,
			{Path: "database_url", EnvVar: "DATABASE_URL", Sensitive: true, Required: true},
			{Path: "stripe.secret_key", EnvVar: "STRIPE_SECRET_KEY", Sensitive: true},
			{Path: "stripe.site_url", EnvVar: "STRIPE_SITE_URL"},
		}},
	}
	return FlattenBlockLeaves(messages, "AppConfig"), roots
}

func writeRootsKCLProject(t *testing.T, main string) string {
	t.Helper()
	fields, roots := twoRootFixture()
	module, err := GenerateConfigKCL(fields, "demo")
	if err != nil {
		t.Fatal(err)
	}
	return writeRootsKCLProjectModule(t, module+RootsKCL(roots), main)
}

// writeRootsKCLProjectModule writes a KCL project holding module as
// config_gen.k, a stub forge package and main.
func writeRootsKCLProjectModule(t *testing.T, module, main string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"kcl.mod":                 "[package]\nname = \"roots_proof\"\n",
		ConfigSchemaModule + ".k": module,
		"forge/kcl.mod":           "[package]\nname = \"forge\"\n",
		"forge/core.k":            "schema SecretRef:\n    name: str\n    key: str\n    optional?: bool\n    store_key?: str\n",
		"main.k":                  main,
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestRootsKCL_TwoRootsRenderDisjointEnv(t *testing.T) {
	_, roots := twoRootFixture()
	src := RootsKCL(roots)
	for _, want := range []string{
		`"server" = Root {`,
		`required_secrets = ["DATABASE_URL"]`,
		`secrets = ["DATABASE_URL", "STRIPE_SECRET_KEY"]`,
		`env = ["PORT", "TICK_INTERVAL"]`,
		"root_of = lambda name: str",
		"workloadEnvMap = lambda c: AppConfig, w: any",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("ROOTS lacks %q:\n%s", want, src)
		}
	}
	if RootsKCL(nil) != "" || RootsKCL([]GraphRoot{{Name: "x", Args: []string{"x"}}}) != "" {
		t.Error("no roots, or roots whose reach is unknown, project no ROOTS")
	}

	if _, err := exec.LookPath("kcl"); err != nil {
		t.Skip("kcl not on PATH; skipping the evaluation half")
	}
	dir := writeRootsKCLProject(t, `import config_gen

_c = config_gen.AppConfig {}
_worker = {name = "worker"} | config_gen.root_of("worker")
_api = {name = "api", config_secrets = []} | config_gen.root_of("server")
_legacy = {name = "legacy", config_secrets = ["DATABASE_URL"]}
worker_env = sorted([_k for _k in config_gen.workloadEnvMap(_c, _worker)])
api_env = sorted([_k for _k in config_gen.workloadEnvMap(_c, _api)])
legacy_env = sorted([_k for _k in config_gen.workloadEnvMap(_c, _legacy)])
worker_args = _worker.args
`)
	out, err := kcltest.Run(t.Context(), dir, "run", ".", "--format", "json")
	if err != nil {
		t.Fatalf("kcl run failed: %v\n%s", err, out)
	}
	var got struct {
		WorkerEnv  []string `json:"worker_env"`
		APIEnv     []string `json:"api_env"`
		LegacyEnv  []string `json:"legacy_env"`
		WorkerArgs []string `json:"worker_args"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if strings.Join(got.WorkerEnv, ",") != "PORT,TICK_INTERVAL" {
		t.Errorf("worker env = %v: exactly what the worker root reaches", got.WorkerEnv)
	}
	// The server root's required credential is projected; its optional one
	// is not, because the workload did not opt into it.
	if strings.Join(got.APIEnv, ",") != "DATABASE_URL,PORT,STRIPE_SITE_URL" {
		t.Errorf("api env = %v", got.APIEnv)
	}
	if strings.Join(got.WorkerArgs, ",") != "worker" {
		t.Errorf("root_of set args %v", got.WorkerArgs)
	}
	// The two roots' env, beyond the process field they share, is disjoint.
	shared := map[string]bool{}
	for _, k := range got.WorkerEnv {
		shared[k] = true
	}
	var both []string
	for _, k := range got.APIEnv {
		if shared[k] && k != "PORT" {
			both = append(both, k)
		}
	}
	if len(both) > 0 {
		t.Errorf("both roots' workloads get %v", both)
	}
	// A workload bound to no root keeps appConfigEnvMap: every value.
	sort.Strings(got.LegacyEnv)
	if strings.Join(got.LegacyEnv, ",") != "DATABASE_URL,PORT,STRIPE_SITE_URL,TICK_INTERVAL" {
		t.Errorf("legacy env = %v", got.LegacyEnv)
	}
}

func TestRootsKCL_RefusesAnUnknownRootAndAnUnreachedSecret(t *testing.T) {
	if _, err := exec.LookPath("kcl"); err != nil {
		t.Skip("kcl not on PATH")
	}
	for name, tc := range map[string]struct{ main, want string }{
		"unknown root": {
			main: "import config_gen\n\nw = {name = \"x\"} | config_gen.root_of(\"gateway\")\n",
			want: `no root named "gateway"`,
		},
		"unreached secret": {
			main: "import config_gen\n\n_w = {name = \"tick\", config_secrets = [\"DATABASE_URL\"]} | config_gen.root_of(\"worker\")\nenv = config_gen.workloadEnvMap(config_gen.AppConfig {}, _w)\n",
			want: `which root "worker" does not reach`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := kcltest.Run(t.Context(), writeRootsKCLProject(t, tc.main), "run", ".", "--format", "json")
			if err == nil || !strings.Contains(string(out)+err.Error(), tc.want) {
				t.Fatalf("render must fail naming %q: %v\n%s", tc.want, err, out)
			}
		})
	}
}

// Before generate has composed internal/app (a fresh scaffold, or one whose
// bootstrap generate failed) there are no providers to assemble, so the
// graph's reach is unknown: GraphRoots answers nil, and config_gen.k projects
// the default `server` root reaching every field, which is what a scaffold's
// server reaches. A graph assembled from that partial tree would give a
// server root that reaches no database, and the scaffold's own workloads,
// which opt into DATABASE_URL, would fail the render.
func TestGraphRoots_UnknownBeforeTheFirstCompose(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/fresh\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	roots, err := GraphRoots(dir)
	if err != nil || roots != nil {
		t.Fatalf("GraphRoots on a project with no internal/app/compose.go = %+v, %v; want nil, nil", roots, err)
	}
	kclDir := filepath.Join(dir, "deploy", "kcl")
	if err := os.MkdirAll(kclDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var fields []ConfigField
	for _, m := range DefaultConfigMessages() {
		fields = append(fields, m.Fields...)
	}
	if err := GenerateConfigNativeSharedRoots(fields, roots, "fresh", dir, kclDir, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(kclDir, ConfigSchemaModule+".k"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"workloadEnvMap = lambda", `"DATABASE_URL"`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("config_gen.k lacks %s: the default server root was not projected", want)
		}
	}
}
