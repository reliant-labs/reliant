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

// The managed-mode keys of a frontend whose code lives in another repo: the
// declaring project's runtime_config is the whole document, so a key the
// schema requires in the mode the document selects, and the document lacks
// (or leaves at the self-hosted default), is refused at render, naming each.
var cloudSchemaFields = []codegen.ConfigField{
	{Name: "hosting", EnvVar: "HOSTING", ProtoType: "string", AllowedValues: []string{"cloud", "self-hosted"}},
	{Name: "api_url", EnvVar: "API_URL", ProtoType: "string",
		RequiredWhen: []codegen.ConfigCondition{{Field: "hosting", Equals: "cloud"}}},
	{Name: "supabase_url", EnvVar: "SUPABASE_URL", ProtoType: "string", DefaultValue: "http://127.0.0.1:54321",
		RequiredWhen: []codegen.ConfigCondition{{Field: "hosting", Equals: "cloud"}}},
	{Name: "region", EnvVar: "REGION", ProtoType: "string", Required: true},
}

func TestCheckFrontendRuntimeConfigKeys_RefusesAMissingRequiredKey(t *testing.T) {
	t.Parallel()
	projectDir, rel := writeFrontendSchema(t, cloudSchemaFields...)
	err := checkFrontendRuntimeConfigKeys(projectDir, []FrontendEntity{{
		Name: "reliant-web", Path: rel,
		RuntimeConfig: map[string]string{"HOSTING": "cloud", "SUPABASE_URL": "http://127.0.0.1:54321"},
	}})
	if err == nil {
		t.Fatal("a cloud document without API_URL, with SUPABASE_URL at its default and without REGION was accepted")
	}
	msg := err.Error()
	for _, want := range []string{
		"Frontend 'reliant-web'.runtime_config is missing 3 keys",
		`API_URL (required when HOSTING is "cloud")`,
		`SUPABASE_URL (required when HOSTING is "cloud")`,
		"REGION (required)",
		"../reliant/web/src/lib/config_schema_gen.json",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal lacks %q:\n%s", want, msg)
		}
	}
}

// The prod-web incident of 2026-10-24: a hotfix branch's KCL still handed
// reliant-web its values as build-time env, so the cross-repo document was
// {OTEL_ENDPOINT} alone. With the mode selector absent no required_when
// condition holds, and the document read as a valid self-hosted one; the
// bundle booted self-hosted for every user. An unstated selector is refused.
func TestCheckFrontendRuntimeConfigKeys_RefusesADocumentThatStatesNoMode(t *testing.T) {
	t.Parallel()
	projectDir, rel := writeFrontendSchema(t, cloudSchemaFields...)
	for name, doc := range map[string]map[string]string{
		"the prod-web document": {"OTEL_ENDPOINT": "/_otel", "REGION": "eu"},
		"selector set to blank": {"HOSTING": " ", "REGION": "eu"},
	} {
		err := checkFrontendRuntimeConfigKeys(projectDir, []FrontendEntity{{
			Name: "reliant-web", Path: rel, RuntimeConfig: doc,
		}})
		if err == nil {
			t.Fatalf("%s: a document that states no HOSTING mode was accepted", name)
		}
		for _, want := range []string{
			"Frontend 'reliant-web'.runtime_config is missing 1 key",
			"HOSTING (unstated: it selects the mode that decides whether API_URL, SUPABASE_URL are required",
			`e.g. "cloud"`,
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: refusal lacks %q:\n%s", name, want, err)
			}
		}
	}
}

func TestCheckFrontendRuntimeConfigKeys_AcceptsTheModesItsDocumentMeets(t *testing.T) {
	t.Parallel()
	projectDir, rel := writeFrontendSchema(t, cloudSchemaFields...)
	ref := deployv1alpha1.RuntimeConfigValue{WorkloadURL: &deployv1alpha1.WorkloadURLRef{Name: "api"}}
	cloud := "cloud"
	for name, fe := range map[string]FrontendEntity{
		"self-hosted, stated":  {RuntimeConfig: map[string]string{"REGION": "eu", "HOSTING": "self-hosted"}},
		"cloud, every key set": {RuntimeConfig: map[string]string{"REGION": "eu", "HOSTING": "cloud", "API_URL": "https://api", "SUPABASE_URL": "https://sb"}},
		"cloud, API_URL another workload's URL": {
			RuntimeConfig: map[string]string{"REGION": "eu", "SUPABASE_URL": "https://sb"},
			RuntimeConfigSpec: map[string]deployv1alpha1.RuntimeConfigValue{
				"HOSTING": {Value: &cloud}, "API_URL": ref,
			},
		},
	} {
		fe.Name, fe.Path = "reliant-web", rel
		if err := checkFrontendRuntimeConfigKeys(projectDir, []FrontendEntity{fe}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// An in-tree frontend's document is its typed config.k values plus the
// runtime_config overlay, and its generated KCL schema checks required-ness:
// the overlay alone is not the document, so it is not judged here.
func TestCheckFrontendRuntimeConfigKeys_LeavesAnInTreeFrontendToItsKCLSchema(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	body, err := codegen.GenerateFrontendConfigSchema(codegen.FrontendConfig{Frontend: "web", MessageName: "WebConfig", Fields: cloudSchemaFields})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projectDir, "frontends", "web", filepath.FromSlash(codegen.FrontendConfigSchemaFile))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkFrontendRuntimeConfigKeys(projectDir, []FrontendEntity{{
		Name: "web", Path: "frontends/web", RuntimeConfig: map[string]string{"HOSTING": "cloud"},
	}}); err != nil {
		t.Errorf("in-tree overlay judged as the whole document: %v", err)
	}
}
