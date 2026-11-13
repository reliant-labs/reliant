// Copyright (c) 2025 Reliant Labs

package connauth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
)

const orgInstallation = "controlplane:github-app:4242"

// orgActingBroker is a GitHub broker that also speaks as an org's linked
// installation, the ids starting "controlplane:github-app:".
type orgActingBroker struct {
	actingBroker
	orgActed []string // "owner@org"
	err      error
}

type orgBotCred struct{ brokerCred }

func (orgBotCred) ConnectionID() string { return orgInstallation }

func (b *orgActingBroker) OwnsConnectionID(id string) bool {
	return strings.HasPrefix(id, "controlplane:github-app:")
}

func (b *orgActingBroker) OrgActingCredential(_ context.Context, owner, orgID string, _ httpaction.CredentialRequest) (httpaction.Credential, error) {
	b.orgActed = append(b.orgActed, owner+"@"+orgID)
	if b.err != nil {
		return nil, b.err
	}
	return orgBotCred{}, nil
}

// members is authz.Membership over a fixed set of "user@org".
type members map[string]bool

func (m members) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	return m[userID+"@"+orgID], true, nil
}

type membershipDown struct{}

func (membershipDown) IsMember(context.Context, string, string) (bool, bool, error) {
	return false, false, errors.New("db down")
}

// newOrgRun is newRun for a run of orgID ("" for none).
func (e *env) newOrgRun(userID, orgID string) string {
	e.t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	chatID := uuid.NewString()
	chat := &db.Chat{ID: chatID, Title: "t", ProjectID: "test-project", UserID: userID, CreatedAt: now, UpdatedAt: now, LastActive: now}
	if orgID != "" {
		chat.OrgID = &orgID
	}
	require.NoError(e.t, e.repo.CreateChat(ctx, chat))
	_, err := e.repo.CreateThread(ctx, &db.Thread{ID: chatID, ChatID: chatID, CreatedAt: now})
	require.NoError(e.t, err)
	owner := userID
	wf := &db.Workflow{ID: chatID, ChatID: chatID, WorkflowName: "builtin://agent", Thread: chatID, Status: db.Active(), CreatedAt: now, OwnerUserID: &owner}
	if orgID != "" {
		wf.OrgID = &orgID
	}
	require.NoError(e.t, e.repo.CreateWorkflow(ctx, wf))
	return chatID
}

func orgWrite(t *testing.T, runID, connectionID string) httpaction.CredentialRequest {
	t.Helper()
	return httpaction.CredentialRequest{
		RunID: runID, IntegrationID: "github", Connection: specs(t, "github")["github"],
		ConnectionID: connectionID, ServerPlaced: true, Mutates: true, ActingIdentity: httpaction.IdentityApp,
		ActionID: "issue.comment", Params: map[string]any{"owner": "acme", "repo": "api"},
	}
}

// A call that names the org's installation, in a run of that org by a live
// member, speaks as the org's installation: the broker is asked with the run's
// owner and org, and nothing else is consulted.
func TestOrgInstallationIsUsedInARunOfItsOrgByAMember(t *testing.T) {
	e := newEnv(t)
	e.githubPAT("alice") // the owner's own connection must not win
	broker := &orgActingBroker{}
	ctx := authz.WithMembership(context.Background(), members{"alice@org-1": true})

	cred, err := e.withBroker(broker).Credential(ctx, orgWrite(t, e.newOrgRun("alice", "org-1"), orgInstallation))
	require.NoError(t, err)
	assert.Equal(t, orgInstallation, cred.ConnectionID())
	assert.Equal(t, []string{"alice@org-1"}, broker.orgActed)
	assert.Empty(t, broker.acted, "the personal bot is not asked")
}

// Every refusal is final: the write is never made as the owner or the
// personal bot instead.
func TestOrgInstallationRefusals(t *testing.T) {
	cases := map[string]struct {
		org        string
		membership authz.Membership
		brokerErr  error
		wantCode   string
		wantText   string
		asked      bool
	}{
		"run of no org": {
			org: "", membership: members{"alice@org-1": true},
			wantCode: httpaction.CodeFailedPrecondition, wantText: "belongs to no organization",
		},
		"owner not a member": {
			org: "org-1", membership: members{"alice@org-2": true},
			wantCode: httpaction.CodeFailedPrecondition, wantText: "not a member of the organization",
		},
		"membership unreadable": {
			org: "org-1", membership: membershipDown{},
			wantCode: httpaction.CodeUnavailable, wantText: "try again",
		},
		"broker refuses (installation not linked)": {
			org: "org-1", membership: members{"alice@org-1": true},
			brokerErr: &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "not linked to this organization"},
			wantCode:  httpaction.CodeFailedPrecondition, wantText: "not linked", asked: true,
		},
		"broker fails untyped": {
			org: "org-1", membership: members{"alice@org-1": true},
			brokerErr: errors.New("boom"),
			wantCode:  httpaction.CodeInternal, wantText: "delegated authority failed", asked: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.githubPAT("alice")
			broker := &orgActingBroker{err: tc.brokerErr}
			ctx := authz.WithMembership(context.Background(), tc.membership)
			cred, err := e.withBroker(broker).Credential(ctx, orgWrite(t, e.newOrgRun("alice", tc.org), orgInstallation))
			require.Error(t, err)
			assert.Nil(t, cred)
			var ce *httpaction.CredentialError
			require.True(t, errors.As(err, &ce), "got %v", err)
			assert.Equal(t, tc.wantCode, ce.Code)
			assert.Contains(t, ce.Message, tc.wantText)
			assert.Equal(t, tc.asked, len(broker.orgActed) == 1)
			assert.Empty(t, broker.acted, "never the personal bot instead")
		})
	}
}

// A call that names no org installation never reaches the org path, so a
// broker that can speak for orgs changes nothing for it (D10: no fallback).
func TestCallsThatNameNoOrgInstallationAreUnchanged(t *testing.T) {
	e := newEnv(t)
	broker := &orgActingBroker{}
	ctx := authz.WithMembership(context.Background(), members{"alice@org-1": true})

	cred, err := e.withBroker(broker).Credential(ctx, orgWrite(t, e.newOrgRun("alice", "org-1"), ""))
	require.NoError(t, err)
	assert.Equal(t, "bot", cred.ConnectionID(), "the personal bot, as before")
	assert.Empty(t, broker.orgActed)
}
