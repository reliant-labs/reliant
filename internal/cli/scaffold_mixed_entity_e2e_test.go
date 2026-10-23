//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// TestE2ENightlyScaffoldHandlersFilePlusMarkedEntityCompiles is the regression
// gate for the Deps.DB-injection bug.
//
// A service whose handler package already carries a handlers.go (here: a
// pb-through stub for a custom RPC) plus a `// forge:entity`-marked
// message that gets a table born must still gain a `DB orm.Context` field
// on its Deps struct — the generated handlers_crud_ops_gen.go dereferences
// s.deps.DB. ensureDepsDBField used to SKIP injecting that field whenever a
// handlers.go existed, so the first real CRUD entity broke `go build` with
// `s.deps.DB undefined` (and, after the pipeline's stage-then-validate
// rollback swept the ops file, the even more confusing
// `s.crud<Verb><Entity>Op undefined`).
//
// With `project new` no longer shipping an example service, this test
// authors its own handlers.go trigger — a custom RPC — alongside the
// marked entity, so the "handlers.go exists → injection suppressed" gate is
// exercised. The existing scaffold compile-proofs STUB the generate
// pipeline, so they never ran real CRUD-ops generation and never caught
// this; this test runs the REAL pipeline (buf compile, migration
// shadow-apply, CRUD ops + shim + ORM + pages) and then `go build`.
//
// NIGHTLY (the TestE2ENightly prefix; internal/e2eshard). 44s median on a CI
// shard but 285-299s on a contended one, and no commit since it landed
// names it as the test that caught a regression: every other e2e test that
// scaffolds an entity and builds runs the same CRUD-ops path. nightly.yml
// runs it every day; a regression here is one day's commits to bisect.
func TestE2ENightlyScaffoldHandlersFilePlusMarkedEntityCompiles(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	// A fresh service project: an empty service proto stub, no RPCs, no
	// handlers.go yet.
	runCmd(t, dir, forgeBin, "project", "new", "mixedapp", "--mod", "example.com/mixedapp", "--service", "widget")
	projectDir := filepath.Join(dir, "mixedapp")
	addCorpusForgePkgReplace(t, projectDir)

	// Precondition: no example RPC surface is shipped — the service starts
	// with no handler file for Ping. If `project new` ever starts shipping
	// one again, this gate is exercising a state it no longer sets up and
	// should be re-pointed, not silently weakened.
	handlersPath := filepath.Join(projectDir, "internal", "handlers", "widget",
		codegen.RPCHandlerFileName("Ping"))
	assertPathNotExistsE2E(t, handlersPath)

	// Precondition: the Deps struct starts WITHOUT a DB field (the bug is
	// that it never gains one).
	serviceGoPath := filepath.Join(projectDir, "internal", "handlers", "widget", "service.go")
	if pre := readFileE2E(t, serviceGoPath); strings.Contains(pre, "orm.Context") {
		t.Fatalf("fresh service.go already carries a DB orm.Context field — this test no longer exercises the injection path:\n%s", pre)
	}

	// Author BOTH a custom RPC (which forces a pb-through handler file) AND a
	// marked entity (which gets a table born) into the SAME service proto.
	protoPath := filepath.Join(projectDir, "proto", "services", "widget", "v1", "widget.proto")
	proto := readFileE2E(t, protoPath)
	proto = strings.Replace(proto, "  // TODO: Add your RPC methods here.",
		"  rpc Ping(PingRequest) returns (PingResponse);", 1)
	proto += "\nmessage PingRequest {}\nmessage PingResponse { string status = 1; }\n"
	proto += "\n// forge:entity\nmessage Peptide { string id=1; string name=2; string sku=3; double price=4; bool rx_required=5; }\n"
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("append custom RPC + marked entity to widget proto: %v", err)
	}

	// The one-command flow: birth the marked entity (migration + CRUD
	// quintet), then run the real generate pipeline.
	runCmd(t, projectDir, forgeBin, "scaffold")

	// The custom RPC surfaced as a pb-through stub in a real handlers.go.
	assertPathExistsE2E(t, handlersPath)
	if h := readFileE2E(t, handlersPath); !strings.Contains(h, "func (s *Service) Ping(") {
		t.Errorf("custom RPC Ping was not emitted as a pb-through *Service method:\n%s", h)
	}

	// The marked entity's table was born and the CRUD ops projected.
	assertPathExistsE2E(t, filepath.Join(projectDir, "internal", "handlers", "widget", "handlers_crud_ops_gen.go"))
	migEntries, _ := os.ReadDir(filepath.Join(projectDir, "db", "migrations"))
	foundMig := false
	for _, e := range migEntries {
		if strings.HasSuffix(e.Name(), "_create_peptides.up.sql") {
			foundMig = true
		}
	}
	if !foundMig {
		t.Errorf("expected a create_peptides migration to be born; migrations dir: %v", migEntries)
	}

	// The fix: the generated ops reference s.deps.DB, so the Deps struct
	// MUST have grown the field (despite the handlers.go the custom RPC
	// created earlier in the same generate).
	serviceGo := readFileE2E(t, serviceGoPath)
	if !strings.Contains(serviceGo, "orm.Context") {
		t.Errorf("Deps.DB (orm.Context) was not injected into service.go despite a handlers.go — the generated CRUD ops will not compile:\n%s", serviceGo)
	}

	// The whole point: the rendered project compiles and vets clean.
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./...")
}
