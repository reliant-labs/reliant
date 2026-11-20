package chat

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
)

// SearchChats searches the caller's chats by title and message content: in
// one project, or in all of their projects when project_id is empty; in the
// named org's chats (orgs.ListScope), or in every org's when it names none.
func (s *Service) SearchChats(
	ctx context.Context,
	req *connect.Request[reliantv1.SearchChatsRequest],
) (*connect.Response[reliantv1.SearchChatsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Return empty results if no query
	if req.Msg.Query == "" {
		return connect.NewResponse(&reliantv1.SearchChatsResponse{
			Chats: []*reliantv1.Chat{},
			Total: 0,
		}), nil
	}

	limit := 100
	if req.Msg.Limit != nil && *req.Msg.Limit > 0 {
		limit = int(*req.Msg.Limit)
	}

	scope, err := s.listScope(ctx, userID, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	filters := db.ChatSearchFilters{
		UserID:      userID,
		ProjectID:   req.Msg.ProjectId,
		SearchQuery: req.Msg.Query,
		Org:         scope,
		Limit:       limit,
	}

	chats, err := s.deps.Repo.SearchChats(ctx, filters)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to search chats"))
	}
	chats = authz.Visible(ctx, userID, chats, authz.CanReadChat[*db.Chat])

	protoChats := make([]*reliantv1.Chat, len(chats))
	for i, c := range chats {
		protoChats[i] = chatToProto(c)
	}

	return connect.NewResponse(&reliantv1.SearchChatsResponse{
		Chats: protoChats,
		Total: int32(len(protoChats)),
	}), nil
}
