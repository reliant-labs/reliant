// File: internal/cli/db_squash_dsn.go
//
// `forge db squash --from-dsn`: squash a database another tool migrated into
// one baseline.
//
// `forge db squash` replays forge's own migrations in a throwaway container,
// so it reads only `*.up.sql`. A project leaving goose (or any other tool) has
// a history forge cannot replay — goose's annotations, Go migrations, another
// tool's file format — and reliant had to pg_dump a goose-built database by
// hand, strip pg_dump's prelude by hand (the hazard #678 removed from squash)
// and type the `-- forge:adopts goose …` list.
//
// A LIVE DATABASE IS THE GENERAL INPUT. Whatever tool built it, its schema is
// the history's result, so dumping it needs no knowledge of the tool and no
// goose dependency. The database should be a throwaway the COMPLETE history
// built (`goose up` on an empty database): its rows become INSERTs in every
// fresh database, which is why the command prints them.
//
// THE ADOPTION LIST COMES FROM THE SAME DATABASE AS THE SCHEMA. When the
// database has goose_db_version, the baseline declares every version goose
// recorded as applied there, read as migratekit reads it (latest row per
// version decides, version 0 is goose's own). The list then describes exactly
// the schema in the file: a production database whose goose history differs
// is refused at adoption, with the versions named, rather than adopted onto a
// schema it does not have.
//
// The dump itself is the same as the replayed one: pg_dump in a container of
// the server's major version, then squashBaseline (db_squash_dump.go), which
// keeps every statement that builds the database and drops what configures
// the session.

package cli

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/database"
	"github.com/reliant-labs/forge/pkg/migratekit/compat"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// squashGooseTable is goose's default version table, the one migratekit
// adopts from (pkg/migratekit/adopt.go). Unqualified, as goose creates it.
const squashGooseTable = "goose_db_version"

// squashBookkeepingTables are left out of every --from-dsn baseline: forge's
// own bookkeeping (a database forge also migrated) and goose's, which
// adoption reads from the database being adopted and the baseline must never
// create.
//
// goose's sequence is named on its own. Older goose releases created the table
// with `id serial` (current ones use an identity column), and pg_dump leaves
// out the identity sequence of a
// table it excludes but NOT a serial's owned sequence: without this entry, a
// baseline cut from such a database creates an orphan goose_db_version_id_seq
// in every fresh database.
var squashBookkeepingTables = []string{
	"public.schema_migrations",
	"public.schema_migrations_compat",
	"public.schema_migrations_applied",
	squashGooseTable,
	squashGooseTable + "_id_seq",
}

// adoptsPerLine is how many versions one `-- forge:adopts` line carries; the
// directive repeats to wrap a long history.
const adoptsPerLine = 8

func runDBSquashFromDSN(ctx context.Context, w io.Writer, opts squashOptions) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker not found on PATH (`forge db squash --from-dsn` runs pg_dump in a postgres container of the server's major version)")
	}
	if err := checkSquashDSN(opts.FromDSN); err != nil {
		return err
	}
	db, err := database.ConnectDB(ctx, opts.FromDSN)
	if err != nil {
		return fmt.Errorf("--from-dsn: %w", err)
	}
	defer func() { _ = db.Close() }()

	major, err := serverMajor(ctx, db)
	if err != nil {
		return fmt.Errorf("--from-dsn: %w", err)
	}
	goose, err := readGooseHistory(ctx, db)
	if err != nil {
		return fmt.Errorf("--from-dsn: %w", err)
	}
	excludes := append(slices.Clone(squashBookkeepingTables), opts.ExcludeTables...)
	lookalikes, err := historyLookalikes(ctx, db, excludes)
	if err != nil {
		return fmt.Errorf("--from-dsn: %w", err)
	}

	image := opts.Image
	if image == "" {
		image = fmt.Sprintf("postgres:%d-alpine", major)
	}
	outDir := opts.OutDir
	if outDir == "" {
		outDir = resolveMigrationsDir("")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create --out-dir %q: %w", outDir, err)
	}

	_, _ = fmt.Fprintf(w, "Dumping the database with pg_dump (%s)...\n", image)
	dump, err := pgDumpInDocker(ctx, image, opts.FromDSN, excludes, runtime.GOOS)
	if err != nil {
		return err
	}
	cleaned := squashBaseline(string(dump))

	stem := opts.Baseline
	if stem == "" {
		stem = "00001_baseline"
	}
	upPath := filepath.Join(outDir, stem+".up.sql")
	content := dsnSquashHeader(goose, excludes) + cleaned
	// Read the file back as the migrator will: an adoption it cannot parse,
	// or one listing other versions, would refuse every database at boot.
	if err := checkWrittenAdoption(content, goose); err != nil {
		return fmt.Errorf("the baseline for %s: %w", upPath, err)
	}
	if err := atomicfile.WriteFile(upPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write baseline up: %w", err)
	}

	_, _ = fmt.Fprintf(w, "\n  Wrote %s\n", upPath)
	printBaselineRows(w, cleaned)
	if len(lookalikes) > 0 {
		_, _ = fmt.Fprintf(w, "\nThese tables look like another migration tool's history, and ARE in the baseline: %s.\n", strings.Join(lookalikes, ", "))
		_, _ = fmt.Fprintln(w, "If one is, re-run with --exclude-table <schema.table> for it: a fresh database must not inherit it.")
	}
	printDSNSquashNextSteps(w, upPath, goose)
	return nil
}

// checkWrittenAdoption parses a baseline the way the migrator does and
// confirms it declares exactly goose's history, or nothing when there is none.
func checkWrittenAdoption(content string, goose *gooseHistory) error {
	got, err := compat.ParseAdoption(content)
	if err != nil {
		return fmt.Errorf("its adoption declaration does not parse: %w", err)
	}
	switch {
	case goose == nil && got != nil:
		return fmt.Errorf("it declares an adoption (line %d) though the database has no goose history", got.Line)
	case goose != nil && (got == nil || !slices.Equal(got.Versions, goose.Versions)):
		return fmt.Errorf("its adoption declaration does not list the %d versions goose applied", len(goose.Versions))
	}
	return nil
}

// checkSquashDSN accepts a postgres:// URL, the one DSN form the docker
// rewrite below can read.
func checkSquashDSN(dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		return fmt.Errorf("--from-dsn must be a postgres://user:pass@host:port/dbname URL")
	}
	return nil
}

// serverMajor returns the server's major version: pg_dump refuses a server
// newer than itself, so the dump runs in that version's image.
func serverMajor(ctx context.Context, db *sql.DB) (int, error) {
	var num int
	if err := db.QueryRowContext(ctx, "SELECT current_setting('server_version_num')::int").Scan(&num); err != nil {
		return 0, fmt.Errorf("read the server version: %w", err)
	}
	return num / 10000, nil
}

// gooseHistory is what goose recorded as applied in the source database.
type gooseHistory struct {
	// Versions is every applied version, ascending.
	Versions []int64
}

// readGooseHistory reads goose's applied set, or nil when the database has no
// goose_db_version. It reads the table exactly as migratekit's adoption does
// (readGooseApplied in pkg/migratekit/adopt.go), because the list written here
// is the list that check compares against: the latest row for a version
// decides (older goose wrote is_applied = false rows on rollback), and
// version 0 is the row goose writes when it creates the table. The
// end-to-end test adopts the source database with the written baseline,
// which pins the two readers together.
func readGooseHistory(ctx context.Context, db *sql.DB) (*gooseHistory, error) {
	var exists bool
	if err := db.QueryRowContext(ctx, "SELECT to_regclass('"+squashGooseTable+"') IS NOT NULL").Scan(&exists); err != nil {
		return nil, fmt.Errorf("look for %s: %w", squashGooseTable, err)
	}
	if !exists {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, "SELECT version_id, is_applied FROM "+squashGooseTable+" ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", squashGooseTable, err)
	}
	defer func() { _ = rows.Close() }()
	latest := map[int64]bool{}
	for rows.Next() {
		var (
			v       int64
			applied bool
		)
		if err := rows.Scan(&v, &applied); err != nil {
			return nil, fmt.Errorf("read %s: %w", squashGooseTable, err)
		}
		latest[v] = applied
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", squashGooseTable, err)
	}
	h := &gooseHistory{}
	for v, applied := range latest {
		if applied && v != 0 {
			h.Versions = append(h.Versions, v)
		}
	}
	if len(h.Versions) == 0 {
		return nil, fmt.Errorf("%s records no applied migration: point --from-dsn at a database the goose history built (`goose up` on an empty database)", squashGooseTable)
	}
	sort.Slice(h.Versions, func(i, j int) bool { return h.Versions[i] < h.Versions[j] })
	return h, nil
}

// historyLookalikePattern matches a table name another migration tool may
// keep its history in (flyway_schema_history, alembic_version,
// django_migrations, _prisma_migrations, knex_migrations, …).
var historyLookalikePattern = regexp.MustCompile(`(?i)migration|schema_version|schema_history|alembic_version|revisions`)

// historyLookalikes lists tables that look like a migration tool's history
// and are not excluded from the dump. The command cannot know every tool, so
// it shows the user what it is about to carry into every fresh database
// rather than guess.
func historyLookalikes(ctx context.Context, db *sql.DB, excludes []string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT table_schema, table_name FROM information_schema.tables
		WHERE table_type = 'BASE TABLE' AND table_schema NOT IN ('pg_catalog', 'information_schema')
		ORDER BY 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var schema, table string
		if err := rows.Scan(&schema, &table); err != nil {
			return nil, fmt.Errorf("list tables: %w", err)
		}
		if historyLookalikePattern.MatchString(table) && !excludedTable(excludes, schema, table) {
			out = append(out, schema+"."+table)
		}
	}
	return out, rows.Err()
}

// excludedTable reports whether one of pg_dump's --exclude-table names (a
// bare name matches the table in any schema) covers schema.table.
func excludedTable(excludes []string, schema, table string) bool {
	for _, e := range excludes {
		if e == table || e == schema+"."+table {
			return true
		}
	}
	return false
}

// pgDumpInDocker runs pg_dump against dsn from a container of image. The
// password travels in the environment (`-e PGPASSWORD`, no value), never on
// the command line where `ps` shows it.
func pgDumpInDocker(ctx context.Context, image, dsn string, excludes []string, goos string) ([]byte, error) {
	target, netArgs, password, err := dockerDumpTarget(dsn, goos)
	if err != nil {
		return nil, err
	}
	args := append([]string{"run", "--rm"}, netArgs...)
	if password != "" {
		args = append(args, "-e", "PGPASSWORD")
	}
	args = append(args, image, "pg_dump", "--no-owner", "--no-privileges", "--inserts")
	for _, t := range excludes {
		args = append(args, "--exclude-table="+t)
	}
	args = append(args, "--dbname="+target)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = append(os.Environ(), "PGPASSWORD="+password)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("pg_dump (in %s) failed: %w", image, err)
	}
	return out, nil
}

// dockerDumpTarget turns the DSN the host reached into one a container
// reaches, with the docker networking flags that make it so, and lifts the
// password out of it.
//
// On Linux the container shares the host's network (`--network host`), so the
// DSN works unchanged — a server bound to 127.0.0.1 included, which the
// docker bridge cannot reach. Docker Desktop (macOS, Windows) runs containers
// in a VM where loopback is the VM's own, so a loopback host becomes
// host.docker.internal, which Desktop routes to the host.
func dockerDumpTarget(dsn, goos string) (target string, netArgs []string, password string, err error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", nil, "", fmt.Errorf("--from-dsn: %w", err)
	}
	if u.User != nil {
		password, _ = u.User.Password()
		u.User = url.User(u.User.Username())
	}
	switch {
	case goos == "linux":
		netArgs = []string{"--network", "host"}
	case isLoopbackHost(u.Hostname()):
		host := "host.docker.internal"
		if port := u.Port(); port != "" {
			host = net.JoinHostPort(host, port)
		}
		u.Host = host
		netArgs = []string{"--add-host", "host.docker.internal:host-gateway"}
	}
	return u.String(), netArgs, password, nil
}

// dsnSquashHeader is the comment a --from-dsn baseline opens with. Like the
// replayed squash's, it is a function of the squashed history alone — no DSN
// (it carries credentials and names one machine), no time — so a re-cut of the
// same database writes the same bytes.
func dsnSquashHeader(goose *gooseHistory, excludes []string) string {
	var b strings.Builder
	if goose == nil {
		b.WriteString("-- Generated by `forge db squash --from-dsn` from a database with no goose history (no " + squashGooseTable + "),\n")
		b.WriteString("-- so it declares no adoption.\n")
	} else {
		vs := goose.Versions
		fmt.Fprintf(&b, "-- Generated by `forge db squash --from-dsn` from a database goose migrated: %d versions, %d through %d.\n", len(vs), vs[0], vs[len(vs)-1])
		b.WriteString("-- It replaces that history: a database goose migrated is adopted, never re-run (see the\n")
		b.WriteString("-- forge skill db/deploy-migrations, \"Adopting a database another tool migrated\").\n")
		for i := 0; i < len(vs); i += adoptsPerLine {
			end := min(i+adoptsPerLine, len(vs))
			b.WriteString(compat.AdoptsDirective + " " + compat.ToolGoose)
			for _, v := range vs[i:end] {
				fmt.Fprintf(&b, " %d", v)
			}
			b.WriteByte('\n')
		}
	}
	b.WriteString("-- pg_dump --no-owner --no-privileges --inserts")
	for _, t := range excludes {
		b.WriteString(" --exclude-table=" + t)
	}
	b.WriteString("\n-- pg_dump's session prelude (SET, set_config) and psql meta-commands (\\restrict) are stripped.\n\n")
	return b.String()
}

// insertTableRe matches the table an INSERT pg_dump wrote targets.
var insertTableRe = regexp.MustCompile(`(?m)^INSERT INTO ([^\s(]+)`)

// printBaselineRows says which rows the baseline carries. A baseline cut from a
// live database instead of a throwaway one carries every customer row in it,
// into every fresh database; this is where that shows.
func printBaselineRows(w io.Writer, baseline string) {
	counts := map[string]int{}
	total := 0
	for _, m := range insertTableRe.FindAllStringSubmatch(baseline, -1) {
		counts[m[1]]++
		total++
	}
	if total == 0 {
		_, _ = fmt.Fprintln(w, "  It carries no rows: schema only.")
		return
	}
	tables := make([]string, 0, len(counts))
	for t := range counts {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	parts := make([]string, len(tables))
	for i, t := range tables {
		parts[i] = fmt.Sprintf("%s (%d)", t, counts[t])
	}
	_, _ = fmt.Fprintf(w, "  It carries %d row(s), which every fresh database will get: %s.\n", total, strings.Join(parts, ", "))
}

// printDSNSquashNextSteps tells the user how the baseline meets the databases
// the original history built.
func printDSNSquashNextSteps(w io.Writer, upPath string, goose *gooseHistory) {
	_, _ = fmt.Fprintln(w, "\nNext steps:")
	_, _ = fmt.Fprintln(w, "  1. Review the baseline: it should hold what the history built (its schema, and the rows its")
	_, _ = fmt.Fprintln(w, "     migrations inserted) and nothing an application wrote.")
	if goose == nil {
		_, _ = fmt.Fprintln(w, "  2. The database had no goose history, so the baseline declares no adoption: a database the")
		_, _ = fmt.Fprintln(w, "     original history built would RUN it and fail on its first CREATE. --from-dsn declares only a")
		_, _ = fmt.Fprintln(w, "     goose history. A history forge's own migrations built is squashed from them instead (`forge db")
		_, _ = fmt.Fprintln(w, "     squash` without --from-dsn), which declares it; rebuild any other database before this release")
		_, _ = fmt.Fprintln(w, "     reaches it.")
	} else {
		_, _ = fmt.Fprintf(w, "  2. Make %s the FIRST migration in the directory: only the first may\n", filepath.Base(upPath))
		_, _ = fmt.Fprintln(w, "     declare an adoption (`forge lint` reports `invalid-adopts-directive` otherwise).")
		_, _ = fmt.Fprintf(w, "  3. A database goose migrated adopts it on its next `migrate up` or AutoMigrate: the migrator checks\n")
		_, _ = fmt.Fprintf(w, "     that goose applied exactly these %d versions, records the baseline as applied, and runs none\n", len(goose.Versions))
		_, _ = fmt.Fprintln(w, "     of its SQL. One behind or ahead of this history is refused, versions named, and nothing changes.")
		_, _ = fmt.Fprintln(w, "  4. Prove it before shipping: `forge db migrate up --dsn <a copy of a goose database>` adopts it;")
		_, _ = fmt.Fprintln(w, "     a fresh database applies it; `pg_dump --schema-only` both and diff (forge skill")
		_, _ = fmt.Fprintln(w, "     db/deploy-migrations). `forge db migrate status --dsn <dsn>` previews any database.")
	}
	_, _ = fmt.Fprintln(w, "     Never `forge db migrate force` the baseline onto a database: force records it as applied")
	_, _ = fmt.Fprintln(w, "     without checking that the schema is the one it builds.")
}
