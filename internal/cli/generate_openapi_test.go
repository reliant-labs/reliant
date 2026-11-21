package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
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
	got := openapiBufTemplate("openapi", "users.yaml")
	for _, want := range []string{
		"version: v2",
		"- local: protoc-gen-connect-openapi",
		"out: openapi",
		"format=yaml",
		"path=users.yaml",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("openapiBufTemplate missing %q\ngot:\n%s", want, got)
		}
	}
	// base= names a base-spec FILE; the plugin rejects any value that is
	// not a .yaml/.json path, so passing it failed every spec (obs 3923ec30).
	if strings.Contains(got, "base=") {
		t.Errorf("openapiBufTemplate must not pass base=:\n%s", got)
	}
}

func TestOpenAPISpecName(t *testing.T) {
	for in, want := range map[string]string{
		"proto/services/users":   "users.yaml",
		"proto/services/orders/": "orders.yaml",
	} {
		if got := openAPISpecName(in); got != want {
			t.Errorf("openAPISpecName(%q) = %q, want %q", in, got, want)
		}
	}
}

// stubOpenAPIPlugin points the plugin seams at a fake PATH and installer.
// onPath is the version of the plugin on PATH ("" = none); installErr is what
// the pinned install returns. It reports how many installs were attempted.
func stubOpenAPIPlugin(t *testing.T, onPath string, installErr error) *int {
	t.Helper()
	installs := 0
	oldLook, oldInstall, oldVer := openAPILookPath, installOpenAPIPluginFn, openAPIPluginVersionFor
	t.Cleanup(func() { openAPILookPath, installOpenAPIPluginFn, openAPIPluginVersionFor = oldLook, oldInstall, oldVer })
	openAPILookPath = func(string) (string, error) {
		if onPath == "" {
			return "", exec.ErrNotFound
		}
		return "/fake/bin/" + openAPIPluginBinary, nil
	}
	openAPIPluginVersionFor = func(context.Context, string) (string, bool) {
		return onPath, onPath == openAPIPluginPinnedVersion
	}
	installOpenAPIPluginFn = func(context.Context, string) (string, error) {
		installs++
		if installErr != nil {
			return "", installErr
		}
		return "/fake/cache", nil
	}
	return &installs
}

func TestResolveOpenAPIPlugin(t *testing.T) {
	ctx := context.Background()
	t.Run("pinned version on PATH is used without installing", func(t *testing.T) {
		installs := stubOpenAPIPlugin(t, openAPIPluginPinnedVersion, nil)
		dir, err := resolveOpenAPIPlugin(ctx)
		if err != nil || dir != "" || *installs != 0 {
			t.Fatalf("dir=%q err=%v installs=%d, want PATH build, no install", dir, err, *installs)
		}
	})
	t.Run("another version on PATH is replaced by the pinned one", func(t *testing.T) {
		installs := stubOpenAPIPlugin(t, "v0.25.0", nil)
		dir, err := resolveOpenAPIPlugin(ctx)
		if err != nil || dir != "/fake/cache" || *installs != 1 {
			t.Fatalf("dir=%q err=%v installs=%d, want the cache dir", dir, err, *installs)
		}
	})
	t.Run("install fails but PATH has a build: use it", func(t *testing.T) {
		stubOpenAPIPlugin(t, "v0.25.0", errors.New("offline"))
		dir, err := resolveOpenAPIPlugin(ctx)
		if err != nil || dir != "" {
			t.Fatalf("dir=%q err=%v, want the PATH build", dir, err)
		}
	})
	t.Run("install fails and nothing on PATH: actionable error", func(t *testing.T) {
		stubOpenAPIPlugin(t, "", errors.New("offline"))
		_, err := resolveOpenAPIPlugin(ctx)
		if err == nil {
			t.Fatal("want an error")
		}
		msg := err.Error()
		// The old sudorandom path no longer installs: the module declares
		// connectrpc.com (obs 3923ec30).
		want := "go install connectrpc.com/protoc-gen-connect-openapi@" + openAPIPluginPinnedVersion
		if !strings.Contains(msg, want) || strings.Contains(msg, "sudorandom") {
			t.Errorf("error should name %q (and not the retired sudorandom path): %s", want, msg)
		}
	})
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

// TestRunOpenAPIGenerateMissingPluginFails pins that a plugin forge cannot
// provision fails the step, and leaves no openapi/ dir behind to look like
// output.
func TestRunOpenAPIGenerateMissingPluginFails(t *testing.T) {
	stubOpenAPIPlugin(t, "", errors.New("offline"))
	dir := t.TempDir()
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
	if !strings.Contains(err.Error(), openAPIPluginInstallCmd) {
		t.Errorf("error message should include the install command: %s", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "openapi")); !os.IsNotExist(err) {
		t.Errorf("openapi/ must not be created when the plugin is unavailable")
	}
}

// TestRunOpenAPIGenerateEndToEnd runs the real buf and the pinned plugin
// (installed into forge's tool cache): a valid service gets
// openapi/<svc>.yaml, and a service buf cannot compile fails the step
// instead of being warned past under a success line (obs 3923ec30).
func TestRunOpenAPIGenerateEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("runs buf and go-installs protoc-gen-connect-openapi; runs in task test")
	}
	if _, err := exec.LookPath("buf"); err != nil {
		t.Skip("buf is not on PATH")
	}
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("buf.yaml", "version: v2\nmodules:\n  - path: proto\n")
	write("proto/services/users/v1/users.proto", `syntax = "proto3";
package services.users.v1;
message GetUserRequest { string id = 1; }
message GetUserResponse { string name = 1; }
service UserService { rpc GetUser(GetUserRequest) returns (GetUserResponse); }
`)
	cfg := &config.ProjectConfig{API: config.APIConfig{OpenAPI: true}}
	if err := runOpenAPIGenerate(dir, cfg); err != nil {
		t.Fatalf("runOpenAPIGenerate: %v", err)
	}
	spec, err := os.ReadFile(filepath.Join(dir, "openapi", "users.yaml"))
	if err != nil {
		t.Fatalf("spec not written: %v", err)
	}
	if !strings.Contains(string(spec), "UserService/GetUser") {
		t.Errorf("spec does not describe UserService.GetUser:\n%s", spec)
	}

	write("proto/services/orders/v1/orders.proto", "syntax = \"proto3\";\nmessage Broken { NoSuchType x = 1; }\n")
	err = runOpenAPIGenerate(dir, cfg)
	if err == nil || !strings.Contains(err.Error(), "proto/services/orders") {
		t.Fatalf("a service buf cannot compile must fail the step, naming it; got %v", err)
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
