package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// frontendConfigProject lays out the minimal on-disk shape the frontend
// runtime document is rendered from: the forge-owned KCL module carrying
// the frontend's schema + runtime projection, and one per-env config.k
// holding that env's typed instance.
//
// It writes real KCL (rendered by the embedded kcl runtime, not a stub) so
// these tests exercise the same evaluation path `forge generate` and
// `forge env deploy` take.
func frontendConfigProject(t *testing.T, envValues map[string]string) string {
	t.Helper()
	projectDir := t.TempDir()

	kclDir := filepath.Join(projectDir, "deploy", "kcl")
	if err := os.MkdirAll(kclDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A bare package marker: kpm resolves the deploy/kcl package from it,
	// and nothing here imports the forge module, so no dependency is needed.
	writeFrontendCfgFile(t, filepath.Join(kclDir, "kcl.mod"), "[package]\nname = \"deploy\"\nversion = \"0.0.1\"\n")

	body, err := codegen.GenerateFrontendConfigKCL(
		[]codegen.FrontendConfig{testWebFrontendConfig()}, "demo")
	if err != nil {
		t.Fatalf("GenerateFrontendConfigKCL: %v", err)
	}
	writeFrontendCfgFile(t, filepath.Join(kclDir, codegen.FrontendConfigModule+".k"), body)

	for env, environment := range envValues {
		envDir := filepath.Join(kclDir, env)
		if err := os.MkdirAll(envDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// The env pins ONLY `environment`. `api_url` is deliberately left
		// unpinned so the proto default has to supply it.
		writeFrontendCfgFile(t, filepath.Join(envDir, "config.k"),
			"import "+codegen.FrontendConfigModule+" as fcfg\n\n"+
				"web_config: fcfg.WebConfig = {\n"+
				"    environment = \""+environment+"\"\n"+
				"}\n")
	}
	return projectDir
}

// testWebFrontendConfig is the proto-side declaration both tests share:
// one frontend ("web") with two fields, each carrying a proto default.
// `environment` defaults to "production" — the value the pre-fix generator
// wrote into the DEV document, which is the whole bug.
func testWebFrontendConfig() codegen.FrontendConfig {
	return codegen.FrontendConfig{
		Frontend:    "web",
		MessageName: "WebConfig",
		Fields: []codegen.ConfigField{
			{
				Name:         "api_url",
				ProtoType:    "string",
				EnvVar:       "NEXT_PUBLIC_API_URL",
				DefaultValue: "http://localhost:8080",
			},
			{
				Name:         "environment",
				ProtoType:    "string",
				EnvVar:       "NEXT_PUBLIC_ENVIRONMENT",
				DefaultValue: "production",
			},
		},
	}
}

func writeFrontendCfgFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestFrontendConfigJS_CarriesEnvKCLValues is the regression for the dev
// document rendering from PROTO DEFAULTS instead of the environment's own
// KCL.
//
// Before the fix, devRuntimeValues read only ConfigField.DefaultValue, so
// a dev environment that declared `environment = "dev"` in
// deploy/kcl/dev/config.k still got a config.js saying "production" — a
// silently wrong value in development, in a forge-OWNED file, so
// hand-correcting it does not survive the next `forge generate`.
//
// The contract: an environment's config.js reflects THAT environment's
// declared KCL values, with the proto default as the fallback for a field
// the env does not pin.
func TestFrontendConfigJS_CarriesEnvKCLValues(t *testing.T) {
	projectDir := frontendConfigProject(t, map[string]string{"dev": "dev"})

	cfg := &config.ProjectConfig{
		Name:      "demo",
		Frontends: []config.FrontendConfig{{Name: "web", Type: "nextjs"}},
	}
	messages := []codegen.ConfigMessage{{
		Name:     "WebConfig",
		Frontend: "web",
		Fields:   testWebFrontendConfig().Fields,
	}}

	if err := generateFrontendConfigModules(cfg, messages, projectDir, &checksums.FileChecksums{}); err != nil {
		t.Fatalf("generateFrontendConfigModules: %v", err)
	}

	js := readFrontendConfigJS(t, projectDir, "web")

	// The env PINS environment=dev. This is the assertion that fails
	// before the fix (the document says "production").
	if !strings.Contains(js, `"NEXT_PUBLIC_ENVIRONMENT": "dev"`) {
		t.Errorf("dev config.js must carry the env's KCL value environment=dev; got:\n%s", js)
	}
	if strings.Contains(js, `"NEXT_PUBLIC_ENVIRONMENT": "production"`) {
		t.Errorf("dev config.js still carries the PROTO DEFAULT environment=production; got:\n%s", js)
	}

	// The env does NOT pin api_url, so the proto default must still supply
	// it — the fallback half of the contract.
	if !strings.Contains(js, `"NEXT_PUBLIC_API_URL": "http://localhost:8080"`) {
		t.Errorf("an unpinned field must fall back to its proto default; got:\n%s", js)
	}
}

// TestFrontendConfigJS_FallsBackWithoutEnvKCL confirms the no-KCL path is
// unchanged: a project with no deploy/kcl/<env>/config.k (a fresh scaffold
// before any environment exists) still gets a working document rendered
// from the proto's own defaults. The fix must not make `forge generate`
// depend on an environment being present.
func TestFrontendConfigJS_FallsBackWithoutEnvKCL(t *testing.T) {
	projectDir := t.TempDir() // no deploy/kcl at all

	cfg := &config.ProjectConfig{
		Name:      "demo",
		Frontends: []config.FrontendConfig{{Name: "web", Type: "nextjs"}},
	}
	messages := []codegen.ConfigMessage{{
		Name:     "WebConfig",
		Frontend: "web",
		Fields:   testWebFrontendConfig().Fields,
	}}

	if err := generateFrontendConfigModules(cfg, messages, projectDir, &checksums.FileChecksums{}); err != nil {
		t.Fatalf("generateFrontendConfigModules with no env KCL: %v", err)
	}

	js := readFrontendConfigJS(t, projectDir, "web")
	if !strings.Contains(js, `"NEXT_PUBLIC_ENVIRONMENT": "production"`) {
		t.Errorf("with no env KCL the proto default must apply; got:\n%s", js)
	}
}

// The key schema is written beside config_gen.ts by the same step, so a
// project declaring this frontend from another repo can check its
// runtime_config keys against it (checkFrontendRuntimeConfigKeys).
func TestGenerateFrontendConfigModules_WritesTheKeySchema(t *testing.T) {
	projectDir := t.TempDir()
	cfg := &config.ProjectConfig{
		Name:      "demo",
		Frontends: []config.FrontendConfig{{Name: "web", Type: "nextjs"}},
	}
	messages := []codegen.ConfigMessage{{Name: "WebConfig", Frontend: "web", Fields: testWebFrontendConfig().Fields}}
	if err := generateFrontendConfigModules(cfg, messages, projectDir, &checksums.FileChecksums{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(projectDir, "frontends", "web", filepath.FromSlash(codegen.FrontendConfigSchemaFile)))
	if err != nil {
		t.Fatalf("schema not written: %v", err)
	}
	schema, ok, err := codegen.ParseFrontendConfigSchema(data)
	if err != nil || !ok {
		t.Fatalf("parse schema: ok=%v err=%v\n%s", ok, err, data)
	}
	if schema.Frontend != "web" || schema.Message != "WebConfig" {
		t.Errorf("schema names %q/%q, want web/WebConfig", schema.Frontend, schema.Message)
	}
	names := map[string]string{}
	for _, k := range schema.Keys {
		names[k.Name] = k.Source
	}
	for _, f := range testWebFrontendConfig().Fields {
		key := f.EnvVar
		if key == "" {
			key = f.Name
		}
		if names[key] != codegen.FrontendConfigKeySourceConfig {
			t.Errorf("config field %s is not in the schema as a config key: %v", key, names)
		}
	}
}

func readFrontendConfigJS(t *testing.T, projectDir, frontend string) string {
	t.Helper()
	path := filepath.Join(projectDir, "frontends", frontend, "public", codegen.FrontendConfigJSFile)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}
