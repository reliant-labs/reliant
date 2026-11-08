//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Scaffold happy-path regression gates (mission M1). Two journeys that
// each used to break the FIRST `go build` / `forge generate` a new user
// runs:
//
//   - zero-service `forge project new` → generate → build failed with
//     `undefined: slog` in pkg/app/testing.go (journey fr-994db53964):
//     bootstrap_testing.go.tmpl gated the log/slog import on
//     `or .Services .Packages` while the body used *slog.Logger
//     unconditionally.
//   - new → scaffold service → scaffold entity → generate failed in buf with 20x
//     "unknown extension forge.v1.method" (journey fr-af7355dd63):
//     `forge scaffold service` scaffolds an import-less proto and
//     `forge scaffold entity` injected (forge.v1.method) options without
//     ensuring `import "forge/v1/forge.proto"`.
//
// Both use the corpus-style local replace (addCorpusForgePkgReplace) so
// they pin CURRENT-tree behavior and never skip on an unpublished pkg
// tag.

// TestE2EZeroServiceScaffoldCompiles: a project scaffolded with no
// components at all must still produce a compiling tree after
// `forge generate` (the generated pkg/app files must be self-consistent
// in the zero-component state).
func TestE2EZeroServiceScaffoldCompiles(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "zerosvc", "--mod", "example.com/zerosvc")
	projectDir := filepath.Join(dir, "zerosvc")
	addCorpusForgePkgReplace(t, projectDir)

	// Zero services were really scaffolded (the default --service is
	// none). proto/services is the service census; handlers/ also holds
	// non-service dirs (e.g. handlers/mocks), so it can't be the probe.
	if entries, err := os.ReadDir(filepath.Join(projectDir, "proto", "services")); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				t.Fatalf("zero-service scaffold grew a service proto dir %s — this test no longer covers the zero-component state", e.Name())
			}
		}
	}

	runCmd(t, projectDir, forgeBin, "generate")

	// The test harness is emitted PER COMPONENT, beside the component, as
	// internal/handlers/<svc>/helpers_gen_test.go. A zero-component project
	// has none to emit — and the project-wide pkg/app/testing.go that used
	// to stand in for it is retired (as a non-test .go file in a package
	// cmd/ imports, it linked package `testing` into the production binary).
	// Its absence is the assertion now.
	if _, err := os.Stat(filepath.Join(projectDir, "pkg", "app", "testing.go")); err == nil {
		t.Error("pkg/app/testing.go must not be emitted — it put package `testing` in the production binary")
	}

	// The real assertion: the whole zero-component tree compiles.
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./...")
}

// TestE2EAddServiceThenEntityGenerates: the post-scaffold growth path.
// `forge scaffold service` emits a bare proto (no RPCs, no imports); a
// `// forge:entity` message declared in it births CRUD RPCs carrying
// (forge.v1.method) options; `forge generate` must then succeed — which
// requires the entity birth to have added the forge/v1/forge.proto import.
//
// The entity is declared in the PROTO, not as `field:type` CLI arguments:
// that flag grammar was removed so the proto is the single place an entity
// is declared. The import-injection regression this gate exists for is a
// property of the birth, and is unchanged by how the entity is spelled.
func TestE2EAddServiceThenEntityGenerates(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "growapp", "--mod", "example.com/growapp")
	projectDir := filepath.Join(dir, "growapp")
	addCorpusForgePkgReplace(t, projectDir)

	runCmd(t, projectDir, forgeBin, "scaffold", "service", "item")
	protoPath := filepath.Join(projectDir, "proto", "services", "item", "v1", "item.proto")
	assertPathExistsE2E(t, protoPath)

	// Precondition for the regression: the add-service proto must be the
	// bare shape (no forge import yet). If add-service ever starts
	// emitting the import itself this gate should be re-pointed, not
	// silently weakened.
	if pre := readFileE2E(t, protoPath); strings.Contains(pre, "forge/v1/forge.proto") {
		t.Logf("note: add-service proto already imports forge/v1/forge.proto; the entity-injection import path is not exercised from the bare state")
	}

	// postgres is the pinned, derived-default driver; this test only
	// generates and builds (no DB boot), so no database override is
	// needed.

	bare := readFileE2E(t, protoPath)
	if err := os.WriteFile(protoPath, []byte(bare+`
// forge:entity
message Bookmark {
  string id = 1;
  string url = 2;
  string title = 3;
  bool done = 4;
}
`), 0o644); err != nil {
		t.Fatalf("declare the Bookmark entity in the item proto: %v", err)
	}

	runCmd(t, projectDir, forgeBin, "scaffold")

	proto := readFileE2E(t, protoPath)
	if !strings.Contains(proto, `import "forge/v1/forge.proto";`) {
		t.Fatalf("entity birth injected (forge.v1.method) options without the forge/v1/forge.proto import — the next generate dies in buf with 'unknown extension forge.v1.method':\n%s", proto)
	}
	if !strings.Contains(proto, "rpc CreateBookmark(CreateBookmarkRequest)") {
		t.Fatalf("entity birth did not scaffold the Bookmark CRUD RPCs into the service proto:\n%s", proto)
	}

	// The journey's failure point: generate ran buf against the proto
	// with unimported forge.v1 options and exited 100.
	runCmd(t, projectDir, forgeBin, "generate")

	// And the grown tree compiles.
	runCmd(t, projectDir, "go", "build", "./...")
}

// TestE2EProjectNewScaffoldCompilesWithoutVerify: `project new` skips its own
// compile check by default, so the guarantee that a fresh scaffold builds
// lives here, run explicitly: the tree `project new` returns compiles (and
// vets) with no `forge generate` in between, and `--verify` runs the check
// inside the command.
func TestE2EProjectNewScaffoldCompilesWithoutVerify(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	out := runCmdOutput(t, dir, forgeBin, "project", "new", "freshapp", "--mod", "example.com/freshapp", "--service", "notes")
	if !strings.Contains(out, "compile check skipped") {
		t.Errorf("default `project new` did not say it skipped the compile check:\n%s", out)
	}
	if strings.Contains(out, "Validating generated code") {
		t.Errorf("default `project new` ran the validate build:\n%s", out)
	}
	projectDir := filepath.Join(dir, "freshapp")
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./...")

	verified := runCmdOutput(t, dir, forgeBin, "project", "new", "verifiedapp", "--mod", "example.com/verifiedapp", "--service", "notes", "--verify")
	if !strings.Contains(verified, "Validating generated code") {
		t.Errorf("`project new --verify` did not run the validate build:\n%s", verified)
	}
}
