package system

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// DevAuthSave saves the global auth session (development only)
func (s *Service) DevAuthSave(
	ctx context.Context,
	req *connect.Request[reliantv1.DevAuthSaveRequest],
) (*connect.Response[reliantv1.DevAuthSaveResponse], error) {
	return nil, svcerr.Unimplemented("DevAuthSave is not supported")
}
