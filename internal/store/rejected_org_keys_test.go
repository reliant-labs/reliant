// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// The rejected org keys of the orgs asked about, and only those: a key that
// works, another org's, and a person's own rejected key are not listed.
func TestListRejectedOrgKeysIn(t *testing.T) {
	f := newCredFixture(t)
	ctx := context.Background()
	reject := func(id string) {
		f.exec(`UPDATE provider_accounts SET needs_reauth_at = $2, needs_reauth_reason = 'rejected' WHERE id = $1`, id, credT0)
	}
	any, err := store.HasRejectedOrgKeys(ctx, f.env.ORM)
	require.NoError(t, err)
	assert.False(t, any)

	f.orgKey("k-acme", "acme")
	f.orgKey("k-acme-fine", "acme")
	f.orgKey("k-beta", "beta")
	f.personal("own-dead", "member", "", "")
	reject("own-dead")
	any, err = store.HasRejectedOrgKeys(ctx, f.env.ORM)
	require.NoError(t, err)
	assert.False(t, any, "a person's own rejected key is not an org's")

	reject("k-acme")
	reject("k-beta")
	any, err = store.HasRejectedOrgKeys(ctx, f.env.ORM)
	require.NoError(t, err)
	assert.True(t, any)

	got, err := store.ListRejectedOrgKeysIn(ctx, f.env.ORM, []string{"acme", "gamma"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "k-acme", got[0].ID)
	assert.Equal(t, "acme", got[0].OrgID)
	require.NotNil(t, got[0].NeedsReauthAt)
	none, err := store.ListRejectedOrgKeysIn(ctx, f.env.ORM, nil)
	require.NoError(t, err)
	assert.Empty(t, none)
}

// The partial index the inbox's probe reads exists on the migrated schema, is
// valid (a CONCURRENTLY build that failed leaves an invalid one), indexes
// org_id, and covers exactly the org rows that need attention.
func TestRejectedOrgKeysIndex(t *testing.T) {
	f := newCredFixture(t)
	var (
		valid     bool
		columns   string
		predicate string
	)
	require.NoError(t, f.env.SQL.QueryRowContext(context.Background(), `
		SELECT i.indisvalid,
		       (SELECT string_agg(a.attname, ',' ORDER BY k.n) FROM unnest(i.indkey) WITH ORDINALITY AS k(attnum, n)
		          JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum),
		       pg_get_expr(i.indpred, i.indrelid)
		FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = 'provider_accounts_org_needs_attention'`).Scan(&valid, &columns, &predicate),
		"the index exists")
	assert.True(t, valid)
	assert.Equal(t, "org_id", columns)
	assert.Contains(t, predicate, "owner_kind = 'org'")
	assert.Contains(t, predicate, "needs_reauth_at IS NOT NULL")
}

// The org-key notice sweep is fair (as the pause sweep): with 120 older
// marks whose start keeps failing claimed by earlier sweeps, a newer mark is
// the one the next sweep claims; a claimed mark is not claimed again within
// its lease, but is at once when the key is marked anew.
func TestClaimDueOrgKeyNotices_IsFairToANewerMark(t *testing.T) {
	f := newCredFixture(t)
	ctx := context.Background()
	for i := 1; i <= 120; i++ {
		id := fmt.Sprintf("k-old-%d", i)
		f.orgKey(id, "acme")
		f.exec(`UPDATE provider_accounts SET needs_reauth_at = now() - interval '1 day' + $2 * interval '1 second',
			needs_reauth_reason = 'rejected', reauth_notice_next_at = now() + interval '1 hour' WHERE id = $1`, id, i)
	}
	f.orgKey("k-new", "acme")
	f.exec(`UPDATE provider_accounts SET needs_reauth_at = now(), needs_reauth_reason = 'rejected' WHERE id = 'k-new'`)

	claim := func() []string {
		t.Helper()
		got, err := store.ClaimDueOrgKeyNotices(ctx, f.env.ORM, 100, time.Hour)
		require.NoError(t, err)
		var out []string
		for _, a := range got {
			out = append(out, a.ID)
		}
		return out
	}
	assert.Equal(t, []string{"k-new"}, claim(), "only the newer mark is due")
	assert.Empty(t, claim(), "claimed: not due again within the lease")

	// Marked anew after its claim: due at once.
	f.exec(`UPDATE provider_accounts SET needs_reauth_at = now() + interval '1 second' WHERE id = 'k-old-7'`)
	assert.Equal(t, []string{"k-old-7"}, claim())
}

// A rejected org key's mark is a notice still to start until it is confirmed
// for exactly that mark: confirming lists it no more, a confirm for an older
// mark changes nothing, and a new mark (after a rotation) is listed again.
// Working keys and a person's own rejected key are never listed.
func TestUnstartedOrgKeyNotices(t *testing.T) {
	f := newCredFixture(t)
	ctx := context.Background()
	mark := func(id string, at time.Time) {
		f.exec(`UPDATE provider_accounts SET needs_reauth_at = $2, needs_reauth_reason = 'rejected' WHERE id = $1`, id, at)
	}
	ids := func() []string {
		t.Helper()
		f.exec(`UPDATE provider_accounts SET reauth_notice_next_at = NULL`) // every mark due again
		got, err := store.ClaimDueOrgKeyNotices(ctx, f.env.ORM, 10, time.Hour)
		require.NoError(t, err)
		var out []string
		for _, a := range got {
			out = append(out, a.ID)
		}
		return out
	}
	f.orgKey("k-acme", "acme")
	f.orgKey("k-fine", "acme")
	f.personal("own-dead", "member", "", "")
	mark("own-dead", credT0)
	mark("k-acme", credT0)
	assert.Equal(t, []string{"k-acme"}, ids())

	require.NoError(t, store.ConfirmOrgKeyNotice(ctx, f.env.ORM, "beta", "k-acme", credT0))
	assert.Equal(t, []string{"k-acme"}, ids(), "another org's confirm reaches nothing")
	require.NoError(t, store.ConfirmOrgKeyNotice(ctx, f.env.ORM, "acme", "k-acme", credT0))
	assert.Empty(t, ids(), "confirmed for its mark")

	later := credT0.Add(time.Hour)
	mark("k-acme", later)
	assert.Equal(t, []string{"k-acme"}, ids(), "a new mark is a new notice")
	require.NoError(t, store.ConfirmOrgKeyNotice(ctx, f.env.ORM, "acme", "k-acme", credT0))
	assert.Equal(t, []string{"k-acme"}, ids(), "a confirm of the old mark is not this one's")
	require.NoError(t, store.ConfirmOrgKeyNotice(ctx, f.env.ORM, "acme", "k-acme", later))
	assert.Empty(t, ids())
}

// Round-2 B1: pauses whose notice is running, waiting or refused never hold
// the sweep's batch. With 120 older pauses claimed (their workflows running)
// or refused, a newer pause whose start failed at the pause is the one the
// next sweep claims; and a claimed pause comes round again once its lease
// passes, behind the never-claimed.
func TestClaimDuePauseNotices_IsFairToANewerPause(t *testing.T) {
	f := newCredFixture(t)
	ctx := context.Background()
	f.exec(`INSERT INTO projects (id, user_id, name, path, created_at, updated_at, last_active) VALUES ('p-f', 'owner', 'p', '/tmp/p', now(), now(), now()) ON CONFLICT DO NOTHING`)
	f.exec(`INSERT INTO triggers (id, user_id, project_id, org_id, name, kind, config, enabled, workflow, created_at, updated_at,
			paused_reason, paused_at, pause_notice_next_at, pause_notice_refused_for)
		SELECT 'old-' || g, 'owner', 'p-f', 'acme', 'old-' || g, 'schedule', '{}', false, 'builtin://agent', now(), now(),
			'no_org_account', now() - interval '1 day' + g * interval '1 second',
			CASE WHEN g % 2 = 0 THEN now() + interval '1 hour' END,
			CASE WHEN g % 2 = 1 THEN now() - interval '1 day' + g * interval '1 second' END
		  FROM generate_series(1, 120) g`)
	f.exec(`INSERT INTO triggers (id, user_id, project_id, org_id, name, kind, config, enabled, workflow, created_at, updated_at,
			paused_reason, paused_at)
		VALUES ('new', 'owner', 'p-f', 'acme', 'new', 'schedule', '{}', false, 'builtin://agent', now(), now(), 'no_org_account', now())`)

	got, err := store.ClaimDuePauseNotices(ctx, f.env.ORM, 100, time.Hour)
	require.NoError(t, err)
	require.Len(t, got, 1, "only the newer pause is due")
	assert.Equal(t, "new", got[0].ID)

	// Every running one's lease passes: they are due again, after a pause
	// never claimed, and never the refused.
	f.exec(`UPDATE triggers SET pause_notice_next_at = now() - interval '1 minute' WHERE id LIKE 'old-%' AND pause_notice_next_at IS NOT NULL`)
	f.exec(`INSERT INTO triggers (id, user_id, project_id, org_id, name, kind, config, enabled, workflow, created_at, updated_at,
			paused_reason, paused_at)
		VALUES ('newest', 'owner', 'p-f', 'acme', 'newest', 'schedule', '{}', false, 'builtin://agent', now(), now(), 'no_org_account', now())`)
	got, err = store.ClaimDuePauseNotices(ctx, f.env.ORM, 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, got, 10)
	claimed := map[string]bool{}
	for _, tr := range got {
		claimed[tr.ID] = true
		var n int
		_, _ = fmt.Sscanf(tr.ID, "old-%d", &n)
		assert.True(t, tr.ID == "newest" || n%2 == 0, "never a refused pause: %s", tr.ID)
	}
	assert.True(t, claimed["newest"], "a never-claimed pause goes first")
}

// The no-org-account pauses: listed to their owner and to the admins of their
// org, a pause until its notice is confirmed for exactly that pause (a resume,
// or another reason's pause, lists nothing); and an owner's org triggers that
// may spend their own accounts (any, or unstamped).
func TestTriggerNoticeQueries(t *testing.T) {
	f := newCredFixture(t)
	ctx := context.Background()
	f.exec(`INSERT INTO projects (id, user_id, name, path, created_at, updated_at, last_active) VALUES ('p-n', 'owner', 'p', '/tmp/p', now(), now(), now()) ON CONFLICT DO NOTHING`)
	add := func(id, user, org string, funding any) {
		f.exec(`INSERT INTO triggers (id, user_id, project_id, org_id, name, kind, config, enabled, workflow, created_at, updated_at, unattended_funding)
			VALUES ($1, $2, 'p-n', NULLIF($3, ''), $1, 'schedule', '{}', true, 'builtin://agent', now(), now(), $4)`, id, user, org, funding)
	}
	add("t-owned", "owner", "acme", "org_only")
	add("t-team", "teammate", "acme", "org_only")
	add("t-beta", "stranger", "beta", "org_only")
	add("t-any", "owner", "acme", "any")
	add("t-null", "owner", "acme", nil)
	add("t-noorg", "owner", "", nil)
	for _, id := range []string{"t-owned", "t-team", "t-beta"} {
		require.NoError(t, store.PauseTrigger(ctx, f.env.ORM, id, "no_org_account"))
	}
	require.NoError(t, store.PauseTrigger(ctx, f.env.ORM, "t-any", "creator_left_org"))

	ids := func(ts []*core.Trigger, err error) []string {
		t.Helper()
		require.NoError(t, err)
		var out []string
		for _, tr := range ts {
			out = append(out, tr.ID)
		}
		sort.Strings(out)
		return out
	}
	assert.Equal(t, []string{"t-owned"}, ids(store.ListNoOrgAccountPauses(ctx, f.env.ORM, "owner", nil)))
	assert.Equal(t, []string{"t-owned", "t-team"}, ids(store.ListNoOrgAccountPauses(ctx, f.env.ORM, "owner", []string{"acme"})))

	// The sweep claims what is owed and due; a claim holds for its lease.
	claim := func() []string {
		t.Helper()
		f.exec(`UPDATE triggers SET pause_notice_next_at = NULL`) // every pause due again
		return ids(store.ClaimDuePauseNotices(ctx, f.env.ORM, 10, time.Hour))
	}
	assert.Equal(t, []string{"t-beta", "t-owned", "t-team"}, claim())
	assert.Empty(t, ids(store.ClaimDuePauseNotices(ctx, f.env.ORM, 10, time.Hour)), "claimed: not due again within the lease")
	owned, err := store.GetTrigger(ctx, f.env.ORM, "t-owned")
	require.NoError(t, err)
	require.NotNil(t, owned.PausedAt)
	require.NoError(t, store.ConfirmPauseNotice(ctx, f.env.ORM, "t-owned", owned.PausedAt.Add(-time.Second)))
	assert.Contains(t, claim(), "t-owned", "a confirm of another pause is not this one's")
	require.NoError(t, store.ConfirmPauseNotice(ctx, f.env.ORM, "t-owned", *owned.PausedAt))
	assert.Equal(t, []string{"t-beta", "t-team"}, claim())

	// Refused for good: never claimed again for that pause; a new pause is
	// owed its own notice, due at once (PauseTrigger clears the claim).
	beta, err := store.GetTrigger(ctx, f.env.ORM, "t-beta")
	require.NoError(t, err)
	require.NoError(t, store.RefusePauseNotice(ctx, f.env.ORM, "t-beta", *beta.PausedAt))
	assert.Equal(t, []string{"t-team"}, claim())
	f.exec(`UPDATE triggers SET pause_notice_next_at = now() + interval '1 hour' WHERE id = 't-beta'`)
	require.NoError(t, store.PauseTrigger(ctx, f.env.ORM, "t-beta", "no_org_account"))
	assert.Contains(t, ids(store.ClaimDuePauseNotices(ctx, f.env.ORM, 10, time.Hour)), "t-beta", "a new pause is due now")

	assert.Equal(t, []string{"t-any", "t-null"}, ids(store.ListPersonallyFundedTriggers(ctx, f.env.ORM, "owner")))
}
