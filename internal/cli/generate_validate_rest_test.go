package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/templates"
)

func writeRESTFixture(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// scaffoldedBufYAML is buf.yaml as forge scaffolds it with api.rest off: the
// googleapis dep is only a comment.
func scaffoldedBufYAML(t *testing.T, rest bool) string {
	t.Helper()
	out, err := templates.ProjectTemplates().Render("buf.yaml.tmpl", map[string]any{"RESTEnabled": rest})
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

const restTestProto = `syntax = "proto3";
package services.users.v1;
service UserService { rpc GetUser(GetUserRequest) returns (GetUserResponse); }
`

const restTestAnnotatedProto = `syntax = "proto3";
package services.users.v1;
import "google/api/annotations.proto";
service UserService {
  rpc GetUser(GetUserRequest) returns (GetUserResponse) {
    option (google.api.http) = { get: "/v1/users/{id}" };
  }
}
`

const restTestProviders = `package app

func NewAPIListener() {
	l, err := serverkit.NewListener(serverkit.ListenerDeps{
		Logger: d.Logger,
		REST:   %s,
	})
}
`

// obs 4e9ce6c3: api.rest: true on a project scaffolded with it off changed
// nothing, silently. Each unwired piece is named with its manual edit.
func TestRESTWiringWarnings_ExistingProjectNamesEachManualEdit(t *testing.T) {
	dir := t.TempDir()
	writeRESTFixture(t, dir, map[string]string{
		"buf.yaml":                            scaffoldedBufYAML(t, false),
		"internal/app/providers.go":           strings.Replace(restTestProviders, "%s", "false", 1),
		"proto/services/users/v1/users.proto": restTestProto,
	})
	cfg := &config.ProjectConfig{API: config.APIConfig{REST: true}}
	got := restWiringWarnings(dir, cfg, true)
	if len(got) != 3 {
		t.Fatalf("want 3 warnings (server, buf.yaml, annotations), got %d:\n%s", len(got), strings.Join(got, "\n"))
	}
	for i, want := range []string{"internal/app/providers.go:6 sets `REST: false`", "buf dep update", "google.api.http"} {
		if !strings.Contains(got[i], want) {
			t.Errorf("warning %d should contain %q: %s", i, want, got[i])
		}
	}

	// Off, or no services: nothing to say.
	if w := restWiringWarnings(dir, &config.ProjectConfig{}, true); w != nil {
		t.Errorf("api.rest off: want no warnings, got %v", w)
	}
	if w := restWiringWarnings(t.TempDir(), cfg, false); w != nil {
		t.Errorf("no services: want no warnings, got %v", w)
	}
	// Empty service metadata (the directory-scan fallback) is not "no
	// services" while proto/services/ holds a proto (review B2).
	if w := restWiringWarnings(dir, cfg, false); len(w) != 3 {
		t.Errorf("empty metadata with a service proto: want the 3 warnings, got %v", w)
	}
}

// The server check reads Go syntax in the PRIMARY server's files (review B1):
// a comment, another binary, or a RESTHandler result that never reaches the
// server's Handler does not count as REST being served.
func TestRESTServerWarning_ReadsThePrimaryServersWiring(t *testing.T) {
	providersOff := strings.Replace(restTestProviders, "%s", "false", 1)
	for _, tt := range []struct {
		name  string
		files map[string]string
		warn  bool
	}{
		{"REST: true only in a comment", map[string]string{
			"internal/app/providers.go": providersOff + "\n// REST: true once we are ready\n",
		}, true},
		{"another binary serves REST", map[string]string{
			"internal/app/providers.go": providersOff,
			"cmd/admin/cmd/serve.go":    "package cmd\n\nfunc serve() { srv.Handler = serverkit.RESTHandler(srv.Mux, m, l) }\n",
		}, true},
		{"a RESTHandler result that never reaches Handler", map[string]string{
			"cmd/app/cmd/serve.go": "package cmd\n\nfunc serve() { rest := serverkit.RESTHandler(srv.Mux, m, l); _ = rest }\n",
		}, true},
		{"REST from config is the project's choice", map[string]string{
			"internal/app/providers.go": strings.Replace(restTestProviders, "%s", "cfg.API.REST", 1),
		}, false},
		{"the primary binary assigns the handler directly", map[string]string{
			"cmd/app/cmd/serve.go": "package cmd\n\nfunc serve() { srv.Handler = serverkit.RESTHandler(srv.Mux, m, l) }\n",
		}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.files["forge.yaml"] = "name: app\n"
			writeRESTFixture(t, dir, tt.files)
			if w := restServerWarning(dir); (w != "") != tt.warn {
				t.Fatalf("warning = %q, want warn=%v", w, tt.warn)
			}
		})
	}
}

func TestRESTWiringWarnings_WiredProjectIsQuiet(t *testing.T) {
	dir := t.TempDir()
	writeRESTFixture(t, dir, map[string]string{
		"buf.yaml":                            scaffoldedBufYAML(t, true),
		"buf.lock":                            "version: v2\n",
		"internal/app/providers.go":           strings.Replace(restTestProviders, "%s", "true", 1),
		"proto/services/users/v1/users.proto": restTestAnnotatedProto,
	})
	cfg := &config.ProjectConfig{API: config.APIConfig{REST: true}}
	if got := restWiringWarnings(dir, cfg, true); len(got) != 0 {
		t.Fatalf("a wired project should get no warnings, got:\n%s", strings.Join(got, "\n"))
	}

	// The dep without its lock still cannot resolve.
	if err := os.Remove(filepath.Join(dir, "buf.lock")); err != nil {
		t.Fatal(err)
	}
	got := restWiringWarnings(dir, cfg, true)
	if len(got) != 1 || !strings.Contains(got[0], "no buf.lock") {
		t.Fatalf("want the buf.lock warning alone, got %v", got)
	}
}

// A project that predates the composition graph serves from
// cmd/<bin>/cmd/serve.go: REST is on only when it calls RESTHandler.
func TestRESTWiringWarnings_LegacyServeGo(t *testing.T) {
	for _, tt := range []struct {
		name, serve string
		warn        bool
	}{
		{"no REST swap", "package cmd\n\nfunc serve() { srv.Handler = srv.Mux }\n", true},
		{"REST swap", "package cmd\n\nfunc serve() {\n\tif rest := serverkit.RESTHandler(srv.Mux, mounted, logger); rest != nil {\n\t\tsrv.Handler = rest\n\t}\n}\n", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeRESTFixture(t, dir, map[string]string{"forge.yaml": "name: app\n", "cmd/app/cmd/serve.go": tt.serve})
			w := restServerWarning(dir)
			if got := w != ""; got != tt.warn {
				t.Fatalf("warning = %q, want warn=%v", w, tt.warn)
			}
			if tt.warn && !strings.Contains(w, "serverkit.RESTHandler(srv.Mux, mounted, logger)") {
				t.Errorf("legacy warning should name the edit: %s", w)
			}
		})
	}
}
