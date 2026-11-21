package connection

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
	"github.com/reliant-labs/reliant/internal/connections"
)

func (s *Service) CompleteOAuth(ctx context.Context, req *connect.Request[reliantv1.CompleteOAuthRequest]) (*connect.Response[reliantv1.CompleteOAuthResponse], error) {
	userID, err := connectionrpc.Caller(ctx)
	if err != nil {
		return nil, err
	}
	// Whether the caller is a person only matters to a flow started for an
	// org, which only a person may finish; a personal flow is finished as
	// before, by any credential of the user who started it.
	done, err := s.deps.Connections.CompleteOAuthFlow(ctx, connections.CompleteParams{
		State: req.Msg.GetState(), Code: req.Msg.GetCode(), UserID: userID,
		Person: auth.RequirePerson(ctx, "connect an organization's account") == nil,
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("connection service error"))
	}
	return connect.NewResponse(&reliantv1.CompleteOAuthResponse{
		Connection: connectionToProto(done.Connection), RedirectAfter: done.RedirectAfter,
	}), nil
}
