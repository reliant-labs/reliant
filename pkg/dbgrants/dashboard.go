package dbgrants

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// # The dashboard role
//
// [DashboardRoleName] is the role live dashboard panels query as. forge owns
// its LIFECYCLE and nothing else:
//
//   - The role exists in every cluster a forge migrator touches. Roles are
//     cluster-wide: on a server shared by several databases there is one
//     forge_dash, and its login reaches the grants of every database on it.
//     The migrator creates it NOLOGIN before any migration runs
//     ([EnsureRole]), so a migration may name it in dev, in tests, in CI and
//     in production, whether dashboards are on or not. A role that already
//     exists is adopted as it is, whatever its login and password.
//   - The deploy decides whether it can log in. A ManagedDatabase renders it as
//     a CNPG managed role (pkg/deploy): LOGIN, a password Secret and a
//     connection limit while ManagedDatabase.dashboards is enabled, NOLOGIN
//     with no password while it is not. CNPG adopts a role that already
//     exists: its reconciler ALTERs a declared role it finds in the catalog to
//     the declared attributes instead of creating it.
//   - What it may READ is the project's: ordinary migrations grant it, column
//     by column (`GRANT SELECT (id, created_at, cost) ON messages TO
//     forge_dash;`), and a later migration revokes. forge renders no grants for
//     it and converges none.
//
// Switching dashboards off therefore leaves the grants in place on a role that
// cannot log in, so they are unusable until it can again.
//
// The one check forge keeps costs no annotation: a read-only role can only
// read. [DashboardPrivilegeViolations] lists what it can do besides, and the
// migrator refuses to finish while the list is not empty (pkg/migratekit).

// ErrRoleNotCreatable means the role does not exist and the connecting role may
// not create it (it has neither CREATEROLE nor superuser). A ManagedDatabase's
// application role is such a role: there the role comes from CNPG's managed
// roles, which can reconcile after the first migration run starts.
var ErrRoleNotCreatable = errors.New("role does not exist and the connecting role may not create it")

// EnsureDashboardRole is [EnsureRole] for [DashboardRoleName].
func EnsureDashboardRole(ctx context.Context, c Conn) (created bool, err error) {
	return EnsureRole(ctx, c, DashboardRoleName)
}

// EnsureRole creates the role called name, NOLOGIN NOINHERIT with no password,
// when the cluster has none, and reports whether it did. An existing role is
// left exactly as it is: its LOGIN, password and connection limit belong to
// whoever declared it (CNPG), and so do its grants.
//
// Roles are cluster-wide, so two migrators of different databases on one
// server can race to create it; the loser finds it created and reports false.
// A connecting role that may not create roles gets an error wrapping
// [ErrRoleNotCreatable].
func EnsureRole(ctx context.Context, c Conn, name string) (created bool, err error) {
	if err := checkIdent(name); err != nil {
		return false, err
	}
	exists, err := RoleExists(ctx, c, name)
	if err != nil || exists {
		return false, err
	}
	// NOINHERIT matches the managed-role declaration (pkg/deploy), so CNPG
	// adopting it changes as little as possible.
	if _, err := c.ExecContext(ctx, "CREATE ROLE "+quoteIdent(name)+" NOLOGIN NOINHERIT"); err != nil {
		switch sqlState(err) {
		case "42710", "23505": // duplicate_object, unique_violation: another session created it first
			return false, nil
		case "42501": // insufficient_privilege
			return false, fmt.Errorf("dbgrants: %s: %w", name, ErrRoleNotCreatable)
		}
		return false, fmt.Errorf("dbgrants: create role %s: %w", name, err)
	}
	return true, nil
}

// sqlState is the SQLSTATE of a driver error, "" when it carries none. pgx's
// *pgconn.PgError and lib/pq's *pq.Error both implement SQLState().
func sqlState(err error) string {
	var s interface{ SQLState() string }
	if errors.As(err, &s) {
		return s.SQLState()
	}
	return ""
}

// Violation is one thing the dashboard role can do besides reading.
type Violation struct {
	// What names the object and the privilege: "INSERT on table public.users".
	What string
	// Fix is the statement that takes it away, for the failure message.
	Fix string
}

func (v Violation) String() string { return v.What + " (fix: " + v.Fix + ")" }

// DescribeViolations is the failure message for violations of role: what it
// can do besides reading, each with its fix, and the rule to follow instead.
func DescribeViolations(role string, violations []Violation) string {
	lines := make([]string, len(violations))
	for i, v := range violations {
		lines[i] = "  - " + v.String()
	}
	return fmt.Sprintf("role %s may only read (SELECT, schema USAGE, CONNECT), but it can also:\n%s\n"+
		"Take each away in a new migration, and grant %s column SELECTs only. "+
		"A privilege held through PUBLIC is every role's: revoke it from PUBLIC and grant the roles that need it. "+
		"See `forge skill load db/dashboards`", role, strings.Join(lines, "\n"), role)
}

// DashboardPrivilegeViolations lists what role can do besides reading, sorted:
// nil when nothing, or when the role does not exist.
//
// It asks Postgres for the role's EFFECTIVE privileges (has_table_privilege
// and its siblings), not for ACL entries naming it, so a privilege it holds
// through PUBLIC counts exactly like one granted to it, and ownership, which
// carries every privilege without an ACL entry, is listed on its own. Listed:
//
//   - every object the role owns, in any database and of any kind
//     (pg_shdepend);
//   - INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER (and MAINTAIN from
//     Postgres 17) on any table, view, materialized view or foreign table, and
//     SELECT WITH GRANT OPTION;
//   - SELECT that PUBLIC holds on a table (whole or some columns): every role,
//     this one included, reads those columns whatever the migrations granted;
//   - USAGE or UPDATE on a sequence (nextval, setval);
//   - CREATE on any schema, and CREATE on any database of the cluster;
//   - USAGE on a foreign-data wrapper (CREATE SERVER) or a foreign server
//     (CREATE USER MAPPING);
//   - UPDATE on a large object granted to the role or to PUBLIC, and SELECT
//     on one granted to PUBLIC;
//   - a grant option on anything, read privileges included: the role could
//     hand them on;
//   - SET or ALTER SYSTEM on a parameter (Postgres 15+), granted to the role or
//     to PUBLIC: ALTER SYSTEM SET archive_command runs an OS command;
//   - ALTER DEFAULT PRIVILEGES entries naming the role, and PUBLIC's beyond
//     SELECT and Postgres's own defaults: a table the application creates
//     between runs (a partition) would otherwise be born writable;
//   - membership in any role, and the SUPERUSER, CREATEROLE, CREATEDB,
//     REPLICATION and BYPASSRLS attributes (a superuser is reported by its
//     attribute alone: it holds everything).
//
// Tables, schemas and sequences are those of the database c is connected to,
// outside pg_catalog, information_schema and pg_toast. Not listed, by choice:
// TEMP on a database (PUBLIC's default; a temporary table lives in its own
// session and cannot touch the application's data), EXECUTE on functions
// (PUBLIC's default; a SECURITY DEFINER one is [SecurityDefinerExposure]'s
// warning), and privileges on another database's objects, which that
// database's own migrator checks.
func DashboardPrivilegeViolations(ctx context.Context, c Conn, role string) ([]Violation, error) {
	var version int
	if err := c.QueryRowContext(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&version); err != nil {
		return nil, fmt.Errorf("dbgrants: read the server version: %w", err)
	}
	var raw []byte
	if err := c.QueryRowContext(ctx, violationsSQL(version), role).Scan(&raw); err != nil {
		return nil, fmt.Errorf("dbgrants: read the privileges of %s: %w", role, err)
	}
	var rows []struct {
		Kind   string `json:"kind"`
		Object string `json:"object"`
		Target string `json:"target"`
		Priv   string `json:"priv"`
		Public bool   `json:"public"`
		Whole  bool   `json:"whole"`
		Owner  string `json:"owner"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("dbgrants: decode the privileges of %s: %w", role, err)
	}
	out := make([]Violation, 0, len(rows))
	for _, r := range rows {
		out = append(out, violation(role, r.Kind, r.Object, r.Target, r.Priv, r.Owner, r.Public, r.Whole))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].What < out[j].What })
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// violation words one row of violationsSQL.
func violation(role, kind, object, target, priv, owner string, public, whole bool) Violation {
	from := quoteIdent(role)
	if public {
		from = "PUBLIC"
	}
	revoke := func(p string) string {
		if rest, ok := strings.CutSuffix(p, " WITH GRANT OPTION"); ok {
			return "REVOKE GRANT OPTION FOR " + rest + " ON " + target + " FROM " + from
		}
		return "REVOKE " + p + " ON " + target + " FROM " + from
	}
	held := ""
	if public {
		held = ", held through PUBLIC"
	}
	switch kind {
	case "attribute":
		return Violation{"role attribute " + priv, "ALTER ROLE " + quoteIdent(role) + " NO" + priv}
	case "membership":
		return Violation{"membership in role " + object, "REVOKE " + object + " FROM " + quoteIdent(role)}
	case "owner":
		return Violation{"owns " + object, "give it to the role that should own it (ALTER ... OWNER TO ...), or drop it"}
	case "public-select":
		what := "SELECT on some columns of " + object + " is granted to PUBLIC, so " + role + " reads them whatever the migrations granted"
		if whole {
			what = "SELECT on " + object + " is granted to PUBLIC, so " + role + " reads all of it"
		}
		return Violation{what, "REVOKE SELECT ON " + target + " FROM PUBLIC, then grant the roles that need it"}
	case "default":
		to := role
		if public {
			to = "PUBLIC"
		}
		return Violation{object + " grant " + priv + " to " + to, "ALTER DEFAULT PRIVILEGES " + target + " FROM " + from}
	}
	fix := revoke(priv)
	if public && owner != "" {
		// Only the grantor or the owner can revoke what PUBLIC was given;
		// any other role's REVOKE "succeeds" with a WARNING and changes
		// nothing. On Postgres 14 public's owner is the bootstrap superuser.
		fix += ", run as the owner of " + target + " (" + owner + "): any other role's REVOKE only warns \"no privileges could be revoked\" and changes nothing"
	}
	return Violation{priv + " on " + object + held, fix}
}

// violationsSQL is the effective-privilege query for a server_version_num:
// MAINTAIN exists from 17, pg_parameter_acl from 15. Each row is a JSON object
// that [violation] words.
func violationsSQL(version int) string {
	tablePrivs := `'DELETE', 'TRUNCATE', 'TRIGGER'`
	if version >= 170000 {
		tablePrivs += `, 'MAINTAIN'`
	}
	parameters := ""
	if version >= 150000 {
		parameters = `
  UNION ALL
  SELECT json_build_object('kind', 'privilege', 'object', 'parameter ' || p.parname, 'target', 'PARAMETER ' || p.parname,
                           'priv', x.privilege_type, 'public', x.grantee = 0)
    FROM r, pg_parameter_acl p, aclexplode(p.paracl) x
   WHERE NOT r.rolsuper AND x.grantee IN (r.id, 0)`
	}
	return `
WITH r AS (
  SELECT oid AS id, rolsuper, rolcreaterole, rolcreatedb, rolreplication, rolbypassrls FROM pg_roles WHERE rolname = $1
), rels AS (
  SELECT c.oid, c.relkind, c.relowner, c.oid::regclass::text AS name,
         CASE c.relkind WHEN 'S' THEN 'sequence' WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized view'
                        WHEN 'f' THEN 'foreign table' ELSE 'table' END AS kind,
         EXISTS (SELECT 1 FROM pg_depend e WHERE e.classid = 'pg_class'::regclass AND e.objid = c.oid AND e.deptype = 'e') AS extension
    FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
     AND n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg\_toast%' AND n.nspname NOT LIKE 'pg\_temp\_%'
), schemas AS (
  SELECT n.oid, n.nspname, n.nspowner FROM pg_namespace n
   WHERE n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg\_toast%' AND n.nspname NOT LIKE 'pg\_temp\_%'
)
SELECT coalesce(json_agg(v.row), '[]'::json) FROM (
  SELECT json_build_object('kind', 'attribute', 'priv', a.name) AS row
    FROM r, LATERAL (VALUES ('SUPERUSER', r.rolsuper), ('CREATEROLE', r.rolcreaterole), ('CREATEDB', r.rolcreatedb),
                            ('REPLICATION', r.rolreplication), ('BYPASSRLS', r.rolbypassrls)) AS a(name, held)
   WHERE a.held
  UNION ALL
  SELECT json_build_object('kind', 'membership', 'object', quote_ident(pg_get_userbyid(m.roleid)))
    FROM r JOIN pg_auth_members m ON m.member = r.id
  UNION ALL
  SELECT json_build_object('kind', 'owner', 'object',
           CASE WHEN d.dbid = 0 OR db.datname = current_database() THEN pg_describe_object(d.classid, d.objid, d.objsubid)
                ELSE format('an object (%s) in database %I', d.classid::regclass, db.datname) END)
    FROM r JOIN pg_shdepend d ON d.refclassid = 'pg_authid'::regclass AND d.refobjid = r.id AND d.deptype = 'o'
    LEFT JOIN pg_database db ON db.oid = d.dbid
  UNION ALL
  SELECT json_build_object('kind', 'privilege', 'object', t.kind || ' ' || t.name, 'target', 'TABLE ' || t.name,
                           'priv', p.priv, 'public', has_table_privilege('public', t.oid, p.priv))
    FROM r, rels t, unnest(ARRAY[` + tablePrivs + `]) AS p(priv)
   WHERE NOT r.rolsuper AND t.relkind <> 'S' AND t.relowner <> r.id AND has_table_privilege(r.id, t.oid, p.priv)
  UNION ALL
  SELECT json_build_object('kind', 'privilege', 'object', t.kind || ' ' || t.name, 'target', 'TABLE ' || t.name,
                           'priv', p.priv, 'public', has_any_column_privilege('public', t.oid, p.priv))
    FROM r, rels t, unnest(ARRAY['INSERT', 'UPDATE', 'REFERENCES', 'SELECT WITH GRANT OPTION']) AS p(priv)
   WHERE NOT r.rolsuper AND t.relkind <> 'S' AND t.relowner <> r.id AND has_any_column_privilege(r.id, t.oid, p.priv)
  UNION ALL
  SELECT json_build_object('kind', 'public-select', 'object', t.kind || ' ' || t.name, 'target', 'TABLE ' || t.name,
                           'whole', has_table_privilege('public', t.oid, 'SELECT'))
    FROM r, rels t
   WHERE NOT r.rolsuper AND t.relkind <> 'S' AND NOT t.extension AND t.relowner <> r.id
     AND has_any_column_privilege('public', t.oid, 'SELECT')
  UNION ALL
  SELECT json_build_object('kind', 'privilege', 'object', 'sequence ' || t.name, 'target', 'SEQUENCE ' || t.name,
                           'priv', p.priv, 'public', has_sequence_privilege('public', t.oid, p.priv))
    FROM r, rels t, unnest(ARRAY['USAGE', 'UPDATE', 'SELECT WITH GRANT OPTION']) AS p(priv)
   WHERE NOT r.rolsuper AND t.relkind = 'S' AND t.relowner <> r.id AND has_sequence_privilege(r.id, t.oid, p.priv)
  UNION ALL
  SELECT json_build_object('kind', 'privilege', 'object', 'schema ' || quote_ident(n.nspname), 'target', 'SCHEMA ' || quote_ident(n.nspname),
                           'priv', p.priv, 'public', has_schema_privilege('public', n.oid, p.priv),
                           'owner', quote_ident(pg_get_userbyid(n.nspowner)))
    FROM r, schemas n, unnest(ARRAY['CREATE', 'USAGE WITH GRANT OPTION']) AS p(priv)
   WHERE NOT r.rolsuper AND n.nspowner <> r.id AND has_schema_privilege(r.id, n.oid, p.priv)
  UNION ALL
  SELECT json_build_object('kind', 'privilege', 'object', 'database ' || quote_ident(d.datname), 'target', 'DATABASE ' || quote_ident(d.datname),
                           'priv', p.priv, 'public', has_database_privilege('public', d.oid, p.priv))
    FROM r, pg_database d, unnest(ARRAY['CREATE', 'CONNECT WITH GRANT OPTION', 'TEMP WITH GRANT OPTION']) AS p(priv)
   WHERE NOT r.rolsuper AND d.datdba <> r.id AND has_database_privilege(r.id, d.oid, p.priv)
  UNION ALL
  SELECT json_build_object('kind', 'privilege', 'object', 'large object ' || l.oid, 'target', 'LARGE OBJECT ' || l.oid,
                           'priv', x.privilege_type || CASE WHEN x.is_grantable THEN ' WITH GRANT OPTION' ELSE '' END, 'public', x.grantee = 0)
    FROM r, pg_largeobject_metadata l, aclexplode(l.lomacl) x
   WHERE NOT r.rolsuper AND l.lomowner <> r.id AND x.grantee IN (r.id, 0) AND (x.privilege_type <> 'SELECT' OR x.is_grantable)
  UNION ALL
  SELECT json_build_object('kind', 'public-select', 'object', 'large object ' || l.oid, 'target', 'LARGE OBJECT ' || l.oid, 'whole', true)
    FROM r, pg_largeobject_metadata l, aclexplode(l.lomacl) x
   WHERE NOT r.rolsuper AND l.lomowner <> r.id AND x.grantee = 0 AND x.privilege_type = 'SELECT'
  UNION ALL
  SELECT json_build_object('kind', 'privilege', 'object', 'foreign-data wrapper ' || quote_ident(w.fdwname), 'target', 'FOREIGN DATA WRAPPER ' || quote_ident(w.fdwname),
                           'priv', 'USAGE', 'public', has_foreign_data_wrapper_privilege('public', w.oid, 'USAGE'))
    FROM r, pg_foreign_data_wrapper w
   WHERE NOT r.rolsuper AND w.fdwowner <> r.id AND has_foreign_data_wrapper_privilege(r.id, w.oid, 'USAGE')
  UNION ALL
  SELECT json_build_object('kind', 'privilege', 'object', 'foreign server ' || quote_ident(fs.srvname), 'target', 'FOREIGN SERVER ' || quote_ident(fs.srvname),
                           'priv', 'USAGE', 'public', has_server_privilege('public', fs.oid, 'USAGE'))
    FROM r, pg_foreign_server fs
   WHERE NOT r.rolsuper AND fs.srvowner <> r.id AND has_server_privilege(r.id, fs.oid, 'USAGE')
  UNION ALL
  SELECT json_build_object('kind', 'default', 'public', x.grantee = 0,
           'priv', x.privilege_type || CASE WHEN x.is_grantable THEN ' WITH GRANT OPTION' ELSE '' END,
           'object', format('default privileges of role %I%s on new %s', pg_get_userbyid(d.defaclrole),
                            CASE WHEN d.defaclnamespace = 0 THEN '' ELSE format(' in schema %I', n.nspname) END,
                            CASE d.defaclobjtype WHEN 'r' THEN 'tables' WHEN 'S' THEN 'sequences' WHEN 'f' THEN 'functions'
                                                 WHEN 'T' THEN 'types' WHEN 'n' THEN 'schemas' WHEN 'L' THEN 'large objects' ELSE d.defaclobjtype::text END),
           'target', format('FOR ROLE %I%s REVOKE %s ON %s', pg_get_userbyid(d.defaclrole),
                            CASE WHEN d.defaclnamespace = 0 THEN '' ELSE format(' IN SCHEMA %I', n.nspname) END,
                            CASE WHEN x.is_grantable THEN 'GRANT OPTION FOR ' ELSE '' END || x.privilege_type,
                            CASE d.defaclobjtype WHEN 'r' THEN 'TABLES' WHEN 'S' THEN 'SEQUENCES' WHEN 'f' THEN 'FUNCTIONS'
                                                 WHEN 'T' THEN 'TYPES' WHEN 'n' THEN 'SCHEMAS' WHEN 'L' THEN 'LARGE OBJECTS' ELSE d.defaclobjtype::text END))
    FROM r, pg_default_acl d LEFT JOIN pg_namespace n ON n.oid = d.defaclnamespace, aclexplode(d.defaclacl) x
   -- The role's own defaults are all refused: even SELECT hands it every
   -- future table whole. PUBLIC's are refused beyond reading and Postgres's
   -- own defaults (EXECUTE on functions, USAGE on types and schemas), so a
   -- table the application creates between runs (a partition) is never born
   -- writable by every role.
   WHERE x.grantee = r.id
      OR (x.grantee = 0 AND NOT r.rolsuper
          AND (x.is_grantable OR NOT (x.privilege_type = 'SELECT'
                                      OR (x.privilege_type = 'EXECUTE' AND d.defaclobjtype = 'f')
                                      OR (x.privilege_type = 'USAGE' AND d.defaclobjtype IN ('T', 'n')))))` + parameters + `
) v`
}
