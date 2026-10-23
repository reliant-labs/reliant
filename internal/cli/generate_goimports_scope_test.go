package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// goimports must see ONLY what forge wrote this run.
//
// It used to be handed the cmd/pkg/gen/internal directories, which made
// `forge generate` rewrite every hand-owned Go file in the project. That is
// forge editing what it does not own — the thing the tier model exists to
// prevent — and it is not merely cosmetic: goimports resolves unknown
// identifiers by GUESSING a package, so an untouched file can silently gain an
// import. A linter fixture in this repo acquired an `html/template` import
// purely because `forge generate` ran over it.
//
// The regression is invisible without this test: widening the scope back to
// directories breaks nothing that compiles, and the damage lands in files no
// forge test asserts on.
func TestWrittenGoFiles_OnlyReturnsFilesForgeWroteThisRun(t *testing.T) {
	dir := t.TempDir()

	write := func(rel string) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("package p\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// On disk: one file forge wrote, one it did not, both in a directory the
	// old implementation passed to goimports wholesale.
	write("internal/handlers/handlers_gen.go")
	write("internal/handlers/handwritten.go")
	// Written this run but a non-Go artifact, and written then removed by the
	// cleanup sweep — neither is a valid goimports target.
	write("deploy/kcl/config.k")

	defer restoreWrittenThisRun(t)()
	checksums.SetWrittenThisRun(map[string]bool{
		"internal/handlers/handlers_gen.go": true,
		"deploy/kcl/config.k":               true,
		"internal/handlers/deleted_gen.go":  true, // written, then swept
	})

	got := writtenGoFiles(dir)

	want := []string{"internal/handlers/handlers_gen.go"}
	if len(got) != len(want) {
		t.Fatalf("writtenGoFiles = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("writtenGoFiles[%d] = %q, want %q (full=%v)", i, got[i], w, got)
		}
	}

	// The load-bearing assertion, stated on its own so a failure names the
	// actual hazard rather than a slice mismatch.
	for _, p := range got {
		if p == "internal/handlers/handwritten.go" {
			t.Error("writtenGoFiles returned a file forge did not write — " +
				"`forge generate` would rewrite hand-owned code")
		}
	}
}

// restoreWrittenThisRun snapshots the package-level written set so a test can
// install its own without leaking into the rest of the package. The set is
// process-global mutable state, so a test that forgets this corrupts whatever
// runs next.
func restoreWrittenThisRun(t *testing.T) func() {
	t.Helper()
	saved := checksums.WrittenPaths()
	return func() { checksums.SetWrittenThisRun(saved) }
}
