// Copyright (c) 2025 Reliant Labs
package project

import (
	"context"
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
			s := newProjectService(t, repo, &createProjectDaemonRouter{})

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
	s := newProjectService(t, repo, &createProjectDaemonRouter{})

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
