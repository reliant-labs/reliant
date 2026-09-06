// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// windowsProjectPaths are the shapes a Windows daemon reports. A POSIX API
// server must accept every one of them: only the daemon has a filesystem, and
// it may run on a different machine than this process, so path/filepath's
// host-OS rules are the wrong authority here.
var windowsProjectPaths = []struct {
	name string
	path string
}{
	{"drive letter with backslashes", `C:\Users\sean\src\proj`},
	{"drive letter with forward slashes", "C:/Users/sean/src/proj"},
	{"UNC share", `\\server\share\proj`},
}

// TestCreateProject_AcceptsWindowsDaemonPaths is the handler-level
// reproduction. Before the ospath fix, every case failed with
// InvalidArgument "project path must be absolute".
func TestCreateProject_AcceptsWindowsDaemonPaths(t *testing.T) {
	for _, tc := range windowsProjectPaths {
		t.Run(tc.name, func(t *testing.T) {
			repo, cleanup := db.SetupTestDB(t)
			defer cleanup()

			userID := "user-windows-project"
			ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
			s := NewProjectService(repo, &createProjectDaemonRouter{})

			resp, err := s.CreateProject(ctx, connect.NewRequest(&reliantv1.CreateProjectRequest{
				Name: "proj",
				Path: tc.path,
			}))
			require.NoError(t, err, "a Windows daemon's absolute path must not be rejected as relative")
			require.NotNil(t, resp)
			assert.Equal(t, tc.path, resp.Msg.Project.Path, "the path must be persisted verbatim, not rewritten into POSIX form")
		})
	}
}

// TestCreateProject_StillRejectsNonAbsolutePaths pins the other half: widening
// the check must not make it accept a path no machine can resolve.
func TestCreateProject_StillRejectsNonAbsolutePaths(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "user-windows-project-neg")
	s := NewProjectService(repo, &createProjectDaemonRouter{})

	for _, path := range []string{
		"relative/path",
		`src\proj`,
		`C:proj`,  // drive-relative: resolved against that drive's cwd
		`\proj`,   // rooted but driveless
		"   ",     // whitespace only
		"/a\x00b", // NUL truncates at the syscall boundary
	} {
		_, err := s.CreateProject(ctx, connect.NewRequest(&reliantv1.CreateProjectRequest{
			Name: "proj",
			Path: path,
		}))
		require.Error(t, err, "path %q must still be refused", path)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "path %q", path)
	}
}

// TestFileSystemProxy_CreateDirectory_AcceptsWindowsDaemonPaths covers the
// project picker's "New folder" against a Windows daemon. The path is executed
// on the daemon, so it must reach it unmodified.
func TestFileSystemProxy_CreateDirectory_AcceptsWindowsDaemonPaths(t *testing.T) {
	for _, tc := range windowsProjectPaths {
		t.Run(tc.name, func(t *testing.T) {
			router := &recordingFSDaemonRouter{}
			svc := NewFileSystemProxyService(router, nil)
			ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

			resp, err := svc.CreateDirectory(ctx, connect.NewRequest(&reliantv1.CreateDirectoryRequest{
				Path: tc.path,
			}))
			require.NoError(t, err)
			assert.Equal(t, tc.path, resp.Msg.Path)

			assert.Equal(t, "fs.mkdir", router.lastCommandType)
			var sent map[string]string
			require.NoError(t, json.Unmarshal(router.lastPayload, &sent))
			assert.Equal(t, tc.path, sent["path"], "the daemon must receive the path it gave us, byte for byte")
		})
	}
}

// TestNormalizeProjectPath_WindowsPaths covers the helper that gates every
// project-config snapshot and delta from a daemon. Returning "" here is a
// silent, unrecoverable drop (see the comment in persistProjectConfigSnapshot).
func TestNormalizeProjectPath_WindowsPaths(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"posix", "/home/workspace/projects/app", "/home/workspace/projects/app"},
		{"posix trailing slash", "/home/workspace/projects/app/", "/home/workspace/projects/app"},
		{"drive backslash", `C:\Users\sean\src\proj`, `C:\Users\sean\src\proj`},
		{"drive backslash trailing", `C:\Users\sean\src\proj\`, `C:\Users\sean\src\proj`},
		{"drive backslash dotdot", `C:\Users\sean\x\..\proj`, `C:\Users\sean\proj`},
		{"drive forward slash", "C:/Users/sean/src/proj", "C:/Users/sean/src/proj"},
		{"unc", `\\server\share\proj`, `\\server\share\proj`},

		{"relative rejected", "src/proj", ""},
		{"drive relative rejected", `C:proj`, ""},
		{"empty rejected", "", ""},
		{"whitespace rejected", "   ", ""},
		{"nul rejected", "/a\x00b", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, normalizeProjectPath(tt.in))
		})
	}
}

// TestFileSystemService_CreateDirectory_RejectsNonAbsolute keeps the negative
// half of the local FileSystemService honest after the widening.
func TestFileSystemService_CreateDirectory_RejectsNonAbsolute(t *testing.T) {
	svc, _ := setupTestFileSystemService(t)

	for _, path := range []string{"", "relative/path", `C:proj`, `\proj`} {
		_, err := svc.CreateDirectory(context.Background(), connect.NewRequest(&reliantv1.CreateDirectoryRequest{
			Path: path,
		}))
		require.Error(t, err, "path %q must be refused", path)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "path %q", path)
	}
}
