package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// UpdateItem implements the UpdateItem RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.UpdateItem
func (s *Service) UpdateItem(
	ctx context.Context,
	req *connect.Request[pb.UpdateItemRequest],
) (*connect.Response[pb.UpdateItemResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("UpdateItem"))
}
