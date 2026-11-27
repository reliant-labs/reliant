package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ListItems implements the ListItems RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.ListItems
func (s *Service) ListItems(
	ctx context.Context,
	req *connect.Request[pb.ListItemsRequest],
) (*connect.Response[pb.ListItemsResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("ListItems"))
}
