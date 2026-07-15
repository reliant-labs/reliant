package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
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