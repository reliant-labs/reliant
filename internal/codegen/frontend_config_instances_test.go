package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func webFrontendConfig() FrontendConfig {
	return FrontendConfig{
		Frontend:    "web",
		MessageName: "WebConfig",
		Fields: []ConfigField{
			{Name: "api_url", ProtoType: "string", EnvVar: "NEXT_PUBLIC_API_URL", DefaultValue: "http://localhost:8080"},
			{Name: "environment", ProtoType: "string", EnvVar: "NEXT_PUBLIC_ENVIRONMENT", DefaultValue: "production"},
		},
	}
}

func writeConfigK(t *testing.T, kclDir, env, body string) string {
	t.Helper()
	dir := filepath.Join(kclDir, env)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.k")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const backendOnlyConfigK = `import config_gen

app_config: config_gen.AppConfig = {
    log_level = "debug"
}
`

// TestEnsureFrontendConfigInstances_AppendsToExisting is the case that
// actually happens: a project scaffolds its environments, and only later
// annotates a frontend config. The env's config.k already exists, so the
// write-if-absent scaffolder will never revisit it — the frontend instance
// has to be APPENDED or the runtime projection has nothing to project.
func TestEnsureFrontendConfigInstances_AppendsToExisting(t *testing.T) {
	kclDir := filepath.Join(t.TempDir(), "deploy", "kcl")
	path := writeConfigK(t, kclDir, "prod", backendOnlyConfigK)

	added, err := EnsureFrontendConfigInstances([]FrontendConfig{webFrontendConfig()}, kclDir, "prod", "acme")
	if err != nil {
		t.Fatalf("EnsureFrontendConfigInstances: %v", err)
	}
	if len(added) != 1 || added[0] != "web" {
		t.Fatalf("expected the web frontend to be added, got %v", added)
	}

	got := readFile(t, path)
	for _, want := range []string{
		"import " + FrontendConfigModule,
		"web_config: " + FrontendConfigModule + ".WebConfig = {",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config.k missing %q; got:\n%s", want, got)
		}
	}
	// The pre-existing backend half is preserved verbatim.
	if !strings.Contains(got, `app_config: config_gen.AppConfig = {`) ||
		!strings.Contains(got, `log_level = "debug"`) {
		t.Errorf("appending must not disturb the backend config; got:\n%s", got)
	}
}

// TestEnsureFrontendConfigInstances_Idempotent confirms a second run adds
// nothing — `forge generate` runs constantly, and an append that repeated
// would grow the file without bound and make the KCL ambiguous.
func TestEnsureFrontendConfigInstances_Idempotent(t *testing.T) {
	kclDir := filepath.Join(t.TempDir(), "deploy", "kcl")
	path := writeConfigK(t, kclDir, "prod", backendOnlyConfigK)

	configs := []FrontendConfig{webFrontendConfig()}
	if _, err := EnsureFrontendConfigInstances(configs, kclDir, "prod", "acme"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	first := readFile(t, path)

	added, err := EnsureFrontendConfigInstances(configs, kclDir, "prod", "acme")
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(added) != 0 {
		t.Errorf("second run must add nothing, got %v", added)
	}
	if got := readFile(t, path); got != first {
		t.Errorf("second run changed the file:\nbefore:\n%s\nafter:\n%s", first, got)
	}
}

// TestEnsureFrontendConfigInstances_RespectsAuthoredInstance confirms forge
// leaves an instance an author already wrote alone — including one bound
// through an aliased import under a different variable name, which is the
// style a real project uses.
func TestEnsureFrontendConfigInstances_RespectsAuthoredInstance(t *testing.T) {
	kclDir := filepath.Join(t.TempDir(), "deploy", "kcl")
	authored := `import frontend_config_gen as fcfg

my_web: fcfg.WebConfig = {
    api_url = "https://api.example.com"
}
`
	path := writeConfigK(t, kclDir, "prod", authored)

	added, err := EnsureFrontendConfigInstances([]FrontendConfig{webFrontendConfig()}, kclDir, "prod", "acme")
	if err != nil {
		t.Fatalf("EnsureFrontendConfigInstances: %v", err)
	}
	if len(added) != 0 {
		t.Errorf("an authored instance must not be duplicated, got %v", added)
	}
	if got := readFile(t, path); got != authored {
		t.Errorf("authored config.k was modified:\n%s", got)
	}
}

// TestEnsureFrontendConfigInstances_DevPinsMode pins the dev-environment
// rule: the field carrying the mode/environment ROLE is set to the dev
// value rather than inheriting a proto default authored for production.
// That default is exactly what made a dev bundle announce itself as
// "production".
func TestEnsureFrontendConfigInstances_DevPinsMode(t *testing.T) {
	kclDir := filepath.Join(t.TempDir(), "deploy", "kcl")
	fc := webFrontendConfig()
	fc.Fields[1].Role = configModeRole
	path := writeConfigK(t, kclDir, "dev", backendOnlyConfigK)

	if _, err := EnsureFrontendConfigInstances([]FrontendConfig{fc}, kclDir, "dev", "acme"); err != nil {
		t.Fatalf("EnsureFrontendConfigInstances: %v", err)
	}
	got := readFile(t, path)
	if !strings.Contains(got, "environment = \""+configDevModeValue+"\"") {
		t.Errorf("dev config.k must pin the mode field to %q; got:\n%s", configDevModeValue, got)
	}

	// A non-dev env inherits the proto default instead of pinning.
	prodPath := writeConfigK(t, kclDir, "prod", backendOnlyConfigK)
	if _, err := EnsureFrontendConfigInstances([]FrontendConfig{fc}, kclDir, "prod", "acme"); err != nil {
		t.Fatalf("prod: %v", err)
	}
	if strings.Contains(readFile(t, prodPath), "environment = ") {
		t.Errorf("a non-dev env must inherit the schema default, not pin it; got:\n%s", readFile(t, prodPath))
	}
}

// TestEnsureFrontendConfigInstances_NoConfigK is the fresh-project order:
// the backend scaffolder creates config.k in the same pass, so finding no
// file yet is normal and must not error.
func TestEnsureFrontendConfigInstances_NoConfigK(t *testing.T) {
	kclDir := filepath.Join(t.TempDir(), "deploy", "kcl")
	added, err := EnsureFrontendConfigInstances([]FrontendConfig{webFrontendConfig()}, kclDir, "prod", "acme")
	if err != nil {
		t.Fatalf("missing config.k must be a no-op, got: %v", err)
	}
	if len(added) != 0 {
		t.Errorf("nothing can be added to a file that does not exist, got %v", added)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
