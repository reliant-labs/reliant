package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// writeFrontendSchema lays out a sibling frontend repo holding the schema its
// own forge generated, and returns the declaring project's directory.
func writeFrontendSchema(t *testing.T, fields ...codegen.ConfigField) (projectDir, frontendRel string) {
	t.Helper()
	root := t.TempDir()
	projectDir = filepath.Join(root, "control-plane")
	feDir := filepath.Join(root, "reliant", "web")
	body, err := codegen.GenerateFrontendConfigSchema(codegen.FrontendConfig{Frontend: "web", MessageName: "WebConfig", Fields: fields})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(feDir, filepath.FromSlash(codegen.FrontendConfigSchemaFile))
	for _, d := range []string{projectDir, filepath.Dir(path)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return projectDir, "../reliant/web"
}

var webSchemaFields = []codegen.ConfigField{
	{Name: "supabase_url", EnvVar: "SUPABASE_URL", ProtoType: "string"},
	{Name: "download_base_url", EnvVar: "DOWNLOAD_BASE_URL", ProtoType: "string"},
}

// The cross-repo typo: control-plane sets a key reliant's web never reads.
// It used to render into config.js and be ignored; now the render refuses
// it, naming the key, the near miss and the schema it was checked against.
func TestCheckFrontendRuntimeConfigKeys_RefusesAKeyTheFrontendDoesNotRead(t *testing.T) {
	t.Parallel()
	projectDir, rel := writeFrontendSchema(t, webSchemaFields...)
	err := checkFrontendRuntimeConfigKeys(projectDir, []FrontendEntity{{
		Name: "reliant-web", Path: rel,
		RuntimeConfig: map[string]string{"SUPABASE_URL": "https://x", "SUPABSE_ANON_KEY": "k", "API_URL": "https://api"},
	}})
	if err == nil {
		t.Fatal("a runtime_config key absent from the frontend's schema was accepted")
	}
	msg := err.Error()
	for _, want := range []string{
		"Frontend 'reliant-web'.runtime_config sets `SUPABSE_ANON_KEY`",
		"../reliant/web/src/lib/config_schema_gen.json",
		"WebConfig",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "`SUPABASE_URL`") || strings.Contains(msg, "`API_URL`") {
		t.Errorf("declared keys were reported as unknown:\n%s", msg)
	}
}

func TestCheckFrontendRuntimeConfigKeys_SuggestsTheNearMiss(t *testing.T) {
	t.Parallel()
	projectDir, rel := writeFrontendSchema(t, webSchemaFields...)
	err := checkFrontendRuntimeConfigKeys(projectDir, []FrontendEntity{{
		Name: "reliant-web", Path: rel,
		RuntimeConfigSpec: map[string]deployv1alpha1.RuntimeConfigValue{"DOWNLOAD_BASE_UR": {}},
	}})
	if err == nil || !strings.Contains(err.Error(), "DOWNLOAD_BASE_UR: did you mean DOWNLOAD_BASE_URL?") {
		t.Fatalf("want a did-you-mean for the hosted spec's key, got %v", err)
	}
}

// Keys forge's own generated code reads — the telemetry knobs render.k
// derives, ENVIRONMENT, API_URL — are valid for every frontend, including
// one whose schema came from an older forge that did not list them.
func TestCheckFrontendRuntimeConfigKeys_AllowsForgesRuntimeKeys(t *testing.T) {
	t.Parallel()
	projectDir, rel := writeFrontendSchema(t, webSchemaFields...)
	schema := filepath.Join(projectDir, rel, filepath.FromSlash(codegen.FrontendConfigSchemaFile))
	old := `{"version": 1, "frontend": "web", "message": "WebConfig", "keys": [{"name": "SUPABASE_URL", "type": "string", "source": "config"}]}`
	if err := os.WriteFile(schema, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	rc := map[string]string{"SUPABASE_URL": "x"}
	for _, k := range codegen.FrontendRuntimeKeys {
		rc[k] = "v"
	}
	if err := checkFrontendRuntimeConfigKeys(projectDir, []FrontendEntity{{Name: "reliant-web", Path: rel, RuntimeConfig: rc}}); err != nil {
		t.Fatalf("forge's runtime keys were refused: %v", err)
	}
}

// Nothing to prove wrong, nothing refused: no schema (no config message, or
// a frontend repo whose forge predates it), a schema format newer than this
// forge reads, or a source pin whose code is not on disk yet.
func TestCheckFrontendRuntimeConfigKeys_SkipsWhatItCannotCheck(t *testing.T) {
	t.Parallel()
	rc := map[string]string{"ANYTHING": "x"}

	if err := checkFrontendRuntimeConfigKeys(t.TempDir(), []FrontendEntity{{Name: "web", Path: "frontends/web", RuntimeConfig: rc}}); err != nil {
		t.Errorf("no schema: %v", err)
	}
	if err := checkFrontendRuntimeConfigKeys(t.TempDir(), []FrontendEntity{{Name: "web", RuntimeConfig: rc}}); err != nil {
		t.Errorf("unmaterialized source pin: %v", err)
	}

	projectDir, rel := writeFrontendSchema(t, webSchemaFields...)
	schema := filepath.Join(projectDir, rel, filepath.FromSlash(codegen.FrontendConfigSchemaFile))
	if err := os.WriteFile(schema, []byte(`{"version": 99, "keys": []}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkFrontendRuntimeConfigKeys(projectDir, []FrontendEntity{{Name: "web", Path: rel, RuntimeConfig: rc}}); err != nil {
		t.Errorf("newer schema version: %v", err)
	}

	if err := os.WriteFile(schema, []byte(`not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkFrontendRuntimeConfigKeys(projectDir, []FrontendEntity{{Name: "web", Path: rel, RuntimeConfig: rc}}); err == nil ||
		!strings.Contains(err.Error(), "cannot read its config schema") {
		t.Errorf("a corrupt schema must be reported, got %v", err)
	}
}
