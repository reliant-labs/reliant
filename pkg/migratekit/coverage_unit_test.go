package migratekit

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/golang-migrate/migrate/v4/database"
	"github.com/jackc/pgx/v5/pgconn"
)

// failingFS serves its files but fails reading, or listing, the paths it is
// told to: the embedded set a broken build would hand a migrator.
type failingFS struct {
	fstest.MapFS
	failRead map[string]bool
	failGlob bool
}

func (f failingFS) Open(name string) (fs.File, error) {
	if f.failRead[name] {
		return nil, errors.New("disk on fire: " + name)
	}
	return f.MapFS.Open(name)
}

func (f failingFS) ReadFile(name string) ([]byte, error) {
	if f.failRead[name] {
		return nil, errors.New("disk on fire: " + name)
	}
	return f.MapFS.ReadFile(name)
}

func (f failingFS) Glob(pattern string) ([]string, error) {
	if f.failGlob {
		return nil, errors.New("glob refused")
	}
	return fs.Glob(f.MapFS, pattern)
}

func logOutput(t *testing.T, run func(*slog.Logger)) string {
	t.Helper()
	var buf bytes.Buffer
	run(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return buf.String()
}

func TestLogRunSaysWhatTheRunDid(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  Result
		err  error
		want []string
		not  []string
	}{
		{"nothing embedded", Result{NothingEmbedded: true}, nil, []string{"no migrations embedded, skipping auto-migrate"}, []string{"no pending"}},
		{"migrations applied", Result{Changed: true, Before: State{Version: 1, Applied: true}, After: State{Version: 3, Applied: true}}, nil,
			[]string{"migrations applied", "from_version=1", "to_version=3"}, nil},
		{"nothing pending", Result{After: State{Version: 4, Applied: true}}, nil, []string{"no pending migrations", "version=4"}, nil},
		{"schema ahead", Result{Ahead: &Ahead{Version: 9, Latest: 7, Versions: []uint{8, 9}}}, nil,
			[]string{"AHEAD of this binary", "schema_version=9", "binary_latest=7", `unknown_versions="8, 9"`, "level=WARN"}, []string{"no pending"}},
		{"adopted history", Result{Adopted: &Adopted{Tool: "goose", Name: "1_base.up.sql", Version: 1, Versions: 12}, After: State{Version: 1, Applied: true}}, nil,
			[]string{"ADOPTED a database", "tool=goose", "baseline=1_base.up.sql", "verified_versions=12", "level=WARN"}, nil},
		{"a failed run logs no outcome line", Result{Changed: true}, errors.New("boom"), nil, []string{"migrations applied", "no pending", "AHEAD"}},
		{"role not provisioned", Result{Grants: &Grants{RoleMissing: true}}, nil, []string{"read-only role not provisioned"}, []string{"grants applied"}},
		{"role created and granted", Result{Grants: &Grants{Applied: true, Created: true}}, nil, []string{"read-only role created (NOLOGIN) and its grants applied"}, nil},
		{"grants applied", Result{Grants: &Grants{Applied: true}}, nil, []string{"read-only grants applied"}, []string{"role created"}},
		{"grants of a failed run are not reported", Result{Grants: &Grants{Applied: true}}, errors.New("boom"), nil, []string{"grants applied"}},
		{"dashboard role created", Result{Dashboard: &DashboardRole{Created: true}}, nil, []string{"dashboard role created (NOLOGIN)"}, nil},
		{"dashboard role missing", Result{Dashboard: &DashboardRole{Missing: true}}, nil, []string{"dashboard role not provisioned"}, nil},
		{"dashboard role untouched", Result{Dashboard: &DashboardRole{}}, nil, []string{"no pending migrations"}, []string{"dashboard role"}},
		{"security definer functions are named once, sorted", Result{
			Grants:    &Grants{Applied: true, SecurityDefiner: []string{"public.zeta", "public.alpha"}},
			Dashboard: &DashboardRole{SecurityDefiner: []string{"public.alpha", "public.mid"}},
		}, nil, []string{"SECURITY DEFINER functions", `functions="public.alpha, public.mid, public.zeta"`}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := logOutput(t, func(l *slog.Logger) { logRun(l, tc.res, tc.err) })
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("log lacks %q:\n%s", w, out)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(out, n) {
					t.Errorf("log must not say %q:\n%s", n, out)
				}
			}
		})
	}
	// A nil logger is allowed and says nothing.
	logRun(nil, Result{Changed: true, Grants: &Grants{Applied: true}}, nil)
}

func TestUniqueSortedAndSecurityDefiner(t *testing.T) {
	if got := uniqueSorted(nil); got != nil {
		t.Errorf("uniqueSorted(nil) = %v", got)
	}
	if got := uniqueSorted([]string{"b", "a", "b", "c", "a"}); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("uniqueSorted = %v", got)
	}
	if got := securityDefiner(Result{}); got != nil {
		t.Errorf("a run that did not get that far lists none: %v", got)
	}
	if got := securityDefiner(Result{Dashboard: &DashboardRole{SecurityDefiner: []string{"f"}}}); !slices.Equal(got, []string{"f"}) {
		t.Errorf("securityDefiner = %v", got)
	}
}

func TestSchemaPendingErrorNamesWhatTheWaiterSaw(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state State
		want  string
	}{
		{"nothing applied", State{}, "no migration applied"},
		{"behind", State{Applied: true, Version: 2}, "schema version 2)"},
		{"dirty", State{Applied: true, Version: 2, Dirty: true}, "schema version 2 (dirty: a migration is running, or failed partway)"},
	} {
		msg := (&SchemaPendingError{Want: 5, State: tc.state, Waited: 90*time.Second + 400*time.Millisecond}).Error()
		for _, w := range []string{tc.want, "timed out after 1m30s", "migration 5", "migrate=apply"} {
			if !strings.Contains(msg, w) {
				t.Errorf("%s: message lacks %q:\n%s", tc.name, w, msg)
			}
		}
	}
}

func TestEmbeddedLatest(t *testing.T) {
	files := fstest.MapFS{
		"migrations/1_a.up.sql": {Data: []byte("SELECT 1;")},
		"migrations/7_b.up.sql": {Data: []byte("SELECT 2;")},
		"migrations/README.md":  {Data: []byte("x")},
	}
	if got, err := embeddedLatest(files, "migrations"); err != nil || got != 7 {
		t.Errorf("latest = %d, %v; want 7", got, err)
	}
	root := fstest.MapFS{"3_x.up.sql": {Data: []byte("SELECT 1;")}}
	if got, err := embeddedLatest(root, ""); err != nil || got != 3 {
		t.Errorf("an empty dir means the FS root: %d, %v", got, err)
	}
	if got, err := embeddedLatest(nil, "migrations"); err != nil || got != 0 {
		t.Errorf("no FS embeds nothing: %d, %v", got, err)
	}
	if got, err := embeddedLatest(fstest.MapFS{"migrations/.gitkeep": {}}, "migrations"); err != nil || got != 0 {
		t.Errorf("a directory with no SQL embeds nothing: %d, %v", got, err)
	}
	if _, err := embeddedLatest(files, "elsewhere"); err == nil || !strings.Contains(err.Error(), "elsewhere") {
		t.Errorf("a missing directory is a packaging bug naming it: %v", err)
	}
	bad := fstest.MapFS{"m/99999999999999999999999_x.up.sql": {Data: []byte("SELECT 1;")}}
	if _, err := embeddedLatest(bad, "m"); err == nil {
		t.Error("an unreadable version must fail, not read as zero")
	}
}

func TestAwaitMigrationsOverNothingNeedsNoDatabase(t *testing.T) {
	wait := AwaitMigrations(nil, "migrations", AwaitOptions{})
	if err := wait(context.Background(), nil, nil); err != nil {
		t.Errorf("a binary embedding no migrations has nothing to wait for: %v", err)
	}
	wait = AwaitMigrations(fstest.MapFS{"migrations/1_a.up.sql": {Data: []byte("SELECT 1;")}}, "elsewhere", AwaitOptions{})
	if err := wait(context.Background(), nil, nil); err == nil || !strings.Contains(err.Error(), "elsewhere") {
		t.Errorf("a missing directory is reported: %v", err)
	}
}

func TestLockTimeoutErrorOfAPartlyAppliedMigrationSaysToInspectIt(t *testing.T) {
	msg := (&LockTimeoutError{Version: 7, Name: "7_x.up.sql", Attempts: 1, Timeout: 5 * time.Second, PartlyApplied: true,
		Err: &pgconn.PgError{Code: "55P03", Message: "lock timeout"}}).Error()
	for _, w := range []string{"was not retried", "dirty at version 7", "db migrate force"} {
		if !strings.Contains(msg, w) {
			t.Errorf("message lacks %q:\n%s", w, msg)
		}
	}
	if strings.Contains(msg, "Nothing was applied") {
		t.Errorf("a partly applied migration must not claim nothing was applied:\n%s", msg)
	}
	grants := (&LockTimeoutError{Name: "read-only grants", Grants: true, Attempts: 3, Timeout: time.Second, Err: errors.New("lock")}).Error()
	if !strings.Contains(grants, "read-only grants could not get their lock in 3 attempt(s)") || !strings.Contains(grants, "running again applies them") {
		t.Errorf("grants message = %s", grants)
	}
}

func TestIsStatementTimeoutReadsThroughGolangMigrateErrors(t *testing.T) {
	pg := &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}
	for name, err := range map[string]error{
		"bare":             pg,
		"wrapped":          errors.Join(errors.New("ctx"), pg),
		"database.Error":   database.Error{OrigErr: pg},
		"*database.Error":  &database.Error{OrigErr: pg},
		"wrapped db error": errors.Join(errors.New("ctx"), database.Error{OrigErr: pg}),
	} {
		if !isStatementTimeout(err) {
			t.Errorf("%s: want a statement timeout", name)
		}
	}
	for name, err := range map[string]error{
		"nil":             nil,
		"lock timeout":    &pgconn.PgError{Code: "55P03"},
		"plain":           errors.New("boom"),
		"other db error":  database.Error{OrigErr: &pgconn.PgError{Code: "23505"}},
		"*other db error": &database.Error{OrigErr: errors.New("x")},
	} {
		if isStatementTimeout(err) {
			t.Errorf("%s: not a statement timeout", name)
		}
	}
	if !isLockTimeout(&database.Error{OrigErr: &pgconn.PgError{Code: "55P03"}}) {
		t.Error("a lock timeout behind *database.Error is found")
	}
}

func TestLockGuardFindsItsFileByVersion(t *testing.T) {
	src, err := scanSource(fstest.MapFS{"m/1_a.up.sql": {Data: []byte("SELECT 1;")}, "m/2_b.up.sql": {Data: []byte("SELECT 2;")}}, "m")
	if err != nil {
		t.Fatal(err)
	}
	g := newLockGuard(nil, nil, src, RunOptions{}, true)
	if f, ok := g.file(2); !ok || f.Name != "2_b.up.sql" {
		t.Errorf("file(2) = %+v, %v", f, ok)
	}
	if _, ok := g.file(9); ok {
		t.Error("an unknown version has no file")
	}
	if g.log == nil {
		t.Error("a guard with no logger uses the default one")
	}
	if src.latest() != 2 || sourceSet(nil).latest() != 0 {
		t.Errorf("latest = %d / %d", src.latest(), sourceSet(nil).latest())
	}
}

func TestSettledStateOfADirtyVersionZero(t *testing.T) {
	for _, tc := range []struct{ in, want State }{
		{State{Version: 4, Applied: true}, State{Version: 4, Applied: true}},
		{State{Version: 4, Applied: true, Dirty: true}, State{Version: 3, Applied: true}},
		{State{Version: 0, Applied: true, Dirty: true}, State{}},
	} {
		if got := tc.in.settled(); got != tc.want {
			t.Errorf("%+v.settled() = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestSQLDSNAndDriverConfigOnUnusableDSNs(t *testing.T) {
	const unparseable = "postgres://u:p@host:notaport/db"
	if got := sqlDSN(unparseable); got != unparseable {
		t.Errorf("a DSN url cannot parse is left for the driver to report: %q", got)
	}
	cfg, err := driverConfig(unparseable)
	if err != nil || cfg == nil || cfg.MultiStatementMaxSize == 0 {
		t.Errorf("driverConfig of an unparseable DSN = %+v, %v; want the defaults (sql.Open reports the DSN)", cfg, err)
	}
	for _, tc := range []struct{ dsn, want string }{
		{"postgres://h/db?x-migrations-table-quoted=maybe", "x-migrations-table-quoted"},
		{"postgres://h/db?x-migrations-table=nope&x-migrations-table-quoted=true", "must be quoted"},
		{"postgres://h/db?x-statement-timeout=soon", "x-statement-timeout"},
		{"postgres://h/db?x-multi-statement=perhaps", "x-multi-statement"},
		{"postgres://h/db?x-multi-statement-max-size=big", "x-multi-statement-max-size"},
	} {
		if _, err := driverConfig(tc.dsn); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("driverConfig(%q) = %v, want an error naming %q", tc.dsn, err, tc.want)
		}
	}
	cfg, err = driverConfig(`postgres://h/db?x-migrations-table="m"."t"&x-migrations-table-quoted=true&x-statement-timeout=1500&x-multi-statement=true&x-multi-statement-max-size=0`)
	if err != nil || !cfg.MigrationsTableQuoted || cfg.StatementTimeout != 1500*time.Millisecond || !cfg.MultiStatementEnabled {
		t.Errorf("driverConfig = %+v, %v", cfg, err)
	}
	if cfg.MultiStatementMaxSize <= 0 {
		t.Error("a max size of 0 keeps the default")
	}
}

func TestOpenRefusesWhatItCannotBuildBeforeConnecting(t *testing.T) {
	ok := fstest.MapFS{"migrations/1_a.up.sql": {Data: []byte("SELECT 1;")}}
	const dsn = "postgres://u@127.0.0.1:1/db?connect_timeout=1"
	for _, tc := range []struct {
		name string
		opts Options
		want string
	}{
		{"no FS", Options{DSN: dsn}, "embeds no migrations"},
		{"no DSN", Options{FS: ok, Dir: "migrations"}, "DSN is required"},
		{"a bad driver option", Options{FS: ok, Dir: "migrations", DSN: "postgres://h/db?x-statement-timeout=soon"}, "open migrator"},
		{"a missing directory", Options{FS: ok, Dir: "elsewhere", DSN: dsn}, "open embedded migrations (elsewhere)"},
		{"an unreadable migration set", Options{FS: fstest.MapFS{"m/99999999999999999999999_x.up.sql": {Data: []byte("SELECT 1;")}}, Dir: "m", DSN: dsn}, "open embedded migrations (m)"},
		{"grants rows with no place in the script", Options{FS: fstest.MapFS{
			"migrations/1_a.up.sql":     {Data: []byte("SELECT 1;")},
			"grants/readonly_gen.sql":   {Data: []byte("-- no placeholder\n")},
			"grants/readonly/a_gen.sql": {Data: []byte("(row)")},
		}, Dir: "migrations", DSN: dsn}, "run `forge generate`"},
		{"a database that cannot be reached", Options{FS: ok, Dir: "migrations", DSN: dsn}, "open migrator"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mg, err := Open(tc.opts)
			if err == nil {
				_ = mg.Close()
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
	if mg, err := Open(Options{FS: ok, DSN: dsn}); err == nil {
		_ = mg.Close()
		t.Error("an empty Dir means the FS root, which holds no migrations here: the connection fails instead")
	}
}

func TestApplyWithRefusesWhatItCannotReadAndSkipsWhatIsAbsent(t *testing.T) {
	ctx := context.Background()
	if res, err := ApplyWith(ctx, nil, nil, "migrations", RunOptions{}); err != nil || !res.NothingEmbedded {
		t.Errorf("no FS: %+v, %v", res, err)
	}
	// An empty dir is the FS root; a root with no SQL embeds nothing.
	if res, err := ApplyWith(ctx, nil, fstest.MapFS{"README.md": {Data: []byte("x")}}, "", RunOptions{}); err != nil || !res.NothingEmbedded {
		t.Errorf("an FS root with no SQL: %+v, %v", res, err)
	}
	if _, err := ApplyWith(ctx, nil, fstest.MapFS{"migrations/1_a.up.sql": {Data: []byte("SELECT 1;")}}, "elsewhere", RunOptions{}); err == nil || !strings.Contains(err.Error(), "elsewhere") {
		t.Errorf("a missing directory: %v", err)
	}
	if _, err := ApplyWith(ctx, nil, fstest.MapFS{"m/99999999999999999999999_x.up.sql": {Data: []byte("SELECT 1;")}}, "m", RunOptions{}); err == nil {
		t.Error("an unreadable migration set must fail")
	}
	broken := fstest.MapFS{
		"migrations/1_a.up.sql":     {Data: []byte("SELECT 1;")},
		"grants/readonly_gen.sql":   {Data: []byte("-- no placeholder\n")},
		"grants/readonly/a_gen.sql": {Data: []byte("(row)")},
	}
	if _, err := ApplyWith(ctx, nil, broken, "migrations", RunOptions{}); err == nil || !strings.Contains(err.Error(), "forge generate") {
		t.Errorf("a grants script that cannot take its rows: %v", err)
	}
}

func TestGrantsScriptReadFailures(t *testing.T) {
	base := func() failingFS {
		return failingFS{MapFS: fstest.MapFS{
			"migrations/1_a.up.sql":     {Data: []byte("SELECT 1;")},
			"grants/readonly_gen.sql":   {Data: []byte("GRANT 1;\n    -- forge:rows\n")},
			"grants/readonly/a_gen.sql": {Data: []byte("(a)")},
		}}
	}
	if got, err := grantsScript(nil, "migrations"); got != "" || err != nil {
		t.Errorf("no FS: %q, %v", got, err)
	}
	if got, err := grantsScript(fstest.MapFS{"migrations/1_a.up.sql": {}}, "migrations"); got != "" || err != nil {
		t.Errorf("a project with no grants file embeds none: %q, %v", got, err)
	}
	f := base()
	f.failRead = map[string]bool{"grants/readonly_gen.sql": true}
	if _, err := grantsScript(f, "migrations"); err == nil || !strings.Contains(err.Error(), "read embedded grants") {
		t.Errorf("an unreadable script: %v", err)
	}
	f = base()
	f.failGlob = true
	if _, err := grantsScript(f, "migrations"); err == nil || !strings.Contains(err.Error(), "list embedded grants rows") {
		t.Errorf("rows that cannot be listed: %v", err)
	}
	f = base()
	f.failRead = map[string]bool{"grants/readonly/a_gen.sql": true}
	if _, err := grantsScript(f, "migrations"); err == nil || !strings.Contains(err.Error(), "read embedded grants row") {
		t.Errorf("an unreadable row: %v", err)
	}
}

func TestScanDashboardRefsReadFailure(t *testing.T) {
	files := fstest.MapFS{"migrations/1_a.up.sql": {Data: []byte("SELECT 1;")}}
	src, err := scanSource(files, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	broken := failingFS{MapFS: files, failRead: map[string]bool{"migrations/1_a.up.sql": true}}
	if _, err := scanDashboardRefs(broken, "migrations", src, "forge_dash"); err == nil || !strings.Contains(err.Error(), "read embedded migration 1_a.up.sql") {
		t.Errorf("err = %v", err)
	}
}

func TestSplitStatementsReadsTheLexicalFormsPostgresDoes(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want int
	}{
		{"a semicolon inside an E-string with an escaped quote", `SELECT E'a\';b'; SELECT 2`, 2},
		{"a plain string does not honour a backslash", `SELECT 'a\'; SELECT 2`, 2},
		{"a doubled quote stays inside the string", `SELECT 'it''s; ok'; SELECT 2`, 2},
		{"a dollar-quoted body hides its semicolons", `CREATE FUNCTION f() RETURNS int AS $$ SELECT 1; SELECT 2; $$ LANGUAGE sql; SELECT 3`, 2},
		{"a tagged dollar quote", `DO $fn$ BEGIN PERFORM 1; END $fn$; SELECT 2`, 2},
		{"a dollar quote that never closes runs to the end", `DO $fn$ BEGIN PERFORM 1; SELECT 2`, 1},
		{"a positional parameter is not a dollar quote", `SELECT $1; SELECT $2`, 2},
		{"a lone dollar is punctuation", `SELECT $; SELECT 2`, 2},
		{"a quoted identifier may hold a semicolon", `SELECT "a;b"; SELECT 2`, 2},
		{"an unterminated quoted identifier runs to the end", `SELECT "a; SELECT 2`, 1},
		{"a block comment is skipped, nested", `SELECT 1 /* a; /* b; */ c; */; SELECT 2`, 2},
		{"a line comment is skipped", "SELECT 1 -- a; b\n; SELECT 2", 2},
		{"a line comment at the end of input", `SELECT 1; -- done`, 1},
		{"empty statements are dropped", `;;; SELECT 1;; ;`, 1},
		{"an unterminated block comment swallows the rest", `SELECT 1; /* never closed; SELECT 2`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(splitStatements(tc.src)); got != tc.want {
				t.Errorf("splitStatements(%q) = %d statements, want %d", tc.src, got, tc.want)
			}
		})
	}
}

func TestQuotedAndDollarQuoteEnds(t *testing.T) {
	if got := quotedEnd(`'a\'b' x`, 0, '\'', true); got != 6 {
		t.Errorf("a backslash escapes the quote in an E-string: end = %d, want 6", got)
	}
	if got := quotedEnd(`'a\'b' x`, 0, '\'', false); got != 4 {
		t.Errorf("a backslash is plain in an ordinary string: end = %d, want 4", got)
	}
	if got := quotedEnd(`'ab`, 0, '\'', false); got != 3 {
		t.Errorf("an unterminated string ends at the input: %d", got)
	}
	if end, ok := dollarQuoteEnd("a$$x$$", 1); ok {
		t.Errorf("a $ that continues an identifier opens nothing, got end %d", end)
	}
	if _, ok := dollarQuoteEnd("$1", 0); ok {
		t.Error("$1 is a parameter")
	}
	if _, ok := dollarQuoteEnd("$", 0); ok {
		t.Error("a trailing $ opens nothing")
	}
	if end, ok := dollarQuoteEnd("$t$ body $t$ tail", 0); !ok || end != len("$t$ body $t$") {
		t.Errorf("end = %d, %v", end, ok)
	}
	if end, ok := dollarQuoteEnd("$t$ never closed", 0); !ok || end != len("$t$ never closed") {
		t.Errorf("an unclosed body runs to the end: %d, %v", end, ok)
	}
}

func TestSessionSettingsRestoreWithoutOverrideIsANoOp(t *testing.T) {
	var s sessionSettings
	s.restore(nil) // nothing was saved: must not touch the (nil) connection
	if s.saved {
		t.Error("restore must leave an unsaved session unsaved")
	}
}
