package codegen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// assertCanonicalGoTree walks root and asserts, for every
// forge-certified (marker-bearing) .go file, the invariant that keeps
// the Tier-1 stomp guard free of formatter false positives:
//
//	generated Go output is a fixed point of the canonical formatter
//	(goimports = gofmt + import grouping, module path as local prefix)
//
// A failure means a template's output would be rewritten by the user's
// goimports pass — byte drift the guard would misread as a hand-edit
// (control-plane 2026-07-08) — or that an emitter bypassed the
// checksums.WriteGeneratedFile format-before-stamp chokepoint.
func assertCanonicalGoTree(t *testing.T, root string) {
	t.Helper()
	localPrefix := checksums.GoImportsLocalPrefix(root)
	checked := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, werr error) error {
		if werr != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return werr
		}
		content, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if !bytes.Contains(content, []byte("forge:hash=")) {
			return nil // scaffold / user-owned — not certified output
		}
		rel, _ := filepath.Rel(root, path)
		if got := checksums.Verify(content); got != checksums.Pristine {
			t.Errorf("%s: generated file does not verify Pristine (got %v)", rel, got)
		}
		formatted, perr := checksums.CanonicalGoSource(localPrefix, rel, content)
		if perr != nil {
			t.Errorf("%s: canonical formatter rejected generated output: %v", rel, perr)
			return nil
		}
		if !bytes.Equal(formatted, content) {
			t.Errorf("%s: generated Go is NOT a fixed point of the canonical formatter — a goimports pass would rewrite it.\n--- generated ---\n%s\n--- canonical ---\n%s",
				rel, content, formatted)
		}
		checked++
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if checked == 0 {
		t.Fatal("assertCanonicalGoTree checked zero certified .go files — the emitter under test produced nothing")
	}
}

// TestGenerateConfigLoader_CanonicalFormatterFixedPoint renders the
// default config-loader shim (a writeForgeOwned emitter) and pins the
// canonical-formatter fixed-point invariant on its output.
func TestGenerateConfigLoader_CanonicalFormatterFixedPoint(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/proj\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	checksums.ResetSkipWrite()
	checksums.ResetPerRunState()
	t.Cleanup(func() {
		checksums.ResetSkipWrite()
		checksums.ResetPerRunState()
	})
	if err := GenerateConfigLoader(DefaultConfigMessages(), dir, nil); err != nil {
		t.Fatalf("GenerateConfigLoader: %v", err)
	}
	assertCanonicalGoTree(t, dir)
}
