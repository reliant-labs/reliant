// Copyright (c) 2025 Reliant Labs

package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// Live dashboard panels query as forge_dash, which reads exactly the columns
// db/migrations/*_dashboard_grants.up.sql grants (forge db/dashboards). These
// tests pin that allowlist and prove it by logging in.

// dashboardColumns is every column forge_dash may read. A panel that needs a
// new column adds a GRANT migration AND a row here; never PII, free text or a
// secret.
var dashboardColumns = map[string][]string{
	"chats":                {"created_at", "id", "last_active", "project_id", "state", "user_id"},
	"llm_call_usage":       {"attempt", "auth_mode", "cache_read_tokens", "cache_write_tokens", "chat_id", "created_at", "error_class", "input_tokens", "latency_ms", "model", "org_id", "outcome", "output_tokens", "provider", "purpose", "user_id"},
	"llm_call_usage_daily": {"auth_mode", "cache_read_tokens", "cache_write_tokens", "calls", "day", "error_calls", "input_tokens", "latency_ms_total", "model", "org_id", "output_tokens", "provider", "purpose", "updated_at", "user_id"},
	"messages":             {"chat_id", "cost", "created_at", "model", "provider_account_id", "role", "token_count"},
	"provider_accounts":    {"auth_kind", "id", "provider", "user_id"},
}

// TestDashboardRoleHoldsExactlyTheGrantedColumns reads the catalog: column-level
// SELECT on the allowlist above, and no table-level privilege of any kind (a
// whole-table grant would cover every column the table ever gains).
func TestDashboardRoleHoldsExactlyTheGrantedColumns(t *testing.T) {
	t.Parallel()
	raw, _ := NewTestDatabase(t)

	rows, err := raw.Query(`
		SELECT table_name, column_name, privilege_type
		  FROM information_schema.column_privileges
		 WHERE grantee = 'forge_dash'
		 ORDER BY table_name, column_name`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	got := map[string][]string{}
	for rows.Next() {
		var tbl, col, priv string
		require.NoError(t, rows.Scan(&tbl, &col, &priv))
		require.Equal(t, "SELECT", priv, "forge_dash holds %s on %s.%s: it may only read", priv, tbl, col)
		got[tbl] = append(got[tbl], col)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, dashboardColumns, got, "forge_dash's column grants differ from the allowlist")

	var tableGrants []string
	tr, err := raw.Query(`
		SELECT table_name || ':' || privilege_type
		  FROM information_schema.role_table_grants
		 WHERE grantee = 'forge_dash'`)
	require.NoError(t, err)
	defer func() { _ = tr.Close() }()
	for tr.Next() {
		var g string
		require.NoError(t, tr.Scan(&g))
		tableGrants = append(tableGrants, g)
	}
	require.NoError(t, tr.Err())
	require.Empty(t, tableGrants, "forge_dash holds table-level privileges; grant column lists only")
}

// TestDashboardRoleLoginReadsGrantedColumnsOnly logs in with forge_dash's
// privileges and runs what a panel would: the granted columns read, and
// everything else is refused by the server.
func TestDashboardRoleLoginReadsGrantedColumnsOnly(t *testing.T) {
	t.Parallel()
	_, dsn := NewTestDatabase(t)
	dash := dashboardLogin(t, dsn)

	for _, q := range []string{
		// The spend panel's shape: messages joined to their chat and provider account.
		`SELECT m.model, sum(m.cost), sum(m.token_count), c.user_id, c.project_id, pa.provider, pa.auth_kind
		   FROM messages m
		   JOIN chats c ON c.id = m.chat_id
		   LEFT JOIN provider_accounts pa ON pa.id = m.provider_account_id
		  WHERE m.created_at > now() - interval '30 days' AND m.role IS NOT NULL
		  GROUP BY 1, 4, 5, 6, 7`,
		`SELECT state, count(*), max(last_active), min(created_at) FROM chats GROUP BY state`,
		`SELECT provider, model, auth_mode, purpose, outcome, error_class, sum(input_tokens + output_tokens + cache_read_tokens + cache_write_tokens), sum(latency_ms), max(attempt)
		   FROM llm_call_usage WHERE created_at > now() - interval '1 day' GROUP BY 1, 2, 3, 4, 5, 6`,
		`SELECT day, user_id, org_id, sum(calls), sum(error_calls), sum(latency_ms_total), max(updated_at) FROM llm_call_usage_daily GROUP BY 1, 2, 3`,
	} {
		_, err := dash.Exec(q)
		require.NoError(t, err, "forge_dash cannot run a query over granted columns:\n%s", q)
	}

	for _, q := range []string{
		`SELECT title FROM chats`,
		`SELECT * FROM chats`,
		`SELECT email FROM provider_accounts`,
		`SELECT label, key_hint, organization_name FROM provider_accounts`,
		`SELECT secret_sealed FROM provider_accounts`,
		`SELECT id FROM messages`,
		`SELECT run_id FROM llm_call_usage`,
		`SELECT id FROM projects`,
	} {
		_, err := dash.Exec(q)
		var pgErr *pgconn.PgError
		require.True(t, errors.As(err, &pgErr), "forge_dash ran %q (err=%v); want permission denied", q, err)
		require.Equal(t, "42501", pgErr.Code, "%q: want insufficient_privilege, got %v", q, err)
	}
}

// dashboardLogin returns a pool that logs in with exactly forge_dash's
// privileges. forge_dash is a cluster role, shared by every test process on
// pgtest's server, so the test does not give it a login of its own: it creates
// a throwaway login role that is a member of forge_dash (inheriting its grants
// and nothing else) and drops it at the end.
func dashboardLogin(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	admin := openTestDSN(t, dsn)
	suffix := make([]byte, 4)
	_, err := rand.Read(suffix)
	require.NoError(t, err)
	role := fmt.Sprintf("dash_login_t%d_%s", os.Getpid(), hex.EncodeToString(suffix))
	const pw = "dash-login-pw"
	_, err = admin.Exec(fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s' INHERIT IN ROLE forge_dash`, role, pw))
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.ExecContext(context.Background(), `DROP ROLE IF EXISTS `+role) })

	u, err := url.Parse(dsn)
	require.NoError(t, err)
	u.User = url.UserPassword(role, pw)
	dash := openTestDSN(t, u.String())
	var who string
	require.NoError(t, dash.QueryRow(`SELECT session_user`).Scan(&who), "log in as the dashboard login role")
	require.Equal(t, role, who)
	return dash
}
