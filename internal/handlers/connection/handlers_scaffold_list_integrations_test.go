package connection_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/connections"
	connection "github.com/reliant-labs/reliant/internal/handlers/connection"
)

// connection_test.go covers the catalog a deployment's providers build; these
// rows need none.
func TestListIntegrations_Generated(t *testing.T) {
	t.Parallel()

	// One integration, no auth methods.
	svc := connection.NewTestConnection(t, connection.WithConnectionDeps(connection.Deps{Connections: &connection.StubConnections{
		ListIntegrationsFunc: func() []connections.Integration {
			return []connections.Integration{{ID: "github", DisplayName: "GitHub"}}
		},
	}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListIntegrationsRequest, pb.ListIntegrationsResponse]{
		{
			Name:    "a caller without a user is unauthenticated",
			Req:     connect.NewRequest(&pb.ListIntegrationsRequest{}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name: "the integrations are listed",
			Req:  connect.NewRequest(&pb.ListIntegrationsRequest{}),
			Ctx:  connection.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ListIntegrationsResponse]) {
				if got := resp.Msg.GetIntegrations(); len(got) != 1 || got[0].GetId() != "github" {
					t.Fatalf("integrations = %v, want github", got)
				}
			},
		},
	}, svc.ListIntegrations)
}

// The picker's org policy marker (control-plane docs/design/org-policies.md
// §4.3): set only when the request names an org whose policy governs the
// integration; a request without org_id is answered as before the field.
func TestListIntegrations_OrgPolicyMarker(t *testing.T) {
	t.Parallel()

	svc := connection.NewTestConnection(t, connection.WithConnectionDeps(connection.Deps{Connections: &connection.StubConnections{
		ListIntegrationsFunc: func() []connections.Integration {
			return []connections.Integration{{ID: "github", DisplayName: "GitHub"}, {ID: "slack", DisplayName: "Slack"}}
		},
		OrgIntegrationPoliciesFunc: func(_ context.Context, _, orgID string, _ []string) (map[string]connections.IntegrationPolicy, error) {
			if orgID != "org-a" {
				return nil, connections.ErrInvalidArgument
			}
			return map[string]connections.IntegrationPolicy{"github": {Allowed: false, Enforced: true, Version: 5}}, nil
		},
	}}))
	org := "org-a"

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListIntegrationsRequest, pb.ListIntegrationsResponse]{
		{
			Name: "no org: no marker, the org's policy is not asked",
			Req:  connect.NewRequest(&pb.ListIntegrationsRequest{}),
			Ctx:  connection.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ListIntegrationsResponse]) {
				for _, i := range resp.Msg.GetIntegrations() {
					if i.GetOrgPolicy() != nil {
						t.Fatalf("%s carries an org policy without an org: %v", i.GetId(), i.GetOrgPolicy())
					}
				}
			},
		},
		{
			Name: "an org: the governed integration is marked",
			Req:  connect.NewRequest(&pb.ListIntegrationsRequest{OrgId: &org}),
			Ctx:  connection.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ListIntegrationsResponse]) {
				got := map[string]*pb.IntegrationOrgPolicy{}
				for _, i := range resp.Msg.GetIntegrations() {
					got[i.GetId()] = i.GetOrgPolicy()
				}
				if gh := got["github"]; gh == nil || gh.GetAllowed() || !gh.GetEnforced() || gh.GetPolicyVersion() != 5 {
					t.Fatalf("github marker = %v", gh)
				}
				if got["slack"] != nil {
					t.Fatalf("slack is not governed, got %v", got["slack"])
				}
			},
		},
	}, svc.ListIntegrations)
}
