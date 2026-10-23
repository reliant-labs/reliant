// Copyright (c) 2025 Reliant Labs
package forge

import (
	"testing"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/wirecheck"
)

// A plan whose forge run could not authenticate to the control plane is the
// rpc's ERROR, with forge's reason and scopes carried onto it
// (13-p9-frontend-plan.md §7.1 #7): the deploy panel tells "sign in" from "ask
// an admin" by the reason, and names the grant from the scopes. The sentence
// keeps forge's own words, which clients from before the reason match on.
func TestForgeService_PlanDeploy_AuthzRefusalOnTheWire(t *testing.T) {
	for _, tc := range []struct {
		name   string
		report string
		want   wirecheck.Want
	}{
		{
			name: "the control plane refused a missing scope",
			report: `{"env":"prod","ok":false,"exit_code":1,` +
				`"error":"your organization permissions do not include deploy:write; ask an organization admin to grant it",` +
				`"error_code":"permission_denied","error_reason":"scope_missing","error_scopes":["deploy:write"]}`,
			want: wirecheck.Want{
				Code: connect.CodePermissionDenied,
				Message: "daemon command forge.deploy_plan failed: forge command failed: exit 1: " +
					"your organization permissions do not include deploy:write; ask an organization admin to grant it",
				Reason: errreason.ScopeMissing,
				Meta:   map[string]string{fat.ScopesHeader: "deploy:write"},
			},
		},
		{
			name: "forge holds no credential",
			report: `{"env":"prod","ok":false,"exit_code":1,` +
				`"error":"no control-plane credential for https://cp.example.com\nfix: forge login",` +
				`"error_code":"unauthenticated","error_reason":"no_control_plane_credential"}`,
			want: wirecheck.Want{
				// Never Unauthenticated on a reliant rpc: that code signs the
				// user out of Reliant over a credential forge holds.
				Code: connect.CodeFailedPrecondition,
				Message: "daemon command forge.deploy_plan failed: forge command failed: exit 1: " +
					"no control-plane credential for https://cp.example.com | fix: forge login",
				Reason: errreason.NoControlPlaneCredential,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := &forgeTestRouter{reply: deployReply(t, tc.report, map[string]any{"exit_code": 1})}
			_, err := newForgeTestService(router).PlanDeploy(authedCtx(),
				connect.NewRequest(&reliantv1.PlanForgeDeployRequest{ProjectId: "p", Env: "prod"}))
			wirecheck.Assert(t, err, tc.want)
		})
	}
}

// Every other failed document stays data — a refused guard, a failed apply, a
// reason this layer has no branch for — so a UI renders what forge said.
func TestForgeService_PlanDeploy_OtherFailuresStayData(t *testing.T) {
	for _, report := range []string{
		`{"env":"prod","ok":false,"exit_code":1,"error":"preflight: missing secret key"}`,
		`{"env":"prod","ok":false,"exit_code":1,"error":"x","error_code":"failed_precondition","error_reason":"rollout_in_flight"}`,
		`{"env":"prod","ok":true,"exit_code":0,"error_reason":"scope_missing"}`,
	} {
		router := &forgeTestRouter{reply: deployReply(t, report, map[string]any{"exit_code": 1})}
		resp, err := newForgeTestService(router).PlanDeploy(authedCtx(),
			connect.NewRequest(&reliantv1.PlanForgeDeployRequest{ProjectId: "p", Env: "prod"}))
		require.NoError(t, err, report)
		assert.JSONEq(t, report, resp.Msg.ReportJson)
	}
}
