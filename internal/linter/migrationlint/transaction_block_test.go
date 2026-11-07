package migrationlint

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// txBlockCase is one migration file and what the transaction-block rules must
// say about it. setup prepares a fresh database the way earlier migrations
// would have, so the PG-backed test can run body exactly as the migrator does.
type txBlockCase struct {
	name  string
	setup string
	body  string
	// want is every finding, as "rule@line".
	want []string
	// sqlstate is what postgres answers when body runs as ONE query, the way
	// golang-migrate sends a migration file: "" for success. Empty for a case
	// a bare database cannot run (a subscription, a tablespace).
	sqlstate string
	// noDB marks a case the PG-backed test skips.
	noDB bool
}

const usersTable = "CREATE TABLE users (id INT PRIMARY KEY, email TEXT, org_id INT);\n"

var txBlockCases = []txBlockCase{
	{
		// The P5a file: two CONCURRENTLY statements in one migration.
		name:     "two CREATE INDEX CONCURRENTLY",
		setup:    usersTable,
		body:     "-- indexes\nCREATE INDEX CONCURRENTLY idx_users_email ON users (email);\nCREATE UNIQUE INDEX CONCURRENTLY idx_users_org\n  ON users (org_id);\n",
		want:     []string{"non-transactional-statement@2", "non-transactional-statement@3"},
		sqlstate: "25001",
	},
	{
		name:     "CONCURRENTLY beside any other statement",
		setup:    usersTable,
		body:     "SET lock_timeout = '5s';\nCREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_email ON users (email);\n",
		want:     []string{"non-transactional-statement@2"},
		sqlstate: "25001",
	},
	{
		name:  "one CONCURRENTLY statement alone, under comments, is fine",
		setup: usersTable,
		body:  "-- forge:backward-compatible\n/* an index; built online */\nCREATE INDEX CONCURRENTLY idx_users_email ON users (email)\n  WHERE email <> 'a;b';\n-- done;\n",
	},
	{
		name:     "DROP INDEX CONCURRENTLY",
		setup:    usersTable + "CREATE INDEX idx_users_email ON users (email);\n",
		body:     "DROP INDEX CONCURRENTLY idx_users_email;\nALTER TABLE users ADD COLUMN name TEXT;\n",
		want:     []string{"non-transactional-statement@1"},
		sqlstate: "25001",
	},
	{
		name:     "REINDEX … CONCURRENTLY, keyword or option",
		setup:    usersTable + "CREATE INDEX idx_users_email ON users (email);\n",
		body:     "REINDEX INDEX CONCURRENTLY idx_users_email;\nREINDEX (CONCURRENTLY) TABLE users;\n",
		want:     []string{"non-transactional-statement@1", "non-transactional-statement@2"},
		sqlstate: "25001",
	},
	{
		name:  "a plain REINDEX runs in a transaction",
		setup: usersTable,
		body:  "REINDEX TABLE users;\nREINDEX (CONCURRENTLY false) TABLE users;\n",
	},
	{
		name:     "VACUUM after a backfill",
		setup:    usersTable,
		body:     "UPDATE users SET email = lower(email);\nVACUUM (ANALYZE) users;\n",
		want:     []string{"non-transactional-statement@2"},
		sqlstate: "25001",
	},
	{
		name:  "CONCURRENTLY in a comment or a string is not the statement",
		setup: usersTable,
		body:  "-- run CREATE INDEX CONCURRENTLY idx later\nCREATE INDEX idx_users_email ON users (email);\nCOMMENT ON INDEX idx_users_email IS 'rebuild with CREATE INDEX CONCURRENTLY; never VACUUM';\n",
	},
	{
		name: "DETACH PARTITION CONCURRENTLY",
		setup: "CREATE TABLE events (id INT, at DATE) PARTITION BY RANGE (at);\n" +
			"CREATE TABLE events_2026 PARTITION OF events FOR VALUES FROM ('2026-01-01') TO ('2027-01-01');\n",
		body:     "ALTER TABLE events DETACH PARTITION events_2026 CONCURRENTLY;\nDROP TABLE events_2026;\n",
		want:     []string{"non-transactional-statement@1"},
		sqlstate: "25001",
	},
	{
		name: "CLUSTER without a table, but not CLUSTER of one",
		body: "CLUSTER users USING idx_users_email;\nCLUSTER VERBOSE;\nCLUSTER \"Users\";\n",
		want: []string{"non-transactional-statement@2"},
		noDB: true,
	},
	{
		name: "database, tablespace and system statements",
		body: "CREATE DATABASE x;\nDROP DATABASE IF EXISTS x;\nCREATE TABLESPACE t LOCATION '/x';\nDROP TABLESPACE t;\n" +
			"ALTER DATABASE x SET TABLESPACE t;\nALTER DATABASE x SET search_path TO app;\nALTER SYSTEM SET work_mem = '64MB';\nDISCARD ALL;\n",
		want: []string{"non-transactional-statement@1", "non-transactional-statement@2", "non-transactional-statement@3",
			"non-transactional-statement@4", "non-transactional-statement@5", "non-transactional-statement@7", "non-transactional-statement@8"},
		noDB: true,
	},
	{
		name: "subscriptions that touch a replication slot",
		body: "CREATE SUBSCRIPTION s CONNECTION 'host=x' PUBLICATION p;\n" +
			"CREATE SUBSCRIPTION s2 CONNECTION 'host=x' PUBLICATION p WITH (connect = false);\n" +
			"ALTER SUBSCRIPTION s REFRESH PUBLICATION;\nALTER SUBSCRIPTION s SET PUBLICATION p WITH (refresh = false);\n" +
			"DROP SUBSCRIPTION s;\n",
		want: []string{"non-transactional-statement@1", "non-transactional-statement@3", "non-transactional-statement@5"},
		noDB: true,
	},
	{
		// The other reason a goose file declares NO TRANSACTION.
		name:     "an enum value used in the migration that adds it",
		setup:    "CREATE TYPE user_state AS ENUM ('active');\nCREATE TABLE users (id INT PRIMARY KEY, state user_state);\nINSERT INTO users VALUES (1, 'active');\n",
		body:     "ALTER TYPE user_state ADD VALUE IF NOT EXISTS 'suspended';\n\nUPDATE users SET state = 'suspended' WHERE id = 1;\n",
		want:     []string{"enum-value-used-before-commit@3"},
		sqlstate: "55P04",
	},
	{
		// postgres has allowed ADD VALUE inside a transaction since 12.
		name:  "ADD VALUE beside other statements, the value unused, is fine",
		setup: "CREATE TYPE public.user_state AS ENUM ('active');\n",
		body:  "ALTER TYPE public.user_state ADD VALUE 'suspended';\nALTER TYPE user_state ADD VALUE 'banned' AFTER 'suspended';\nCREATE TABLE audits (id INT, note TEXT DEFAULT 'active');\n",
	},
	{
		// The rules read statements, not the database: a subscription
		// created with slot_name = NONE runs in a transaction, so its author
		// says so in the file.
		name: "the in-file opt-out silences both rules",
		body: "-- forge:allow-in-transaction: s has no replication slot\nDROP SUBSCRIPTION s;\nALTER TYPE t ADD VALUE 'b';\nINSERT INTO x VALUES ('b');\n",
		noDB: true,
	},
	{
		name: "a value added to a type the same migration creates is safe",
		body: "CREATE TYPE user_state AS ENUM ('active');\nALTER TYPE user_state ADD VALUE 'suspended';\n" +
			"CREATE TABLE users (id INT PRIMARY KEY, state user_state DEFAULT 'suspended');\n",
	},
}

func TestLintTransactionBlock(t *testing.T) {
	for _, tc := range txBlockCases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, f := range lintTransactionBlock("m.up.sql", tc.body) {
				if f.Severity != SeverityError {
					t.Errorf("%s at line %d is a %s; a file that cannot apply is an error", f.Rule, f.Line, f.Severity)
				}
				got = append(got, fmt.Sprintf("%s@%d", f.Rule, f.Line))
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("findings = %v, want %v", got, tc.want)
			}
		})
	}
}

// Every case's verdict, checked against postgres itself: a file the rules
// refuse fails with the SQLSTATE the message names, and a file they accept
// applies. A rule that disagreed with postgres would be worse than none.
func TestLintTransactionBlockAgreesWithPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a real postgres server")
	}
	for _, tc := range txBlockCases {
		if tc.noDB {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			db, cleanup, err := pgtest.New()
			if err != nil {
				t.Skipf("no postgres available: %v", err)
			}
			t.Cleanup(cleanup)
			if tc.setup != "" {
				if _, err := db.Exec(tc.setup); err != nil {
					t.Fatalf("setup: %v", err)
				}
			}
			err = execAsOneQuery(db, tc.body)
			var pgErr interface{ SQLState() string }
			switch {
			case tc.sqlstate == "" && err != nil:
				t.Errorf("the rules accept this file, but postgres refuses it: %v", err)
			case tc.sqlstate != "" && !errors.As(err, &pgErr):
				t.Errorf("the rules refuse this file with SQLSTATE %s, but postgres answered %v", tc.sqlstate, err)
			case tc.sqlstate != "" && pgErr.SQLState() != tc.sqlstate:
				t.Errorf("postgres refused with SQLSTATE %s (%v), the rules name %s", pgErr.SQLState(), err, tc.sqlstate)
			}
		})
	}
}

// execAsOneQuery runs a migration file the way golang-migrate does: one Exec
// with no arguments, which the driver sends as one simple-protocol query.
func execAsOneQuery(db *sql.DB, body string) error {
	_, err := db.Exec(body)
	return err
}

// The statement splitter, on what makes splitting hard: a `;` that does not
// end a statement, and the line each statement starts on.
func TestLexStatements(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		lines []int
	}{
		{"plain", "SELECT 1;\nSELECT 2;", []int{1, 2}},
		{"strings and quoted identifiers", "SELECT 'a;b', E'it\\'s;', \"x;y\";\nSELECT 2;", []int{1, 2}},
		{"dollar-quoted bodies", "CREATE FUNCTION f() RETURNS int AS $body$ SELECT 1; $body$ LANGUAGE sql;\nDO $$ BEGIN PERFORM 1; END $$;", []int{1, 2}},
		{"a $1 parameter is not a dollar quote", "PREPARE p AS SELECT $1;\nSELECT 2;", []int{1, 2}},
		{"nested block comments", "/* a; /* b; */ c; */ SELECT 1;\n-- x;\nSELECT 2;", []int{1, 3}},
		{"BEGIN ATOMIC", "CREATE OR REPLACE FUNCTION f(x int) RETURNS int LANGUAGE sql\nBEGIN ATOMIC\n  SELECT CASE WHEN x > 0 THEN 1 ELSE 2 END;\n  SELECT 3;\nEND;\nSELECT 4;", []int{1, 6}},
		{"a trailing unterminated statement counts", "SELECT 1;\n\nSELECT 2", []int{1, 3}},
		{"empty statements do not", ";;\nSELECT 1;;", []int{2}},
		{"comments only", "-- nothing\n/* here */\n", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var lines []int
			for _, s := range lexStatements(tc.src) {
				lines = append(lines, s.Line)
			}
			if !slices.Equal(lines, tc.lines) {
				t.Errorf("statement lines = %v, want %v", lines, tc.lines)
			}
		})
	}
}

// The P5a regression through the lane `forge lint --migration-safety` runs: a
// migration with two CREATE INDEX CONCURRENTLY statements passed the lint and
// failed at `migrate up` with SQLSTATE 25001, leaving the schema dirty. It is
// now refused at review time, at file:line, with the fix.
func TestLintMigrationsDirRefusesConcurrentlyBesideAnotherStatement(t *testing.T) {
	dir := writeMigration(t, "20260101000000_index_users.up.sql",
		"-- forge db migration new index_users\n\nCREATE INDEX CONCURRENTLY idx_users_email ON users (email);\nCREATE INDEX CONCURRENTLY idx_users_org ON users (org_id);\n")
	result, err := LintMigrationsDir(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("LintMigrationsDir: %v", err)
	}
	if !result.HasErrors() {
		t.Fatalf("a migration that cannot apply passed the lint: %#v", result.Findings)
	}
	text := result.FormatText()
	for _, want := range []string{
		filepath.Join(dir, "20260101000000_index_users.up.sql") + ":3: error [non-transactional-statement] CREATE INDEX CONCURRENTLY cannot run inside a transaction block, and this migration holds 2 statements",
		"20260101000000_index_users.up.sql:4: error [non-transactional-statement]",
		"SQLSTATE 25001",
		"`forge db migration new <name>`",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("lint report lacks %q:\n%s", want, text)
		}
	}
	if got := PrimaryRemediation(result.Findings); got != NonTransactionalStatementRemediation {
		t.Errorf("the fix printed under the findings is %q", got)
	}
}

func TestTransactionBlockRemediations(t *testing.T) {
	for rule, want := range map[string]string{
		RuleNonTransactionalStatement: NonTransactionalStatementRemediation,
		RuleEnumValueUsedBeforeCommit: EnumValueUsedBeforeCommitRemediation,
	} {
		if got := RemediationFor(rule); got != want {
			t.Errorf("RemediationFor(%q) = %q, want the rule's own fix", rule, got)
		}
	}
}
