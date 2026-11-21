package system

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ============================================
// Dev Auth endpoints
// ============================================

// DevAuthLoad loads the global auth session (development only)
func (s *Service) DevAuthLoad(
	ctx context.Context,
	req *connect.Request[reliantv1.DevAuthLoadRequest],
) (*connect.Response[reliantv1.DevAuthLoadResponse], error) {
	return nil, svcerr.Unimplemented("DevAuthLoad is not supported")
}
