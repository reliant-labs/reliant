// Copyright (c) 2025 Reliant Labs
package worktree

import (
	"context"
	"sort"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// recoveryMembers is a Membership in which the caller is a live member of
// exactly the named orgs, and whose default org is "home".
type recoveryMembers map[string]bool

func (m recoveryMembers) IsMember(_ context.Context, _, orgID string) (bool, bool, error) {
	return m[orgID], true, nil
}

func (recoveryMembers) DefaultOrg(context.Context, string) (string, error) { return "home", nil }

// ListWorktreeRecoveries leaves out recoveries of a project the caller may no
// longer read: a removed member's left org, and the other org of a credential
// bound to one. A recovery whose project is gone stays listed for its owner.
func TestListWorktreeRecoveries_LeavesOutAnOrgTheCallerMayNotRead(t *testing.T) {
	org := func(s string) *string { return &s }
	projectOrgs := map[string]*string{"p-acme": org("acme"), "p-home": org("home")}
	repo := &MockRepository{
		ListWorktreeRecoveriesFunc: func(context.Context, string, string) ([]*db.WorktreeRecovery, error) {
			return []*db.WorktreeRecovery{
				{ID: "acme", ProjectID: "p-acme"},
				{ID: "home", ProjectID: "p-home"},
				{ID: "gone", ProjectID: "p-gone"},
			}, nil
		},
		GetProjectFunc: func(_ context.Context, id string) (*db.Project, error) {
			if id == "p-gone" {
				return nil, core.ErrProjectNotFound
			}
			return &db.Project{ID: id, UserID: "test-user", OrgID: projectOrgs[id]}, nil
		},
	}
	svc := NewTestWorktree(t, WithWorktreeDeps(Deps{Repo: repo}))
	ids := func(ctx context.Context) []string {
		t.Helper()
		resp, err := svc.ListWorktreeRecoveries(ctx, connect.NewRequest(&reliantv1.ListWorktreeRecoveriesRequest{}))
		require.NoError(t, err)
		var out []string
		for _, r := range resp.Msg.GetRecoveries() {
			out = append(out, r.GetId())
		}
		sort.Strings(out)
		return out
	}

	removed := authz.WithMembership(AuthedContext(t), recoveryMembers{"home": true})
	require.Equal(t, []string{"gone", "home"}, ids(removed), "a removed member of acme")

	both := authz.WithMembership(AuthedContext(t), recoveryMembers{"home": true, "acme": true})
	require.Equal(t, []string{"acme", "gone", "home"}, ids(both), "a member of both")
	require.Equal(t, []string{"gone", "home"}, ids(authz.WithCredentialOrg(both, "home")), "a credential bound to home, the default org")
	// One whose project is gone is a row of no org: a credential bound to an
	// org other than the owner's default does not reach it.
	require.Equal(t, []string{"acme"}, ids(authz.WithCredentialOrg(both, "acme")), "a credential bound to acme, not the default org")
}
