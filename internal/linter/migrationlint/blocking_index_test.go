package migrationlint

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBlockingIndexBuild pins NS FG-35: a plain CREATE INDEX on a table an
// earlier migration created is a warning; a concurrent build, or an index on
// a table the same file creates, is not.
func TestBlockingIndexBuild(t *testing.T) {
	cases := []struct {
		name, sql string
		want      int
	}{
		{"plain index on an existing table", "CREATE INDEX idx_products_name ON products (name);", 1},
		{"unique, if not exists, schema-qualified", "CREATE UNIQUE INDEX IF NOT EXISTS ux ON public.products (sku);", 1},
		{"unnamed index", "create index on products (name);", 1},
		{"concurrently", "CREATE INDEX CONCURRENTLY idx_products_name ON products (name);", 0},
		{"table created in the same file", "CREATE TABLE products (id text PRIMARY KEY, name text);\nCREATE INDEX idx_products_name ON products (name);", 0},
		{"quoted table created in the same file", "CREATE TABLE public.\"Products\" (id text);\nCREATE INDEX i ON public.\"Products\" (id);", 0},
		{"different schemas do not share an emptiness proof", "CREATE TABLE staging.products (id text);\nCREATE INDEX i ON public.products (id);", 1},
		{"different qualification does not prove the same relation", "CREATE TABLE staging.products (id text);\nCREATE INDEX i ON products (id);", 1},
		{"a quoted dot is part of a relation name", "CREATE TABLE \"staging.products\" (id text);\nCREATE INDEX i ON staging.products (id);", 1},
		{"an ADD COLUMN default can invoke SQL", "CREATE TABLE products (id int);\nALTER TABLE old_products ADD COLUMN copied boolean DEFAULT backfill_products();\nCREATE INDEX i ON products (id);", 1},
		{"a changed search path invalidates unqualified evidence", "CREATE TABLE products (id int);\nSET search_path TO public;\nCREATE INDEX i ON products (id);", 1},
		{"another table created in the same file", "CREATE TABLE orders (id text);\nCREATE INDEX i ON products (name);", 1},
		{"in a comment", "-- CREATE INDEX i ON products (name);\nSELECT 1;", 0},
		// Emptiness is proved only up to the first statement that could
		// write a row: an INSERT (or any DML, or a function call) before the
		// index ends it.
		{"created, then filled, then indexed", "CREATE TABLE products (id text, name text);\nINSERT INTO products VALUES ('a', 'b');\nCREATE INDEX i ON products (name);", 1},
		{"created, a function call, then indexed", "CREATE TABLE products (id text);\nSELECT backfill_products();\nCREATE INDEX i ON products (id);", 1},
		{"created, row-free DDL, then indexed", "CREATE TABLE products (id text);\nALTER TABLE products ADD COLUMN name text;\nCOMMENT ON TABLE products IS 'x';\nCREATE INDEX i ON products (name);", 0},
		{"indexed before the file creates it", "CREATE INDEX i ON products (name);\nCREATE TABLE IF NOT EXISTS products (id text, name text);", 1},
		{"CREATE TABLE IF NOT EXISTS proves nothing for a plain index", "CREATE TABLE IF NOT EXISTS products (id text);\nCREATE INDEX i ON products (id);", 1},
		// The re-runnable shape migratekit's MissingMigrationError
		// recommends: wherever the table is new the index is built empty,
		// and wherever an earlier run of the file created the table it
		// created the index too, so the build is a no-op.
		{"IF NOT EXISTS table, then IF NOT EXISTS index", "CREATE TABLE IF NOT EXISTS products (id text, name text);\nCREATE INDEX IF NOT EXISTS i ON products (id);\nCREATE UNIQUE INDEX IF NOT EXISTS j ON products (name);", 0},
		{"IF NOT EXISTS table, schema-qualified", "create table if not exists controlplane.llm_step_keys (id text);\ncreate index if not exists i on controlplane.llm_step_keys (id);", 0},
		{"IF NOT EXISTS index on a table only an IF NOT EXISTS of another table names", "CREATE TABLE IF NOT EXISTS orders (id text);\nCREATE INDEX IF NOT EXISTS i ON products (id);", 1},
		{"IF NOT EXISTS table, filled, then IF NOT EXISTS index", "CREATE TABLE IF NOT EXISTS products (id text);\nINSERT INTO products VALUES ('a');\nCREATE INDEX IF NOT EXISTS i ON products (id);", 1},
		{"a plain index on an IF NOT EXISTS table ends other proofs", "CREATE TABLE orders (id text);\nCREATE TABLE IF NOT EXISTS products (id text);\nCREATE INDEX i ON products (id);\nCREATE INDEX j ON orders (id);", 2},
		{"IF NOT EXISTS after a plain CREATE TABLE keeps the stronger proof", "CREATE TABLE products (id text);\nCREATE TABLE IF NOT EXISTS products (id text);\nCREATE INDEX i ON products (id);", 0},
		{"IF NOT EXISTS CTAS creates it full", "CREATE TABLE IF NOT EXISTS products AS SELECT * FROM old_products;\nCREATE INDEX IF NOT EXISTS i ON products (id);", 1},
		{"CREATE TABLE AS creates it full", "CREATE TABLE products AS SELECT * FROM old_products;\nCREATE INDEX i ON products (id);", 1},
		{"CTAS with named columns is not a plain table", "CREATE TABLE products (id) AS SELECT * FROM old_products;\nCREATE INDEX i ON products (id);", 1},
		{"partition creation does not prove an empty relation", "CREATE TABLE products_child PARTITION OF products FOR VALUES IN (1);\nCREATE INDEX i ON products_child (id);", 1},
		{"partition creation ends an earlier emptiness proof", "CREATE TABLE products (id int) PARTITION BY LIST (id);\nCREATE TABLE products_child PARTITION OF products FOR VALUES IN (1);\nCREATE INDEX i ON products (id);", 1},
		{"attaching a partition may expose its existing rows", "CREATE TABLE products (id int) PARTITION BY LIST (id);\nALTER TABLE products ATTACH PARTITION old_products FOR VALUES IN (1);\nCREATE INDEX i ON products (id);", 1},
		{"unknown CREATE ends an earlier emptiness proof", "CREATE TABLE products (id int);\nCREATE EXTENSION custom_backfill;\nCREATE INDEX i ON products (id);", 1},
		{"plain CREATE TABLE and CREATE INDEX preserve a fresh proof", "CREATE TABLE products (id int);\nCREATE TABLE orders (id int);\nCREATE INDEX i ON products (id);\nCREATE INDEX j ON products (id);", 0},
		{"a generated column is not CREATE TABLE AS", "CREATE TABLE products (id text, n int GENERATED ALWAYS AS (1) STORED);\nCREATE INDEX i ON products (id);", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []Finding
			for _, f := range lintStatements("m.up.sql", tc.sql, DefaultConfig()) {
				if f.Rule == RuleBlockingIndexBuild {
					got = append(got, f)
				}
			}
			if len(got) != tc.want {
				t.Fatalf("findings = %+v, want %d", got, tc.want)
			}
			for _, f := range got {
				if f.Severity != SeverityWarn {
					t.Errorf("severity = %q, want a warning: static lint cannot see the table's size", f.Severity)
				}
			}
		})
	}
}

// The rule runs through the directory pass and leaves the verdict green.
func TestBlockingIndexBuildIsAWarningInTheDirectoryPass(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("20260101000000_products.up.sql", "-- forge:backward-compatible — new table\nCREATE TABLE products (id text PRIMARY KEY, name text);\n")
	write("20260102000000_index_products_name.up.sql", "-- forge:backward-compatible — an index\nCREATE INDEX idx_products_name ON products (name);\n")
	res, err := LintMigrationsDir(dir, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range res.Findings {
		if f.Rule == RuleBlockingIndexBuild {
			found = true
			if f.Line != 2 {
				t.Errorf("line = %d, want 2", f.Line)
			}
		}
	}
	if !found || res.HasErrors() {
		t.Fatalf("findings = %+v; want one blocking-index-build warning and no error", res.Findings)
	}
}
