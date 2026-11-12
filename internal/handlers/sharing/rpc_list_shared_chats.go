package sharing

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/chatproto"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// ListSharedChats lists the chats other members shared with an org the
// caller is a live member of: the store's visibility predicate, then the
// viewer rule per row (a person, a live member, no credential bound
// elsewhere), each in the viewer's shape.
func (s *Service) ListSharedChats(
	ctx context.Context,
	req *connect.Request[pb.ListSharedChatsRequest],
) (*connect.Response[pb.ListSharedChatsResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if err := auth.RequirePerson(ctx, "read what others shared with your organization"); err != nil {
		return nil, err
	}
	org, err := s.sharedOrg(ctx, userID, req.Msg.OrgId)
	if err != nil {
		return nil, err
	}
	out := &pb.ListSharedChatsResponse{Chats: []*pb.Chat{}}
	if org == "" {
		return connect.NewResponse(out), nil
	}
	limit, offset := page(req.Msg.GetLimit(), req.Msg.GetOffset())
	chats, err := s.deps.Repo.ListSharedChats(ctx, core.SharedChatFilters{
		OrgID: org, ViewerUserID: userID, ProjectID: req.Msg.ProjectId, Limit: limit, Offset: offset,
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list shared chats"))
	}
	ctx = authz.WithViewerReads(ctx)
	for _, c := range authz.SharedChatsFor(ctx, userID, chats) {
		out.Chats = append(out.Chats, chatproto.ForViewer(chatproto.Chat(c)))
	}
	return connect.NewResponse(out), nil
}
