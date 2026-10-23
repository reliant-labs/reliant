package generator

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

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
	if err := EmitFixtureFreshnessSurface(root, feRel, fpFiles, fpConfig, cs); err != nil {
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
	before, _ := emitFreshness(t, freshnessProject(t, map[string]string{
		"00001_create_orders.up.sql": "CREATE TABLE orders (id UUID PRIMARY KEY);",
	}), filepath.Join("frontends", "web"))

	after, _ := emitFreshness(t, freshnessProject(t, map[string]string{
		"00001_create_orders.up.sql": "CREATE TABLE orders (id UUID PRIMARY KEY);",
		"00002_add_status.up.sql":    "ALTER TABLE orders ADD COLUMN status TEXT;",
	}), filepath.Join("frontends", "web"))

	beforeFP := extractConst(t, before, "SEED_FINGERPRINT")
	afterFP := extractConst(t, after, "SEED_FINGERPRINT")

	if beforeFP == "" || afterFP == "" {
		t.Fatal("a project WITH migrations recorded an empty fingerprint — the guard would " +
			"have no evidence to check")
	}
	if beforeFP == afterFP {
		t.Error("adding a migration did not change the recorded fingerprint; a stale fixture set " +
			"would pass the freshness guard")
	}
	if got := extractConst(t, after, "SEED_FINGERPRINT_FILES"); got != "2" {
		t.Errorf("SEED_FINGERPRINT_FILES = %s, want 2", got)
	}
}

// A project with no migrations must render the placeholder branch and record
// an EMPTY fingerprint — and the guard must still be a real assertion, not a
// skip, so the "nothing to check" state is visible in the test report.
func TestFreshnessSurfaceWithoutMigrations(t *testing.T) {
	manifest, guard := emitFreshness(t, freshnessProject(t, nil), filepath.Join("frontends", "web"))

	if got := extractConst(t, manifest, "SEED_FINGERPRINT"); got != "" {
		t.Errorf("SEED_FINGERPRINT = %q, want empty for a project with no migrations", got)
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
	if end := strings.Index(msg, ").toBe(SEED_FINGERPRINT)"); end > 0 {
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
