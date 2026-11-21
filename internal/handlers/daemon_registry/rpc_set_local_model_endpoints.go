package daemon_registry

import (
	"context"
	"encoding/json"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

func (s *Service) SetLocalModelEndpoints(
	ctx context.Context,
	req *connect.Request[reliantv1.SetLocalModelEndpointsRequest],
) (*connect.Response[reliantv1.SetLocalModelEndpointsResponse], error) {
	daemonID := req.Msg.GetDaemonId()
	userID, router, err := s.ownedOnlineDaemon(ctx, daemonID)
	if err != nil {
		return nil, err
	}
	baseURLs := req.Msg.GetBaseUrls()
	if baseURLs == nil {
		baseURLs = []string{}
	}
	payload, err := json.Marshal(map[string][]string{"base_urls": baseURLs})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("marshal request"))
	}
	if _, err := router.SendDaemonCommandToDaemon(ctx, userID, daemonID, toolexec.CommandLocalModelsSetEndpoints, payload, 15000); err != nil {
		return nil, localModelRelayError("write local model endpoints", err)
	}
	inv, err := router.RefreshLocalModels(ctx, userID, daemonID)
	if err != nil {
		return nil, localModelRelayError("refresh local models", err)
	}
	return connect.NewResponse(&reliantv1.SetLocalModelEndpointsResponse{LocalModels: inv}), nil
}
