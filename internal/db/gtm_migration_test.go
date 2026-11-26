// Copyright (c) 2026 Reliant Labs
package db

import (
	"database/sql"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"
)

// The gtm_* tables (20261126173100_create_gtm) hold their invariants
// themselves: a row links only rows of its own org, a verified claim carries
// its evidence, a sent item is an email with its send record, and an org
// holds each claim text once.
func TestMigrate_GtmTablesHoldTheirInvariants(t *testing.T) {
	testkit.Slow(t, "builds a fresh database through every migration")
	t.Parallel()

	_, raw := newMigratedTestDB(t)
	exec := func(q string, args ...any) error {
		_, err := raw.Exec(q, args...)
		return err
	}

	require.NoError(t, exec(`INSERT INTO gtm_campaigns (id, org_id, name) VALUES ('c1', 'org-a', 'Launch')`))
	require.NoError(t, exec(`INSERT INTO gtm_ideas (id, org_id, title, created_by, campaign_id) VALUES ('i1', 'org-a', 'Idea', 'u1', 'c1')`))

	t.Run("a link to another org's row is refused", func(t *testing.T) {
		require.Error(t, exec(`INSERT INTO gtm_ideas (id, org_id, title, created_by, campaign_id) VALUES ('i2', 'org-b', 'Idea', 'u1', 'c1')`))
		require.Error(t, exec(`INSERT INTO gtm_items (id, org_id, title, campaign_id) VALUES ('t1', 'org-b', 'Post', 'c1')`))
		require.Error(t, exec(`INSERT INTO gtm_items (id, org_id, title, idea_id) VALUES ('t2', 'org-b', 'Post', 'i1')`))
		require.NoError(t, exec(`INSERT INTO gtm_items (id, org_id, title, campaign_id, idea_id) VALUES ('t3', 'org-a', 'Post', 'c1', 'i1')`))
	})

	t.Run("defaults are the first real enum member and revision 1", func(t *testing.T) {
		var status, kind string
		var revision int
		require.NoError(t, raw.QueryRow(`SELECT status, kind, revision FROM gtm_ideas WHERE id = 'i1'`).Scan(&status, &kind, &revision))
		require.Equal(t, "GTM_IDEA_STATUS_PROPOSED", status)
		require.Equal(t, "GTM_IDEA_KIND_PRODUCT", kind)
		require.Equal(t, 1, revision)
		require.Error(t, exec(`UPDATE gtm_ideas SET status = 'GTM_IDEA_STATUS_UNSPECIFIED' WHERE id = 'i1'`), "the proto zero is never stored")
	})

	t.Run("a sent item is an email with its send record", func(t *testing.T) {
		require.NoError(t, exec(`INSERT INTO gtm_items (id, org_id, title, kind) VALUES ('e1', 'org-a', 'Mail', 'GTM_ITEM_KIND_EMAIL')`))
		require.Error(t, exec(`UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_SENT' WHERE id = 'e1'`))
		require.Error(t, exec(`UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_SENT', gmail_message_id = 'm', sent_at = now(), sent_by = 'u1', approval_hash = 'h' WHERE id = 't3'`), "only an email is sent")
		require.NoError(t, exec(`UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_SENT', gmail_message_id = 'm', sent_at = now(), sent_by = 'u1', approval_hash = 'h' WHERE id = 'e1'`))
		require.Error(t, exec(`UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_SCHEDULED' WHERE id = 't3'`), "a scheduled item has a date")
	})

	t.Run("a verified claim carries its evidence", func(t *testing.T) {
		require.NoError(t, exec(`INSERT INTO gtm_claims (id, org_id, text, category) VALUES ('k1', 'org-a', 'Hosting costs $5 a month.', 'GTM_CLAIM_CATEGORY_PRICING')`))
		require.Error(t, exec(`UPDATE gtm_claims SET status = 'GTM_CLAIM_STATUS_VERIFIED' WHERE id = 'k1'`))
		require.Error(t, exec(`UPDATE gtm_claims SET status = 'GTM_CLAIM_STATUS_VERIFIED', source_url = 'https://x', source_kind = 'GTM_CLAIM_SOURCE_KIND_DOCS', verified_at = now() WHERE id = 'k1'`), "a verifier is required")
		require.NoError(t, exec(`UPDATE gtm_claims SET status = 'GTM_CLAIM_STATUS_VERIFIED', source_url = 'https://x', source_kind = 'GTM_CLAIM_SOURCE_KIND_DOCS', verified_at = now(), verified_by = 'u1' WHERE id = 'k1'`))

		require.NoError(t, exec(`INSERT INTO gtm_claims (id, org_id, text, category) VALUES ('k2', 'org-a', 'Faster than Acme', 'GTM_CLAIM_CATEGORY_COMPARISON')`))
		require.Error(t, exec(`UPDATE gtm_claims SET status = 'GTM_CLAIM_STATUS_VERIFIED', source_url = 'https://x', source_kind = 'GTM_CLAIM_SOURCE_KIND_DOCS', verified_at = now(), verified_by = 'u1' WHERE id = 'k2'`), "a comparison needs a primary third-party source")
	})

	t.Run("an org holds each claim text once", func(t *testing.T) {
		var norm string
		require.NoError(t, raw.QueryRow(`SELECT text_norm FROM gtm_claims WHERE id = 'k1'`).Scan(&norm))
		require.Equal(t, "hosting costs $5 a month", norm)
		require.Error(t, exec(`INSERT INTO gtm_claims (id, org_id, text, category) VALUES ('k3', 'org-a', '  hosting   costs $5 a MONTH!', 'GTM_CLAIM_CATEGORY_PRICING')`))
		require.NoError(t, exec(`INSERT INTO gtm_claims (id, org_id, text, category) VALUES ('k4', 'org-b', 'Hosting costs $5 a month.', 'GTM_CLAIM_CATEGORY_PRICING')`))
	})

	t.Run("deleting a campaign unlinks its rows and keeps their org", func(t *testing.T) {
		require.NoError(t, exec(`DELETE FROM gtm_campaigns WHERE id = 'c1'`))
		var campaign sql.NullString
		var org string
		require.NoError(t, raw.QueryRow(`SELECT campaign_id, org_id FROM gtm_items WHERE id = 't3'`).Scan(&campaign, &org))
		require.False(t, campaign.Valid)
		require.Equal(t, "org-a", org)
	})
}
