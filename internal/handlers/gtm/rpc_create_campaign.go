package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// CreateCampaign implements the CreateCampaign RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.CreateCampaign
func (s *Service) CreateCampaign(
	ctx context.Context,
	req *connect.Request[pb.CreateCampaignRequest],
) (*connect.Response[pb.CreateCampaignResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("CreateCampaign"))
}
