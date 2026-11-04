package db

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/config"
)

// Each machine's snapshot carries the git state its files were read from;
// it is stored per machine, so the provenance of every copy of a workflow
// says which machine, file, branch and commit it is, and warns about a
// feature branch or uncommitted edits.
func TestProjectWorkflowProvenance_TwoMachines(t *testing.T) {
	p := newTwoMachineProject(t)
	ctx := context.Background()
	release := config.StoredWorkflow{Slug: "release", Name: "release", RelativePath: p.releasePath, YAMLContent: releaseYAML}
	push := func(daemonID string, at int64, states []config.SyncedRepoState) {
		t.Helper()
		wj, err := config.EncodeCatalogJSON([]config.StoredWorkflow{release})
		require.NoError(t, err)
		sj, err := config.EncodeCatalogJSON(states)
		require.NoError(t, err)
		_, err = p.repo.SyncProjectConfigSnapshot(ctx, &ProjectConfigRecord{
			ProjectID: p.projectID, DaemonID: daemonID, ProjectWorkflowsJSON: wj, RepoStatesJSON: sj,
			PushedAt: time.UnixMilli(at).UTC(),
		}, false)
		require.NoError(t, err)
	}
	const sha = "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b"
	push(p.cicdID, 1000, []config.SyncedRepoState{
		{Repo: "", Branch: "main", Commit: sha, DefaultBranch: "main"},
		{Repo: "control-plane", Branch: "main", Commit: sha, DefaultBranch: "main"},
	})
	push(p.defaultID, 2000, []config.SyncedRepoState{
		{Repo: "", Branch: "main", Commit: sha, DefaultBranch: "main"},
		{Repo: "control-plane", Branch: "feat/x", Commit: sha, Dirty: true, DefaultBranch: "main"},
	})

	provenance, err := p.repo.ProjectWorkflowProvenance(ctx, p.projectID)
	require.NoError(t, err)
	require.Len(t, provenance, 2)
	byMachine := map[string]config.WorkflowProvenance{}
	for _, pv := range provenance {
		byMachine[pv.Machine] = pv
	}
	cicd := byMachine["cicd"]
	assert.Equal(t, "machine cicd at control-plane/.reliant/workflows/release.yaml @ main 1a2b3c4, clean", cicd.Describe())
	assert.Empty(t, cicd.Warnings())

	def := byMachine["default"]
	assert.Equal(t, "machine default at control-plane/.reliant/workflows/release.yaml @ feat/x 1a2b3c4, uncommitted changes", def.Describe())
	warnings := strings.Join(def.Warnings(), "\n")
	assert.Contains(t, warnings, "has control-plane checked out on feat/x, not main")
	assert.Contains(t, warnings, "uncommitted changes under control-plane/.reliant")

	// A daemon that does not report it leaves the state unknown, not clean.
	push(p.cicdID, 3000, nil)
	provenance, err = p.repo.ProjectWorkflowProvenance(ctx, p.projectID)
	require.NoError(t, err)
	for _, pv := range provenance {
		if pv.Machine == "cicd" {
			assert.Nil(t, pv.Repo)
			assert.Equal(t, "machine cicd at "+p.releasePath, pv.Describe())
		}
	}
}
