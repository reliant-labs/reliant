package migratekit

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/golang-migrate/migrate/v4/database"

	"github.com/reliant-labs/forge/pkg/migratekit/compat"
)

func TestParseLimits(t *testing.T) {
	l, err := ParseLimits("", "", 0)
	if err != nil || l != (Limits{}) {
		t.Fatalf("empty flags: %+v, %v, want the zero Limits", l, err)
	}
	l, err = ParseLimits("30s", "2h", 3)
	if err != nil || l.LockTimeout != 30*time.Second || l.StatementTimeout != 2*time.Hour || l.LockAttempts != 3 {
		t.Fatalf("got %+v, %v", l, err)
	}
	l, err = ParseLimits("off", "0", 1)
	if err != nil || l.LockTimeout != Off || l.StatementTimeout != Off {
		t.Fatalf("off: %+v, %v", l, err)
	}
	for _, c := range []struct {
		lock, stm string
		attempts  int
		want      string
	}{
		{"soon", "", 0, "lock timeout"},
		{"", "10", 0, "statement timeout"},
		{"", "", -1, "negative"},
	} {
		if _, err := ParseLimits(c.lock, c.stm, c.attempts); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("ParseLimits(%q, %q, %d) = %v, want an error containing %q", c.lock, c.stm, c.attempts, err, c.want)
		}
	}
}

func TestLimitsDefaultsAndFileOverrides(t *testing.T) {
	r := Limits{}.resolved()
	if r.LockTimeout != DefaultLockTimeout || r.StatementTimeout != DefaultStatementTimeout ||
		r.LockAttempts != DefaultLockAttempts || r.RetryBackoff != DefaultRetryBackoff {
		t.Fatalf("zero Limits resolved to %+v, want the defaults", r)
	}
	if got := (Limits{LockTimeout: Off}).resolved().LockTimeout; got != Off {
		t.Errorf("Off resolved to %v", got)
	}

	sec := func(n int) *time.Duration { d := time.Duration(n) * time.Second; return &d }
	off := new(time.Duration)
	for _, c := range []struct {
		name     string
		file     compat.Timeouts
		lock, st time.Duration
	}{
		{"no directive keeps the run's", compat.Timeouts{}, 5 * time.Second, 30 * time.Minute},
		{"lock directive", compat.Timeouts{Lock: sec(60)}, time.Minute, 30 * time.Minute},
		{"statement directive", compat.Timeouts{Statement: sec(7200)}, 5 * time.Second, 2 * time.Hour},
		{"off disables", compat.Timeouts{Lock: off, Statement: off}, Off, Off},
	} {
		lock, st := r.forFile(c.file)
		if lock != c.lock || st != c.st {
			t.Errorf("%s: lock=%v statement=%v, want %v and %v", c.name, lock, st, c.lock, c.st)
		}
	}
}

func TestTimeoutSetting(t *testing.T) {
	for d, want := range map[time.Duration]string{Off: "0", 0: "0", 5 * time.Second: "5000ms", 1500 * time.Microsecond: "1ms", 30 * time.Minute: "1800000ms"} {
		if got := timeoutSetting(d); got != want {
			t.Errorf("timeoutSetting(%v) = %q, want %q", d, got, want)
		}
	}
}

// golang-migrate's database.Error has no Unwrap, so a lock timeout is found
// through its OrigErr; only SQLSTATE 55P03 counts.
func TestIsLockTimeout(t *testing.T) {
	lock := &pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout"}
	for name, c := range map[string]struct {
		err  error
		want bool
	}{
		"bare":                   {lock, true},
		"wrapped by migrate":     {database.Error{OrigErr: lock, Err: "migration failed"}, true},
		"pointer wrapped":        {&database.Error{OrigErr: lock}, true},
		"joined":                 {errors.Join(errors.New("x"), database.Error{OrigErr: lock}), true},
		"statement timeout":      {database.Error{OrigErr: &pgconn.PgError{Code: "57014"}}, false},
		"deadlock":               {&pgconn.PgError{Code: "40P01"}, false},
		"not postgres":           {errors.New("lock timeout"), false},
		"database error no orig": {database.Error{Err: "x"}, false},
		"nil":                    {nil, false},
	} {
		if got := isLockTimeout(c.err); got != c.want {
			t.Errorf("%s: isLockTimeout = %v, want %v", name, got, c.want)
		}
	}
}

func TestLockTimeoutErrorSaysWhatToDo(t *testing.T) {
	err := &LockTimeoutError{Version: 7, Name: "7_x.up.sql", Attempts: 5, Timeout: 5 * time.Second,
		Leftover: []string{"public.ix"}, Err: &pgconn.PgError{Code: "55P03", Message: "lock timeout"}}
	msg := err.Error()
	for _, want := range []string{"7_x.up.sql", "5 attempt", "5s", "schema is unchanged", "public.ix", compat.LockTimeoutDirective, "pg_stat_activity"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "55P03" {
		t.Error("the postgres error is not reachable through Unwrap")
	}
}

func TestPlanFileProvesAtomicity(t *testing.T) {
	for name, c := range map[string]struct {
		sql       string
		retryable bool
	}{
		"plain ddl":                {"ALTER TABLE t ADD COLUMN c INT;", true},
		"no terminator":            {"ALTER TABLE t ADD COLUMN c INT", true},
		"commit on its own line":   {"UPDATE t SET a=1;\nCOMMIT;\nALTER TABLE t ADD c INT;", false},
		"same-line begin..commit":  {"BEGIN; INSERT INTO audit VALUES (1); COMMIT; ALTER TABLE t ADD c INT;", false},
		"comment before commit":    {"UPDATE t SET a=1; /* done */ COMMIT; ALTER TABLE t ADD c INT;", false},
		"line comment then commit": {"UPDATE t SET a=1;\n-- finish\nCOMMIT;", false},
		"end work":                 {"UPDATE t SET a=1; END WORK;", false},
		"start transaction":        {"START TRANSACTION; SELECT 1;", false},
		"rollback":                 {"SAVEPOINT s; ROLLBACK TO s;", false},
		"prepare transaction":      {"PREPARE TRANSACTION 'x';", false},
		"commit in a string":       {"INSERT INTO t VALUES ('x; COMMIT; y');", true},
		"commit in a comment":      {"-- COMMIT;\n/* COMMIT; */ ALTER TABLE t ADD c INT;", true},
		"commit in a function":     {"CREATE FUNCTION f() RETURNS void AS $$ BEGIN PERFORM 1; END; $$ LANGUAGE plpgsql;", true},
		"commit in a tagged body":  {"CREATE FUNCTION f() RETURNS void AS $f$ COMMIT; $f$ LANGUAGE plpgsql;", true},
		"commit as an identifier":  {"ALTER TABLE t RENAME COLUMN \"commit\" TO x; ALTER TABLE t ADD commit_at INT;", true},
		"call":                     {"CALL load_audit();", false},
		"do block":                 {"DO $$ BEGIN INSERT INTO t VALUES (1); END $$;", false},
		"do after ddl":             {"ALTER TABLE t ADD c INT; DO $$ BEGIN NULL; END $$;", false},
		"call as an identifier":    {"ALTER TABLE t ADD COLUMN call INT; ALTER TABLE t ADD COLUMN \"do\" INT;", true},
		"call in a string":         {"INSERT INTO t VALUES ('CALL p(); DO x');", true},
		"create index":             {"CREATE INDEX CONCURRENTLY ix ON t (a);", true},
		"drop index":               {"DROP INDEX CONCURRENTLY IF EXISTS ix;", false},
		"plain drop index":         {"DROP INDEX IF EXISTS ix;", true},
		"empty":                    {"-- nothing\n", false},
		"vacuum":                   {"VACUUM items;", false},
		"vacuum analyze":           {"VACUUM (ANALYZE) items;", false},
		"reindex":                  {"REINDEX TABLE items;", false},
		"cluster":                  {"CLUSTER items USING ix;", false},
		"add enum value":           {"ALTER TYPE mood ADD VALUE 'ok';", false},
		"add enum value if":        {"ALTER TYPE mood ADD VALUE IF NOT EXISTS 'ok' BEFORE 'sad';", false},
		"rename enum value":        {"ALTER TYPE mood RENAME VALUE 'a' TO 'b';", true},
		"alter system":             {"ALTER SYSTEM SET work_mem = '64MB';", false},
		"create database":          {"CREATE DATABASE x;", false},
		"drop database":            {"DROP DATABASE x;", false},
		"create tablespace":        {"CREATE TABLESPACE t LOCATION '/x';", false},
		"refresh concurrently":     {"REFRESH MATERIALIZED VIEW CONCURRENTLY mv;", false},
		"refresh":                  {"REFRESH MATERIALIZED VIEW mv;", true},
		"copy":                     {"COPY t FROM '/x';", false},
		"unknown statement":        {"FROBNICATE t;", false},
		"set then ddl":             {"SET search_path TO schema_a; ALTER TABLE items ADD c INT;", true},
		"concurrent among others":  {"SET search_path TO a; CREATE INDEX CONCURRENTLY ix ON items (v);", false},
		"reindex concurrently":     {"REINDEX INDEX CONCURRENTLY ix;", false},
		"detach concurrently":      {"ALTER TABLE p DETACH PARTITION c CONCURRENTLY;", false},
		"unnamed concurrent index": {"CREATE INDEX CONCURRENTLY ON t (a);", false},
	} {
		if got := planFile([]byte(c.sql)).retryable(); got != c.retryable {
			t.Errorf("%s: retryable = %v, want %v", name, got, c.retryable)
		}
	}
}

func TestPlanFileReadsTheIndexTarget(t *testing.T) {
	for name, c := range map[string]struct {
		sql          string
		index, table string
	}{
		"plain":                   {"CREATE INDEX CONCURRENTLY items_v ON items (v);", "items_v", `"items"`},
		"unique, if not":          {"CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS items_v ON ONLY items (v)", "items_v", `"items"`},
		"qualified table":         {"CREATE INDEX CONCURRENTLY items_v ON schema_a.items (v);", "items_v", `"schema_a"."items"`},
		"quoted":                  {`CREATE INDEX CONCURRENTLY "Items V" ON "Schema A"."Items" (v);`, "Items V", `"Schema A"."Items"`},
		"name in a string, alone": {"/* CREATE INDEX CONCURRENTLY x ON y */ CREATE INDEX CONCURRENTLY real ON items (v) WHERE v <> 'CREATE INDEX CONCURRENTLY z ON w';", "real", `"items"`},
	} {
		plan := planFile([]byte(c.sql))
		if len(plan.creates) != 1 || plan.creates[0].index != c.index || plan.creates[0].table != c.table {
			t.Errorf("%s: creates = %+v, want %s on %s", name, plan.creates, c.index, c.table)
		}
	}
	if p := planFile([]byte("-- CREATE INDEX CONCURRENTLY x ON y (z);\nSELECT 1;")); len(p.creates) != 0 {
		t.Errorf("a comment was read as a statement: %+v", p.creates)
	}
}
