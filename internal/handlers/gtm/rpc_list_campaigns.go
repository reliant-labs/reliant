package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ListCampaigns implements the ListCampaigns RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.ListCampaigns
func (s *Service) ListCampaigns(
	ctx context.Context,
	req *connect.Request[pb.ListCampaignsRequest],
) (*connect.Response[pb.ListCampaignsResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("ListCampaigns"))
}
