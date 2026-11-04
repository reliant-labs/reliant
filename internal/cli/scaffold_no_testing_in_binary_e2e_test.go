//go:build e2e

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2ETestingPackageNotLinkedIntoBinary is an ARCHITECTURAL GATE, not a
// feature test: package `testing` must never appear in the dependency graph
// of a scaffolded project's production binary.
//
// THE DEFECT IT PINS. forge used to emit `pkg/app/testing.go` and
// `pkg/app/factory_gen.go` — NON-test `.go` files that imported "testing" —
// into `pkg/app`, which `cmd/<proj>/cmd` imports. So every scaffolded project
// linked package `testing` into the binary it shipped:
//
//	$ go list -deps ./cmd/tierapp | grep -c '^testing$'
//	1
//
// That is not merely dead weight. Package `testing` registers flags in its
// init() (-test.v, -test.run, …), so a production binary that links it has a
// flag namespace it did not choose, mutated at init time by a package the
// author never imported.
//
// THE FIX IT GUARDS. Both the per-component harness (`helpers_gen_test.go`)
// and the typed entity factories (`factories_gen_test.go`) now land beside the
// component as `_test.go` files, so the toolchain compiles them only into that
// package's test binary. The factories previously lived in
// `internal/testfactory` — a non-test package importing `testing`, which cmd/
// merely happened not to reach; the `_test.go` suffix makes that structural
// rather than a property this test has to keep checking.
//
// WHY `go list -deps` AND NOT A GREP. Grepping the emitters for `"testing"`
// would pin today's two known leaks and miss the third. The dependency graph
// is the actual property we care about: it is transitive, so it catches a
// leak introduced through any depth of intermediate package, by any future
// emitter, without that emitter knowing this test exists.
//
// This shells out to the Go toolchain, so it follows the corpus convention of
// skipping under -short (see reliant.md "Testing tiers").
func TestE2ETestingPackageNotLinkedIntoBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to `go list -deps` over a real scaffolded project")
	}
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once

	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "notestbin",
		"--mod", "example.com/notestbin", "--service", "order")
	projectDir := filepath.Join(dir, "notestbin")
	addCorpusForgePkgReplace(t, projectDir)

	// Exercise the ENTITY path too: the entity factories are the second
	// emitter that leaked `testing`, and they are only emitted once a
	// `// forge:entity` message exists. A project with no entity would pass
	// this gate while the factory leak went unguarded.
	protoPath := filepath.Join(projectDir, "proto", "services", "order", "v1", "order.proto")
	proto := readFileE2E(t, protoPath)
	writeFileE2E(t, protoPath, proto+`
// forge:entity
message Item {
  string name = 2;
  int64 price_cents = 3;
  bool active = 4;
}
`)
	runCmd(t, projectDir, forgeBin, "scaffold")

	// The tree must build before a deps query means anything.
	runCmd(t, projectDir, "go", "build", "./...")

	assertNotInBinaryDeps(t, projectDir, "./cmd/notestbin", "testing")

	// The harness must still EXIST and be usable — a gate that passes
	// because the helpers were deleted would be worse than the defect.
	helper := filepath.Join(projectDir, "internal", "handlers", "order", "helpers_gen_test.go")
	body := readFileE2E(t, helper)
	if !strings.Contains(body, "func NewTestOrder(") {
		t.Errorf("%s does not declare NewTestOrder — the harness must survive the split, not be deleted", helper)
	}
	// The package clause is load-bearing: `package order` (not `order_test`)
	// is what lets BOTH the in-package and the external `order_test` files
	// use these helpers.
	if !strings.Contains(body, "\npackage order\n") {
		t.Errorf("%s must be `package order` (not order_test) so external order_test files can use it", helper)
	}

	// Same for the entity factories, which moved here from
	// internal/testfactory. The Item entity added above is owned by the
	// order service's CRUD RPCs, so its factory lands in that package.
	factories := filepath.Join(projectDir, "internal", "handlers", "order", "factories_gen_test.go")
	fbody := readFileE2E(t, factories)
	if !strings.Contains(fbody, "func NewItem(") {
		t.Errorf("%s does not declare NewItem — the factories must survive the move, not be deleted", factories)
	}
	if !strings.Contains(fbody, "\npackage order\n") {
		t.Errorf("%s must be `package order` so the CRUD lifecycle test (package order_test, same dir) can call it", factories)
	}
	// The package they came from must be GONE, not merely unreferenced: a
	// non-test .go file importing `testing` left in the tree is the defect,
	// whether or not today's import graph happens to reach it.
	if _, err := os.Stat(filepath.Join(projectDir, "internal", "testfactory")); !os.IsNotExist(err) {
		t.Errorf("internal/testfactory still exists (stat err: %v) — the factories moved beside their consumers; the old non-test package must not be left behind", err)
	}

	// And the tests it serves must actually compile.
	runCmd(t, projectDir, "go", "vet", "./...")
}

// assertNotInBinaryDeps fails when pkg appears in `go list -deps <target>`
// run inside projectDir. The comparison is on whole lines: `go list -deps`
// prints one import path per line, and a substring match would confuse
// `testing` with `internal/testfactory` or `testing/fstest`.
func assertNotInBinaryDeps(t *testing.T, projectDir, target, pkg string) {
	t.Helper()

	cmd := exec.Command("go", "list", "-deps", target)
	cmd.Dir = projectDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps %s in %s: %v\n%s", target, projectDir, err, out)
	}

	var offenders []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == pkg {
			offenders = append(offenders, line)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("package %q is linked into the production binary %s.\n\n"+
			"Something under %s imports it from a NON-test .go file. Find the\n"+
			"edge with:\n\n"+
			"    go list -deps -json %s | grep -B5 '\"%s\"'\n\n"+
			"Test-only helpers belong in a `_test.go` file beside the component\n"+
			"they serve (compiled only into that package's test binary), or in a\n"+
			"package cmd/ does not import (e.g. internal/testfactory) when they\n"+
			"must be importable across packages.",
			pkg, target, target, target, pkg)
	}
}
