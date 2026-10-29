package worktree

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
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
		project, err := s.deps.Repo.GetProjectWithUserCheck(ctx, req.Msg.ProjectId, userID)
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
	s.settleOwnersInBackground(ctx, userID, worktrees)

	return connect.NewResponse(&reliantv1.ListWorktreesResponse{
		Worktrees:     protoWorktrees,
		Total:         int32(len(protoWorktrees)),
		NextPageToken: nextPageToken,
	}), nil
}

// ListArchivedWorktrees pages through the project's archived workspaces, most
// recently archived first. ListWorktrees never returns them.
func (s *Service) ListArchivedWorktrees(
	ctx context.Context,
	req *connect.Request[reliantv1.ListArchivedWorktreesRequest],
) (*connect.Response[reliantv1.ListArchivedWorktreesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}
	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	pageSize := defaultListWorktreesPageSize
	if req.Msg.Limit > 0 {
		pageSize = int(req.Msg.Limit)
	}
	offset, err := decodeWorktreePageToken(req.Msg.GetPageToken())
	if err != nil {
		return nil, svcerr.InvalidArgument("invalid page_token")
	}

	projectID := req.Msg.ProjectId
	worktrees, err := s.deps.Repo.ListWorktrees(ctx, db.WorktreeFilters{
		ProjectID:    &projectID,
		ArchivedOnly: true,
		// One extra row tells us whether another page follows.
		Limit:  pageSize + 1,
		Offset: offset,
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list archived worktrees"))
	}

	nextPageToken := ""
	if len(worktrees) > pageSize {
		worktrees = worktrees[:pageSize]
		nextPageToken = encodeWorktreePageToken(offset + pageSize)
	}

	protoWorktrees := make([]*reliantv1.Worktree, len(worktrees))
	for i, w := range worktrees {
		protoWorktrees[i] = worktreeToProto(w)
	}
	return connect.NewResponse(&reliantv1.ListArchivedWorktreesResponse{
		Worktrees:     protoWorktrees,
		Total:         int32(len(protoWorktrees)),
		NextPageToken: nextPageToken,
	}), nil
}

const defaultListWorktreesPageSize = 100

// The token is an offset into a stable (main first, last_active, id) ordering.
// It is opaque to clients so the ordering can move to a keyset later.
func encodeWorktreePageToken(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

func decodeWorktreePageToken(token string) (int, error) {
	if token == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, err
	}
	offset, err := strconv.Atoi(string(raw))
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("invalid offset")
	}
	return offset, nil
}

// GetWorktree retrieves a worktree by ID
func (s *Service) GetWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.GetWorktreeRequest],
) (*connect.Response[reliantv1.GetWorktreeResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, svcerr.InvalidArgument("worktree_id is required")
	}

	if err := s.worktreeBelongsToUser(ctx, req.Msg.WorktreeId, userID); err != nil {
		return nil, err
	}

	worktree, err := s.deps.Repo.GetWorktree(ctx, req.Msg.WorktreeId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get worktree"))
	}

	return connect.NewResponse(&reliantv1.GetWorktreeResponse{
		Worktree: worktreeToProto(worktree),
	}), nil
}

// listSettleTimeout bounds the background settling one list starts.
const listSettleTimeout = 30 * time.Second

// settleOwnersInBackground settles, after the list has answered, the machine
// of each listed branch workspace whose row records none
// (daemonplacement.Resolver), so the next list carries it and the new-chat
// composer offers the workspace only on its machine. The list itself never
// waits on a machine. Rows are settled one after another: each already asks
// every connected machine at once, and a long list must not fan out further.
func (s *Service) settleOwnersInBackground(ctx context.Context, userID string, worktrees []*db.Worktree) {
	var unattributed []*db.Worktree
	for _, w := range worktrees {
		if w != nil && w.DaemonID == nil && !w.IsMain && w.DeletedAt == nil && w.Path != "" {
			unattributed = append(unattributed, w)
		}
	}
	if len(unattributed) == 0 || s.owners == nil {
		return
	}
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), listSettleTimeout)
	go func() {
		defer cancel()
		for _, w := range unattributed {
			if settleCtx.Err() != nil {
				return
			}
			s.owners.Owner(settleCtx, userID, w)
		}
	}()
}
