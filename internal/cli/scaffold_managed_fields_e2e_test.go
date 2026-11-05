//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EScaffoldMarkedEntityDeclaresManagedFields is the end-to-end gate
// for the managed fields on a hand-authored `// forge:entity` message.
//
// The managed fields used to exist by CONVENTION: the message declared no
// `id`, the shipped proto skill said never to declare one, and every
// emitter assumed one anyway. One `forge scaffold` run therefore produced
// BOTH a message without `id` AND a born CRUD test calling `GetId()` on it
// — `go vet` failed on a by-the-book scaffold, 100% of the time, plus a
// TypeScript error on every generated edit page.
//
// Birth now DECLARES the fields in the author's own message, so the proto
// is the whole truth. The test pins the three properties that make that
// edit safe to run over a file someone else owns:
//
//   - the fields appear (id / created_at / updated_at), and the project
//     compiles and vets clean afterwards — the original repro;
//   - author-written field numbers are never renumbered (a renumber is a
//     wire-breaking change, a worse bug than the one being fixed);
//   - a message that already declares them is a clean no-op — no
//     duplicate declaration, no retyping.
func TestE2EScaffoldMarkedEntityDeclaresManagedFields(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "mfapp", "--mod", "example.com/mfapp", "--service", "widget")
	projectDir := filepath.Join(dir, "mfapp")
	addCorpusForgePkgReplace(t, projectDir)

	// Gadget declares NOTHING managed — the by-the-book shape the shipped
	// docs produced. Doodad already declares all three by hand.
	protoPath := filepath.Join(projectDir, "proto", "services", "widget", "v1", "widget.proto")
	proto := readFileE2E(t, protoPath)
	proto += "\n// forge:entity\n" +
		"message Gadget {\n" +
		"  string name = 1;\n" +
		"}\n" +
		"\n// forge:entity\n" +
		"message Doodad {\n" +
		"  string id = 1;\n" +
		"  string label = 2;\n" +
		"  google.protobuf.Timestamp created_at = 3;\n" +
		"  google.protobuf.Timestamp updated_at = 4;\n" +
		"}\n"
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("append entities to widget proto: %v", err)
	}

	runCmd(t, projectDir, forgeBin, "scaffold")

	genProto := readFileE2E(t, protoPath)

	// Gadget: the managed fields are DECLARED, and `name` keeps its number.
	gadget := sliceBetweenCLI(genProto, "message Gadget {", "}")
	if gadget == "" {
		t.Fatalf("message Gadget vanished from the proto:\n%s", genProto)
	}
	for _, want := range []string{"string id =", "google.protobuf.Timestamp created_at =", "google.protobuf.Timestamp updated_at ="} {
		if !strings.Contains(gadget, want) {
			t.Errorf("birth did not declare %q on the entity message:\n%s", want, gadget)
		}
	}
	if !strings.Contains(gadget, "string name = 1;") {
		t.Errorf("birth renumbered an author-written field (wire-breaking):\n%s", gadget)
	}
	// Soft delete is opt-in: no marker, no column, so no field either.
	if strings.Contains(gadget, "deleted_at") {
		t.Errorf("deleted_at declared without `// forge:soft-delete` — the table has no such column:\n%s", gadget)
	}

	// Doodad: already complete — injected nothing, duplicated nothing.
	doodad := sliceBetweenCLI(genProto, "message Doodad {", "}")
	if doodad == "" {
		t.Fatalf("message Doodad vanished from the proto:\n%s", genProto)
	}
	for _, managed := range []string{"id", "created_at", "updated_at"} {
		if n := strings.Count(doodad, " "+managed+" = "); n != 1 {
			t.Errorf("managed field %q declared %d times on a message that already had it:\n%s", managed, n, doodad)
		}
	}

	// The born CRUD test calls Get<Pk>() on the entity — the exact call that
	// used to fail to compile. The whole project must build and vet clean.
	crudTest := readFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "widget", "handlers_crud_test.go"))
	if !strings.Contains(crudTest, "GetId()") {
		t.Fatalf("born CRUD test no longer exercises the entity id — the regression this pins is gone:\n%s", crudTest)
	}
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./...")
}
