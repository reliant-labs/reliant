package daemon_registry

import (
	"context"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

func (s *Service) RefreshLocalModels(
	ctx context.Context,
	req *connect.Request[reliantv1.RefreshLocalModelsRequest],
) (*connect.Response[reliantv1.RefreshLocalModelsResponse], error) {
	userID, router, err := s.ownedOnlineDaemon(ctx, req.Msg.GetDaemonId())
	if err != nil {
		return nil, err
	}
	inv, err := router.RefreshLocalModels(ctx, userID, req.Msg.GetDaemonId())
	if err != nil {
		return nil, localModelRelayError("refresh local models", err)
	}
	return connect.NewResponse(&reliantv1.RefreshLocalModelsResponse{LocalModels: inv}), nil
}
