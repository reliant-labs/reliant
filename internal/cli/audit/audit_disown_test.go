package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/cli/audittype"
)

// TestAuditCodegen_DisownedFiles pins the machine-readable disown
// surface in `forge project audit --json`:
//
//   - disowned entries (.forge/disowned.json — including ones the
//     legacy-manifest migration converted from `forked: true`) show up
//     under codegen.details.disowned_files with path / since / reason;
//   - the legacy forked_files key still exists but is ALWAYS an empty
//     array, with a deprecation note field (additive-extension
//     contract: keys are never repurposed);
//   - the summary counts disowned files but the status stays OK —
//     disowning is a legitimate end state, not a warning;
//   - disowned files are excluded from user_edited_gen_files even when
//     a stale certification marker survived in their bytes (drift on a
//     disowned path is the user's business).
func TestAuditCodegen_DisownedFiles(t *testing.T) {
	dir := t.TempDir()
	// Disowned wire_gen.go whose content drifted AFTER the disown — and
	// which (worst case) still carries a stale marker. Must not surface
	// as "modified": the disown record outranks the marker.
	staleMarked, ok := checksums.StampWithValue("pkg/app/wire_gen.go",
		[]byte("package app // edited after disown\n"),
		checksums.BodyHash([]byte("content at disown time")))
	if !ok {
		t.Fatal("wire_gen.go should be stampable")
	}
	full := filepath.Join(dir, "pkg", "app", "wire_gen.go")
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, staleMarked, 0o644); err != nil {
		t.Fatal(err)
	}
	// bootstrap.go: a legacy fork the migration converted — plain
	// user-owned bytes plus a disowned.json record carrying the fork-era
	// timestamp as DisownedAt.
	bootstrap := filepath.Join(dir, "pkg", "app", "bootstrap.go")
	if err := os.WriteFile(bootstrap, []byte("package app // ex-fork\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := &checksums.FileChecksums{Disowned: map[string]checksums.DisownedEntry{
		"pkg/app/wire_gen.go": {
			Reason:     "custom wiring",
			DisownedAt: "2026-06-01T00:00:00Z",
		},
		"pkg/app/bootstrap.go": {
			Reason:     "migrated from legacy .forge/checksums.json (legacy fork-era entry; the fork state was removed)",
			DisownedAt: "2026-01-01T00:00:00Z",
		},
	}}
	if err := checksums.Save(dir, state); err != nil {
		t.Fatal(err)
	}

	cat := auditCodegen(testFactory(auditAPIConfig{}), nil, dir)
	if cat.Status != audittype.StatusOK {
		t.Errorf("status = %s, want ok — disowned files are a legitimate end state (summary: %s)", cat.Status, cat.Summary)
	}
	if !strings.Contains(cat.Summary, "2 disowned") {
		t.Errorf("summary %q missing disowned count", cat.Summary)
	}

	disowned, ok := cat.Details["disowned_files"].([]auditDisownedFile)
	if !ok {
		t.Fatalf("disowned_files detail missing or wrong shape: %#v", cat.Details["disowned_files"])
	}
	if len(disowned) != 2 {
		t.Fatalf("disowned_files = %+v, want both entries", disowned)
	}
	if disowned[0].Path != "pkg/app/bootstrap.go" || disowned[0].Since != "2026-01-01T00:00:00Z" {
		t.Errorf("disowned_files[0] = %+v (the migrated legacy fork must keep its fork-era timestamp as since)", disowned[0])
	}
	if disowned[1].Path != "pkg/app/wire_gen.go" || disowned[1].Since != "2026-06-01T00:00:00Z" {
		t.Errorf("disowned_files[1] = %+v", disowned[1])
	}
	if disowned[1].Reason != "custom wiring" {
		t.Errorf("disowned_files[1].Reason = %q, want the disowned.json reason", disowned[1].Reason)
	}
	if _, ok := cat.Details["disowned_hint"]; !ok {
		t.Errorf("disowned_hint detail missing")
	}

	// Legacy key contract: always present, always empty, with a note.
	legacy, ok := cat.Details["forked_files"].([]auditDisownedFile)
	if !ok {
		t.Fatalf("legacy forked_files key missing or wrong shape: %#v", cat.Details["forked_files"])
	}
	if len(legacy) != 0 {
		t.Errorf("legacy forked_files must always be empty; got %+v", legacy)
	}
	note, _ := cat.Details["forked_files_note"].(string)
	if !strings.Contains(note, "deprecated") || !strings.Contains(note, "disowned_files") {
		t.Errorf("forked_files_note = %q, want a deprecation note pointing at disowned_files", note)
	}

	// Post-disown edits are not "drift".
	if mod, ok := cat.Details["user_edited_gen_files"]; ok {
		t.Errorf("disowned file surfaced as user-edited drift: %v", mod)
	}
}

// TestAuditCodegen_NoDisowned pins the empty-state shape: the legacy
// forked_files key + note are still emitted (consumers can rely on
// them unconditionally for the deprecation window) and no disowned
// keys appear. The steady state has NO .forge state files at all — the
// manifest-era empty checksums.json is gone.
func TestAuditCodegen_NoDisowned(t *testing.T) {
	dir := t.TempDir()

	cat := auditCodegen(testFactory(auditAPIConfig{}), nil, dir)
	if cat.Status != audittype.StatusOK {
		t.Errorf("status = %s, want ok", cat.Status)
	}
	legacy, ok := cat.Details["forked_files"].([]auditDisownedFile)
	if !ok || len(legacy) != 0 {
		t.Errorf("legacy forked_files must be an empty array even with no disowned files; got %#v", cat.Details["forked_files"])
	}
	if _, ok := cat.Details["disowned_files"]; ok {
		t.Errorf("disowned_files should be omitted when nothing is disowned")
	}
}

// TestAuditCodegen_DisownedFilesCarriesReason pins that disowned_files
// rows surface the --reason recorded in each path's .forge/disowned.json
// entry, and that a record predating reason capture stays reason-less.
func TestAuditCodegen_DisownedFilesCarriesReason(t *testing.T) {
	dir := t.TempDir()
	content := "package app // user-owned\n"
	for _, rel := range []string{"pkg/app/wire_gen.go", "pkg/app/bootstrap.go", "pkg/app/migrate.go"} {
		writeFileTest(t, dir, rel, content)
	}
	csState := &checksums.FileChecksums{Disowned: map[string]checksums.DisownedEntry{
		"pkg/app/wire_gen.go":  {Reason: "custom pool wiring forge can't express", DisownedAt: "2026-06-01T00:00:00Z"},
		"pkg/app/bootstrap.go": {Reason: "legacy fork-era reason", DisownedAt: "2026-06-01T00:00:00Z"},
		// migrate.go: record predates reason capture — no reason.
		"pkg/app/migrate.go": {DisownedAt: "2026-06-01T00:00:00Z"},
	}}
	if err := checksums.Save(dir, csState); err != nil {
		t.Fatal(err)
	}

	f := testFactory(auditAPIConfig{})

	cat := auditCodegen(f, nil, dir)
	disowned, ok := cat.Details["disowned_files"].([]auditDisownedFile)
	if !ok {
		t.Fatalf("disowned_files detail missing or wrong shape: %#v", cat.Details["disowned_files"])
	}
	if len(disowned) != 3 {
		t.Fatalf("disowned_files = %+v, want all three disowned entries", disowned)
	}
	byPath := map[string]auditDisownedFile{}
	for _, fnd := range disowned {
		byPath[fnd.Path] = fnd
	}
	if got := byPath["pkg/app/wire_gen.go"].Reason; got != "custom pool wiring forge can't express" {
		t.Errorf("wire_gen.go reason = %q, want the manifest --reason text", got)
	}
	if got := byPath["pkg/app/bootstrap.go"].Reason; got != "legacy fork-era reason" {
		t.Errorf("bootstrap.go reason = %q, want the manifest --reason text", got)
	}
	if got := byPath["pkg/app/migrate.go"].Reason; got != "" {
		t.Errorf("migrate.go reason = %q, want empty (record predates reason capture)", got)
	}
}
