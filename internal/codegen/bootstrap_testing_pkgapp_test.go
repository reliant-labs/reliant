package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

// GenerateBootstrapTesting writes each helper beside its component, never
// under pkg/app/, so it must not create pkg/app/. An empty pkg/app/ is
// service evidence to config.deriveProjectKindFromSources: reliant (every
// proto service `// forge:unmanaged`, so no handlers) derived as a CLI on its
// first `forge generate` and as a service on the second, which then
// scaffolded three more CI workflows. Generate has to be idempotent.
func TestGenerateBootstrapTesting_DoesNotCreatePkgApp(t *testing.T) {
	projectDir := t.TempDir()
	if err := GenerateBootstrapTesting(BootstrapTestingGenInput{
		GenContext: GenContext{ProjectDir: projectDir, ModulePath: "example.com/proj"},
	}); err != nil {
		t.Fatalf("GenerateBootstrapTesting() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "pkg", "app")); !os.IsNotExist(err) {
		t.Errorf("pkg/app/ was created (stat err = %v); nothing is written there, and its mere existence reads as a service project", err)
	}
}
