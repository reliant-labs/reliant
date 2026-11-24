package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/migrationver"
)

type gooseFile struct {
	Name    string
	Content string
}

func writeGooseSrc(t *testing.T, files []gooseFile) string {
	t.Helper()
	src := t.TempDir()
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(src, f.Name), []byte(f.Content), 0o644); err != nil {
			t.Fatalf("write %s: %v", f.Name, err)
		}
	}
	return src
}

func readMigrationsDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func readMigrateImportFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// migrationWithStem finds the single imported migration whose name ends in
// `_<stem>.up.sql`.
//
// Imported migrations are versioned with a UTC timestamp allocated at import
// time, so a test cannot name the file it expects. It can still pin
// everything that matters — which stems exist, their relative order, and
// their contents — by looking them up by stem. Asserting on an exact version
// would only be asserting on the clock.
func migrationWithStem(t *testing.T, dir, stem string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*_"+stem+".up.sql"))
	if err != nil {
		t.Fatalf("glob %s: %v", stem, err)
	}
	if len(matches) != 1 {
		t.Fatalf("want exactly one migration with stem %q in %s, got %v", stem, dir, matches)
	}
	return matches[0]
}

// stemsInOrder returns the stems of every migration in dir, in version
// order — the order the migrator will apply them.
func stemsInOrder(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, name := range readMigrationsDir(t, dir) {
		base := strings.TrimSuffix(name, ".up.sql")
		if _, stem, ok := strings.Cut(base, "_"); ok {
			out = append(out, stem)
		}
	}
	return out
}

// assertTimestampVersioned fails unless every migration in dir carries a
// timestamp version. This is the property that makes parallel branches
// collision-free, so it is asserted directly rather than inferred from a
// filename.
func assertTimestampVersioned(t *testing.T, dir string) {
	t.Helper()
	for _, name := range readMigrationsDir(t, dir) {
		version, _, ok := strings.Cut(name, "_")
		if !ok || !migrationver.IsTimestamp(version) {
			t.Errorf("migration %q is not timestamp-versioned", name)
		}
	}
}

func TestMigrateImportRoundtrip(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates migration versions with git; runs in task test")
	}
	src := writeGooseSrc(t, []gooseFile{
		{
			Name: "20240501_add_users.sql",
			Content: `-- +goose Up
CREATE TABLE users (id INT PRIMARY KEY);
-- +goose Down
DROP TABLE users;
`,
		},
		{
			Name: "20240502_add_orgs.sql",
			Content: `-- +goose Up
CREATE TABLE orgs (id INT PRIMARY KEY);
-- +goose Down
DROP TABLE orgs;
`,
		},
		{
			Name: "20240503_add_memberships.sql",
			Content: `-- +goose Up
CREATE TABLE memberships (
  user_id INT REFERENCES users(id),
  org_id INT REFERENCES orgs(id)
);
-- +goose Down
DROP TABLE memberships;
`,
		},
	})
	dest := t.TempDir()

	var buf bytes.Buffer
	err := runMigrateImport(migrateImportOptions{
		From:    "goose",
		SrcDir:  src,
		DestDir: dest,
		Stdout:  &buf,
	})
	if err != nil {
		t.Fatalf("runMigrateImport: %v", err)
	}

	// The source order must be preserved: memberships references users and
	// orgs, so an import that reordered them would produce migrations that
	// cannot apply.
	got := stemsInOrder(t, dest)
	want := []string{"add_users", "add_orgs", "add_memberships"}
	if len(got) != len(want) {
		t.Fatalf("file count: got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("migration[%d]: got stem %q, want %q", i, got[i], w)
		}
	}
	assertTimestampVersioned(t, dest)

	up := readMigrateImportFile(t, migrationWithStem(t, dest, "add_users"))
	if !strings.Contains(up, "CREATE TABLE users") {
		t.Errorf("up file missing CREATE TABLE: %q", up)
	}
	if strings.Contains(up, "+goose") {
		t.Errorf("up file still has goose markers: %q", up)
	}
	// Forward only: the Down section is dropped, never written, and the drop
	// is reported rather than silent.
	if downs, _ := filepath.Glob(filepath.Join(dest, "*.down.sql")); len(downs) != 0 {
		t.Errorf("import must write no .down.sql (forge rolls forward only), got %v", downs)
	}
	if strings.Contains(up, "DROP TABLE users") {
		t.Errorf("up file must not carry the Down section's SQL: %q", up)
	}
	if !strings.Contains(buf.String(), "Dropped -- +goose Down sections") ||
		!strings.Contains(buf.String(), "20240501_add_users.sql") {
		t.Errorf("expected the dropped Down sections to be reported, got: %q", buf.String())
	}

	if !strings.Contains(buf.String(), "Foreign-key check") {
		t.Errorf("expected FK warning in stdout, got: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "add_memberships.up.sql") {
		t.Errorf("expected memberships flagged in FK warning, got: %q", buf.String())
	}
}

// A NO TRANSACTION file of ONE statement stays one migration: golang-migrate
// sends a single statement as a single query, which postgres runs outside a
// transaction block, so CONCURRENTLY works there as it did under goose.
func TestMigrateImportNoTransactionSingleStatement(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates migration versions with git; runs in task test")
	}
	src := writeGooseSrc(t, []gooseFile{
		{
			Name: "20240501_create_index.sql",
			Content: `-- +goose NO TRANSACTION
-- +goose Up
CREATE INDEX CONCURRENTLY idx_users_email ON users(email);
-- +goose Down
DROP INDEX CONCURRENTLY idx_users_email;
`,
		},
	})
	dest := t.TempDir()

	var buf bytes.Buffer
	if err := runMigrateImport(migrateImportOptions{
		From:    "goose",
		SrcDir:  src,
		DestDir: dest,
		Stdout:  &buf,
	}); err != nil {
		t.Fatalf("runMigrateImport: %v", err)
	}

	if got := stemsInOrder(t, dest); !slices.Equal(got, []string{"create_index"}) {
		t.Fatalf("a one-statement NO TRANSACTION file must stay one migration, got %v", got)
	}
	up := readMigrateImportFile(t, migrationWithStem(t, dest, "create_index"))
	if up != "CREATE INDEX CONCURRENTLY idx_users_email ON users(email);\n" {
		t.Errorf("up = %q, want the statement alone", up)
	}
	if strings.Contains(buf.String(), "Split -- +goose NO TRANSACTION") {
		t.Errorf("nothing was split, but the import reported a split:\n%s", buf.String())
	}
}

// The P5a regression: a NO TRANSACTION file holding several statements was
// written as ONE migration under an `-- x-no-tx-wrap: true` header that
// nothing reads. golang-migrate sent it as one multi-statement query, postgres
// ran that as one implicit transaction, and the first CREATE INDEX
// CONCURRENTLY failed with SQLSTATE 25001, leaving the schema dirty. It now
// becomes one migration per goose statement, split where goose split it.
func TestMigrateImportSplitsNoTransactionFile(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates migration versions with git; runs in task test")
	}
	src := writeGooseSrc(t, []gooseFile{
		{
			Name: "20240501_add_users.sql",
			Content: `-- +goose Up
CREATE TABLE users (id INT PRIMARY KEY, email TEXT, org_id INT);
-- +goose Down
DROP TABLE users;
`,
		},
		{
			Name: "20240502_index_users.sql",
			Content: `-- +goose NO TRANSACTION
-- +goose Up
-- Lookups by email.
CREATE INDEX CONCURRENTLY idx_users_email
    ON users (email);

CREATE INDEX CONCURRENTLY idx_users_org ON users (org_id); -- by org
-- +goose StatementBegin
DO $$
BEGIN
  RAISE NOTICE 'indexed;';
END;
$$;
-- +goose StatementEnd
-- trailing note
-- +goose Down
DROP INDEX CONCURRENTLY idx_users_org;
DROP INDEX CONCURRENTLY idx_users_email;
`,
		},
		{
			Name: "20240503_add_orgs.sql",
			Content: `-- +goose Up
CREATE TABLE orgs (id INT PRIMARY KEY);
`,
		},
	})
	dest := t.TempDir()

	var buf bytes.Buffer
	if err := runMigrateImport(migrateImportOptions{
		From:    "goose",
		SrcDir:  src,
		DestDir: dest,
		Stdout:  &buf,
	}); err != nil {
		t.Fatalf("runMigrateImport: %v", err)
	}

	want := []string{"add_users", "index_users_part1", "index_users_part2", "index_users_part3", "add_orgs"}
	if got := stemsInOrder(t, dest); !slices.Equal(got, want) {
		t.Fatalf("migrations in version order = %v, want %v", got, want)
	}
	assertTimestampVersioned(t, dest)

	header := func(part int) string {
		return "-- Statement " + string(rune('0'+part)) + " of 3 of goose migration 20240502_index_users.sql, which declared NO TRANSACTION.\n"
	}
	parts := map[string]string{
		"index_users_part1": "-- Lookups by email.\nCREATE INDEX CONCURRENTLY idx_users_email\n    ON users (email);\n",
		"index_users_part2": "CREATE INDEX CONCURRENTLY idx_users_org ON users (org_id); -- by org\n",
		"index_users_part3": "DO $$\nBEGIN\n  RAISE NOTICE 'indexed;';\nEND;\n$$;\n\n-- trailing note\n",
	}
	for i, stem := range []string{"index_users_part1", "index_users_part2", "index_users_part3"} {
		up := readMigrateImportFile(t, migrationWithStem(t, dest, stem))
		if !strings.HasPrefix(up, header(i+1)) {
			t.Errorf("%s does not open with its provenance comment:\n%s", stem, up)
		}
		if !strings.HasSuffix(up, "\n\n"+parts[stem]) {
			t.Errorf("%s body:\n%s\nwant it to end with:\n%s", stem, up, parts[stem])
		}
		for _, banned := range []string{"x-no-tx-wrap", "+goose", "DROP INDEX"} {
			if strings.Contains(up, banned) {
				t.Errorf("%s carries %q:\n%s", stem, banned, up)
			}
		}
	}

	out := buf.String()
	for _, w := range []string{
		"Split -- +goose NO TRANSACTION files into one migration per statement",
		"20240502_index_users.sql → 3 migrations, index_users_part1 … index_users_part3",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("import output lacks %q:\n%s", w, out)
		}
	}
	if n := strings.Count(out, "20240502_index_users.sql\n"); n != 1 {
		t.Errorf("the dropped Down section of the split file is reported %d times, want once:\n%s", n, out)
	}
}

// gooseStatements reproduces where goose itself ends a statement, because a
// split file must run each statement as goose ran it: what goose sent as one
// query stays one migration.
func TestGooseStatements(t *testing.T) {
	cases := []struct {
		name string
		up   string
		want []string
	}{
		{
			name: "one statement per terminated line",
			up:   "CREATE INDEX CONCURRENTLY a ON t (x);\nCREATE INDEX CONCURRENTLY b ON t (y);\n",
			want: []string{"CREATE INDEX CONCURRENTLY a ON t (x);\n", "CREATE INDEX CONCURRENTLY b ON t (y);\n"},
		},
		{
			name: "a statement spans lines until one ends in a semicolon",
			up:   "CREATE INDEX CONCURRENTLY a\n  ON t (x)\n  WHERE x > 0;\nVACUUM t;\n",
			want: []string{"CREATE INDEX CONCURRENTLY a\n  ON t (x)\n  WHERE x > 0;\n", "VACUUM t;\n"},
		},
		{
			name: "a trailing comment does not hide the semicolon",
			up:   "ALTER TYPE s ADD VALUE 'b'; -- new state\nUPDATE t SET s = 'b';\n",
			want: []string{"ALTER TYPE s ADD VALUE 'b'; -- new state\n", "UPDATE t SET s = 'b';\n"},
		},
		{
			name: "a semicolon only inside a comment does not end a statement",
			up:   "CREATE INDEX CONCURRENTLY a -- not here;\n  ON t (x);\nVACUUM t;\n",
			want: []string{"CREATE INDEX CONCURRENTLY a -- not here;\n  ON t (x);\n", "VACUUM t;\n"},
		},
		{
			name: "two statements goose sent as one query stay together",
			up:   "UPDATE t SET x = 1; UPDATE t SET y = 2;\nVACUUM t;\n",
			want: []string{"UPDATE t SET x = 1; UPDATE t SET y = 2;\n", "VACUUM t;\n"},
		},
		{
			name: "a StatementBegin block is one statement whatever it holds",
			up:   "-- +goose StatementBegin\nCREATE FUNCTION f() RETURNS void AS $$\nBEGIN\n  PERFORM 1;\nEND;\n$$ LANGUAGE plpgsql;\n-- +goose StatementEnd\nVACUUM t;\n",
			want: []string{"CREATE FUNCTION f() RETURNS void AS $$\nBEGIN\n  PERFORM 1;\nEND;\n$$ LANGUAGE plpgsql;\n", "VACUUM t;\n"},
		},
		{
			name: "leading comments travel with the statement, trailing ones with the last",
			up:   "\n-- first\n\nVACUUM a;\n-- second\nVACUUM b;\n\n-- the end\n",
			want: []string{"-- first\n\nVACUUM a;\n", "-- second\nVACUUM b;\n\n-- the end\n"},
		},
		{
			name: "an unterminated tail is kept, not dropped",
			up:   "VACUUM a;\nVACUUM b\n",
			want: []string{"VACUUM a;\n", "VACUUM b\n"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := gooseStatements(strings.Split(strings.TrimSuffix(tc.up, "\n"), "\n"))
			if !slices.Equal(got, tc.want) {
				t.Errorf("gooseStatements:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// A re-import of a file an older forge imported whole meets that file under
// its unsplit stem: refused without --force, replaced with it.
func TestMigrateImportSplitReplacesAWholeImportOfTheSameFile(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates migration versions with git; runs in task test")
	}
	dest := t.TempDir()
	old := filepath.Join(dest, "20240101000000_index_users.up.sql")
	if err := os.WriteFile(old, []byte("-- x-no-tx-wrap: true\nCREATE INDEX CONCURRENTLY a ON t (x);\nCREATE INDEX CONCURRENTLY b ON t (y);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := writeGooseSrc(t, []gooseFile{{
		Name:    "20240502_index_users.sql",
		Content: "-- +goose NO TRANSACTION\n-- +goose Up\nCREATE INDEX CONCURRENTLY a ON t (x);\nCREATE INDEX CONCURRENTLY b ON t (y);\n",
	}})

	var buf bytes.Buffer
	err := runMigrateImport(migrateImportOptions{From: "goose", SrcDir: src, DestDir: dest, Stdout: &buf})
	if err == nil || !strings.Contains(err.Error(), `stem "index_users" already exists`) {
		t.Fatalf("re-import without --force: err = %v, want the unsplit stem refused", err)
	}

	if err := runMigrateImport(migrateImportOptions{From: "goose", SrcDir: src, DestDir: dest, Force: true, Stdout: &buf}); err != nil {
		t.Fatalf("re-import with --force: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("--force left the whole-file import in place (stat err %v): its statements would run twice", err)
	}
	if got := stemsInOrder(t, dest); !slices.Equal(got, []string{"index_users_part1", "index_users_part2"}) {
		t.Errorf("after --force: %v", got)
	}
}

func TestMigrateImportStripsStatementMarkers(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates migration versions with git; runs in task test")
	}
	src := writeGooseSrc(t, []gooseFile{
		{
			Name: "20240501_create_fn.sql",
			Content: `-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION foo() RETURNS void AS $$
BEGIN
  RAISE NOTICE 'hi';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DROP FUNCTION foo();
-- +goose StatementEnd
`,
		},
	})
	dest := t.TempDir()

	var buf bytes.Buffer
	if err := runMigrateImport(migrateImportOptions{
		From:    "goose",
		SrcDir:  src,
		DestDir: dest,
		Stdout:  &buf,
	}); err != nil {
		t.Fatalf("runMigrateImport: %v", err)
	}

	up := readMigrateImportFile(t, migrationWithStem(t, dest, "create_fn"))

	if strings.Contains(up, "StatementBegin") || strings.Contains(up, "StatementEnd") {
		t.Errorf("up still contains Statement markers: %q", up)
	}
	if !strings.Contains(up, "CREATE FUNCTION foo") {
		t.Errorf("up missing CREATE FUNCTION: %q", up)
	}
	if strings.Contains(up, "DROP FUNCTION foo") {
		t.Errorf("the Down section leaked into up: %q", up)
	}
}

// TestMigrateImportSortsAfterExistingSequentialFiles is the mid-adoption
// case: a project whose migrations dir already holds 5-digit sequential
// files imports new ones, which are timestamp-versioned. The imported
// migrations must sort AFTER every existing file — that ordering is the
// entire reason old sequential files never need renaming.
func TestMigrateImportSortsAfterExistingSequentialFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates migration versions with git; runs in task test")
	}
	dest := t.TempDir()
	for _, name := range []string{
		"00001_audit_log.up.sql",
		"00002_api_key.up.sql",
		"00003_session.up.sql",
	} {
		if err := os.WriteFile(filepath.Join(dest, name), []byte("-- existing\n"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	src := writeGooseSrc(t, []gooseFile{
		{
			Name: "20240501_add_users.sql",
			Content: `-- +goose Up
CREATE TABLE users (id INT);
-- +goose Down
DROP TABLE users;
`,
		},
		{
			Name: "20240502_add_orgs.sql",
			Content: `-- +goose Up
CREATE TABLE orgs (id INT);
-- +goose Down
DROP TABLE orgs;
`,
		},
	})

	var buf bytes.Buffer
	if err := runMigrateImport(migrateImportOptions{
		From:    "goose",
		SrcDir:  src,
		DestDir: dest,
		Stdout:  &buf,
	}); err != nil {
		t.Fatalf("runMigrateImport: %v", err)
	}

	// The pre-existing files keep their sequential names, and the imports
	// land after them in version order.
	want := []string{"audit_log", "api_key", "session", "add_users", "add_orgs"}
	if got := stemsInOrder(t, dest); !slices.Equal(got, want) {
		t.Errorf("version order = %v, want %v (imports must sort after existing files)", got, want)
	}

	for _, stem := range []string{"add_users", "add_orgs"} {
		name := filepath.Base(migrationWithStem(t, dest, stem))
		version, _, _ := strings.Cut(name, "_")
		if !migrationver.IsTimestamp(version) {
			t.Errorf("imported %s is not timestamp-versioned", name)
		}
	}

	if got := readMigrateImportFile(t, filepath.Join(dest, "00001_audit_log.up.sql")); got != "-- existing\n" {
		t.Errorf("pre-existing file 00001 was clobbered: %q", got)
	}
}

func TestMigrateImportRefusesOverwriteWithoutForce(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates migration versions with git; runs in task test")
	}
	src := writeGooseSrc(t, []gooseFile{
		{
			Name: "20240501_add_users.sql",
			Content: `-- +goose Up
CREATE TABLE users (id INT);
-- +goose Down
DROP TABLE users;
`,
		},
	})
	dest := t.TempDir()

	var buf bytes.Buffer
	if err := runMigrateImport(migrateImportOptions{
		From:    "goose",
		SrcDir:  src,
		DestDir: dest,
		Stdout:  &buf,
	}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	migrationWithStem(t, dest, "add_users") // fails the test if absent

	buf.Reset()
	err := runMigrateImport(migrateImportOptions{
		From:    "goose",
		SrcDir:  src,
		DestDir: dest,
		Stdout:  &buf,
	})
	if err == nil {
		t.Fatal("expected error on re-import without --force")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should mention 'already exists', got: %v", err)
	}

	files := readMigrationsDir(t, dest)
	if len(files) != 1 {
		t.Errorf("refused run should not touch disk, got: %v", files)
	}
}

func TestMigrateImportForceOverwrites(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates migration versions with git; runs in task test")
	}
	dest := t.TempDir()

	src1 := writeGooseSrc(t, []gooseFile{
		{
			Name: "20240501_add_users.sql",
			Content: `-- +goose Up
CREATE TABLE users (id INT);
-- +goose Down
DROP TABLE users;
`,
		},
	})
	var buf bytes.Buffer
	if err := runMigrateImport(migrateImportOptions{
		From:    "goose",
		SrcDir:  src1,
		DestDir: dest,
		Stdout:  &buf,
	}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	firstPath := migrationWithStem(t, dest, "add_users")
	original := readMigrateImportFile(t, firstPath)
	if !strings.Contains(original, "id INT") {
		t.Fatalf("first run produced unexpected content: %q", original)
	}

	src2 := writeGooseSrc(t, []gooseFile{
		{
			Name: "20240501_add_users.sql",
			Content: `-- +goose Up
CREATE TABLE users (id BIGINT PRIMARY KEY, email TEXT);
-- +goose Down
DROP TABLE users;
`,
		},
	})
	buf.Reset()
	if err := runMigrateImport(migrateImportOptions{
		From:    "goose",
		SrcDir:  src2,
		DestDir: dest,
		Force:   true,
		Stdout:  &buf,
	}); err != nil {
		t.Fatalf("force re-run: %v", err)
	}

	// --force replaces by STEM: the old file is removed and exactly one
	// add_users migration remains, carrying the new content under a freshly
	// allocated version.
	if _, err := os.Stat(firstPath); !os.IsNotExist(err) {
		t.Errorf("expected the old %s to be removed, stat err: %v", filepath.Base(firstPath), err)
	}
	newPath := migrationWithStem(t, dest, "add_users")
	if newPath == firstPath {
		t.Errorf("--force should have written a newly versioned file, got the original %s", newPath)
	}
	updated := readMigrateImportFile(t, newPath)
	if !strings.Contains(updated, "BIGINT") {
		t.Errorf("--force should have re-imported new content at %s, got: %q", newPath, updated)
	}
}

func TestMigrateImportDryRunTouchesNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates migration versions with git; runs in task test")
	}
	src := writeGooseSrc(t, []gooseFile{
		{
			Name: "20240501_add_users.sql",
			Content: `-- +goose Up
CREATE TABLE users (id INT);
-- +goose Down
DROP TABLE users;
`,
		},
	})
	dest := t.TempDir()

	var buf bytes.Buffer
	if err := runMigrateImport(migrateImportOptions{
		From:    "goose",
		SrcDir:  src,
		DestDir: dest,
		DryRun:  true,
		Stdout:  &buf,
	}); err != nil {
		t.Fatalf("dry-run: %v", err)
	}

	files := readMigrationsDir(t, dest)
	if len(files) != 0 {
		t.Errorf("dry-run should not write files, got: %v", files)
	}

	out := buf.String()
	if !strings.Contains(out, "Dry run") {
		t.Errorf("expected 'Dry run' in output: %q", out)
	}
	if !strings.Contains(out, "add_users.up.sql") {
		t.Errorf("expected planned filename in dry-run output: %q", out)
	}
}

// A source with no Down section imports cleanly and reports no drop — it is
// already the shape the policy asks for.
func TestMigrateImportWithoutDownReportsNoDrop(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates migration versions with git; runs in task test")
	}
	src := writeGooseSrc(t, []gooseFile{
		{
			Name: "20240501_no_down.sql",
			Content: `-- +goose Up
CREATE TABLE users (id INT);
-- +goose Down
-- intentionally empty: roll forward
`,
		},
	})
	dest := t.TempDir()

	var buf bytes.Buffer
	if err := runMigrateImport(migrateImportOptions{
		From:    "goose",
		SrcDir:  src,
		DestDir: dest,
		Stdout:  &buf,
	}); err != nil {
		t.Fatalf("runMigrateImport: %v", err)
	}

	if got := stemsInOrder(t, dest); len(got) != 1 || got[0] != "no_down" {
		t.Fatalf("want only the up migration, got %v", got)
	}
	if strings.Contains(buf.String(), "Dropped") {
		t.Errorf("an empty Down section is not a drop worth reporting: %q", buf.String())
	}
}

func TestMigrateImportSkipsAlreadyConverted(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates migration versions with git; runs in task test")
	}
	src := writeGooseSrc(t, []gooseFile{
		{
			Name: "20240501_add_users.sql",
			Content: `-- +goose Up
CREATE TABLE users (id INT);
-- +goose Down
DROP TABLE users;
`,
		},
		{
			Name: "00001_already_converted.up.sql",
			Content: `CREATE TABLE orgs (id INT);
`,
		},
	})
	dest := t.TempDir()

	var buf bytes.Buffer
	if err := runMigrateImport(migrateImportOptions{
		From:    "goose",
		SrcDir:  src,
		DestDir: dest,
		Stdout:  &buf,
	}); err != nil {
		t.Fatalf("runMigrateImport: %v", err)
	}

	files := readMigrationsDir(t, dest)
	if len(files) != 1 {
		t.Errorf("expected 1 output file (only add_users converted), got: %v", files)
	}

	out := buf.String()
	if !strings.Contains(out, "Skipped:") {
		t.Errorf("expected skip notice in output: %q", out)
	}
	if !strings.Contains(out, "no goose markers") {
		t.Errorf("expected 'no goose markers' reason: %q", out)
	}
}

func TestMigrateImportRejectsUnknownFrom(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()

	var buf bytes.Buffer
	err := runMigrateImport(migrateImportOptions{
		From:    "dbmate",
		SrcDir:  src,
		DestDir: dest,
		Stdout:  &buf,
	})
	if err == nil {
		t.Fatal("expected error for unsupported --from")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("error should mention 'not supported', got: %v", err)
	}
}

func TestMigrateImportCommandWiring(t *testing.T) {
	cmd := newMigrateCmd()
	if cmd.Name() != "migrate" {
		t.Errorf("expected name 'migrate', got %q", cmd.Name())
	}
	imp := commandName(cmd, "import")
	if imp == nil {
		t.Fatal("expected 'import' subcommand under migrate")
	}
	for _, flag := range []string{"from", "src-dir", "dry-run", "force"} {
		if imp.Flag(flag) == nil {
			t.Errorf("expected --%s flag on migrate import", flag)
		}
	}
}
