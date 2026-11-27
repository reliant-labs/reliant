package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// GetItem implements the GetItem RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.GetItem
func (s *Service) GetItem(
	ctx context.Context,
	req *connect.Request[pb.GetItemRequest],
) (*connect.Response[pb.GetItemResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("GetItem"))
}
