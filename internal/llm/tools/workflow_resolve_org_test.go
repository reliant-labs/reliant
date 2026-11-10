// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// leftGlobex: test-user is a live member of acme, and has left globex.
type leftGlobex struct{}

func (leftGlobex) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	return userID == "test-user" && orgID == "acme", true, nil
}

// A workflow in an org its owner has left resolves for no tool, by UUID, slug
// or name; one in an org they are still in resolves by all three.
func TestResolveWorkflowDraft_NeedsLiveMembership(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	create := func(org string) *db.WorkflowDraft {
		now := time.Now()
		unique := uuid.New().String()[:8]
		draft := &db.WorkflowDraft{
			ID: uuid.New().String(), UserID: "test-user", OrgID: &org,
			Name: "Org Draft " + unique, Slug: "org-draft-" + unique,
			Definition: validWorkflowYAML, Status: db.WorkflowDraftStatusComplete,
			CreatedAt: now, UpdatedAt: now,
		}
		require.NoError(t, repo.CreateWorkflowDraft(context.Background(), draft))
		return draft
	}
	kept, left := create("acme"), create("globex")

	base := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	ctx := rctx.NewToolContext(authz.WithMembership(base, leftGlobex{}), "", "", nil, nil)

	for _, ref := range []string{kept.ID, kept.Slug, kept.Name} {
		got, err := resolveWorkflowDraft(ctx, repo, ref)
		require.NoError(t, err, "a live member's workflow by %q", ref)
		require.Equal(t, kept.ID, got.ID)
	}
	for _, ref := range []string{left.ID, left.Slug, left.Name} {
		got, err := resolveWorkflowDraft(ctx, repo, ref)
		require.Error(t, err, "a workflow of an org the caller left, by %q", ref)
		require.Nil(t, got)
		require.Contains(t, err.Error(), "workflow not found")
	}
}
