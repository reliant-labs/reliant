// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/store"
)

// The rejected org keys the inbox asks about are those of the orgs the person
// has a footprint in: a project there, a grant, a seat it pays for, or a key
// they added. A key that works, a person's own rejected key, and an org they
// have nothing in are not listed. Whether they may see it is decided by role.
func TestListRejectedOrgKeysNear(t *testing.T) {
	f := newCredFixture(t)
	ctx := context.Background()
	reject := func(id string) {
		f.exec(`UPDATE provider_accounts SET needs_reauth_at = $2, needs_reauth_reason = 'rejected' WHERE id = $1`, id, credT0)
	}
	f.mvProject("p-1") // mvOwner's project in mvSrc
	for _, k := range []struct{ id, org string }{
		{"k-project", mvSrc}, {"k-grant", "org-grant"}, {"k-seat", "org-seat"}, {"k-mine", "org-added"},
		{"k-elsewhere", "org-else"}, {"k-fine", mvSrc},
	} {
		f.orgKey(k.id, k.org)
	}
	for _, id := range []string{"k-project", "k-grant", "k-seat", "k-mine", "k-elsewhere"} {
		reject(id)
	}
	f.grant("k-grant", "org-grant", mvOwner, credT0)
	f.personal("seat", mvOwner, "org-seat", "workspace")
	f.exec(`UPDATE provider_accounts SET created_by_user_id = $1 WHERE id = 'k-mine'`, mvOwner)
	f.personal("own-dead", mvOwner, "", "")
	reject("own-dead")

	got, err := store.ListRejectedOrgKeysNear(ctx, f.env.ORM, mvOwner)
	require.NoError(t, err)
	var ids []string
	for _, a := range got {
		ids = append(ids, a.ID)
		assert.NotNil(t, a.NeedsReauthAt)
		assert.NotEmpty(t, a.OrgID)
	}
	assert.ElementsMatch(t, []string{"k-project", "k-grant", "k-seat", "k-mine"}, ids)

	none, err := store.ListRejectedOrgKeysNear(ctx, f.env.ORM, "nobody")
	require.NoError(t, err)
	assert.Empty(t, none)
}
