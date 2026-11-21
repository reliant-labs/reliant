package db

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/authz"
)

// One batched read answers, for every slug it was read for, exactly what the
// two single lookups answer: a complete workflow, an unfinished one (not
// runnable), hidden ones, one of an org its owner has left, another user's
// of the same slug, and a slug nobody has.
func TestWorkflowDraftsBySlug_AnswersAsTheSingleLookups(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	userID := "user-drafts-batch-" + uuid.NewString()[:8]
	ctx := authz.WithMembership(context.Background(), liveIn{"home": true})
	globex := "globex"

	draft := func(name string, status WorkflowDraftStatus, hidden bool, orgID *string) *WorkflowDraft {
		d := newDraftForTest(userID)
		d.Name, d.Slug, d.Status, d.IsHidden, d.OrgID = name, name+"-"+uuid.NewString()[:6], status, hidden, orgID
		require.NoError(t, repo.CreateWorkflowDraft(ctx, d))
		return d
	}
	drafts := []*WorkflowDraft{
		draft("complete", WorkflowDraftStatusComplete, false, nil),
		draft("unfinished", WorkflowDraftStatusDraft, false, nil),
		draft("hidden-complete", WorkflowDraftStatusComplete, true, nil),
		draft("hidden-unfinished", WorkflowDraftStatusDraft, true, nil),
		draft("left-org-complete", WorkflowDraftStatusComplete, false, &globex),
		draft("left-org-unfinished", WorkflowDraftStatusDraft, false, &globex),
	}
	// Another user's workflow of a slug the caller asks for is not theirs.
	other := newDraftForTest("user-drafts-other-" + uuid.NewString()[:8])
	other.Slug, other.Status = "theirs-"+uuid.NewString()[:6], WorkflowDraftStatusComplete
	require.NoError(t, repo.CreateWorkflowDraft(ctx, other))

	slugs := []string{"nobody-has-this", other.Slug}
	for _, d := range drafts {
		slugs = append(slugs, d.Slug)
	}
	batch, err := repo.GetWorkflowDraftsBySlugs(ctx, userID, slugs)
	require.NoError(t, err)

	for _, slug := range slugs {
		wantDraft, wantErr := repo.GetWorkflowDraftBySlug(ctx, userID, slug)
		gotDraft, gotErr := batch.Draft(ctx, slug)
		require.Equal(t, wantErr, gotErr, "draft %s", slug)
		require.Equal(t, wantDraft, gotDraft, "draft %s", slug)

		wantUsable, wantErr := repo.GetUsableWorkflowBySlug(ctx, userID, slug)
		gotUsable, gotErr := batch.Usable(ctx, slug)
		require.Equal(t, wantErr, gotErr, "usable %s", slug)
		require.Equal(t, wantUsable, gotUsable, "usable %s", slug)
	}

	// The cases are the ones meant: one runnable, one reported unfinished.
	usable, err := batch.Usable(ctx, drafts[0].Slug)
	require.NoError(t, err)
	require.NotNil(t, usable)
	_, err = batch.Usable(ctx, drafts[1].Slug)
	var notRunnable *WorkflowDraftNotRunnableError
	require.ErrorAs(t, err, &notRunnable)
}
