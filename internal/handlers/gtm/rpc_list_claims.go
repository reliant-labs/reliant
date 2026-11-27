package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ListClaims implements the ListClaims RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.ListClaims
func (s *Service) ListClaims(
	ctx context.Context,
	req *connect.Request[pb.ListClaimsRequest],
) (*connect.Response[pb.ListClaimsResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("ListClaims"))
}
