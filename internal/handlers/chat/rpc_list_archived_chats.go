package chat

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
)

// ListArchivedChats lists the caller's archived chats with worktree info: the
// named org's (orgs.ListScope), or every org's when it names none.
func (s *Service) ListArchivedChats(
	ctx context.Context,
	req *reliantv1.ListArchivedChatsRequest,
) (*reliantv1.ListArchivedChatsResponse, error) {
	userID := auth.MustGetUserID(ctx)

	scope, err := s.listScope(ctx, userID, req.GetOrgId())
	if err != nil {
		return nil, err
	}
	archivedChats, err := s.deps.Repo.ListArchivedChats(ctx, userID, scope)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list archived chats"))
	}
	archivedChats = authz.Visible(ctx, userID, archivedChats, func(ctx context.Context, userID string, ac *db.ArchivedChatInfo) bool {
		return authz.CanReadChat(ctx, userID, &ac.Chat)
	})

	protoChats := make([]*reliantv1.ArchivedChat, len(archivedChats))
	for i, ac := range archivedChats {
		acProto := chatToProto(&ac.Chat)
		protoChats[i] = &reliantv1.ArchivedChat{
			Chat: acProto,
		}
		if ac.WorktreeName != nil {
			protoChats[i].WorktreeName = ac.WorktreeName
		}
		if ac.WorktreeDeletedAt != nil {
			deletedAt := ac.WorktreeDeletedAt.Format(time.RFC3339)
			protoChats[i].WorktreeDeletedAt = &deletedAt
		}
	}

	return &reliantv1.ListArchivedChatsResponse{
		Chats: protoChats,
		Total: int32(len(protoChats)),
	}, nil
}
