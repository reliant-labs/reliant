package pipeline

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ListCapabilityGrants lists the capability grants the caller made, newest
// first. A grant is only ever its owner's to read: the list is scoped to the
// caller, so no other user's grant can be named.
func (s *Service) ListCapabilityGrants(
	ctx context.Context,
	req *connect.Request[pb.ListCapabilityGrantsRequest],
) (*connect.Response[pb.ListCapabilityGrantsResponse], error) {
	if err := s.enabled(); err != nil {
		return nil, svcerr.Wrap(err)
	}
	userID, err := callerID(ctx)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	if s.deps.Grants == nil {
		return nil, svcerr.Wrap(svcerr.Unavailable("capability grants are not available on this server"))
	}
	grants, err := s.deps.Grants.List(ctx, userID, strings.TrimSpace(req.Msg.GetTriggerId()), req.Msg.GetIncludeRevoked())
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	out := &pb.ListCapabilityGrantsResponse{}
	for _, g := range grants {
		out.Grants = append(out.Grants, grantProto(g))
	}
	return connect.NewResponse(out), nil
}
