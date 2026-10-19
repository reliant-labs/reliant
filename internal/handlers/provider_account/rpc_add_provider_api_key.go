package provider_account

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// AddProviderAPIKey implements the AddProviderAPIKey RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=provider_account.AddProviderAPIKey
func (s *Service) AddProviderAPIKey(
	ctx context.Context,
	req *connect.Request[pb.AddProviderAPIKeyRequest],
) (*connect.Response[pb.AddProviderAPIKeyResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("AddProviderAPIKey"))
}
