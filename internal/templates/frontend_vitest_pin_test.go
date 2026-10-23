package templates

import (
	"encoding/json"
	"strings"
	"testing"
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
			if got := pkg.Overrides["@hyperdx/otel-web-session-recorder"]["fflate"]; got != "^0.7.5" {
				t.Fatalf("recorder fflate override = %q; need patched 0.7 line (GHSA-px8p-9vwx-vf98)", got)
			}
		})
	}
}
