// Copyright (c) 2025 Reliant Labs
package projectaccess_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/projectaccess"
)

// members answers IsMember from a (user/org) table; an absent pair is not a member.
type members map[string]bool

func (m members) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	return m[userID+"/"+orgID], true, nil
}

type outage struct{}

func (outage) IsMember(context.Context, string, string) (bool, bool, error) {
	return false, true, errors.New("control-plane unavailable")
}

// ownerStore is the owner-filtered read the real store does: a project is
// returned only to its owner, else core.ErrProjectNotFound.
type ownerStore struct{ rows map[string]*core.Project }

func (s ownerStore) GetProjectWithUserCheck(_ context.Context, id, userID string) (*core.Project, error) {
	if p, ok := s.rows[id]; ok && p.UserID == userID {
		return p, nil
	}
	return nil, core.ErrProjectNotFound
}

func org(s string) *string { return &s }

func newService() projectaccess.Service {
	return projectaccess.New(projectaccess.Deps{Store: ownerStore{rows: map[string]*core.Project{
		"mine":    {ID: "mine", UserID: "alice"},
		"in-acme": {ID: "in-acme", UserID: "alice", OrgID: org("acme")},
		"theirs":  {ID: "theirs", UserID: "bob", OrgID: org("acme")},
	}}})
}

// Another tenant's project, a missing one and a refused one are the same
// answer: NotFound, so ids cannot be probed.
func TestGetProjectWithUserCheck_CrossTenantIsNotFound(t *testing.T) {
	t.Parallel()
	svc := newService()
	ctx := authz.WithMembership(context.Background(), members{"alice/acme": true, "bob/acme": true})

	for name, id := range map[string]string{"another user's": "theirs", "missing": "nope"} {
		t.Run(name, func(t *testing.T) {
			got, err := svc.GetProjectWithUserCheck(ctx, id, "alice")
			require.Error(t, err)
			assert.Nil(t, got)
			assert.ErrorIs(t, err, core.ErrProjectNotFound)
			assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
		})
	}

	got, err := svc.GetProjectWithUserCheck(ctx, "mine", "alice")
	require.NoError(t, err)
	assert.Equal(t, "mine", got.ID)
	got, err = svc.GetProjectWithUserCheck(ctx, "in-acme", "alice")
	require.NoError(t, err)
	assert.Equal(t, "in-acme", got.ID)
}

// An owner who left the project's org, and an outage of the membership answer,
// are NotFound too, each carrying its own withheld cause and never the other's.
func TestGetProjectWithUserCheck_OrgRefusalsCarryWhy(t *testing.T) {
	t.Parallel()
	svc := newService()
	for name, tc := range map[string]struct {
		ctx   context.Context
		cause error
	}{
		"the owner left the org": {authz.WithMembership(context.Background(), members{"alice/home": true}), authz.ErrNotOrgMember},
		"membership outage":      {authz.WithMembership(context.Background(), outage{}), authz.ErrMembershipUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.GetProjectWithUserCheck(tc.ctx, "in-acme", "alice")
			require.Error(t, err)
			assert.ErrorIs(t, err, core.ErrProjectNotFound)
			assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
			assert.ErrorIs(t, err, tc.cause)
			for _, other := range []error{authz.ErrNotOrgMember, authz.ErrMembershipUnavailable} {
				if other != tc.cause {
					assert.NotErrorIs(t, err, other)
				}
			}
		})
	}
}

// A credential bound to another org cannot reach the project, even for its owner.
func TestGetProjectWithUserCheck_CredentialOfAnotherOrgIsRefused(t *testing.T) {
	t.Parallel()
	svc := newService()
	ctx := authz.WithCredentialOrg(authz.WithMembership(context.Background(), members{"alice/acme": true, "alice/globex": true}), "globex")
	_, err := svc.GetProjectWithUserCheck(ctx, "in-acme", "alice")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrProjectNotFound)
}

// An empty id or caller never reaches the store: an empty owner would
// otherwise be a filter that matches no, or every, row.
func TestGetProjectWithUserCheck_EmptyScopeFailsClosed(t *testing.T) {
	t.Parallel()
	called := false
	svc := projectaccess.New(projectaccess.Deps{Store: storeFunc(func() { called = true })})
	for _, c := range [][2]string{{"", "alice"}, {"mine", ""}, {"", ""}} {
		_, err := svc.GetProjectWithUserCheck(context.Background(), c[0], c[1])
		require.Error(t, err)
		assert.NotErrorIs(t, err, core.ErrProjectNotFound, "an empty scope is a caller bug, not a miss")
	}
	assert.False(t, called)
}

type storeFunc func()

func (f storeFunc) GetProjectWithUserCheck(context.Context, string, string) (*core.Project, error) {
	f()
	return nil, errors.New("unreachable")
}
