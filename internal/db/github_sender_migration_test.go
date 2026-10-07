// Copyright (c) 2025 Reliant Labs

package db

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// githubSenderNumericIDVersion is 20261007012023_github_sender_numeric_id.sql.
const githubSenderNumericIDVersion = 20261007012023

// TestGitHubSenderMigrationReplacesLogins rewinds a migrated database to
// before trigger.sender.id meant a GitHub user id, plants what the login era
// stored, and runs the real migration: connections get their numeric id from
// the probe's own account id, and every GitHub trigger whose filter named
// senders by login is switched off (its "Only from" clause removed), so none
// is left enabled and silently matching nobody.
func TestGitHubSenderMigrationReplacesLogins(t *testing.T) {
	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	exec := func(stmt string, args ...any) {
		t.Helper()
		_, err := raw.Exec(stmt, args...)
		require.NoError(t, err, stmt)
	}
	exec(fmt.Sprintf(`DELETE FROM %s WHERE version_id = %d`, goose.TableName(), githubSenderNumericIDVersion))

	conn := `INSERT INTO connections (id, user_id, integration_id, auth_kind, name, status, account_label, external_account_id, sender_id)
		VALUES ($1, $2, $3, 'oauth2', 'n', 'active', $4, $5, $6)`
	exec(conn, "c-gh", "u1", "github", "octocat", "583231", "octocat")
	exec(conn, "c-gh-old", "u2", "github", "hubot", "7", nil) // made before connections recorded a sender
	exec(conn, "c-gh-odd", "u3", "github", "odd", "not-a-number", "odd")
	exec(conn, "c-slack", "u1", "slack", "acme", "T0ACME", "U0HUMAN")

	trig := `INSERT INTO triggers (id, user_id, project_id, name, kind, enabled, workflow, config, filter, workflow_trigger, no_machine)
		VALUES ($1, 'u1', 'test-project', $1, 'integration', true, 'triage', $2, $3, $4, true)`
	github := `{"integration":"github","events":["issues.opened"]}`
	exec(trig, "only-clause", github, `trigger.sender.verified && trigger.sender.id in ["octocat", "hubot"]`, nil)
	exec(trig, "rest-and-clause", github, `(trigger.payload.data.action == "opened") && trigger.sender.verified && trigger.sender.id in ["octocat"]`, nil)
	exec(trig, "hand-written", github, `trigger.sender.id != "dependabot[bot]"`, nil)
	exec(trig, "activation", github, `trigger.sender.verified && trigger.sender.id in ["octocat"]`, "from-team")
	exec(trig, "no-sender", github, `trigger.payload.data.action == "opened"`, nil)
	exec(trig, "slack", `{"integration":"slack","events":["message.channels"]}`, `trigger.sender.verified && trigger.sender.id in ["U0HUMAN"]`, nil)

	require.NoError(t, initGoose())
	require.NoError(t, goose.UpTo(raw, migrationsDir, githubSenderNumericIDVersion, goose.WithAllowMissing()))

	sender := func(id string) *string {
		var s sql.NullString
		require.NoError(t, raw.QueryRow(`SELECT sender_id FROM connections WHERE id = $1`, id).Scan(&s))
		if !s.Valid {
			return nil
		}
		return &s.String
	}
	ptr := func(s string) *string { return &s }
	assert.Equal(t, ptr("583231"), sender("c-gh"), "the login becomes the user id the same probe recorded")
	assert.Equal(t, ptr("7"), sender("c-gh-old"), "a connection with no sender gets one: it is the same person")
	assert.Nil(t, sender("c-gh-odd"), "no numeric id to convert to: cleared, never left as a login")
	assert.Equal(t, ptr("U0HUMAN"), sender("c-slack"), "Slack user ids are already stable")

	type row struct {
		filter  string
		enabled bool
	}
	get := func(id string) row {
		var r row
		require.NoError(t, raw.QueryRow(`SELECT filter, enabled FROM triggers WHERE id = $1`, id).Scan(&r.filter, &r.enabled))
		return r
	}
	assert.Equal(t, row{"", false}, get("only-clause"))
	assert.Equal(t, row{`trigger.payload.data.action == "opened"`, false}, get("rest-and-clause"), "the rest of the filter is kept")
	assert.Equal(t, row{`trigger.sender.id != "dependabot[bot]"`, false}, get("hand-written"), "not a shape to rewrite: left for its owner, off")
	assert.Equal(t, row{`trigger.sender.verified && trigger.sender.id in ["octocat"]`, false}, get("activation"),
		"an activation's filter is its declaration's; it is switched off, not rewritten")
	assert.Equal(t, row{`trigger.payload.data.action == "opened"`, true}, get("no-sender"), "no sender in the filter: untouched")
	assert.Equal(t, row{`trigger.sender.verified && trigger.sender.id in ["U0HUMAN"]`, true}, get("slack"), "other integrations: untouched")
}
