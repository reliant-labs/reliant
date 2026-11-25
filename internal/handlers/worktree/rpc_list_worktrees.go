package worktree

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ListWorktrees lists worktrees for a project
func (s *Service) ListWorktrees(
	ctx context.Context,
	req *connect.Request[reliantv1.ListWorktreesRequest],
) (*connect.Response[reliantv1.ListWorktreesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}

	// Check project permission
	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	projectID := req.Msg.ProjectId
	pageSize := defaultListWorktreesPageSize
	if req.Msg.Limit > 0 {
		pageSize = int(req.Msg.Limit)
	}
	offset, err := decodeWorktreePageToken(req.Msg.GetPageToken())
	if err != nil {
		return nil, svcerr.InvalidArgument("invalid page_token")
	}
	filters := db.WorktreeFilters{
		ProjectID: &projectID,
		MainOnly:  req.Msg.GetMainOnly(),
		// One extra row tells us whether another page follows.
		Limit:  pageSize + 1,
		Offset: offset,
	}

	if req.Msg.ChatId != nil && *req.Msg.ChatId != "" {
		filters.ChatID = req.Msg.ChatId
	}

	worktrees, err := s.deps.Repo.ListWorktrees(ctx, filters)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list worktrees"))
	}

	nextPageToken := ""
	if len(worktrees) > pageSize {
		worktrees = worktrees[:pageSize]
		nextPageToken = encodeWorktreePageToken(offset + pageSize)
	}

	// Self-heal invariant: every project should have a main worktree.
	// This also repairs older projects created while Postgres parity bugs existed.
	if len(worktrees) == 0 && offset == 0 {
		project, err := s.deps.Projects.GetProjectWithUserCheck(ctx, req.Msg.ProjectId, userID)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
		}

		defaultBranch := "main"
		if project.DefaultBranch != nil && *project.DefaultBranch != "" {
			defaultBranch = *project.DefaultBranch
		}

		now := time.Now().UTC()
		mainWorktree := &db.Worktree{
			ID:         uuid.New().String(),
			Name:       defaultBranch,
			Path:       project.Path,
			Branch:     defaultBranch,
			BaseBranch: defaultBranch,
			ProjectID:  project.ID,
			Status:     int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
			IsMain:     true,
			CreatedAt:  now,
			UpdatedAt:  now,
			LastActive: now,
		}

		if err := s.deps.Repo.CreateWorktree(ctx, mainWorktree); err != nil {
			logging.WarnContext(ctx, "Failed to auto-create main worktree during ListWorktrees", "error", err, "projectID", project.ID)
			// Re-fetch in case another request created it concurrently.
			worktrees, err = s.deps.Repo.ListWorktrees(ctx, filters)
			if err != nil {
				return nil, svcerr.WrapOr(err, svcerr.Internal("failed to ensure main worktree"))
			}
			if len(worktrees) == 0 {
				return nil, svcerr.Internal("failed to ensure main worktree")
			}
		} else {
			worktrees = []*db.Worktree{mainWorktree}
		}
	}

	protoWorktrees := make([]*reliantv1.Worktree, len(worktrees))
	for i, w := range worktrees {
		protoWorktrees[i] = worktreeToProto(w)
	}

	return connect.NewResponse(&reliantv1.ListWorktreesResponse{
		Worktrees:     protoWorktrees,
		Total:         int32(len(protoWorktrees)),
		NextPageToken: nextPageToken,
	}), nil
}
