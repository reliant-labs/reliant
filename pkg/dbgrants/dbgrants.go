// Package dbgrants is the read-only database role every forge-managed Postgres
// carries, and the grants that bound what it can read.
//
// The role exists so something outside the application — `forge db query`, the
// control plane's QueryDatabase — can look at data without being able to change
// it or to read what the application marked secret. The boundary is POSTGRES,
// not a redactor: a redactor cannot follow `upper(secret_col)`, but a column the
// role has no SELECT privilege on cannot be read through any expression.
//
// # The three pieces
//
//   - The role itself is declared by the ManagedDatabase render (pkg/deploy adds
//     a CNPG managed role, [RoleName], whose password lives in [SecretName]).
//   - The grants are a projection of the applied schema and the proto
//     `forge:secret` markers, rendered here by [Render] into
//     db/grants/readonly_gen.sql.
//   - The migrator applies that file after every run (pkg/migratekit), through
//     [Apply]. Every migration therefore converges the grants: a new table is
//     granted, a new secret column stays excluded, and a column that stopped
//     being secret becomes readable.
//
// # Why REVOKE then GRANT
//
// The script is a CONVERGENCE, not a delta: per schema it revokes everything
// the role holds on tables and sequences, then grants exactly the declared
// columns, in one transaction. A delta would leave a table dropped from the
// declaration, or a column that became secret, readable until someone noticed.
// Revoking a table's privileges also revokes its column-level ones.
//
// What the role is never given: INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES,
// TRIGGER, any sequence privilege, any view (a view can expose a secret column
// of its base table, and the catalog cannot say which), and any table-level
// SELECT — a table-level SELECT would cover columns added later, including
// secret ones.
package dbgrants

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	// RoleName is the read-only Postgres role.
	RoleName = "forge_ro"
	// SecretSuffix is appended to a cluster name to name the Secret that holds
	// the role's password.
	SecretSuffix = "-forge-ro"
	// GrantsPath is the generated grants file, relative to the project root.
	GrantsPath = "db/grants/readonly_gen.sql"
)

// SecretName is the kubernetes.io/basic-auth Secret holding the role's
// credentials for the ManagedDatabase called cluster, in the cluster's
// namespace. It is a contract with CNPG's managed-role reconciler (the role's
// passwordSecret) and with whoever connects as the role.
func SecretName(cluster string) string { return cluster + SecretSuffix }

// Table is one table the role may read.
type Table struct {
	Schema  string
	Name    string
	Columns []Column
}

// Column is one column of a Table, with the declarations that decide whether
// the role may read it (see [Column.Disposition]).
type Column struct {
	Name string
	// Secret: declared forge:secret (proto field or SQL comment). Withheld.
	Secret bool
	// Readable: declared forge:readable <reason>. Granted even when the name
	// looks like a credential. Secret wins over it.
	Readable bool
	// Kind is the column's type class. The zero value, [KindText], is the
	// stricter reading.
	Kind ColumnKind
}

// dollarTag delimits the script's single DO block. An identifier containing it
// would end the block early, so Render refuses one.
const dollarTag = "$forge_grants$"

// Render returns the grants script for tables.
//
// It is deterministic: schemas, tables and columns are sorted, so the same
// schema renders the same bytes. A table whose every column is secret is
// declared with no readable column and is granted nothing.
//
// THE SCRIPT IS DATA PLUS A FIXED LOOP, not a list of GRANT statements. The
// declaration is a VALUES list of (schema, table, readable columns), and the
// loop grants the intersection of that with what the database has. A file that
// is older than the schema (a migration landed and `forge generate` has not
// run) can therefore never fail the migration that carries it by naming a
// table or column that has since been dropped, and can never grant more than
// it declares: an unlisted table or column is simply not granted.
//
// The script is a no-op when the role does not exist, so it is safe to apply to
// a database that has none (a local compose Postgres).
func Render(tables []Table) (string, error) { return RenderFor(RoleName, tables) }

// RenderFor is Render for a named role. Production has exactly one role,
// [RoleName]; the parameter exists because roles are cluster-wide, so tests
// that share a Postgres server cannot all create forge_ro.
func RenderFor(role string, tables []Table) (string, error) {
	if err := checkIdent(role); err != nil {
		return "", err
	}
	bySchema := map[string][]Table{}
	for _, t := range tables {
		if t.Schema == "" || t.Name == "" {
			return "", errors.New("dbgrants: a table needs a schema and a name")
		}
		if err := checkIdent(t.Schema); err != nil {
			return "", err
		}
		if err := checkIdent(t.Name); err != nil {
			return "", err
		}
		for _, c := range t.Columns {
			if err := checkIdent(c.Name); err != nil {
				return "", err
			}
		}
		bySchema[t.Schema] = append(bySchema[t.Schema], t)
	}
	schemas := make([]string, 0, len(bySchema))
	for s := range bySchema {
		schemas = append(schemas, s)
	}
	sort.Strings(schemas)

	var b strings.Builder
	b.WriteString("-- The read-only role's grants: a convergence, not a delta. See pkg/dbgrants.\n")
	b.WriteString("DO " + dollarTag + "\n")
	b.WriteString("DECLARE\n  r record;\n  readable text;\nBEGIN\n")
	fmt.Fprintf(&b, "  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = %s) THEN\n", quoteLiteral(role))
	fmt.Fprintf(&b, "    RAISE NOTICE %s;\n    RETURN;\n  END IF;\n\n", quoteLiteral("role "+role+" does not exist: read-only grants not applied"))

	// Everything the role holds, in every declared schema, goes first. A column
	// that became secret, a table that was dropped from the declaration and a
	// table no declaration has met yet are all covered by this, which is why it
	// precedes the grants rather than being a diff against them.
	b.WriteString("  -- schemas: where the role may look\n")
	b.WriteString("  FOR r IN SELECT s.nspname AS schema FROM (VALUES\n")
	for i, schema := range schemas {
		sep := ","
		if i == len(schemas)-1 {
			sep = ""
		}
		fmt.Fprintf(&b, "    (%s)%s\n", quoteLiteral(schema), sep)
	}
	if len(schemas) == 0 {
		b.WriteString("    (NULL::text)\n")
	}
	b.WriteString("  ) AS s(nspname) WHERE s.nspname IS NOT NULL AND EXISTS (SELECT 1 FROM pg_namespace n WHERE n.nspname = s.nspname) LOOP\n")
	fmt.Fprintf(&b, "    EXECUTE format('GRANT USAGE ON SCHEMA %%I TO %%I', r.schema, %s);\n", quoteLiteral(role))
	fmt.Fprintf(&b, "    EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA %%I FROM %%I', r.schema, %s);\n", quoteLiteral(role))
	fmt.Fprintf(&b, "    EXECUTE format('REVOKE ALL ON ALL SEQUENCES IN SCHEMA %%I FROM %%I', r.schema, %s);\n", quoteLiteral(role))
	b.WriteString("  END LOOP;\n\n")

	b.WriteString("  -- tables: the columns the role may read. A forge:secret column, and a credential-shaped one nobody decided, is not listed.\n")
	b.WriteString("  FOR r IN SELECT g.* FROM (VALUES\n")
	var rows []string
	var withheld, withheldUndecided []string
	for _, schema := range schemas {
		ts := bySchema[schema]
		sort.Slice(ts, func(i, j int) bool { return ts[i].Name < ts[j].Name })
		for _, t := range ts {
			var open, secret, undecided []string
			for _, c := range t.Columns {
				switch c.Disposition() {
				case WithheldSecret:
					secret = append(secret, c.Name)
				case WithheldUndecided:
					undecided = append(undecided, c.Name)
				default:
					open = append(open, c.Name)
				}
			}
			sort.Strings(open)
			sort.Strings(secret)
			sort.Strings(undecided)
			lits := make([]string, len(open))
			for i, c := range open {
				lits[i] = quoteLiteral(c)
			}
			arr := "ARRAY[]::text[]"
			if len(lits) > 0 {
				arr = "ARRAY[" + strings.Join(lits, ", ") + "]::text[]"
			}
			rows = append(rows, fmt.Sprintf("    (%s, %s, %s)", quoteLiteral(schema), quoteLiteral(t.Name), arr))
			if len(secret) > 0 {
				withheld = append(withheld, fmt.Sprintf("%s.%s: %s", schema, t.Name, strings.Join(secret, ", ")))
			}
			if len(undecided) > 0 {
				withheldUndecided = append(withheldUndecided, fmt.Sprintf("%s.%s: %s", schema, t.Name, strings.Join(undecided, ", ")))
			}
		}
	}
	if len(rows) == 0 {
		rows = []string{"    (NULL::text, NULL::text, ARRAY[]::text[])"}
	}
	b.WriteString(strings.Join(rows, ",\n") + "\n")
	b.WriteString("  ) AS g(schema, tbl, cols) WHERE g.schema IS NOT NULL LOOP\n")
	b.WriteString("    SELECT string_agg(format('%I', a.attname), ', ' ORDER BY a.attname) INTO readable\n")
	b.WriteString("      FROM pg_attribute a\n")
	b.WriteString("     WHERE a.attrelid = to_regclass(format('%I.%I', r.schema, r.tbl))\n")
	b.WriteString("       AND a.attnum > 0 AND NOT a.attisdropped AND a.attname = ANY (r.cols);\n")
	b.WriteString("    IF readable IS NOT NULL THEN\n")
	fmt.Fprintf(&b, "      EXECUTE format('GRANT SELECT (%%s) ON TABLE %%I.%%I TO %%I', readable, r.schema, r.tbl, %s);\n", quoteLiteral(role))
	b.WriteString("    END IF;\n  END LOOP;\n")

	// Defence in depth only: the grants above already give the role no write
	// privilege, and every query runs in a READ ONLY transaction. Changing a
	// role's settings needs superuser or CREATEROLE with admin option on it,
	// which the application's owner role does not have (and CNPG's managed-role
	// stanza has no per-role setting), so an insufficient privilege is a notice.
	fmt.Fprintf(&b, "\n  BEGIN\n    ALTER ROLE %s SET default_transaction_read_only = on;\n", quoteIdent(role))
	b.WriteString("  EXCEPTION WHEN insufficient_privilege THEN\n")
	fmt.Fprintf(&b, "    RAISE NOTICE %s;\n  END;\n", quoteLiteral("could not set default_transaction_read_only on "+role+": the migrating role may not alter it"))
	b.WriteString("END\n" + dollarTag + ";\n")
	if len(withheld) > 0 {
		b.WriteString("\n-- forge:secret columns withheld from the role:\n")
		for _, w := range withheld {
			b.WriteString("--   " + strings.ReplaceAll(w, "\n", " ") + "\n")
		}
	}
	if len(withheldUndecided) > 0 {
		b.WriteString("\n-- credential-shaped columns nobody decided, withheld until a migration declares\n")
		b.WriteString("-- COMMENT ON COLUMN t.c IS 'forge:secret'  (stay hidden)  or  'forge:readable <reason>'  (grant):\n")
		for _, w := range withheldUndecided {
			b.WriteString("--   " + strings.ReplaceAll(w, "\n", " ") + "\n")
		}
	}
	return b.String(), nil
}

func checkIdent(s string) error {
	if strings.ContainsAny(s, "\x00\r\n") || strings.Contains(s, dollarTag) {
		return fmt.Errorf("dbgrants: identifier %q cannot be rendered into the grants script", s)
	}
	return nil
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func quoteLiteral(s string) string { return `'` + strings.ReplaceAll(s, `'`, `''`) + `'` }

// Conn is the slice of *sql.Conn / *sql.DB / *sql.Tx that this package needs.
type Conn interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// RoleExists reports whether the database cluster has a role called name. A
// database no one provisioned [RoleName] for (a local compose Postgres) has
// none, and that is a normal state, not a fault.
func RoleExists(ctx context.Context, c Conn, name string) (bool, error) {
	var exists bool
	if err := c.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, name).Scan(&exists); err != nil {
		return false, fmt.Errorf("dbgrants: look up role %s: %w", name, err)
	}
	return exists, nil
}

// Apply runs script, a [Render] result, on c. The script is one statement, so
// it is atomic: a failure leaves the previous grants in place.
func Apply(ctx context.Context, c Conn, script string) error {
	if _, err := c.ExecContext(ctx, script); err != nil {
		return fmt.Errorf("dbgrants: apply read-only grants: %w", err)
	}
	return nil
}
