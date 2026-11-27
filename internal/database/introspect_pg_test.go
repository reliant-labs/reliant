package database

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/testkit"
)

const introspectDDL = `
CREATE TABLE accounts (
    id    TEXT PRIMARY KEY,
    email VARCHAR(120) NOT NULL,
    note  TEXT,
    score INTEGER NOT NULL DEFAULT 7
);
CREATE UNIQUE INDEX accounts_email_key ON accounts (email);
CREATE INDEX accounts_note_score ON accounts (note, score);
CREATE TABLE invoices (
    id         BIGSERIAL PRIMARY KEY,
    account_id TEXT NOT NULL,
    CONSTRAINT invoices_account_fk FOREIGN KEY (account_id) REFERENCES accounts (id)
);
CREATE SCHEMA other;
CREATE TABLE other.ignored (id INT);
CREATE VIEW account_emails AS SELECT email FROM accounts;
`

// introspectDSN boots a throwaway postgres database holding introspectDDL.
func introspectDSN(t *testing.T) string {
	t.Helper()
	testkit.Slow(t, "boots real postgres")
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("test postgres: %v", err)
	}
	t.Cleanup(cleanup)
	db, err := ConnectDB(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(), introspectDDL); err != nil {
		t.Fatalf("apply ddl: %v", err)
	}
	return dsn
}

func connect(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := ConnectDB(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestIntrospectSchemaReadsTablesColumnsIndexesAndForeignKeys(t *testing.T) {
	db := connect(t, introspectDSN(t))
	tables, err := IntrospectSchema(context.Background(), db, "")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tb := range tables {
		names = append(names, tb.Name)
	}
	// Base tables of the public schema only: no view, no other schema.
	if !slices.Equal(names, []string{"accounts", "invoices"}) {
		t.Fatalf("tables = %v, want [accounts invoices]", names)
	}

	acc := tables[0]
	if acc.Schema != "public" || !slices.Equal(acc.PrimaryKey, []string{"id"}) {
		t.Errorf("accounts schema=%q pk=%v", acc.Schema, acc.PrimaryKey)
	}
	byName := map[string]Column{}
	var order []string
	for _, c := range acc.Columns {
		byName[c.Name] = c
		order = append(order, c.Name)
	}
	if !slices.Equal(order, []string{"id", "email", "note", "score"}) {
		t.Errorf("columns are not in declaration order: %v", order)
	}
	if id := byName["id"]; !id.IsPrimary || id.Nullable || id.Type != "text" {
		t.Errorf("id = %+v", id)
	}
	if email := byName["email"]; email.Type != "character varying" || email.MaxLength == nil || *email.MaxLength != 120 || email.Nullable || email.IsPrimary {
		t.Errorf("email = %+v, want varchar(120) not null", email)
	}
	if note := byName["note"]; !note.Nullable || note.Default != "" || note.MaxLength != nil {
		t.Errorf("note = %+v, want nullable with no default", note)
	}
	if score := byName["score"]; score.Default != "7" || score.UDTName != "int4" {
		t.Errorf("score = %+v, want default 7 and udt int4", score)
	}

	// The primary key's own index is not listed; the other two are, by name.
	if len(acc.Indexes) != 2 || acc.Indexes[0].Name != "accounts_email_key" || !acc.Indexes[0].Unique ||
		!slices.Equal(acc.Indexes[0].Columns, []string{"email"}) {
		t.Fatalf("indexes = %+v", acc.Indexes)
	}
	if idx := acc.Indexes[1]; idx.Name != "accounts_note_score" || idx.Unique || !slices.Equal(idx.Columns, []string{"note", "score"}) {
		t.Errorf("composite index = %+v, want its columns in index order", idx)
	}

	inv := tables[1]
	if len(inv.ForeignKeys) != 1 {
		t.Fatalf("foreign keys = %+v", inv.ForeignKeys)
	}
	if fk := inv.ForeignKeys[0]; fk != (ForeignKey{Name: "invoices_account_fk", Column: "account_id", ReferencedTable: "accounts", ReferencedColumn: "id"}) {
		t.Errorf("fk = %+v", fk)
	}
	if len(acc.ForeignKeys) != 0 {
		t.Errorf("accounts has no foreign keys: %+v", acc.ForeignKeys)
	}
}

func TestIntrospectSchemaFiltersToOneTable(t *testing.T) {
	db := connect(t, introspectDSN(t))
	tables, err := IntrospectSchema(context.Background(), db, "invoices")
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 1 || tables[0].Name != "invoices" {
		t.Errorf("tables = %+v, want only invoices", tables)
	}
	none, err := IntrospectSchema(context.Background(), db, "no_such_table")
	if err != nil || len(none) != 0 {
		t.Errorf("unknown table = %+v, %v; want an empty schema, not an error", none, err)
	}
	// Both the view and a table of another schema are excluded even by name.
	for _, name := range []string{"account_emails", "ignored"} {
		if got, err := IntrospectSchema(context.Background(), db, name); err != nil || len(got) != 0 {
			t.Errorf("IntrospectSchema(%q) = %+v, %v; want nothing", name, got, err)
		}
	}
}

func TestIntrospectTableOfAnUnknownNameIsEmptyNotAnError(t *testing.T) {
	db := connect(t, introspectDSN(t))
	tb, err := IntrospectTable(context.Background(), db, "no_such_table")
	if err != nil {
		t.Fatal(err)
	}
	if tb.Name != "no_such_table" || len(tb.Columns) != 0 || len(tb.PrimaryKey) != 0 {
		t.Errorf("table = %+v", tb)
	}
}

// The introspection feeds both output formats and the migration context
// comment, so the three agree on what a live table looks like.
func TestIntrospectedSchemaFormatsAndFeedsTheContextComment(t *testing.T) {
	dsn := introspectDSN(t)
	db := connect(t, dsn)
	tables, err := IntrospectSchema(context.Background(), db, "accounts")
	if err != nil {
		t.Fatal(err)
	}
	text := FormatSchemaText(tables)
	for _, want := range []string{"accounts_email_key UNIQUE (email)", "character varying(120)"} {
		if !strings.Contains(text, want) {
			t.Errorf("text output lacks %q:\n%s", want, text)
		}
	}
	js, err := FormatSchemaJSON(tables)
	if err != nil || !strings.Contains(js, `"name": "accounts"`) {
		t.Errorf("json = %q, %v", js, err)
	}

	// With a DSN the context carries the live schema, preferred over parsed SQL.
	dir := t.TempDir()
	mctx, err := GatherMigrationContext(context.Background(), "add_things", dir, MigrationOptions{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	if len(mctx.LiveTables) != 2 {
		t.Fatalf("LiveTables = %d, want the two tables", len(mctx.LiveTables))
	}
	comment := GenerateContextComment(mctx)
	for _, want := range []string{
		"CURRENT SCHEMA (from database)",
		"--   id TEXT PRIMARY KEY NOT NULL",
		"--   email VARCHAR NOT NULL,",
		"--   score INT4 NOT NULL DEFAULT 7",
		"--   note TEXT,",
	} {
		if !strings.Contains(comment, want) {
			t.Errorf("context comment lacks %q:\n%s", want, comment)
		}
	}
}

func TestConnectDBReportsAnUnreachableServer(t *testing.T) {
	// Port 1 on loopback: nothing listens, the connection is refused at once.
	_, err := ConnectDB(context.Background(), "postgres://u:p@127.0.0.1:1/db?connect_timeout=2&sslmode=disable")
	if err == nil || !strings.Contains(err.Error(), "failed to connect to database") {
		t.Errorf("err = %v, want a connect failure", err)
	}
}

func TestConnectDBRejectsAMalformedDSN(t *testing.T) {
	_, err := ConnectDB(context.Background(), "postgres://u:p@host:notaport/db")
	if err == nil {
		t.Fatal("a malformed DSN must not connect")
	}
}

func TestConnectDBHonoursACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ConnectDB(ctx, "postgres://u:p@127.0.0.1:1/db?sslmode=disable"); err == nil {
		t.Fatal("a cancelled context must not connect")
	}
}

// Queries against a closed database surface as wrapped errors that name the
// step that failed, so a user knows whether the table list or one table broke.
func TestIntrospectionErrorsNameTheFailingStep(t *testing.T) {
	db := connect(t, introspectDSN(t))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := IntrospectSchema(context.Background(), db, ""); err == nil || !strings.Contains(err.Error(), "listing tables") {
		t.Errorf("IntrospectSchema on a closed db: err = %v, want listing tables", err)
	}
	if _, err := IntrospectTable(context.Background(), db, "accounts"); err == nil || !strings.Contains(err.Error(), "getting columns") {
		t.Errorf("IntrospectTable on a closed db: err = %v, want getting columns", err)
	}
}
