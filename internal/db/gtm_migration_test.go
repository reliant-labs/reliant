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

// The gtm_* tables (20261127160000_create_gtm) hold their invariants
// themselves: a row links only rows of its own org, a verified claim carries
// its evidence, a sent item is email-type with its send record, an email-type
// item is never SCHEDULED or PUBLISHED, a channel's mode matches its
// integration, an asset is attached to something, and an org holds each claim
// text and channel key once.
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

	require.NoError(t, exec(`INSERT INTO gtm_strategies (id, org_id, name, created_by, updated_by) VALUES ('s1', 'org-a', 'Grow', 'u1', 'u1')`))
	require.NoError(t, exec(`INSERT INTO gtm_strategies (id, org_id, name, created_by, updated_by) VALUES ('s9', 'org-b', 'Grow', 'u1', 'u1')`))
	require.NoError(t, exec(`INSERT INTO gtm_campaigns (id, org_id, name, strategy_id) VALUES ('c1', 'org-a', 'Launch', 's1')`))
	require.NoError(t, exec(`INSERT INTO gtm_ideas (id, org_id, title, created_by, campaign_id) VALUES ('i1', 'org-a', 'Idea', 'u1', 'c1')`))

	t.Run("an empty org is refused", func(t *testing.T) {
		refused(t, "gtm_strategies_org_id_check", `INSERT INTO gtm_strategies (id, org_id, name, created_by, updated_by) VALUES ('s0', '', 'Grow', 'u1', 'u1')`)
		refused(t, "gtm_channels_org_id_check", `INSERT INTO gtm_channels (id, org_id, key, name, created_by, mode) VALUES ('h0', '', 'blog', 'Blog', 'u1', 'GTM_CHANNEL_MODE_MANUAL')`)
		refused(t, "gtm_campaigns_org_id_check", `INSERT INTO gtm_campaigns (id, org_id, name, strategy_id) VALUES ('c0', '', 'Launch', 's1')`)
		refused(t, "gtm_assets_org_id_check", `INSERT INTO gtm_assets (id, org_id, campaign_id, object_key, content_type, size_bytes, uploaded_by, source) VALUES ('a0', '', 'c1', 'gtm//a0', 'image/png', 1, 'u1', 'GTM_ASSET_SOURCE_PERSON')`)
		refused(t, "gtm_strategy_library_org_id_check", `INSERT INTO gtm_strategy_library (id, org_id, key, category, depth, name, source_revision, imported_by) VALUES ('l0', '', 'x', 'GTM_STRATEGY_CATEGORY_LAUNCHES_PR', 'GTM_LIBRARY_DEPTH_TACTIC', 'X', 'rev', 'u1')`)
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
		require.NoError(t, exec(`INSERT INTO gtm_items (id, org_id, title, format, status) VALUES ('e1', 'org-a', 'Mail', 'GTM_ITEM_FORMAT_EMAIL', 'GTM_ITEM_STATUS_APPROVED')`))
		refused(t, "gtm_items_sent_has_record", `UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_SENT' WHERE id = 'e1'`)
		// (Its send record's approval hash already marks it: the approval-hash
		// backstop answers before gtm_items_sent_is_email.)
		refused(t, "gtm_items_approval_hash_is_email", `UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_SENT', gmail_message_id = 'm', sent_at = now(), sent_by = 'u1', approval_hash = 'h' WHERE id = 't3'`)
		require.NoError(t, exec(`UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_SENT', gmail_message_id = 'm', sent_at = now(), sent_by = 'u1', approval_hash = 'h' WHERE id = 'e1'`))
		refused(t, "gtm_items_scheduled_has_date", `UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_SCHEDULED' WHERE id = 't3'`)
	})

	t.Run("an email is never scheduled or published", func(t *testing.T) {
		// A dated, approved email stays APPROVED.
		require.NoError(t, exec(`INSERT INTO gtm_items (id, org_id, title, format, status, scheduled_at) VALUES ('e2', 'org-a', 'Mail', 'GTM_ITEM_FORMAT_EMAIL', 'GTM_ITEM_STATUS_APPROVED', now())`))
		refused(t, "gtm_items_email_never_scheduled", `UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_SCHEDULED' WHERE id = 'e2'`)
		refused(t, "gtm_items_email_never_published", `UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_PUBLISHED' WHERE id = 'e2'`)
		// A post may be both.
		require.NoError(t, exec(`UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_SCHEDULED', scheduled_at = now() WHERE id = 't3'`))
		require.NoError(t, exec(`UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_PUBLISHED' WHERE id = 't3'`))
	})

	t.Run("cold outreach is email-type", func(t *testing.T) {
		require.NoError(t, exec(`INSERT INTO gtm_items (id, org_id, title, format, status, scheduled_at) VALUES ('o1', 'org-a', 'Hi', 'GTM_ITEM_FORMAT_COLD_OUTREACH', 'GTM_ITEM_STATUS_APPROVED', now())`))
		refused(t, "gtm_items_email_never_scheduled", `UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_SCHEDULED' WHERE id = 'o1'`)
		refused(t, "gtm_items_email_never_published", `UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_PUBLISHED' WHERE id = 'o1'`)
		require.NoError(t, exec(`UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_SENT', gmail_message_id = 'm', sent_at = now(), sent_by = 'u1', approval_hash = 'h' WHERE id = 'o1'`))
		require.NoError(t, exec(`INSERT INTO gtm_items (id, org_id, title, format) VALUES ('v1', 'org-a', 'Demo', 'GTM_ITEM_FORMAT_VIDEO')`))
		refused(t, "gtm_items_approval_hash_is_email", `UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_SENT', gmail_message_id = 'm', sent_at = now(), sent_by = 'u1', approval_hash = 'h' WHERE id = 'v1'`)
		refused(t, "gtm_items_format_check", `UPDATE gtm_items SET format = 'GTM_ITEM_FORMAT_UNSPECIFIED' WHERE id = 'v1'`)
	})

	t.Run("an approval hash is held only by an approved or sent email-type item", func(t *testing.T) {
		require.NoError(t, exec(`INSERT INTO gtm_items (id, org_id, title, format) VALUES ('e5', 'org-a', 'Mail', 'GTM_ITEM_FORMAT_EMAIL')`))
		refused(t, "gtm_items_approval_hash_status", `UPDATE gtm_items SET approval_hash = 'h' WHERE id = 'e5'`)
		// Held: APPROVED with a hash, its send in doubt.
		require.NoError(t, exec(`UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_APPROVED', approval_hash = 'h' WHERE id = 'e5'`))
		refused(t, "gtm_items_approval_hash_status", `UPDATE gtm_items SET status = 'GTM_ITEM_STATUS_PAUSED' WHERE id = 'e5'`)
		refused(t, "gtm_items_approval_hash_is_email", `UPDATE gtm_items SET format = 'GTM_ITEM_FORMAT_SHORT_POST' WHERE id = 'e5'`)
		// Released: the hash goes, and the item may move again.
		require.NoError(t, exec(`UPDATE gtm_items SET approval_hash = '', status = 'GTM_ITEM_STATUS_PAUSED' WHERE id = 'e5'`))
		require.NoError(t, exec(`INSERT INTO gtm_items (id, org_id, title, format, status) VALUES ('p5', 'org-a', 'Post', 'GTM_ITEM_FORMAT_SHORT_POST', 'GTM_ITEM_STATUS_APPROVED')`))
		refused(t, "gtm_items_approval_hash_is_email", `UPDATE gtm_items SET approval_hash = 'h' WHERE id = 'p5'`)
	})

	t.Run("published_url is http or https", func(t *testing.T) {
		refused(t, "gtm_items_published_url_http", `UPDATE gtm_items SET published_url = 'ftp://x' WHERE id = 'v1'`)
		require.NoError(t, exec(`UPDATE gtm_items SET published_url = 'https://example.com/p/1', status = 'GTM_ITEM_STATUS_PUBLISHED' WHERE id = 'v1'`))
	})

	t.Run("a strategy holds its campaigns", func(t *testing.T) {
		refused(t, "gtm_strategies_period_order", `INSERT INTO gtm_strategies (id, org_id, name, created_by, updated_by, period_start, period_end) VALUES ('s2', 'org-a', 'Q', 'u1', 'u1', '2026-12-31', '2026-12-01')`)
		refused(t, "gtm_strategies_status_check", `UPDATE gtm_strategies SET status = 'GTM_STRATEGY_STATUS_UNSPECIFIED' WHERE id = 's1'`)
		refused(t, "gtm_strategies_category_known", `UPDATE gtm_strategies SET category = 'GTM_STRATEGY_CATEGORY_UNSPECIFIED' WHERE id = 's1'`)
		require.NoError(t, exec(`UPDATE gtm_strategies SET category = 'GTM_STRATEGY_CATEGORY_LAUNCHES_PR', library_key = 'made-up-play' WHERE id = 's1'`))
		refused(t, "gtm_campaigns_strategy_same_org_fkey", `INSERT INTO gtm_campaigns (id, org_id, name, strategy_id) VALUES ('c8', 'org-a', 'Launch', 's9')`)
		refused(t, "gtm_campaigns_strategy_same_org_fkey", `DELETE FROM gtm_strategies WHERE id = 's1'`)
		refused(t, "gtm_campaigns_planned_formats_known", `UPDATE gtm_campaigns SET planned_formats = '{GTM_ITEM_FORMAT_BILLBOARD}' WHERE id = 'c1'`)
		require.NoError(t, exec(`UPDATE gtm_campaigns SET planned_formats = '{GTM_ITEM_FORMAT_SHORT_POST,GTM_ITEM_FORMAT_EMAIL}', planned_channel_ids = '{h1}' WHERE id = 'c1'`))
	})

	t.Run("a channel's mode matches its integration, and its key is a unique slug", func(t *testing.T) {
		require.NoError(t, exec(`INSERT INTO gtm_channels (id, org_id, key, name, created_by, mode, integration_kind) VALUES ('h1', 'org-a', 'email', 'Email', 'u1', 'GTM_CHANNEL_MODE_INTEGRATED', 'gmail')`))
		require.NoError(t, exec(`INSERT INTO gtm_channels (id, org_id, key, name, created_by, mode, manual_instructions_md) VALUES ('h2', 'org-a', 'hacker-news', 'Hacker News', 'u1', 'GTM_CHANNEL_MODE_MANUAL', 'Post by hand, then paste the URL on the item')`))
		refused(t, "gtm_channels_org_id_key_key", `INSERT INTO gtm_channels (id, org_id, key, name, created_by, mode) VALUES ('h3', 'org-a', 'hacker-news', 'HN', 'u1', 'GTM_CHANNEL_MODE_MANUAL')`)
		require.NoError(t, exec(`INSERT INTO gtm_channels (id, org_id, key, name, created_by, mode) VALUES ('h9', 'org-b', 'hacker-news', 'HN', 'u1', 'GTM_CHANNEL_MODE_MANUAL')`))
		refused(t, "gtm_channels_key_slug", `INSERT INTO gtm_channels (id, org_id, key, name, created_by, mode) VALUES ('h4', 'org-a', 'Hacker News', 'HN', 'u1', 'GTM_CHANNEL_MODE_MANUAL')`)
		refused(t, "gtm_channels_integrated_has_kind", `INSERT INTO gtm_channels (id, org_id, key, name, created_by, mode) VALUES ('h5', 'org-a', 'mail2', 'Mail', 'u1', 'GTM_CHANNEL_MODE_INTEGRATED')`)
		refused(t, "gtm_channels_manual_has_no_integration", `INSERT INTO gtm_channels (id, org_id, key, name, created_by, mode, connection_id) VALUES ('h6', 'org-a', 'reddit', 'Reddit', 'u1', 'GTM_CHANNEL_MODE_MANUAL', 'conn-1')`)
		refused(t, "gtm_channels_manual_has_no_integration", `UPDATE gtm_channels SET mode = 'GTM_CHANNEL_MODE_MANUAL' WHERE id = 'h1'`)
		// Never '': an empty integration field is NULL, and a type is a word.
		refused(t, "gtm_channels_integration_kind_not_empty", `UPDATE gtm_channels SET integration_kind = '' WHERE id = 'h2'`)
		refused(t, "gtm_channels_connection_id_not_empty", `UPDATE gtm_channels SET connection_id = '' WHERE id = 'h1'`)
		require.NoError(t, exec(`UPDATE gtm_channels SET connection_id = 'conn-1', channel_types = '{inbox,newsletter}' WHERE id = 'h1'`))
		refused(t, "gtm_channels_channel_types_not_empty", `UPDATE gtm_channels SET channel_types = '{forum,""}' WHERE id = 'h2'`)
		refused(t, "gtm_channels_channel_types_not_empty", `UPDATE gtm_channels SET channel_types = ARRAY['forum', NULL]::TEXT[] WHERE id = 'h2'`)
		refused(t, "gtm_channels_channel_types_slugs", `UPDATE gtm_channels SET channel_types = '{Forum}' WHERE id = 'h2'`)
		refused(t, "gtm_channels_channel_types_slugs", `UPDATE gtm_channels SET channel_types = '{"a b"}' WHERE id = 'h2'`)
		refused(t, "gtm_channels_channel_types_slugs", `UPDATE gtm_channels SET channel_types = ARRAY[repeat('a', 33)] WHERE id = 'h2'`)
		require.NoError(t, exec(`UPDATE gtm_channels SET channel_types = '{forum,community-post}' WHERE id = 'h2'`))
		// A key may be renamed, never onto another channel's.
		require.NoError(t, exec(`UPDATE gtm_channels SET key = 'hn' WHERE id = 'h2'`))
		refused(t, "gtm_channels_org_id_key_key", `UPDATE gtm_channels SET key = 'email' WHERE id = 'h2'`)
		// A seeded channel has no creator; an added one names who added it.
		require.NoError(t, exec(`INSERT INTO gtm_channels (id, org_id, key, name, mode, source) VALUES ('h7', 'org-a', 'blog', 'Blog', 'GTM_CHANNEL_MODE_MANUAL', 'GTM_CHANNEL_SOURCE_SEED')`))
		refused(t, "gtm_channels_creator", `INSERT INTO gtm_channels (id, org_id, key, name, mode, source) VALUES ('h8', 'org-a', 'forum', 'Forum', 'GTM_CHANNEL_MODE_MANUAL', 'GTM_CHANNEL_SOURCE_AGENT')`)
		require.NoError(t, exec(`INSERT INTO gtm_channels (id, org_id, key, name, mode, source, created_by) VALUES ('h8', 'org-a', 'forum', 'Forum', 'GTM_CHANNEL_MODE_MANUAL', 'GTM_CHANNEL_SOURCE_AGENT', 'u1')`))

		refused(t, "gtm_items_channel_same_org_fkey", `INSERT INTO gtm_items (id, org_id, title, channel_id) VALUES ('t8', 'org-a', 'Post', 'h9')`)
		require.NoError(t, exec(`INSERT INTO gtm_items (id, org_id, title, channel_id) VALUES ('t9', 'org-a', 'Post', 'h2')`))
		refused(t, "gtm_items_channel_same_org_fkey", `DELETE FROM gtm_channels WHERE id = 'h2'`)
	})

	t.Run("an asset is attached to an item or a campaign of its org", func(t *testing.T) {
		const cols = `(id, org_id, item_id, campaign_id, object_key, content_type, size_bytes, uploaded_by, source)`
		refused(t, "gtm_assets_attached", `INSERT INTO gtm_assets `+cols+` VALUES ('a1', 'org-a', NULL, NULL, 'gtm/org-a/a1', 'image/png', 1, 'u1', 'GTM_ASSET_SOURCE_PERSON')`)
		refused(t, "gtm_assets_size_bytes_check", `INSERT INTO gtm_assets `+cols+` VALUES ('a1', 'org-a', 't9', NULL, 'gtm/org-a/a1', 'image/png', 0, 'u1', 'GTM_ASSET_SOURCE_PERSON')`)
		refused(t, "gtm_assets_source_check", `INSERT INTO gtm_assets `+cols+` VALUES ('a1', 'org-a', 't9', NULL, 'gtm/org-a/a1', 'image/png', 1, 'u1', 'GTM_ASSET_SOURCE_UNSPECIFIED')`)
		refused(t, "gtm_assets_item_same_org_fkey", `INSERT INTO gtm_assets `+cols+` VALUES ('a1', 'org-b', 't9', NULL, 'gtm/org-b/a1', 'image/png', 1, 'u1', 'GTM_ASSET_SOURCE_PERSON')`)
		require.NoError(t, exec(`INSERT INTO gtm_assets `+cols+` VALUES ('a1', 'org-a', 't9', 'c1', 'gtm/org-a/a1', 'image/png', 10, 'u1', 'GTM_ASSET_SOURCE_CHAT')`))
		require.NoError(t, exec(`INSERT INTO gtm_assets `+cols+` VALUES ('a2', 'org-a', 't9', NULL, 'gtm/org-a/a2', 'image/png', 10, 'u1', 'GTM_ASSET_SOURCE_WORKFLOW')`))
		// An item's last link cannot be dropped from under its only asset.
		refused(t, "gtm_assets_attached", `DELETE FROM gtm_items WHERE id = 't9'`)

		var status string
		require.NoError(t, raw.QueryRow(`SELECT status FROM gtm_assets WHERE id = 'a1'`).Scan(&status))
		require.Equal(t, "GTM_ASSET_STATUS_PENDING", status)
		refused(t, "gtm_assets_object_key_is_its_own", `INSERT INTO gtm_assets `+cols+` VALUES ('a3', 'org-a', 't9', NULL, 'gtm/org-b/a3', 'image/png', 10, 'u1', 'GTM_ASSET_SOURCE_PERSON')`)
		refused(t, "gtm_assets_size_bytes_check", `INSERT INTO gtm_assets `+cols+` VALUES ('a3', 'org-a', 't9', NULL, 'gtm/org-a/a3', 'video/mp4', 67108865, 'u1', 'GTM_ASSET_SOURCE_PERSON')`)
		// A pending upload may not know its size yet; a ready one carries its checks.
		require.NoError(t, exec(`INSERT INTO gtm_assets `+cols+` VALUES ('a3', 'org-a', 't9', NULL, 'gtm/org-a/a3', 'image/png', NULL, 'u1', 'GTM_ASSET_SOURCE_PERSON')`))
		refused(t, "gtm_assets_ready_is_checked", `UPDATE gtm_assets SET status = 'GTM_ASSET_STATUS_READY' WHERE id = 'a3'`)
		refused(t, "gtm_assets_ready_is_checked", `UPDATE gtm_assets SET status = 'GTM_ASSET_STATUS_READY', size_bytes = 10 WHERE id = 'a3'`)
		refused(t, "gtm_assets_sha256_hex", `UPDATE gtm_assets SET sha256 = 'not-hex' WHERE id = 'a3'`)
		require.NoError(t, exec(`UPDATE gtm_assets SET status = 'GTM_ASSET_STATUS_READY', size_bytes = 10, sha256 = repeat('ab', 32) WHERE id = 'a3'`))
	})

	t.Run("an org's library holds each key once", func(t *testing.T) {
		// Made-up entries: Reliant carries no catalog content.
		const cols = `(id, org_id, key, category, depth, name, source_revision, imported_by`
		require.NoError(t, exec(`INSERT INTO gtm_strategy_library `+cols+`, stage, hours, expected_delta, fits, steps) VALUES ('l1', 'org-a', 'sample-play', 'GTM_STRATEGY_CATEGORY_COMMUNITY_EVENTS', 'GTM_LIBRARY_DEPTH_PLAY', 'Sample play', 'rev1', 'u1', 'signup', 4, 0.5, '{saas}', '[{"who": "agent", "title": "Step one"}]')`))
		require.NoError(t, exec(`INSERT INTO gtm_strategy_library `+cols+`) VALUES ('l2', 'org-a', 'sample-tactic', 'GTM_STRATEGY_CATEGORY_TRUST_REPUTATION', 'GTM_LIBRARY_DEPTH_TACTIC', 'Sample tactic', 'rev1', 'u1')`))
		refused(t, "gtm_strategy_library_org_id_key_key", `INSERT INTO gtm_strategy_library `+cols+`) VALUES ('l3', 'org-a', 'sample-play', 'GTM_STRATEGY_CATEGORY_LAUNCHES_PR', 'GTM_LIBRARY_DEPTH_TACTIC', 'Again', 'rev1', 'u1')`)
		require.NoError(t, exec(`INSERT INTO gtm_strategy_library `+cols+`) VALUES ('l4', 'org-b', 'sample-play', 'GTM_STRATEGY_CATEGORY_LAUNCHES_PR', 'GTM_LIBRARY_DEPTH_TACTIC', 'Other org', 'rev1', 'u1')`))
		refused(t, "gtm_strategy_library_key_slug", `INSERT INTO gtm_strategy_library `+cols+`) VALUES ('l5', 'org-a', 'Not A Slug', 'GTM_STRATEGY_CATEGORY_LAUNCHES_PR', 'GTM_LIBRARY_DEPTH_TACTIC', 'X', 'rev1', 'u1')`)
		refused(t, "gtm_strategy_library_category_check", `INSERT INTO gtm_strategy_library `+cols+`) VALUES ('l5', 'org-a', 'x', 'GTM_STRATEGY_CATEGORY_UNSPECIFIED', 'GTM_LIBRARY_DEPTH_TACTIC', 'X', 'rev1', 'u1')`)
		refused(t, "gtm_strategy_library_depth_check", `INSERT INTO gtm_strategy_library `+cols+`) VALUES ('l5', 'org-a', 'x', 'GTM_STRATEGY_CATEGORY_LAUNCHES_PR', 'GTM_LIBRARY_DEPTH_UNSPECIFIED', 'X', 'rev1', 'u1')`)
		refused(t, "gtm_strategy_library_steps_check", `UPDATE gtm_strategy_library SET steps = '{"who": "agent"}' WHERE id = 'l1'`)
		refused(t, "gtm_strategy_library_source_revision_check", `UPDATE gtm_strategy_library SET source_revision = '' WHERE id = 'l1'`)
		// The import's upsert.
		require.NoError(t, exec(`INSERT INTO gtm_strategy_library `+cols+`) VALUES ('l6', 'org-a', 'sample-play', 'GTM_STRATEGY_CATEGORY_COMMUNITY_EVENTS', 'GTM_LIBRARY_DEPTH_PLAY', 'Sample play v2', 'rev2', 'u1') ON CONFLICT (org_id, key) DO UPDATE SET name = EXCLUDED.name, source_revision = EXCLUDED.source_revision`))
		var name, id string
		require.NoError(t, raw.QueryRow(`SELECT id, name FROM gtm_strategy_library WHERE org_id = 'org-a' AND key = 'sample-play'`).Scan(&id, &name))
		require.Equal(t, "l1", id)
		require.Equal(t, "Sample play v2", name)
	})

	t.Run("a token's import never prunes; its prune waits for a person", func(t *testing.T) {
		const req = `(id, org_id, requested_by, credential_kind, source_revision, remove_keys)`
		require.NoError(t, exec(`INSERT INTO gtm_library_prune_requests `+req+` VALUES ('r1', 'org-a', 'u1', 'GTM_CREDENTIAL_KIND_TOKEN', 'rev2', '{sample-tactic}')`))
		require.NoError(t, exec(`INSERT INTO gtm_library_prune_requests `+req+` VALUES ('r9', 'org-b', 'u1', 'GTM_CREDENTIAL_KIND_TOKEN', 'rev2', '{x}')`))
		refused(t, "gtm_library_prune_requests_remove_keys_check", `INSERT INTO gtm_library_prune_requests `+req+` VALUES ('r2', 'org-a', 'u1', 'GTM_CREDENTIAL_KIND_TOKEN', 'rev2', '{}')`)
		refused(t, "gtm_library_prune_requests_credential_kind_check", `INSERT INTO gtm_library_prune_requests `+req+` VALUES ('r2', 'org-a', 'u1', 'GTM_CREDENTIAL_KIND_UNSPECIFIED', 'rev2', '{x}')`)
		// A decision is a person's, with its time; PENDING and EXPIRED carry none.
		refused(t, "gtm_library_prune_requests_decision", `UPDATE gtm_library_prune_requests SET status = 'GTM_PRUNE_REQUEST_STATUS_CONFIRMED' WHERE id = 'r1'`)
		refused(t, "gtm_library_prune_requests_decided_together", `UPDATE gtm_library_prune_requests SET status = 'GTM_PRUNE_REQUEST_STATUS_CONFIRMED', decided_by = 'u2' WHERE id = 'r1'`)
		refused(t, "gtm_library_prune_requests_decision", `UPDATE gtm_library_prune_requests SET decided_by = 'u2', decided_at = now() WHERE id = 'r1'`)
		require.NoError(t, exec(`UPDATE gtm_library_prune_requests SET status = 'GTM_PRUNE_REQUEST_STATUS_EXPIRED' WHERE id = 'r9'`))
		refused(t, "gtm_library_prune_requests_decision", `UPDATE gtm_library_prune_requests SET decided_by = 'u2', decided_at = now() WHERE id = 'r9'`)

		const imp = `(id, org_id, actor_user_id, credential_kind, source_revision, created, updated, unchanged, pruned, prune_request_id)`
		require.NoError(t, exec(`INSERT INTO gtm_library_imports `+imp+` VALUES ('m1', 'org-a', 'u1', 'GTM_CREDENTIAL_KIND_TOKEN', 'rev2', 0, 1, 1, 0, 'r1')`))
		refused(t, "gtm_library_imports_token_never_prunes", `INSERT INTO gtm_library_imports `+imp+` VALUES ('m2', 'org-a', 'u1', 'GTM_CREDENTIAL_KIND_TOKEN', 'rev2', 0, 0, 0, 1, NULL)`)
		refused(t, "gtm_library_imports_request_is_token", `INSERT INTO gtm_library_imports `+imp+` VALUES ('m2', 'org-a', 'u1', 'GTM_CREDENTIAL_KIND_PERSON', 'rev2', 0, 0, 0, 0, 'r1')`)
		refused(t, "gtm_library_imports_prune_request_same_org_fkey", `INSERT INTO gtm_library_imports `+imp+` VALUES ('m2', 'org-a', 'u1', 'GTM_CREDENTIAL_KIND_TOKEN', 'rev2', 0, 0, 0, 0, 'r9')`)
		refused(t, "gtm_library_imports_created_check", `INSERT INTO gtm_library_imports `+imp+` VALUES ('m2', 'org-a', 'u1', 'GTM_CREDENTIAL_KIND_PERSON', 'rev2', -1, 0, 0, 0, NULL)`)
		// A person-class import prunes at once.
		require.NoError(t, exec(`INSERT INTO gtm_library_imports `+imp+` VALUES ('m3', 'org-a', 'u2', 'GTM_CREDENTIAL_KIND_PERSON', 'rev3', 0, 0, 2, 1, NULL)`))
		require.NoError(t, exec(`UPDATE gtm_library_prune_requests SET status = 'GTM_PRUNE_REQUEST_STATUS_CONFIRMED', decided_by = 'u2', decided_at = now() WHERE id = 'r1'`))
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
		var assetCampaign sql.NullString
		require.NoError(t, raw.QueryRow(`SELECT campaign_id FROM gtm_assets WHERE id = 'a1'`).Scan(&assetCampaign))
		require.False(t, assetCampaign.Valid, "the asset keeps its item, loses the campaign")
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
