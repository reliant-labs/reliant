package naming

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestFrontendServiceNameAgreesWithTheWebRuntime reads the cases the
// web-runtime's frontendServiceName (web-runtime/src/service-name.ts) is
// tested against. A desktop app's main process derives its service.name in
// TypeScript, and the generated observability filters on this derivation, so
// the two spellings are held to one table.
func TestFrontendServiceNameAgreesWithTheWebRuntime(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "web-runtime", "src", "testdata", "frontend-service-names.json"))
	if err != nil {
		t.Fatalf("reading the shared cases: %v", err)
	}
	var cases []struct {
		Project  string `json:"project"`
		Frontend string `json:"frontend"`
		Service  string `json:"service"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("parsing the shared cases: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("no shared cases")
	}
	for _, c := range cases {
		if got := FrontendServiceName(c.Project, c.Frontend); got != c.Service {
			t.Errorf("FrontendServiceName(%q, %q) = %q, the web-runtime says %q", c.Project, c.Frontend, got, c.Service)
		}
	}
}
