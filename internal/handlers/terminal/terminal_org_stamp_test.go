// Copyright (c) 2025 Reliant Labs
package terminal

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// orgProjects is fakeTerminalCheckouts whose projects belong to an org.
type orgProjects struct {
	fakeTerminalCheckouts
	orgs map[string]string
}

func (f orgProjects) GetProject(ctx context.Context, id string) (*db.Project, error) {
	p, err := f.fakeTerminalCheckouts.GetProject(ctx, id)
	if org, ok := f.orgs[id]; ok && err == nil {
		p.OrgID = &org
	}
	return p, err
}

// forcingOrg forces credentials:none on org-force's machines, in mode.
type forcingOrg struct{ mode controlplanev1.OrgPolicyMode }

func (s forcingOrg) ResolveOrgPolicy(_ context.Context, orgID string) (accesstokenclient.OrgPolicy, error) {
	if orgID != "org-force" {
		return accesstokenclient.OrgPolicy{}, nil
	}
	return accesstokenclient.OrgPolicy{Version: 6, Policy: &controlplanev1.OrgPolicy{Machines: &controlplanev1.MachinesPolicy{
		Mode: s.mode, ForceCredentialsNone: true,
	}}}, nil
}

func TestTerminalOrgStamp(t *testing.T) {
	prev := orgpolicy.EnforcementOf(orgpolicy.PointMachines)
	orgpolicy.SetProcessEnforcement(orgpolicy.PointMachines, orgpolicy.EnforcementEnforce)
	t.Cleanup(func() { orgpolicy.SetProcessEnforcement(orgpolicy.PointMachines, prev) })

	force, other := "org-force", "org-other"
	cfg := terminalWSConfig{checkouts: orgProjects{
		fakeTerminalCheckouts: fakeTerminalCheckouts{
			chats: map[string]*db.Chat{
				"chat-forced": {ID: "chat-forced", ProjectID: "proj-forced", OrgID: &force},
				"chat-other":  {ID: "chat-other", ProjectID: "proj-other", OrgID: &other},
				// A chat whose project records no org carries its own.
				"chat-forced-solo": {ID: "chat-forced-solo", ProjectID: "proj-no-org", OrgID: &force},
			},
			worktrees: map[string]*db.Worktree{"wt-1": {ID: "wt-1", ProjectID: "proj-forced"}},
		},
		orgs: map[string]string{"proj-forced": force, "proj-other": other},
	}}
	enforced := orgpolicy.WithResolver(context.Background(), orgpolicy.New(forcingOrg{controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE}))

	for name, ids := range map[string][3]string{
		"the chat's project's org":      {"", "", "chat-forced"},
		"the chat's own org":            {"", "", "chat-forced-solo"},
		"the project's org":             {"proj-forced", "", ""},
		"the workspace's project's org": {"", "wt-1", ""},
		// A chat of a relaxed org named beside a forced org's project or
		// workspace is another project's: it cannot shed the stamp.
		"a relaxed chat beside a forced project":   {"proj-forced", "", "chat-other"},
		"a relaxed chat beside a forced workspace": {"", "wt-1", "chat-other"},
	} {
		stamp := daemonpolicy.OrgStampFrom(map[string]interface{}{daemonpolicy.OrgStampContextKey: cfg.orgStamp(enforced, ids[0], ids[1], ids[2])})
		require.NotNil(t, stamp, name)
		assert.True(t, stamp.CredentialsForced(), name)
		assert.Equal(t, force, stamp.OrgID, name)
		assert.EqualValues(t, 6, stamp.Version, name)
	}

	// Another org, an audited section, a switch in audit, and no checkouts
	// stamp nothing: today's terminal.
	assert.Nil(t, cfg.orgStamp(enforced, "", "", "chat-other"))
	// Nor does a stale forced chat impose its org on another project.
	assert.Nil(t, cfg.orgStamp(enforced, "proj-other", "", "chat-forced"))
	assert.Nil(t, cfg.orgStamp(enforced, "proj-no-org", "", "chat-forced"))
	audited := orgpolicy.WithResolver(context.Background(), orgpolicy.New(forcingOrg{controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_AUDIT}))
	assert.Nil(t, cfg.orgStamp(audited, "", "", "chat-forced"))
	orgpolicy.SetProcessEnforcement(orgpolicy.PointMachines, orgpolicy.EnforcementAudit)
	assert.Nil(t, cfg.orgStamp(enforced, "", "", "chat-forced"))
	assert.Nil(t, terminalWSConfig{}.orgStamp(enforced, "proj-forced", "", ""))
}
