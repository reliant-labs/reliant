// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// mkdirRecordingRouter records every fs.mkdir path the heal asks a daemon for.
type mkdirRecordingRouter struct {
	worktreeTestDaemonRouter
	mu      sync.Mutex
	mkdired []string
}

func (r *mkdirRecordingRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, payload []byte, _ int32) ([]byte, error) {
	if commandType == "fs.mkdir" {
		var req struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(payload, &req)
		r.mu.Lock()
		r.mkdired = append(r.mkdired, req.Path)
		r.mu.Unlock()
	}
	return json.Marshal(map[string]any{})
}

// project-dir-heal runs on every daemon connect and creates each project's
// directory. For a project whose checkout comes from a repository, that is
// the defect behind the prod incident: a git.clone queued while the daemon
// was down never ran, the daemon connected, the heal created an EMPTY
// /home/workspace/projects/houndersclub, and the connect-time reconcile then
// saw a directory there and marked the install done. The owner was left with
// a project the product called installed and that had no files.
//
// A plain project (no remote) still has its directory healed — that is what
// the heal is for: an empty project created while the daemon was booting.
func TestProjectDirHeal_NeverCreatesARepositoryProjectsDirectory(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "user-heal-" + uuid.NewString()
	ctx := context.Background()
	now := time.Now().UTC()
	remote := "https://github.com/acme/widgets.git"

	cloned := &db.Project{
		ID: uuid.NewString(), UserID: userID, Name: "widgets",
		Path: "/home/workspace/projects/widgets-" + uuid.NewString(), RemoteURL: &remote,
		IsGitRepo: true, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	plain := &db.Project{
		ID: uuid.NewString(), UserID: userID, Name: "notes",
		Path:      "/home/workspace/projects/notes-" + uuid.NewString(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	require.NoError(t, repo.CreateProject(ctx, cloned))
	require.NoError(t, repo.CreateProject(ctx, plain))

	router := &mkdirRecordingRouter{}
	svc := NewProjectService(repo, router)
	svc.ensureProjectDirsOnDaemon(userID, "daemon-1")

	router.mu.Lock()
	defer router.mu.Unlock()
	require.NotContains(t, router.mkdired, cloned.Path,
		"the heal must not create a repository project's directory; only a successful clone may")
	require.Contains(t, router.mkdired, plain.Path,
		"a plain project's directory is still healed — that is what the heal exists for")
}
