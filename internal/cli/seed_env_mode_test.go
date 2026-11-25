package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// The classifier reads the MODE structurally from EVALUATED instances, so a
// field nested in a composed block counts — the shape the old line match
// could not see (reliant's `runtime = {environment = "dev"}`).
func TestEnvModeFindingsReadsNestedAndRoleFields(t *testing.T) {
	nested := map[string]map[string]any{
		"api_server_config": {"grpc_port": float64(21000), "auth_mode": "apikey"},
		"reliant_config":    {"runtime": map[string]any{"environment": "dev"}, "log": map[string]any{"level": ""}},
		// An unrelated, unset field of the same name (reliant's OTel label).
		"server_process_config": {"observability": map[string]any{"environment": ""}},
	}
	cases := []struct {
		name      string
		instances map[string]map[string]any
		rolePaths []string
		wantDev   bool
		wantIn    string
	}{
		{"nested block by name", nested, nil, true, `reliant_config.runtime.environment = "dev"`},
		{"nested non-dev by name", map[string]map[string]any{
			"reliant_config": {"runtime": map[string]any{"environment": "prod"}},
		}, nil, false, `reliant_config.runtime.environment = "prod"`},
		{"root field (scaffold shape)", map[string]map[string]any{
			"app_config": {"environment": "development", "log_level": "info"},
		}, nil, true, `app_config.environment = "development"`},
		{"schema default left unset is not dev", map[string]map[string]any{
			"app_config": {"environment": "production"},
		}, nil, false, `"production"`},
		{"every same-named field unset", map[string]map[string]any{
			"reliant_config":        {"runtime": map[string]any{"environment": ""}},
			"server_process_config": {"observability": map[string]any{"environment": ""}},
		}, nil, false, "sets no runtime MODE"},
		{"no mode field at all", map[string]map[string]any{
			"app_config": {"log_level": "info"},
		}, nil, false, "sets no runtime MODE"},
		// An annotated MODE field left empty is production, not "no reading".
		{"annotated mode unset is not dev", map[string]map[string]any{
			"app_config": {"tier": "", "environment": "development"},
		}, []string{"tier"}, false, `app_config.tier = ""`},
		{"configs disagreeing fail closed", map[string]map[string]any{
			"admin_config":  {"environment": "development"},
			"worker_config": {"environment": "production"},
		}, nil, false, `worker_config.environment = "production"`},
		// The annotation wins over the name: a renamed MODE field is found,
		// and an unannotated `environment` beside it is ignored.
		{"role path wins over name", map[string]map[string]any{
			"app_config": {"tier": "development", "environment": "production"},
		}, []string{"tier"}, true, `app_config.tier = "development"`},
		{"role path in a block", map[string]map[string]any{
			"app_config": {"base": map[string]any{"tier": "prod"}, "environment": "development"},
		}, []string{"base.tier"}, false, `app_config.base.tier = "prod"`},
		// A role path no instance holds leaves the name convention in force.
		{"unmatched role path falls back to name", nested, []string{"tier"}, true, "runtime.environment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := envModeVerdict("deploy/kcl/dev/config.k", envModeFindings(tc.instances, tc.rolePaths))
			if got.Dev != tc.wantDev {
				t.Errorf("Dev = %v, want %v (source: %s)", got.Dev, tc.wantDev, got.Source)
			}
			if !strings.Contains(got.Source, tc.wantIn) {
				t.Errorf("Source = %q, want it to name %q", got.Source, tc.wantIn)
			}
		})
	}
}

// modeRolePaths reads role=CONFIG_FIELD_ROLE_MODE off the descriptor: a root
// field by its name, a composed block's leaf as <block>.<field>.
func TestModeRolePathsFromDescriptor(t *testing.T) {
	configs := []codegen.ConfigMessage{
		{Name: "AppConfig", Fields: []codegen.ConfigField{
			{Name: "tier", ProtoType: "string", Role: "CONFIG_FIELD_ROLE_MODE"},
			{Name: "environment", ProtoType: "string"},
		}},
		{Name: "RuntimeConfig", Fields: []codegen.ConfigField{
			{Name: "environment", ProtoType: "string", Role: "CONFIG_FIELD_ROLE_MODE"},
		}},
		{Name: "ReliantConfig", Fields: []codegen.ConfigField{
			{Name: "runtime", ProtoType: "message", MessageType: "RuntimeConfig"},
		}},
	}
	got := strings.Join(modeRolePathsFrom(configs), ",")
	if want := "environment,runtime.environment,tier"; got != want {
		t.Errorf("modeRolePathsFrom = %q, want %q", got, want)
	}
	if got := modeRolePaths(t.TempDir()); got != nil {
		t.Errorf("no descriptor must yield no role paths, got %v", got)
	}
}

// A project forge cannot evaluate (no generated config schema) keeps the old
// text read, and the refusal says the file was read as text and why.
func TestClassifySeedEnvFallsBackToTextWithoutASchema(t *testing.T) {
	dir := t.TempDir()
	writeModeFixture(t, dir, map[string]string{
		"deploy/kcl/dev/config.k":  "app_config: config_gen.AppConfig = {\n    environment = \"development\"\n}\n",
		"deploy/kcl/prod/config.k": "app_config: config_gen.AppConfig = {\n    environment = \"production\"\n}\n",
	})
	if got := classifySeedEnv(dir, "dev"); !got.Dev || !strings.Contains(got.Source, "read as text") {
		t.Errorf("dev = %+v, want dev read as text", got)
	}
	got := classifySeedEnv(dir, "prod")
	if got.Dev {
		t.Fatalf("prod classified as dev: %+v", got)
	}
	for _, want := range []string{"deploy/kcl/prod/config.k", `environment = "production"`, "config_gen.k"} {
		if !strings.Contains(got.Source, want) {
			t.Errorf("prod source %q does not name %q", got.Source, want)
		}
	}
	if got := classifySeedEnv(dir, "missing"); got.Dev || !strings.Contains(got.Source, "does not exist") {
		t.Errorf("missing env = %+v, want refused naming the absent file", got)
	}
}

func TestClassifySeedEnvRejectsPathsBeforeReadingConfig(t *testing.T) {
	dir := t.TempDir()
	writeModeFixture(t, dir, map[string]string{
		"deploy/kcl/dev/config.k": `environment = "dev"`,
		"deploy/sibling/config.k": `environment = "dev"`,
	})
	for _, env := range []string{"", ".", "..", "../sibling", "dev/../dev", `..\sibling`, "/dev", `C:\dev`, filepath.Join(dir, "deploy", "kcl", "dev")} {
		if got := classifySeedEnv(dir, env); got.Dev || !strings.Contains(got.Source, "must be one directory name") {
			t.Errorf("classifySeedEnv(%q) = %+v, want refused before reading a config", env, got)
		}
	}
	if got := classifySeedEnv(dir, "dev"); !got.Dev {
		t.Errorf("literal dev env not accepted: %+v", got)
	}
}

// The regression: a typed config that nests the MODE in a composed block —
// reliant's `reliant_config: config_gen.ReliantConfig = { runtime =
// {environment = "dev"} }` — is dev once EVALUATED, where the line match
// refused it with `environment "dev" is not dev`. A nested non-dev value
// still refuses, as does a block left at its schema default.
func TestClassifySeedEnvEvaluatesNestedTypedConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	dir := t.TempDir()
	const schema = `schema ReliantConfigRuntime:
    environment: str = ""

schema ReliantConfig:
    runtime: ReliantConfigRuntime = ReliantConfigRuntime {}
    log_level: str = "info"

schema ApiServerConfig:
    grpc_port: int = 8080
`
	writeModeFixture(t, dir, map[string]string{
		"deploy/kcl/kcl.mod":        "[package]\nname = \"modefixture\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n",
		"deploy/kcl/config_gen.k":   schema,
		"deploy/kcl/dev/config.k":   "import config_gen\n\napi_server_config: config_gen.ApiServerConfig = {\n    grpc_port = 21000\n}\n\nreliant_config: config_gen.ReliantConfig = {\n    runtime = {environment = \"dev\"}\n}\n",
		"deploy/kcl/demo/config.k":  "import config_gen\n\nreliant_config: config_gen.ReliantConfig = {\n    runtime = {environment = \"prod\"}\n}\n",
		"deploy/kcl/plain/config.k": "import config_gen\n\nreliant_config: config_gen.ReliantConfig = {\n}\n",
	})

	// The line match is what refused dev before; pin that it still cannot
	// see the nested field, so this test proves the evaluation path.
	if _, ok := envModeFromKCLConfig(dir, "dev"); ok {
		t.Fatal("fixture no longer exercises the nested shape: the text match found it")
	}

	dev := classifySeedEnv(dir, "dev")
	if !dev.Dev {
		t.Fatalf("nested dev config refused: %s", dev.Source)
	}
	if !strings.Contains(dev.Source, `reliant_config.runtime.environment = "dev"`) || !strings.Contains(dev.Source, "evaluated") {
		t.Errorf("dev source %q must name the evaluated field and value", dev.Source)
	}
	if err := requireDevResetTargetIn(dir, "dev"); err != nil {
		t.Errorf("db reset gate refused nested dev: %v", err)
	}

	demo := classifySeedEnv(dir, "demo")
	if demo.Dev || !strings.Contains(demo.Source, `reliant_config.runtime.environment = "prod"`) {
		t.Errorf("nested prod config = %+v, want refused naming the value", demo)
	}
	err := requireDevResetTargetIn(dir, "demo")
	if err == nil || !strings.Contains(err.Error(), `reliant_config.runtime.environment = "prod"`) {
		t.Errorf("db reset gate on nested prod = %v, want a refusal naming what it read", err)
	}

	plain := classifySeedEnv(dir, "plain")
	if plain.Dev || !strings.Contains(plain.Source, "sets no runtime MODE") {
		t.Errorf("schema-default mode = %+v, want refused as setting no mode", plain)
	}
}

func writeModeFixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
