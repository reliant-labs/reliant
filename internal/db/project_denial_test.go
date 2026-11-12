// Copyright (c) 2025 Reliant Labs

package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// down is a membership whose every answer is an outage.
type down struct{}

func (down) IsMember(context.Context, string, string) (bool, bool, error) {
	return false, true, errors.New("control-plane unavailable")
}

// The project check stays NotFound to every caller, whoever is refused and
// why, and carries why as its withheld cause: the owner having left the
// project's org (an answer) apart from an outage (not one).
func TestGetProjectWithUserCheck_RefusalsCarryWhy(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	userID := "user-project-denial-" + uuid.NewString()[:8]
	acme, now := "acme", time.Now().UTC()
	project := &Project{ID: uuid.NewString(), UserID: userID, OrgID: &acme, Name: "p", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, repo.CreateProject(context.Background(), project))

	for name, tc := range map[string]struct {
		ctx    context.Context
		caller string
		cause  error
	}{
		"the owner, who left the org": {ctx: authz.WithMembership(context.Background(), liveIn{"home": true}), caller: userID, cause: authz.ErrNotOrgMember},
		"an outage":                   {ctx: authz.WithMembership(context.Background(), down{}), caller: userID, cause: authz.ErrMembershipUnavailable},
		"another user":                {ctx: authz.WithMembership(context.Background(), liveIn{"acme": true}), caller: "someone-else"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := repo.GetProjectWithUserCheck(tc.ctx, project.ID, tc.caller)
			require.Error(t, err)
			assert.ErrorIs(t, err, core.ErrProjectNotFound)
			assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
			if tc.cause != nil {
				assert.ErrorIs(t, err, tc.cause)
			}
			for _, other := range []error{authz.ErrNotOrgMember, authz.ErrMembershipUnavailable} {
				if other != tc.cause {
					assert.NotErrorIs(t, err, other)
				}
			}
		})
	}

	got, err := repo.GetProjectWithUserCheck(authz.WithMembership(context.Background(), liveIn{"acme": true}), project.ID, userID)
	require.NoError(t, err)
	assert.Equal(t, project.ID, got.ID)
}
