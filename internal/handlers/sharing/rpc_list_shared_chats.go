package sharing

import (
	"context"
	"fmt"

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
	// A project list the request may name, as ListChats checks it.
	if n := len(req.Msg.ProjectIds); n > core.MaxListProjectIDs {
		return nil, svcerr.InvalidArgument(fmt.Sprintf("project_ids names %d projects; at most %d", n, core.MaxListProjectIDs))
	}
	for _, id := range req.Msg.ProjectIds {
		if id == "" {
			return nil, svcerr.InvalidArgument("project_ids must not name an empty project id")
		}
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
	filters := core.SharedChatFilters{
		OrgID: org, ViewerUserID: userID, ProjectID: req.Msg.ProjectId, ProjectIDs: req.Msg.ProjectIds,
		Limit: limit, Offset: offset,
	}
	// An app's chat list; "" is the same as unset.
	if app := req.Msg.GetAppName(); app != "" {
		filters.AppName = &app
	}
	chats, err := s.deps.Repo.ListSharedChats(ctx, filters)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list shared chats"))
	}
	ctx = authz.WithViewerReads(ctx)
	for _, c := range authz.SharedChatsFor(ctx, userID, chats) {
		out.Chats = append(out.Chats, chatproto.ForViewer(chatproto.Chat(c)))
	}
	return connect.NewResponse(out), nil
}
