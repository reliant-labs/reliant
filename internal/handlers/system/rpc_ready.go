package system

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	"go.temporal.io/sdk/client"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// Ready returns the readiness status of the service
func (s *Service) Ready(
	ctx context.Context,
	req *connect.Request[reliantv1.ReadyRequest],
) (*connect.Response[reliantv1.ReadyResponse], error) {
	var failures []string

	// Check database connectivity
	if s.deps.Repo != nil {
		if err := s.deps.Repo.Ping(ctx); err != nil {
			failures = append(failures, "db: "+err.Error())
		}
	}

	// Check Temporal connectivity
	if s.deps.Temporal != nil {
		_, err := s.deps.Temporal.CheckHealth(ctx, &client.CheckHealthRequest{})
		if err != nil {
			failures = append(failures, "temporal: "+err.Error())
		}
	}

	// Check NATS connectivity
	if s.deps.NATSConnected != nil && !s.deps.NATSConnected() {
		failures = append(failures, "nats: disconnected")
	}

	// Check streaming hub connectivity
	if s.deps.StreamingHub != nil && !s.deps.StreamingHub.IsConnected() {
		failures = append(failures, "nats-streaming: disconnected")
	}

	status := "ready"
	if len(failures) > 0 {
		status = "not_ready: " + strings.Join(failures, "; ")
	}

	resp := &reliantv1.ReadyResponse{
		Status:    status,
		Timestamp: time.Now().UTC().Unix(),
	}
	return connect.NewResponse(resp), nil
}
