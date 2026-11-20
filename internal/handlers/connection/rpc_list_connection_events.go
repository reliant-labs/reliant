package connection

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
	"github.com/reliant-labs/reliant/internal/db/core"
)

func (s *Service) ListConnectionEvents(ctx context.Context, req *reliantv1.ListConnectionEventsRequest) (*reliantv1.ListConnectionEventsResponse, error) {
	org := req.GetOwnerOrgId()
	caller := connectionrpc.Caller
	if org != "" {
		caller = orgAdminCaller
	}
	userID, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	var evs []core.ConnectionEvent
	if org != "" {
		evs, err = s.deps.Connections.EventsOrg(ctx, userID, org, req.GetId(), int(req.GetLimit()), req.GetBeforeId())
	} else {
		evs, err = s.deps.Connections.Events(ctx, userID, req.GetId(), int(req.GetLimit()), req.GetBeforeId())
	}
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("connection service error"))
	}
	out := make([]*reliantv1.ConnectionEvent, 0, len(evs))
	for _, e := range evs {
		out = append(out, &reliantv1.ConnectionEvent{
			Id: e.ID, ConnectionId: e.ConnectionID, Kind: e.Kind, RunId: e.RunID, NodeId: e.NodeID,
			ToolCallId: e.ToolCallID, Actor: e.Actor, At: e.At.UTC().Format(time.RFC3339), UserId: e.UserID,
		})
	}
	return &reliantv1.ListConnectionEventsResponse{Events: out}, nil
}
