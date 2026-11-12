// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/ptr"
)

type ghMembers map[string]bool // "user@org"

func (m ghMembers) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	return m[userID+"@"+orgID], true, nil
}

func githubTrigger(t *testing.T, org string) *core.Trigger {
	t.Helper()
	cfg, err := json.Marshal(core.IntegrationConfig{Integration: "github", Events: []string{"issues.opened"}})
	require.NoError(t, err)
	tr := &core.Trigger{Kind: core.TriggerKindIntegration, Config: cfg}
	if org != "" {
		tr.OrgID = &org
	}
	return tr
}

func namingConnection(id string) *reliantv1.TriggerDefinition {
	return &reliantv1.TriggerDefinition{ConnectionId: &id}
}

func installationOf(t *testing.T, tr *core.Trigger) string {
	t.Helper()
	var cfg core.IntegrationConfig
	require.NoError(t, json.Unmarshal(tr.Config, &cfg))
	return cfg.Installation
}

func linkedApps() *StubOrgGitHubApps {
	return &StubOrgGitHubApps{ListOrgGitHubInstallationsFunc: func(_ context.Context, org string) ([]gitcredentialclient.OrgInstallation, error) {
		if org != "acme" {
			return nil, nil
		}
		return []gitcredentialclient.OrgInstallation{
			{InstallationID: 4242, AccountLogin: "acme"},
			{InstallationID: 7, AccountLogin: "acme-old", Suspended: true},
		}, nil
	}}
}

// A GitHub trigger of an org that names one of that org's linked, live
// installations records it in its config, so routing gives it only that
// installation's events; the rendered trigger names it back.
func TestBindOrgGitHubApp_RecordsTheInstallation(t *testing.T) {
	svc := NewTestTrigger(t, WithTriggerDeps(Deps{OrgGitHubApps: linkedApps()}))
	ctx := authz.WithMembership(context.Background(), ghMembers{"alice@acme": true})
	tr := githubTrigger(t, "acme")

	require.NoError(t, svc.bindOrgGitHubApp(ctx, "alice", namingConnection("controlplane:github-app:4242"), tr))
	assert.Equal(t, "4242", installationOf(t, tr))

	rendered, err := renderTrigger(&core.Trigger{ID: "t1", Kind: core.TriggerKindIntegration, Config: tr.Config}, nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "controlplane:github-app:4242", rendered.GetConnectionId(), "the definition round-trips")
}

// A definition that names no org installation is left exactly as it was.
func TestBindOrgGitHubApp_LeavesOtherDefinitionsAlone(t *testing.T) {
	svc := NewTestTrigger(t, WithTriggerDeps(Deps{OrgGitHubApps: linkedApps()}))
	for name, def := range map[string]*reliantv1.TriggerDefinition{
		"no definition":      nil,
		"no connection":      {},
		"a saved connection": namingConnection("conn-1"),
		"the personal bot":   namingConnection("controlplane:github-app"),
	} {
		t.Run(name, func(t *testing.T) {
			tr := githubTrigger(t, "acme")
			before := string(tr.Config)
			require.NoError(t, svc.bindOrgGitHubApp(context.Background(), "alice", def, tr))
			assert.Equal(t, before, string(tr.Config))
		})
	}
}

func TestBindOrgGitHubApp_Refusals(t *testing.T) {
	slack, err := json.Marshal(core.IntegrationConfig{Integration: "slack"})
	require.NoError(t, err)
	cases := map[string]struct {
		trigger *core.Trigger
		conn    string
		apps    OrgGitHubApps
		member  bool
		code    connect.Code
	}{
		"not an integration trigger": {
			trigger: &core.Trigger{Kind: core.TriggerKindSchedule}, conn: "controlplane:github-app:4242",
			apps: linkedApps(), member: true, code: connect.CodeInvalidArgument,
		},
		"another integration": {
			trigger: &core.Trigger{Kind: core.TriggerKindIntegration, Config: slack, OrgID: ptr.Of("acme")}, conn: "controlplane:github-app:4242",
			apps: linkedApps(), member: true, code: connect.CodeInvalidArgument,
		},
		"a trigger of no org": {
			trigger: githubTrigger(t, ""), conn: "controlplane:github-app:4242",
			apps: linkedApps(), member: true, code: connect.CodeFailedPrecondition,
		},
		"owner not a member": {
			trigger: githubTrigger(t, "acme"), conn: "controlplane:github-app:4242",
			apps: linkedApps(), member: false, code: connect.CodePermissionDenied,
		},
		"installation not linked to the org": {
			trigger: githubTrigger(t, "acme"), conn: "controlplane:github-app:999",
			apps: linkedApps(), member: true, code: connect.CodeFailedPrecondition,
		},
		"installation suspended": {
			trigger: githubTrigger(t, "acme"), conn: "controlplane:github-app:7",
			apps: linkedApps(), member: true, code: connect.CodeFailedPrecondition,
		},
		"no control plane": {
			trigger: githubTrigger(t, "acme"), conn: "controlplane:github-app:4242",
			apps: nil, member: true, code: connect.CodeFailedPrecondition,
		},
		"control plane unreachable": {
			trigger: githubTrigger(t, "acme"), conn: "controlplane:github-app:4242",
			apps: &StubOrgGitHubApps{ListOrgGitHubInstallationsFunc: func(context.Context, string) ([]gitcredentialclient.OrgInstallation, error) {
				return nil, errors.New("down")
			}},
			member: true, code: connect.CodeUnavailable,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc := NewTestTrigger(t, WithTriggerDeps(Deps{OrgGitHubApps: tc.apps}))
			ctx := authz.WithMembership(context.Background(), ghMembers{"alice@acme": tc.member})
			before := string(tc.trigger.Config)
			err := svc.bindOrgGitHubApp(ctx, "alice", namingConnection(tc.conn), tc.trigger)
			require.Error(t, err)
			assert.Equal(t, tc.code, svcerr.Code(err), "error: %v", err)
			assert.Equal(t, before, string(tc.trigger.Config), "a refusal binds nothing")
		})
	}
}
