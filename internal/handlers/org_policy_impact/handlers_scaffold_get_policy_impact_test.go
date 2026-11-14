package org_policy_impact_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	org_policy_impact "github.com/reliant-labs/reliant/internal/handlers/org_policy_impact"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
	"github.com/reliant-labs/reliant/internal/orgs"
	activityhandlers "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
)

const org = "org-acme"

// roles is control-plane's answer: user -> role in org.
type roles map[string]string

func (r roles) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	return orgID == org && r[userID] != "", true, nil
}

func (r roles) RoleFresh(_ context.Context, userID, orgID string) (string, bool, error) {
	if orgID != org {
		return "", true, nil
	}
	return r[userID], true, nil
}

// calls is the org's chats' latest calls and the chats themselves (with
// their launches and who took a turn: what the attendance rule reads), and
// the window and limit it was asked for.
type calls struct {
	mu    sync.Mutex
	rows  []db.OrgChatCall
	chats map[string]*db.Chat
	// turns is the chats a person sent a message into; parents is each
	// agent-started chat's parent.
	turns   map[string]bool
	parents map[string]string
	since   time.Time
	limit   int
}

func (c *calls) LatestOrgChatCalls(_ context.Context, orgID string, since time.Time, limit int) ([]db.OrgChatCall, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.since, c.limit = since, limit
	if orgID != org {
		return nil, nil
	}
	if len(c.rows) > limit {
		return c.rows[:limit], nil
	}
	return c.rows, nil
}

func (c *calls) GetChat(_ context.Context, id string) (*db.Chat, error) {
	if chat, ok := c.chats[id]; ok {
		return chat, nil
	}
	return &db.Chat{ID: id, LaunchKind: string(core.TriggerEventKindChatStart)}, nil
}

func (c *calls) GetTriggerEventByChat(_ context.Context, kind core.TriggerEventKind, chatID string) (*core.TriggerEvent, error) {
	parent, ok := c.parents[chatID]
	if !ok || kind != core.TriggerEventKindAgentStartRun {
		return nil, core.ErrTriggerEventNotFound
	}
	return &core.TriggerEvent{Kind: kind, ChatID: &chatID, Payload: map[string]any{"parent_chat_id": parent}}, nil
}

func (c *calls) ChatPersonTurnAt(_ context.Context, chatID string) (*time.Time, error) {
	if c.turns[chatID] {
		at := now.Add(-time.Hour)
		return &at, nil
	}
	return nil, nil
}

func (c *calls) GetTrigger(_ context.Context, id string) (*core.Trigger, error) {
	return &core.Trigger{ID: id}, nil
}

var now = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

func newService(t *testing.T, store *calls) *org_policy_impact.Service {
	t.Helper()
	return org_policy_impact.NewTestOrgPolicyImpact(t, org_policy_impact.WithOrgPolicyImpactDeps(org_policy_impact.Deps{
		Store: store, Now: func() time.Time { return now },
		// The activities' own attendance rule, the one each call is scoped with.
		Attendance: activityhandlers.Attendance{Store: store},
	}))
}

func as(user string, c auth.Credential) context.Context {
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, user)
	return auth.WithCredential(authz.WithMembership(ctx, roles{"ada": orgs.RoleAdmin, "olga": orgs.RoleOwner, "mia": orgs.RoleMember}), c)
}

func policyJSON(t *testing.T, doc *controlplanev1.OrgPolicy) string {
	t.Helper()
	b, err := protojson.Marshal(doc)
	require.NoError(t, err)
	return string(b)
}

// scheduled is a chat a stored trigger launched; triggerID nil is one whose
// trigger was deleted since (the launch keeps its kind).
func scheduled(id string, triggerID *string) *db.Chat {
	return &db.Chat{ID: id, LaunchKind: string(core.TriggerEventKindSchedule), TriggerID: triggerID}
}

func agentStarted(id string) *db.Chat {
	return &db.Chat{ID: id, LaunchKind: string(core.TriggerEventKindAgentStartRun)}
}

// A proposed policy's impact is counted, by reason, on each chat's latest
// call, the way the resolver judges the next one: the provider and the kind
// of account (who pays), the automations section for a chat nobody is behind
// (by the attendance rule its calls are scoped with), and the models section
// for the managed driver only.
func TestGetPolicyImpact_CountsTheChatsTheProposedPolicyWouldRefuse(t *testing.T) {
	trigger := "trg-1"
	store := &calls{rows: []db.OrgChatCall{
		{ChatID: "c1", Provider: "claude", Model: "claude-4.6-sonnet@claude", Funding: "personal"},
		{ChatID: "c2", Provider: "anthropic", Model: "claude-4.6-sonnet", Funding: "org_key"},
		{ChatID: "c3", Provider: "reliant", Model: "claude-4.6-sonnet@reliant", Funding: "org_wallet"},
		{ChatID: "c4", Provider: "reliant", Model: "claude-4.6-opus", Funding: "org_wallet"},
		{ChatID: "c5", Provider: "claude", Model: "claude-4.6-sonnet", Funding: "org_seat"},
		{ChatID: "c6", Provider: "anthropic", Model: "claude-4.6-sonnet", Funding: "org_key"},
	}, chats: map[string]*db.Chat{"c5": scheduled("c5", &trigger), "c6": scheduled("c6", &trigger)}}
	svc := newService(t, store)
	enforce := controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE
	doc := &controlplanev1.OrgPolicy{
		Providers: &controlplanev1.ProvidersPolicy{Mode: enforce, AllowedSources: []controlplanev1.ProviderSource{
			controlplanev1.ProviderSource_PROVIDER_SOURCE_ORG_ACCOUNTS, controlplanev1.ProviderSource_PROVIDER_SOURCE_ORG_SEATS,
			controlplanev1.ProviderSource_PROVIDER_SOURCE_RELIANT,
		}},
		Automations: &controlplanev1.AutomationsPolicy{Mode: enforce, AllowedSources: []controlplanev1.ProviderSource{
			controlplanev1.ProviderSource_PROVIDER_SOURCE_ORG_ACCOUNTS, controlplanev1.ProviderSource_PROVIDER_SOURCE_RELIANT,
		}},
		// Audit or enforce, the preview counts what the rule refuses.
		Models: &controlplanev1.ModelsPolicy{Mode: controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_AUDIT, Allowed: []string{"claude-opus-4-6"}},
	}

	resp, err := svc.GetPolicyImpact(as("ada", auth.CredentialSession), connect.NewRequest(&pb.GetPolicyImpactRequest{
		OrgId: org, PolicyJson: policyJSON(t, doc),
	}))
	require.NoError(t, err)
	got := resp.Msg
	assert.EqualValues(t, 14, got.GetWindowDays())
	assert.Equal(t, now.Add(-org_policy_impact.ImpactWindow), store.since)
	assert.False(t, got.GetTruncated())
	assert.EqualValues(t, 6, got.GetChatsConsidered())
	assert.EqualValues(t, 3, got.GetChatsRefused(), "c1 (a personal account), c3 (a managed model not allowed), c5 (an automation on a seat)")
	assert.EqualValues(t, 1, got.GetAutomationsRefused())
	require.Len(t, got.GetReasons(), 2)
	assert.Equal(t, orgpolicy.ReasonModelNotAllowed, got.GetReasons()[0].GetReason())
	assert.EqualValues(t, 1, got.GetReasons()[0].GetChats())
	assert.Equal(t, orgpolicy.ReasonProviderNotAllowed, got.GetReasons()[1].GetReason())
	assert.EqualValues(t, 2, got.GetReasons()[1].GetChats())
	assert.EqualValues(t, 1, got.GetReasons()[1].GetAutomations())

	// No policy refuses nothing.
	resp, err = svc.GetPolicyImpact(as("olga", auth.CredentialSession), connect.NewRequest(&pb.GetPolicyImpactRequest{OrgId: org}))
	require.NoError(t, err)
	assert.EqualValues(t, 6, resp.Msg.GetChatsConsidered())
	assert.Zero(t, resp.Msg.GetChatsRefused())
	assert.Empty(t, resp.Msg.GetReasons())
}

// The automations section judges exactly the chats it will govern, by the
// attendance rule each call is scoped with: a trigger's run nobody adopted
// (its trigger deleted since or not) and an agent-started run under one are
// counted; a trigger's chat a person adopted, and an agent-started run under
// a person's chat, are not.
func TestGetPolicyImpact_AutomationsFollowTheAttendanceRule(t *testing.T) {
	trigger := "trg-1"
	store := &calls{
		rows: []db.OrgChatCall{
			{ChatID: "fired", Provider: "claude", Model: "claude-4.6-sonnet", Funding: "org_seat"},
			{ChatID: "adopted", Provider: "claude", Model: "claude-4.6-sonnet", Funding: "org_seat"},
			{ChatID: "descendant", Provider: "claude", Model: "claude-4.6-sonnet", Funding: "org_seat"},
			{ChatID: "deleted-trigger", Provider: "claude", Model: "claude-4.6-sonnet", Funding: "org_seat"},
			{ChatID: "persons-descendant", Provider: "claude", Model: "claude-4.6-sonnet", Funding: "org_seat"},
			{ChatID: "persons", Provider: "claude", Model: "claude-4.6-sonnet", Funding: "org_seat"},
		},
		chats: map[string]*db.Chat{
			"fired":              scheduled("fired", &trigger),
			"adopted":            scheduled("adopted", &trigger),
			"descendant":         agentStarted("descendant"),
			"deleted-trigger":    scheduled("deleted-trigger", nil),
			"persons-descendant": agentStarted("persons-descendant"),
		},
		turns:   map[string]bool{"adopted": true},
		parents: map[string]string{"descendant": "fired", "persons-descendant": "persons"},
	}
	svc := newService(t, store)
	enforce := controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE
	doc := &controlplanev1.OrgPolicy{Automations: &controlplanev1.AutomationsPolicy{Mode: enforce, AllowedSources: []controlplanev1.ProviderSource{
		controlplanev1.ProviderSource_PROVIDER_SOURCE_ORG_ACCOUNTS, controlplanev1.ProviderSource_PROVIDER_SOURCE_RELIANT,
	}}}
	resp, err := svc.GetPolicyImpact(as("ada", auth.CredentialSession), connect.NewRequest(&pb.GetPolicyImpactRequest{
		OrgId: org, PolicyJson: policyJSON(t, doc),
	}))
	require.NoError(t, err)
	assert.EqualValues(t, 6, resp.Msg.GetChatsConsidered())
	assert.EqualValues(t, 3, resp.Msg.GetChatsRefused(), "fired, descendant and deleted-trigger: nobody is behind them, and a seat is not an automation's source")
	assert.EqualValues(t, 3, resp.Msg.GetAutomationsRefused())
}

// A larger org's preview reads its most recently active chats and says so.
func TestGetPolicyImpact_SaysWhenItIsTruncated(t *testing.T) {
	rows := make([]db.OrgChatCall, org_policy_impact.ImpactChatLimit+5)
	for i := range rows {
		rows[i] = db.OrgChatCall{ChatID: "c", Provider: "anthropic", Funding: "org_key"}
	}
	store := &calls{rows: rows}
	resp, err := newService(t, store).GetPolicyImpact(as("ada", auth.CredentialSession), connect.NewRequest(&pb.GetPolicyImpactRequest{OrgId: org}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetTruncated())
	assert.EqualValues(t, org_policy_impact.ImpactChatLimit, resp.Msg.GetChatsConsidered())
	assert.Equal(t, org_policy_impact.ImpactChatLimit+1, store.limit, "one more than it reads, to know")
}

// Who may ask: an owner or admin of the org, as a person; a bad document is
// the caller's to fix.
func TestGetPolicyImpact_Refusals(t *testing.T) {
	svc := newService(t, &calls{})
	for name, tc := range map[string]struct {
		ctx    context.Context
		req    *pb.GetPolicyImpactRequest
		code   connect.Code
		reason string
	}{
		"a member":               {as("mia", auth.CredentialSession), &pb.GetPolicyImpactRequest{OrgId: org}, connect.CodePermissionDenied, orgs.ReasonOrgAdminRequired},
		"a non-member":           {as("nick", auth.CredentialSession), &pb.GetPolicyImpactRequest{OrgId: org}, connect.CodePermissionDenied, orgs.ReasonOrgAdminRequired},
		"an admin's CLI login":   {as("ada", auth.CredentialAccessToken), &pb.GetPolicyImpactRequest{OrgId: org}, connect.CodePermissionDenied, orgs.ReasonHumanOnly},
		"no org":                 {as("ada", auth.CredentialSession), &pb.GetPolicyImpactRequest{}, connect.CodeInvalidArgument, ""},
		"a document that is not": {as("ada", auth.CredentialSession), &pb.GetPolicyImpactRequest{OrgId: org, PolicyJson: `{"providers": 7}`}, connect.CodeInvalidArgument, org_policy_impact.ReasonPolicyInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.GetPolicyImpact(tc.ctx, connect.NewRequest(tc.req))
			require.Error(t, err)
			assert.Equal(t, tc.code, connect.CodeOf(err), "%v", err)
			if tc.reason != "" {
				assert.Equal(t, tc.reason, svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader))
			}
		})
	}
}
