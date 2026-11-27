package migrationlint

import (
	"testing"
)

// lintSequence lints files the way LintMigrationsDir does: per file, then the
// cross-file CHECK pass. It returns the set-not-null-without-backfill
// findings left, as "file:line".
func lintSequence(t *testing.T, files ...compatFile) []string {
	t.Helper()
	var findings []Finding
	for _, f := range files {
		findings = append(findings, lintMigrationFile(f.path, f.content, DefaultConfig())...)
	}
	var left []string
	for _, f := range dropCheckProvenSetNotNull(files, findings) {
		if f.Rule == RuleSetNotNullWithoutBackfill {
			left = append(left, f.File)
		}
	}
	return left
}

// TestSetNotNull_ProvenByValidatedCheck pins TL-07: the three-migration path
// `forge db analyze` recommends (CHECK NOT VALID, VALIDATE, SET NOT NULL)
// passes, and only a CHECK that is valid, present, on the same table and
// column proves the SET NOT NULL.
func TestSetNotNull_ProvenByValidatedCheck(t *testing.T) {
	add := compatFile{"1_add.up.sql", `ALTER TABLE users ADD CONSTRAINT users_email_nn CHECK (email IS NOT NULL) NOT VALID;`}
	validate := compatFile{"2_validate.up.sql", `ALTER TABLE users VALIDATE CONSTRAINT users_email_nn;`}
	set := compatFile{"3_set.up.sql", "-- the CHECK proves it\nALTER TABLE users ALTER COLUMN email SET NOT NULL;"}

	cases := []struct {
		name  string
		files []compatFile
		want  int // set-not-null findings left
	}{
		{"the advised three files", []compatFile{add, validate, set}, 0},
		{"a CHECK added valid", []compatFile{{"1.up.sql", `ALTER TABLE users ADD CONSTRAINT nn CHECK ((email IS NOT NULL));`}, set}, 0},
		{"same statement, valid CHECK first", []compatFile{{"1.up.sql", `ALTER TABLE users ADD CONSTRAINT nn CHECK (email IS NOT NULL), ALTER COLUMN email SET NOT NULL;`}}, 0},
		{"never validated", []compatFile{add, set}, 1},
		{"validated then dropped", []compatFile{add, validate, {"3_drop.up.sql", `ALTER TABLE users DROP CONSTRAINT IF EXISTS users_email_nn;`}, {"4_set.up.sql", set.content}}, 1},
		{"another table's CHECK", []compatFile{add, validate, {"3_set.up.sql", `ALTER TABLE accounts ALTER COLUMN email SET NOT NULL;`}}, 1},
		{"another column", []compatFile{add, validate, {"3_set.up.sql", `ALTER TABLE users ALTER COLUMN name SET NOT NULL;`}}, 1},
		{"SET NOT NULL before the validation", []compatFile{add, {"2_set.up.sql", set.content}, {"3_validate.up.sql", validate.content}}, 1},
		{"a CHECK that is not IS NOT NULL", []compatFile{{"1.up.sql", `ALTER TABLE users ADD CONSTRAINT len CHECK (length(email) > 0);`}, set}, 1},
		{"quoted identifiers", []compatFile{{"1.up.sql", `ALTER TABLE "users" ADD CONSTRAINT "nn" CHECK ("email" IS NOT NULL);`}, set}, 0},
		{"unqualified drop invalidates a possibly matching qualified proof", []compatFile{
			{"1.up.sql", `ALTER TABLE public.users ADD CONSTRAINT nn CHECK (email IS NOT NULL);`},
			{"2.up.sql", `ALTER TABLE users DROP CONSTRAINT nn;`},
			{"3.up.sql", `ALTER TABLE public.users ALTER COLUMN email SET NOT NULL;`}}, 1},
		{"unqualified check cannot prove a different qualified name", []compatFile{
			{"1.up.sql", `ALTER TABLE users ADD CONSTRAINT nn CHECK (email IS NOT NULL);`},
			{"2.up.sql", `ALTER TABLE other.users ALTER COLUMN email SET NOT NULL;`}}, 1},
		{"a dot inside a table name is not a schema separator", []compatFile{
			{"1.up.sql", `ALTER TABLE "public.users" ADD CONSTRAINT nn CHECK (email IS NOT NULL);`},
			{"2.up.sql", `ALTER TABLE public.users ALTER COLUMN email SET NOT NULL;`}}, 1},
		{"quoted uppercase column is not an unquoted lower-case column", []compatFile{
			{"1.up.sql", `ALTER TABLE users ADD CONSTRAINT nn CHECK (email IS NOT NULL);`},
			{"2.up.sql", `ALTER TABLE users ALTER COLUMN "EMAIL" SET NOT NULL;`}}, 1},
		{"matching quoted uppercase column remains provable", []compatFile{
			{"1.up.sql", `ALTER TABLE users ADD CONSTRAINT nn CHECK ("EMAIL" IS NOT NULL);`},
			{"2.up.sql", `ALTER TABLE users ALTER COLUMN "EMAIL" SET NOT NULL;`}}, 0},
		{"two columns differing in case on the same line need separate proofs", []compatFile{
			{"1.up.sql", `ALTER TABLE users ADD CONSTRAINT nn CHECK (email IS NOT NULL);`},
			{"2.up.sql", `ALTER TABLE users ALTER COLUMN email SET NOT NULL, ALTER COLUMN "EMAIL" SET NOT NULL;`}}, 1},
		{"parenthesis in a default must not hide the next SET NOT NULL", []compatFile{
			{"1.up.sql", `ALTER TABLE users ADD COLUMN note text DEFAULT '(', ALTER COLUMN email SET NOT NULL;`}}, 1},
		{"doubled quote in a default keeps the parenthesis literal", []compatFile{
			{"1.up.sql", `ALTER TABLE users ADD COLUMN note text DEFAULT 'we''re (', ALTER COLUMN email SET NOT NULL;`}}, 1},
		{"escape string default must not hide the next SET NOT NULL", []compatFile{
			{"1.up.sql", `ALTER TABLE users ADD COLUMN note text DEFAULT E'\'x(', ALTER COLUMN email SET NOT NULL;`}}, 1},
		{"dollar quote default must not hide the next SET NOT NULL", []compatFile{
			{"1.up.sql", `ALTER TABLE users ADD COLUMN note text DEFAULT $text$(,;)$text$ || $text$($text$, ALTER COLUMN email SET NOT NULL;`}}, 1},
		{"quoted identifier parentheses must not hide the next SET NOT NULL", []compatFile{
			{"1.up.sql", `ALTER TABLE users ADD COLUMN "x(," text, ALTER COLUMN email SET NOT NULL;`}}, 1},
		{"literal SQL text is not a SET NOT NULL clause", []compatFile{
			{"1.up.sql", `ALTER TABLE users ADD COLUMN note text DEFAULT 'body , ALTER COLUMN email SET NOT NULL , text';`}}, 0},
		{"nested block comments are not ALTER clause syntax", []compatFile{
			{"1.up.sql", `ALTER TABLE users ADD COLUMN note text /* outer ( /* inner */ outer , */, ALTER COLUMN email SET NOT NULL;`}}, 1},
		{"a search path change invalidates unqualified proofs", []compatFile{add, validate,
			{"3.up.sql", `SET search_path TO other, public;`}, {"4.up.sql", set.content}}, 1},
		// Every spelling of a search path change, and any SET not known to be
		// harmless, forgets the proofs (review B1, round 5).
		{"SET SCHEMA is a search path change", []compatFile{add, validate,
			{"3.up.sql", `SET SCHEMA 'other';`}, {"4.up.sql", set.content}}, 1},
		{"a quoted search_path is the search path", []compatFile{add, validate,
			{"3.up.sql", `SET "search_path" TO other;`}, {"4.up.sql", set.content}}, 1},
		{"SET LOCAL of a quoted SEARCH_PATH", []compatFile{add, validate,
			{"3.up.sql", `SET LOCAL "search_path" = other;`}, {"4.up.sql", set.content}}, 1},
		{"an unknown parameter is not known to be harmless", []compatFile{add, validate,
			{"3.up.sql", `SET myext.path = 'x';`}, {"4.up.sql", set.content}}, 1},
		{"a lock timeout keeps the proof", []compatFile{add, validate,
			{"3.up.sql", `SET lock_timeout = '5s';`}, {"4.up.sql", set.content}}, 0},
		{"a quoted harmless parameter keeps the proof", []compatFile{add, validate,
			{"3.up.sql", `SET "statement_timeout" TO 0;`}, {"4.up.sql", set.content}}, 0},

		// The whole expression must be the proof: another predicate admits
		// NULLs (review B1 of #2533).
		{"OR true", []compatFile{{"1.up.sql", `ALTER TABLE users ADD CONSTRAINT nn CHECK ((email IS NOT NULL) OR true);`}, set}, 1},
		{"OR true, NOT VALID then validated", []compatFile{
			{"1.up.sql", `ALTER TABLE users ADD CONSTRAINT nn CHECK ((email IS NOT NULL) OR true) NOT VALID;`},
			{"2.up.sql", `ALTER TABLE users VALIDATE CONSTRAINT nn;`}, set}, 1},
		{"OR another column", []compatFile{{"1.up.sql", `ALTER TABLE users ADD CONSTRAINT nn CHECK (email IS NOT NULL OR name IS NOT NULL);`}, set}, 1},
		{"a trailing modifier it does not read", []compatFile{{"1.up.sql", `ALTER TABLE users ADD CONSTRAINT nn CHECK (email IS NOT NULL) NO INHERIT;`}, set}, 1},

		// A proof dies with what it proves (review B2 of #2533).
		{"column dropped and re-added", []compatFile{add, validate,
			{"3.up.sql", "ALTER TABLE users DROP COLUMN email;\nALTER TABLE users ADD COLUMN email TEXT;"},
			{"4_set.up.sql", set.content}}, 1},
		{"table dropped and recreated", []compatFile{add, validate,
			{"3.up.sql", "DROP TABLE users;\nCREATE TABLE users (id TEXT PRIMARY KEY, email TEXT);"},
			{"4_set.up.sql", set.content}}, 1},
		{"column renamed away and another renamed in", []compatFile{add, validate,
			{"3.up.sql", "ALTER TABLE users RENAME COLUMN email TO old_email;\nALTER TABLE users RENAME COLUMN backup TO email;"},
			{"4_set.up.sql", set.content}}, 1},
		{"table renamed away and another renamed in", []compatFile{add, validate,
			{"3.up.sql", "ALTER TABLE users RENAME TO old_users;\nALTER TABLE new_users RENAME TO users;"},
			{"4_set.up.sql", set.content}}, 1},
		{"column retyped", []compatFile{add, validate,
			{"3.up.sql", "ALTER TABLE users ALTER COLUMN email TYPE varchar(320);"},
			{"4_set.up.sql", set.content}}, 1},
		{"harmless statements keep the proof", []compatFile{add, validate,
			{"3.up.sql", "UPDATE users SET name = 'x' WHERE name IS NULL;\nCREATE INDEX users_name ON users (name);\nSET lock_timeout = '5s';\nCOMMENT ON TABLE users IS 'x';"},
			{"4_set.up.sql", set.content}}, 0},
		// Conservative from here: what the pass does not positively know to
		// be harmless forgets proofs. An extra warning is acceptable.
		{"any other ALTER TABLE clause on the table", []compatFile{add, validate,
			{"3.up.sql", "ALTER TABLE users DROP COLUMN legacy;"},
			{"4_set.up.sql", set.content}}, 1},

		// Review round 4 of #2533.
		// B1: two statements on one line share the finding's file:line:column,
		// so the findings cannot be told apart. Only users.email is proven:
		// both stay (the unproven accounts.email must never be silenced; the
		// extra warning on users.email is the price of not guessing).
		{"one line, two tables, one proven", []compatFile{add, validate,
			{"3_set.up.sql", "ALTER TABLE users ALTER COLUMN email SET NOT NULL; ALTER TABLE accounts ALTER COLUMN email SET NOT NULL;"}}, 2},
		{"one line, two tables, both proven", []compatFile{add, validate,
			{"3.up.sql", "ALTER TABLE accounts ADD CONSTRAINT nn CHECK (email IS NOT NULL);"},
			{"4_set.up.sql", "ALTER TABLE users ALTER COLUMN email SET NOT NULL; ALTER TABLE accounts ALTER COLUMN email SET NOT NULL;"}}, 0},
		// B2: a schema cascade removes the table without naming it.
		{"DROP SCHEMA CASCADE then recreate", []compatFile{
			{"1.up.sql", `ALTER TABLE public.users ADD CONSTRAINT nn CHECK (email IS NOT NULL);`},
			{"2.up.sql", "DROP SCHEMA public CASCADE;\nCREATE SCHEMA public;\nCREATE TABLE public.users (id TEXT PRIMARY KEY, email TEXT);"},
			{"3_set.up.sql", `ALTER TABLE public.users ALTER COLUMN email SET NOT NULL;`}}, 1},
		{"DROP SCHEMA CASCADE alone", []compatFile{
			{"1.up.sql", `ALTER TABLE public.users ADD CONSTRAINT nn CHECK (email IS NOT NULL);`},
			{"2.up.sql", "DROP SCHEMA public CASCADE;"},
			{"3_set.up.sql", `ALTER TABLE public.users ALTER COLUMN email SET NOT NULL;`}}, 1},
		// B2: procedural DDL the pass cannot read.
		{"DO block dropping the constraint", []compatFile{add, validate,
			{"3.up.sql", "DO $$ BEGIN EXECUTE 'ALTER TABLE users DROP CONSTRAINT users_email_nn'; END $$;"},
			{"4_set.up.sql", set.content}}, 1},
		{"a function call", []compatFile{add, validate,
			{"3.up.sql", "SELECT drop_old_constraints();"},
			{"4_set.up.sql", set.content}}, 1},
		// B3: PostgreSQL's inheritance star after the table name.
		{"inheritance star, unproven", []compatFile{{"1_set.up.sql", `ALTER TABLE users * ALTER COLUMN email SET NOT NULL;`}}, 1},
		{"inheritance star, proven", []compatFile{add, validate, {"3_set.up.sql", `ALTER TABLE users * ALTER COLUMN email SET NOT NULL;`}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := lintSequence(t, tc.files...); len(got) != tc.want {
				t.Errorf("set-not-null-without-backfill findings = %v, want %d", got, tc.want)
			}
		})
	}
}
