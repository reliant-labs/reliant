package connection

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
	"github.com/reliant-labs/reliant/internal/connections"
)

func (s *Service) ListIntegrations(ctx context.Context, req *connect.Request[reliantv1.ListIntegrationsRequest]) (*connect.Response[reliantv1.ListIntegrationsResponse], error) {
	userID, err := connectionrpc.Caller(ctx)
	if err != nil {
		return nil, err
	}
	integrations := s.deps.Connections.ListIntegrations()
	// The org's policy only when the request names the org: an old client,
	// which never does, sees the catalog as before.
	var policies map[string]connections.IntegrationPolicy
	if org := req.Msg.GetOrgId(); org != "" {
		ids := make([]string, 0, len(integrations))
		for _, i := range integrations {
			ids = append(ids, i.ID)
		}
		if policies, err = s.deps.Connections.OrgIntegrationPolicies(ctx, userID, org, ids); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("connection service error"))
		}
	}
	out := []*reliantv1.Integration{}
	for _, i := range integrations {
		in := &reliantv1.Integration{Id: i.ID, DisplayName: i.DisplayName}
		for _, m := range i.Methods {
			in.Methods = append(in.Methods, &reliantv1.IntegrationAuthMethod{
				Kind: connectionrpc.AuthKind(m.Kind), Available: m.Available,
				UnavailableReason: m.Reason, FieldLabels: m.FieldLabels,
			})
		}
		for _, p := range i.Params {
			in.ConnectionParams = append(in.ConnectionParams, &reliantv1.IntegrationConnectionParam{
				Name: p.GetName(), DisplayName: p.GetDisplayName(), Description: p.GetDescription(),
				Pattern: p.GetPattern(), DefaultValue: p.GetDefaultValue(), Required: p.GetDefaultValue() == "",
			})
		}
		if pol, ok := policies[i.ID]; ok {
			in.OrgPolicy = &reliantv1.IntegrationOrgPolicy{Allowed: pol.Allowed, Enforced: pol.Enforced, PolicyVersion: pol.Version}
		}
		out = append(out, in)
	}
	return connect.NewResponse(&reliantv1.ListIntegrationsResponse{Integrations: out}), nil
}
