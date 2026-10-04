// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// copyPathsRecordingRouter answers worktree.create, and records every
// worktree.create payload and every worktree.copy_paths request.
type copyPathsRecordingRouter struct {
	worktreeTestDaemonRouter
	home string

	mu        sync.Mutex
	creates   []map[string]any
	copyCalls []struct {
		SourceRoot string   `json:"source_root"`
		DestRoot   string   `json:"dest_root"`
		Paths      []string `json:"paths"`
	}
}

func (r *copyPathsRecordingRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *copyPathsRecordingRouter) SendDaemonCommand(ctx context.Context, userID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	switch commandType {
	case "worktree.create":
		var req map[string]any
		_ = json.Unmarshal(payload, &req)
		r.mu.Lock()
		r.creates = append(r.creates, req)
		r.mu.Unlock()
		path := filepath.Join(r.home, ".reliant", "worktrees", req["workspace_id"].(string))
		if sub, _ := req["sub_path"].(string); sub != "" && sub != "." {
			path = filepath.Join(path, sub)
		}
		return json.Marshal(map[string]any{"success": true, "worktree_path": path})
	case "worktree.copy_paths":
		var call struct {
			SourceRoot string   `json:"source_root"`
			DestRoot   string   `json:"dest_root"`
			Paths      []string `json:"paths"`
		}
		_ = json.Unmarshal(payload, &call)
		r.mu.Lock()
		r.copyCalls = append(r.copyCalls, call)
		r.mu.Unlock()
		return json.Marshal(map[string]any{"copied": call.Paths})
	}
	return r.worktreeTestDaemonRouter.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

// copy_files are workspace-root-relative and copied ONCE, root to root, after
// the checkouts — not handed to every repo's worktree.create to be matched
// against that repo's own directory, which is how `reliant/.env` used to
// silently match nothing.
func TestCreateWorktreeCopiesPathsOnceFromProjectRoot(t *testing.T) {
	repo := db.NewTestRepo(t)
	router := &copyPathsRecordingRouter{home: t.TempDir()}
	svc := NewWorktreeService(repo, nil, router)

	userID := uuid.New().String()
	projectID := seedMultiRepoProject(t, repo, userID, "control-plane", "forge", "reliant")
	project, err := repo.GetProject(context.Background(), projectID)
	require.NoError(t, err)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	resp, err := svc.CreateWorktree(ctx, connect.NewRequest(&reliantv1.CreateWorktreeRequest{
		ProjectId: projectID,
		Name:      "with-env",
		Branch:    "with-env",
		CopyFiles: []string{".env", "reliant/.env", "reliant/web/node_modules/"},
	}))
	require.NoError(t, err)
	settled := awaitWorktreeStatus(t, repo, resp.Msg.Worktree.Id, reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)

	router.mu.Lock()
	defer router.mu.Unlock()

	require.Len(t, router.copyCalls, 1, "one copy for the whole workspace, not one per repo")
	call := router.copyCalls[0]
	assert.Equal(t, project.Path, call.SourceRoot, "copied from the project ROOT, where the paths are relative to")
	assert.Equal(t, settled.Path, call.DestRoot, "copied into the workspace ROOT")
	assert.Equal(t, []string{".env", "reliant/.env", "reliant/web/node_modules"}, call.Paths)

	require.Len(t, router.creates, 3)
	for _, create := range router.creates {
		assert.NotContains(t, create, "copy_files", "a checkout no longer copies anything itself")
	}
}

func TestCreateWorktreeWithoutCopyFilesSendsNoCopy(t *testing.T) {
	repo := db.NewTestRepo(t)
	router := &copyPathsRecordingRouter{home: t.TempDir()}
	svc := NewWorktreeService(repo, nil, router)

	userID := uuid.New().String()
	projectID := seedMultiRepoProject(t, repo, userID, "")
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	resp, err := svc.CreateWorktree(ctx, connect.NewRequest(&reliantv1.CreateWorktreeRequest{
		ProjectId: projectID, Name: "plain", Branch: "plain",
	}))
	require.NoError(t, err)
	awaitWorktreeStatus(t, repo, resp.Msg.Worktree.Id, reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)

	router.mu.Lock()
	defer router.mu.Unlock()
	assert.Empty(t, router.copyCalls)
}

// An invalid entry is rejected synchronously — before a row exists — so the
// caller sees the mistake instead of a workspace missing its .env.
func TestCreateWorktreeRejectsInvalidCopyPath(t *testing.T) {
	repo := db.NewTestRepo(t)
	router := &copyPathsRecordingRouter{home: t.TempDir()}
	svc := NewWorktreeService(repo, nil, router)

	userID := uuid.New().String()
	projectID := seedMultiRepoProject(t, repo, userID, "")
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	for _, bad := range []string{"../outside", "/etc/passwd", " "} {
		_, err := svc.CreateWorktree(ctx, connect.NewRequest(&reliantv1.CreateWorktreeRequest{
			ProjectId: projectID, Name: "bad", Branch: "bad", CopyFiles: []string{bad},
		}))
		require.Error(t, err, "copy path %q", bad)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "copy path %q", bad)
	}

	router.mu.Lock()
	defer router.mu.Unlock()
	assert.Empty(t, router.creates, "nothing may be checked out for a rejected request")
}
