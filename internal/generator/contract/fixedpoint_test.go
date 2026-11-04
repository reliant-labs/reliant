package contract

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// fixedPointModule is the module path every corpus project declares —
// it doubles as the goimports local prefix, so fixtures with
// project-local imports (testdata/localimport) exercise the
// stdlib / third-party / local grouping.
const fixedPointModule = "example.com/proj"

// TestGenerate_MockOutputIsCanonicalFormatterFixedPoint renders the
// ENTIRE contract fixture corpus through the manifest-aware write path
// (the checksums.WriteGeneratedFile chokepoint) and asserts the
// invariant that keeps the Tier-1 stomp guard honest:
//
//	generated Go output is a fixed point of the canonical formatter
//	(goimports = gofmt + import grouping, module path as local prefix).
//
// If a template change lands whose output goimports would rewrite —
// or an emitter bypasses the chokepoint's format-before-stamp pass —
// this fails loudly, naming the fixture. That failure mode is exactly
// the control-plane 2026-07-08 incident: mock_gen.go stamped with
// non-canonical import grouping, so a later goimports pass produced
// byte drift the guard misread as a hand-edit on 4 files.
func TestGenerate_MockOutputIsCanonicalFormatterFixedPoint(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatalf("ReadDir(testdata): %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		fixture := e.Name()
		t.Run(fixture, func(t *testing.T) {
			src := filepath.Join("testdata", fixture)
			if _, serr := os.Stat(filepath.Join(src, "contract.go")); serr != nil {
				t.Skipf("fixture %s has no contract.go", fixture)
			}

			// Assemble a minimal project: go.mod at the root, the fixture
			// as internal/<name>/ so the chokepoint's rel paths look real.
			root := t.TempDir()
			if werr := os.WriteFile(filepath.Join(root, "go.mod"),
				[]byte("module "+fixedPointModule+"\n\ngo 1.24\n"), 0o644); werr != nil {
				t.Fatal(werr)
			}
			pkgDir := filepath.Join(root, "internal", fixture)
			if merr := os.MkdirAll(pkgDir, 0o755); merr != nil {
				t.Fatal(merr)
			}
			files, rerr := os.ReadDir(src)
			if rerr != nil {
				t.Fatal(rerr)
			}
			for _, f := range files {
				data, ferr := os.ReadFile(filepath.Join(src, f.Name()))
				if ferr != nil {
					t.Fatal(ferr)
				}
				if werr := os.WriteFile(filepath.Join(pkgDir, f.Name()), data, 0o644); werr != nil {
					t.Fatal(werr)
				}
			}

			checksums.ResetSkipWrite()
			checksums.ResetPerRunState()
			t.Cleanup(func() {
				checksums.ResetSkipWrite()
				checksums.ResetPerRunState()
			})
			cs := &checksums.FileChecksums{}
			if gerr := GenerateWithOptions(filepath.Join(pkgDir, "contract.go"), Options{
				ProjectRoot: root,
				Checksums:   cs,
			}); gerr != nil {
				t.Fatalf("GenerateWithOptions(%s): %v", fixture, gerr)
			}

			mockPath := filepath.Join(pkgDir, "mock_gen.go")
			content, merr := os.ReadFile(mockPath)
			if merr != nil {
				t.Fatalf("mock_gen.go not written: %v", merr)
			}

			// The write must have gone through the certification chokepoint
			// (marker present + pristine) — a bypassing emitter would skip
			// format-before-stamp and silently reintroduce the bug class.
			if got := checksums.Verify(content); got != checksums.Pristine {
				t.Fatalf("mock_gen.go does not verify Pristine (got %v) — did the write bypass the chokepoint?\n%s", got, content)
			}

			formatted, perr := checksums.CanonicalGoSource(fixedPointModule, "mock_gen.go", content)
			if perr != nil {
				t.Fatalf("canonical formatter rejected generated output (template emits unparseable Go?): %v\n%s", perr, content)
			}
			if !bytes.Equal(formatted, content) {
				t.Fatalf("generated mock_gen.go is NOT a fixed point of the canonical formatter — a goimports pass would rewrite it and the stomp guard would flag the byte drift.\n--- generated ---\n%s\n--- canonical ---\n%s",
					content, formatted)
			}
		})
	}
}

// TestGenerate_LocalImportGrouping pins the incident shape end-to-end:
// a contract whose mock imports stdlib + third-party (contractkit) +
// PROJECT-LOCAL packages must come out of the generator with the local
// import in its own trailing group — i.e. already in the form
// `goimports -local <module>` produces, so that pass is a no-op.
func TestGenerate_LocalImportGrouping(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module "+fixedPointModule+"\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkgDir := filepath.Join(root, "internal", "localimport")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("testdata", "localimport", "contract.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "contract.go"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	checksums.ResetSkipWrite()
	checksums.ResetPerRunState()
	t.Cleanup(func() {
		checksums.ResetSkipWrite()
		checksums.ResetPerRunState()
	})
	if err := GenerateWithOptions(filepath.Join(pkgDir, "contract.go"), Options{
		ProjectRoot: root,
		Checksums:   &checksums.FileChecksums{},
	}); err != nil {
		t.Fatalf("GenerateWithOptions: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(pkgDir, "mock_gen.go"))
	if err != nil {
		t.Fatal(err)
	}

	// The local import must sit in its own group, separated from the
	// third-party group by a blank line (goimports -local layout).
	want := "\"github.com/reliant-labs/forge/pkg/contractkit\"\n\n\t\"example.com/proj/internal/widgets\""
	if !strings.Contains(string(content), want) {
		t.Fatalf("local import not grouped after third-party (goimports -local layout):\n%s", content)
	}
}
