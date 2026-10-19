package provider_account

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// RefreshProviderAccountUsage implements the RefreshProviderAccountUsage RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=provider_account.RefreshProviderAccountUsage
func (s *Service) RefreshProviderAccountUsage(
	ctx context.Context,
	req *connect.Request[pb.RefreshProviderAccountUsageRequest],
) (*connect.Response[pb.RefreshProviderAccountUsageResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("RefreshProviderAccountUsage"))
}
