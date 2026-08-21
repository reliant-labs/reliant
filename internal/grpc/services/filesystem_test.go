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

// A file outside the workspace, named by absolute path, opens. This is the
// user-facing point of the change: clicking an absolute path the assistant
// mentioned shows the file instead of a dead "cannot be opened" tooltip.
func TestFileSystemService_GetFileContent_AllowsAbsolutePathOutsideWorkspace(t *testing.T) {
	svc, _ := setupTestFileSystemService(t)

	outside := filepath.Join(t.TempDir(), "notes.md")
	require.NoError(t, os.WriteFile(outside, []byte("# outside the workspace\n"), 0644))

	resp, err := svc.GetFileContent(context.Background(), connect.NewRequest(&reliantv1.GetFileContentRequest{
		ProjectId: "test-project",
		Path:      outside,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Contains(t, resp.Msg.Content, "outside the workspace")
}

// Relative traversal stays refused on the read path. Widening absolute paths
// must not widen this: a relative path is interpreted against a base the user
// did not choose, so climbing out of it is still a traversal attempt.
func TestFileSystemService_GetFileContent_StillRejectsRelativeTraversal(t *testing.T) {
	svc, projectPath := setupTestFileSystemService(t)

	secret := filepath.Join(filepath.Dir(projectPath), "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("top secret"), 0644))

	_, err := svc.GetFileContent(context.Background(), connect.NewRequest(&reliantv1.GetFileContentRequest{
		ProjectId: "test-project",
		Path:      "../secret.txt",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// Writes stay confined even for an absolute path. Reading a file the user
// named is a different act from writing one: a write outside the workspace was
// never requested and is refused.
func TestFileSystemService_SaveFileContent_RejectsAbsolutePathOutsideWorkspace(t *testing.T) {
	svc, _ := setupTestFileSystemService(t)

	outside := filepath.Join(t.TempDir(), "should-not-be-written.txt")

	_, err := svc.SaveFileContent(context.Background(), connect.NewRequest(&reliantv1.SaveFileContentRequest{
		ProjectId: "test-project",
		Path:      outside,
		Content:   "nope",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	_, statErr := os.Stat(outside)
	assert.True(t, os.IsNotExist(statErr), "file outside the workspace must not have been created")
}

// An absolute path that already points INSIDE the workspace resolves to itself.
// The previous implementation joined it onto the base a second time, producing
// /workspace/workspace/src/... and a spurious NotFound.
func TestFileSystemService_GetFileContent_AbsolutePathInsideWorkspaceIsNotDoubled(t *testing.T) {
	svc, projectPath := setupTestFileSystemService(t)

	inside := filepath.Join(projectPath, "main.go")
	require.NoError(t, os.WriteFile(inside, []byte("package main\n"), 0644))

	resp, err := svc.GetFileContent(context.Background(), connect.NewRequest(&reliantv1.GetFileContentRequest{
		ProjectId: "test-project",
		Path:      inside,
	}))
	require.NoError(t, err)
	assert.Contains(t, resp.Msg.Content, "package main")
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
