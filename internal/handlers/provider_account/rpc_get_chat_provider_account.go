package provider_account

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// GetChatProviderAccount implements the GetChatProviderAccount RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=provider_account.GetChatProviderAccount
func (s *Service) GetChatProviderAccount(
	ctx context.Context,
	req *connect.Request[pb.GetChatProviderAccountRequest],
) (*connect.Response[pb.GetChatProviderAccountResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("GetChatProviderAccount"))
}
