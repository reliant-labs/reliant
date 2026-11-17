package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// otel_gen.ts imports `apiBase` from the frontend's own api-base.ts when it
// identifies the signed-in user. That file is the app's, scaffolded once: one
// scaffolded before the current template exports `apiBaseUrl()` instead
// (control-plane's internal-console), and gating identity on the file merely
// existing made the regenerated otel_gen.ts fail the frontend's typecheck
// ("Module './api-base' has no exported member 'apiBase'").
//
// Mutation that fails it: gate IdentifySession on the file's existence again.
func TestWithSessionIdentity_RequiresTheAPIBaseExport(t *testing.T) {
	cases := map[string]struct {
		apiBase string
		want    bool
	}{
		"the current template's export":  {"export function apiBase(configured: string): string {\n  return configured;\n}\n", true},
		"an exported const":              {"export const apiBase = (configured: string) => configured;\n", true},
		"an older scaffold's apiBaseUrl": {"export function apiBaseUrl(): string {\n  return \"/api\";\n}\n", false},
		"a private apiBase":              {"function apiBase(configured: string): string { return configured; }\n", false},
		"no api-base.ts":                 {"", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			projectDir := t.TempDir()
			lib := filepath.Join(projectDir, "frontends", "web", "src", "lib")
			if err := os.MkdirAll(filepath.Join(lib, "auth"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(lib, "auth", "native-login.ts"), []byte("export {};\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if c.apiBase != "" {
				if err := os.WriteFile(filepath.Join(lib, "api-base.ts"), []byte(c.apiBase), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got := withSessionIdentity(projectDir, filepath.Join("frontends", "web"), templates.FrontendTemplateData{FrontendName: "web"})
			if got.IdentifySession != c.want {
				t.Errorf("IdentifySession = %v, want %v", got.IdentifySession, c.want)
			}
		})
	}
}
