package daemon_registry

import (
	"context"
	"encoding/json"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/diskwatch"
)

// ClearMachineCaches relays a confirmed cache clean-up to the machine. The
// daemon decides what is safe (diskwatch.ClearCaches) and starts it in the
// background; this returns what started and what it refused.
func (s *Service) ClearMachineCaches(
	ctx context.Context,
	req *connect.Request[reliantv1.ClearMachineCachesRequest],
) (*connect.Response[reliantv1.ClearMachineCachesResponse], error) {
	daemonID := req.Msg.GetDaemonId()
	if len(req.Msg.GetKinds()) == 0 {
		return nil, svcerr.InvalidArgument("kinds is required")
	}
	userID, router, err := s.ownedOnlineDaemon(ctx, daemonID)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(diskwatch.ClearCachesRequest{Kinds: req.Msg.GetKinds()})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("marshal request"))
	}
	raw, err := router.SendDaemonCommandToDaemon(ctx, userID, daemonID, diskwatch.ClearCachesCommand, payload, 15000)
	if err != nil {
		return nil, localModelRelayError("daemon command "+diskwatch.ClearCachesCommand, err)
	}
	var out diskwatch.ClearCachesResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal(fmt.Sprintf("decode %s response", diskwatch.ClearCachesCommand)))
	}
	resp := &reliantv1.ClearMachineCachesResponse{Started: out.Started}
	for _, sk := range out.Skipped {
		resp.Skipped = append(resp.Skipped, &reliantv1.ClearMachineCachesSkip{Kind: sk.Kind, Reason: sk.Reason})
	}
	if len(resp.Started) == 0 {
		activity.Skip(ctx) // the machine refused every kind: nothing is being cleared
	}
	return connect.NewResponse(resp), nil
}
