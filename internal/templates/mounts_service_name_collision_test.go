package templates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A service named after one of forge's OWN imported packages must not break
// the generated mount file.
//
// mounts_services_gen.go imports forge/pkg/mountkit/inventory alongside one
// import per user service. Unaliased, both bind the identifier `inventory`,
// so `forge scaffold service inventory` — an ordinary name for an ordinary
// domain — emitted a file that could not compile:
//
//	internal/app/mounts_services_gen.go:37:2: inventory redeclared in this block
//	internal/app/mounts_services_gen.go:37:2: "…/internal/handlers/inventory" imported and not used
//
// The failure is at scaffold time, in generated code the author is told not
// to edit, so there is no move available except renaming the domain concept.
// Aliasing forge's own import removes the collision for every possible
// service name, not just this one.
func TestMountsServicesTemplate_AliasesForgeInventoryImport(t *testing.T) {
	path := filepath.Join("project", "mounts_services_gen.go.tmpl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	src := string(raw)

	const forgeImport = `"github.com/reliant-labs/forge/pkg/mountkit/inventory"`
	idx := strings.Index(src, forgeImport)
	if idx < 0 {
		t.Fatalf("template no longer imports mountkit/inventory; update this test")
	}

	// The import must carry an explicit alias — i.e. the token immediately
	// before it on that line is not just whitespace.
	lineStart := strings.LastIndex(src[:idx], "\n") + 1
	prefix := strings.TrimSpace(src[lineStart:idx])
	if prefix == "" {
		t.Error("forge's mountkit/inventory import is unaliased; a user service named " +
			"`inventory` binds the same identifier and the generated file will not compile")
	}

	// And nothing may still reference the package through the bare name.
	for _, bare := range []string{"[]inventory.ComponentInfo", "inventory.FindByName("} {
		if strings.Contains(src, bare) {
			t.Errorf("template still uses the unaliased selector %q", bare)
		}
	}
}
