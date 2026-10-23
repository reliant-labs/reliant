package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDiscoverPackages_HonorsExcludeContractDirective guards the wiring of
// the per-package `//forge:exclude-contract` header into the bootstrap /
// Build-injector component discovery walk (discoverPackages).
//
// The directive already excludes a package from the mock / middleware walk
// (generate_middleware.go). It MUST also exclude it from discoverPackages —
// otherwise a header-only exclude would drop the mock yet still feed a
// (typically non-Service-shaped) package into the type-topological injector,
// which would emit an uncompilable pkg.New(pkg.Deps{}) node. This is the
// per-package equivalent of a forge.yaml contracts.exclude entry; both
// sources must produce the IDENTICAL excluded set.
func TestDiscoverPackages_HonorsExcludeContractDirective(t *testing.T) {
	projectDir := t.TempDir()
	internalDir := filepath.Join(projectDir, "internal")

	// A normal contract-shaped package: stays in the component set.
	writePkg(t, filepath.Join(internalDir, "widget"), "contract.go", `// Package widget is a normal contract-shaped component.
package widget

type Service interface{ Do() }
type Deps struct{}
func New(Deps) Service { return nil }
`)

	// A package carrying the exclude-contract header: must be dropped from
	// the component set even though it has a contract.go.
	writePkg(t, filepath.Join(internalDir, "natsioish"), "contract.go", `//forge:exclude-contract

// Package natsioish is a utility-shaped package opted out via header.
package natsioish

type EventPublisher interface{ Publish() }
`)

	names, err := discoverPackages(projectDir)
	if err != nil {
		t.Fatalf("discoverPackages: %v", err)
	}

	got := map[string]bool{}
	for _, p := range names {
		got[p.ImportPath] = true
	}
	if !got["widget"] {
		t.Errorf("expected widget to be a discovered component; got %v", names)
	}
	if got["natsioish"] {
		t.Errorf("natsioish carries //forge:exclude-contract and must NOT be a discovered component; got %v", names)
	}
}

func writePkg(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
}
