package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// staleMarker is a well-formed certification hash that is deliberately
// not the body hash of anything — the shape of a marker left behind
// when a file's bytes moved on and nothing re-stamped it.
const staleMarker = "0000000000000000000000000000000000000000000000000000000000000000"

// retireFixture builds a temp project with one internal package whose
// contract.go has been generated from, and returns the project root plus
// the mock's project-relative path. The mock on disk is exactly what
// forge would write: canonically formatted and freshly stamped.
func retireFixture(t *testing.T) (root, rel string, opts Options) {
	t.Helper()
	root = t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/proj\n\ngo 1.26.2\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	dir := filepath.Join(root, "internal", "thing")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	contractSrc := `package thing

// Service is the behavioral surface of the thing package.
type Service interface {
	// Do does the thing.
	Do(id string) error
}

// Deps is the dependency set for the thing Service.
type Deps struct{}

// New constructs a thing.Service.
func New(_ Deps) Service { return nil }
`
	if err := os.WriteFile(filepath.Join(dir, "contract.go"), []byte(contractSrc), 0o644); err != nil {
		t.Fatalf("write contract.go: %v", err)
	}

	checksums.ResetSkipWrite()
	checksums.ResetPerRunState()
	opts = Options{ProjectRoot: root, Checksums: &checksums.FileChecksums{}}
	if err := GenerateWithOptions(filepath.Join(dir, "contract.go"), opts); err != nil {
		t.Fatalf("seed generate: %v", err)
	}
	rel = "internal/thing/" + mockFileName
	if checksums.Verify(mustRead(t, root, rel)) != checksums.Pristine {
		t.Fatalf("fixture precondition: seeded mock is not pristine")
	}
	return root, rel, opts
}

func mustRead(t *testing.T, root, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return b
}

func mustWriteRel(t *testing.T, root, rel string, content []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), content, 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func exists(t *testing.T, root, rel string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

// staleTheMarker rewrites the file's embedded hash to a different (but
// syntactically valid) value, leaving every other byte alone. That is
// exactly the state a text sweep over the generated banner leaves
// behind, generalized: the certificate no longer matches, the content
// is still forge's own render.
func staleTheMarker(t *testing.T, root, rel string) {
	t.Helper()
	content := mustRead(t, root, rel)
	embedded, found := checksums.ExtractMarker(content)
	if !found {
		t.Fatalf("%s carries no marker to stale", rel)
	}
	mustWriteRel(t, root, rel, []byte(strings.Replace(string(content), embedded, staleMarker, 1)))
	if got := checksums.Verify(mustRead(t, root, rel)); got != checksums.Modified {
		t.Fatalf("precondition: staled marker should read Modified, got %v", got)
	}
}

// TestRetire_StaleMarkerMatchingRender is the regression test for the
// unhealable state: a certified artifact in a package that has since
// opted out of contract codegen, whose marker no longer verifies even
// though its body is byte-identical to what forge renders today.
//
// Before retirement existed this file was stranded — no emitter would
// rewrite it (the package is opted out) and the stale-artifact sweep
// refused to remove it (a failed marker reads as a hand-edit), so
// `forge lint --generated-drift` failed on it on every run forever.
func TestRetire_StaleMarkerMatchingRender(t *testing.T) {
	root, rel, opts := retireFixture(t)
	staleTheMarker(t, root, rel)

	// Precondition: the drift gate sees it.
	if got := len(checksums.ScanTier1Drift(root, opts.Checksums)); got != 1 {
		t.Fatalf("precondition: want 1 drift entry before retirement, got %d", got)
	}

	r, err := RetireExcludedArtifacts(filepath.Join(root, "internal", "thing"), opts)
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	if len(r.Retired) != 1 || r.Retired[0] != rel {
		t.Fatalf("Retired = %v, want [%s]", r.Retired, rel)
	}
	if len(r.Kept) != 0 {
		t.Fatalf("Kept = %v, want none", r.Kept)
	}
	if exists(t, root, rel) {
		t.Fatalf("%s still on disk after retirement", rel)
	}
	if got := checksums.ScanTier1Drift(root, opts.Checksums); len(got) != 0 {
		t.Fatalf("drift after retirement = %v, want none", got)
	}
}

// TestRetire_HandEditKeptAndStillGated is the other half of the
// contract: a genuine hand-edit must NOT be laundered away. Its body
// differs from the render, so forge cannot prove the bytes are its own,
// leaves the file alone, and the drift gate keeps failing on it.
func TestRetire_HandEditKeptAndStillGated(t *testing.T) {
	root, rel, opts := retireFixture(t)
	edited := strings.Replace(string(mustRead(t, root, rel)),
		"type MockService struct {",
		"// HandEdited: a real customization the user does not want deleted.\ntype MockService struct {", 1)
	mustWriteRel(t, root, rel, []byte(edited))

	r, err := RetireExcludedArtifacts(filepath.Join(root, "internal", "thing"), opts)
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	if len(r.Retired) != 0 {
		t.Fatalf("Retired = %v, want none — a hand-edit must never be deleted", r.Retired)
	}
	if len(r.Kept) != 1 || r.Kept[0] != rel {
		t.Fatalf("Kept = %v, want [%s]", r.Kept, rel)
	}
	if !exists(t, root, rel) {
		t.Fatalf("%s was deleted — hand-edited bytes must survive", rel)
	}
	if !strings.Contains(string(mustRead(t, root, rel)), "HandEdited") {
		t.Fatalf("the hand-edit was rewritten away")
	}
	drift := checksums.ScanTier1Drift(root, opts.Checksums)
	if len(drift) != 1 || drift[0].Path != rel {
		t.Fatalf("drift = %v, want the hand-edit to STILL fail the gate", drift)
	}
}

// TestRetire_PristineArtifact covers the ordinary case: the artifact
// still verifies, so it is unambiguously forge's own and goes away when
// the package opts out.
func TestRetire_PristineArtifact(t *testing.T) {
	root, rel, opts := retireFixture(t)
	r, err := RetireExcludedArtifacts(filepath.Join(root, "internal", "thing"), opts)
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	if len(r.Retired) != 1 || r.Retired[0] != rel {
		t.Fatalf("Retired = %v, want [%s]", r.Retired, rel)
	}
	if exists(t, root, rel) {
		t.Fatalf("%s still on disk", rel)
	}
}

// TestRetire_UnmarkedFileUntouched: forge never certified these bytes,
// so the path is not forge's to reclaim even though the filename
// matches a generated artifact.
func TestRetire_UnmarkedFileUntouched(t *testing.T) {
	root, rel, opts := retireFixture(t)
	mustWriteRel(t, root, rel, checksums.StripMarker(mustRead(t, root, rel)))

	r, err := RetireExcludedArtifacts(filepath.Join(root, "internal", "thing"), opts)
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	if len(r.Retired) != 0 {
		t.Fatalf("Retired = %v, want none for an unmarked file", r.Retired)
	}
	if !exists(t, root, rel) {
		t.Fatalf("%s was deleted despite carrying no forge marker", rel)
	}
}

// TestRetire_DisownedUntouched: `forge project disown` is a one-way
// ownership transfer, and retirement must respect it even for a file
// that is otherwise byte-perfect forge output.
func TestRetire_DisownedUntouched(t *testing.T) {
	root, rel, opts := retireFixture(t)
	opts.Checksums.Disowned = map[string]checksums.DisownedEntry{rel: {Reason: "mine now"}}

	r, err := RetireExcludedArtifacts(filepath.Join(root, "internal", "thing"), opts)
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	if len(r.Retired) != 0 || len(r.Kept) != 0 {
		t.Fatalf("Retired=%v Kept=%v, want a disowned path reported in neither", r.Retired, r.Kept)
	}
	if !exists(t, root, rel) {
		t.Fatalf("%s was deleted despite being disowned", rel)
	}
}

// TestRetire_NoArtifactsIsNoOp: the steady state after the first
// retirement (and for a package that opted out before forge ever
// generated into it) is that there is nothing to do, quietly.
func TestRetire_NoArtifactsIsNoOp(t *testing.T) {
	root, rel, opts := retireFixture(t)
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		t.Fatalf("remove: %v", err)
	}
	r, err := RetireExcludedArtifacts(filepath.Join(root, "internal", "thing"), opts)
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	if len(r.Retired) != 0 || len(r.Kept) != 0 {
		t.Fatalf("Retired=%v Kept=%v, want both empty", r.Retired, r.Kept)
	}
}

// TestRetire_ObservabilityWrapperStaleIsKept: middleware_gen.go cannot
// be re-rendered from this package, so a stale marker is where the
// evidence runs out — keep it and let the user decide.
func TestRetire_ObservabilityWrapperStaleIsKept(t *testing.T) {
	root, _, opts := retireFixture(t)
	dir := filepath.Join(root, "internal", "thing")
	wrapperRel := "internal/thing/middleware_gen.go"

	body := "// Code generated by forge. DO NOT EDIT.\npackage thing\n"
	stamped, ok := checksums.Stamp(wrapperRel, []byte(body))
	if !ok {
		t.Fatal("wrapper should be stampable")
	}
	mustWriteRel(t, root, wrapperRel, stamped)

	// Pristine: retired along with the mock.
	r, err := RetireExcludedArtifacts(dir, opts)
	if err != nil {
		t.Fatalf("retire pristine wrapper: %v", err)
	}
	if !contains(r.Retired, wrapperRel) {
		t.Fatalf("Retired = %v, want it to include %s", r.Retired, wrapperRel)
	}

	// Stale: kept, because there is no render to compare against.
	mustWriteRel(t, root, wrapperRel, stamped)
	staleTheMarker(t, root, wrapperRel)
	r, err = RetireExcludedArtifacts(dir, opts)
	if err != nil {
		t.Fatalf("retire stale wrapper: %v", err)
	}
	if contains(r.Retired, wrapperRel) {
		t.Fatalf("Retired = %v, a stale wrapper must not be deleted", r.Retired)
	}
	if !contains(r.Kept, wrapperRel) {
		t.Fatalf("Kept = %v, want it to include %s", r.Kept, wrapperRel)
	}
	if !exists(t, root, wrapperRel) {
		t.Fatalf("%s was deleted", wrapperRel)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
