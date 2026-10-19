package provider_account

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// UpdateProviderAccount implements the UpdateProviderAccount RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=provider_account.UpdateProviderAccount
func (s *Service) UpdateProviderAccount(
	ctx context.Context,
	req *connect.Request[pb.UpdateProviderAccountRequest],
) (*connect.Response[pb.UpdateProviderAccountResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("UpdateProviderAccount"))
}
