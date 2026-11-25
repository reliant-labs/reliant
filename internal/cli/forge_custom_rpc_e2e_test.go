//go:build e2e

package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gizmoWireE2E is a Gizmo quintet's wire messages; the rpcs are the caller's
// to declare (marked or not).
const gizmoWireE2E = `
message Gizmo {
  string id = 1;
  string name = 2;
  int32 status = 3;
}
message CreateGizmoRequest { string name = 1; }
message CreateGizmoResponse { Gizmo gizmo = 1; }
message GetGizmoRequest { string id = 1; }
message GetGizmoResponse { Gizmo gizmo = 1; }
message UpdateGizmoRequest { Gizmo gizmo = 1; }
message UpdateGizmoResponse { Gizmo gizmo = 1; }
message DeleteGizmoRequest { string id = 1; }
message DeleteGizmoResponse {}
message ListGizmosRequest { string project_id = 1; }
message ListGizmosResponse { repeated Gizmo gizmos = 1; }
`

// gizmoRPCsE2E declares the quintet, every rpc prefixed by marker (a full
// comment line, or "").
func gizmoRPCsE2E(marker string) string {
	var b strings.Builder
	for _, rpc := range []string{"CreateGizmo", "GetGizmo", "UpdateGizmo", "DeleteGizmo", "ListGizmos"} {
		if marker != "" {
			b.WriteString("  " + marker + "\n")
		}
		b.WriteString("  rpc " + rpc + "(" + rpc + "Request) returns (" + rpc + "Response);\n")
	}
	return b.String()
}

// authorGizmoE2E adds the Gizmo rpcs (inside the service block), the wire
// messages, any extra proto text, and a `gizmos` table migration.
func authorGizmoE2E(t *testing.T, projectDir, rpcs, extra string) {
	t.Helper()
	protoPath := filepath.Join(projectDir, "proto", "services", "widget", "v1", "widget.proto")
	src := readFileE2E(t, protoPath)
	const anchor = "  // TODO: Add your RPC methods here.\n"
	if !strings.Contains(src, anchor) {
		t.Fatalf("widget.proto has no %q anchor — fixture is stale:\n%s", anchor, src)
	}
	src = strings.Replace(src, anchor, rpcs, 1) + gizmoWireE2E + extra
	if err := os.WriteFile(protoPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	migDir := filepath.Join(projectDir, "db", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mig := "CREATE TABLE gizmos (\n    id TEXT PRIMARY KEY,\n    name TEXT NOT NULL,\n    status INTEGER NOT NULL DEFAULT 0\n);\n"
	if err := os.WriteFile(filepath.Join(migDir, "20260101000000_create_gizmos.up.sql"), []byte(mig), 0o644); err != nil {
		t.Fatal(err)
	}
}

// assertGizmoStubbedE2E: every Gizmo rpc got a pb-through stub and a scaffold
// test row, and none got CRUD wiring.
func assertGizmoStubbedE2E(t *testing.T, widgetDir string) {
	t.Helper()
	for _, f := range []string{"create_gizmo", "get_gizmo", "update_gizmo", "delete_gizmo", "list_gizmos"} {
		stub := filepath.Join(widgetDir, "rpc_"+f+".go")
		if !strings.Contains(readFileE2E(t, stub), "forge:gen unwired-stub") {
			t.Errorf("%s is not a pb-through stub", stub)
		}
		if _, err := os.Stat(filepath.Join(widgetDir, "handlers_scaffold_"+f+"_test.go")); err != nil {
			t.Errorf("no scaffold test row for %s: %v", f, err)
		}
	}
	for _, f := range []string{"handlers_crud.go", "handlers_crud_ops_gen.go"} {
		if b, err := os.ReadFile(filepath.Join(widgetDir, f)); err == nil && strings.Contains(string(b), "Gizmo") {
			t.Errorf("%s wires Gizmo CRUD although no Gizmo rpc is CRUD", f)
		}
	}
}

// auditE2E runs `forge project audit --json` (which exits non-zero on an
// error-status category the fixture does not care about) and decodes it.
func auditE2E(t *testing.T, projectDir, forgeBin string) map[string]struct {
	Status  string         `json:"status"`
	Summary string         `json:"summary"`
	Details map[string]any `json:"details"`
} {
	t.Helper()
	cmd := exec.Command(forgeBin, "project", "audit", "--json")
	cmd.Dir = projectDir
	out, _ := cmd.Output()
	var report struct {
		Categories map[string]struct {
			Status  string         `json:"status"`
			Summary string         `json:"summary"`
			Details map[string]any `json:"details"`
		} `json:"categories"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("parse audit JSON: %v\n%s", err, out)
	}
	return report.Categories
}

// TestE2EForgeCustomRPC drives `// forge:custom` through the real pipeline.
//
//  1. A table whose five CRUD-named rpcs are all marked is no CRUD entity:
//     five stubs and test rows, no CRUD wiring — and the project builds. It
//     keeps its store: the ORM projects every table, whatever its rpcs
//     (`forge:no-entity` on the table is the store opt-out). ListGizmos's plain `project_id` filter, which the
//     CRUD projection would reject for naming no column, is the author's.
//  2. The audit lists the rpcs as custom and finds none unhandled.
//  3. Marking an rpc that WAS CRUD refuses generate while its shim still
//     calls the op forge stops generating, naming file and line; once the
//     shim and the lifecycle test that used its factory are gone, the rpc
//     gets a stub and the op is gone.
func TestE2EForgeCustomRPC(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "customapp", "--mod", "example.com/customapp", "--service", "widget")
	projectDir := filepath.Join(dir, "customapp")
	addCorpusForgePkgReplace(t, projectDir)
	authorGizmoE2E(t, projectDir, gizmoRPCsE2E("// forge:custom — gizmos are minted against a serial ledger."),
		"\n// forge:entity\nmessage Gadget { string name = 2; }\n")
	runCmd(t, projectDir, forgeBin, "scaffold")

	// ── (1) an all-custom table is no CRUD entity, but keeps its store ────
	widgetDir := filepath.Join(projectDir, "internal", "handlers", "widget")
	if _, err := os.Stat(filepath.Join(projectDir, "internal", "db", "gizmo_orm_gen.go")); err != nil {
		t.Errorf("every Gizmo rpc is forge:custom, but the gizmos table must still get its store: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "internal", "db", "gadget_orm_gen.go")); err != nil {
		t.Errorf("the unmarked Gadget entity lost its ORM: %v", err)
	}
	assertGizmoStubbedE2E(t, widgetDir)
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./internal/handlers/...")

	// ── (2) the audit sees them ───────────────────────────────────────────
	cats := auditE2E(t, projectDir, forgeBin)
	if c := cats["unhandled_rpcs"]; c.Status != "ok" {
		t.Errorf("unhandled_rpcs = %s (%s), want ok", c.Status, c.Summary)
	}
	raw, _ := json.Marshal(cats["shape"].Details["services"])
	var services []struct {
		RPCs []struct {
			Name   string `json:"name"`
			Custom bool   `json:"custom"`
		} `json:"rpcs"`
	}
	if err := json.Unmarshal(raw, &services); err != nil {
		t.Fatalf("decode shape.services: %v\n%s", err, raw)
	}
	custom := map[string]bool{}
	for _, svc := range services {
		for _, r := range svc.RPCs {
			custom[r.Name] = r.Custom
		}
	}
	if !custom["CreateGizmo"] || !custom["ListGizmos"] || custom["CreateGadget"] {
		t.Errorf("audit shape custom flags = %v, want every Gizmo rpc custom and Gadget's not", custom)
	}

	// ── (3) CRUD → custom ─────────────────────────────────────────────────
	protoPath := filepath.Join(projectDir, "proto", "services", "widget", "v1", "widget.proto")
	src := readFileE2E(t, protoPath)
	const createGadget = "  rpc CreateGadget("
	if !strings.Contains(src, createGadget) {
		t.Fatalf("scaffold did not inject CreateGadget:\n%s", src)
	}
	src = strings.Replace(src, createGadget, "  // forge:custom — a gadget is minted from a template.\n"+createGadget, 1)
	if err := os.WriteFile(protoPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runCmdAllowFail(t, projectDir, forgeBin, "generate")
	if err == nil {
		t.Fatalf("generate must refuse a forge:custom rpc whose shim still calls its op:\n%s", out)
	}
	if v := lineContainingE2E(out, "still used as CRUD"); !strings.Contains(v, "internal/handlers/widget/handlers_crud.go:") || !strings.Contains(v, "uses crudCreateGadgetOp") {
		t.Errorf("refusal must name the shim's file:line and the op; got %q\nfull output:\n%s", v, out)
	}

	shimPath := filepath.Join(widgetDir, "handlers_crud.go")
	rest, _ := cutServiceMethodE2E(t, readFileE2E(t, shimPath), "CreateGadget")
	writeGoPrunedE2E(t, shimPath, rest)
	if err := os.Remove(filepath.Join(widgetDir, "handlers_crud_test.go")); err != nil {
		t.Fatal(err) // the lifecycle test calls NewCreateGadgetRequest, a CRUD Create's factory
	}
	runCmd(t, projectDir, forgeBin, "generate")
	if !strings.Contains(readFileE2E(t, filepath.Join(widgetDir, "rpc_create_gadget.go")), "forge:gen unwired-stub") {
		t.Error("CreateGadget, now forge:custom, did not get a pb-through stub")
	}
	if strings.Contains(readFileE2E(t, filepath.Join(widgetDir, "handlers_crud_ops_gen.go")), "crudCreateGadgetOp") {
		t.Error("handlers_crud_ops_gen.go still emits crudCreateGadgetOp for a forge:custom rpc")
	}
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./internal/handlers/...")
}

// TestE2ENoORMStubsCRUDNamedRPCs pins the silent gap `//forge:no-orm` left:
// a CRUD-named rpc over a real table got no stub (the stub step skipped it as
// CRUD) and no shim (the CRUD step is off with the ORM), so it served
// Unimplemented with nothing in the tree or the audit saying so. With no ORM
// there is no generic CRUD, so every rpc is stubbed.
func TestE2ENoORMStubsCRUDNamedRPCs(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "noormapp", "--mod", "example.com/noormapp", "--service", "widget")
	projectDir := filepath.Join(dir, "noormapp")
	addCorpusForgePkgReplace(t, projectDir)
	authorGizmoE2E(t, projectDir, gizmoRPCsE2E(""), "")
	dbDir := filepath.Join(projectDir, "internal", "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "// Package db is the hand-owned repository layer.\n//\n//forge:no-orm: the repository is hand-written; entities are not projected\npackage db\n"
	if err := os.WriteFile(filepath.Join(dbDir, "doc.go"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	runCmd(t, projectDir, forgeBin, "generate")

	assertGizmoStubbedE2E(t, filepath.Join(projectDir, "internal", "handlers", "widget"))
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./internal/handlers/...")
	if c := auditE2E(t, projectDir, forgeBin)["unhandled_rpcs"]; c.Status != "ok" {
		t.Errorf("unhandled_rpcs = %s (%s), want ok", c.Status, c.Summary)
	}
}
