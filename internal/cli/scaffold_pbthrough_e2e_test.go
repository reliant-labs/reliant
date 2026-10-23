//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// TestE2EPbThroughCrudPlusCustomRpc is the acceptance gate for the
// "pb-through" collapse: forge's two RPC codegen architectures are now
// ONE shape — every RPC is a method on the handler package's *Service
// working with pb (wire) types. A CRUD RPC delegates to the generated
// pkg/crud ops; a custom RPC is an owned pb-through stub. The separate
// per-service domain package (internal/<svc>) the old "typed vertical"
// created — and the svcWidget/SvcWidget collision flip it forced — are
// GONE.
//
// Scenario: a fresh project with, in ONE service, BOTH a CRUD entity
// (proto CRUD-shape message marked `// forge:entity` + its born table)
// AND a genuine custom (non-CRUD) RPC. Runs the REAL pipeline (buf
// compile + embedded postgres shadow-apply) and asserts:
//   - new → scaffold → generate → generate → generate → build → vet all
//     green, and IDEMPOTENT (3 back-to-back generates = zero file churn);
//   - NO internal/<svc> domain package is created by any default path;
//   - NO svcWidget/pkgWidget/SvcWidget identity flip in compose.go /
//     pkg/app/testing.go;
//   - the custom RPC is a pb-through *Service method in the handler pkg;
//   - the CRUD RPCs delegate to the generated ops (handlers_crud.go +
//     handlers_crud_ops_gen.go).
func TestE2EPbThroughCrudPlusCustomRpc(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "pbapp", "--mod", "example.com/pbapp", "--service", "widget")
	projectDir := filepath.Join(dir, "pbapp")
	addCorpusForgePkgReplace(t, projectDir)

	// No example RPC surface is shipped: fresh service, empty proto stub.
	assertPathNotExistsE2E(t, filepath.Join(projectDir, "internal", "handlers", "widget",
		codegen.RPCHandlerFileName("Ping")))
	assertPathNotExistsE2E(t, filepath.Join(projectDir, "internal", "widget"))

	// Author, in ONE service proto: a genuine custom (non-CRUD) RPC AND a
	// `// forge:entity`-marked CRUD entity. `forge scaffold` births the
	// entity's table + injects its CRUD quintet; the custom RPC surfaces as
	// a pb-through stub.
	protoPath := filepath.Join(projectDir, "proto", "services", "widget", "v1", "widget.proto")
	proto := readFileE2E(t, protoPath)
	proto = strings.Replace(proto, "  // TODO: Add your RPC methods here.",
		"  rpc Ping(PingRequest) returns (PingResponse);", 1)
	proto += "\nmessage PingRequest {}\nmessage PingResponse { string status = 1; }\n"
	proto += "\n// forge:entity\nmessage Gadget { string id = 1; string name = 2; }\n"
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("author widget proto: %v", err)
	}

	runCmd(t, projectDir, forgeBin, "scaffold")

	widgetDir := filepath.Join(projectDir, "internal", "handlers", "widget")

	// The custom RPC is a pb-through *Service method in its OWN file in the
	// handler package — one file per RPC, so two authors never collide.
	handlers := readFileE2E(t, filepath.Join(widgetDir, codegen.RPCHandlerFileName("Ping")))
	if !strings.Contains(handlers, "func (s *Service) Ping(") {
		t.Errorf("custom RPC Ping is not a pb-through *Service method:\n%s", handlers)
	}
	// The stub answers with the SCAFFOLD-STUB sentinel, not a bare
	// Unimplemented. Both carry connect.CodeUnimplemented on the wire, but
	// only the sentinel means "forge wrote this and nobody replaced it":
	// keying the generated scaffold test row on the bare code made it
	// unfalsifiable, because a finished handler returns that code for its
	// own reasons (an unwired optional dep answers Unimplemented on a
	// harness that leaves deps nil). See svcerr.ErrScaffoldStub.
	// The stable on-the-wire reason header the row identifies the stub by
	// is stamped inside svcerr.ScaffoldStub, not spelled in the emitted
	// handler, so it is pinned there (pkg/svcerr, pkg/tdd) rather than by
	// string-matching this file.
	if !strings.Contains(handlers, "svcerr.ScaffoldStub(") {
		t.Errorf("custom RPC Ping stub should return the scaffold-stub sentinel via svcerr:\n%s", handlers)
	}

	// The CRUD RPCs delegate to the generated ops.
	assertPathExistsE2E(t, filepath.Join(widgetDir, "handlers_crud_ops_gen.go"))
	crudShim := readFileE2E(t, filepath.Join(widgetDir, "handlers_crud.go"))
	for _, verb := range []string{"CreateGadget", "GetGadget", "ListGadgets", "UpdateGadget", "DeleteGadget"} {
		if !strings.Contains(crudShim, "func (s *Service) "+verb+"(") {
			t.Errorf("CRUD RPC %s is not wired in handlers_crud.go:\n%s", verb, crudShim)
		}
	}

	// NO internal/<svc> domain package is created by any default path.
	assertPathNotExistsE2E(t, filepath.Join(projectDir, "internal", "widget"))

	// NO svcWidget/pkgWidget/SvcWidget identity flip anywhere it would land.
	compose := readFileE2E(t, filepath.Join(projectDir, "internal", "app", "compose.go"))
	for _, banned := range []string{"svcWidget", "SvcWidget", "pkgWidget"} {
		if strings.Contains(compose, banned) {
			t.Errorf("compose.go carries a collision-flip identity %q — no domain package should force one:\n%s", banned, compose)
		}
	}
	helpersRel := filepath.Join("internal", "handlers", "widget", "helpers_gen_test.go")
	testingGo := readFileE2E(t, filepath.Join(projectDir, helpersRel))
	if strings.Contains(testingGo, "NewTestSvcWidget") {
		t.Errorf("%s carries the flipped NewTestSvcWidget factory:\n%s", helpersRel, testingGo)
	}
	if !strings.Contains(testingGo, "NewTestWidget") {
		t.Errorf("%s should carry the plain NewTestWidget factory:\n%s", helpersRel, testingGo)
	}

	// Idempotency: three back-to-back generates must be byte-for-byte stable
	// (zero file churn), and build + vet green throughout.
	snaps := make([]map[string]string, 0, 3)
	for i := 0; i < 3; i++ {
		runCmd(t, projectDir, forgeBin, "generate")
		snaps = append(snaps, hashProjectTree(t, projectDir))
		runCmd(t, projectDir, "go", "build", "./...")
		runCmd(t, projectDir, "go", "vet", "./...")
	}
	for i := 1; i < len(snaps); i++ {
		if diff := diffTreeE2E(snaps[0], snaps[i]); diff != "" {
			t.Errorf("generate #%d is not idempotent vs #1 (file churn):\n%s", i+1, diff)
		}
	}
}

// TestE2EPbThroughStubToCrudTransition is the acceptance gate for the
// stub→CRUD transition: a CRUD-SHAPED RPC first authored with no backing
// table is a pb-through unwired stub; the moment a table makes it
// entity-backed, CRUD gen must EXCISE the marked stub and take over with
// a delegating shim — no duplicate-method compile error, builds green.
//
// This is RED before the fix: without excision, the marked stub stays in
// handlers.go and CRUD gen filters the method out (ScanExistingMethods
// counts the stub as user-implemented), so GetSprocket never delegates to
// CRUD — the two assertions below (marker gone; shim present) both fail.
func TestE2EPbThroughStubToCrudTransition(t *testing.T) {
	t.Parallel()
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "catalogapp", "--mod", "example.com/catalogapp", "--service", "catalog")
	projectDir := filepath.Join(dir, "catalogapp")
	addCorpusForgePkgReplace(t, projectDir)

	catalogDir := filepath.Join(projectDir, "internal", "handlers", "catalog")
	protoPath := filepath.Join(projectDir, "proto", "services", "catalog", "v1", "catalog.proto")

	// Author a CRUD-SHAPED RPC (GetSprocket) with NO backing table. Its
	// Sprocket message is present but unmarked, and the rpc is marked
	// `// forge:custom` — so at first generate it is a custom RPC (no
	// sprockets entity) and surfaces as an unwired stub. Unmarked, generate
	// refuses it: a CRUD-named rpc over a wire message with no table and no
	// hand-written method would serve Unimplemented with nothing saying why
	// (#962, codegen.UnmatchedCRUDRPCs).
	proto := readFileE2E(t, protoPath)
	proto = strings.Replace(proto, "  // TODO: Add your RPC methods here.",
		"  // forge:custom\n  rpc GetSprocket(GetSprocketRequest) returns (GetSprocketResponse);", 1)
	proto += "\nmessage GetSprocketRequest { string id = 1; }\n"
	proto += "message GetSprocketResponse { Sprocket sprocket = 1; }\n"
	proto += "message Sprocket { string id = 1; string name = 2; }\n"
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("author catalog proto: %v", err)
	}

	// First generate: GetSprocket is custom → pb-through unwired stub in its
	// own per-RPC file.
	runCmd(t, projectDir, forgeBin, "generate")
	sprocketStub := filepath.Join(catalogDir, codegen.RPCHandlerFileName("GetSprocket"))
	handlersBefore := readFileE2E(t, sprocketStub)
	if !strings.Contains(handlersBefore, "func (s *Service) GetSprocket(") {
		t.Fatalf("GetSprocket should start as a pb-through stub in %s:\n%s",
			codegen.RPCHandlerFileName("GetSprocket"), handlersBefore)
	}
	if !strings.Contains(handlersBefore, "unwired-stub symbol=catalog.GetSprocket") {
		t.Fatalf("GetSprocket stub should carry the unwired-stub marker:\n%s", handlersBefore)
	}
	runCmd(t, projectDir, "go", "build", "./...")

	// Make the RPC entity-backed: drop the custom marker and mark Sprocket
	// so `forge scaffold` births the sprockets table (GetSprocket already
	// present → quintet completion skips it and injects the other four).
	// Now GetSprocket is a CRUD op.
	proto = readFileE2E(t, protoPath)
	proto = strings.Replace(proto, "  // forge:custom\n  rpc GetSprocket(", "  rpc GetSprocket(", 1)
	proto = strings.Replace(proto, "message Sprocket {", "// forge:entity\nmessage Sprocket {", 1)
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("mark Sprocket entity: %v", err)
	}
	runCmd(t, projectDir, forgeBin, "scaffold")

	// The marked stub was EXCISED and CRUD took over — no duplicate method.
	// Its per-RPC file held nothing else, so excision removed the file too
	// rather than leaving a bare `package catalog` husk behind.
	if _, err := os.Stat(sprocketStub); !os.IsNotExist(err) {
		handlersAfter := readFileE2E(t, sprocketStub)
		t.Errorf("the marked GetSprocket unwired stub was NOT excised after the table was added:\n%s",
			handlersAfter)
	}
	crudShim := readFileE2E(t, filepath.Join(catalogDir, "handlers_crud.go"))
	if !strings.Contains(crudShim, "func (s *Service) GetSprocket(") {
		t.Errorf("GetSprocket should now be a delegating CRUD shim in handlers_crud.go:\n%s", crudShim)
	}

	// Builds green — no duplicate-method compile error.
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./...")
}

// diffTreeE2E reports files added, removed, or changed between two
// snapshots (empty string == identical trees).
func diffTreeE2E(before, after map[string]string) string {
	var diffs []string
	for rel := range before {
		if _, ok := after[rel]; !ok {
			diffs = append(diffs, "removed: "+rel)
		}
	}
	for rel, sum := range after {
		prev, ok := before[rel]
		switch {
		case !ok:
			diffs = append(diffs, "added: "+rel)
		case prev != sum:
			diffs = append(diffs, "changed: "+rel)
		}
	}
	sort.Strings(diffs)
	return strings.Join(diffs, "\n")
}
