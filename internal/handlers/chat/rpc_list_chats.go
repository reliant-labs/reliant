package chat

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ListChats lists the caller's non-archived chats: in one project, in the
// union of project_id and project_ids (the sidebar's multi-project filter), or
// across all of their projects when both are empty (the sidebar's "All
// projects" view). Chats are filtered by owner in every case.
func (s *Service) ListChats(
	ctx context.Context,
	req *connect.Request[reliantv1.ListChatsRequest],
) (*connect.Response[reliantv1.ListChatsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	limit := int(100)
	if req.Msg.Limit != nil && *req.Msg.Limit > 0 {
		limit = int(*req.Msg.Limit)
	}

	var projectID *string
	if req.Msg.ProjectId != "" {
		projectID = &req.Msg.ProjectId
	}
	if err := validListProjectIDs(req.Msg.ProjectIds); err != nil {
		return nil, err
	}
	scope, err := s.listScope(ctx, userID, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	filters := db.ChatFilters{
		UserID:          userID,
		ProjectID:       projectID,
		ProjectIDs:      req.Msg.ProjectIds,
		Org:             scope,
		Limit:           limit,
		AppName:         appNameFilter(req.Msg.AppName),
		ExcludeArchived: true,
		// The chat list holds conversations: USER-origin chats and adopted
		// runs. Every other run lists under Runs (RunService.ListRuns).
		SidebarOnly: true,
	}

	chats, err := s.chatLists().ListChats(ctx, filters)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list chats"))
	}
	// The store lists what the caller owns; a chat of an org they have left
	// is not theirs to see, named org or not (GetChat refuses it too).
	chats = authz.Visible(ctx, userID, chats, authz.CanReadChat[*db.Chat])

	// The background reconciler repairs stale DB status against Temporal.
	protoChats := make([]*reliantv1.Chat, len(chats))
	for i, c := range chats {
		protoChats[i] = chatToProto(c)
	}
	mainWorktrees := s.attachWorktrees(ctx, userID, filters.Projects(), scope, chats, protoChats)

	// Get latest user update sequence for stream sync —
	// the frontend uses this to connect the streaming RPC without redundant event replay.
	latestSeq, err := s.deps.Repo.GetLatestUserUpdateSequence(ctx, userID)
	if err != nil {
		// Non-fatal: log and return 0 so the frontend falls back to full replay
		logging.WarnContext(ctx, "Failed to get latest user update sequence", "error", err, "userID", userID)
	}

	return connect.NewResponse(&reliantv1.ListChatsResponse{
		Chats:                  protoChats,
		Total:                  int32(len(protoChats)),
		LastUserUpdateSequence: latestSeq,
		MainWorktrees:          mainWorktrees,
	}), nil
}

// validListProjectIDs refuses a project_ids list longer than
// core.MaxListProjectIDs, or one naming an empty id. An id the caller cannot
// read is not refused: it lists nothing, like such a project_id.
func validListProjectIDs(ids []string) error {
	if len(ids) > core.MaxListProjectIDs {
		return svcerr.InvalidArgument(fmt.Sprintf("project_ids names %d projects; at most %d", len(ids), core.MaxListProjectIDs))
	}
	for _, id := range ids {
		if id == "" {
			return svcerr.InvalidArgument("project_ids must not name an empty project id")
		}
	}
	return nil
}
