// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A project's root reliant.local.md is personal context exactly as a nested
// repo's is: read beside the root reliant.md, after it, on every surface that
// reports the project memory. It used to be read only inside nested repos, so
// a single-repo project's local file was silently ignored.
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
