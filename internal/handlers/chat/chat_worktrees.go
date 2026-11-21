package chat

import (
	"context"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// worktreeSummaryToProto is the part of a worktree a chat list renders.
func worktreeSummaryToProto(w *db.Worktree) *reliantv1.WorktreeSummary {
	out := &reliantv1.WorktreeSummary{
		Id:        w.ID,
		Name:      w.Name,
		Branch:    w.Branch,
		Path:      w.Path,
		IsMain:    w.IsMain,
		Status:    reliantv1.WorktreeStatus(w.Status),
		ProjectId: w.ProjectID,
	}
	if w.DeletedAt != nil {
		deletedAt := w.DeletedAt.Format(time.RFC3339)
		out.DeletedAt = &deletedAt
	}
	return out
}

// attachWorktrees fills each chat's worktree summary and returns the main
// workspace of every project in scope (projects, or all of the user's when it
// is empty), in two queries however many chats, projects and workspaces there
// are.
// The client groups the sidebar from this, so it never has to page the whole
// worktree list just to name the groups. A failure leaves the summaries out
// (the chats still list); the client then groups those chats under a fallback.
//
// The store's main-workspace read checks only that the projects are the
// caller's; a main workspace is listed only when the caller may read its
// project (authz, so not one of an org they have left or a credential's
// other org) and the project is in the list's org scope.
func (s *Service) attachWorktrees(ctx context.Context, userID string, projects []string, scope *core.OrgScope, chats []*db.Chat, protoChats []*reliantv1.Chat) []*reliantv1.WorktreeSummary {
	seen := make(map[string]struct{}, len(chats))
	ids := make([]string, 0, len(chats))
	for _, c := range chats {
		if c.WorktreeID == nil || *c.WorktreeID == "" {
			continue
		}
		if _, dup := seen[*c.WorktreeID]; dup {
			continue
		}
		seen[*c.WorktreeID] = struct{}{}
		ids = append(ids, *c.WorktreeID)
	}

	if len(ids) > 0 {
		rows, err := s.deps.Repo.ListWorktreesByIDs(ctx, ids)
		if err != nil {
			logging.WarnContext(ctx, "Failed to load the workspaces of a chat list", "error", err, "projects", projects)
		} else {
			byID := make(map[string]*reliantv1.WorktreeSummary, len(rows))
			for _, w := range rows {
				byID[w.ID] = worktreeSummaryToProto(w)
			}
			for i, c := range chats {
				if c.WorktreeID != nil {
					protoChats[i].Worktree = byID[*c.WorktreeID]
				}
			}
		}
	}

	// The query checks ownership itself: a project id that is not the
	// caller's gets no main workspace.
	mains, err := s.deps.Repo.ListMainWorktreesForUser(ctx, userID, projects)
	if err != nil {
		logging.WarnContext(ctx, "Failed to load the main workspaces of a chat list", "error", err, "projects", projects)
		return nil
	}
	readable := s.readableProjects(ctx, userID, scope, mains)
	out := make([]*reliantv1.WorktreeSummary, 0, len(mains))
	for _, w := range mains {
		if readable[w.ProjectID] {
			out = append(out, worktreeSummaryToProto(w))
		}
	}
	return out
}

// readableProjects reads the projects of these workspaces in one query and
// reports which the caller may read within scope. A project that is missing,
// or a failed read, is not readable: the main workspace is then left out, as
// a chat list with no main workspaces still lists.
func (s *Service) readableProjects(ctx context.Context, userID string, scope *core.OrgScope, worktrees []*db.Worktree) map[string]bool {
	seen := make(map[string]struct{}, len(worktrees))
	ids := make([]string, 0, len(worktrees))
	for _, w := range worktrees {
		if _, dup := seen[w.ProjectID]; dup {
			continue
		}
		seen[w.ProjectID] = struct{}{}
		ids = append(ids, w.ProjectID)
	}
	readable := make(map[string]bool, len(ids))
	if len(ids) == 0 {
		return readable
	}
	projects, err := s.deps.Repo.ListProjectsByIDs(ctx, ids)
	if err != nil {
		logging.WarnContext(ctx, "Failed to load the projects of a chat list's main workspaces", "error", err)
		return readable
	}
	for _, p := range projects {
		readable[p.ID] = authz.CanReadProject(ctx, userID, p) && scope.Includes(p.OrgID)
	}
	return readable
}

// attachWorktree fills the workspace summary of a single chat, so the chat a
// client is handed by StartChat, BranchChat or GetChat groups correctly without
// waiting for the next list.
func (s *Service) attachWorktree(ctx context.Context, chat *db.Chat, out *reliantv1.Chat) {
	if chat.WorktreeID == nil || *chat.WorktreeID == "" {
		return
	}
	rows, err := s.deps.Repo.ListWorktreesByIDs(ctx, []string{*chat.WorktreeID})
	if err != nil {
		logging.WarnContext(ctx, "Failed to load the chat's workspace", "error", err, "chatID", chat.ID)
		return
	}
	if len(rows) > 0 {
		out.Worktree = worktreeSummaryToProto(rows[0])
	}
}
