// Tests for the Tier-2 drift exemption (generate_tier_migrate.go) and
// the one-time legacy-manifest pipeline migration
// (generate_legacy_migrate.go).
//
// The manifest-era tier-reclassification step (migrateTemplateTiers /
// migrateLegacyForks flipping entries inside .forge/checksums.json) is
// gone with the manifest. Its two jobs survive in new homes, covered
// here:
//
//   - sanctioned edits to Tier-2-managed starters must not trip the
//     stomp guard → generator.FilterTier2Managed / scanProjectDrift;
//   - legacy forked/disowned manifest entries must convert to
//     .forge/disowned.json (and pristine Tier-1 entries to embedded
//     markers) → stepMigrateLegacyManifest + finishLegacyMigration over
//     checksums.MigrateLegacyManifest.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/templates"
)

// TestScanProjectDrift_FiltersTier2ManagedPaths locks the drift
// exemption: a hand-edit to a Tier-2-managed starter (sanctioned) is
// dropped from the shared drift probe, while a hand-edit to an honest
// Tier-1 file stays in.
func TestScanProjectDrift_FiltersTier2ManagedPaths(t *testing.T) {
	dir := t.TempDir()
	cs := &generator.FileChecksums{}

	writeModified := func(rel, edited, original string) {
		t.Helper()
		stamped, ok := checksums.StampWithValue(rel, []byte(edited), checksums.BodyHash([]byte(original)))
		if !ok {
			t.Fatalf("stamp %s: unstampable", rel)
		}
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, stamped, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Tier-2-managed starter, hand-edited (marker fails verification):
	// sanctioned — must be filtered out.
	if !generator.IsTier2Managed("Taskfile.yml") {
		t.Fatal("Taskfile.yml is no longer Tier-2-managed; pick another exempt path")
	}
	writeModified("Taskfile.yml", "# user-tuned tasks\n", "# scaffolded tasks\n")

	// Honest Tier-1 file, hand-edited: must stay in the drift set.
	writeModified("pkg/app/wire_gen.go", "package app // edited\n", "package app // generated\n")

	drift := scanProjectDrift(dir, cs)
	if len(drift) != 1 || drift[0].Path != "pkg/app/wire_gen.go" {
		t.Fatalf("scanProjectDrift = %+v, want exactly the wire_gen.go entry (Taskfile.yml exempt)", drift)
	}

	// Unit check on the filter itself.
	raw := []checksums.Tier1DriftEntry{
		{Path: "Taskfile.yml"},
		{Path: "pkg/app/wire_gen.go"},
	}
	kept := generator.FilterTier2Managed(raw)
	if len(kept) != 1 || kept[0].Path != "pkg/app/wire_gen.go" {
		t.Errorf("FilterTier2Managed = %+v, want only wire_gen.go", kept)
	}
}

// TestStepCheckTier1Drift_Tier2ManagedEditDoesNotTrip drives the real
// guard step over a project whose ONLY drift is a hand-edited
// Tier-2-managed starter — the guard must wave the run through.
func TestStepCheckTier1Drift_Tier2ManagedEditDoesNotTrip(t *testing.T) {
	dir := t.TempDir()
	stamped, ok := checksums.StampWithValue("Taskfile.yml",
		[]byte("# user-tuned tasks\n"),
		checksums.BodyHash([]byte("# scaffolded tasks\n")))
	if !ok {
		t.Fatal("Taskfile.yml should be stampable")
	}
	mustWriteScopeFile(t, filepath.Join(dir, "Taskfile.yml"), string(stamped))

	ctx := &pipelineContext{ProjectDir: dir, AbsPath: dir, Checksums: &generator.FileChecksums{}}
	if err := stepCheckTier1Drift(ctx); err != nil {
		t.Errorf("sanctioned Tier-2-managed edit tripped the stomp guard: %v", err)
	}
}

// TestScanProjectDrift_NilAndEmpty covers the degenerate inputs the
// pipeline step can hand over (no ownership state yet / fresh project).
func TestScanProjectDrift_NilAndEmpty(t *testing.T) {
	dir := t.TempDir()
	if got := scanProjectDrift(dir, nil); len(got) != 0 {
		t.Errorf("nil cs: got %+v, want empty", got)
	}
	if got := scanProjectDrift(dir, &generator.FileChecksums{}); len(got) != 0 {
		t.Errorf("empty cs: got %+v, want empty", got)
	}
}

// TestTier2ManagedPathsContents locks the registry-derived exemption set:
// the starter-class files cp-forge forked must be present; files that
// `forge generate` re-renders every run (Tier-1) must NOT be.
func TestTier2ManagedPathsContents(t *testing.T) {
	set := generator.Tier2ManagedPaths()

	for _, want := range []string{
		"Dockerfile",
		"Taskfile.yml",
		"docker-compose.yml",
		".golangci.yml",
		".gitignore",
		"pkg/middleware/middleware.go",
		// buf.yaml is Tier-2: a pristine copy tracks the api.rest-derived
		// googleapis dep, but its `lint.except` block is a sanctioned
		// hand-edit home (`forge lint --suggest-buf-excepts` prints a snippet
		// to paste in), so user edits must be respected, not stomped.
		"buf.yaml",
		// One-shot .github scaffolds written only at `forge project new` time.
		".github/CODEOWNERS",
		".github/pull_request_template.md",
		// The CI workflows, all written by writeCIScaffold: write-once,
		// user-owned from birth, no marker.
		//
		// These used to be listed in the reject set below, on the reasoning
		// that a marker-less file never enters the drift scan and so needs
		// no exemption. True for a project scaffolded today — and false for
		// every project scaffolded before the workflows became write-once,
		// which still has forge's Tier-1 forge:hash marker in its e2e.yml.
		// Editing that file is sanctioned now, but the scan read the stale
		// marker as a hand-edited generated file and aborted `forge
		// generate` wholesale, with no extension point it could name
		// because the whole file is the user's. The exemption is what makes
		// the tier registry agree with the writer for both vintages.
		".github/workflows/ci.yml",
		".github/workflows/e2e.yml",
		".github/workflows/deploy.yml",
		".github/workflows/build-images.yml",
		".github/workflows/proto-breaking.yml",
		".github/dependabot.yml",
	} {
		if !set[want] {
			t.Errorf("Tier2ManagedPaths() missing %q", want)
		}
	}

	for _, reject := range []string{
		// Regenerated every run by stepRegenerateInfra — honest Tier-1.
		"internal/cli/serve.go",
		"internal/cli/server.go",
		"internal/cli/root.go",
		"cmd/main.go",
		"internal/cli/db.go",
		"internal/cli/version.go",
	} {
		if set[reject] {
			t.Errorf("Tier2ManagedPaths() must not contain %q (Tier-1 / generate-owned)", reject)
		}
	}
}

// TestEmitScaffoldOnceIfMissing_ExistingFileNeverTouched: a scaffold
// page that already exists on disk is preserved verbatim on every run —
// there is one scaffold tier now, and forge never overwrites an existing
// file (no flag). Refresh is delete-then-regenerate.
func TestEmitScaffoldOnceIfMissing_ExistingFileNeverTouched(t *testing.T) {
	dir := t.TempDir()
	rel := "frontends/web/src/app/page.tsx"
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	userContent := "// my fully rewritten page\n"
	if err := os.WriteFile(full, []byte(userContent), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := emitScaffoldOnceIfMissing(dir, rel, "nextjs/src/app/page.tsx.tmpl",
		templates.FrontendTemplateData{FrontendName: "web", ProjectName: "demo"}); err != nil {
		t.Fatalf("emitScaffoldOnceIfMissing: %v", err)
	}
	got, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != userContent {
		t.Errorf("existing scaffold file was clobbered:\n%s", got)
	}

	// Delete-then-regenerate refreshes it from the template.
	if err := os.Remove(full); err != nil {
		t.Fatal(err)
	}
	if err := emitScaffoldOnceIfMissing(dir, rel, "nextjs/src/app/page.tsx.tmpl",
		templates.FrontendTemplateData{FrontendName: "web", ProjectName: "demo"}); err != nil {
		t.Fatalf("emitScaffoldOnceIfMissing (after delete): %v", err)
	}
	got, _ = os.ReadFile(full)
	if string(got) == userContent {
		t.Error("deleted scaffold should be re-rendered from the template")
	}
}

// writeLegacyMigrateFile writes content at root/rel, creating parents.
func writeLegacyMigrateFile(t *testing.T, root, rel string, content []byte) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestStepMigrateLegacyManifest pins the one-time pipeline conversion
// off the dead global manifest (the successor of the manifest-era
// migrateLegacyForks coverage; the per-entry conversion rules
// themselves live in checksums.MigrateLegacyManifest):
//
//   - a legacy `forked: true` entry converts to .forge/disowned.json,
//     inheriting the fork timestamp as the disowned-since time;
//   - a Tier-1 entry whose hash matches the on-disk bytes is stamped
//     with a verifying embedded marker;
//   - a Tier-1 entry whose bytes match NOTHING recorded is quarantined
//     on ctx.LegacyUnverified (writes side-render redirected);
//   - the legacy manifest is deleted;
//   - finishLegacyMigration rescues a quarantined path whose fresh side
//     render matches the on-disk bytes (stamps it pristine) and stamps
//     everything unrescued with the unverified-legacy sentinel,
//     returning an error that names the file.
func TestStepMigrateLegacyManifest(t *testing.T) {
	checksums.ResetSkipWrite()
	checksums.ResetPerRunState()
	defer checksums.ResetPerRunState()
	defer checksums.ResetSkipWrite()

	root := t.TempDir()

	forkedContent := []byte("package app // user fork\n")
	pristineContent := []byte("package app // as generated\n")
	rescuedContent := []byte("package app // matches the fresh render\n")
	orphanContent := []byte("package app // matches nothing\n")

	writeLegacyMigrateFile(t, root, "pkg/app/bootstrap.go", forkedContent)
	writeLegacyMigrateFile(t, root, "pkg/app/wire_gen.go", pristineContent)
	writeLegacyMigrateFile(t, root, "pkg/app/app_gen.go", rescuedContent)
	writeLegacyMigrateFile(t, root, "pkg/app/testing.go", orphanContent)

	legacy := `{
  "forge_version": "old",
  "files": {
    "pkg/app/bootstrap.go": {"hash": "stale", "tier": 1, "forked": true, "forked_at": "2026-01-01T00:00:00Z"},
    "pkg/app/wire_gen.go": {"hash": "` + checksums.Hash(pristineContent) + `", "tier": 1},
    "pkg/app/app_gen.go": {"hash": "recorded-from-another-lane", "tier": 1},
    "pkg/app/testing.go": {"hash": "also-from-another-lane", "tier": 1}
  }
}`
	writeLegacyMigrateFile(t, root, checksums.LegacyChecksumFile, []byte(legacy))

	cs := &generator.FileChecksums{
		Disowned:    map[string]checksums.DisownedEntry{},
		Unstampable: map[string]string{},
	}
	ctx := &pipelineContext{ProjectDir: root, AbsPath: root, Checksums: cs}
	if err := stepMigrateLegacyManifest(ctx); err != nil {
		t.Fatalf("stepMigrateLegacyManifest: %v", err)
	}

	// Legacy fork → disowned, with the fork-era timestamp.
	entry, ok := cs.Disowned["pkg/app/bootstrap.go"]
	if !ok {
		t.Fatalf("forked entry not converted to disowned; have %+v", cs.Disowned)
	}
	if entry.DisownedAt != "2026-01-01T00:00:00Z" {
		t.Errorf("DisownedAt = %q, want the inherited forked_at", entry.DisownedAt)
	}
	if !strings.Contains(entry.Reason, "fork") {
		t.Errorf("Reason = %q, want it to name the legacy fork conversion", entry.Reason)
	}
	// The persisted form: disowned.json written on save.
	if err := checksums.Save(root, cs); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, checksums.DisownedFile)); err != nil {
		t.Errorf("%s not written: %v", checksums.DisownedFile, err)
	}

	// Matching Tier-1 entry: stamped, verifies.
	wireGen, err := os.ReadFile(filepath.Join(root, "pkg", "app", "wire_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if checksums.Verify(wireGen) != checksums.Pristine {
		t.Errorf("pristine legacy entry not stamped with a verifying marker:\n%s", wireGen)
	}

	// Mismatching entries: quarantined for the side-render rescue.
	if len(ctx.LegacyUnverified) != 2 ||
		ctx.LegacyUnverified[0] != "pkg/app/app_gen.go" ||
		ctx.LegacyUnverified[1] != "pkg/app/testing.go" {
		t.Fatalf("LegacyUnverified = %v, want the two mismatching paths (sorted)", ctx.LegacyUnverified)
	}

	// The legacy manifest is gone — the migration is durable.
	if _, err := os.Stat(filepath.Join(root, checksums.LegacyChecksumFile)); !os.IsNotExist(err) {
		t.Errorf("legacy manifest still present after migration (stat err=%v)", err)
	}

	// Rescue setup: app_gen.go's "fresh render" (parked side render)
	// matches its on-disk bytes — provably pristine. testing.go gets no
	// render (its emitter never ran / content genuinely unknown).
	if err := checksums.WriteSideRenderNoBase(root, "pkg/app/app_gen.go", rescuedContent); err != nil {
		t.Fatal(err)
	}

	finishErr := finishLegacyMigration(ctx)
	if finishErr == nil {
		t.Fatal("finishLegacyMigration = nil; want the drift error naming the unrescued file")
	}
	if !strings.Contains(finishErr.Error(), "pkg/app/testing.go") {
		t.Errorf("error should name the unrescued file; got:\n%v", finishErr)
	}
	if strings.Contains(finishErr.Error(), "pkg/app/app_gen.go") {
		t.Errorf("rescued file should not appear in the error; got:\n%v", finishErr)
	}

	// Rescued: stamped pristine.
	appGen, err := os.ReadFile(filepath.Join(root, "pkg", "app", "app_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if checksums.Verify(appGen) != checksums.Pristine {
		t.Errorf("rescued file not stamped pristine:\n%s", appGen)
	}

	// Unrescued: stamped with the unverified-legacy sentinel so the
	// stomp guard keeps naming it on every run.
	testingGo, err := os.ReadFile(filepath.Join(root, "pkg", "app", "testing.go"))
	if err != nil {
		t.Fatal(err)
	}
	marker, found := checksums.ExtractMarker(testingGo)
	if !found || marker != checksums.UnverifiedMarkerValue {
		t.Errorf("unrescued file marker = %q (found=%v), want the %q sentinel",
			marker, found, checksums.UnverifiedMarkerValue)
	}
}

// TestStepMigrateLegacyManifest_NoManifestIsNoOp: the steady state —
// no legacy manifest — does nothing and touches nothing.
func TestStepMigrateLegacyManifest_NoManifestIsNoOp(t *testing.T) {
	root := t.TempDir()
	cs := &generator.FileChecksums{}
	ctx := &pipelineContext{ProjectDir: root, AbsPath: root, Checksums: cs}
	if err := stepMigrateLegacyManifest(ctx); err != nil {
		t.Fatalf("stepMigrateLegacyManifest on a clean project: %v", err)
	}
	if len(ctx.LegacyUnverified) != 0 {
		t.Errorf("LegacyUnverified = %v, want empty", ctx.LegacyUnverified)
	}
	if err := finishLegacyMigration(ctx); err != nil {
		t.Errorf("finishLegacyMigration no-op returned %v", err)
	}
}

// TestLegacyMigrationStampable pins the migration's Tier-2 exclusion: a
// legacy Tier-1 entry whose canonical template tier is Tier-2 (user-
// owned starter) must NOT be certified — a marker there would
// misrepresent sanctioned edits as drift.
func TestLegacyMigrationStampable(t *testing.T) {
	if legacyMigrationStampable("Taskfile.yml") {
		t.Error("Taskfile.yml is Tier-2-managed; the migration must not stamp it")
	}
	if !legacyMigrationStampable("pkg/app/wire_gen.go") {
		t.Error("wire_gen.go is honest Tier-1; the migration must stamp it")
	}
}
