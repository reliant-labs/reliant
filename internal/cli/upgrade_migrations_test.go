package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadMigrationMetas_RegistryIsWellFormed walks whatever the binary
// actually ships. An EMPTY registry is a valid, expected state — forge
// ships no migration until a release in the supported window breaks
// something — so this asserts shape, not count.
//
// The invariants below are what let the machinery stay correct as skills
// come and go: an ID that isn't the release version breaks
// `upgrade apply`, and a missing detection makes a migration unable to
// show it applies to anything.
func TestLoadMigrationMetas_RegistryIsWellFormed(t *testing.T) {
	metas, err := loadMigrationMetas()
	if err != nil {
		t.Fatalf("loadMigrationMetas: %v", err)
	}
	for _, m := range metas {
		if m.SkillPath != "migrations/"+m.ID {
			t.Errorf("migration %q: SkillPath = %q, want %q", m.ID, m.SkillPath, "migrations/"+m.ID)
		}
		// The directory name IS the release version. The CLI prints and
		// records the directory; the frontmatter is what an agent reads.
		// If they disagree, one of the two is lying.
		if m.Version != m.ID {
			t.Errorf("migration %q declares version %q — the directory name and `version:` frontmatter must match", m.ID, m.Version)
		}
		if semverKey(m.Version) == "" {
			t.Errorf("migration %q has unorderable version %q — it can never be compared against a project baseline", m.ID, m.Version)
		}
		// Detection is the state-based gate. Without it a migration
		// cannot demonstrate a project needs it.
		if strings.TrimSpace(m.Detection) == "" {
			t.Errorf("migration %q declares no detection script — it cannot show it applies to any project", m.ID)
		}
	}
}

// TestComputePendingMigrations_EmptyRegistryIsNotAnError is the
// zero-skill guarantee. forge currently ships no migrations, and the
// machinery must treat that as a normal answer rather than an error or a
// crash — this is the state the registry sits in between breaking
// releases, which is most of the time.
func TestComputePendingMigrations_EmptyRegistryIsNotAnError(t *testing.T) {
	metas, err := loadMigrationMetas()
	if err != nil {
		t.Fatalf("loadMigrationMetas: %v", err)
	}
	if len(metas) != 0 {
		t.Skipf("registry is non-empty (%d migrations) — this test pins the empty case", len(metas))
	}

	dir := newTestProject(t)
	var pending []pendingMigration
	withCwd(t, dir, func() {
		got, err := computePendingMigrations()
		if err != nil {
			t.Fatalf("computePendingMigrations on an empty registry: %v", err)
		}
		pending = got
	})
	if len(pending) != 0 {
		t.Errorf("empty registry produced %d pending migrations: %+v", len(pending), pending)
	}
}

// TestWritePendingMigrationsHuman_EmptyRegistrySaysSo distinguishes the
// two empty cases. "forge ships none" is a fact about the binary;
// "none apply to you" is a fact about the project. A user mid-upgrade
// who is told "up to date" when the real reason is that the binary has
// no migrations at all has been told the wrong thing.
func TestWritePendingMigrationsHuman_EmptyRegistrySaysSo(t *testing.T) {
	var buf bytes.Buffer
	if err := writePendingMigrationsHuman(&buf, nil, 0 /* shipped */); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "No migrations shipped") {
		t.Errorf("empty-registry message should say the binary ships none, got: %q", out)
	}

	// With migrations shipped but none applicable, the answer is about
	// the project instead.
	buf.Reset()
	if err := writePendingMigrationsHuman(&buf, nil, 3 /* shipped */); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(buf.String(), "up to date") {
		t.Errorf("expected 'up to date' when migrations exist but none apply, got: %q", buf.String())
	}
}

// TestRunUpgradeApply_EmptyRegistryGivesAnHonestHint: on a binary with no
// migrations, "available IDs: " followed by nothing is a dead end. Say
// there are none.
func TestRunUpgradeApply_EmptyRegistryGivesAnHonestHint(t *testing.T) {
	metas, err := loadMigrationMetas()
	if err != nil {
		t.Fatalf("loadMigrationMetas: %v", err)
	}
	if len(metas) != 0 {
		t.Skipf("registry is non-empty (%d migrations) — this test pins the empty case", len(metas))
	}

	dir := newTestProject(t)
	withCwd(t, dir, func() {
		var buf bytes.Buffer
		err := runUpgradeApply(&buf, "v0.5.0")
		if err == nil {
			t.Fatal("expected an error applying an unknown migration")
		}
		if !strings.Contains(err.Error(), "ships no migrations") {
			t.Errorf("error should say the binary ships no migrations, got: %v", err)
		}
	})
}

// TestParseMigrationFrontmatter_ExtractsAllFields exercises the
// migration-specific parser. Quoted values must be unquoted; missing
// fields should leave the meta empty (not error).
func TestParseMigrationFrontmatter_ExtractsAllFields(t *testing.T) {
	body := []byte(`---
name: v0.5.0
description: the deploy target moved from forge.yaml into KCL
version: "v0.5.0"
detection: 'grep -q "^dev_target:" forge.yaml'
---

# body
`)
	m := parseMigrationFrontmatter(body)
	if m.Title != "v0.5.0" {
		t.Errorf("Title = %q, want v0.5.0", m.Title)
	}
	if m.Description != "the deploy target moved from forge.yaml into KCL" {
		t.Errorf("Description = %q", m.Description)
	}
	if m.Version != "v0.5.0" {
		t.Errorf("Version = %q (quotes should be stripped)", m.Version)
	}
	if m.Detection != `grep -q "^dev_target:" forge.yaml` {
		t.Errorf("Detection = %q", m.Detection)
	}
}

// An unquoted detection that ENDS in a quoted shell argument keeps its
// closing quote. Stripping quote characters from each end independently cut
// `"$f"` to `"$f`, the shell rejected the script, and the migration matched
// no project at all — with nothing reported, since detection is silent.
func TestParseMigrationFrontmatter_KeepsAnUnpairedTrailingQuote(t *testing.T) {
	body := []byte("---\nversion: v0.5.0\ndetection: f=forge.yaml; grep -q x \"$f\"\n---\n")
	if got, want := parseMigrationFrontmatter(body).Detection, `f=forge.yaml; grep -q x "$f"`; got != want {
		t.Errorf("Detection = %q, want %q", got, want)
	}
}

// TestParseMigrationFrontmatter_NoFrontmatter returns an empty meta
// rather than panicking on bodies without a leading "---\n".
func TestParseMigrationFrontmatter_NoFrontmatter(t *testing.T) {
	m := parseMigrationFrontmatter([]byte("# just markdown\n"))
	if m.Title != "" || m.Description != "" || m.Version != "" {
		t.Errorf("expected empty meta, got %+v", m)
	}
}

// TestBaselinePrecedes is the version half of the applicability
// decision: has this project crossed the release the migration belongs
// to?
//
// Ordering is real SemVer precedence, which matters because the versions
// forge actually produces are not simple vX.Y.Z triples. A build from a
// local checkout stamps a Go pseudo-version —
// `v0.0.4-0.20260724212501-dfb85daf8474+dirty` — and SemVer says that is
// a PRE-RELEASE of v0.0.4: after v0.0.3, before v0.0.4, and unambiguously
// below v0.1.0. A component-wise string comparison gets that wrong in
// both directions, which is how a dev build ends up sorted against
// migrations it has nothing to do with.
func TestBaselinePrecedes(t *testing.T) {
	tests := []struct {
		name     string
		baseline string
		version  string
		want     bool
	}{
		{"below the release", "v0.4.9", "v0.5.0", true},
		{"exactly at the release: already crossed", "v0.5.0", "v0.5.0", false},
		{"past the release", "v0.6.0", "v0.5.0", false},
		{"far below", "v0.1.0", "v0.5.0", true},
		// A dev build's pseudo-version is a pre-release of its base, so
		// it orders below that base.
		{"pseudoversion below", "v0.0.4-0.20260724212501-dfb85daf8474+dirty", "v0.5.0", true},
		{"pseudoversion is a pre-release of its base", "v0.5.0-0.20260724212501-dfb85daf8474", "v0.5.0", true},
		{"pseudoversion above", "v0.6.1-0.20260724212501-dfb85daf8474+dirty", "v0.5.0", false},
		// Unorderable inputs leave the decision to detection rather than
		// silently dropping the migration.
		{"unorderable baseline defers to detection", "dev", "v0.5.0", true},
		{"unorderable version defers to detection", "v0.4.0", "not-a-version", true},
		// Normalisation.
		{"missing patch component", "v0.5", "v0.5.0", false},
		{"unprefixed baseline", "0.4.0", "v0.5.0", true},
		// The 0.0.x range forge actually ships in today.
		{"0.0.3 below 0.0.4", "v0.0.3", "v0.0.4", true},
		{"0.0.4 at 0.0.4", "v0.0.4", "v0.0.4", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := baselinePrecedes(tt.baseline, tt.version); got != tt.want {
				t.Errorf("baselinePrecedes(%q, %q) = %v, want %v",
					tt.baseline, tt.version, got, tt.want)
			}
		})
	}
}

// TestMigrationApplies_StagedJumpGetsEveryCrossedRelease is THE property
// the per-version model exists to provide, and the one the old
// per-transition range model got wrong.
//
// A project pinned several releases back must be handed every migration
// it crossed, in release order — not just the last hop's. Under the old
// [from, to) ranges a project that jumped a window without landing inside
// it fell through the gap and was told there was nothing to do.
func TestMigrationApplies_StagedJumpGetsEveryCrossedRelease(t *testing.T) {
	root := t.TempDir()
	// Every migration's detection matches, so the version gate is the
	// only thing being measured.
	all := []migrationMeta{
		{ID: "v0.6.0", Version: "v0.6.0", Detection: "true"},
		{ID: "v0.3.0", Version: "v0.3.0", Detection: "true"},
		{ID: "v0.5.0", Version: "v0.5.0", Detection: "true"},
		{ID: "v0.4.0", Version: "v0.4.0", Detection: "true"},
	}

	got := applicableMigrations(all, "v0.2.0", root)
	var ids []string
	for _, r := range got {
		ids = append(ids, r.Meta.ID)
	}
	want := []string{"v0.3.0", "v0.4.0", "v0.5.0", "v0.6.0"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("a v0.2.0 project jumping to current got %v, want every crossed release in order %v", ids, want)
	}

	// A project already at v0.4.0 has crossed v0.3.0 and v0.4.0; only the
	// releases still ahead of it remain.
	got = applicableMigrations(all, "v0.4.0", root)
	ids = nil
	for _, r := range got {
		ids = append(ids, r.Meta.ID)
	}
	want = []string{"v0.5.0", "v0.6.0"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("a v0.4.0 project got %v, want %v", ids, want)
	}
}

// TestMigrationApplies_DetectionGatesInsideTheWindow: predating the
// release is necessary, not sufficient. The project must actually exhibit
// the old shape — a project that never used the feature that changed
// needs no migration for it.
func TestMigrationApplies_DetectionGatesInsideTheWindow(t *testing.T) {
	root := t.TempDir()
	m := migrationMeta{ID: "v0.5.0", Version: "v0.5.0", Detection: "test -f old-shape-marker"}
	if migrationApplies(m, "v0.4.0", root) {
		t.Error("migration offered to a project that does not exhibit the old shape")
	}
	if err := os.WriteFile(filepath.Join(root, "old-shape-marker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !migrationApplies(m, "v0.4.0", root) {
		t.Error("migration withheld from a project that DOES exhibit the old shape")
	}
}

// TestMigrationApplies_NoDetectionAppliesToNothing: a migration that
// names no way to tell whether it applies must stay out of every
// worklist. Defaulting the other way puts a malformed skill into ALL of
// them.
func TestMigrationApplies_NoDetectionAppliesToNothing(t *testing.T) {
	root := t.TempDir()
	m := migrationMeta{ID: "v0.5.0", Version: "v0.5.0"}
	for _, baseline := range []string{"", "dev", "v0.1.0", "v0.4.0", "v9.0.0"} {
		if migrationApplies(m, baseline, root) {
			t.Errorf("detection-less migration offered at baseline %q", baseline)
		}
	}
}

// TestMigrationApplies_UnknownBaselineUsesDetectionAlone is the
// dev-without-a-tag guarantee: a project bridged to a local forge
// checkout, or one that predates the forge_version field, has no usable
// position on the timeline. Inventing one and filtering against the
// invention is how a project gets handed the migrations of a release it
// was never on. What the tree contains is the only thing still true.
func TestMigrationApplies_UnknownBaselineUsesDetectionAlone(t *testing.T) {
	root := t.TempDir()
	m := migrationMeta{ID: "v0.5.0", Version: "v0.5.0", Detection: "test -f old-shape-marker"}

	for _, baseline := range []string{"", "0.0.0", "v0.0.0", "dev", "(devel)", "not-a-version"} {
		if migrationApplies(m, baseline, root) {
			t.Errorf("baseline %q: migration offered before its shape exists in the tree", baseline)
		}
	}

	if err := os.WriteFile(filepath.Join(root, "old-shape-marker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, baseline := range []string{"", "0.0.0", "dev", "(devel)", "not-a-version"} {
		if !migrationApplies(m, baseline, root) {
			t.Errorf("baseline %q: detection matched but the migration was withheld", baseline)
		}
	}
}

// TestMigrationApplies_AlreadyCrossedIsNeverOffered: a project at or past
// a release has crossed it by definition, even if some unrelated file
// still trips the detection grep. The version gate is what stops a
// detection false-positive from re-offering old work.
func TestMigrationApplies_AlreadyCrossedIsNeverOffered(t *testing.T) {
	root := t.TempDir()
	m := migrationMeta{ID: "v0.5.0", Version: "v0.5.0", Detection: "true"}
	for _, baseline := range []string{"v0.5.0", "v0.5.1", "v0.6.0", "v1.0.0"} {
		if migrationApplies(m, baseline, root) {
			t.Errorf("baseline %q has already crossed v0.5.0 but was offered its migration", baseline)
		}
	}
}

// TestRunDetection_EmptyScriptDoesNotMatch: an absent detection is the
// absence of evidence, not evidence the migration applies.
func TestRunDetection_EmptyScriptDoesNotMatch(t *testing.T) {
	if runDetection(t.TempDir(), "") {
		t.Error("expected runDetection to return false for an empty script")
	}
}

// TestRunDetection_ScriptExitCode covers the script-runs path: the exit
// code drives the result.
func TestRunDetection_ScriptExitCode(t *testing.T) {
	dir := t.TempDir()
	if !runDetection(dir, "true") {
		t.Error("script `true` should report match")
	}
	if runDetection(dir, "false") {
		t.Error("script `false` should report no match")
	}
}

// TestRunDetection_ProjectDirSeen verifies the detection script is
// executed with the project root as CWD — important because detection
// scripts are path-relative (`grep -q ... forge.yaml`).
func TestRunDetection_ProjectDirSeen(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if !runDetection(dir, "test -f marker") {
		t.Error("detection script should see CWD = project root")
	}
}

// TestReadWriteMigrationsState round-trips the state file.
func TestReadWriteMigrationsState(t *testing.T) {
	dir := t.TempDir()
	state := migrationsState{Applied: map[string]string{
		"v0.5.0": "2026-06-04T10:00:00Z",
	}}
	if err := writeMigrationsState(dir, state); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := readMigrationsState(dir)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Applied["v0.5.0"] != "2026-06-04T10:00:00Z" {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	// Absent file = empty applied map, no error.
	got2, err := readMigrationsState(t.TempDir())
	if err != nil {
		t.Fatalf("read empty: %v", err)
	}
	if len(got2.Applied) != 0 {
		t.Errorf("expected empty Applied for absent file, got %+v", got2)
	}
}

// TestRunUpgradeApply_UnknownIDErrors makes sure typos surface as a
// UserErr rather than silently appending an unknown migration id.
func TestRunUpgradeApply_UnknownIDErrors(t *testing.T) {
	dir := newTestProject(t)
	withCwd(t, dir, func() {
		var buf bytes.Buffer
		err := runUpgradeApply(&buf, "no-such-migration")
		if err == nil {
			t.Fatal("expected error for unknown migration id")
		}
		if !strings.Contains(err.Error(), "no-such-migration") {
			t.Errorf("error should mention the bad id: %v", err)
		}
	})
}

// TestWritePendingMigrationsHuman_ListsInReleaseOrder pins the two things
// an agent reads off the worklist: the load/apply commands, and the
// instruction that the order is load-bearing.
func TestWritePendingMigrationsHuman_ListsInReleaseOrder(t *testing.T) {
	pending := []pendingMigration{
		{Meta: migrationMeta{ID: "v0.4.0", SkillPath: "migrations/v0.4.0", Version: "v0.4.0", Title: "Older"}},
		{Meta: migrationMeta{ID: "v0.5.0", SkillPath: "migrations/v0.5.0", Version: "v0.5.0", Title: "Newer"}},
	}
	var buf bytes.Buffer
	if err := writePendingMigrationsHuman(&buf, pending, len(pending)); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := buf.String()
	if i, j := strings.Index(out, "v0.4.0"), strings.Index(out, "v0.5.0"); i < 0 || j < 0 || i > j {
		t.Errorf("releases should print oldest first:\n%s", out)
	}
	for _, want := range []string{
		"skill load migrations/v0.4.0",
		"project upgrade apply v0.4.0",
		"in the order listed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestWritePendingMigrationsJSON_StableShape pins the JSON keys so
// downstream parsers don't break on a key rename. shipped_migrations is
// what lets a consumer tell "forge ships none" from "none apply to you"
// without re-deriving it from an empty array.
func TestWritePendingMigrationsJSON_StableShape(t *testing.T) {
	pending := []pendingMigration{
		{Meta: migrationMeta{ID: "v0.5.0", Title: "Demo", Version: "v0.5.0"}},
	}
	var buf bytes.Buffer
	if err := writePendingMigrationsJSON(&buf, pending, 1); err != nil {
		t.Fatalf("json: %v", err)
	}
	out := buf.String()
	for _, key := range []string{
		`"binary_version"`,
		`"shipped_migrations"`,
		`"pending"`,
		`"id": "v0.5.0"`,
		`"version"`,
	} {
		if !strings.Contains(out, key) {
			t.Errorf("JSON missing %s: %s", key, out)
		}
	}
}

// TestRunUpgradeApply_RecordsToStateFile is the integration test for the
// apply subcommand body. It plants a synthetic migration in the applied
// set directly (the shipped registry is empty), then verifies the file
// round-trips — the state format has to keep working across the periods
// when forge ships no migrations at all.
func TestRunUpgradeApply_RecordsToStateFile(t *testing.T) {
	dir := newTestProject(t)
	state := migrationsState{Applied: map[string]string{"v0.5.0": "2026-06-04T10:00:00Z"}}
	if err := writeMigrationsState(dir, state); err != nil {
		t.Fatalf("write state: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".forge", "migrations.json"))
	if err != nil {
		t.Fatalf("read migrations.json: %v", err)
	}
	var st migrationsState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := st.Applied["v0.5.0"]; !ok {
		t.Errorf("expected v0.5.0 in applied set, got %+v", st)
	}
}

// newTestProject creates a minimal forge.yaml + .forge directory in a
// temp dir so the command's project-root walk-up succeeds. Returns the
// project root path.
//
// The forge.yaml mirrors the strict-loader schema: module_path and
// version are required, so a too-small fixture causes LoadStrict to
// fail and the project-version probe falls back to "" (which would
// silently pass version gates).
func newTestProject(t *testing.T) string {
	t.Helper()
	return newTestProjectWithVersion(t, "")
}

// newTestProjectWithVersion is like newTestProject but lets the caller
// pin a specific forge_version. The empty string means "no pin".
func newTestProjectWithVersion(t *testing.T, forgeVersion string) string {
	t.Helper()
	dir := t.TempDir()
	pin := ""
	if forgeVersion != "" {
		pin = "forge_version: " + forgeVersion + "\n"
	}
	cfg := []byte(pin + `name: test-project
module_path: github.com/example/test
version: 0.1.0
`)
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), cfg, 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	markServiceProject(t, dir)
	return dir
}

// withCwd runs fn with the process CWD switched to dir, restoring it
// on return. The migration command uses findProjectRoot() which walks
// up from cwd, so the test must chdir to exercise the real code path.
//
// Tests that use this helper must not run in parallel — process-wide
// CWD is shared state.
func withCwd(t *testing.T, dir string, fn func()) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	defer func() {
		if err := os.Chdir(prev); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	}()
	fn()
}

// TestIsPreV01Baseline pins the baseline classification. Forge has never
// released v0.1.0, so every real pin in existence — released 0.0.x tags,
// pseudo-versions from local checkouts, the unpinned sentinel — is below the
// line. Getting this wrong in either direction is how a project gets handed
// the migrations of a release it was never on.
func TestIsPreV01Baseline(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"", true},        // unpinned
		{"0.0.0", true},   // unpinned sentinel
		{"dev", true},     // unorderable sentinel
		{"(devel)", true}, // unorderable sentinel
		{"v0.0.3", true},  // released 0.0.x tag
		{"v0.0.4-0.20260724212501-dfb85daf8474+dirty", true}, // dirty local build
		{"v0.0.0-20260430002332-8f05b089372c", true},         // untagged-base pseudo-version
		{"v0.1.0-0.20260724212501-dfb85daf8474", true},       // pre-release OF v0.1.0 is still below it
		{"v0.1.0", false}, // the first compat tag
		{"v0.1.3", false},
		{"v1.4.0", false},
	}
	for _, tt := range tests {
		if got := isPreV01Baseline(tt.version); got != tt.want {
			t.Errorf("isPreV01Baseline(%q) = %v, want %v", tt.version, got, tt.want)
		}
	}
}

// TestStaticRoutesMigration_DetectsDynamicIDRoutes runs the SHIPPED
// detection of the static-routes migration against the two shapes it has to
// tell apart: a frontend still carrying `src/app/<slug>/[id]/` (offered) and
// one already on the static `view/` + `edit/` pages (not offered). Detection
// runs in forge's in-process shell, where a quoted `'[id]'` path component
// never globs — the first draft of this skill read naturally and matched
// nothing, so `upgrade list` reported every project up to date.
func TestStaticRoutesMigration_DetectsDynamicIDRoutes(t *testing.T) {
	metas, err := loadMigrationMetas()
	if err != nil {
		t.Fatalf("loadMigrationMetas: %v", err)
	}
	var m migrationMeta
	for _, candidate := range metas {
		if candidate.SkillPath == staticRoutesMigrationSkill {
			m = candidate
		}
	}
	if m.ID == "" {
		t.Fatalf("no shipped migration at %s — `forge generate` points users at it", staticRoutesMigrationSkill)
	}

	legacy := t.TempDir()
	mkfile(t, filepath.Join(legacy, "frontends", "web", "src", "app", "books", "[id]", "page.tsx"))
	if !migrationApplies(m, "v0.1.29", legacy) {
		t.Error("a frontend with src/app/books/[id]/ was not offered the static-routes migration")
	}

	current := t.TempDir()
	mkfile(t, filepath.Join(current, "frontends", "web", "src", "app", "books", "view", "page.tsx"))
	mkfile(t, filepath.Join(current, "frontends", "web", "src", "app", "books", "edit", "page.tsx"))
	if migrationApplies(m, "v0.1.29", current) {
		t.Error("a frontend already on static view/edit routes was offered the migration")
	}
}
