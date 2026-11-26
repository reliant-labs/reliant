package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// GetGtmAccess implements the GetGtmAccess RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.GetGtmAccess
func (s *Service) GetGtmAccess(
	ctx context.Context,
	req *connect.Request[pb.GetGtmAccessRequest],
) (*connect.Response[pb.GetGtmAccessResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("GetGtmAccess"))
}
