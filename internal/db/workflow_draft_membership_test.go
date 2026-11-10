package db

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/authz"
)

// liveIn is a membership of the orgs it names only.
type liveIn map[string]bool

func (m liveIn) IsMember(_ context.Context, _, orgID string) (bool, bool, error) {
	return m[orgID], true, nil
}

// A saved workflow of an org its owner has left is theirs by no key: the
// lookups by slug and name, the runnable lookup and the list miss it, and the
// writes addressed by slug neither overwrite nor delete it. A live member's
// drafts and drafts with no org are unaffected.
func TestWorkflowDrafts_RemovedMemberReachesNoneByAlternateKey(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	userID := "user-draft-member-" + uuid.NewString()[:8]
	ctx := authz.WithMembership(context.Background(), liveIn{"home": true})

	left, home, legacy := newDraftForTest(userID), newDraftForTest(userID), newDraftForTest(userID)
	globex, homeOrg := "globex", "home"
	left.OrgID, home.OrgID = &globex, &homeOrg
	left.Name, home.Name, legacy.Name = "Left Behind", "At Home", "Before Orgs"
	left.Status = WorkflowDraftStatusComplete
	for _, d := range []*WorkflowDraft{left, home, legacy} {
		require.NoError(t, repo.CreateWorkflowDraft(ctx, d))
	}

	got, err := repo.GetWorkflowDraftBySlug(ctx, userID, left.Slug)
	require.NoError(t, err)
	require.Nil(t, got, "by slug")
	got, err = repo.GetWorkflowDraftByName(ctx, userID, left.Name)
	require.NoError(t, err)
	require.Nil(t, got, "by name")
	got, err = repo.GetUsableWorkflowBySlug(ctx, userID, left.Slug)
	require.NoError(t, err)
	require.Nil(t, got, "runnable by slug")

	listed, err := repo.ListWorkflowDraftsByUser(ctx, userID)
	require.NoError(t, err)
	ids := make([]string, 0, len(listed))
	for _, d := range listed {
		ids = append(ids, d.ID)
	}
	require.ElementsMatch(t, []string{home.ID, legacy.ID}, ids, "the list holds the live org's and the unassigned drafts")

	for _, d := range []*WorkflowDraft{home, legacy} {
		got, err := repo.GetWorkflowDraftBySlug(ctx, userID, d.Slug)
		require.NoError(t, err)
		require.NotNil(t, got, "a visible draft by slug: %s", d.Name)
		got, err = repo.GetWorkflowDraftByName(ctx, userID, d.Name)
		require.NoError(t, err)
		require.NotNil(t, got, "a visible draft by name: %s", d.Name)
	}

	overwrite := *left
	overwrite.ID = uuid.NewString()
	overwrite.OrgID = nil
	overwrite.Definition = "name: hijacked\n"
	_, err = repo.UpsertWorkflowDraft(ctx, &overwrite)
	require.ErrorIs(t, err, ErrWorkflowSlugTaken, "an upsert onto a denied draft's slug")
	require.NoError(t, repo.DeleteWorkflowDraftBySlug(ctx, userID, left.Slug))

	kept, err := repo.GetWorkflowDraft(ctx, left.ID)
	require.NoError(t, err, "the denied draft survives the delete")
	require.Equal(t, left.Definition, kept.Definition, "and is not overwritten")
}
