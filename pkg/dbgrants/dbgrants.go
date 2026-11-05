// Package dbgrants is the read-only database role every forge-managed Postgres
// carries, and the grants that bound what it can read.
//
// The role exists so something outside the application — `forge db query`, the
// control plane's QueryDatabase — can look at data without being able to change
// it or to read what the application marked secret. The boundary is POSTGRES,
// not a redactor: a redactor cannot follow `upper(secret_col)`, but a column the
// role has no SELECT privilege on cannot be read through any expression the
// ROLE evaluates. A SECURITY DEFINER function is the exception: it evaluates
// its body as its owner, so one the role may execute (PUBLIC holds EXECUTE on
// every new function by default) returns whatever its body selects, secret
// columns included. forge generates none; [SecurityDefinerExposure] finds them
// and the migrator logs a warning. Revoke PUBLIC's EXECUTE on such a function,
// or do not write one over a table with a secret column.
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
//
// "Everything the role holds" is read from the catalog, not from the
// declaration, so a privilege the declaration never named converges too:
//
//   - schema, table, view, sequence and routine privileges in every declared
//     schema, and column privileges column by column (a table-level REVOKE
//     cascades to them in Postgres, but the script does not rely on that);
//   - ALTER DEFAULT PRIVILEGES entries that name the role, in any schema, which
//     would otherwise hand it every object created later;
//   - a leftover verification: a privilege the migrating role cannot revoke (it
//     was granted by another role, or sits on an object the migrator does not
//     own) fails the run instead of standing behind a converged-looking script.
//
// What a script about ONE role cannot converge, and does not try to:
//
//   - privileges PUBLIC holds, which every role inherits (Postgres grants
//     EXECUTE on new functions to PUBLIC by default, which is how a SECURITY
//     DEFINER function becomes a way around the column grants, above);
//   - the role's memberships in other roles (pg_read_all_data, an application
//     role): revoking one needs ADMIN OPTION on it, and the ManagedDatabase
//     render declares none (pkg/deploy);
//   - database-level privileges (CONNECT, TEMP, CREATE) and row-level
//     security policies that name the role;
//   - objects in a schema the declaration does not name.
//
// What the role is never given: INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES,
// TRIGGER, any sequence privilege, any view (a view can expose a secret column
// of its base table, and the catalog cannot say which), and any table-level
// SELECT — a table-level SELECT would cover columns added later, including
// secret ones.
//
// The dashboard role, [DashboardRoleName], is a different contract: forge
// renders no grants for it. The project grants it with ordinary migrations,
// and this package only makes sure the role exists before they run and that it
// holds nothing but read privileges after (dashboard.go).
package dbgrants

import (
	"context"
	"database/sql"
	"encoding/json"
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
	// GrantsPath is the generated grants script, relative to the project root.
	GrantsPath = "db/grants/readonly_gen.sql"
	// GrantsRowsDir holds the script's per-table rows, one [FileName] each,
	// relative to the project root (see [Files]).
	GrantsRowsDir = "db/grants/readonly"

	// DashboardRoleName is the dashboard Postgres role: the role live
	// dashboard panels query as (ManagedDatabase.dashboards in KCL). What it
	// may read is whatever the project's own migrations GRANT it; see
	// dashboard.go.
	DashboardRoleName = "forge_dash"
	// DashboardSecretSuffix is appended to a cluster name to name the Secret
	// that holds [DashboardRoleName]'s password.
	DashboardSecretSuffix = "-forge-dash"
)

// SecretName is the kubernetes.io/basic-auth Secret holding the role's
// credentials for the ManagedDatabase called cluster, in the cluster's
// namespace. It is a contract with CNPG's managed-role reconciler (the role's
// passwordSecret) and with whoever connects as the role.
func SecretName(cluster string) string { return cluster + SecretSuffix }

// DashboardSecretName is the kubernetes.io/basic-auth Secret holding the
// dashboard role's credentials for the ManagedDatabase called cluster, in the
// cluster's namespace. Same contract as [SecretName], for [DashboardRoleName].
func DashboardSecretName(cluster string) string { return cluster + DashboardSecretSuffix }

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
//
// The script is [Assemble] over [RenderFiles]: what a project commits is the
// files, and what the migrator runs is this.
func Render(tables []Table) (string, error) { return RenderFor(RoleName, tables) }

// RenderFor is Render for a named role. Production has exactly one role,
// [RoleName]; the parameter exists because roles are cluster-wide, so tests
// that share a Postgres server cannot all create forge_ro.
func RenderFor(role string, tables []Table) (string, error) {
	return assembled(RenderFilesFor(role, tables))
}

func assembled(f Files, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return Assemble(f.Main, f.Rows())
}

// # One file per table
//
// A project commits the grants as one CONSTANT script per role (Files.Main,
// db/grants/<role>_gen.sql) and one ROW file per table
// (db/grants/<role>/<schema>.<table>_gen.sql): the table's declaration, a
// VALUES row of (schema, table, readable columns), with its withheld columns
// noted beside it. [Assemble] splices the rows into the script, so what runs
// is still one statement, atomic as before.
//
// WHY FILES, NOT ROWS IN ONE FILE. Tables are born and dropped independently,
// one migration each, and two branches that each add a table that sorts
// between the same two neighbours insert at one point of any single file:
// line merging cannot order them, so git conflicted however the file was
// divided (measured by TestE2EMergeProperty). As files they are two new paths,
// which merge, and a dropped table is a deleted path.

// Files is one role's grants as a project commits them.
type Files struct {
	// Main is the role's convergence script, with [rowsPlaceholder] where the
	// rows go. It depends on nothing but the role, so it never changes with
	// the schema.
	Main string
	// Tables are the per-table rows, in [FileName] order.
	Tables []TableFile
}

// TableFile is one table's row.
type TableFile struct {
	Schema, Name string
	// Row is the VALUES row, then a comment line per kind of withheld column.
	Row string
}

// Rows are the files' rows in order.
func (f Files) Rows() []string {
	rows := make([]string, len(f.Tables))
	for i, t := range f.Tables {
		rows[i] = t.Row
	}
	return rows
}

// FileName is a table's row file name within its role's directory:
// <schema>.<table>_gen.sql, with any byte outside [A-Za-z0-9_-] of either
// name written %XX so the name is one path segment on every filesystem.
func FileName(schema, table string) string {
	return escapeFileName(schema) + "." + escapeFileName(table) + "_gen.sql"
}

func escapeFileName(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// rowsPlaceholder is the line of [Files.Main] that [Assemble] replaces with the
// rows. It sits inside a VALUES list, after a constant first row, so every row
// opens with its own comma.
const rowsPlaceholder = "    -- forge:rows\n"

// Assemble is the script the migrator runs: main with rows spliced in, in
// order. A main with no placeholder is a script rendered before the rows had
// files of their own: it is returned whole when there are no rows (so a binary
// built from a project not yet regenerated applies what it embeds), and
// refused when there are.
func Assemble(main string, rows []string) (string, error) {
	n := strings.Count(main, rowsPlaceholder)
	if n == 0 {
		if len(rows) > 0 {
			return "", errors.New("dbgrants: the grants script has no place for its per-table rows: it predates them; run `forge generate`")
		}
		return main, nil
	}
	var b strings.Builder
	for _, row := range rows {
		for _, line := range strings.SplitAfter(strings.TrimRight(row, "\n")+"\n", "\n") {
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "(") {
				line = ", " + line
			}
			b.WriteString("    " + line)
		}
	}
	return strings.ReplaceAll(main, rowsPlaceholder, b.String()), nil
}

// RenderFiles is the grants for tables as a project commits them; see
// [Files]. [Render] is the same declaration assembled.
func RenderFiles(tables []Table) (Files, error) { return RenderFilesFor(RoleName, tables) }

// RenderFilesFor is [RenderFiles] for a named role, as [RenderFor] is
// [Render]'s.
func RenderFilesFor(role string, tables []Table) (Files, error) {
	if err := checkIdent(role); err != nil {
		return Files{}, err
	}
	bySchema := map[string][]Table{}
	for _, t := range tables {
		if t.Schema == "" || t.Name == "" {
			return Files{}, errors.New("dbgrants: a table needs a schema and a name")
		}
		if err := checkIdent(t.Schema); err != nil {
			return Files{}, err
		}
		if err := checkIdent(t.Name); err != nil {
			return Files{}, err
		}
		for _, c := range t.Columns {
			if err := checkIdent(c.Name); err != nil {
				return Files{}, err
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
	b.WriteString("DECLARE\n  r record;\n  readable text;\n  leftover text;\n  declared text[] := ARRAY[]::text[];\n  role_oid oid;\nBEGIN\n")
	fmt.Fprintf(&b, "  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = %s) THEN\n", quoteLiteral(role))
	fmt.Fprintf(&b, "    RAISE NOTICE %s;\n    RETURN;\n  END IF;\n\n", quoteLiteral("role "+role+" does not exist: read-only grants not applied"))

	fmt.Fprintf(&b, "  SELECT oid INTO role_oid FROM pg_roles WHERE rolname = %s;\n\n", quoteLiteral(role))

	// Everything the role holds, in every declared schema, goes first. A column
	// that became secret, a table that was dropped from the declaration and a
	// table no declaration has met yet are all covered by this, which is why it
	// precedes the grants rather than being a diff against them.
	// The declared schemas are the schemas of the declared tables: the rows,
	// spliced here as well as below, so no list of schemas is kept apart from
	// them.
	b.WriteString("  -- schemas: where the role may look (the schemas of the tables below)\n")
	b.WriteString("  FOR r IN SELECT DISTINCT g.schema FROM (VALUES\n")
	b.WriteString("    (NULL::text, NULL::text, ARRAY[]::text[])\n")
	b.WriteString(rowsPlaceholder)
	b.WriteString("  ) AS g(schema, tbl, cols) WHERE g.schema IS NOT NULL AND EXISTS (SELECT 1 FROM pg_namespace n WHERE n.nspname = g.schema) ORDER BY 1 LOOP\n")
	b.WriteString("    declared := declared || r.schema;\n")
	fmt.Fprintf(&b, "    EXECUTE format('REVOKE ALL ON SCHEMA %%I FROM %%I', r.schema, %s);\n", quoteLiteral(role))
	fmt.Fprintf(&b, "    EXECUTE format('GRANT USAGE ON SCHEMA %%I TO %%I', r.schema, %s);\n", quoteLiteral(role))
	fmt.Fprintf(&b, "    EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA %%I FROM %%I', r.schema, %s);\n", quoteLiteral(role))
	fmt.Fprintf(&b, "    EXECUTE format('REVOKE ALL ON ALL SEQUENCES IN SCHEMA %%I FROM %%I', r.schema, %s);\n", quoteLiteral(role))
	b.WriteString("  END LOOP;\n\n")

	// Routines are revoked from the catalog's own list rather than with ALL
	// FUNCTIONS IN SCHEMA, which also visits functions the migrating role does
	// not own and holds nothing on (an extension's), and fails on those.
	b.WriteString("  -- routines: every function, procedure and aggregate the role was granted EXECUTE on\n")
	b.WriteString("  FOR r IN SELECT n.nspname AS schema, p.oid::regprocedure::text AS proc, p.prokind AS kind\n")
	b.WriteString("             FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace\n")
	b.WriteString("            WHERE n.nspname = ANY (declared) AND p.proacl IS NOT NULL\n")
	b.WriteString("              AND EXISTS (SELECT 1 FROM aclexplode(p.proacl) x WHERE x.grantee = role_oid)\n")
	b.WriteString("            ORDER BY 1, 2 LOOP\n")
	fmt.Fprintf(&b, "    EXECUTE format('REVOKE ALL ON %%s %%s FROM %%I', CASE r.kind WHEN 'p' THEN 'PROCEDURE' ELSE 'FUNCTION' END, r.proc, %s);\n", quoteLiteral(role))
	b.WriteString("  END LOOP;\n\n")

	// A table-level REVOKE already removes the column privileges recorded on
	// the table's columns (Postgres documents it), so this sweep is not what
	// makes the columns converge. It makes that independent of the cascade: it
	// reads the privileges the catalog actually records, column by column, and
	// revokes each one, so the result cannot depend on which tables the
	// statement above reached or on how a server version cascades.
	b.WriteString("  -- columns: every column privilege the catalog records for the role, in the declared schemas\n")
	b.WriteString("  FOR r IN SELECT n.nspname AS schema, c.relname AS tbl, a.attname AS col\n")
	b.WriteString("             FROM pg_attribute a\n")
	b.WriteString("             JOIN pg_class c ON c.oid = a.attrelid\n")
	b.WriteString("             JOIN pg_namespace n ON n.oid = c.relnamespace\n")
	b.WriteString("            WHERE a.attnum > 0 AND NOT a.attisdropped AND a.attacl IS NOT NULL\n")
	b.WriteString("              AND n.nspname = ANY (declared)\n")
	b.WriteString("              AND EXISTS (SELECT 1 FROM aclexplode(a.attacl) x WHERE x.grantee = role_oid)\n")
	b.WriteString("            ORDER BY 1, 2, 3 LOOP\n")
	fmt.Fprintf(&b, "    EXECUTE format('REVOKE ALL (%%I) ON TABLE %%I.%%I FROM %%I', r.col, r.schema, r.tbl, %s);\n", quoteLiteral(role))
	b.WriteString("  END LOOP;\n\n")

	// Default privileges name the role for objects that do not exist yet, so a
	// table created after this run would be readable the moment it appears.
	// They are per creating role and per schema (or global), and none of the
	// statements above touches them. Every entry that names the role goes, in
	// any schema, because a default is not scoped to the declared schemas.
	// Changing another role's defaults needs membership in that role; when the
	// migrating role has none the run fails rather than leaving the boundary
	// open.
	b.WriteString("  -- default privileges: what the role would be given on objects created later\n")
	b.WriteString("  FOR r IN SELECT pg_get_userbyid(d.defaclrole) AS creator, n.nspname AS schema, d.defaclobjtype AS kind\n")
	b.WriteString("             FROM pg_default_acl d\n")
	b.WriteString("             LEFT JOIN pg_namespace n ON n.oid = d.defaclnamespace\n")
	b.WriteString("            WHERE EXISTS (SELECT 1 FROM aclexplode(d.defaclacl) x WHERE x.grantee = role_oid)\n")
	b.WriteString("            ORDER BY 1, 2, 3 LOOP\n")
	b.WriteString("    BEGIN\n")
	fmt.Fprintf(&b, "      EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %%I %%s REVOKE ALL ON %%s FROM %%I',\n")
	b.WriteString("                     r.creator,\n")
	b.WriteString("                     CASE WHEN r.schema IS NULL THEN '' ELSE format('IN SCHEMA %I', r.schema) END,\n")
	b.WriteString("                     CASE r.kind WHEN 'r' THEN 'TABLES' WHEN 'S' THEN 'SEQUENCES' WHEN 'f' THEN 'FUNCTIONS' WHEN 'T' THEN 'TYPES' WHEN 'n' THEN 'SCHEMAS' WHEN 'L' THEN 'LARGE OBJECTS' END,\n")
	fmt.Fprintf(&b, "                     %s);\n", quoteLiteral(role))
	b.WriteString("    EXCEPTION WHEN insufficient_privilege THEN\n")
	fmt.Fprintf(&b, "      RAISE EXCEPTION 'cannot revoke the default privileges of role %% (schema %%) that name %%: the migrating role is not a member of it', r.creator, coalesce(r.schema, '<all>'), %s\n", quoteLiteral(role))
	b.WriteString("        USING ERRCODE = 'insufficient_privilege';\n")
	b.WriteString("    END;\n")
	b.WriteString("  END LOOP;\n\n")

	// A REVOKE issued by the migrating role removes only the privileges
	// recorded as granted by it (or by a role it is a member of). One recorded
	// under another grantor (a role holding a grant option), or on an object
	// the migrating role does not own, survives with at most a WARNING. The
	// boundary must not look converged while such a privilege stands, so what
	// is left fails the run, naming the object.
	b.WriteString("  -- verify: nothing the role was given before this run may still stand\n")
	b.WriteString("  SELECT string_agg(o.what, ', ' ORDER BY o.what) INTO leftover FROM (\n")
	b.WriteString("    SELECT format('schema %s (%s)', n.nspname, x.privilege_type) AS what\n")
	b.WriteString("      FROM pg_namespace n, aclexplode(n.nspacl) x\n")
	b.WriteString("     WHERE n.nspname = ANY (declared) AND x.grantee = role_oid AND x.privilege_type <> 'USAGE'\n")
	b.WriteString("    UNION ALL\n")
	b.WriteString("    SELECT format('%s.%s', n.nspname, c.relname)\n")
	b.WriteString("      FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace\n")
	b.WriteString("     WHERE n.nspname = ANY (declared) AND c.relkind IN ('r','p','v','m','f','S') AND c.relacl IS NOT NULL\n")
	b.WriteString("       AND EXISTS (SELECT 1 FROM aclexplode(c.relacl) x WHERE x.grantee = role_oid)\n")
	b.WriteString("    UNION ALL\n")
	b.WriteString("    SELECT format('%s.%s.%s', n.nspname, c.relname, a.attname)\n")
	b.WriteString("      FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace\n")
	b.WriteString("     WHERE n.nspname = ANY (declared) AND a.attnum > 0 AND NOT a.attisdropped AND a.attacl IS NOT NULL\n")
	b.WriteString("       AND EXISTS (SELECT 1 FROM aclexplode(a.attacl) x WHERE x.grantee = role_oid)\n")
	b.WriteString("    UNION ALL\n")
	b.WriteString("    SELECT format('%s.%s()', n.nspname, p.proname)\n")
	b.WriteString("      FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace\n")
	b.WriteString("     WHERE n.nspname = ANY (declared) AND p.proacl IS NOT NULL\n")
	b.WriteString("       AND EXISTS (SELECT 1 FROM aclexplode(p.proacl) x WHERE x.grantee = role_oid)\n")
	b.WriteString("  ) o;\n")
	b.WriteString("  IF leftover IS NOT NULL THEN\n")
	fmt.Fprintf(&b, "    RAISE EXCEPTION 'privileges of role %% could not be revoked (granted by another role, or on an object the migrating role does not own): %%', %s, leftover\n", quoteLiteral(role))
	b.WriteString("      USING ERRCODE = 'insufficient_privilege';\n")
	b.WriteString("  END IF;\n\n")

	b.WriteString("  -- tables: the columns the role may read. A forge:secret column, and a credential-shaped one nobody decided, is not listed.\n")
	b.WriteString("  -- One row per table, each from its own file (db/grants/<role>/<schema>.<table>_gen.sql; see pkg/dbgrants.Files).\n")
	b.WriteString("  FOR r IN SELECT g.* FROM (VALUES\n")
	// The first row is a constant the loop skips, so every table's row can
	// open with its own comma.
	b.WriteString("    (NULL::text, NULL::text, ARRAY[]::text[])\n")
	b.WriteString(rowsPlaceholder)
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

	out := Files{Main: b.String()}
	for _, schema := range schemas {
		for _, t := range bySchema[schema] {
			out.Tables = append(out.Tables, TableFile{Schema: schema, Name: t.Name, Row: tableRow(schema, t)})
		}
	}
	// In file-name order: the order the migrator reads the files in, so
	// [Render] and an applied project splice the same script.
	sort.Slice(out.Tables, func(i, j int) bool {
		return FileName(out.Tables[i].Schema, out.Tables[i].Name) < FileName(out.Tables[j].Schema, out.Tables[j].Name)
	})
	return out, nil
}

// tableRow is t's VALUES row: (schema, table, readable columns), then one
// comment line per kind of withheld column.
func tableRow(schema string, t Table) string {
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
	var b strings.Builder
	fmt.Fprintf(&b, "(%s, %s, %s)\n", quoteLiteral(schema), quoteLiteral(t.Name), arr)
	if len(secret) > 0 {
		b.WriteString("--   " + strings.ReplaceAll(schema+"."+t.Name+": "+strings.Join(secret, ", "), "\n", " ") + "  (forge:secret, withheld)\n")
	}
	if len(undecided) > 0 {
		b.WriteString("--   " + strings.ReplaceAll(schema+"."+t.Name+": "+strings.Join(undecided, ", "), "\n", " ") +
			"  (credential-shaped columns nobody decided: withheld until a migration declares COMMENT ON COLUMN t.c IS 'forge:secret' or 'forge:readable <reason>')\n")
	}
	return b.String()
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

// RoleLogin reports whether the cluster has a role called name and whether it
// can log in. The dashboard role is rendered NOLOGIN while dashboards are off
// (pkg/deploy), which is how the migrator, which never sees the KCL, learns the
// role is switched off.
func RoleLogin(ctx context.Context, c Conn, name string) (exists, canLogin bool, err error) {
	err = c.QueryRowContext(ctx, `SELECT rolcanlogin FROM pg_roles WHERE rolname = $1`, name).Scan(&canLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("dbgrants: look up role %s: %w", name, err)
	}
	return true, canLogin, nil
}

// SecurityDefinerExposure lists the SECURITY DEFINER functions role can execute
// in a schema it can see: the one way a column the role has no SELECT on is
// still readable, because the function runs as its owner. It is a WARNING for
// the caller to log, not a failure: forge generates no such function, and
// revoking PUBLIC's default EXECUTE would change the application's own
// semantics. Names come back as regprocedure text, already quoted.
func SecurityDefinerExposure(ctx context.Context, c Conn, role string) ([]string, error) {
	var raw []byte
	err := c.QueryRowContext(ctx, `
SELECT coalesce(json_agg(f.name ORDER BY f.name), '[]'::json) FROM (
  SELECT p.oid::regprocedure::text AS name
    FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
   WHERE p.prosecdef
     AND n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg\_toast%'
     AND has_schema_privilege($1::name, n.oid, 'USAGE')
     AND has_function_privilege($1::name, p.oid, 'EXECUTE')
) f`, role).Scan(&raw)
	if err != nil {
		return nil, fmt.Errorf("dbgrants: look up security definer functions for %s: %w", role, err)
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("dbgrants: decode security definer functions: %w", err)
	}
	return out, nil
}

// Apply runs script, a [Render] result, on c. The script is one statement, so
// it is atomic: a failure leaves the previous grants in place.
func Apply(ctx context.Context, c Conn, script string) error {
	if _, err := c.ExecContext(ctx, script); err != nil {
		return fmt.Errorf("dbgrants: apply read-only grants: %w", err)
	}
	return nil
}
