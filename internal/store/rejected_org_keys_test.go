// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
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
		got, err := store.ListUnstartedOrgKeyNotices(ctx, f.env.ORM, 10)
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

	assert.Equal(t, []string{"t-beta", "t-owned", "t-team"}, ids(store.ListUndeliveredPauseNotices(ctx, f.env.ORM, 10)))
	owned, err := store.GetTrigger(ctx, f.env.ORM, "t-owned")
	require.NoError(t, err)
	require.NotNil(t, owned.PausedAt)
	require.NoError(t, store.ConfirmPauseNotice(ctx, f.env.ORM, "t-owned", owned.PausedAt.Add(-time.Second)))
	assert.Contains(t, ids(store.ListUndeliveredPauseNotices(ctx, f.env.ORM, 10)), "t-owned", "a confirm of another pause is not this one's")
	require.NoError(t, store.ConfirmPauseNotice(ctx, f.env.ORM, "t-owned", *owned.PausedAt))
	assert.Equal(t, []string{"t-beta", "t-team"}, ids(store.ListUndeliveredPauseNotices(ctx, f.env.ORM, 10)))

	assert.Equal(t, []string{"t-any", "t-null"}, ids(store.ListPersonallyFundedTriggers(ctx, f.env.ORM, "owner")))
}
