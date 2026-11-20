package connection

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
)

func (s *Service) ListConnections(ctx context.Context, req *reliantv1.ListConnectionsRequest) (*reliantv1.ListConnectionsResponse, error) {
	userID, err := connectionrpc.Caller(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := s.deps.Connections.List(ctx, userID, req.GetIntegrationId())
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("connection service error"))
	}
	// The org's own connections only when the request names the org: an old
	// client, which never does, sees exactly its own, as before.
	org := req.GetOrgId()
	if org != "" {
		orgs, err := s.deps.Connections.ListOrg(ctx, userID, org, req.GetIntegrationId())
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("connection service error"))
		}
		cs = append(cs, orgs...)
	}
	out := make([]*reliantv1.Connection, 0, len(cs))
	for _, c := range cs {
		out = append(out, connectionToProto(c))
	}
	// ListOrg has established the caller is a live member of org.
	if org != "" {
		out = append(out, s.orgGitHubApps(ctx, org, req.GetIntegrationId())...)
	}
	return &reliantv1.ListConnectionsResponse{Connections: out}, nil
}
