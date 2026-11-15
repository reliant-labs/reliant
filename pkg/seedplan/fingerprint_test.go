package seedplan

import (
	"os"
	"path/filepath"
	"testing"
)

// writeMig writes one migration file into a fresh project's migrations dir and
// returns that dir.
func migDirWith(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	migDir := filepath.Join(root, "db", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		path := filepath.Join(migDir, name)
		if name == "vocab.yaml" {
			path = VocabPath(migDir)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return migDir
}

func mustFingerprint(t *testing.T, migDir string) Fingerprint {
	t.Helper()
	fp, err := FingerprintInputs(migDir)
	if err != nil {
		t.Fatalf("FingerprintInputs: %v", err)
	}
	return fp
}

// The property the whole staleness guard rests on: same inputs, same digest.
// Without it the guard cries wolf on every run and gets disabled.
func TestFingerprintIsStableAcrossRuns(t *testing.T) {
	dir := migDirWith(t, map[string]string{
		"00001_create_orders.up.sql": "CREATE TABLE orders (id UUID PRIMARY KEY);",
	})

	first := mustFingerprint(t, dir)
	if first.Empty() {
		t.Fatal("fingerprint covered no files — every assertion below would be vacuous")
	}
	if got := mustFingerprint(t, dir); got.Hex != first.Hex {
		t.Errorf("fingerprint is not stable across calls:\n first = %s\nsecond = %s", first.Hex, got.Hex)
	}
}

// The property that makes the guard WORTH having: an edit to the schema must
// move the digest. This is the exact scenario the mock fixtures go stale on —
// a column added without re-running codegen.
func TestFingerprintMovesWhenAColumnIsAdded(t *testing.T) {
	before := mustFingerprint(t, migDirWith(t, map[string]string{
		"00001_create_orders.up.sql": "CREATE TABLE orders (id UUID PRIMARY KEY);",
	}))
	after := mustFingerprint(t, migDirWith(t, map[string]string{
		"00001_create_orders.up.sql": "CREATE TABLE orders (id UUID PRIMARY KEY);",
		"00002_add_status.up.sql":    "ALTER TABLE orders ADD COLUMN status TEXT;",
	}))

	if before.Hex == after.Hex {
		t.Error("adding a migration did not move the fingerprint — the staleness guard would " +
			"pass on exactly the schema change it exists to catch")
	}
	if after.Files != 2 {
		t.Errorf("Files = %d, want 2", after.Files)
	}
}

// The vocab overlay changes VALUES without changing the schema, so it has to
// be part of the digest or a vocab edit silently leaves stale fixtures.
func TestFingerprintCoversTheVocabOverlay(t *testing.T) {
	schema := "CREATE TABLE orders (id UUID PRIMARY KEY, customer_name TEXT);"
	withoutVocab := mustFingerprint(t, migDirWith(t, map[string]string{
		"00001_create_orders.up.sql": schema,
	}))
	withVocab := mustFingerprint(t, migDirWith(t, map[string]string{
		"00001_create_orders.up.sql": schema,
		"vocab.yaml":                 "columns:\n  orders.customer_name: [Ada, Grace]\n",
	}))

	if withoutVocab.Hex == withVocab.Hex {
		t.Error("adding db/seeds/vocab.yaml did not move the fingerprint — a vocabulary edit " +
			"changes every seeded value and would leave fixtures stale undetected")
	}
	if withVocab.Files != 2 {
		t.Errorf("Files = %d, want 2 (migration + vocab)", withVocab.Files)
	}
}

// Down migrations are never applied, so hashing them would invalidate correct
// fixtures for an edit that cannot affect a single value.
func TestFingerprintIgnoresDownMigrations(t *testing.T) {
	base := map[string]string{"00001_create_orders.up.sql": "CREATE TABLE orders (id UUID PRIMARY KEY);"}
	withDown := map[string]string{
		"00001_create_orders.up.sql":   "CREATE TABLE orders (id UUID PRIMARY KEY);",
		"00001_create_orders.down.sql": "DROP TABLE orders;",
	}

	if a, b := mustFingerprint(t, migDirWith(t, base)), mustFingerprint(t, migDirWith(t, withDown)); a.Hex != b.Hex {
		t.Error("a down migration moved the fingerprint; schemadef applies only *.up.sql, so this " +
			"reports drift that cannot affect any seeded value")
	}
}

// A project with no migrations must be a well-defined empty result, not an
// error and not a digest — every project is in this state between `forge
// project new` and its first migration.
func TestFingerprintOfProjectWithoutMigrationsIsEmpty(t *testing.T) {
	fp, err := FingerprintInputs(filepath.Join(t.TempDir(), "db", "migrations"))
	if err != nil {
		t.Fatalf("a missing migrations dir must not be an error: %v", err)
	}
	if !fp.Empty() || fp.Hex != "" {
		t.Errorf("want empty fingerprint, got %+v", fp)
	}
}

// Line-ending translation on checkout is not a schema change.
func TestFingerprintIgnoresLineEndings(t *testing.T) {
	lf := mustFingerprint(t, migDirWith(t, map[string]string{
		"00001_create_orders.up.sql": "CREATE TABLE orders (\n  id UUID PRIMARY KEY\n);\n",
	}))
	crlf := mustFingerprint(t, migDirWith(t, map[string]string{
		"00001_create_orders.up.sql": "CREATE TABLE orders (\r\n  id UUID PRIMARY KEY\r\n);\r\n",
	}))
	if lf.Hex != crlf.Hex {
		t.Error("CRLF vs LF moved the fingerprint — a Windows checkout would report permanent drift")
	}
}

// The digest must not depend on where the project happens to live, or the same
// tree fingerprints differently in CI and on a laptop.
func TestFingerprintIsIndependentOfProjectLocation(t *testing.T) {
	files := map[string]string{"00001_create_orders.up.sql": "CREATE TABLE orders (id UUID PRIMARY KEY);"}
	if a, b := mustFingerprint(t, migDirWith(t, files)), mustFingerprint(t, migDirWith(t, files)); a.Hex != b.Hex {
		t.Error("the same schema in two directories fingerprinted differently — the digest is " +
			"capturing the checkout path")
	}
}

// Salt and row counts reshuffle every planned value without touching a
// migration, so a consumer caching the plan's OUTPUT must see them move.
func TestFingerprintWithConfigRespondsToSaltAndRows(t *testing.T) {
	dir := migDirWith(t, map[string]string{
		"00001_create_orders.up.sql": "CREATE TABLE orders (id UUID PRIMARY KEY);",
	})

	base, err := FingerprintWithConfig(dir, Config{Rows: 20, Salt: 0})
	if err != nil {
		t.Fatalf("FingerprintWithConfig: %v", err)
	}
	if base.Empty() {
		t.Fatal("fingerprint covered no files — the assertions below would be vacuous")
	}

	salted, err := FingerprintWithConfig(dir, Config{Rows: 20, Salt: 7})
	if err != nil {
		t.Fatal(err)
	}
	if base.Hex == salted.Hex {
		t.Error("changing the seed salt did not move the fingerprint, but it changes every seeded value")
	}

	rowed, err := FingerprintWithConfig(dir, Config{Rows: 5, Salt: 0})
	if err != nil {
		t.Fatal(err)
	}
	if base.Hex == rowed.Hex {
		t.Error("changing the row count did not move the fingerprint, but it changes how many fixtures exist")
	}

	// Per-table overrides live in a map; iteration order must not leak in.
	cfg := Config{Rows: 20, RowsPerTable: map[string]int{"orders": 3, "users": 4, "items": 5}}
	first, err := FingerprintWithConfig(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		again, err := FingerprintWithConfig(dir, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if again.Hex != first.Hex {
			t.Fatal("FingerprintWithConfig is unstable across runs — map iteration order is leaking " +
				"into the digest, which would make the guard flap")
		}
	}
}
