package chat

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ListChats lists the caller's non-archived chats: in one project, or across
// all of their projects when project_id is empty (the sidebar's "All
// projects" view). Chats are filtered by owner in both cases.
func (s *Service) ListChats(
	ctx context.Context,
	req *reliantv1.ListChatsRequest,
) (*reliantv1.ListChatsResponse, error) {
	userID := auth.MustGetUserID(ctx)

	limit := int(100)
	if req.Limit != nil && *req.Limit > 0 {
		limit = int(*req.Limit)
	}

	var projectID *string
	if req.ProjectId != "" {
		projectID = &req.ProjectId
	}
	scope, err := s.listScope(ctx, userID, req.GetOrgId())
	if err != nil {
		return nil, err
	}
	filters := db.ChatFilters{
		UserID:          userID,
		ProjectID:       projectID,
		Org:             scope,
		Limit:           limit,
		AppName:         appNameFilter(req.AppName),
		ExcludeArchived: true,
		// The chat list holds conversations: USER-origin chats and adopted
		// runs. Every other run lists under Runs (RunService.ListRuns).
		SidebarOnly: true,
	}

	chats, err := s.deps.Repo.ListChats(ctx, filters)
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
	mainWorktrees := s.attachWorktrees(ctx, userID, req.ProjectId, scope, chats, protoChats)

	// Get latest user update sequence for stream sync —
	// the frontend uses this to connect the streaming RPC without redundant event replay.
	latestSeq, err := s.deps.Repo.GetLatestUserUpdateSequence(ctx, userID)
	if err != nil {
		// Non-fatal: log and return 0 so the frontend falls back to full replay
		logging.WarnContext(ctx, "Failed to get latest user update sequence", "error", err, "userID", userID)
	}

	return &reliantv1.ListChatsResponse{
		Chats:                  protoChats,
		Total:                  int32(len(protoChats)),
		LastUserUpdateSequence: latestSeq,
		MainWorktrees:          mainWorktrees,
	}, nil
}
