package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
)

// Every project-relative db input resolves against the -C project root —
// the default db/migrations, a relative --dir, a bare migration filename
// — no matter where the CWD is. Flag DEFAULTS used to be computed when the
// command tree was built, before -C was parsed, and then opened against the
// CWD.
func TestResolveMigrationsDir_AnchorsAtTheProjectRoot(t *testing.T) {
	clearProjectDir(t)
	proj := writeProjectDirFixture(t, "anchored")
	elsewhere(t)
	if err := cmdutil.SetProjectDir(proj); err != nil {
		t.Fatal(err)
	}

	if got, want := resolveMigrationsDir(""), filepath.Join(proj, "db", "migrations"); got != want {
		t.Errorf("default migrations dir = %q, want %q", got, want)
	}
	if got, want := resolveMigrationsDir("sql/migrations"), filepath.Join(proj, "sql", "migrations"); got != want {
		t.Errorf("relative --dir = %q, want %q (project-relative, not CWD-relative)", got, want)
	}
	// Absolute for THIS platform: "/abs/migrations" has no volume, so on
	// Windows it is not absolute (filepath.IsAbs) and rightly resolves
	// against the project like any other relative path.
	abs := filepath.Join(t.TempDir(), "abs", "migrations")
	if got := resolveMigrationsDir(abs); got != abs {
		t.Errorf("absolute --dir = %q, want it untouched (%q)", got, abs)
	}
	migDir := resolveMigrationsDir("")
	if got, want := resolveMigrationFileArg(migDir, "20260101000000_x.up.sql"), filepath.Join(migDir, "20260101000000_x.up.sql"); got != want {
		t.Errorf("bare migration filename = %q, want it inside the migrations dir %q", got, want)
	}

	// The migrations directory is db/migrations, resolved from the -C project.
	// There is no database.migrations_dir to point elsewhere.
	body := "name: anchored\nmodule_path: github.com/example/anchored\n"
	if err := os.WriteFile(filepath.Join(proj, "forge.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := resolveMigrationsDir(""), filepath.Join(proj, "db", "migrations"); got != want {
		t.Errorf("migrations dir = %q, want %q", got, want)
	}

	// A directory that does not exist is an error naming it — never "no
	// migrations".
	if _, err := requireMigrationsDir(""); err == nil || !strings.Contains(err.Error(), filepath.Join(proj, "db", "migrations")) {
		t.Errorf("requireMigrationsDir on a missing dir = %v; want an error naming it", err)
	}
}

// isLoopbackDSN reads a DSN the way pgx will dial it.
func TestIsLoopbackDSN(t *testing.T) {
	t.Setenv("PGHOST", "")
	cases := []struct {
		dsn  string
		want bool
	}{
		{"postgres://u:p@localhost:55432/scratch?sslmode=disable", true},
		{"postgres://u:p@127.0.0.1:5999/x?sslmode=disable", true},
		{"postgres://u:p@127.0.0.42/x", true},
		{"postgres://u:p@[::1]:5433/x", true},
		{"postgres:///x?host=/var/run/postgresql", true}, // unix socket
		{"host=localhost port=5499 dbname=x sslmode=disable", true},
		{"host=/tmp dbname=x", true},
		{"postgres://u:p@db.example.com:5432/app", false},
		{"postgres://u:p@10.0.0.5:5432/app", false},
		{"postgres://u:p@192.0.2.10/app", false},
		{"host=db.example.com dbname=app", false},
		{"postgres://u:p@localhost,db.example.com/app", false}, // a remote fallback
		{"://not a dsn", false},
	}
	for _, c := range cases {
		if got := isLoopbackDSN(c.dsn); got != c.want {
			t.Errorf("isLoopbackDSN(%q) = %v, want %v", c.dsn, got, c.want)
		}
	}

	// A DSN with no host connects wherever PGHOST says — so PGHOST decides.
	t.Setenv("PGHOST", "db.example.com")
	if isLoopbackDSN("postgres:///x") {
		t.Error("a hostless DSN with PGHOST=db.example.com is NOT loopback")
	}
}

// The seed DSN policy: an explicit loopback --dsn passes whatever the env
// declares; a remote one passes only as the env's own database or with the
// override; an AMBIENT $DATABASE_URL gets no relaxation at all.
func TestResolveSeedWriteDSN_Policy(t *testing.T) {
	ctx := context.Background()
	const declared = "postgres://postgres:postgres@localhost:5432/app_dev?sslmode=disable"
	proj := devProject(t, declared)

	loopback := "postgres://postgres:postgres@127.0.0.1:55432/scratch?sslmode=disable"
	if got, err := resolveSeedWriteDSN(ctx, loopback, proj, "dev", false); err != nil || got != loopback {
		t.Errorf("loopback --dsn = (%q, %v); want it accepted", got, err)
	}

	remote := "postgres://app:hunter2@db.example.com:5432/app?sslmode=require"
	if _, err := resolveSeedWriteDSN(ctx, remote, proj, "dev", false); err == nil {
		t.Error("a remote --dsn that is not the env's database was accepted without --allow-remote-dsn")
	} else if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("refusal leaked a password: %v", err)
	}
	if got, err := resolveSeedWriteDSN(ctx, remote, proj, "dev", true); err != nil || got != remote {
		t.Errorf("remote --dsn with --allow-remote-dsn = (%q, %v); want it accepted", got, err)
	}

	// An ambient $DATABASE_URL naming a loopback database the env does not
	// declare stays refused: nobody chose it for this command.
	t.Setenv("DATABASE_URL", loopback)
	if _, err := resolveSeedWriteDSN(ctx, "", proj, "dev", false); err == nil {
		t.Error("an ambient $DATABASE_URL that is not the env's database was accepted; only an explicit --dsn is relaxed")
	}

	// The env's own database is accepted even when it is remote. (Last: a
	// second devProject repoints the process-wide KCL render fixture.)
	remoteProj := devProject(t, remote)
	if _, err := resolveSeedWriteDSN(ctx, "postgres://other:creds@db.example.com:5432/app", remoteProj, "dev", false); err != nil {
		t.Errorf("the env's own (remote) database was refused: %v", err)
	}
}
