package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// TestOpenAPIBufTemplateShape pins the synthesized buf.gen.openapi.yaml
// content. The template is small but load-bearing: the plugin name has
// to match the binary on PATH, the out: dir is what consumers find on
// disk, and the format= opt drives yaml vs json. Tightening the test
// here means future churn surfaces as a deliberate edit.
func TestOpenAPIBufTemplateShape(t *testing.T) {
	got := openapiBufTemplate("openapi")
	for _, want := range []string{
		"version: v2",
		"- local: protoc-gen-connect-openapi",
		"out: openapi",
		"format=yaml",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("openapiBufTemplate missing %q\ngot:\n%s", want, got)
		}
	}
}

// TestGateOpenAPIEnabled covers the three states that decide whether
// the OpenAPI step runs: no config (directory-scan fallback) → off,
// codegen disabled → off, flag false → off, flag true → on. The gate
// must NEVER mutate ctx — callers may invoke it many times.
func TestGateOpenAPIEnabled(t *testing.T) {
	tests := []struct {
		name string
		ctx  *pipelineContext
		want bool
	}{
		{
			name: "nil cfg (directory-scan)",
			ctx:  &pipelineContext{},
			want: false,
		},
		{
			name: "flag false",
			ctx: &pipelineContext{
				Cfg: &config.ProjectConfig{API: config.APIConfig{OpenAPI: false}},
			},
			want: false,
		},
		{
			name: "flag true",
			ctx: &pipelineContext{
				Cfg: &config.ProjectConfig{API: config.APIConfig{OpenAPI: true}},
			},
			want: true,
		},
		{
			name: "flag true but codegen disabled",
			ctx: func() *pipelineContext {
				return &pipelineContext{
					Cfg: &config.ProjectConfig{
						API:      config.APIConfig{OpenAPI: true},
						Features: config.FeaturesConfig{}.With(config.FeatureCodegen, false),
					},
				}
			}(),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gateOpenAPIEnabled(tt.ctx); got != tt.want {
				t.Errorf("gateOpenAPIEnabled(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// TestRunOpenAPIGenerateOffByDefault is the regression seat-belt for
// the "existing projects must regenerate identically" constraint. When
// cfg is nil or api.openapi is false, the call must short-circuit
// without touching disk (no openapi/ dir created, no plugin lookup).
func TestRunOpenAPIGenerateOffByDefault(t *testing.T) {
	dir := t.TempDir()

	// nil cfg
	if err := runOpenAPIGenerate(dir, nil); err != nil {
		t.Fatalf("runOpenAPIGenerate(nil) = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "openapi")); !os.IsNotExist(err) {
		t.Errorf("openapi/ dir should not exist when cfg is nil")
	}

	// cfg with flag false
	cfg := &config.ProjectConfig{API: config.APIConfig{OpenAPI: false}}
	if err := runOpenAPIGenerate(dir, cfg); err != nil {
		t.Fatalf("runOpenAPIGenerate(off) = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "openapi")); !os.IsNotExist(err) {
		t.Errorf("openapi/ dir should not exist when api.openapi is false")
	}
}

// TestRunOpenAPIGenerateMissingPluginGivesActionableError pins the
// error path: when the user opts in but hasn't installed the binary,
// the error message must include the exact install command. This is
// the highest-friction failure mode for a feature that's gated on a
// separate go install — a generic "fork/exec" error from buf would
// strand users.
func TestRunOpenAPIGenerateMissingPluginGivesActionableError(t *testing.T) {
	// Skip if the plugin happens to already be on PATH (CI runner
	// might have it pre-installed). The error path is the test
	// subject; happy path requires a real plugin invocation which
	// belongs in an e2e test.
	if isPluginAvailable(openAPIPluginBinary) {
		t.Skipf("%s is on PATH; this test exercises the missing-plugin error path", openAPIPluginBinary)
	}

	dir := t.TempDir()
	// Need at least one service dir with a proto so we get past the
	// discoverServiceProtoDirs no-op. Otherwise we'd hit the
	// "no services" silent return instead of the plugin-check error.
	svcDir := filepath.Join(dir, "proto", "services", "users")
	if err := os.MkdirAll(svcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svcDir, "users.proto"), []byte("syntax=\"proto3\";\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.ProjectConfig{API: config.APIConfig{OpenAPI: true}}
	err := runOpenAPIGenerate(dir, cfg)
	if err == nil {
		t.Fatal("expected error when plugin missing, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, openAPIPluginBinary) {
		t.Errorf("error message should name the plugin %q: %s", openAPIPluginBinary, msg)
	}
	if !strings.Contains(msg, "go install") {
		t.Errorf("error message should include the install command: %s", msg)
	}
}

// TestDiscoverServiceProtoDirs covers the canonical (proto/services/
// <svc>/v1/<svc>.proto) and flat (proto/services/<svc>/<svc>.proto)
// shapes. The walk has to find both so projects with either layout
// get specs emitted.
func TestDiscoverServiceProtoDirs(t *testing.T) {
	dir := t.TempDir()

	// Canonical layout: proto/services/users/v1/users.proto
	canon := filepath.Join(dir, "proto", "services", "users", "v1")
	if err := os.MkdirAll(canon, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(canon, "users.proto"), []byte("syntax=\"proto3\";\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Flat layout: proto/services/orders/orders.proto
	flat := filepath.Join(dir, "proto", "services", "orders")
	if err := os.MkdirAll(flat, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(flat, "orders.proto"), []byte("syntax=\"proto3\";\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Empty service dir (no protos) — must be skipped.
	if err := os.MkdirAll(filepath.Join(dir, "proto", "services", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := discoverServiceProtoDirs(dir)
	// Both layouts surface at the service-dir level — buf's --path is
	// recursive so users/ scopes to users/v1/users.proto, and orders/
	// scopes to orders/orders.proto. The plugin emits one yaml per
	// proto file regardless of intermediate directories.
	wantSubstrings := []string{"proto/services/orders", "proto/services/users"}
	for _, want := range wantSubstrings {
		found := false
		for _, p := range got {
			if p == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("discoverServiceProtoDirs did not return entry %q: got %v", want, got)
		}
	}
	for _, p := range got {
		if strings.Contains(p, "empty") {
			t.Errorf("discoverServiceProtoDirs returned the empty service dir: %v", got)
		}
	}
}

// TestWriteDefaultBufGenYamlMentionsOpenAPIOptIn is the discoverability
// test: the default buf.gen.yaml scaffolded by `forge generate` (the
// fallback when the user has no buf.gen.yaml) must point users at the
// opt-in flag in forge.yaml. Without this hint, users with `api.openapi:
// true` who edit buf.gen.yaml looking for the plugin wire-up would be
// confused.
func TestWriteDefaultBufGenYamlMentionsOpenAPIOptIn(t *testing.T) {
	dir := t.TempDir()
	if err := writeDefaultBufGenYaml(dir); err != nil {
		t.Fatalf("writeDefaultBufGenYaml: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "buf.gen.yaml"))
	if err != nil {
		t.Fatalf("read buf.gen.yaml: %v", err)
	}
	content := string(data)
	for _, want := range []string{"openapi", "protoc-gen-connect-openapi"} {
		if !strings.Contains(content, want) {
			t.Errorf("default buf.gen.yaml should mention %q in a comment so the opt-in flag is discoverable\n\n%s", want, content)
		}
	}
}
