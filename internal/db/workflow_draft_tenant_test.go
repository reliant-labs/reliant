package db

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/authz"
)

// One user's saved workflows are reachable by none of another user's keys:
// every read by slug or name answers nothing, the list holds only their own,
// and a write addressed by slug (delete, upsert) touches only the writer's
// own row, never the other user's of the same slug.
func TestWorkflowDrafts_AnotherUsersDraftsAreUnreachableByAnyKey(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	drafts := testDrafts(t, repo)
	owner := "user-tenant-owner-" + uuid.NewString()[:8]
	other := "user-tenant-other-" + uuid.NewString()[:8]
	ctx := authz.WithMembership(context.Background(), liveIn{})

	mine := newDraftForTest(owner)
	mine.Name, mine.Status = "Tenant Flow", WorkflowDraftStatusComplete
	require.NoError(t, drafts.CreateWorkflowDraft(ctx, mine))

	got, err := drafts.GetWorkflowDraftBySlug(ctx, other, mine.Slug)
	require.NoError(t, err)
	require.Nil(t, got, "by slug")
	got, err = drafts.GetWorkflowDraftByName(ctx, other, mine.Name)
	require.NoError(t, err)
	require.Nil(t, got, "by name")
	got, err = drafts.GetUsableWorkflowBySlug(ctx, other, mine.Slug)
	require.NoError(t, err)
	require.Nil(t, got, "runnable by slug")
	listed, err := drafts.ListWorkflowDraftsByUser(ctx, other)
	require.NoError(t, err)
	require.Empty(t, listed, "the other user's list")
	batch, err := drafts.GetWorkflowDraftsBySlugs(ctx, other, []string{mine.Slug})
	require.NoError(t, err)
	got, err = batch.Draft(ctx, mine.Slug)
	require.NoError(t, err)
	require.Nil(t, got, "by batched slug")

	// A delete by slug under the other user's id removes nothing of the owner's.
	require.NoError(t, drafts.DeleteWorkflowDraftBySlug(ctx, other, mine.Slug))
	kept, err := drafts.GetWorkflowDraft(ctx, mine.ID)
	require.NoError(t, err, "the owner's draft survives another user's delete")
	require.Equal(t, mine.Definition, kept.Definition)

	// An upsert of the same slug by the other user is their own row, not an
	// overwrite of the owner's.
	theirs := newDraftForTest(other)
	theirs.Slug, theirs.Definition = mine.Slug, "name: theirs\n"
	saved, err := drafts.UpsertWorkflowDraft(ctx, theirs)
	require.NoError(t, err)
	require.NotEqual(t, mine.ID, saved.ID)
	kept, err = drafts.GetWorkflowDraft(ctx, mine.ID)
	require.NoError(t, err)
	require.Equal(t, mine.Definition, kept.Definition, "the owner's definition is not overwritten")
	require.Equal(t, owner, kept.UserID)
}
