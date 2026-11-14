// Copyright (c) 2025 Reliant Labs
package catalog

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	llmdrivers "github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// The model pickers list what a chat of the org the request names may run
// on (control-plane docs/design/org-policies.md §4.1 point 3, §4.2): the org
// is the caller's live membership, never the client's word alone, and the
// org's policy narrows the list exactly as it narrows the run's calls.

const (
	pickerUser      = "picker-user"
	pickerOrg       = "org-picker"
	pickerOtherOrg  = "org-not-mine"
	pickerLocalName = "qwen3:latest"
)

// memberOf is an org resolver whose caller is a live member of orgs only, and
// records every org it was asked about.
type memberOf struct {
	orgs  map[string]bool
	asked []string
}

func (m *memberOf) Named(_ context.Context, _ string, requested string) (string, error) {
	m.asked = append(m.asked, requested)
	if requested == "" {
		return "", nil
	}
	if !m.orgs[requested] {
		return "", svcerr.WithReason(svcerr.PermissionDenied("you are not a member of organization "+requested), "not_org_member")
	}
	return requested, nil
}

// pickerPolicy is pickerOrg's policy: providers limited to allowed, enforced.
type pickerPolicy struct{ allowed []string }

func (p pickerPolicy) ResolveOrgPolicy(_ context.Context, orgID string) (accesstokenclient.OrgPolicy, error) {
	if orgID != pickerOrg {
		return accesstokenclient.OrgPolicy{}, nil
	}
	return accesstokenclient.OrgPolicy{Version: 3, Policy: &controlplanev1.OrgPolicy{
		Providers: &controlplanev1.ProvidersPolicy{Mode: controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, Allowed: p.allowed},
	}}, nil
}

// newPickerFixture is a catalog over a person with their own Anthropic key and
// one online local model, a member of pickerOrg alone. The returned context
// carries pickerOrg's policy, which admits only OpenAI.
func newPickerFixture(t *testing.T) (*Service, *memberOf, context.Context) {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	llmdrivers.InitializeAPIKeyProvider(repo)
	db.SeedProviderAccount(t, repo, pickerUser, "anthropic", "sk-ant-picker")

	prev := orgpolicy.EnforcementOf(orgpolicy.PointProviders)
	orgpolicy.SetProcessEnforcement(orgpolicy.PointProviders, orgpolicy.EnforcementEnforce)
	t.Cleanup(func() { orgpolicy.SetProcessEnforcement(orgpolicy.PointProviders, prev) })

	orgs := &memberOf{orgs: map[string]bool{pickerOrg: true}}
	svc := NewTestCatalog(t, WithCatalogDeps(Deps{
		Orgs:        orgs,
		LocalModels: localPickerDaemons{},
	}))
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, pickerUser)
	ctx = orgpolicy.WithResolver(ctx, orgpolicy.New(pickerPolicy{allowed: []string{"openai"}}))
	return svc, orgs, ctx
}

type localPickerDaemons struct{}

func (localPickerDaemons) LocalDaemons(context.Context, string) ([]local.DaemonInventory, error) {
	return []local.DaemonInventory{{
		DaemonID: "d1", Machine: "GPU box", Online: true,
		Inventory: &reliantv1.LocalModelInventory{Endpoints: []*reliantv1.LocalModelEndpoint{{
			Id: "ollama", Kind: "ollama",
			Models: []*reliantv1.LocalModelInfo{{Name: pickerLocalName, ContextWindow: 40960, SupportsTools: true, SupportsChat: true}},
		}}},
	}}, nil
}

func orgIDPtr(s string) *string { return &s }

func listModelDrivers(t *testing.T, svc *Service, ctx context.Context, orgID *string) map[string]bool {
	t.Helper()
	resp, err := svc.ListModels(ctx, connect.NewRequest(&reliantv1.ListModelsRequest{OrgId: orgID}))
	require.NoError(t, err)
	drivers := map[string]bool{}
	for _, m := range resp.Msg.GetModels() {
		drivers[m.GetDriverId()] = true
	}
	return drivers
}

func availableModelDrivers(t *testing.T, svc *Service, ctx context.Context, orgID *string) map[string]bool {
	t.Helper()
	resp, err := svc.ListAvailableModels(ctx, connect.NewRequest(&reliantv1.ListAvailableModelsRequest{OrgId: orgID}))
	require.NoError(t, err)
	drivers := map[string]bool{}
	for _, m := range resp.Msg.GetModels() {
		drivers[m.GetProvider()] = true
	}
	return drivers
}

func TestPickers_NamingNoOrgListTheCallersOwnAccounts(t *testing.T) {
	svc, orgs, ctx := newPickerFixture(t)

	assert.True(t, listModelDrivers(t, svc, ctx, nil)["anthropic"], "the person's own key serves a chat of no org")
	assert.True(t, listModelDrivers(t, svc, ctx, nil)["local"], "and so do their local models")
	assert.True(t, availableModelDrivers(t, svc, ctx, nil)["anthropic"])
	for _, asked := range orgs.asked {
		assert.Empty(t, asked, "a request naming no org names none to the resolver")
	}
}

func TestPickers_ListWhatTheNamedOrgsPolicyAllows(t *testing.T) {
	svc, orgs, ctx := newPickerFixture(t)

	got := listModelDrivers(t, svc, ctx, orgIDPtr(pickerOrg))
	assert.False(t, got["anthropic"], "the org's policy allows only OpenAI: the person's Anthropic key is not offered")
	assert.False(t, got["local"], "nor a local model, which the driver build would refuse at the call")
	assert.False(t, availableModelDrivers(t, svc, ctx, orgIDPtr(pickerOrg))["anthropic"])
	assert.Contains(t, orgs.asked, pickerOrg, "the named org is verified against the caller's membership")
}

func TestPickers_AnOrgWithNoPolicyKeepsThePersonsAccounts(t *testing.T) {
	svc, _, ctx := newPickerFixture(t)
	ctx = orgpolicy.WithResolver(ctx, orgpolicy.New(pickerPolicy{}))

	got := listModelDrivers(t, svc, ctx, orgIDPtr(pickerOrg))
	assert.True(t, got["anthropic"], "an org with no providers rule admits the person's own key")
	assert.True(t, got["local"])
}

func TestPickers_RefuseAnOrgTheCallerIsNotAMemberOf(t *testing.T) {
	svc, _, ctx := newPickerFixture(t)

	_, err := svc.ListModels(ctx, connect.NewRequest(&reliantv1.ListModelsRequest{OrgId: orgIDPtr(pickerOtherOrg)}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(svcerr.Wrap(err)))
	assert.True(t, strings.Contains(err.Error(), pickerOtherOrg))

	_, err = svc.ListAvailableModels(ctx, connect.NewRequest(&reliantv1.ListAvailableModelsRequest{OrgId: orgIDPtr(pickerOtherOrg)}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(svcerr.Wrap(err)))
}
