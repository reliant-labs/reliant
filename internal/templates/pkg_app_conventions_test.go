package templates_test

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// TestPkgAppConventionsDescribesTheCompositionGraph pins the scaffolded
// pkg/app/CONVENTIONS.md to the composition graph every new project is born
// on. It used to describe the legacy model (OpenInfra → NewComponents →
// mounts_services_gen.go + lifecycle.go) and a pkg/app/migrate.go that no
// longer exists, so an agent reading it in a graph project looked for files
// and functions the project does not have (forge friction #32, Ranger).
func TestPkgAppConventionsDescribesTheCompositionGraph(t *testing.T) {
	t.Parallel()
	raw, err := templates.ProjectTemplates().Get("pkg-app-CONVENTIONS.md.tmpl")
	if err != nil {
		t.Fatalf("read pkg-app-CONVENTIONS.md.tmpl: %v", err)
	}
	doc := string(raw)

	for _, want := range []string{
		"internal/app/providers.go",
		"internal/app/compose.go",
		"internal/app/graph_gen.go",
		"internal/app/routes_gen.go",
		"roots_gen.go",
		"//forge:root",
		"resolves by type",
		"validateDeps",
		"helpers_gen_test.go",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("pkg/app/CONVENTIONS.md must describe the composition graph; missing %q", want)
		}
	}

	// The legacy composition may be NAMED once, as what `forge fix
	// composition-graph` migrates; it must not be described as the model.
	for _, stale := range []string{
		"mounts_services_gen.go",
		"lifecycle.go",
		"serverkit.Run\n",
		"`migrate.go` (generated)",
		"`Components` field",
	} {
		if strings.Contains(doc, stale) {
			t.Errorf("pkg/app/CONVENTIONS.md still describes the legacy model: %q", stale)
		}
	}
	if n := strings.Count(doc, "NewComponents"); n > 1 {
		t.Errorf("pkg/app/CONVENTIONS.md names NewComponents %d times; only the one pointer to `forge fix composition-graph` belongs there", n)
	}
	if !strings.Contains(doc, "forge fix composition-graph") {
		t.Error("pkg/app/CONVENTIONS.md must point a pre-graph project at `forge fix composition-graph`")
	}
}
