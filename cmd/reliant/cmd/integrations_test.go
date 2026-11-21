// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

// fakeConnections is the ConnectionService reads behind `integrations list`
// and `trigger get`'s Connection line.
type fakeConnections struct {
	reliantv1connect.UnimplementedConnectionServiceHandler
	mu       sync.Mutex
	conns    []*reliantv1.Connection
	listReqs []*reliantv1.ListConnectionsRequest
	bearer   string
}

func (f *fakeConnections) ListConnections(_ context.Context, r *connect.Request[reliantv1.ListConnectionsRequest]) (*connect.Response[reliantv1.ListConnectionsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listReqs = append(f.listReqs, r.Msg)
	f.bearer = r.Header().Get("Authorization")
	var out []*reliantv1.Connection
	for _, c := range f.conns {
		if r.Msg.IntegrationId != nil && c.GetIntegrationId() != r.Msg.GetIntegrationId() {
			continue
		}
		if c.GetOwnerKind() == reliantv1.ConnectionOwnerKind_CONNECTION_OWNER_KIND_ORG && c.GetOrgId() != r.Msg.GetOrgId() {
			continue
		}
		out = append(out, c)
	}
	return connect.NewResponse(&reliantv1.ListConnectionsResponse{Connections: out}), nil
}

func (f *fakeConnections) GetConnection(_ context.Context, r *connect.Request[reliantv1.GetConnectionRequest]) (*connect.Response[reliantv1.GetConnectionResponse], error) {
	for _, c := range f.conns {
		if c.GetId() != r.Msg.GetId() {
			continue
		}
		// An org's connection is read only as the org's.
		if c.GetOwnerKind() == reliantv1.ConnectionOwnerKind_CONNECTION_OWNER_KIND_ORG && c.GetOrgId() != r.Msg.GetOwnerOrgId() {
			break
		}
		return connect.NewResponse(&reliantv1.GetConnectionResponse{Connection: c}), nil
	}
	return nil, connect.NewError(connect.CodeNotFound, assertNotFound{})
}

// oneTriggerService serves GetTrigger/ListTriggers for one trigger.
type oneTriggerService struct {
	reliantv1connect.UnimplementedTriggerServiceHandler
	trigger *reliantv1.Trigger
}

func (f *oneTriggerService) GetTrigger(_ context.Context, r *connect.Request[reliantv1.GetTriggerRequest]) (*connect.Response[reliantv1.GetTriggerResponse], error) {
	if r.Msg.GetId() != f.trigger.GetId() {
		return nil, connect.NewError(connect.CodeNotFound, assertNotFound{})
	}
	return connect.NewResponse(&reliantv1.GetTriggerResponse{Trigger: f.trigger}), nil
}

func (f *oneTriggerService) ListTriggers(context.Context, *connect.Request[reliantv1.ListTriggersRequest]) (*connect.Response[reliantv1.ListTriggersResponse], error) {
	return connect.NewResponse(&reliantv1.ListTriggersResponse{Triggers: []*reliantv1.Trigger{f.trigger}}), nil
}

func newConnectionsServer(t *testing.T, trigger *reliantv1.Trigger) *fakeConnections {
	t.Helper()
	isolateCLI(t)
	fake := &fakeConnections{conns: []*reliantv1.Connection{
		{Id: "conn-gh", IntegrationId: "github", Name: "work", AccountLabel: "octocat", IsDefault: true,
			Status: reliantv1.ConnectionStatus_CONNECTION_STATUS_ACTIVE, OwnerKind: reliantv1.ConnectionOwnerKind_CONNECTION_OWNER_KIND_USER},
		{Id: "conn-slack", IntegrationId: "slack", Name: "acme-slack", AccountLabel: "acme",
			Status: reliantv1.ConnectionStatus_CONNECTION_STATUS_NEEDS_REAUTH, StatusReason: "invalid_grant"},
		{Id: "conn-org", IntegrationId: "slack", Name: "acme bot", AccountLabel: "acme-bot",
			Status: reliantv1.ConnectionStatus_CONNECTION_STATUS_ACTIVE, OwnerKind: reliantv1.ConnectionOwnerKind_CONNECTION_OWNER_KIND_ORG, OrgId: "org-1"},
	}}
	mux := http.NewServeMux()
	p, h := reliantv1connect.NewConnectionServiceHandler(fake)
	mux.Handle(p, h)
	if trigger != nil {
		p, h = reliantv1connect.NewTriggerServiceHandler(&oneTriggerService{trigger: trigger})
		mux.Handle(p, h)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	loginFor(t, srv.URL, fakeLoginToken)
	t.Setenv(envServerURL, srv.URL)
	return fake
}

// `integrations list` is a new top-level group: it must load the CLI's
// connection config (cli_config.proto subtrees, bindCLIConfig) and dial the
// server as the login.
func TestIntegrationsListDialsTheServer(t *testing.T) {
	fake := newConnectionsServer(t, nil)

	stdout, stderr, err := executeRootSplit(t, "integrations", "list")
	require.NoError(t, err, stderr)
	assert.Equal(t, "Bearer "+fakeLoginToken, fake.bearer)
	assert.Contains(t, stdout, "INTEGRATION")
	assert.Contains(t, stdout, "github")
	assert.Contains(t, stdout, "octocat")
	assert.Contains(t, stdout, "conn-gh")
	assert.Contains(t, stdout, "needs_reauth (invalid_grant)")
	assert.NotContains(t, stdout, "conn-org", "an org's connections only with --org")

	stdout, stderr, err = executeRootSplit(t, "integrations", "list", "--integration", "slack", "--org", "org-1", "--json")
	require.NoError(t, err, stderr)
	last := fake.listReqs[len(fake.listReqs)-1]
	assert.Equal(t, "slack", last.GetIntegrationId())
	assert.Equal(t, "org-1", last.GetOrgId())
	var rows []connectionRow
	require.NoError(t, json.Unmarshal([]byte(stdout), &rows), stdout)
	require.Len(t, rows, 2)
	assert.Equal(t, "conn-slack", rows[0].ID)
	assert.Equal(t, "you", rows[0].Owner)
	assert.Equal(t, connectionRow{ID: "conn-org", Integration: "slack", Name: "acme bot", Account: "acme-bot", Status: "active", Owner: "org", OrgID: "org-1"}, rows[1])
}

func TestIntegrationsListSaysWhereToConnect(t *testing.T) {
	fake := newConnectionsServer(t, nil)
	fake.conns = nil
	stdout, stderr, err := executeRootSplit(t, "integrations", "list")
	require.NoError(t, err, stderr)
	assert.Contains(t, stdout, "No integration connections. Connect an account in the app: Settings → Integrations.")
}

// `trigger get` names the connection an integration activation acts
// through, read from the server: the account, its name and id, and an org
// connection read as the org's.
func TestTriggerGetNamesTheConnection(t *testing.T) {
	connID := "conn-gh"
	trig := &reliantv1.Trigger{Id: "trg-1", Name: "pr-review · opened", ProjectId: "p", Enabled: true, ConnectionId: &connID,
		ActingIdentity: reliantv1.ActingIdentity_ACTING_IDENTITY_APP,
		Source:         &reliantv1.Trigger_Integration{Integration: &reliantv1.IntegrationSource{Integration: "github"}}}
	newConnectionsServer(t, trig)

	stdout, stderr, err := executeRootSplit(t, "trigger", "get", "trg-1")
	require.NoError(t, err, stderr)
	assert.Regexp(t, `Connection: +github · octocat \("work"\) conn-gh\n`, stdout)
	assert.Regexp(t, `Acting as: +app \(the Reliant GitHub App's bot\)`, stdout)

	orgConn, org := "conn-org", "org-1"
	trig.ConnectionId, trig.OrgId = &orgConn, &org
	stdout, stderr, err = executeRootSplit(t, "trigger", "get", "trg-1")
	require.NoError(t, err, stderr)
	assert.Contains(t, stdout, `slack · acme-bot ("acme bot") conn-org, the organization's`)

	gone := "conn-deleted"
	trig.ConnectionId = &gone
	stdout, stderr, err = executeRootSplit(t, "trigger", "get", "trg-1")
	require.NoError(t, err, stderr)
	assert.Regexp(t, `Connection: +conn-deleted\n`, stdout, "a connection that does not read degrades to its id")
}

func TestTriggerConnectionLabelWithoutARow(t *testing.T) {
	gh := &reliantv1.Trigger_Integration{Integration: &reliantv1.IntegrationSource{Integration: "github"}}
	assert.Empty(t, triggerConnectionLabel(&reliantv1.Trigger{}, nil), "a schedule has no connection")
	assert.Equal(t, "none bound: github events reach it through the owner's own access",
		triggerConnectionLabel(&reliantv1.Trigger{Source: gh}, nil))
	app := "controlplane:github-app:42"
	assert.Equal(t, "the organization's GitHub App installation 42 (controlplane:github-app:42)",
		triggerConnectionLabel(&reliantv1.Trigger{Source: gh, ConnectionId: &app}, nil))
}

func TestPausedReasonLabel(t *testing.T) {
	assert.Empty(t, pausedReasonLabel(&reliantv1.Trigger{Enabled: true, PausedReason: "no_org_account"}))
	assert.Empty(t, pausedReasonLabel(&reliantv1.Trigger{}), "a person's pause has no reason")
	assert.Contains(t, pausedReasonLabel(&reliantv1.Trigger{PausedReason: "no_org_account"}), "two firings in a row")
	assert.Equal(t, "org_move", pausedReasonLabel(&reliantv1.Trigger{PausedReason: "org_move"}), "an unknown reason is shown as sent")
}
