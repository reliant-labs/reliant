package provider_account

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ListProviderAccounts implements the ListProviderAccounts RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=provider_account.ListProviderAccounts
func (s *Service) ListProviderAccounts(
	ctx context.Context,
	req *connect.Request[pb.ListProviderAccountsRequest],
) (*connect.Response[pb.ListProviderAccountsResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("ListProviderAccounts"))
}
