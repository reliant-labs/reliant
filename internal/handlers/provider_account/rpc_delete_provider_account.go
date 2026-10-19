package provider_account

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// DeleteProviderAccount implements the DeleteProviderAccount RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=provider_account.DeleteProviderAccount
func (s *Service) DeleteProviderAccount(
	ctx context.Context,
	req *connect.Request[pb.DeleteProviderAccountRequest],
) (*connect.Response[pb.DeleteProviderAccountResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("DeleteProviderAccount"))
}
