package filepreview

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
)

// ResolveBasePath must treat a worktree with no PATH as unusable, not merely
// check that the row exists.
//
// ── The bug ──
//
// Creating a worktree named `release` failed because the repo already had a
// branch `release/compute-tier-redesign`: git stores refs as files, so
// `refs/heads/release` cannot exist beside `refs/heads/release/...`. The
// creation left a row behind — status FAILED, path empty — and a chat bound to
// it.
//
// GetWorktree then SUCCEEDED for that row, so the old `if err == nil` returned
// its empty path and never reached the project fallback. Every filesystem RPC
// scoped to that chat died with:
//
//	[failed_precondition] project "baa5f7c2-…" has no workspace path to scope
//	this request to
//
// which names the PROJECT — whose path was correct the whole time — and sends
// whoever reads it to the wrong table.

// stubRepo answers the lookups ResolveBasePath makes — all of BasePathReader —
// and its project stands for the main checkout on the request's machine.
type stubRepo struct {
	worktree    *db.Worktree
	worktreeErr error
	project     *db.Project
	projectErr  error
	chat        *db.Chat
	chatErr     error
}

func (s *stubRepo) GetWorktree(_ context.Context, _ string) (*db.Worktree, error) {
	return s.worktree, s.worktreeErr
}

// mainCheckout is the MainCheckout ResolveBasePath falls back to: the
// project's checkout on the request's machine.
func (s *stubRepo) mainCheckout(context.Context) (string, error) {
	if s.projectErr != nil {
		return "", s.projectErr
	}
	return s.project.Path, nil
}

func (s *stubRepo) GetChat(_ context.Context, _ string) (*db.Chat, error) {
	return s.chat, s.chatErr
}

var _ BasePathReader = (*stubRepo)(nil)

func strPtr(s string) *string { return &s }

const projectPath = "/Users/dev/src/project"

// THE REGRESSION TEST. A FAILED worktree row with an empty path must fall back
// to the project, exactly as a missing row does.
func TestResolveBasePath_FailedWorktreeWithEmptyPathFallsBackToProject(t *testing.T) {
	repo := &stubRepo{
		// The real row this reproduces: it EXISTS, so the lookup succeeds.
		worktree: &db.Worktree{ID: "wt-failed", Path: ""},
		project:  &db.Project{ID: "proj-1", Path: projectPath},
	}

	got, err := ResolveBasePath(context.Background(), repo, "proj-1", strPtr("wt-failed"), nil, repo.mainCheckout)
	if err != nil {
		t.Fatalf("ResolveBasePath: %v", err)
	}
	if got != projectPath {
		t.Fatalf("base path = %q, want the project path %q — a worktree row with no path is a "+
			"tombstone from a failed create, and returning its empty path makes every scoped "+
			"filesystem RPC fail while blaming the project", got, projectPath)
	}
}

// Whitespace is the same nothing. A path of " " would pass a bare != "" check
// and then fail identically downstream.
func TestResolveBasePath_WhitespaceOnlyWorktreePathFallsBackToProject(t *testing.T) {
	repo := &stubRepo{
		worktree: &db.Worktree{ID: "wt-blank", Path: "   "},
		project:  &db.Project{ID: "proj-1", Path: projectPath},
	}

	got, err := ResolveBasePath(context.Background(), repo, "proj-1", strPtr("wt-blank"), nil, repo.mainCheckout)
	if err != nil {
		t.Fatalf("ResolveBasePath: %v", err)
	}
	if got != projectPath {
		t.Fatalf("base path = %q, want %q", got, projectPath)
	}
}

// The happy path is unchanged: a real worktree still wins over the project.
// Without this the fix could "pass" by always ignoring worktrees, which would
// silently scope every branch chat to the project root.
func TestResolveBasePath_UsableWorktreeStillWins(t *testing.T) {
	const worktreePath = "/Users/dev/.reliant/worktrees/proj/feature-abc"
	repo := &stubRepo{
		worktree: &db.Worktree{ID: "wt-ok", Path: worktreePath},
		project:  &db.Project{ID: "proj-1", Path: projectPath},
	}

	got, err := ResolveBasePath(context.Background(), repo, "proj-1", strPtr("wt-ok"), nil, repo.mainCheckout)
	if err != nil {
		t.Fatalf("ResolveBasePath: %v", err)
	}
	if got != worktreePath {
		t.Fatalf("base path = %q, want the worktree path %q", got, worktreePath)
	}
}

// A chat-derived worktree reaches the same guard: the chat names a worktree,
// that worktree is a failed tombstone, so the project answers. This is the
// exact shape of the reported bug, where the client sent only a chat id.
func TestResolveBasePath_ChatPointingAtFailedWorktreeFallsBackToProject(t *testing.T) {
	repo := &stubRepo{
		chat:     &db.Chat{ID: "chat-1", WorktreeID: strPtr("wt-failed")},
		worktree: &db.Worktree{ID: "wt-failed", Path: ""},
		project:  &db.Project{ID: "proj-1", Path: projectPath},
	}

	got, err := ResolveBasePath(context.Background(), repo, "proj-1", nil, strPtr("chat-1"), repo.mainCheckout)
	if err != nil {
		t.Fatalf("ResolveBasePath: %v", err)
	}
	if got != projectPath {
		t.Fatalf("base path = %q, want %q", got, projectPath)
	}
}

// A genuinely missing worktree already fell back; pinned so the two "unusable"
// shapes stay behaviourally identical.
func TestResolveBasePath_MissingWorktreeFallsBackToProject(t *testing.T) {
	repo := &stubRepo{
		worktreeErr: errors.New("worktree not found: wt-gone"),
		project:     &db.Project{ID: "proj-1", Path: projectPath},
	}

	got, err := ResolveBasePath(context.Background(), repo, "proj-1", strPtr("wt-gone"), nil, repo.mainCheckout)
	if err != nil {
		t.Fatalf("ResolveBasePath: %v", err)
	}
	if got != projectPath {
		t.Fatalf("base path = %q, want %q", got, projectPath)
	}
}

// The main worktree row mirrors projects.path, the creating machine's path.
// It is the main checkout, which is the request machine's own: a Files tab on
// a cloud machine must never be scoped to the laptop path the project was
// created at.
func TestResolveBasePath_MainWorktreeRowIsTheMachinesCheckout(t *testing.T) {
	const cloudCheckout = "/home/workspace/projects/project"
	repo := &stubRepo{
		worktree: &db.Worktree{ID: "wt-main", Path: projectPath, IsMain: true},
		project:  &db.Project{ID: "proj-1", Path: cloudCheckout},
	}

	got, err := ResolveBasePath(context.Background(), repo, "proj-1", strPtr("wt-main"), nil, repo.mainCheckout)
	if err != nil {
		t.Fatalf("ResolveBasePath: %v", err)
	}
	if got != cloudCheckout {
		t.Fatalf("base path = %q, want the machine's checkout %q, not the main row's creation path", got, cloudCheckout)
	}
}
