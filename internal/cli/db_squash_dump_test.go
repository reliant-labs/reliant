// File: internal/cli/db_squash_dump_test.go
//
// squashBaseline against real pg_dump output and against the lexing edge
// cases a line filter gets wrong, then the baseline it produces run through
// migratekit — together with the next migration, in ONE run — against a real
// postgres.
//
// The next migration names its tables unqualified, the way every
// hand-written migration does. A baseline that kept pg_dump's
// `set_config('search_path', '', false)` left that setting on the migrator's
// session, and that migration failed with `relation "accounts" does not
// exist`. Under AutoMigrate the session is a connection borrowed from the
// app's pool, so the test reads the search_path back from the pool too.
//
// `forge db squash` itself — its own docker postgres, pg_dump and all — is
// driven end to end in db_squash_integration_test.go.

package cli

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/reliant-labs/forge/pkg/migratekit"
)

// squashFixture is pg_dump 16.15 (`--no-owner --no-privileges --inserts`, the
// flags squash passes) over the history in db_squash_integration_test.go: a
// function body and an inserted row that each hold a line beginning with
// `SET ` and one beginning with `\`, and a LANGUAGE sql function naming a
// table pg_dump writes after it.
const squashFixture = "pg_dump_16.15_inserts.sql"

func readSquashFixture(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "squash", squashFixture))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// afterSquash is the first migration written on top of the baseline.
const afterSquash = `ALTER TABLE accounts ADD COLUMN plan_id INT REFERENCES plans (id);
INSERT INTO plans (id, kind, account_id) VALUES (1, 'pro', 1);
UPDATE accounts SET plan_id = 1;
`

// squashSeededNote is the note the squashed history inserted; the baseline
// must carry it byte for byte.
const squashSeededNote = "line one\nSET search_path = evil;\n\\restrict nope\nend"

// The four lines inside quotes must survive; the prelude and the \restrict
// pair must not.
func TestSquashBaseline_PgDump16StripsThePreludeAndKeepsEveryStatement(t *testing.T) {
	dump := readSquashFixture(t)
	got := squashBaseline(dump)

	// Every line of the result that begins with SET or a backslash is one
	// of the four inside a quote — never the prelude, never a meta-command.
	var setLines, metaLines []string
	for _, line := range strings.Split(got, "\n") {
		switch {
		case strings.HasPrefix(line, "SET "):
			setLines = append(setLines, line)
		case strings.HasPrefix(line, `\`):
			metaLines = append(metaLines, line)
		}
	}
	if want := []string{"SET LOCAL search_path = public;", "SET search_path = evil;"}; !slices.Equal(setLines, want) {
		t.Errorf("SET lines in the baseline = %q, want only the two inside the function body and the row %q", setLines, want)
	}
	if want := []string{`\restrict not-a-meta');`, `\restrict nope`}; !slices.Equal(metaLines, want) {
		t.Errorf("backslash lines in the baseline = %q, want only the two inside quotes %q", metaLines, want)
	}
	if strings.Contains(got, "set_config('search_path'") {
		t.Errorf("the baseline still empties the search_path:\n%s", got)
	}

	// Kept statements are byte-identical to what pg_dump wrote.
	for _, stmt := range []string{
		"CREATE FUNCTION public.touch() RETURNS trigger\n    LANGUAGE plpgsql\n    AS $_$\nBEGIN\nSET LOCAL search_path = public;\n  NEW.name := NEW.name || $$x$$;\n  NEW.note := coalesce(NEW.note, 'a\n\\restrict not-a-meta');\n  RETURN NEW;\nEND\n$_$;",
		"CREATE FUNCTION public.account_count() RETURNS bigint\n    LANGUAGE sql\n    AS $$\nSELECT count(*) FROM accounts\n$$;",
		"INSERT INTO public.accounts VALUES (1, 'acmex', 'line one\nSET search_path = evil;\n\\restrict nope\nend');",
		"COMMENT ON TABLE public.accounts IS 'customer accounts; SET x = 1';",
		"SELECT pg_catalog.setval('public.accounts_id_seq', 1, true);",
		"CREATE TRIGGER accounts_touch BEFORE INSERT ON public.accounts FOR EACH ROW EXECUTE FUNCTION public.touch();",
		"ALTER TABLE ONLY public.plans\n    ADD CONSTRAINT plans_account_id_fkey FOREIGN KEY (account_id) REFERENCES public.accounts(id);",
	} {
		if !strings.Contains(dump, stmt) {
			t.Fatalf("fixture drift: the dump does not contain %q", stmt)
		}
		if !strings.Contains(got, stmt) {
			t.Errorf("the baseline lost or altered a statement:\n%s\n--- baseline ---\n%s", stmt, got)
		}
	}

	// The one setting a function-defining baseline still needs, scoped to
	// its own transaction, ahead of the first function.
	if n := strings.Count(got, "set_config('check_function_bodies', 'false', true)"); n != 1 {
		t.Errorf("want exactly one transaction-local check_function_bodies, got %d:\n%s", n, got)
	}
	if strings.Index(got, "check_function_bodies") > strings.Index(got, "CREATE FUNCTION") {
		t.Errorf("check_function_bodies is set after the first CREATE FUNCTION:\n%s", got)
	}

	// pg_dump's own comments stay, and the dropped prelude leaves no run of
	// blank lines longer than pg_dump's own.
	for _, comment := range []string{"-- PostgreSQL database dump\n", "-- Dumped by pg_dump version 16.15\n", "-- PostgreSQL database dump complete\n"} {
		if !strings.Contains(got, comment) {
			t.Errorf("pg_dump comment %q was dropped", comment)
		}
	}
	if strings.Contains(got, "\n\n\n\n") {
		t.Errorf("the dropped statements left a run of blank lines:\n%s", got)
	}
}

func TestSquashBaseline_LexesLikePostgres(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "a SET line inside a dollar-quoted body, with a nested $$ quote, is body text",
			in:   "SET a = 1;\nCREATE FUNCTION f() RETURNS text LANGUAGE plpgsql AS $fn$\nBEGIN\nSET search_path = x;\nRETURN $$;$$;\nEND\n$fn$;\nSET b = 2;\n",
			want: checkFunctionBodiesPreamble + "\nCREATE FUNCTION f() RETURNS text LANGUAGE plpgsql AS $fn$\nBEGIN\nSET search_path = x;\nRETURN $$;$$;\nEND\n$fn$;\n\n",
		},
		{
			name: "an escape string's backslash-quote does not end it",
			in:   "INSERT INTO t VALUES (E'it\\'s;\n\\restrict k\nSET y;');\nSET z = 1;\n",
			want: "INSERT INTO t VALUES (E'it\\'s;\n\\restrict k\nSET y;');\n\n",
		},
		{
			name: "a standard string's doubled quote does not end it",
			in:   "INSERT INTO t VALUES ('it''s;\nSET y;');\n",
			want: "INSERT INTO t VALUES ('it''s;\nSET y;');\n",
		},
		{
			name: "a quoted identifier can hold a semicolon and SET",
			in:   "CREATE TABLE \"we;ird\" (\"SET\" int);\nSET x = 1;\n",
			want: "CREATE TABLE \"we;ird\" (\"SET\" int);\n\n",
		},
		{
			name: "block comments nest, and a semicolon inside one ends nothing",
			in:   "/* a /* b; */ SET x; */\nCREATE TABLE t (i int);\n",
			want: "/* a /* b; */ SET x; */\nCREATE TABLE t (i int);\n",
		},
		{
			name: "a positional parameter is not a dollar quote",
			in:   "CREATE FUNCTION f(int) RETURNS int LANGUAGE sql AS 'SELECT $1';\nSET x = 1;\n",
			want: checkFunctionBodiesPreamble + "CREATE FUNCTION f(int) RETURNS int LANGUAGE sql AS 'SELECT $1';\n\n",
		},
		{
			name: "a $ inside an identifier is not a dollar quote",
			in:   "CREATE TABLE a$b$ (i int);\nSET x = 1;\nCREATE TABLE c (i int);\n",
			want: "CREATE TABLE a$b$ (i int);\n\nCREATE TABLE c (i int);\n",
		},
		{
			name: "a meta-command inside a statement is dropped, the statement kept",
			in:   "CREATE TABLE t (\n\\unrestrict k\ni int);\n",
			want: "CREATE TABLE t (\ni int);\n",
		},
		{
			name: "setval is data and is kept; set_config in any spelling is dropped",
			in:   "SELECT pg_catalog.set_config('search_path', '', false);\nselect  set_config('x', 'y', false);\nSELECT pg_catalog.setval('s', 1, true);\n",
			want: "\n\nSELECT pg_catalog.setval('s', 1, true);\n",
		},
		{
			name: "a line comment holding a semicolon or a backslash line is a comment",
			in:   "-- SET x; \\restrict\nCREATE TABLE t (i int); -- trailing; SET\n",
			want: "-- SET x; \\restrict\nCREATE TABLE t (i int); -- trailing; SET\n",
		},
		{
			name: "of the privileges only the dashboard role's grants and PUBLIC's are kept",
			in: "CREATE TABLE public.t (id int, email text);\n" +
				"REVOKE CREATE ON SCHEMA public FROM PUBLIC;\n" +
				"REVOKE ALL ON FUNCTION public.f() FROM PUBLIC;\n" +
				"GRANT ALL ON FUNCTION public.f() TO app;\n" +
				"GRANT SELECT(id) ON TABLE public.t TO forge_dash;\n" +
				"GRANT SELECT(id),SELECT(email) ON TABLE public.t TO forge_ro;\n" +
				"GRANT SELECT ON TABLE public.t TO forge_dashboards;\n" +
				"REVOKE SELECT ON TABLE public.t FROM forge_ro;\n" +
				"ALTER DEFAULT PRIVILEGES FOR ROLE app IN SCHEMA public GRANT SELECT ON TABLES TO forge_dash;\n",
			want: "CREATE TABLE public.t (id int, email text);\nREVOKE CREATE ON SCHEMA public FROM PUBLIC;\nREVOKE ALL ON FUNCTION public.f() FROM PUBLIC;\n\nGRANT SELECT(id) ON TABLE public.t TO forge_dash;\n\n\n",
		},
		{
			name: "no function, no preamble",
			in:   "SET x = 1;\nCREATE TABLE t (i int);\n",
			want: "\nCREATE TABLE t (i int);\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := squashBaseline(tc.in); got != tc.want {
				t.Errorf("squashBaseline:\n--- in ---\n%s\n--- got ---\n%q\n--- want ---\n%q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSquashBaseline_AppliesWithTheNextMigrationInOneMigratekitRun(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a real postgres server")
	}
	release := fstest.MapFS{
		"20260102000000_baseline.up.sql": {Data: []byte(squashBaseline(readSquashFixture(t)))},
		"20260103000000_after.up.sql":    {Data: []byte(afterSquash)},
	}
	assertSquashedReleaseApplies(t, release)
}

// assertSquashedReleaseApplies runs a squashed baseline and the migration
// after it through both migratekit entry points — AutoMigrate (a server's
// boot, on a pool connection) and Open + Up (`db migrate up`) — each in one
// run against its own database.
func assertSquashedReleaseApplies(t *testing.T, release fs.FS) {
	t.Helper()
	t.Run("AutoMigrate returns a pool connection with its search_path intact", func(t *testing.T) {
		pool, err := sql.Open("postgres", scratchDatabase(t))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = pool.Close() }()
		// AutoMigrate migrates on one borrowed connection and reads its
		// records through the pool, so it needs two. Capping the pool there
		// means holding two connections at once afterwards reaches every
		// connection the pool has, the migrator's included.
		const poolSize = 2
		pool.SetMaxOpenConns(poolSize)
		pool.SetMaxIdleConns(poolSize)

		if err := migratekit.AutoMigrate(release, ".")(pool, nil); err != nil {
			t.Fatalf("baseline + next migration in one AutoMigrate run: %v", err)
		}
		ctx := context.Background()
		conns := make([]*sql.Conn, poolSize)
		for i := range conns {
			conn, err := pool.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			conns[i] = conn
		}
		for i, conn := range conns {
			var searchPath, checkBodies string
			if err := conn.QueryRowContext(ctx, "SHOW search_path").Scan(&searchPath); err != nil {
				t.Fatal(err)
			}
			if searchPath == "" || searchPath == `""` {
				t.Errorf("pool connection %d carries search_path %q after the run: the migrator handed its session back emptied", i, searchPath)
			}
			if err := conn.QueryRowContext(ctx, "SHOW check_function_bodies").Scan(&checkBodies); err != nil {
				t.Fatal(err)
			}
			if checkBodies != "on" {
				t.Errorf("pool connection %d has check_function_bodies = %q; the baseline's setting must end with its transaction", i, checkBodies)
			}
		}
		for _, conn := range conns {
			_ = conn.Close()
		}
		assertSquashedSchema(t, pool)
	})

	t.Run("Open + Up applies both", func(t *testing.T) {
		dsn := scratchDatabase(t)
		m, err := migratekit.Open(migratekit.Options{FS: release, DSN: dsn})
		if err != nil {
			t.Fatal(err)
		}
		res, err := m.Up()
		_ = m.Close()
		if err != nil {
			t.Fatalf("baseline + next migration in one Up: %v", err)
		}
		if res.After.Version != 20260103000000 {
			t.Errorf("schema at %d after Up, want 20260103000000", res.After.Version)
		}
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		assertSquashedSchema(t, db)
	})
}

// assertSquashedSchema checks that the squashed database is the one the
// history built: the seeded row, both functions and the trigger.
func assertSquashedSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	var note string
	var planID int
	if err := db.QueryRow("SELECT note, plan_id FROM accounts WHERE id = 1").Scan(&note, &planID); err != nil {
		t.Fatal(err)
	}
	if note != squashSeededNote || planID != 1 {
		t.Errorf("accounts row 1 = (%q, %d), want (%q, 1)", note, planID, squashSeededNote)
	}
	var name string
	if err := db.QueryRow("INSERT INTO accounts (name) VALUES ('b') RETURNING name, note").Scan(&name, &note); err != nil {
		t.Fatalf("insert through the squashed trigger: %v", err)
	}
	if want := "a\n\\restrict not-a-meta"; name != "bx" || note != want {
		t.Errorf("the trigger's function body changed: insert returned (%q, %q), want (\"bx\", %q)", name, note, want)
	}
	var n int
	if err := db.QueryRow("SELECT account_count()").Scan(&n); err != nil || n != 2 {
		t.Errorf("account_count() = %d, %v; want 2", n, err)
	}
}
