package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// ResolveItemSend settles a held email (Service.ResolveInDoubtSend): with a
// Gmail message id it records the item SENT, without one it releases the
// hold. A person's act only.
func (s *Service) ResolveItemSend(
	ctx context.Context,
	req *connect.Request[pb.ResolveItemSendRequest],
) (*connect.Response[pb.ResolveItemSendResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	item, err := s.deps.Service.ResolveInDoubtSend(ctx, a, gtmsvc.ResolveInDoubtSendInput{
		ItemID:         m.GetId(),
		ApprovalHash:   m.GetApprovalHash(),
		GmailMessageID: m.GmailMessageId,
	})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.ResolveItemSendResponse{Item: itemOut(item)}), nil
}
