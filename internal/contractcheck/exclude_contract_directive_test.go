package contractcheck

import (
	"context"
	"path/filepath"
	"testing"
)

// TestInternalContracts_HonorsExcludeContractDirective verifies that a
// package carrying the per-package `//forge:exclude-contract` header is
// skipped by the strict contract-shape rule — the local-header equivalent
// of a forge.yaml contracts.exclude entry, and the union counterpart of the
// generate-time discoverPackages / mock-walk skips. Without this, dropping
// the central exclude list in favour of headers would leave non-canonical
// (but deliberately excluded) packages like internal/daemonstate failing
// the pre-codegen shape check.
func TestInternalContracts_HonorsExcludeContractDirective(t *testing.T) {
	root := t.TempDir()
	pkgDir := filepath.Join(root, "internal", "runner")
	must(t, mkdirAll(pkgDir))

	// A non-canonical contract.go (interface is not named Service; no Deps /
	// New(Deps) Service) that WOULD fire the rule — but it carries the
	// directive, so the rule must skip it.
	must(t, writeFile(filepath.Join(pkgDir, "contract.go"), `//forge:exclude-contract

// Package runner exposes a lifecycle runner, not the canonical Service trio.
package runner

type Repository interface{ Load() error }
`))

	// Sanity: the SAME package WITHOUT the directive fires findings, so we
	// know the fixture is genuinely non-canonical.
	noDir := filepath.Join(t.TempDir(), "internal", "runner")
	must(t, mkdirAll(noDir))
	must(t, writeFile(filepath.Join(noDir, "contract.go"), `// Package runner exposes a lifecycle runner, not the canonical Service trio.
package runner

type Repository interface{ Load() error }
`))
	before, err := Inspect(context.Background(), filepath.Dir(filepath.Dir(noDir)),
		Options{Rules: []Rule{RuleInternalPackageContractNames}})
	if err != nil {
		t.Fatalf("Inspect (no directive): %v", err)
	}
	if len(before) == 0 {
		t.Fatalf("fixture sanity: a non-canonical contract.go without the directive must produce findings")
	}

	// With the directive present, findings drop to zero.
	after, err := Inspect(context.Background(), root,
		Options{Rules: []Rule{RuleInternalPackageContractNames}})
	if err != nil {
		t.Fatalf("Inspect (with directive): %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("expected 0 findings for a package carrying //forge:exclude-contract, got %d:\n%s",
			len(after), AsResult(after).FormatText())
	}
}
