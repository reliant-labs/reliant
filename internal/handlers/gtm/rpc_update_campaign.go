package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// UpdateCampaign implements the UpdateCampaign RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.UpdateCampaign
func (s *Service) UpdateCampaign(
	ctx context.Context,
	req *connect.Request[pb.UpdateCampaignRequest],
) (*connect.Response[pb.UpdateCampaignResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("UpdateCampaign"))
}
