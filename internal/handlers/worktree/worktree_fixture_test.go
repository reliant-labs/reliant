package worktree_test

import (
	"context"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	worktree "github.com/reliant-labs/reliant/internal/handlers/worktree"
)

// The rows of the store ownedWorktreeRepo builds.
const (
	ownProjectID    = "p-1"
	ownWorktreeID   = "w-1"
	mainWorktreeID  = "w-main"
	otherWorktreeID = "w-other" // in a project the caller does not own
	unknownID       = "w-missing"
)

// ownedWorktreeRepo is a store where the harness caller (test-user) owns
// project p-1, which holds the worktree w-1 and its main checkout w-main.
// w-other is in another user's project. Every other worktree or project is
// not found, as GetWorktree and GetProjectWithUserCheck answer.
func ownedWorktreeRepo(t *testing.T) *worktree.MockRepository {
	t.Helper()
	active := int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)
	worktrees := map[string]db.Worktree{
		ownWorktreeID: {
			ID: ownWorktreeID, ProjectID: ownProjectID, Name: "feature", Branch: "feature",
			BaseBranch: "main", Path: "/work/.reliant/worktrees/w-1", Status: active,
		},
		mainWorktreeID: {
			ID: mainWorktreeID, ProjectID: ownProjectID, Name: "main", Branch: "main",
			BaseBranch: "main", Path: "/work", Status: active, IsMain: true,
		},
		otherWorktreeID: {
			ID: otherWorktreeID, ProjectID: "p-2", Name: "theirs", Branch: "theirs",
			BaseBranch: "main", Path: "/elsewhere", Status: active,
		},
	}
	return &worktree.MockRepository{
		GetWorktreeFunc: func(_ context.Context, id string) (*db.Worktree, error) {
			if w, ok := worktrees[id]; ok {
				return &w, nil
			}
			return nil, core.ErrWorktreeNotFound
		},
		GetProjectWithUserCheckFunc: func(_ context.Context, id, userID string) (*db.Project, error) {
			if id == ownProjectID && userID == "test-user" {
				return &db.Project{ID: id, UserID: userID, Name: "proj", Path: "/work", IsGitRepo: true}, nil
			}
			return nil, core.ErrProjectNotFound
		},
	}
}

// newOwnedWorktreeService is the Service over ownedWorktreeRepo. It has no
// daemon router, so a row that reached the machine would fail: every row
// built on it is answered before any command is sent.
func newOwnedWorktreeService(t *testing.T) *worktree.Service {
	t.Helper()
	return worktree.NewTestWorktree(t, worktree.WithWorktreeDeps(worktree.Deps{Repo: ownedWorktreeRepo(t)}))
}
