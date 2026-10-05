package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func draftUpdates(t *testing.T, repo *Repo, userID string) []UserUpdate {
	t.Helper()
	all, err := repo.GetUserUpdatesSince(context.Background(), userID, 0, 100)
	require.NoError(t, err)
	var out []UserUpdate
	for _, u := range all {
		if u.UpdateType == UserUpdateWorkflowDraftUpdated {
			out = append(out, u)
		}
	}
	return out
}

func requireDraftUpdate(t *testing.T, u UserUpdate, draftID, slug string, version int64, deleted ...bool) {
	deletedFlag := len(deleted) > 0 && deleted[0]
	t.Helper()
	require.Equal(t, EntityTypeWorkflowDraft, u.EntityType)
	require.Equal(t, draftID, u.EntityID)
	require.Nil(t, u.ProjectID)
	require.Nil(t, u.ChatID)
	var data struct {
		DraftID string `json:"draft_id"`
		Slug    string `json:"slug"`
		Version int64  `json:"version"`
		Deleted bool   `json:"deleted"`
	}
	require.NoError(t, json.Unmarshal(u.Data, &data))
	require.Equal(t, deletedFlag, data.Deleted)
	require.Equal(t, draftID, data.DraftID)
	require.Equal(t, slug, data.Slug)
	require.Equal(t, version, data.Version)
}

func newDraftForTest(userID string) *WorkflowDraft {
	now := time.Now().UTC()
	return &WorkflowDraft{
		ID:         uuid.NewString(),
		UserID:     userID,
		Name:       "Draft Update Test",
		Slug:       "draft-update-" + uuid.NewString()[:8],
		Definition: "name: x\napiVersion: v2\n",
		Status:     WorkflowDraftStatusDraft,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

func TestWorkflowDraftWrites_EachPublishesOneUpdate(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	userID := "user-draft-updates-" + uuid.NewString()[:8]

	draft := newDraftForTest(userID)
	require.NoError(t, repo.CreateWorkflowDraft(ctx, draft))
	ups := draftUpdates(t, repo, userID)
	require.Len(t, ups, 1)
	requireDraftUpdate(t, ups[0], draft.ID, draft.Slug, 1)

	steps := []struct {
		name    string
		write   func() error
		version int64
	}{
		{"update", func() error { draft.Name = "renamed"; return repo.UpdateWorkflowDraft(ctx, draft) }, 2},
		{"update definition", func() error {
			return repo.UpdateWorkflowDraftDefinition(ctx, draft.ID, draft.Name, draft.Slug, "name: y\n", WorkflowDraftStatusDraft)
		}, 3},
		{"set status", func() error {
			_, err := repo.SetWorkflowDraftStatus(ctx, draft.ID, WorkflowDraftStatusComplete)
			return err
		}, 4},
		{"set hidden", func() error { _, err := repo.SetWorkflowDraftHidden(ctx, draft.ID, true); return err }, 5},
		{"upsert", func() error {
			up := *draft
			_, err := repo.UpsertWorkflowDraft(ctx, &up)
			return err
		}, 6},
	}
	for i, step := range steps {
		require.NoError(t, step.write(), step.name)
		ups = draftUpdates(t, repo, userID)
		require.Len(t, ups, i+2, step.name)
		requireDraftUpdate(t, ups[len(ups)-1], draft.ID, draft.Slug, step.version)
	}

	require.NoError(t, repo.DeleteWorkflowDraft(ctx, draft.ID))
	ups = draftUpdates(t, repo, userID)
	require.Len(t, ups, len(steps)+2)
	requireDraftUpdate(t, ups[len(ups)-1], draft.ID, draft.Slug, 6, true)

	second := newDraftForTest(userID)
	require.NoError(t, repo.CreateWorkflowDraft(ctx, second))
	require.NoError(t, repo.DeleteWorkflowDraftBySlug(ctx, userID, second.Slug))
	ups = draftUpdates(t, repo, userID)
	require.Len(t, ups, len(steps)+4)
	requireDraftUpdate(t, ups[len(ups)-1], second.ID, second.Slug, 1, true)
}

func TestWorkflowDraftWrite_RolledBackEmitsNothing(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	userID := "user-draft-rollback-" + uuid.NewString()[:8]

	draft := newDraftForTest(userID)
	boom := errors.New("boom")
	err := repo.RunTx(ctx, func(txCtx context.Context) error {
		if err := repo.CreateWorkflowDraft(txCtx, draft); err != nil {
			return err
		}
		return boom
	})
	require.ErrorIs(t, err, boom)
	require.Empty(t, draftUpdates(t, repo, userID))

	// A write that fails on its own (duplicate slug) also announces nothing.
	require.NoError(t, repo.CreateWorkflowDraft(ctx, draft))
	require.Len(t, draftUpdates(t, repo, userID), 1)
	dup := newDraftForTest(userID)
	dup.Slug = draft.Slug
	start := time.Now()
	err = repo.CreateWorkflowDraft(ctx, dup)
	require.ErrorIs(t, err, ErrWorkflowSlugTaken)
	require.Contains(t, err.Error(), draft.Slug)
	require.Less(t, time.Since(start), 700*time.Millisecond, "a unique violation must not walk the retry ladder")
	require.Len(t, draftUpdates(t, repo, userID), 1)
}

func TestUpdateWorkflowForkedFrom_PublishesVersionBump(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	userID := "user-draft-fork-" + uuid.NewString()[:8]

	draft := newDraftForTest(userID)
	require.NoError(t, repo.CreateWorkflowDraft(ctx, draft))
	saved, err := repo.UpdateWorkflowForkedFrom(ctx, draft.ID, "origin-slug")
	require.NoError(t, err)
	require.EqualValues(t, 2, saved.Version)
	ups := draftUpdates(t, repo, userID)
	require.Len(t, ups, 2)
	requireDraftUpdate(t, ups[1], draft.ID, draft.Slug, 2)
}

func TestWorkflowDraftWrites_RenameCollisionIsDomainError(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	userID := "user-draft-rename-" + uuid.NewString()[:8]

	a, b := newDraftForTest(userID), newDraftForTest(userID)
	require.NoError(t, repo.CreateWorkflowDraft(ctx, a))
	require.NoError(t, repo.CreateWorkflowDraft(ctx, b))
	b.Slug = a.Slug
	require.ErrorIs(t, repo.UpdateWorkflowDraft(ctx, b), ErrWorkflowSlugTaken)
	require.ErrorIs(t, repo.UpdateWorkflowDraftDefinition(ctx, b.ID, b.Name, a.Slug, "name: z\n", WorkflowDraftStatusDraft), ErrWorkflowSlugTaken)
	_, err := repo.UpsertWorkflowDraft(ctx, &WorkflowDraft{ID: uuid.NewString(), UserID: userID, Name: "n", Slug: a.Slug, Definition: "x", Status: WorkflowDraftStatusDraft})
	require.NoError(t, err) // upsert resolves the conflict by updating, never erroring
}
