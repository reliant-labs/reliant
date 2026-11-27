package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// GetGtmAccess reports whether GTM is enabled for the caller in the org. It
// is never an error for a caller GTM refuses, and it says nothing of why: a
// refusal, a gate outage and GTM not being wired are all enabled = false.
func (s *Service) GetGtmAccess(
	ctx context.Context,
	req *connect.Request[pb.GetGtmAccessRequest],
) (*connect.Response[pb.GetGtmAccessResponse], error) {
	a, err := actor(ctx, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.GetGtmAccessResponse{Enabled: s.enabled(ctx, a)}), nil
}
