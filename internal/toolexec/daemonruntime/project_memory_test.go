// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/stretchr/testify/require"
)

// The root's personal memory follows its shared notes on both config paths.
func TestProjectMemory_RootLocalFileLoadsAfterReliantMD(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeRepoFile(t, root, "reliant.md", "shared notes")
	writeRepoFile(t, root, "reliant.local.md", "my notes")

	snapshot, err := buildProjectSnapshot(root)
	require.NoError(t, err)
	require.Equal(t, "shared notes\n\nmy notes", string(snapshot.GetProjectMemoryMd()),
		"the daemon's synced snapshot carries the root local file after reliant.md")

	record, err := (&filesystemConfigStore{}).GetProjectConfigRecord(context.Background(), root)
	require.NoError(t, err)
	require.NotNil(t, record.ProjectMemoryMD)
	require.Equal(t, "shared notes\n\nmy notes", *record.ProjectMemoryMD,
		"the daemon's own config store reads the same project memory")
}

func TestProjectMemory_RootLocalFileAlone(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeRepoFile(t, root, "reliant.local.md", "only mine")

	snapshot, err := buildProjectSnapshot(root)
	require.NoError(t, err)
	require.Equal(t, "only mine", string(snapshot.GetProjectMemoryMd()))
}

func TestProjectMemory_RootLocalFileEditChangesConfigVersion(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeRepoFile(t, root, "reliant.local.md", "before")
	first, err := buildProjectSnapshot(root)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(root, "reliant.local.md"), []byte("after"), 0o644))
	second, err := buildProjectSnapshot(root)
	require.NoError(t, err)
	require.NotEqual(t, first.GetConfigVersion(), second.GetConfigVersion(),
		"an edit to the root local file must re-sync the snapshot")
}

func TestProjectMemory_NestedFilesDoNotSync(t *testing.T) {
	global := t.TempDir()
	config.UseUserConfigDirForTest(t, global)
	writeRepoFile(t, global, "reliant.md", "global notes")

	root := t.TempDir()
	writeRepoFile(t, root, "forge.yaml", minimalForgeYAML)
	writeRepoFile(t, root, "reliant.md", "root shared notes")
	writeRepoFile(t, root, "reliant.local.md", "root local notes")
	nested := filepath.Join(root, "api")
	writeRepoFile(t, filepath.Join(nested, ".git"), "HEAD", shaA+"\n")
	writeRepoFile(t, nested, "forge.yaml", "name: nested-forge\n")
	writeRepoFile(t, nested, "reliant.md", "nested shared notes")
	writeRepoFile(t, nested, "reliant.local.md", "nested local notes")
	writeRepoFile(t, filepath.Join(nested, ".reliant/presets"), "implementer.yaml", "params: {}\n")
	writeRepoFile(t, filepath.Join(nested, ".reliant/skills/api-skill"), "SKILL.md", "---\nname: api-skill\ndescription: API skill\n---\nAPI instructions\n")
	writeRepoFile(t, filepath.Join(nested, ".reliant/workflows"), "build.yaml", "name: build\n")
	writeRepoFile(t, filepath.Join(nested, ".reliant/workflows/build/scenarios"), "check.yaml", "name: check\n")

	first, err := buildProjectSnapshot(root)
	require.NoError(t, err)
	require.Empty(t, first.GetRepoMemoriesMd())
	require.Equal(t, "global notes", string(first.GetGlobalMemoryMd()))
	projectMemory := string(first.GetProjectMemoryMd())
	require.Contains(t, projectMemory, "# testproj", "root Forge framework guide still syncs")
	require.True(t, strings.HasSuffix(projectMemory, "root shared notes\n\nroot local notes"))
	require.NotContains(t, projectMemory, "nested shared notes")
	require.NotContains(t, projectMemory, "nested local notes")
	require.NotContains(t, projectMemory, "nested-forge")
	require.Len(t, first.GetRepoStates(), 1)
	require.Equal(t, "api", first.GetRepoStates()[0].GetRepo())
	require.Equal(t, shaA, first.GetRepoStates()[0].GetCommitSha())
	require.Len(t, first.GetPresets(), 1)
	require.Equal(t, "api/.reliant/presets/implementer.yaml", first.GetPresets()[0].GetRelativePath())
	require.Len(t, first.GetWorkflows(), 1)
	require.Equal(t, "api/.reliant/workflows/build.yaml", first.GetWorkflows()[0].GetRelativePath())
	require.Len(t, first.GetScenarios(), 1)
	require.Equal(t, "api/.reliant/workflows/build/scenarios/check.yaml", first.GetScenarios()[0].GetRelativePath())
	require.True(t, slices.ContainsFunc(first.GetSkills(), func(s *reliantv1.IndexedSkill) bool {
		return s.GetSource() == "api" && s.GetName() == "api-skill"
	}), "nested custom skills still sync")

	store := &filesystemConfigStore{}
	record, err := store.GetProjectConfigRecord(context.Background(), root)
	require.NoError(t, err)
	require.Nil(t, record.RepoMemoriesJSON)
	require.Equal(t, "global notes", *record.GlobalMemoryMD)
	require.Equal(t, projectMemory, *record.ProjectMemoryMD)

	writeRepoFile(t, nested, "reliant.md", "updated nested shared notes")
	writeRepoFile(t, nested, "reliant.local.md", "updated nested local notes")
	second, err := buildProjectSnapshot(root)
	require.NoError(t, err)
	require.Empty(t, second.GetRepoMemoriesMd())
	require.Equal(t, first.GetConfigVersion(), second.GetConfigVersion(),
		"edits to nested memory alone must not trigger config sync")

	writeRepoFile(t, filepath.Join(nested, ".git"), "HEAD", shaB+"\n")
	third, err := buildProjectSnapshot(root)
	require.NoError(t, err)
	require.NotEqual(t, second.GetConfigVersion(), third.GetConfigVersion(), "nested repo state still participates in sync")
}
