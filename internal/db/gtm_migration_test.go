// Copyright (c) 2026 Reliant Labs
package db

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"
)

// The gtm_* tables (20261126173100_create_gtm) hold their invariants
// themselves: a row links only rows of its own org, a verified claim carries
// its evidence, a sent item is an email with its send record, an email is
// never SCHEDULED or PUBLISHED, and an org holds each claim text once.
func TestMigrate_GtmTablesHoldTheirInvariants(t *testing.T) {
	testkit.Slow(t, "builds a fresh database through every migration")
	t.Parallel()

	_, raw := newMigratedTestDB(t)
	exec := func(q string, args ...any) error {
		_, err := raw.Exec(q, args...)
		return err
	}
	// refused asserts the statement fails on exactly the named constraint.
	refused := func(t *testing.T, constraint, q string, args ...any) {
		t.Helper()
		err := exec(q, args...)
		var pg *pgconn.PgError
		require.True(t, errors.As(err, &pg), "want a postgres error on %s, got %v", constraint, err)
		require.Equal(t, constraint, pg.ConstraintName, "%s", pg.Message)
	}

	require.NoError(t, exec(`INSERT INTO gtm_campaigns (id, org_id, name) VALUES ('c1', 'org-a', 'Launch')`))
	require.NoError(t, exec(`INSERT INTO gtm_ideas (id, org_id, title, created_by, campaign_id) VALUES ('i1', 'org-a', 'Idea', 'u1', 'c1')`))

	t.Run("an empty org is refused", func(t *testing.T) {
		refused(t, "gtm_campaigns_org_id_check", `INSERT INTO gtm_campaigns (id, org_id, name) VALUES ('c0', '', 'Launch')`)
		refused(t, "gtm_ideas_org_id_check", `INSERT INTO gtm_ideas (id, org_id, title, created_by) VALUES ('i0', '', 'Idea', 'u1')`)
		refused(t, "gtm_items_org_id_check", `INSERT INTO gtm_items (id, org_id, title) VALUES ('t0', '', 'Post')`)
		refused(t, "gtm_positioning_org_id_check", `INSERT INTO gtm_positioning (org_id, updated_by) VALUES ('', 'u1')`)
		refused(t, "gtm_claims_org_id_check", `INSERT INTO gtm_claims (id, org_id, text, category) VALUES ('k0', '', 'x', 'GTM_CLAIM_CATEGORY_PRICING')`)
	})

	t.Run("a link to another org's row is refused", func(t *testing.T) {
		refused(t, "gtm_ideas_campaign_same_org_fkey", `INSERT INTO gtm_ideas (id, org_id, title, created_by, campaign_id) VALUES ('i2', 'org-b', 'Idea', 'u1', 'c1')`)
		refused(t, "gtm_items_campaign_same_org_fkey", `INSERT INTO gtm_items (id, org_id, title, campaign_id) VALUES ('t1', 'org-b', 'Post', 'c1')`)
		refused(t, "gtm_items_idea_same_org_fkey", `INSERT INTO gtm_items (id, org_id, title, idea_id) VALUES ('t2', 'org-b', 'Post', 'i1')`)
		require.NoError(t, exec(`INSERT INTO gtm_items (id, org_id, title, campaign_id, idea_id) VALUES ('t3', 'org-a', 'Post', 'c1', 'i1')`))
	})

	t.Run("defaults are the first real enum member and revision 1", func(t *testing.T) {
		var status, kind string
		var revision int
		require.NoError(t, raw.QueryRow(`SELECT status, kind, revision FROM gtm_ideas WHERE id = 'i1'`).Scan(&status, &kind, &revision))
		require.Equal(t, "GTM_IDEA_STATUS_PROPOSED", status)
		require.Equal(t, "GTM_IDEA_KIND_PRODUCT", kind)
		require.Equal(t, 1, revision)
		refused(t, "gtm_ideas_status_check", `UPDATE gtm_ideas SET status = 'GTM_IDEA_STATUS_UNSPECIFIED' WHERE id = 'i1'`)
	})

	t.Run("a sent item is an email with its send record", func(t *testing.T) {
		require.NoError(t, exec(`INSERT INTO gtm_items (id, org_id, title, kind, status) VALUES ('e1', 'org-a', 'Mail', 'GTM_ITEM_KIND_EMAIL', 'GTM_ITEM_STATUS_APPROVED')`))
		refused(t, "gtm_items_sent_has_record", `UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_SENT' WHERE id = 'e1'`)
		refused(t, "gtm_items_sent_is_email", `UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_SENT', gmail_message_id = 'm', sent_at = now(), sent_by = 'u1', approval_hash = 'h' WHERE id = 't3'`)
		require.NoError(t, exec(`UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_SENT', gmail_message_id = 'm', sent_at = now(), sent_by = 'u1', approval_hash = 'h' WHERE id = 'e1'`))
		refused(t, "gtm_items_scheduled_has_date", `UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_SCHEDULED' WHERE id = 't3'`)
	})

	t.Run("an email is never scheduled or published", func(t *testing.T) {
		// A dated, approved email stays APPROVED.
		require.NoError(t, exec(`INSERT INTO gtm_items (id, org_id, title, kind, status, scheduled_at) VALUES ('e2', 'org-a', 'Mail', 'GTM_ITEM_KIND_EMAIL', 'GTM_ITEM_STATUS_APPROVED', now())`))
		refused(t, "gtm_items_email_never_scheduled", `UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_SCHEDULED' WHERE id = 'e2'`)
		refused(t, "gtm_items_email_never_published", `UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_PUBLISHED' WHERE id = 'e2'`)
		// A post may be both.
		require.NoError(t, exec(`UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_SCHEDULED', scheduled_at = now() WHERE id = 't3'`))
		require.NoError(t, exec(`UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_PUBLISHED' WHERE id = 't3'`))
	})

	t.Run("a verified claim carries its evidence", func(t *testing.T) {
		require.NoError(t, exec(`INSERT INTO gtm_claims (id, org_id, text, category) VALUES ('k1', 'org-a', 'Hosting costs $5 a month.', 'GTM_CLAIM_CATEGORY_PRICING')`))
		refused(t, "gtm_claims_verified_has_evidence", `UPDATE gtm_claims SET status = 'GTM_CLAIM_STATUS_VERIFIED' WHERE id = 'k1'`)
		refused(t, "gtm_claims_verified_has_evidence", `UPDATE gtm_claims SET status = 'GTM_CLAIM_STATUS_VERIFIED', source_url = 'https://x', source_kind = 'GTM_CLAIM_SOURCE_KIND_DOCS', verified_at = now() WHERE id = 'k1'`)
		require.NoError(t, exec(`UPDATE gtm_claims SET status = 'GTM_CLAIM_STATUS_VERIFIED', source_url = 'https://x', source_kind = 'GTM_CLAIM_SOURCE_KIND_DOCS', verified_at = now(), verified_by = 'u1' WHERE id = 'k1'`))

		require.NoError(t, exec(`INSERT INTO gtm_claims (id, org_id, text, category) VALUES ('k2', 'org-a', 'Faster than Acme', 'GTM_CLAIM_CATEGORY_COMPARISON')`))
		refused(t, "gtm_claims_comparison_needs_primary_source", `UPDATE gtm_claims SET status = 'GTM_CLAIM_STATUS_VERIFIED', source_url = 'https://x', source_kind = 'GTM_CLAIM_SOURCE_KIND_DOCS', verified_at = now(), verified_by = 'u1' WHERE id = 'k2'`)
	})

	t.Run("an org holds each claim text once", func(t *testing.T) {
		var norm string
		require.NoError(t, raw.QueryRow(`SELECT text_norm FROM gtm_claims WHERE id = 'k1'`).Scan(&norm))
		require.Equal(t, "hosting costs $5 a month", norm)
		refused(t, "gtm_claims_org_id_text_norm_key", `INSERT INTO gtm_claims (id, org_id, text, category) VALUES ('k3', 'org-a', '  hosting   costs $5 a MONTH!', 'GTM_CLAIM_CATEGORY_PRICING')`)
		require.NoError(t, exec(`INSERT INTO gtm_claims (id, org_id, text, category) VALUES ('k4', 'org-b', 'Hosting costs $5 a month.', 'GTM_CLAIM_CATEGORY_PRICING')`))
	})

	t.Run("an org has one positioning row, written by someone", func(t *testing.T) {
		require.NoError(t, exec(`INSERT INTO gtm_positioning (org_id, positioning_md, audiences, updated_by) VALUES ('org-a', 'We help teams ship.', '{founders,platform teams}', 'u1')`))
		var revision int
		var audiences string
		require.NoError(t, raw.QueryRow(`SELECT revision, array_to_string(audiences, '|') FROM gtm_positioning WHERE org_id = 'org-a'`).Scan(&revision, &audiences))
		require.Equal(t, 1, revision)
		require.Equal(t, "founders|platform teams", audiences)
		refused(t, "gtm_positioning_pkey", `INSERT INTO gtm_positioning (org_id, updated_by) VALUES ('org-a', 'u2')`)
		refused(t, "gtm_positioning_updated_by_check", `INSERT INTO gtm_positioning (org_id, updated_by) VALUES ('org-b', '')`)
		refused(t, "gtm_positioning_revision_check", `UPDATE gtm_positioning SET revision = 0 WHERE org_id = 'org-a'`)
	})

	t.Run("deleting an idea unlinks its items and keeps their org", func(t *testing.T) {
		require.NoError(t, exec(`INSERT INTO gtm_ideas (id, org_id, title, created_by) VALUES ('i3', 'org-a', 'Idea', 'u1')`))
		require.NoError(t, exec(`INSERT INTO gtm_items (id, org_id, title, idea_id) VALUES ('t4', 'org-a', 'Post', 'i3')`))
		require.NoError(t, exec(`DELETE FROM gtm_ideas WHERE id = 'i3'`))
		var idea sql.NullString
		var org string
		require.NoError(t, raw.QueryRow(`SELECT idea_id, org_id FROM gtm_items WHERE id = 't4'`).Scan(&idea, &org))
		require.False(t, idea.Valid)
		require.Equal(t, "org-a", org)
	})

	t.Run("deleting a campaign unlinks its rows and keeps their org", func(t *testing.T) {
		require.NoError(t, exec(`DELETE FROM gtm_campaigns WHERE id = 'c1'`))
		var campaign sql.NullString
		var org string
		require.NoError(t, raw.QueryRow(`SELECT campaign_id, org_id FROM gtm_items WHERE id = 't3'`).Scan(&campaign, &org))
		require.False(t, campaign.Valid)
		require.Equal(t, "org-a", org)
		require.NoError(t, raw.QueryRow(`SELECT campaign_id, org_id FROM gtm_ideas WHERE id = 'i1'`).Scan(&campaign, &org))
		require.False(t, campaign.Valid)
		require.Equal(t, "org-a", org)
	})
}
