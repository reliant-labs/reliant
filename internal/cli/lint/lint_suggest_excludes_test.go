package lint

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestSuggestExcludes_DetectsAnalyzerPackage verifies the filename
// heuristic: a package that has an analyzer.go (no contract.go) but
// declares exported methods on a struct is flagged.
func TestSuggestExcludes_DetectsAnalyzerPackage(t *testing.T) {
	root := t.TempDir()
	pkgDir := filepath.Join(root, "internal", "myanalyzer")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// analyzer.go declares an exported method, no contract.go in dir.
	src := `package myanalyzer

type Analyzer struct{}

func (a *Analyzer) Analyze() error { return nil }
`
	if err := os.WriteFile(filepath.Join(pkgDir, "analyzer.go"), []byte(src), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := scanForExcludeCandidates(root, nil)
	if err != nil {
		t.Fatalf("scanForExcludeCandidates: %v", err)
	}
	if len(got) != 1 || got[0].RelPath != "internal/myanalyzer" {
		t.Fatalf("expected 1 suggestion for internal/myanalyzer, got %+v", got)
	}
}

// TestSuggestExcludes_RespectsExistingExcludes verifies a package already
// listed in contracts.exclude is not re-suggested.
func TestSuggestExcludes_RespectsExistingExcludes(t *testing.T) {
	root := t.TempDir()
	pkgDir := filepath.Join(root, "internal", "myanalyzer")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	src := `package myanalyzer

type Analyzer struct{}

func (a *Analyzer) Analyze() error { return nil }
`
	if err := os.WriteFile(filepath.Join(pkgDir, "analyzer.go"), []byte(src), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	existing := map[string]bool{"internal/myanalyzer": true}
	got, err := scanForExcludeCandidates(root, existing)
	if err != nil {
		t.Fatalf("scanForExcludeCandidates: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 suggestions when already excluded, got %+v", got)
	}
}

// TestSuggestExcludes_SkipsPackagesWithContract verifies the heuristic
// pass does not flag packages that already have a contract.go.
func TestSuggestExcludes_SkipsPackagesWithContract(t *testing.T) {
	root := t.TempDir()
	pkgDir := filepath.Join(root, "internal", "good")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	contract := `package good

type Service interface{ Do() error }
type Deps struct{}

func New(Deps) Service { return nil }
`
	svc := `package good

type impl struct{}

func (i *impl) Do() error { return nil }

type Helper struct{}

func (h *Helper) Help() error { return nil }
`
	if err := os.WriteFile(filepath.Join(pkgDir, "contract.go"), []byte(contract), 0644); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "service.go"), []byte(svc), 0644); err != nil {
		t.Fatalf("write svc: %v", err)
	}
	got, err := scanForExcludeCandidates(root, nil)
	if err != nil {
		t.Fatalf("scanForExcludeCandidates: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 suggestions when contract.go present, got %+v", got)
	}
}

// TestSuggestExcludes_SkipsPackagesWithoutExportedMethods verifies that
// packages with no exported methods (e.g. data-only packages) aren't
// suggested — the require-contract analyzer wouldn't flag them either.
func TestSuggestExcludes_SkipsPackagesWithoutExportedMethods(t *testing.T) {
	root := t.TempDir()
	pkgDir := filepath.Join(root, "internal", "types")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	src := `package types

type Order struct{ ID string }
`
	if err := os.WriteFile(filepath.Join(pkgDir, "types.go"), []byte(src), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := scanForExcludeCandidates(root, nil)
	if err != nil {
		t.Fatalf("scanForExcludeCandidates: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 suggestions when no exported methods, got %+v", got)
	}
}

// TestSuggestExcludes_FlagsConventionDir verifies the convention-prefix
// heuristic catches forge utility packages by directory location even
// when the filename doesn't match analyzer/lint patterns.
func TestSuggestExcludes_FlagsConventionDir(t *testing.T) {
	root := t.TempDir()
	pkgDir := filepath.Join(root, "internal", "metrics")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	src := `package metrics

type Collector struct{}

func (c *Collector) Record() error { return nil }
`
	if err := os.WriteFile(filepath.Join(pkgDir, "metrics.go"), []byte(src), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := scanForExcludeCandidates(root, nil)
	if err != nil {
		t.Fatalf("scanForExcludeCandidates: %v", err)
	}
	if len(got) != 1 || got[0].RelPath != "internal/metrics" {
		t.Fatalf("expected 1 suggestion for internal/metrics, got %+v", got)
	}
}

// TestSuggestExcludes_DeterministicOrder verifies the output is sorted
// by relative path so successive invocations produce stable diffs.
func TestSuggestExcludes_DeterministicOrder(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"zlast", "amid", "mfirst"} {
		pkgDir := filepath.Join(root, "internal", p)
		if err := os.MkdirAll(pkgDir, 0755); err != nil {
			t.Fatalf("setup: %v", err)
		}
		src := "package " + p + "\n\ntype Analyzer struct{}\n\nfunc (a *Analyzer) Analyze() error { return nil }\n"
		if err := os.WriteFile(filepath.Join(pkgDir, "analyzer.go"), []byte(src), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	got, err := scanForExcludeCandidates(root, nil)
	if err != nil {
		t.Fatalf("scanForExcludeCandidates: %v", err)
	}
	paths := make([]string, len(got))
	for i, s := range got {
		paths[i] = s.RelPath
	}
	if !sort.StringsAreSorted(paths) {
		t.Errorf("expected sorted output, got %v", paths)
	}
}
