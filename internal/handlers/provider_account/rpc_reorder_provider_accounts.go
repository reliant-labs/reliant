package provider_account

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ReorderProviderAccounts implements the ReorderProviderAccounts RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=provider_account.ReorderProviderAccounts
func (s *Service) ReorderProviderAccounts(
	ctx context.Context,
	req *connect.Request[pb.ReorderProviderAccountsRequest],
) (*connect.Response[pb.ReorderProviderAccountsResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("ReorderProviderAccounts"))
}
