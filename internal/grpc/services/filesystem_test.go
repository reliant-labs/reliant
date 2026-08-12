package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf8"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestFileSystemService(t *testing.T) (*FileSystemService, string) {
	t.Helper()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	// SetupTestDB seeds "test-project" at a sentinel path; point it at this
	// test's temp dir so path-relative file reads resolve there.
	projectPath := t.TempDir()
	_, err := repo.DB.ExecContext(
		context.Background(),
		`INSERT INTO projects (id, user_id, name, path, created_at, updated_at, last_active)
		 VALUES ($1, $2, $3, $4, NOW(), NOW(), NOW())
		 ON CONFLICT (id) DO UPDATE SET path = EXCLUDED.path`,
		"test-project",
		"test-user",
		"Test Project",
		projectPath,
	)
	require.NoError(t, err)

	return NewFileSystemService(repo), projectPath
}

func TestFileSystemService_GetFileContent_AllowsRawPDFContent(t *testing.T) {
	svc, projectPath := setupTestFileSystemService(t)

	pdfPath := filepath.Join(projectPath, "spec.pdf")
	require.NoError(t, os.WriteFile(pdfPath, []byte("%PDF-1.4\n1 0 obj\nstream\xff\nendstream\n"), 0644))

	resp, err := svc.GetFileContent(context.Background(), connect.NewRequest(&reliantv1.GetFileContentRequest{
		ProjectId: "test-project",
		Path:      "spec.pdf",
	}))
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Contains(t, resp.Msg.Content, "%PDF-1.4")
	assert.True(t, utf8.ValidString(resp.Msg.Content))
}

func TestFileSystemService_GetFileContent_RejectsNonPDFBinaryFiles(t *testing.T) {
	svc, projectPath := setupTestFileSystemService(t)

	zipPath := filepath.Join(projectPath, "archive.zip")
	require.NoError(t, os.WriteFile(zipPath, []byte{'P', 'K', 0x03, 0x04, 0x00, 0x00}, 0644))

	_, err := svc.GetFileContent(context.Background(), connect.NewRequest(&reliantv1.GetFileContentRequest{
		ProjectId: "test-project",
		Path:      "archive.zip",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

// GetFileTree serves a live, depth-limited walk: files report real sizes,
// depth=1 returns only immediate children with has_children set on non-empty
// directories, and depth=0 returns the full recursive tree (back-compat).
func TestFileSystemService_GetFileTree_DepthAndLiveWalk(t *testing.T) {
	svc, projectPath := setupTestFileSystemService(t)

	require.NoError(t, os.WriteFile(filepath.Join(projectPath, "keep.txt"), []byte("hello world"), 0o644)) // 11 bytes
	require.NoError(t, os.MkdirAll(filepath.Join(projectPath, "pkg", "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectPath, "pkg", "inner.txt"), []byte("b"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(projectPath, "pkg", "sub", "deep.txt"), []byte("c"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(projectPath, "empty"), 0o755))

	getTree := func(depth int32) []*reliantv1.FileNode {
		resp, err := svc.GetFileTree(context.Background(), connect.NewRequest(&reliantv1.GetFileTreeRequest{
			ProjectId:  "test-project",
			ShowHidden: false,
			Depth:      depth,
		}))
		require.NoError(t, err)
		return resp.Msg.Files
	}
	find := func(nodes []*reliantv1.FileNode, name string) *reliantv1.FileNode {
		for _, n := range nodes {
			if n.Name == name {
				return n
			}
		}
		return nil
	}

	// depth 1: immediate children only, real sizes, has_children hints.
	lvl1 := getTree(1)
	keep := find(lvl1, "keep.txt")
	require.NotNil(t, keep)
	assert.Equal(t, int64(11), keep.GetSize(), "file must report its real on-disk size")
	pkg := find(lvl1, "pkg")
	require.NotNil(t, pkg)
	assert.Empty(t, pkg.Children, "depth 1 must not include grandchildren")
	assert.True(t, pkg.GetHasChildren(), "non-empty dir must carry has_children at the boundary")
	empty := find(lvl1, "empty")
	require.NotNil(t, empty)
	assert.False(t, empty.GetHasChildren(), "empty dir must not carry has_children")

	// depth 0: full recursive tree.
	full := getTree(0)
	fpkg := find(full, "pkg")
	require.NotNil(t, fpkg)
	fsub := find(fpkg.Children, "sub")
	require.NotNil(t, fsub)
	assert.NotNil(t, find(fsub.Children, "deep.txt"), "depth 0 must recurse fully")
}
