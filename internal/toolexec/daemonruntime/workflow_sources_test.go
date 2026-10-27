package daemonruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
	"github.com/stretchr/testify/require"
)

func TestUnsafeWorkflowSourceDoesNotProduceEmptySnapshot(t *testing.T) {
	root := t.TempDir()
	writeRepoFile(t, filepath.Join(root, filepath.FromSlash(workflowref.Dir)), "flow.yaml", "name: flow")
	first, err := buildProjectSnapshot(root)
	require.NoError(t, err)
	require.Len(t, first.Workflows, 1)
	outside := filepath.Join(t.TempDir(), "secret.yaml")
	require.NoError(t, os.WriteFile(outside, []byte("name: secret"), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, filepath.FromSlash(workflowref.Dir), "leak.yaml")))
	failed, err := buildProjectSnapshot(root)
	require.Error(t, err)
	require.Nil(t, failed, "failed reads must not publish an authoritative empty workflow catalog")
	_, err = (&filesystemConfigStore{}).GetProjectConfigRecord(context.Background(), root)
	require.Error(t, err)
}

func TestWorkflowSourcesNestedRepos(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// Seed the same nested-repo source cache memories and skills consume, so
	// this test needs no git subprocess and remains fast under -short.
	repoSourcesMu.Lock()
	repoSourcesCache[root] = repoSourcesEntry{sources: []string{"forge", "control-plane"}, expires: time.Now().Add(repoSourcesTTL)}
	repoSourcesMu.Unlock()
	t.Cleanup(func() { repoSourcesMu.Lock(); delete(repoSourcesCache, root); repoSourcesMu.Unlock() })
	writeRepoFile(t, filepath.Join(root, "control-plane/.reliant/workflows"), "merge-steward.yaml", "name: merge-steward\n")
	writeRepoFile(t, filepath.Join(root, "forge/.reliant/workflows"), "build.yaml", "name: build\n")
	writeRepoFile(t, filepath.Join(root, "control-plane/.reliant/workflows/merge-steward/scenarios"), "check.yaml", "name: check\n")
	sources, err := readWorkflowSources(context.Background(), root)
	require.NoError(t, err)
	scenarios, _ := indexScenarios(sources)
	require.Len(t, scenarios, 1)
	require.Equal(t, "merge-steward", scenarios[0].WorkflowSlug)
	require.Equal(t, "control-plane/.reliant/workflows/merge-steward/scenarios/check.yaml", scenarios[0].RelativePath)
	indexed, version := indexWorkflows(sources)
	require.Len(t, indexed, 2)
	require.Equal(t, "control-plane/.reliant/workflows/merge-steward.yaml", indexed[0].RelativePath)
	record, err := (&filesystemConfigStore{}).GetProjectConfigRecord(context.Background(), root)
	require.NoError(t, err)
	stored, err := config.ParseStoredWorkflows(record.ProjectWorkflowsJSON)
	require.NoError(t, err)
	e, err := config.ProjectWorkflowIndex(stored).Lookup("merge-steward")
	require.NoError(t, err)
	require.Equal(t, "control-plane", e.Repo)
	require.NoError(t, os.WriteFile(filepath.Join(root, "control-plane/.reliant/workflows/merge-steward.yaml"), []byte("name: merge-steward\ndescription: changed\n"), 0600))
	changedSources, err := readWorkflowSources(context.Background(), root)
	require.NoError(t, err)
	_, changed := indexWorkflows(changedSources)
	require.NotEqual(t, version, changed, "nested edits must change snapshot hash")
	require.NoError(t, os.Remove(filepath.Join(root, "forge/.reliant/workflows/build.yaml")))
	remainingSources, err := readWorkflowSources(context.Background(), root)
	require.NoError(t, err)
	remaining, _ := indexWorkflows(remainingSources)
	require.Len(t, remaining, 1)
}
