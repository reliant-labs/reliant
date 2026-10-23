package migratekit

import (
	"strings"
	"testing"
	"testing/fstest"
)

// TestAutoMigrateSkipsNilFS pins the deliberate ASYMMETRY with Open, which
// refuses a nil FS.
//
// The two answer different questions. `db migrate up` is an explicit
// instruction and a binary embedding nothing has failed to carry it out. Boot
// runs on every startup, including a project that has not written its first
// migration — failing there would make a new project unable to start at all.
//
// If this ever flips to an error, a freshly scaffolded project stops booting,
// which is why the case is pinned rather than left to the reader of Open.
func TestAutoMigrateSkipsNilFS(t *testing.T) {
	if err := AutoMigrate(nil, "migrations")(nil, nil); err != nil {
		t.Fatalf("boot must tolerate a binary with no embedded migrations, "+
			"or a project that has written no SQL yet cannot start: %v", err)
	}
}

// TestAutoMigrateSkipsEmptyMigrationSet is the ONE case that is allowed to
// succeed without touching the database: a project whose first migration is
// unwritten. It returns before opening a driver, which is why a nil *sql.DB
// is a valid argument here and is what makes the assertion meaningful.
func TestAutoMigrateSkipsEmptyMigrationSet(t *testing.T) {
	// A .gitkeep is the realistic shape: the directory exists in git,
	// carries no SQL, and must not be mistaken for a migration set.
	fsys := fstest.MapFS{"migrations/.gitkeep": {Data: []byte{}}}

	if err := AutoMigrate(fsys, "migrations")(nil, nil); err != nil {
		t.Fatalf("an empty migration set is a normal state for a new project, not an error: %v", err)
	}
}

// TestAutoMigrateReportsMissingDir distinguishes "no migrations yet" from "the
// embed is broken". Both leave the database unmigrated, but they call for
// different fixes — write a migration vs. fix the build — so they must not
// collapse into the same silent skip.
func TestAutoMigrateReportsMissingDir(t *testing.T) {
	fsys := fstest.MapFS{"elsewhere/0001_init.up.sql": {Data: []byte("SELECT 1;")}}

	err := AutoMigrate(fsys, "migrations")(nil, nil)
	if err == nil {
		t.Fatal("a missing migrations dir is a packaging bug and must not be reported as an empty set")
	}
	if !strings.Contains(err.Error(), "migrations") {
		t.Errorf("error does not name the directory it failed to read: %v", err)
	}
}

// TestHasSQLDistinguishesCompanionFilesFromMigrations is the unit behind the
// skip decision, and the reason it is not `len(entries) == 0`.
//
// A directory holding only companion files (.gitkeep, a README) has no
// migrations to apply, but is not empty. Reading it as non-empty sends an
// empty set to iofs.New, which fails — turning a healthy fresh project into a
// boot failure.
func TestHasSQLDistinguishesCompanionFilesFromMigrations(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]*fstest.MapFile
		want  bool
	}{
		{"empty dir", map[string]*fstest.MapFile{}, false},
		{"gitkeep only", map[string]*fstest.MapFile{"d/.gitkeep": {}}, false},
		{"readme only", map[string]*fstest.MapFile{"d/README.md": {}}, false},
		{"a migration", map[string]*fstest.MapFile{"d/0001_init.up.sql": {}}, true},
		{"migration beside companions", map[string]*fstest.MapFile{
			"d/.gitkeep": {}, "d/0001_init.up.sql": {},
		}, true},
		// ".sql" as the WHOLE name is not a migration — the length guard
		// in hasSQL is what keeps a bare extension from counting.
		{"bare extension", map[string]*fstest.MapFile{"d/.sql": {}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS(tc.files)
			fsys["d/placeholder"] = &fstest.MapFile{} // ensure d/ exists
			entries, err := fsys.ReadDir("d")
			if err != nil {
				t.Fatalf("read fixture dir: %v", err)
			}
			// The placeholder is not .sql, so it never changes the answer.
			if got := hasSQL(entries); got != tc.want {
				t.Errorf("hasSQL = %v, want %v", got, tc.want)
			}
		})
	}
}
