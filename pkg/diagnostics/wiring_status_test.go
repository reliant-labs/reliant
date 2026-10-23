package diagnostics_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pkg/diagnostics' package doc used to describe a running subsystem: codegen
// emitting a `pkg/app/diagnostics_gen.go` whose init() registers every
// scaffold it detected, Bootstrap calling Default().Boot after Setup, and
// `features.strict_wiring` flipping the emitter to fatal. None of that
// exists. Nothing in forge imports this package, so the registry is always
// empty and Boot emits nothing — a reader who believed the doc would think
// their unwired scaffolds were being reported at boot when no such report
// was ever produced. That is the exact failure mode the package was written
// to prevent, in its own documentation.
//
// The doc now says "a library, not yet a pipeline". This test is what keeps
// that honest in BOTH directions:
//
//   - If someone wires the producer up (codegen registration + a Boot call)
//     without updating the doc, the doc understates what forge does and this
//     test fails, naming the importer.
//   - If the doc is edited back to claiming an automatic pipeline while none
//     exists, the claim-side check fails.
//
// The importer set is DERIVED from the real import graph via `go list`, not
// from a hand-maintained list of package names.

// diagnosticsPkg is the import path under test.
const diagnosticsPkg = "github.com/reliant-labs/forge/pkg/diagnostics"

// TestDiagnosticsHasNoProducerAndSaysSo asserts the doc's status section
// agrees with the import graph.
func TestDiagnosticsHasNoProducerAndSaysSo(t *testing.T) {
	if testing.Short() {
		t.Skip("loads every package in the forge module to find importers; runs in task test")
	}
	repoRoot := findRepoRoot(t)

	importers := packagesImporting(t, repoRoot, diagnosticsPkg)

	doc := readDocGo(t)
	// The doc must state the status explicitly. Without this the test could
	// "pass" against a doc that says nothing at all.
	const statusClaim = "Nothing in forge imports this package."
	claimsUnwired := strings.Contains(doc, statusClaim)
	if !claimsUnwired {
		t.Errorf("pkg/diagnostics/doc.go no longer carries the status sentence %q.\n"+
			"This package ships a registry nothing fills; a doc that does not say so "+
			"reads as a description of a running subsystem.", statusClaim)
	}

	switch {
	case len(importers) > 0 && claimsUnwired:
		t.Errorf("pkg/diagnostics is now imported by %d package(s): %s\n"+
			"The doc still says %q. A producer exists — update the status section to "+
			"describe what actually registers diagnostics and where Boot is called.",
			len(importers), strings.Join(importers, ", "), statusClaim)
	case len(importers) == 0 && !claimsUnwired:
		t.Error("no package imports pkg/diagnostics, but the doc does not say so — " +
			"a reader will assume boot-time diagnostics are being emitted when the " +
			"registry is always empty.")
	}
}

// TestDiagnosticsDocClaimsNoGeneratedRegistrationFile pins the specific
// fictions the doc used to carry: a generated registration file and a
// Bootstrap call site. Each is asserted against the filesystem/import graph
// rather than against a remembered string.
func TestDiagnosticsDocClaimsNoGeneratedRegistrationFile(t *testing.T) {
	if testing.Short() {
		// The answer depends on every file `git grep` can see, and a child
		// process's reads are not inputs to go test's cache: a cached ok would
		// survive a new producer appearing anywhere in the checkout.
		t.Skip("greps the whole checkout via git, which the test cache cannot track; runs in task test")
	}
	repoRoot := findRepoRoot(t)
	doc := readDocGo(t)

	// The doc named `pkg/app/diagnostics_gen.go` as a file codegen emits. A
	// claim that codegen EMITS a file is only true if some emitter writes
	// it, which would mean the literal name appears in the generator tree.
	emitted := grepRepo(t, repoRoot, "diagnostics_gen.go")
	// The template-inventory test lists the filename as a known pkg/app
	// file; that is an inventory entry, not a producer. A producer would
	// live outside a _test.go file.
	var producers []string
	for _, f := range emitted {
		if !strings.HasSuffix(f, "_test.go") {
			producers = append(producers, f)
		}
	}

	mentionsGen := strings.Contains(doc, "diagnostics_gen.go")
	if len(producers) == 0 && mentionsGen {
		// Mentioning it is fine ONLY in the negative ("there is no codegen
		// step emitting..."). Require the negation to be adjacent so the
		// sentence cannot drift back into an assertion.
		if !strings.Contains(doc, "no codegen step emitting") {
			t.Errorf("doc.go names pkg/app/diagnostics_gen.go but nothing in forge writes it "+
				"(no non-test producer found).\nDescribe it as absent, or make it exist.\n"+
				"searched files: %v", emitted)
		}
	}
	if len(producers) > 0 && !mentionsGen {
		t.Errorf("something now produces diagnostics_gen.go (%v) but doc.go does not mention it",
			producers)
	}
}

// packagesImporting returns the packages in the forge module that import target.
func packagesImporting(t *testing.T, repoRoot, target string) []string {
	t.Helper()
	// One pattern covers pkg/ too: it is part of the forge module. (It used
	// to be a separate module listed alongside, because `./...` from the
	// workspace root stopped at the module boundary and a walk that missed
	// pkg/ reported "no importers" whatever the code did.)
	cmd := exec.Command("go", "list", "-deps=false",
		"-f", `{{.ImportPath}}{{range .Imports}} {{.}}{{end}}`,
		"github.com/reliant-labs/forge/...")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list in %s: %v", repoRoot, err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		t.Fatalf("go list returned %d line(s) — the import graph is empty, so this test "+
			"would report 'no importers' no matter what the code does", len(lines))
	}
	// The walk must actually REACH the tree the package under test lives
	// in. Without this, a listing that silently stopped short of pkg/ would
	// report "no importers" unconditionally.
	sawOwnModule := false
	for _, line := range lines {
		if fields := strings.Fields(line); len(fields) > 0 &&
			strings.HasPrefix(fields[0], "github.com/reliant-labs/forge/pkg/") {
			sawOwnModule = true
			break
		}
	}
	if !sawOwnModule {
		t.Fatal("the import-graph walk listed no package under forge/pkg — " +
			"it cannot see the tree pkg/diagnostics lives in, so 'no importers' " +
			"would be its answer regardless of the code")
	}

	var importers []string
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		self := fields[0]
		if self == target {
			continue
		}
		for _, imp := range fields[1:] {
			if imp == target {
				importers = append(importers, self)
				break
			}
		}
	}
	return importers
}

// grepRepo returns the files under repoRoot containing needle.
func grepRepo(t *testing.T, repoRoot, needle string) []string {
	t.Helper()
	cmd := exec.Command("git", "grep", "-l", "--", needle)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		// git grep exits 1 with no matches — that is a valid answer.
		return nil
	}
	var files []string
	for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files
}

func readDocGo(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("doc.go")
	if err != nil {
		t.Fatalf("read doc.go: %v", err)
	}
	return string(raw)
}

// findRepoRoot walks up to the forge module root — the go.mod declaring
// github.com/reliant-labs/forge.
//
// It used to look for go.work, which existed only to stitch the pkg/
// submodule to the root module. Both are one module now, so go.work is gone
// and the module's own go.mod is the marker — and matching on the module PATH
// rather than on any go.mod keeps this from stopping at some future nested
// module on the way up.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 10; i++ {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && strings.Contains(string(data), "module github.com/reliant-labs/forge\n") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not locate the forge module root above the test directory")
	return ""
}
