// Copyright (c) 2026 Reliant Labs

package gtm_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/featuregate"
	"github.com/reliant-labs/reliant/internal/gtm"
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
}

func newGTMStack(t *testing.T) *gtmStack {
	t.Helper()
	env := storetest.New(t)
	membership := orgs.New(members{alice: allowedOrg, carol: otherOrg})
	gate := featuregate.NewStatic(&configv1.GtmConfig{AllowedOrgIds: []string{allowedOrg}}, membership)
	svc := gtm.New(gtm.Deps{Store: gtm.NewStore(env.ORM), Gate: gate})

	handler, err := handlergtm.New(handlergtm.Deps{Logger: slog.Default(), Config: &config.Config{}, Service: svc})
	require.NoError(t, err)

	// The send tool's approver, as the worker builds it, over no saved
	// connections: nothing here may send.
	approver := email.NewApprover(svc, email.NewConnectionAccounts(nil))
	factory := tools.NewToolsFactory(&tools.ToolsOptions{Repo: env.Repo, GTM: svc, GTMEmail: approver})

	s := &gtmStack{env: env, gate: gate, handler: handler, factory: factory, executor: toolexec.NewLocalToolExecutor(factory), chats: map[string]string{}}
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
