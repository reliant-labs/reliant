// Package migrationhook runs a project's own migration tool — goose, atlas,
// flyway, dbmate, golang-migrate, or a command of its own — wherever forge
// would otherwise apply migrations with its own migrator
// (forge.yaml `database.migrations:`; config.MigrationsConfig).
//
// It is the migrations counterpart of `generate.hooks`: forge does not read
// or convert the team's migrations, it runs the team's tool against a
// database and trusts the exit code. What it adds over a generate hook is a
// contract the tool can act on: the database to migrate (DATABASE_URL, plus
// the variables each built-in tool reads natively), where the migrations are
// (FORGE_MIGRATIONS_DIR), and why it is being run (FORGE_PHASE).
//
// The consumers are the places forge applied migrations itself: the shadow
// database every schema reader introspects (internal/shadowdb — the ORM
// projection, seeding, the CRUD fixtures, frontend mocks), and `forge db
// migrate up` / `status`.
package migrationhook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
)

// The phases FORGE_PHASE names: why forge is running the tool.
const (
	// PhaseShadow applies the migrations to a throwaway database forge
	// introspects (the ORM projection and every other schema reader).
	PhaseShadow = "shadow"
	// PhaseCLI is `forge db migrate up` / `status` against the env's
	// database.
	PhaseCLI = "cli"
)

// Hook is a project's resolved migration tool: the argv forge runs, with a
// built-in tool's defaults already filled in.
type Hook struct {
	// Tool is config.DatabaseConfig.MigrationTool: goose, atlas, flyway,
	// dbmate, golang-migrate or custom.
	Tool string
	// ProjectDir is the absolute project root; the commands run there.
	ProjectDir string
	// RelDir is the project-relative migrations directory (slash form) and
	// Dir the same, absolute.
	RelDir string
	Dir    string
	// Apply applies every pending migration to $DATABASE_URL.
	Apply []string
	// Status reports what is applied. Empty when a custom tool declared
	// none.
	Status []string
	// Check exits 0 only when $DATABASE_URL is fully migrated
	// (config.MigrationsConfig.Check). Empty unless declared: no built-in
	// tool has one, forge reads its ledger instead (ReadLedger).
	Check []string
}

// Resolve returns the hook the project at projectDir declares in db, and
// false when forge's own migrator applies its migrations (tool forge, the
// default) — the caller then does what it always did.
func Resolve(projectDir string, db config.DatabaseConfig) (Hook, bool) {
	if !db.UsesMigrationHook() {
		return Hook{}, false
	}
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		abs = projectDir
	}
	h := Hook{
		Tool:       db.MigrationTool(),
		ProjectDir: abs,
		RelDir:     db.MigrationsPath(),
	}
	h.Dir = filepath.Join(abs, filepath.FromSlash(h.RelDir))
	h.Apply, h.Status = Preset(h.Tool)
	if len(db.Migrations.Apply) > 0 {
		h.Apply = db.Migrations.Apply
	}
	if len(db.Migrations.Status) > 0 {
		h.Status = db.Migrations.Status
	}
	h.Check = db.Migrations.Check
	return h, true
}

// ResolveIn is Resolve over the `database:` block of projectDir's forge.yaml
// (config.DatabaseIn), for code that has a project directory and no loaded
// config.
func ResolveIn(projectDir string) (Hook, bool) {
	return Resolve(projectDir, config.DatabaseIn(projectDir))
}

// ResolveForDir is Resolve over the database whose migrations live in migDir
// (config.DatabaseForMigrationsDir): when the env declaration declares
// several databases, each directory is applied by its own database's tool. A
// directory no database declares resolves as the primary's (ResolveIn).
func ResolveForDir(projectDir, migDir string) (Hook, bool) {
	return Resolve(projectDir, config.DatabaseForMigrationsDir(projectDir, migDir))
}

// ResolveForNamedDir is ResolveForDir for a directory a person named
// (`forge db --dir`): with several declared databases, a directory none of
// them names is an error (config.MigrationsDirDatabase), never the primary's
// hook.
func ResolveForNamedDir(projectDir, migDir string) (Hook, bool, error) {
	db, err := config.MigrationsDirDatabase(projectDir, migDir)
	if err != nil {
		return Hook{}, false, err
	}
	h, ok := Resolve(projectDir, db)
	return h, ok, nil
}

// Preset returns a built-in tool's default apply and status argv. Custom and
// forge have none. Every preset names its paths through the `${...}`
// variables, so the same argv runs against the shadow database and the env's.
//
// The database URL carries credentials, and an argv is readable by every user
// of the machine (/proc, ps). So a tool that reads its connection from the
// environment (goose: GOOSE_DRIVER/GOOSE_DBSTRING; dbmate: DATABASE_URL;
// flyway: FLYWAY_*; see Vars) is never handed it on the argv. golang-migrate
// and atlas take the database only as a flag, so theirs names it as
// ${FORGE_DATABASE_URL_NO_PASSWORD}, the URL with its password removed, and
// the password reaches them as PGPASSWORD, which their postgres drivers (lib/pq,
// pgx) read when the URL carries none.
func Preset(tool string) (apply, status []string) {
	switch tool {
	case config.MigrationToolGoose:
		base := []string{"goose", "-dir", "${FORGE_MIGRATIONS_DIR}"}
		return append(clone(base), "up"), append(clone(base), "status")
	case config.MigrationToolGolangMigrate:
		// -database is the CLI's only way to name the database: the URL
		// without its password, which travels as PGPASSWORD.
		base := []string{"migrate", "-path", "${FORGE_MIGRATIONS_DIR}", "-database", "${FORGE_DATABASE_URL_NO_PASSWORD}"}
		return append(clone(base), "up"), append(clone(base), "version")
	case config.MigrationToolDbmate:
		// migrate, not up: up also CREATEs the database, which forge has
		// already made (the shadow) or does not own (an env's). And no
		// schema dump: a shadow apply must write nothing into the project.
		base := []string{"dbmate", "--migrations-dir", "${FORGE_MIGRATIONS_DIR}", "--no-dump-schema"}
		return append(clone(base), "migrate"), append(clone(base), "status")
	case config.MigrationToolAtlas:
		// --url is the CLI's only way to name the database without an
		// atlas.hcl: the URL without its password, which travels as
		// PGPASSWORD.
		return []string{"atlas", "migrate", "apply", "--dir", "file://${FORGE_MIGRATIONS_DIR}", "--url", "${FORGE_DATABASE_URL_NO_PASSWORD}"},
			[]string{"atlas", "migrate", "status", "--dir", "file://${FORGE_MIGRATIONS_DIR}", "--url", "${FORGE_DATABASE_URL_NO_PASSWORD}"}
	case config.MigrationToolFlyway:
		// flyway reads FLYWAY_URL / _USER / _PASSWORD / _LOCATIONS from the
		// environment (toolEnv), so the argv names no connection.
		return []string{"flyway", "migrate"}, []string{"flyway", "info"}
	}
	return nil, nil
}

// NewMigrationHint is how the project's own tool creates a migration named
// name — what `forge db migration new` points at, since forge's numbering and
// file shape are not the tool's.
func NewMigrationHint(h Hook, name string) string {
	dir := h.RelDir
	switch h.Tool {
	case config.MigrationToolGoose:
		return fmt.Sprintf("goose -dir %s create %s sql", dir, name)
	case config.MigrationToolGolangMigrate:
		return fmt.Sprintf("migrate create -ext sql -dir %s -seq %s", dir, name)
	case config.MigrationToolDbmate:
		return fmt.Sprintf("dbmate --migrations-dir %s new %s", dir, name)
	case config.MigrationToolAtlas:
		return fmt.Sprintf("atlas migrate new %s --dir file://%s (or atlas migrate diff)", name, dir)
	case config.MigrationToolFlyway:
		return fmt.Sprintf("create %s/V<next>__%s.sql by hand (flyway has no generator)", dir, name)
	}
	return fmt.Sprintf("create the migration in %s with your own tool", dir)
}

// Invocation is one run of the hook.
type Invocation struct {
	// DSN is the postgres URL of the database to migrate — DATABASE_URL.
	DSN string
	// Phase is FORGE_PHASE (PhaseShadow, PhaseCLI).
	Phase string
	// Stdout and Stderr receive the tool's output; nil discards it.
	Stdout, Stderr io.Writer
}

// Apply runs the hook's apply argv against inv.DSN. A non-zero exit is an
// error naming the tool, the command and its exit status; the tool's own
// output went to inv.Stderr.
func Apply(ctx context.Context, h Hook, inv Invocation) error {
	return run(ctx, h, "apply", h.Apply, inv)
}

// Status runs the hook's status argv against inv.DSN. A hook without one is
// an error saying so.
func Status(ctx context.Context, h Hook, inv Invocation) error {
	if len(h.Status) == 0 {
		return fmt.Errorf("database.migrations has no status command for tool %s — add `status:` (the argv that reports what is applied)", h.Tool)
	}
	return run(ctx, h, "status", h.Status, inv)
}

// Check runs the hook's check argv against inv.DSN: nil means the tool says
// the database is fully migrated. A hook without one is an error saying so.
func Check(ctx context.Context, h Hook, inv Invocation) error {
	if len(h.Check) == 0 {
		return fmt.Errorf("database.migrations has no check command for tool %s — add `check:` (the argv that exits 0 only when every migration is applied and none failed)", h.Tool)
	}
	return run(ctx, h, "check", h.Check, inv)
}

func run(ctx context.Context, h Hook, what string, argv []string, inv Invocation) error {
	if len(argv) == 0 {
		return fmt.Errorf("database.migrations has no %s command for tool %s", what, h.Tool)
	}
	vars, err := Vars(h, inv)
	if err != nil {
		return err
	}
	args := Expand(argv, vars)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec // the project's declared migration command
	cmd.Dir = h.ProjectDir
	cmd.Env = mergeEnv(os.Environ(), vars)
	cmd.Stdout = orDiscard(inv.Stdout)
	cmd.Stderr = orDiscard(inv.Stderr)
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		switch {
		case errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("migration tool %s: %q not found on PATH — install it, or set database.migrations.%s in forge.yaml to the command to run", h.Tool, args[0], what)
		case errors.As(err, &ee):
			return fmt.Errorf("migration tool %s: `%s` (%s) exited %d", h.Tool, strings.Join(argv, " "), what, ee.ExitCode())
		}
		return fmt.Errorf("migration tool %s: %s: %w", h.Tool, what, err)
	}
	return nil
}

// Vars are the variables the hook runs with — the environment forge adds and
// the names `${...}` expands. DATABASE_URL and the FORGE_* variables are set
// for every tool; a built-in tool also gets the variables it reads natively
// (GOOSE_DRIVER/GOOSE_DBSTRING/GOOSE_MIGRATION_DIR, DBMATE_MIGRATIONS_DIR,
// FLYWAY_URL/_USER/_PASSWORD/_LOCATIONS), so a hand-written apply argv that
// names no connection still reaches the right database.
//
// FORGE_DATABASE_URL_NO_PASSWORD is DATABASE_URL with its password removed
// (userinfo and a `password` query parameter), and PGPASSWORD carries that
// password, for a tool that must be handed the URL as an argument: the argv
// then names the database without the credential (Preset).
func Vars(h Hook, inv Invocation) (map[string]string, error) {
	noPassword, password, named := splitDSNPassword(inv.DSN)
	vars := map[string]string{
		"DATABASE_URL":                   inv.DSN,
		"FORGE_DATABASE_URL_NO_PASSWORD": noPassword,
		"FORGE_MIGRATIONS_DIR":           h.Dir,
		"FORGE_PROJECT_DIR":              h.ProjectDir,
		"FORGE_PHASE":                    inv.Phase,
	}
	// Set whenever the URL names a password, even an empty one: the
	// stripped URL must authenticate exactly as the original did, never with
	// a PGPASSWORD the caller's environment happens to hold.
	if named {
		vars["PGPASSWORD"] = password
	}
	switch h.Tool {
	case config.MigrationToolGoose:
		vars["GOOSE_DRIVER"] = "postgres"
		vars["GOOSE_DBSTRING"] = inv.DSN
		vars["GOOSE_MIGRATION_DIR"] = h.Dir
	case config.MigrationToolDbmate:
		vars["DBMATE_MIGRATIONS_DIR"] = h.Dir
		vars["DBMATE_NO_DUMP_SCHEMA"] = "true"
	case config.MigrationToolFlyway:
		jdbc, user, pass, err := JDBC(inv.DSN)
		if err != nil {
			return nil, fmt.Errorf("migration tool flyway: %w", err)
		}
		vars["FLYWAY_URL"] = jdbc
		vars["FLYWAY_USER"] = user
		vars["FLYWAY_PASSWORD"] = pass
		vars["FLYWAY_LOCATIONS"] = "filesystem:" + h.Dir
	}
	return vars, nil
}

// splitDSNPassword returns dsn without its password (the userinfo password
// and a `password` query parameter), the password the driver would use, and
// whether the dsn names one at all. The precedence is libpq's: a `password`
// query parameter overrides the userinfo password, even when it is empty. A
// dsn that is not a URL, or names no password, is returned unchanged.
func splitDSNPassword(dsn string) (noPassword, password string, named bool) {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		return dsn, "", false
	}
	if u.User != nil {
		if pw, ok := u.User.Password(); ok {
			password, named = pw, true
			u.User = url.User(u.User.Username())
		}
	}
	if q := u.Query(); q.Has("password") {
		password, named = q.Get("password"), true
		q.Del("password")
		u.RawQuery = q.Encode()
	}
	if !named {
		return dsn, "", false
	}
	return u.String(), password, true
}

// JDBC converts a postgres:// URL into the JDBC URL, user and password flyway
// takes separately.
func JDBC(dsn string) (jdbcURL, user, password string, err error) {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		return "", "", "", fmt.Errorf("DATABASE_URL must be a postgres:// URL to derive flyway's JDBC URL")
	}
	if u.User != nil {
		user = u.User.Username()
		password, _ = u.User.Password()
	}
	jdbcURL = "jdbc:postgresql://" + u.Host + u.EscapedPath()
	if u.RawQuery != "" {
		jdbcURL += "?" + u.RawQuery
	}
	return jdbcURL, user, password, nil
}

var varRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Expand replaces each `${NAME}` in argv whose NAME is one of vars. Anything
// else — `$NAME`, an unknown `${NAME}` — is passed through verbatim: there is
// no shell, and a literal `$` in an argument (a password, a regex) must
// survive.
func Expand(argv []string, vars map[string]string) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = varRef.ReplaceAllStringFunc(a, func(m string) string {
			if v, ok := vars[varRef.FindStringSubmatch(m)[1]]; ok {
				return v
			}
			return m
		})
	}
	return out
}

// Digest names the hook's migration set for the shadow-schema cache: the
// tool, its apply argv, and every regular file under the migrations
// directory (path and bytes). It is deliberately broader than any one tool's
// file pattern — a goose .go migration, atlas.sum, a flyway R__ script all
// count — because a digest that misses a file the tool applies would serve a
// stale schema, while one that over-counts costs only a re-apply. n is the
// number of files; zero means there is nothing to apply.
func Digest(h Hook) (digest string, n int, err error) {
	var files []string
	err = filepath.WalkDir(h.Dir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			if p != h.Dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			files = append(files, p)
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	sort.Strings(files)
	sum := sha256.New()
	fmt.Fprintf(sum, "forge-migrationhook/v1\x00%s\x00%q\x00", h.Tool, h.Apply)
	for _, p := range files {
		b, err := os.ReadFile(p) //nolint:gosec // a file under the project's migrations dir
		if err != nil {
			return "", 0, err
		}
		rel, _ := filepath.Rel(h.Dir, p)
		fmt.Fprintf(sum, "%s\x00%d\x00", filepath.ToSlash(rel), len(b))
		sum.Write(b)
	}
	return hex.EncodeToString(sum.Sum(nil)), len(files), nil
}

// mergeEnv returns base with every key of vars set (replacing any value base
// had for it).
func mergeEnv(base []string, vars map[string]string) []string {
	out := make([]string, 0, len(base)+len(vars))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := vars[k]; !ok {
			out = append(out, kv)
		}
	}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+vars[k])
	}
	return out
}

func orDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

func clone(s []string) []string { return append([]string(nil), s...) }

// LedgerTable is a table a migration tool keeps its own history in. Schema
// is "" when the table is matched in any schema.
type LedgerTable struct {
	Schema string
	Name   string
}

// LedgerTables returns the tables h's tool records its applied migrations
// in: the tool's default, and the one its apply or status argv names instead
// (goose -table, dbmate --migrations-table, golang-migrate's
// x-migrations-table, atlas --revisions-schema, flyway -table). They are the
// tool's bookkeeping, not the app's schema: the ORM never projects them, and
// seeding never plans (or resets) them, which would erase or fabricate the
// tool's history.
func (h Hook) LedgerTables() []LedgerTable {
	argv := append(append([]string{}, h.Apply...), h.Status...)
	switch h.Tool {
	case config.MigrationToolGoose:
		return []LedgerTable{{Name: "goose_db_version"}, ledgerFlag(argv, "goose_db_version", "-table", "--table")}
	case config.MigrationToolDbmate:
		return []LedgerTable{{Name: "schema_migrations"}, ledgerFlag(argv, "schema_migrations", "--migrations-table")}
	case config.MigrationToolGolangMigrate:
		out := []LedgerTable{{Name: "schema_migrations"}}
		for _, a := range argv {
			if i := strings.Index(a, "x-migrations-table="); i >= 0 {
				v, _, _ := strings.Cut(a[i+len("x-migrations-table="):], "&")
				if v, err := url.QueryUnescape(v); err == nil && v != "" {
					out = append(out, parseIdent(v))
				}
			}
		}
		return out
	case config.MigrationToolAtlas:
		schema := ledgerFlag(argv, "atlas_schema_revisions", "--revisions-schema").Name
		return []LedgerTable{{Schema: "atlas_schema_revisions", Name: "atlas_schema_revisions"}, {Schema: schema, Name: "atlas_schema_revisions"}}
	case config.MigrationToolFlyway:
		return []LedgerTable{{Name: "flyway_schema_history"}, ledgerFlag(argv, "flyway_schema_history", "-table")}
	}
	return nil
}

// IsLedgerTable reports whether schema.name is one of h's LedgerTables.
func (h Hook) IsLedgerTable(schema, name string) bool {
	for _, l := range h.LedgerTables() {
		if l.Name == name && (l.Schema == "" || l.Schema == schema) {
			return true
		}
	}
	return false
}

// ledgerFlag is the table a flag in argv names (`-table x` or `-table=x`),
// else def. The value may be schema-qualified and quoted (parseIdent).
func ledgerFlag(argv []string, def string, flags ...string) LedgerTable {
	for i, a := range argv {
		for _, f := range flags {
			if a == f && i+1 < len(argv) {
				return parseIdent(argv[i+1])
			}
			if v, ok := strings.CutPrefix(a, f+"="); ok && v != "" {
				return parseIdent(v)
			}
		}
	}
	return LedgerTable{Name: def}
}

// parseIdent reads a postgres table identifier, `table` or `schema.table`,
// as postgres resolves it: a double-quoted part is taken verbatim ("" is a
// literal quote, and a '.' inside quotes is part of the name), an unquoted
// part is folded to lower case.
func parseIdent(s string) LedgerTable {
	var parts []string
	var cur strings.Builder
	quoted, inQuotes := false, false
	flush := func() {
		p := cur.String()
		if !quoted {
			p = strings.ToLower(p)
		}
		parts = append(parts, p)
		cur.Reset()
		quoted = false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuotes && c == '"' && i+1 < len(s) && s[i+1] == '"':
			cur.WriteByte('"')
			i++
		case c == '"':
			inQuotes, quoted = !inQuotes, true
		case c == '.' && !inQuotes:
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	if len(parts) >= 2 {
		return LedgerTable{Schema: parts[len(parts)-2], Name: parts[len(parts)-1]}
	}
	return LedgerTable{Name: parts[0]}
}
