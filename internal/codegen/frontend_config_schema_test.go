package codegen

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestGenerateFrontendConfigSchema(t *testing.T) {
	fc := FrontendConfig{Frontend: "web", MessageName: "WebConfig", Fields: []ConfigField{
		{Name: "supabase_url", EnvVar: "SUPABASE_URL", ProtoType: "string"},
		{Name: "sample_rate", EnvVar: "SAMPLE_RATE", ProtoType: "double"},
		{Name: "auto_sign_in", EnvVar: "AUTO_SIGN_IN", ProtoType: "bool"},
		{Name: "api_url", EnvVar: "API_URL", ProtoType: "string"}, // also a runtime key: listed once, as config
		{Name: "auth", MessageType: "AuthConfig"},                 // a block reference, not a key
	}}
	body, err := GenerateFrontendConfigSchema(fc)
	if err != nil {
		t.Fatal(err)
	}
	doc, ok, err := ParseFrontendConfigSchema([]byte(body))
	if err != nil || !ok {
		t.Fatalf("round trip: ok=%v err=%v\n%s", ok, err, body)
	}
	if doc.Version != FrontendConfigSchemaVersion || doc.Frontend != "web" || doc.Message != "WebConfig" {
		t.Errorf("header = %+v", doc)
	}
	got := map[string]FrontendConfigSchemaKey{}
	var order []string
	for _, k := range doc.Keys {
		if _, dup := got[k.Name]; dup {
			t.Errorf("key %s listed twice", k.Name)
		}
		got[k.Name] = k
		order = append(order, k.Name)
	}
	if !sort.StringsAreSorted(order) {
		t.Errorf("keys not sorted (the file must be byte-stable): %v", order)
	}
	want := map[string][2]string{
		"SUPABASE_URL": {"string", FrontendConfigKeySourceConfig},
		"SAMPLE_RATE":  {"number", FrontendConfigKeySourceConfig},
		"AUTO_SIGN_IN": {"boolean", FrontendConfigKeySourceConfig},
		"API_URL":      {"string", FrontendConfigKeySourceConfig},
	}
	for _, k := range FrontendRuntimeKeys {
		if _, ok := want[k]; !ok {
			want[k] = [2]string{"string", FrontendConfigKeySourceRuntime}
		}
	}
	if len(got) != len(want) {
		t.Errorf("keys = %v, want %v", order, want)
	}
	for name, w := range want {
		if g := got[name]; g.Type != w[0] || g.Source != w[1] {
			t.Errorf("%s = %+v, want type %s source %s", name, g, w[0], w[1])
		}
	}
	if _, ok := got["auth"]; ok {
		t.Error("a block reference was emitted as a key")
	}
}

func TestParseFrontendConfigSchema_NewerVersionIsNotInterpreted(t *testing.T) {
	_, ok, err := ParseFrontendConfigSchema([]byte(`{"version": 2, "keys": [{"name": "X"}]}`))
	if err != nil || ok {
		t.Fatalf("newer version: ok=%v err=%v, want ok=false and no error", ok, err)
	}
}

// FrontendRuntimeKeys is what forge's generated frontend code reads from
// window.__FORGE_CONFIG__ outside config_gen.ts, plus what KCL's render
// derives into runtime_config. Read off the readers themselves so a key one
// of them starts reading cannot be missing from the schema every frontend
// is checked against.
func TestFrontendRuntimeKeysMatchTheGeneratedReaders(t *testing.T) {
	root := filepath.Join("..", "..")
	read := func(rel string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	found := map[string]bool{"ENVIRONMENT": true} // every rendered document carries it (withRuntimeEnvironment)
	cfgRead := regexp.MustCompile(`\bcfg\.([A-Z][A-Z0-9_]*)\b`)
	for _, rel := range []string{
		"internal/templates/frontend/vite-spa/src/lib/otel_gen.ts.tmpl",
		"internal/templates/frontend/nextjs/src/lib/otel_gen.ts.tmpl",
	} {
		for _, m := range cfgRead.FindAllStringSubmatch(read(rel), -1) {
			found[m[1]] = true
		}
	}
	direct := regexp.MustCompile(`__FORGE_CONFIG__\?\.([A-Z][A-Z0-9_]*)`)
	for _, m := range direct.FindAllStringSubmatch(read("internal/templates/frontend/shared-web/src/lib/auth/native-login.ts.tmpl"), -1) {
		found[m[1]] = true
	}
	// render.k's _telemetry_runtime_config lambda: the keys KCL adds to a
	// frontend's runtime_config on its own.
	renderK := read("kcl/render.k")
	start := strings.Index(renderK, "_telemetry_runtime_config = lambda")
	if start < 0 {
		t.Fatal("kcl/render.k no longer defines _telemetry_runtime_config; update this test")
	}
	body := renderK[start:]
	if end := strings.Index(body, "\n}"); end > 0 {
		body = body[:end]
	}
	for _, m := range regexp.MustCompile(`\{([A-Z][A-Z0-9_]*) = `).FindAllStringSubmatch(body, -1) {
		found[m[1]] = true
	}

	var got []string
	for k := range found {
		got = append(got, k)
	}
	sort.Strings(got)
	want := append([]string(nil), FrontendRuntimeKeys...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("generated readers use %v, FrontendRuntimeKeys = %v — keep them equal", got, want)
	}
}
