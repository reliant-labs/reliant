package generator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// freshnessProject lays out a project root with the given migration files and
// returns it.
func freshnessProject(t *testing.T, migrations map[string]string) string {
	t.Helper()
	root := t.TempDir()
	if len(migrations) == 0 {
		return root
	}
	migDir := filepath.Join(root, "db", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range migrations {
		if err := os.WriteFile(filepath.Join(migDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func emitFreshness(t *testing.T, root, feRel string) (manifest, guard string) {
	t.Helper()
	cs, err := LoadChecksums(root)
	if err != nil {
		t.Fatalf("LoadChecksums: %v", err)
	}
	fpFiles, fpConfig := codegen.SeedFingerprint(root, seedplan.DefaultConfig())
	if err := EmitFixtureFreshnessSurface(root, feRel, 1, fpFiles, fpConfig, cs); err != nil {
		t.Fatalf("EmitFixtureFreshnessSurface: %v", err)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(root, feRel, "src", "mocks", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(b)
	}
	return read("fixture-manifest_gen.ts"), read("fixture-freshness_gen.test.ts")
}

// The core property: the manifest records the fingerprint of the schema on
// disk, and that recorded value MOVES when a migration is added. If it did
// not, the emitted guard would compare a constant to a constant and pass on a
// stale tree — the exact failure this whole surface exists to prevent.
func TestFreshnessManifestRecordsAndTracksTheSchema(t *testing.T) {
	feRel := filepath.Join("frontends", "web")
	beforeRoot := freshnessProject(t, map[string]string{
		"00001_create_orders.up.sql": "CREATE TABLE orders (id UUID PRIMARY KEY);",
	})
	beforeManifest, beforeGuard := emitFreshness(t, beforeRoot, feRel)
	afterRoot := freshnessProject(t, map[string]string{
		"00001_create_orders.up.sql": "CREATE TABLE orders (id UUID PRIMARY KEY);",
		"00002_add_status.up.sql":    "ALTER TABLE orders ADD COLUMN status TEXT;",
	})
	afterManifest, afterGuard := emitFreshness(t, afterRoot, feRel)

	beforeM, afterM := readFixtureMarker(t, beforeRoot, feRel), readFixtureMarker(t, afterRoot, feRel)
	beforeFP, afterFP := beforeM.SeedFingerprint, afterM.SeedFingerprint

	if beforeFP == "" || afterFP == "" {
		t.Fatal("a project WITH migrations recorded an empty fingerprint — the guard would " +
			"have no evidence to check")
	}
	if beforeFP == afterFP {
		t.Error("adding a migration did not change the recorded fingerprint; a stale fixture set " +
			"would pass the freshness guard")
	}
	if afterM.SeedFingerprintFiles != 2 {
		t.Errorf("seed_fingerprint_files = %d, want 2", afterM.SeedFingerprintFiles)
	}
	// The marker vouches for the files it was written beside.
	if afterM.Fixtures[fixtureFreshnessFiles[0]] == "" || afterM.Fixtures[fixtureFreshnessFiles[1]] == "" {
		t.Errorf("the marker does not record the generated files beside it: %v", afterM.Fixtures)
	}

	// The merge invariant: the COMMITTED surface does not move when a
	// migration is added. A digest in a committed file was one line that
	// every branch adding a migration rewrote, so any two conflicted.
	if beforeManifest != afterManifest || beforeGuard != afterGuard {
		t.Error("adding a migration changed the committed manifest or guard; two branches that each add one would conflict there")
	}
}

// readFixtureMarker reads the marker the emitter wrote for feRel.
func readFixtureMarker(t *testing.T, root, feRel string) fixtureMarker {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(FixtureMarkerPath(feRel))))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	var m fixtureMarker
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse marker: %v", err)
	}
	return m
}

// The marker is never committed: .forge/ is ignored in every forge project
// apart from forge's committed state, which the marker is not.
func TestFixtureMarkerIsNotCommittedState(t *testing.T) {
	rel := FixtureMarkerPath(filepath.Join("frontends", "web"))
	if !strings.HasPrefix(rel, ".forge/") || checksums.IsCommittedState(rel) {
		t.Errorf("%s must live under .forge/ and not be committed state", rel)
	}
}

// A project with no migrations must render the placeholder branch and record
// an EMPTY fingerprint — and the guard must still be a real assertion, not a
// skip, so the "nothing to check" state is visible in the test report.
func TestFreshnessSurfaceWithoutMigrations(t *testing.T) {
	root := freshnessProject(t, nil)
	_, guard := emitFreshness(t, root, filepath.Join("frontends", "web"))

	if got := readFixtureMarker(t, root, filepath.Join("frontends", "web")).SeedFingerprint; got != "" {
		t.Errorf("seed_fingerprint = %q, want empty for a project with no migrations", got)
	}
	if !strings.Contains(guard, "no migrations") {
		t.Error("the no-schema guard does not explain why it is checking nothing")
	}
	// The placeholder must still assert. A guard that silently passed here
	// would be indistinguishable from one that is broken.
	if !strings.Contains(guard, "expect(") {
		t.Error("the no-schema guard makes no assertion at all")
	}
	// And it must not carry the node:crypto machinery it has no use for.
	if strings.Contains(guard, "node:crypto") {
		t.Error("the no-schema guard imports the hashing machinery it does not use")
	}
}

// The guard is the fixtures' validity tag, so a frontend with NO fixtures gets
// neither file — and a copy an earlier generate left behind is retired.
//
// This is the adopted-frontend shape: services of custom rpcs, no CRUD entity,
// so no src/mocks/<entity>_gen.ts. The guard used to be emitted there anyway,
// over a fingerprint that described nothing, and every migration that merged
// without a regenerate turned the frontend's `npm test` red (reliant's web
// frontend, repeatedly).
func TestFreshnessSurfaceIsOnlyEmittedBesideFixtures(t *testing.T) {
	root := freshnessProject(t, map[string]string{
		"00001_create_orders.up.sql": "CREATE TABLE orders (id UUID PRIMARY KEY);",
	})
	feRel := filepath.Join("frontends", "web")
	mocksDir := filepath.Join(root, feRel, "src", "mocks")
	emit := func(fixtures int) {
		t.Helper()
		cs, err := LoadChecksums(root)
		if err != nil {
			t.Fatalf("LoadChecksums: %v", err)
		}
		fpFiles, fpConfig := codegen.SeedFingerprint(root, seedplan.DefaultConfig())
		if err := EmitFixtureFreshnessSurface(root, feRel, fixtures, fpFiles, fpConfig, cs); err != nil {
			t.Fatalf("EmitFixtureFreshnessSurface(fixtures=%d): %v", fixtures, err)
		}
	}
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(mocksDir, name))
		return err == nil
	}

	t.Run("no fixtures, nothing emitted", func(t *testing.T) {
		emit(0)
		for _, name := range fixtureFreshnessFiles {
			if exists(name) {
				t.Errorf("%s was emitted for a frontend with no fixtures; it guards nothing and "+
					"goes stale on every migration", name)
			}
		}
	})

	t.Run("fixtures removed, pristine copies retired", func(t *testing.T) {
		emit(1)
		for _, name := range fixtureFreshnessFiles {
			if !exists(name) {
				t.Fatalf("%s was not emitted beside a fixture", name)
			}
		}
		emit(0)
		for _, name := range fixtureFreshnessFiles {
			if exists(name) {
				t.Errorf("%s survived a generate that wrote no fixtures; a forge-certified copy "+
					"must be retired", name)
			}
		}
	})

	t.Run("a hand-edited copy is the user's and stays", func(t *testing.T) {
		emit(1)
		manifest := filepath.Join(mocksDir, fixtureFreshnessFiles[0])
		body, err := os.ReadFile(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(manifest, append(body, []byte("// mine\n")...), 0o600); err != nil {
			t.Fatal(err)
		}
		emit(0)
		if !exists(fixtureFreshnessFiles[0]) {
			t.Error("a hand-edited fixture-manifest_gen.ts was deleted; retirement must leave a " +
				"modified file in place")
		}
		if exists(fixtureFreshnessFiles[1]) {
			t.Error("the pristine guard beside the hand-edited manifest was not retired")
		}
	})
}

// The relative path back to the project root is the guard's single most
// dangerous constant: too few `..` segments and it looks for db/migrations in
// a directory that has none, finds nothing, and PASSES — a disabled detector
// that still reports green.
func TestFreshnessGuardResolvesProjectRootAtAnyFrontendDepth(t *testing.T) {
	cases := map[string]string{
		filepath.Join("frontends", "web"):              "../../../..",
		filepath.Join("apps", "internal", "dashboard"): "../../../../..",
		"web": "../../..",
	}
	for feRel, want := range cases {
		root := freshnessProject(t, map[string]string{
			"00001_create_orders.up.sql": "CREATE TABLE orders (id UUID PRIMARY KEY);",
		})
		manifest, _ := emitFreshness(t, root, feRel)
		got := extractConst(t, manifest, "PROJECT_ROOT_FROM_FRONTEND")
		if got != want {
			t.Errorf("frontend at %s: PROJECT_ROOT_FROM_FRONTEND = %q, want %q", feRel, got, want)
		}
		// Prove the path actually reaches the migrations, rather than
		// trusting the arithmetic: resolve it the way the guard does.
		mocksDir := filepath.Join(root, feRel, "src", "mocks")
		resolved := filepath.Join(mocksDir, filepath.FromSlash(got), "db", "migrations")
		if _, err := os.Stat(resolved); err != nil {
			t.Errorf("frontend at %s: the emitted path does not resolve to db/migrations (%v) — "+
				"the guard would find no schema and pass unconditionally", feRel, err)
		}
	}
}

// TestFreshnessGuardFilenameIsCollectedByVitest pins the guard's FILENAME
// against the scaffolded vitest `include` glob.
//
// This is not pedantry about naming; it caught a real and completely silent
// failure. The guard was first emitted as `fixture-freshness.test_gen.ts`,
// following the `_gen` suffix convention every other generated frontend file
// uses. vitest's include pattern is `src/**/*.{test,spec}.{ts,tsx}`, which
// requires `.test.` immediately before the extension — so `.test_gen.ts`
// matched NOTHING. The file was generated, typechecked, committed, and never
// executed. `npm test` reported all green while the staleness detector it was
// supposed to be running did not exist as far as the runner was concerned.
//
// A staleness guard that silently does not run is worse than no guard: it is
// an assurance that something is being checked when nothing is. The name is
// therefore load-bearing, and asserted against the same glob the scaffold
// ships rather than against a hardcoded string.
func TestFreshnessGuardFilenameIsCollectedByVitest(t *testing.T) {
	root := freshnessProject(t, map[string]string{
		"00001_create_orders.up.sql": "CREATE TABLE orders (id UUID PRIMARY KEY);",
	})
	feRel := filepath.Join("frontends", "web")
	emitFreshness(t, root, feRel)

	mocksDir := filepath.Join(root, feRel, "src", "mocks")
	entries, err := os.ReadDir(mocksDir)
	if err != nil {
		t.Fatalf("read mocks dir: %v", err)
	}

	var guard string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "fixture-freshness") {
			guard = e.Name()
			break
		}
	}
	if guard == "" {
		t.Fatal("no fixture-freshness guard was emitted at all")
	}

	// The scaffolded vitest config's include glob is
	// `src/**/*.{test,spec}.{ts,tsx}`. Collection requires the name to end
	// in `.test.ts` / `.test.tsx` (or the spec spelling).
	collected := strings.HasSuffix(guard, ".test.ts") || strings.HasSuffix(guard, ".test.tsx") ||
		strings.HasSuffix(guard, ".spec.ts") || strings.HasSuffix(guard, ".spec.tsx")
	if !collected {
		t.Fatalf("the freshness guard is emitted as %q, which the scaffolded vitest include glob "+
			"`src/**/*.{test,spec}.{ts,tsx}` does NOT match.\n\n"+
			"The file would be generated and typechecked but never RUN: `npm test` reports green "+
			"while the staleness detector does not execute at all. Name it `*_gen.test.ts` — the "+
			"`_gen` marker stays on the stem, and the `.test.ts` suffix is what vitest collects.", guard)
	}
	// And the generated-file marker must survive the rename, or `forge
	// project disown` and the drift lint stop recognizing the file.
	if !strings.Contains(guard, "_gen") {
		t.Errorf("the freshness guard %q carries no `_gen` marker; forge's ownership tooling "+
			"keys on it", guard)
	}
}

// The staleness message has to be right about WHY. The fingerprint covers two
// inputs, and editing only db/seeds/vocab.yaml — the routine edit a seed agent
// makes — moves it exactly like a migration does. A message that blamed "a
// schema this project no longer has" sent that agent looking for a migration
// change that never happened. It must name both causes and the one command
// that refreshes the fixtures.
func TestFreshnessGuardNamesBothCausesAndTheFix(t *testing.T) {
	_, guard := emitFreshness(t, freshnessProject(t, map[string]string{
		"00001_create_orders.up.sql": "CREATE TABLE orders (id UUID PRIMARY KEY);",
	}), filepath.Join("frontends", "web"))

	start := strings.Index(guard, `"STALE MOCK FIXTURES`)
	if start < 0 {
		t.Fatal("guard no longer carries the STALE MOCK FIXTURES message")
	}
	msg := guard[start:]
	if end := strings.Index(msg, ").toBe(marker.seed_fingerprint)"); end > 0 {
		msg = msg[:end]
	}
	// Join the TypeScript string concatenation back into the text a reader
	// sees, so a phrase split across two literals still matches.
	msg = regexp.MustCompile(`"\s*\+\s*"`).ReplaceAllString(msg, "")
	for _, want := range []string{
		"db/migrations",          // cause 1: the schema
		"db/seeds/vocab.yaml",    // cause 2: the seed vocabulary
		"seed vocabulary",        // ...named as a cause in its own right
		"`forge generate`",       // the fix, spelled as a command
		"reliant forge generate", // and as the embedded CLI spells it
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("staleness message must mention %q; got:\n%s", want, msg)
		}
	}
	// It must not assert a schema change it cannot know happened.
	if strings.Contains(msg, "no longer has") {
		t.Errorf("staleness message still claims the schema changed, which is false after a vocab-only edit:\n%s", msg)
	}
}

// Regeneration must be idempotent, or `forge generate` twice reports changes
// and the drift lint never settles.
func TestFreshnessSurfaceIsIdempotent(t *testing.T) {
	root := freshnessProject(t, map[string]string{
		"00001_create_orders.up.sql": "CREATE TABLE orders (id UUID PRIMARY KEY);",
	})
	feRel := filepath.Join("frontends", "web")

	firstManifest, firstGuard := emitFreshness(t, root, feRel)
	secondManifest, secondGuard := emitFreshness(t, root, feRel)

	if firstManifest != secondManifest {
		t.Error("fixture-manifest_gen.ts changed on a second render with identical inputs")
	}
	if firstGuard != secondGuard {
		t.Error("fixture-freshness_gen.test.ts changed on a second render with identical inputs")
	}
}

// extractConst pulls the value of an emitted `export const NAME = <value>;`,
// unquoting a string literal.
func extractConst(t *testing.T, src, name string) string {
	t.Helper()
	marker := "export const " + name + " = "
	idx := strings.Index(src, marker)
	if idx < 0 {
		t.Fatalf("emitted module has no %s", name)
	}
	rest := src[idx+len(marker):]
	end := strings.Index(rest, ";")
	if end < 0 {
		t.Fatalf("%s is not terminated", name)
	}
	return strings.Trim(strings.TrimSpace(rest[:end]), `"`)
}
