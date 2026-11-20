package connector

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connectorgrant"
	"github.com/reliant-labs/reliant/internal/mcpserver"
)

// CreateConnector mints a credential bound to one daemon.
func (s *Service) CreateConnector(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateConnectorRequest],
) (*connect.Response[reliantv1.CreateConnectorResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}

	msg := req.Msg
	grant, err := s.buildGrant(userID, msg)
	if err != nil {
		return nil, err
	}

	raw, err := s.createGrantWithCredential(ctx, grant)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to create connector"))
	}

	return connect.NewResponse(&reliantv1.CreateConnectorResponse{
		Connector: toProtoConnector(grant),
		// Returned exactly once; the authority holds only its hash.
		Credential: raw,
		McpUrl:     s.mcpURLFor(grant.KindOrDefault()),
	}), nil
}

// mcpURLFor is the endpoint the user pastes into their MCP client for a
// connector of kind.
func (s *Service) mcpURLFor(kind connectorgrant.Kind) string {
	path := mcpserver.MountPath
	if kind == connectorgrant.KindForge {
		path = mcpserver.ForgeMountPath
	}
	// Without a configured public URL, return the path alone rather than
	// inventing a hostname that would silently be wrong.
	return s.publicURL + path
}
