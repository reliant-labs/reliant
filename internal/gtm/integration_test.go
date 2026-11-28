// Copyright (c) 2026 Reliant Labs

package gtm_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cpv1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	deployv1 "github.com/reliant-labs/reliant/gen/controlplane/services/deploy/v1"
	obsv1 "github.com/reliant-labs/reliant/gen/controlplane/services/observability/v1"
	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/featuregate"
	"github.com/reliant-labs/reliant/internal/gtm"
	"github.com/reliant-labs/reliant/internal/gtm/analytics"
	"github.com/reliant-labs/reliant/internal/gtm/email"
	handlergtm "github.com/reliant-labs/reliant/internal/handlers/gtm"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/pkg/config"
)

// GTM end to end over a migrated Postgres, from the same parts internal/app
// composes, built here by hand rather than through its providers: one
// featuregate.NewStatic over an orgs.Resolver gating the domain (gtm.New over
// NewStore), the GtmService handler over that domain, and the agent's tools
// over the same domain in a tools factory run by toolexec's
// LocalToolExecutor. The providers themselves (NewFeatureGate, NewGtmDomain,
// NewWorkerGtmDomain, the analytics, email, connections and assets wiring) are
// covered in internal/app's providers_gtm_test.go, and the gate's path into
// the worker's call_llm in its feature_gate_wiring_test.go.
//
// The domain also reads product analytics as the worker wires it (the panel
// query and the environment lookup over one token) against a fake control
// plane, and the embedded idea bank (internal/gtm/library) as PR-5 serves it.
//
// Two people ask every surface the same questions: alice, a member of the
// allowlisted org, and carol, a member of an org that is not. Only alice sees
// anything; to carol GTM does not exist.
//
// This is not a workflow run. A turn's capability set is built here the way
// call_llm builds it (activities/handlers entitledFeatures, which is
// unexported): the real gate's answer for each gated feature the node reaches,
// for the chat's user and org, becomes CapabilityInputs.EntitledFeatures, and
// tools.ResolveCapabilities does the rest. The tools then run through the
// executor execute_tools runs a server-placed tool with.

const (
	// allowedOrg is the allowlisted org: tenant zero, as in fixtures.
	allowedOrg = "55949942-3ee9-4cfc-bb7c-817bdba75186"
	// otherOrg is an org GTM is not on for.
	otherOrg = "6b1f2b2e-0f43-4a7e-9a64-0c0f3d1d7c11"

	alice = "user-alice"
	carol = "user-carol"
)

// members is the control plane's membership answer: alice in allowedOrg,
// carol in otherOrg, nobody anywhere else.
type members map[string]string

func (m members) ResolveOrgMembership(_ context.Context, userID, orgID string) (accesstokenclient.OrgMembership, error) {
	if m[userID] != orgID {
		return accesstokenclient.OrgMembership{OrgID: orgID}, nil
	}
	return accesstokenclient.OrgMembership{OrgID: orgID, Member: true, Role: "member"}, nil
}

type gtmStack struct {
	env      *storetest.Env
	gate     featuregate.Gate
	handler  *handlergtm.Service
	factory  *tools.ToolsFactory
	executor *toolexec.LocalToolExecutor
	chats    map[string]string // user -> their chat, in their org
	// cp is the fake control plane product analytics reads through.
	cp *fakeControlPlane
}

func newGTMStack(t *testing.T) *gtmStack {
	t.Helper()
	env := storetest.New(t)
	membership := orgs.New(members{alice: allowedOrg, carol: otherOrg})
	gate := featuregate.NewStatic(&configv1.GtmConfig{AllowedOrgIds: []string{allowedOrg}}, membership)
	// Product analytics as the worker wires it (providers_gtm.go
	// gtmAnalytics): the panel query and the environment lookup over one
	// token, here against a fake control plane.
	cp := &fakeControlPlane{envs: []*cpv1.DeployEnvironment{
		{Id: "env-prod", Name: "prod"}, {Id: "env-staging", Name: "staging"},
	}}
	token := func() string { return analyticsToken }
	reader := analytics.New(analytics.Deps{
		CP:    cp,
		Token: token,
		Envs:  analytics.NewEnvResolver(analytics.EnvResolverDeps{CP: cp, Token: token}),
	})
	svc := gtm.New(gtm.Deps{Store: gtm.NewStore(env.ORM), Gate: gate, Analytics: reader})

	handler, err := handlergtm.New(handlergtm.Deps{Logger: slog.Default(), Config: &config.Config{}, Service: svc})
	require.NoError(t, err)

	// The send tool's approver, as the worker builds it, over no saved
	// connections: nothing here may send.
	approver := email.NewApprover(svc, email.NewConnectionAccounts(nil))
	factory := tools.NewToolsFactory(&tools.ToolsOptions{Repo: env.Repo, GTM: svc, GTMEmail: approver})

	s := &gtmStack{env: env, gate: gate, handler: handler, factory: factory, executor: toolexec.NewLocalToolExecutor(factory), chats: map[string]string{}, cp: cp}
	for user, org := range map[string]string{alice: allowedOrg, carol: otherOrg} {
		s.chats[user] = s.newChat(t, user, org)
	}
	return s
}

func (s *gtmStack) newChat(t *testing.T, user, org string) string {
	t.Helper()
	id := "chat-" + user
	now := time.Now().UTC()
	require.NoError(t, store.CreateChat(context.Background(), s.env.ORM, &store.Chat{
		ID: id, Title: id, ProjectID: storetest.SeedProjectID, UserID: user, OrgID: &org,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	return id
}

// session is user signed in to the app: a person-class credential.
func session(user string) context.Context {
	return auth.WithCredential(auth.WithUser(context.Background(), user, "user", ""), auth.CredentialSession)
}

// accessToken is user calling with an rlat_ access token minted in org (an
// API token, or the CLI login every agent shell inherits).
func accessToken(user, org string) context.Context {
	ctx := auth.WithCredential(auth.WithUser(context.Background(), user, "user", ""), auth.CredentialAccessToken)
	return auth.WithMachineToken(ctx, &fat.Principal{TokenID: "tok-" + user, OrgID: org, ActingUserID: user})
}

// caps is the capability set call_llm resolves for user's turn in org: a
// node that may load any tool, preloading preloaded, entitled to exactly the
// features the gate allows user in org.
func (s *gtmStack) caps(t *testing.T, user, org string, preloaded ...string) *tools.Capabilities {
	t.Helper()
	access := tools.ResolveToolAccess(append([]string{tools.ToolView}, preloaded...), []string{tools.LoadableWildcard}, nil)
	var entitled map[string]bool
	for _, f := range tools.ReachedGatedFeatures(access) {
		allowed, err := s.gate.Allowed(context.Background(), f, user, org)
		require.NoError(t, err)
		if !allowed {
			continue
		}
		if entitled == nil {
			entitled = map[string]bool{}
		}
		entitled[string(f)] = true
	}
	return tools.ResolveCapabilities(tools.CapabilityInputs{Access: access, Permission: tools.PermissionMutating, EntitledFeatures: entitled})
}

// call runs one tool call in user's chat under caps, as execute_tools does.
func (s *gtmStack) call(user string, caps *tools.Capabilities, name, input string) *toolexec.ExecutionResult {
	ctx := tools.WithCapabilities(context.Background(), caps)
	return s.executor.ExecuteTool(ctx, name, input, "call-1", 0, map[string]interface{}{"chat_id": s.chats[user], "thread": "thread-1"})
}

func TestGTMIntegration_OnlyTheAllowlistedOrgSeesGTM(t *testing.T) {
	s := newGTMStack(t)

	// alice drafts an idea in the web app, so there is something to see.
	created, err := s.handler.CreateIdea(session(alice), connect.NewRequest(&pb.CreateIdeaRequest{
		OrgId: allowedOrg, Kind: pb.GtmIdeaKind_GTM_IDEA_KIND_GTM, Title: "Launch week recap",
	}))
	require.NoError(t, err)
	ideaID := created.Msg.GetIdea().GetId()
	require.NotEmpty(t, ideaID)

	t.Run("nav access", func(t *testing.T) {
		got, err := s.handler.GetGtmAccess(session(alice), connect.NewRequest(&pb.GetGtmAccessRequest{OrgId: allowedOrg}))
		require.NoError(t, err)
		assert.True(t, got.Msg.GetEnabled())

		for _, org := range []string{otherOrg, allowedOrg} {
			got, err := s.handler.GetGtmAccess(session(carol), connect.NewRequest(&pb.GetGtmAccessRequest{OrgId: org}))
			require.NoError(t, err, org)
			assert.False(t, got.Msg.GetEnabled(), "carol in %s", org)
		}
	})

	t.Run("an rpc", func(t *testing.T) {
		list, err := s.handler.ListIdeas(session(alice), connect.NewRequest(&pb.ListIdeasRequest{OrgId: allowedOrg}))
		require.NoError(t, err)
		require.Len(t, list.Msg.GetIdeas(), 1)
		assert.Equal(t, ideaID, list.Msg.GetIdeas()[0].GetId())

		// carol in her own org, and carol naming alice's: the same bare
		// NotFound, for a read and a write alike.
		var texts []string
		for _, org := range []string{otherOrg, allowedOrg} {
			_, err := s.handler.ListIdeas(session(carol), connect.NewRequest(&pb.ListIdeasRequest{OrgId: org}))
			require.Error(t, err)
			assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err), "ListIdeas in %s", org)
			texts = append(texts, err.Error())

			_, err = s.handler.CreateIdea(session(carol), connect.NewRequest(&pb.CreateIdeaRequest{
				OrgId: org, Kind: pb.GtmIdeaKind_GTM_IDEA_KIND_GTM, Title: "x",
			}))
			require.Error(t, err)
			assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err), "CreateIdea in %s", org)
			texts = append(texts, err.Error())
		}
		for _, text := range texts[1:] {
			assert.Equal(t, texts[0], text, "every refusal reads the same")
		}
	})

	t.Run("a load_tool search", func(t *testing.T) {
		// Search matches tool names: every GTM tool but
		// read_product_analytics has "gtm" in its name.
		got := s.call(alice, s.caps(t, alice, allowedOrg), tools.ToolLoadTool, `{"query":"gtm"}`)
		require.True(t, got.Success, got.Content)
		for name := range tools.FeatureGatedTools() {
			if strings.Contains(name, "gtm") {
				assert.Contains(t, got.Content, name, "alice finds %s", name)
			}
		}
		got = s.call(alice, s.caps(t, alice, allowedOrg), tools.ToolLoadTool, `{"query":"product_analytics"}`)
		assert.Contains(t, got.Content, tools.ToolReadProductAnalytics)

		for _, query := range []string{"gtm", "product_analytics"} {
			got = s.call(carol, s.caps(t, carol, otherOrg), tools.ToolLoadTool, `{"query":"`+query+`"}`)
			assert.Equal(t, "No tools found matching '"+query+"'.", got.Content, "carol finds nothing")
			for name := range tools.FeatureGatedTools() {
				assert.NotContains(t, got.Content, name)
			}
			assert.Empty(t, tools.SearchTools(query, s.caps(t, carol, otherOrg)))
		}
	})

	t.Run("a direct tool call", func(t *testing.T) {
		got := s.call(alice, s.caps(t, alice, allowedOrg, tools.ToolListGtmIdeas), tools.ToolListGtmIdeas, `{}`)
		require.True(t, got.Success, got.Content)
		assert.Contains(t, got.Content, ideaID)
		assert.Contains(t, got.Content, "Launch week recap")

		// To carol the tool does not exist: byte for byte the answer to a
		// name no registry has, even when her node preloads it.
		const unknown = "zz_no_such_tool"
		carolCaps := s.caps(t, carol, otherOrg, tools.ToolListGtmIdeas)
		assert.False(t, carolCaps.Offers(tools.ToolListGtmIdeas))
		got = s.call(carol, carolCaps, tools.ToolListGtmIdeas, `{}`)
		want := s.call(carol, carolCaps, unknown, `{}`)
		swap := func(text string) string { return strings.ReplaceAll(text, unknown, tools.ToolListGtmIdeas) }
		assert.Equal(t, "TOOL_NOT_FOUND", got.ErrorCode)
		assert.Equal(t, "Tool 'list_gtm_ideas' not found", got.Content)
		assert.Equal(t, swap(want.Content), got.Content)
		assert.Equal(t, swap(want.ErrorMessage), got.ErrorMessage)
		assert.Equal(t, swap(carolCaps.Explain(unknown)), carolCaps.Explain(tools.ToolListGtmIdeas))
	})

	t.Run("a stale entitlement still reaches nothing", func(t *testing.T) {
		// A set that claims GTM for carol (one resolved before a revocation)
		// offers the tool, and the domain's own gate check refuses the call
		// exactly as a missing row.
		forged := tools.ResolveCapabilities(tools.CapabilityInputs{
			Access:           tools.ResolveToolAccess([]string{tools.ToolListGtmIdeas}, nil, nil),
			Permission:       tools.PermissionMutating,
			EntitledFeatures: map[string]bool{string(featuregate.GTM): true},
		})
		got := s.call(carol, forged, tools.ToolListGtmIdeas, `{"id":"`+ideaID+`"}`)
		assert.True(t, got.IsError)
		assert.NotContains(t, got.Content, "Launch week recap")
		missing := s.call(alice, s.caps(t, alice, allowedOrg, tools.ToolListGtmIdeas), tools.ToolListGtmIdeas, `{"id":"no-such-idea"}`)
		require.True(t, missing.IsError)
		assert.Equal(t, strings.ReplaceAll(missing.Content, "no-such-idea", ideaID), got.Content, "carol's refusal reads as alice's missing idea")
	})
}

// An rlat_ access token is never a Person: the CLI login every agent shell
// inherits is one. On a Person-only rpc it gets the agent's refusal; the same
// person signed in to the app is allowed.
func TestGTMIntegration_AccessTokenActsAsAgent(t *testing.T) {
	s := newGTMStack(t)
	created, err := s.handler.CreateIdea(accessToken(alice, allowedOrg), connect.NewRequest(&pb.CreateIdeaRequest{
		OrgId: allowedOrg, Kind: pb.GtmIdeaKind_GTM_IDEA_KIND_CONTENT, Title: "Docs tour video",
	}))
	require.NoError(t, err, "an agent may propose an idea")
	idea := created.Msg.GetIdea()
	assert.Equal(t, pb.GtmIdeaStatus_GTM_IDEA_STATUS_PROPOSED, idea.GetStatus())

	accept := &pb.SetIdeaStatusRequest{OrgId: allowedOrg, Id: idea.GetId(), ExpectedRevision: idea.GetRevision(), Status: pb.GtmIdeaStatus_GTM_IDEA_STATUS_ACCEPTED}
	_, err = s.handler.SetIdeaStatus(accessToken(alice, allowedOrg), connect.NewRequest(accept))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err), "accepting an idea is a person's: %v", err)

	got, err := s.handler.SetIdeaStatus(session(alice), connect.NewRequest(accept))
	require.NoError(t, err)
	assert.Equal(t, pb.GtmIdeaStatus_GTM_IDEA_STATUS_ACCEPTED, got.Msg.GetIdea().GetStatus())

	// carol's token is refused before anything else, as NotFound.
	_, err = s.handler.SetIdeaStatus(accessToken(carol, otherOrg), connect.NewRequest(&pb.SetIdeaStatusRequest{
		OrgId: otherOrg, Id: idea.GetId(), ExpectedRevision: 1, Status: pb.GtmIdeaStatus_GTM_IDEA_STATUS_ACCEPTED,
	}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// send_gtm_email is refused in an unattended run before it reads anything:
// no card can be shown there, so nothing is sent.
func TestGTMIntegration_SendEmailRefusedUnattended(t *testing.T) {
	s := newGTMStack(t)
	tool, ok := s.factory.GetToolByName(tools.ToolSendGtmEmail, nil).(tools.AutomationApprovalTool)
	require.True(t, ok, "send_gtm_email is an approval tool")

	ctx := rctx.NewToolContext(tools.WithCapabilities(context.Background(), s.caps(t, alice, allowedOrg, tools.ToolSendGtmEmail)), s.chats[alice], "thread-1", nil, nil)
	_, needsApproval, err := tool.PrepareAutomation(ctx, tools.ToolCall{ID: "call-1", Name: tools.ToolSendGtmEmail, Input: `{"item_id":"item-1","expected_revision":1}`}, true)
	require.Error(t, err)
	assert.False(t, needsApproval)
	assert.Equal(t, email.ErrUnattended.Error(), tools.ActivationRefusalReason(err))
}

// analyticsToken is the GTM analytics token the fake control plane expects.
const analyticsToken = "rlat_test_gtm_analytics"

// fakeControlPlane is the control plane's ObservabilityService (QueryPanel)
// and DeployService (ListEnvironments), as the analytics reader calls them.
// It records what it was asked and answers a funnel with fixed counts.
type fakeControlPlane struct {
	envs []*cpv1.DeployEnvironment

	mu      sync.Mutex
	queries []*obsv1.QueryPanelRequest
	lookups []*deployv1.ListDeployEnvironmentsRequest
	auth    []string
}

func (f *fakeControlPlane) QueryPanel(_ context.Context, req *connect.Request[obsv1.QueryPanelRequest]) (*connect.Response[obsv1.QueryPanelResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, req.Msg)
	f.auth = append(f.auth, req.Header().Get("Authorization"))
	resp := &obsv1.QueryPanelResponse{Kind: "funnel"}
	for i, e := range req.Msg.GetPanel().GetFunnel().GetSteps() {
		resp.FunnelSteps = append(resp.FunnelSteps, &obsv1.FunnelStepResult{Event: e, Count: int64(100 - 40*i), RateFromFirst: float64(100-40*i) / 100})
	}
	return connect.NewResponse(resp), nil
}

func (f *fakeControlPlane) ListEnvironments(_ context.Context, req *connect.Request[deployv1.ListDeployEnvironmentsRequest]) (*connect.Response[deployv1.ListDeployEnvironmentsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups = append(f.lookups, req.Msg)
	f.auth = append(f.auth, req.Header().Get("Authorization"))
	return connect.NewResponse(&deployv1.ListDeployEnvironmentsResponse{Environments: f.envs}), nil
}

func (f *fakeControlPlane) snapshot() (queries []*obsv1.QueryPanelRequest, lookups []*deployv1.ListDeployEnvironmentsRequest, auth []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*obsv1.QueryPanelRequest(nil), f.queries...), append([]*deployv1.ListDeployEnvironmentsRequest(nil), f.lookups...), append([]string(nil), f.auth...)
}

// r4: the org says what kind of business it is; the embedded idea bank is
// read diverse and filtered to fit it; an idea cites a bank entry and
// answers that entry's tailor questions. carol's org reaches none of it.
func TestGTMIntegration_BankFlow(t *testing.T) {
	s := newGTMStack(t)
	ctx := session(alice)

	pos, err := s.handler.UpdatePositioning(ctx, connect.NewRequest(&pb.UpdatePositioningRequest{
		OrgId: allowedOrg, ExpectedRevision: 0, PositioningMd: "Scheduling software for small clinics.",
		BusinessType:   pb.GtmBusinessType_GTM_BUSINESS_TYPE_B2B_SAAS,
		BusinessModels: []pb.GtmBusinessModel{pb.GtmBusinessModel_GTM_BUSINESS_MODEL_SUBSCRIPTION},
	}))
	require.NoError(t, err)
	require.Equal(t, pb.GtmBusinessType_GTM_BUSINESS_TYPE_B2B_SAAS, pos.Msg.GetPositioning().GetBusinessType())

	yes := true
	lib, err := s.handler.ListStrategyLibrary(ctx, connect.NewRequest(&pb.ListStrategyLibraryRequest{
		OrgId: allowedOrg, Diverse: &yes, FitsMyBusiness: &yes, PageSize: 12,
	}))
	require.NoError(t, err)
	entries := lib.Msg.GetEntries()
	require.NotEmpty(t, entries)
	assert.LessOrEqual(t, len(entries), 12)
	assert.Empty(t, lib.Msg.GetNextPageToken(), "diverse mode does not page")
	categories := map[pb.GtmStrategyCategory]bool{}
	for _, e := range entries {
		categories[e.GetCategory()] = true
		types, models := e.GetBusinessTypes(), e.GetBusinessModels()
		assert.True(t, len(types) == 0 || containsEnum(types, pb.GtmBusinessType_GTM_BUSINESS_TYPE_B2B_SAAS), "%s fits the org's type", e.GetKey())
		assert.True(t, len(models) == 0 || containsEnum(models, pb.GtmBusinessModel_GTM_BUSINESS_MODEL_SUBSCRIPTION), "%s fits the org's models", e.GetKey())
	}
	assert.GreaterOrEqual(t, len(categories), 4, "diverse spreads across categories")

	pick := entries[0]
	require.NotEmpty(t, pick.GetTailorQuestions())
	var answers []*pb.GtmTailorAnswer
	for _, q := range pick.GetTailorQuestions() {
		answers = append(answers, &pb.GtmTailorAnswer{Question: q, Answer: "For clinics: front-desk managers who book by phone today."})
	}
	created, err := s.handler.CreateIdea(ctx, connect.NewRequest(&pb.CreateIdeaRequest{
		OrgId: allowedOrg, Kind: pb.GtmIdeaKind_GTM_IDEA_KIND_GTM, Title: "Tailored: " + pick.GetName(),
		LibraryKey: pick.GetKey(), TailorAnswers: answers, SuggestedWorkflowMd: pick.GetSuggestedWorkflowMd(),
	}))
	require.NoError(t, err)
	got, err := s.handler.GetIdea(ctx, connect.NewRequest(&pb.GetIdeaRequest{OrgId: allowedOrg, Id: created.Msg.GetIdea().GetId()}))
	require.NoError(t, err)
	assert.Equal(t, pick.GetKey(), got.Msg.GetIdea().GetLibraryKey())
	require.Len(t, got.Msg.GetIdea().GetTailorAnswers(), len(answers))
	assert.Equal(t, pick.GetTailorQuestions()[0], got.Msg.GetIdea().GetTailorAnswers()[0].GetQuestion())

	// A key the bank does not have is refused, not stored.
	_, err = s.handler.CreateIdea(ctx, connect.NewRequest(&pb.CreateIdeaRequest{
		OrgId: allowedOrg, Kind: pb.GtmIdeaKind_GTM_IDEA_KIND_GTM, Title: "x", LibraryKey: "no-such-bank-key",
	}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// The agent reads the same bank: diverse, fitted to the business.
	tool := s.call(alice, s.caps(t, alice, allowedOrg, tools.ToolListGtmStrategyLibrary), tools.ToolListGtmStrategyLibrary, `{"diverse":true,"limit":8}`)
	require.True(t, tool.Success, tool.Content)
	assert.Contains(t, tool.Content, `"key"`)

	// carol's org has no bank at all.
	_, err = s.handler.ListStrategyLibrary(session(carol), connect.NewRequest(&pb.ListStrategyLibraryRequest{OrgId: otherOrg, Diverse: &yes}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func containsEnum[E comparable](xs []E, x E) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// read_product_analytics narrows to one deploy environment by name: the
// name resolves through the control plane's DeployService to the org's
// environment ids, the panel query is scoped to exactly those ids, and
// both calls carry the GTM analytics token. A name that matches nothing is
// refused before any query runs: a failed lookup never widens the read.
func TestGTMIntegration_AnalyticsEnvironmentFilter(t *testing.T) {
	s := newGTMStack(t)
	caps := s.caps(t, alice, allowedOrg, tools.ToolReadProductAnalytics)
	from := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	to := time.Now().UTC().Format(time.RFC3339)
	input := func(env string) string {
		b, err := json.Marshal(map[string]any{"template": "funnel", "events": []string{"signup", "first_run"}, "from": from, "to": to, "environment": env})
		require.NoError(t, err)
		return string(b)
	}

	got := s.call(alice, caps, tools.ToolReadProductAnalytics, input("prod"))
	require.True(t, got.Success, got.Content)
	assert.Contains(t, got.Content, `"environment":"prod"`)
	assert.Contains(t, got.Content, "signup")

	queries, lookups, auth := s.cp.snapshot()
	require.Len(t, lookups, 1)
	assert.Equal(t, allowedOrg, lookups[0].GetOrgId(), "environments are looked up in the run's org")
	require.Len(t, queries, 1)
	assert.Equal(t, allowedOrg, queries[0].GetScope().GetOrgId())
	assert.Equal(t, []string{"env-prod"}, queries[0].GetScope().GetEnvIds(), "the query reads prod only")
	for _, a := range auth {
		assert.Equal(t, "Bearer "+analyticsToken, a, "every control-plane call carries the GTM analytics token")
	}

	// An unknown name: refused with the names there are, and no query.
	got = s.call(alice, caps, tools.ToolReadProductAnalytics, input("nope"))
	assert.True(t, got.IsError)
	assert.Contains(t, got.Content, "staging")
	queries, _, _ = s.cp.snapshot()
	assert.Len(t, queries, 1, "a failed lookup never runs a query across every environment")

	// No environment: every environment, labelled as such.
	got = s.call(alice, caps, tools.ToolReadProductAnalytics, input(""))
	require.True(t, got.Success, got.Content)
	assert.Contains(t, got.Content, "all environments")
	queries, _, _ = s.cp.snapshot()
	require.Len(t, queries, 2)
	assert.Empty(t, queries[1].GetScope().GetEnvIds())
}
