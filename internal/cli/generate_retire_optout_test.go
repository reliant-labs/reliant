package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/generator/contract"
)

// optOutFixture builds a project with one internal package that forge
// has already generated a mock for, then leaves it to the caller to
// declare the opt-out. Returns the project root and the mock's
// project-relative path.
func optOutFixture(t *testing.T) (root, mockRel string, cs *generator.FileChecksums) {
	t.Helper()
	root = t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.com/proj\n\ngo 1.26.2\n")
	dir := filepath.Join(root, "internal", "thing")
	mustMkdir(t, dir)
	mustWrite(t, filepath.Join(dir, "contract.go"), `package thing

// Service is the behavioral surface of the thing package.
type Service interface {
	// Do does the thing.
	Do(id string) error
}

// Deps is the dependency set for the thing Service.
type Deps struct{}

// New constructs a thing.Service.
func New(_ Deps) Service { return nil }
`)

	checksums.ResetSkipWrite()
	checksums.ResetPerRunState()
	cs = &checksums.FileChecksums{}
	if err := contract.GenerateWithOptions(filepath.Join(dir, "contract.go"),
		contract.Options{ProjectRoot: root, Checksums: cs}); err != nil {
		t.Fatalf("seed mock: %v", err)
	}
	return root, "internal/thing/mock_gen.go", cs
}

// staleTheMarkerAt rewrites a file's embedded certification hash without
// touching any other byte — the generalized form of what a repo-wide
// text sweep over the generated banner does.
func staleTheMarkerAt(t *testing.T, root, rel string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	content, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	embedded, found := checksums.ExtractMarker(content)
	if !found {
		t.Fatalf("%s carries no marker", rel)
	}
	stale := strings.Replace(string(content), embedded,
		"0000000000000000000000000000000000000000000000000000000000000000", 1)
	if err := os.WriteFile(full, []byte(stale), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// TestGenerate_DirectiveOptOutRetiresStrandedMock is the end-to-end
// regression: a package that opted out of contract codegen via
// `//forge:exclude-contract` AFTER forge had generated its mock, whose
// mock then lost its certification to a banner sweep.
//
// The mock is unreachable by every other mechanism — the mock walk skips
// the package so no writer re-stamps it, and the stale-artifact sweep
// reads the failed marker as a hand-edit and refuses to remove it — so
// `forge lint --generated-drift` failed on it forever. The contracts
// step must retire it.
func TestGenerate_DirectiveOptOutRetiresStrandedMock(t *testing.T) {
	root, mockRel, cs := optOutFixture(t)
	staleTheMarkerAt(t, root, mockRel)
	mustWrite(t, filepath.Join(root, "internal", "thing", "doc.go"),
		"// forge:exclude-contract\n\npackage thing\n")

	if got := len(generator.ScanProjectDrift(root, cs)); got != 1 {
		t.Fatalf("precondition: want 1 drift entry, got %d", got)
	}

	if err := generateInternalPackageContracts(root, &config.ProjectConfig{Name: "proj"}, cs); err != nil {
		t.Fatalf("generateInternalPackageContracts: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(mockRel))); !os.IsNotExist(err) {
		t.Fatalf("%s survived the opt-out — it is stranded again", mockRel)
	}
	if got := generator.ScanProjectDrift(root, cs); len(got) != 0 {
		t.Fatalf("drift after generate = %v, want none", got)
	}
}

// TestGenerate_OptOutKeepsHandEditedMock is the guard against
// laundering: when the leftover's body differs from forge's render,
// forge cannot prove the bytes are its own, so it keeps the file and the
// drift gate keeps failing.
func TestGenerate_OptOutKeepsHandEditedMock(t *testing.T) {
	root, mockRel, cs := optOutFixture(t)
	full := filepath.Join(root, filepath.FromSlash(mockRel))
	content, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("read mock: %v", err)
	}
	mustWrite(t, full, strings.Replace(string(content), "type MockService struct {",
		"// HandEdited: customization the user wants kept.\ntype MockService struct {", 1))
	mustWrite(t, filepath.Join(root, "internal", "thing", "doc.go"),
		"// forge:exclude-contract\n\npackage thing\n")

	if err := generateInternalPackageContracts(root, &config.ProjectConfig{Name: "proj"}, cs); err != nil {
		t.Fatalf("generateInternalPackageContracts: %v", err)
	}

	kept, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("%s was deleted — a hand-edit must survive: %v", mockRel, err)
	}
	if !strings.Contains(string(kept), "HandEdited") {
		t.Fatalf("the hand-edit was rewritten away")
	}
	drift := generator.ScanProjectDrift(root, cs)
	if len(drift) != 1 || drift[0].Path != mockRel {
		t.Fatalf("drift = %v, want the hand-edit to STILL fail the gate", drift)
	}
}

// TestGenerate_CentralExcludeRetiresSubtree pins that a forge.yaml
// `contracts.exclude` entry retires the whole excluded subtree. The walk
// SkipDirs an excluded directory, so the sweep is the only pass that
// will ever visit its nested packages.
func TestGenerate_CentralExcludeRetiresSubtree(t *testing.T) {
	root, mockRel, cs := optOutFixture(t)
	staleTheMarkerAt(t, root, mockRel)

	cfg := &config.ProjectConfig{
		Name:      "proj",
		Contracts: config.ContractsConfig{Exclude: []string{"internal/thing"}},
	}
	if err := generateInternalPackageContracts(root, cfg, cs); err != nil {
		t.Fatalf("generateInternalPackageContracts: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(mockRel))); !os.IsNotExist(err) {
		t.Fatalf("%s survived a central contracts.exclude", mockRel)
	}
}

// TestContractArtifactOptedOut_DropsFromStompGuard pins the stomp
// guard's half of the fix: an opted-out artifact is nobody's emit
// target, so the guard must not claim `forge generate` is about to
// overwrite it. Before this, the guard aborted the run on a file the
// pipeline would never write — and pointed the user at a --force that
// had nothing to discard.
func TestContractArtifactOptedOut_DropsFromStompGuard(t *testing.T) {
	root, mockRel, _ := optOutFixture(t)
	mustWrite(t, filepath.Join(root, "internal", "thing", "doc.go"),
		"// forge:exclude-contract\n\npackage thing\n")

	ctx := &pipelineContext{ProjectDir: root, AbsPath: root, Cfg: &config.ProjectConfig{Name: "proj"}}
	drift := []driftStub{{path: mockRel}}
	inScope, outOfScope := filterTier1DriftInScope(ctx, drift, func(d driftStub) string { return d.path })
	if len(inScope) != 0 || len(outOfScope) != 0 {
		t.Fatalf("inScope=%v outOfScope=%v, want the opted-out artifact dropped from both", inScope, outOfScope)
	}

	// Same path in a package that has NOT opted out stays in scope: the
	// emitter really would rewrite it, so the guard must still fire.
	mustWrite(t, filepath.Join(root, "internal", "thing", "doc.go"), "package thing\n")
	inScope, _ = filterTier1DriftInScope(ctx, drift, func(d driftStub) string { return d.path })
	if len(inScope) != 1 {
		t.Fatalf("inScope = %v, want the live mock to stay in scope", inScope)
	}
}
