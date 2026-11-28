// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/projectaccess"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

type liveOrgs map[string]bool

func (m liveOrgs) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	return m[userID+"/"+orgID], true, nil
}

// projectaccess over the real owner-filtered store read: one tenant never reads
// another's project, in the same org or not, and a former member reads none of
// the org's.
func TestProjectAccess_CrossTenantAgainstTheDatabase(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	acme := "acme"
	mk := func(id, user string, orgID *string) {
		require.NoError(t, store.InsertProject(ctx, env.ORM, &core.Project{
			ID: id, UserID: user, OrgID: orgID, Name: id, Path: "/p/" + id, CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
	}
	mk("alice-solo", "alice", nil)
	mk("alice-acme", "alice", &acme)
	mk("bob-acme", "bob", &acme)

	svc := projectaccess.New(projectaccess.Deps{Store: store.NewProjectExtStore(env.ORM)})
	member := authz.WithMembership(ctx, liveOrgs{"alice/acme": true, "bob/acme": true})

	got, err := svc.GetProjectWithUserCheck(member, "alice-acme", "alice")
	require.NoError(t, err)
	assert.Equal(t, "alice-acme", got.ID)

	// A teammate in the same org still cannot read the owner's project.
	_, err = svc.GetProjectWithUserCheck(member, "alice-acme", "bob")
	assert.ErrorIs(t, err, core.ErrProjectNotFound)
	_, err = svc.GetProjectWithUserCheck(member, "bob-acme", "alice")
	assert.ErrorIs(t, err, core.ErrProjectNotFound)
	_, err = svc.GetProjectWithUserCheck(member, "alice-solo", "bob")
	assert.ErrorIs(t, err, core.ErrProjectNotFound)

	// An owner who left the org loses the org's project but not their own.
	left := authz.WithMembership(ctx, liveOrgs{"alice/home": true})
	_, err = svc.GetProjectWithUserCheck(left, "alice-acme", "alice")
	assert.ErrorIs(t, err, core.ErrProjectNotFound)
	assert.ErrorIs(t, err, authz.ErrNotOrgMember)
	_, err = svc.GetProjectWithUserCheck(left, "alice-solo", "alice")
	require.NoError(t, err)
}
