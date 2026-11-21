package system

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// DevAuthClear clears the global auth session (development only)
func (s *Service) DevAuthClear(
	ctx context.Context,
	req *connect.Request[reliantv1.DevAuthClearRequest],
) (*connect.Response[reliantv1.DevAuthClearResponse], error) {
	return nil, svcerr.Unimplemented("DevAuthClear is not supported")
}
