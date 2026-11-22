package tools

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// testNow is the fixed clock the cooldown tests read.
var testNow = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

type fakeProposer struct {
	admin       bool
	adminErr    error
	states      []ConnectionProposalState
	connections map[core.ConnectionOwner]*core.Connection
}

func (f *fakeProposer) IsOrgAdmin(context.Context, string, string) (bool, error) {
	return f.admin, f.adminErr
}
func (f *fakeProposer) ChatProposals(context.Context, string) ([]ConnectionProposalState, error) {
	return f.states, nil
}
func (f *fakeProposer) Connection(_ context.Context, owner core.ConnectionOwner, id string) (*core.Connection, error) {
	if c := f.connections[owner]; c != nil && c.ID == id {
		return c, nil
	}
	return nil, errors.New("not found")
}

func newTestProposeTool(f *fakeProposer, orgID string) Tool {
	chat := &db.Chat{ID: "chat", UserID: "owner"}
	if orgID != "" {
		chat.OrgID = &orgID
	}
	return NewProposeConnectionTool(triggerCallerStore{chat: chat}, f)
}

func prepareProposal(t *testing.T, tool Tool, input string) (ActivationPreview, bool, error) {
	t.Helper()
	call := ToolCall{ID: "call", Name: ToolProposeConnection, Input: input}
	return tool.(AutomationApprovalTool).PrepareAutomation(machineToolCtx(automationpolicy.Execution{}), call, false)
}

// The call raises an approval card: a preview that always needs one, laid
// out as a card, carrying the proposal the card's dialog is opened for.
func TestProposeConnectionRaisesACard(t *testing.T) {
	tool := newTestProposeTool(&fakeProposer{}, "org-1")
	preview, ask, err := prepareProposal(t, tool, `{"provider":"slack","reason":"Post the nightly\nreport to #ops."}`)
	require.NoError(t, err)
	require.True(t, ask, "a connection proposal always needs its card")
	require.True(t, preview.CardRequired)
	require.Equal(t, PreviewKindConnectionProposal, preview.Kind, "the runtime settles the card by its kind")
	require.Equal(t, &core.ConnectionProposal{IntegrationID: "slack", IntegrationName: "Slack", OrgID: "org-1", Reason: "Post the nightly report to #ops."}, preview.Connection,
		"the reason is flattened to one line, and an unscoped proposal lets an org admin choose the org")
	require.Equal(t, connectionProposalHash(*preview.Connection), preview.Hash)
	require.True(t, strings.HasPrefix(preview.Title, "The agent asks you to connect Slack.\nReason: Post the nightly report to #ops.\n"), preview.Title)
	require.Contains(t, preview.Title, "\n\nTechnical details\nOperation: propose_connection\nIntegration: slack")

	_, _, err = prepareProposal(t, tool, `{"provider":"dropbox","reason":"x"}`)
	require.ErrorContains(t, err, "not one of github, slack, gmail, twilio, http")
	_, _, err = prepareProposal(t, tool, `{"provider":"slack","reason":"  "}`)
	require.ErrorContains(t, err, "reason is required")
	_, _, err = prepareProposal(t, tool, `{"provider":"slack","reason":"x","token":"xoxb"}`)
	require.Error(t, err, "an unknown field is refused, not ignored")
}

// The server refuses an org-wide proposal from a person who is not an owner
// or admin of the chat's organization, and one in a chat with no org.
func TestProposeConnectionRefusesAnOrgConnectionToANonAdmin(t *testing.T) {
	_, ask, err := prepareProposal(t, newTestProposeTool(&fakeProposer{admin: false}, "org-1"), `{"provider":"gmail","scope":"org","reason":"Read the shared inbox."}`)
	require.False(t, ask)
	require.ErrorContains(t, err, "Only an owner or admin of the organization can set up an org-wide connection")
	require.Contains(t, ActivationRefusalReason(err), "scope personal", "the refusal says what to do instead")

	_, _, err = prepareProposal(t, newTestProposeTool(&fakeProposer{adminErr: errors.New("control plane down")}, "org-1"), `{"provider":"gmail","scope":"org","reason":"x"}`)
	require.ErrorContains(t, err, "could not be checked")

	_, _, err = prepareProposal(t, newTestProposeTool(&fakeProposer{admin: true}, ""), `{"provider":"gmail","scope":"org","reason":"x"}`)
	require.ErrorContains(t, err, "not in an organization")

	preview, ask, err := prepareProposal(t, newTestProposeTool(&fakeProposer{admin: true}, "org-1"), `{"provider":"gmail","scope":"org","reason":"x"}`)
	require.NoError(t, err)
	require.True(t, ask)
	require.Equal(t, core.ConnectionProposalOrg, preview.Connection.Scope)
	require.Contains(t, preview.Title, "Owner: Your organization (its owners and admins only)")
}

// An identical proposal already waiting in the chat is refused, and so is
// one the person declined moments ago; a different one, or an old decline,
// is not.
func TestProposeConnectionDedupesIdenticalProposals(t *testing.T) {
	prev := proposalNow
	proposalNow = func() time.Time { return testNow }
	t.Cleanup(func() { proposalNow = prev })
	slack := core.ConnectionProposal{IntegrationID: "slack", IntegrationName: "Slack", OrgID: "org-1", Reason: "an earlier reason"}
	for _, tc := range []struct {
		name  string
		state ConnectionProposalState
		want  string
	}{
		{"pending", ConnectionProposalState{Proposal: slack, Pending: true}, "status: already pending"},
		{"declined just now", ConnectionProposalState{Proposal: slack, Declined: true, ResolvedAt: testNow.Add(-time.Minute)}, "status: declined"},
		{"declined long ago", ConnectionProposalState{Proposal: slack, Declined: true, ResolvedAt: testNow.Add(-time.Hour)}, ""},
		{"expired", ConnectionProposalState{Proposal: slack, ResolvedAt: testNow}, ""},
		{"another integration", ConnectionProposalState{Proposal: core.ConnectionProposal{IntegrationID: "gmail", OrgID: "org-1"}, Pending: true}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ask, err := prepareProposal(t, newTestProposeTool(&fakeProposer{states: []ConnectionProposalState{tc.state}}, "org-1"), `{"provider":"slack","reason":"a new reason"}`)
			if tc.want == "" {
				require.NoError(t, err)
				require.True(t, ask)
				return
			}
			require.False(t, ask)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// Approved and connected: the result is the status, the provider and the
// connection's id and name. Without the approval for exactly this proposal,
// or without the connection, nothing is reported connected.
func TestProposeConnectionResultIsStatusOnly(t *testing.T) {
	secret := func(s string) *string { return &s }
	conn := &core.Connection{
		ID: "conn-1", OwnerKind: core.ConnectionOwnerUser, UserID: "owner", IntegrationID: "slack", Name: "Ops Slack",
		AccountLabel: secret("SECRET-account-label"), ExternalAccountID: secret("SECRET-external-id"), AuthHeader: secret("SECRET-header"),
		Scopes: []string{"SECRET-scope"}, Params: map[string]string{"api_base": "SECRET-param"},
	}
	f := &fakeProposer{connections: map[core.ConnectionOwner]*core.Connection{core.UserConnectionOwner("owner"): conn}}
	tool := newTestProposeTool(f, "")
	preview, _, err := prepareProposal(t, tool, `{"provider":"slack","reason":"Post the report."}`)
	require.NoError(t, err)
	call := ToolCall{ID: "call", Name: ToolProposeConnection, Input: `{"provider":"slack","reason":"Post the report."}`}

	resp, err := tool.Run(machineToolCtx(automationpolicy.Execution{Connections: map[string]string{"call": "conn-1"}}), call)
	require.NoError(t, err)
	require.True(t, resp.IsError, "a connection id without the approval reports nothing")
	require.Contains(t, resp.Content, "status: not completed")

	resp, err = tool.Run(machineToolCtx(automationpolicy.Execution{Approvals: map[string]string{"call": preview.Hash}}), call)
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "status: not completed")

	resp, err = tool.Run(machineToolCtx(automationpolicy.Execution{Approvals: map[string]string{"call": preview.Hash}, Connections: map[string]string{"call": "someone-elses"}}), call)
	require.NoError(t, err)
	require.True(t, resp.IsError, "a connection the person does not own is not reported")

	resp, err = tool.Run(machineToolCtx(automationpolicy.Execution{Approvals: map[string]string{"call": preview.Hash}, Connections: map[string]string{"call": "conn-1"}}), call)
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, "status: connected\nprovider: slack\nscope: personal\nconnection_id: conn-1\nconnection_name: Ops Slack", resp.Content)
	require.JSONEq(t, `{"status":"connected","provider":"slack","scope":"personal","connection_id":"conn-1","connection_name":"Ops Slack"}`, resp.Metadata)
	require.NotContains(t, resp.Content+resp.Metadata, "SECRET")
}

// No credential can reach the model: the result type has exactly the
// status and identifier fields, and the card's proposal has none either.
// Adding a field to either fails here, so it is a decision, not an accident.
func TestProposeConnectionCarriesNoSecretField(t *testing.T) {
	fieldsOf := func(v any) []string {
		var names []string
		typ := reflect.TypeOf(v)
		for i := 0; i < typ.NumField(); i++ {
			names = append(names, typ.Field(i).Name)
		}
		sort.Strings(names)
		return names
	}
	require.Equal(t, []string{"ConnectionID", "ConnectionName", "Provider", "Scope", "Status"}, fieldsOf(ConnectionProposalResult{}))
	require.Equal(t, []string{"IntegrationID", "IntegrationName", "OrgID", "Reason", "Scope"}, fieldsOf(core.ConnectionProposal{}))
	for _, name := range append(fieldsOf(ConnectionProposalResult{}), fieldsOf(ProposeConnectionParams{})...) {
		lower := strings.ToLower(name)
		for _, secret := range []string{"token", "secret", "key", "password", "code", "credential"} {
			require.NotContains(t, lower, secret, "%s must not carry a credential", name)
		}
	}
}

// Nobody attends an unattended run, so it can neither raise the card nor run.
func TestProposeConnectionRefusedUnattended(t *testing.T) {
	tool := newTestProposeTool(&fakeProposer{}, "")
	call := ToolCall{ID: "call", Name: ToolProposeConnection, Input: `{"provider":"slack","reason":"x"}`}
	_, ask, err := tool.(AutomationApprovalTool).PrepareAutomation(machineToolCtx(automationpolicy.Execution{}), call, true)
	require.False(t, ask)
	require.ErrorContains(t, err, "Nobody is attending")
	resp, err := tool.Run(machineToolCtx(automationpolicy.Execution{Unattended: true}), call)
	require.NoError(t, err)
	require.True(t, resp.IsError)
}
