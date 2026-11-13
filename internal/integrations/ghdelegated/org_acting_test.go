// Copyright (c) 2025 Reliant Labs

package ghdelegated

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
)

func TestOrgAppConnectionID_RoundTripsAndRejectsEverythingElse(t *testing.T) {
	id := OrgAppConnectionID(4242)
	assert.Equal(t, "controlplane:github-app:4242", id)
	n, ok := ParseOrgAppConnectionID(id)
	require.True(t, ok)
	assert.Equal(t, int64(4242), n)
	assert.True(t, IsOrgAppConnectionID(id))

	for _, bad := range []string{
		BotConnectionID, ConnectionID, "controlplane:github-app:", "controlplane:github-app:0",
		"controlplane:github-app:-1", "controlplane:github-app:12x", "controlplane:github-app: 12",
		"controlplane:github-app:99999999999999999999", "conn-1", "",
	} {
		_, ok := ParseOrgAppConnectionID(bad)
		assert.False(t, ok, "%q is not an org installation", bad)
		assert.False(t, newBroker(nil, APIHost, nil).OwnsConnectionID(bad), "%q", bad)
	}
}

func orgBotReq(connID string) httpaction.CredentialRequest {
	return httpaction.CredentialRequest{
		ConnectionID: connID, ActingIdentity: httpaction.IdentityApp, Automation: "pr-review",
		ActionID: "issue.comment", Mutates: true, Params: repoParams,
	}
}

func orgBroker(inst *fakeInstallations) *broker {
	b := newBroker(nil, APIHost, nil)
	b.installations = inst
	return b
}

// The org's bot is minted through the org's installation: the mint carries the
// org, for the owner, scoped to the one repository, and the credential reports
// the installation the step named.
func TestOrgActingCredential_MintsThroughTheOrgAndReportsTheInstallation(t *testing.T) {
	inst := &fakeInstallations{installationID: 4242}
	cred, err := orgBroker(inst).OrgActingCredential(context.Background(), "idp|alice", "org-1", orgBotReq(OrgAppConnectionID(4242)))
	require.NoError(t, err)

	require.Len(t, inst.asked, 1)
	got := inst.asked[0]
	assert.Equal(t, "org-1", got.OrgID, "control-plane checks the installation is the org's")
	assert.Equal(t, "idp|alice", got.ExternalUserID, "the owner's own push access still gates it")
	assert.Equal(t, "acme", got.Owner)
	assert.Equal(t, "widgets", got.Repo)
	assert.Equal(t, OrgAppConnectionID(4242), cred.ConnectionID())
	assert.Equal(t, "Bearer ghs_bot_token_5d8e", applied(t, cred, "https://api.github.com/repos/acme/widgets/issues/7/comments"))
}

// The personal bot path never sends an org.
func TestActingCredential_SendsNoOrg(t *testing.T) {
	inst := &fakeInstallations{}
	req := orgBotReq("")
	cred, err := orgBroker(inst).ActingCredential(context.Background(), "idp|alice", req)
	require.NoError(t, err)
	require.Len(t, inst.asked, 1)
	assert.Empty(t, inst.asked[0].OrgID)
	assert.Equal(t, BotConnectionID, cred.ConnectionID())
}

func TestOrgActingCredential_Refusals(t *testing.T) {
	cases := map[string]struct {
		orgID    string
		connID   string
		inst     *fakeInstallations
		wantCode string
		wantText string
		minted   bool
	}{
		"no org on the run": {
			orgID: "", connID: OrgAppConnectionID(4242), inst: &fakeInstallations{installationID: 4242},
			wantCode: httpaction.CodeFailedPrecondition, wantText: "only usable in a run of that organization",
		},
		"not an org installation": {
			orgID: "org-1", connID: BotConnectionID, inst: &fakeInstallations{installationID: 4242},
			wantCode: httpaction.CodeInvalidArgument, wantText: "does not name an organization's GitHub App installation",
		},
		"installation not linked to the org": {
			orgID: "org-1", connID: OrgAppConnectionID(4242), inst: &fakeInstallations{err: gitcredentialclient.ErrAppNotLinked},
			wantCode: httpaction.CodeFailedPrecondition, wantText: "not linked to this organization", minted: true,
		},
		"owner not a member": {
			orgID: "org-1", connID: OrgAppConnectionID(4242), inst: &fakeInstallations{err: gitcredentialclient.ErrNotOrgMember},
			wantCode: httpaction.CodeFailedPrecondition, wantText: "not a member of the organization", minted: true,
		},
		"owner cannot push": {
			orgID: "org-1", connID: OrgAppConnectionID(4242), inst: &fakeInstallations{err: gitcredentialclient.ErrOwnerCannotPush},
			wantCode: httpaction.CodeFailedPrecondition, wantText: "cannot push to acme/widgets", minted: true,
		},
		// The org linked another installation that covers the repository: the
		// step named 4242, so a token through 777 is not what it asked for.
		"another installation covers the repository": {
			orgID: "org-1", connID: OrgAppConnectionID(4242), inst: &fakeInstallations{installationID: 777},
			wantCode: httpaction.CodeFailedPrecondition, wantText: "not covered by the GitHub App installation this step names", minted: true,
		},
		// A control plane from before org installations neither checks the
		// link nor reports the installation: fail closed.
		"control plane does not report the installation": {
			orgID: "org-1", connID: OrgAppConnectionID(4242), inst: &fakeInstallations{},
			wantCode: httpaction.CodeFailedPrecondition, wantText: "not covered by the GitHub App installation this step names", minted: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cred, err := orgBroker(tc.inst).OrgActingCredential(context.Background(), "idp|alice", tc.orgID, orgBotReq(tc.connID))
			require.Error(t, err)
			assert.Nil(t, cred)
			var ce *httpaction.CredentialError
			require.True(t, errors.As(err, &ce), "got %v", err)
			assert.Equal(t, tc.wantCode, ce.Code)
			assert.Contains(t, ce.Message, tc.wantText)
			assert.Equal(t, tc.minted, len(tc.inst.asked) == 1, "asked control-plane: %v", tc.inst.asked)
		})
	}
}
