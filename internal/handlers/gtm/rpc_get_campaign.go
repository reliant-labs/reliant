package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// GetCampaign implements the GetCampaign RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.GetCampaign
func (s *Service) GetCampaign(
	ctx context.Context,
	req *connect.Request[pb.GetCampaignRequest],
) (*connect.Response[pb.GetCampaignResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("GetCampaign"))
}
