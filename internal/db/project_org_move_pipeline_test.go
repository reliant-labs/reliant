// Copyright (c) 2026 Reliant Labs
package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// A project with a repository enrolled in the pipeline, mode shadow or on,
// does not move to another org: its pipeline rows carry the org (pipeline
// redesign §13, G1). Setting the mode off (unenrolling) lets it move.
func TestMoveProjectOrg_RefusesWhileAPipelineRepoIsEnrolled(t *testing.T) {
	f := newMoveFixture(t)
	ctx := context.Background()
	f.project("p-pipe", strp(moveSrc))

	enrolled, err := f.repo.ProjectPipelineEnrolled(ctx, "p-pipe")
	require.NoError(t, err)
	assert.False(t, enrolled, "nothing enrolled")

	repo, err := f.repo.UpsertPipelineRepo(ctx, core.PipelineRepoEnrolment{
		OrgID: moveSrc, ProjectID: "p-pipe", Provider: "github", AccountKey: "inst-1",
		RepositoryID: "42", Repository: "acme/widgets", CreatedByUserID: moveOwner,
	})
	require.NoError(t, err)
	enrolled, err = f.repo.ProjectPipelineEnrolled(ctx, "p-pipe")
	require.NoError(t, err)
	assert.False(t, enrolled, "enrolled with mode off: nothing acts, the project may move")

	repo, err = f.repo.SetPipelineRepoMode(ctx, repo.ID, repo.Version, core.PipelineRepoModeShadow)
	require.NoError(t, err)
	enrolled, err = f.repo.ProjectPipelineEnrolled(ctx, "p-pipe")
	require.NoError(t, err)
	assert.True(t, enrolled)

	move := f.prepared("p-pipe")
	_, err = f.commit(ctx, core.ProjectOrgMoveCommit{MoveID: move.ID})
	require.ErrorIs(t, err, core.ErrProjectPipelineEnrolled)
	assert.Contains(t, err.Error(), "unenroll first")
	assert.Equal(t, strp(moveSrc), f.org("projects", "p-pipe"), "a refused move moves nothing")
	got, err := store.GetProjectOrgMove(ctx, f.repo.ORM(), move.ID)
	require.NoError(t, err)
	assert.Equal(t, core.ProjectOrgMoveStatePrepared, got.State)

	_, err = f.repo.SetPipelineRepoMode(ctx, repo.ID, repo.Version, core.PipelineRepoModeOff)
	require.NoError(t, err)
	_, err = f.commit(ctx, core.ProjectOrgMoveCommit{MoveID: move.ID})
	require.NoError(t, err, "unenrolled: the move commits")
	assert.Equal(t, strp(moveDst), f.org("projects", "p-pipe"))
}
