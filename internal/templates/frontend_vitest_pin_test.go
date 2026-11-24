package templates

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Keep this deliberate: Vitest 3 brings vulnerable tinypool; Vitest 4's
// peer resolution crashes npm 10. Coordinate a future major with Vite and
// plugin-react, then run both scaffolded frontend suites before changing it.
func TestFrontendVitestMajor(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"nextjs", "vite-spa"} {
		t.Run(kind, func(t *testing.T) {
			raw, err := templateFS.ReadFile("frontend/" + kind + "/package.json.tmpl")
			if err != nil {
				t.Fatal(err)
			}
			// Strip the optional workspace dependency block before decoding JSON.
			text := string(raw)
			start := strings.Index(text, "{{- if .Workspaces}}")
			end := strings.Index(text, "{{- end}}")
			if start >= 0 && end > start {
				text = text[:start] + text[end+len("{{- end}}"):]
			}
			var pkg struct {
				DevDependencies map[string]string            `json:"devDependencies"`
				Overrides       map[string]map[string]string `json:"overrides"`
			}
			if err := json.Unmarshal([]byte(text), &pkg); err != nil {
				t.Fatal(err)
			}
			if got := pkg.DevDependencies["vitest"]; got != "^5.0.4" {
				t.Fatalf("vitest = %q; deliberate tested major/floor is ^5.0.4", got)
			}
			settings, err := templateFS.ReadFile("frontend/shared-web/pnpm-workspace.yaml.tmpl")
			if err != nil {
				t.Fatal(err)
			}
			var ws struct {
				Overrides map[string]string `yaml:"overrides"`
			}
			if err := yaml.Unmarshal(settings, &ws); err != nil {
				t.Fatal(err)
			}
			if got := ws.Overrides["@hyperdx/otel-web-session-recorder>fflate"]; got != "^0.7.5" {
				t.Fatalf("recorder fflate override = %q; need patched 0.7 line (GHSA-px8p-9vwx-vf98)", got)
			}
		})
	}
}

// TestFrontendZodPin holds the scaffolded frontends at a zod with the fix for
// GHSA-mrhx-c7xc-m3w5 (high; every zod below 4.6.6, no 3.x fix), and the
// web frontends at a resolver and react-hook-form that take zod 4. The
// generated forms are written for zod 4 with @hookform/resolvers 5 or later
// (codegen.FormDialect's zero value); moving a pin off those majors moves
// the forms with it.
func TestFrontendZodPin(t *testing.T) {
	t.Parallel()
	want := map[string]map[string]string{
		"nextjs":       {"zod": "^4.6.6", "@hookform/resolvers": "^7.0.7", "react-hook-form": "^7.55.0"},
		"vite-spa":     {"zod": "^4.6.6", "@hookform/resolvers": "^7.0.7", "react-hook-form": "^7.55.0"},
		"react-native": {"zod": "^4.6.6"},
	}
	for kind, deps := range want {
		t.Run(kind, func(t *testing.T) {
			raw, err := templateFS.ReadFile("frontend/" + kind + "/package.json.tmpl")
			if err != nil {
				t.Fatal(err)
			}
			text := string(raw)
			if start, end := strings.Index(text, "{{- if .Workspaces}}"), strings.Index(text, "{{- end}}"); start >= 0 && end > start {
				text = text[:start] + text[end+len("{{- end}}"):]
			}
			var pkg struct {
				Dependencies map[string]string `json:"dependencies"`
			}
			if err := json.Unmarshal([]byte(text), &pkg); err != nil {
				t.Fatal(err)
			}
			for name, version := range deps {
				if got := pkg.Dependencies[name]; got != version {
					t.Errorf("%s = %q, want %q", name, got, version)
				}
			}
		})
	}
}
