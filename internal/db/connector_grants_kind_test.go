// Copyright (c) 2025 Reliant Labs
package db

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"
)

// TestConnectorGrantKindChecksAreValidatedAndEnforced proves, against a
// database, what 20261114055948_connector_grant_kind and its
// 20261114060001_connector_grant_kind_validate leave behind. The first adds
// connector_grants' kind CHECKs NOT VALID (so its ACCESS EXCLUSIVE lock never
// covers a scan), the second validates them: after the chain each CHECK must be
// validated, so it holds every existing row and not only new writes, and each
// must still refuse the rows it exists to refuse.
func TestConnectorGrantKindChecksAreValidatedAndEnforced(t *testing.T) {
	testkit.Slow(t, "a DB-backed test with several round trips to pgtest's Postgres")
	t.Parallel()

	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	for _, name := range []string{
		"connector_grants_kind_valid",
		"connector_grants_forge_org",
		"connector_grants_project_names_array",
		"connector_grants_daemon_by_kind",
		"connector_grants_path_root_not_empty",
	} {
		var validated bool
		require.NoError(t, raw.QueryRow(
			`SELECT convalidated FROM pg_constraint
			  WHERE conname = $1 AND conrelid = 'connector_grants'::regclass`, name).Scan(&validated),
			"%s is missing after the migration chain", name)
		require.True(t, validated, "%s is NOT VALID after the migration chain: its VALIDATE migration did not run", name)
	}

	insert := func(id, kind, orgID, projectNames string, daemonID *string) error {
		_, err := raw.Exec(
			`INSERT INTO connector_grants (id, user_id, daemon_id, name, token_prefix, allowed_tools, path_root, kind, org_id, project_names)
			 VALUES ($1, 'user-1', $2, 'n', 'rcg_x', '["forge_status"]'::jsonb, '', $3, $4, $5::jsonb)`,
			id, daemonID, kind, orgID, projectNames)
		return err
	}
	require.NoError(t, insert("g-forge", "forge", "org-1", `["shop"]`, nil), "a forge grant: an org, no daemon, no path")

	for name, err := range map[string]error{
		"connector_grants_kind_valid":          insert("g-kind", "other", "org-1", `[]`, nil),
		"connector_grants_forge_org":           insert("g-org", "forge", " ", `[]`, nil),
		"connector_grants_project_names_array": insert("g-names", "forge", "org-1", `{"shop": true}`, nil),
		"connector_grants_daemon_by_kind":      insert("g-daemon", "workspace", "", `[]`, nil),
	} {
		require.Error(t, err, "%s must refuse its row", name)
		require.Contains(t, err.Error(), name)
	}
}
