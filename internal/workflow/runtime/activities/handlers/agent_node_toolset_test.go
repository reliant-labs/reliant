// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/crypto"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/connauth"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/vault"
)

// The tool set an Autopilot agent node is actually handed.
//
// Every scenario test of these workflows MOCKS the agent node (a scripted
// call_llm response), so none of them can notice that the agent was never
// offered its tools. This one mocks nothing between the node declaration and
// the model request: the real CallLLM activity, the real ToolsFactory, the real
// credential source (connauth over connections.Resolver) and a real
// connection, on a thread laid out exactly as a nested `type: workflow` node
// with `thread: {mode: new}` lays it out — a thread of its own that names no
// workflow row. Only the LLM driver is a capture stub.

// autopilotReviewerTools are the tools autopilot-pr-review hands its agent.
var autopilotReviewerTools = []string{
	"github__pr_get", "github__pr_list_files", "github__repo_get_content",
	"github__repo_get_tree", "github__code_search", "github__issue_get",
}

const autopilotResponseTool = "submit_review"

type nestedAgentFixture struct {
	h         *IdempotencyTestHelper
	chat      *db.Chat
	thread    string
	driver    *promptCaptureDriver
	callLLM   *CallLLMActivity
	lastCalls *tools.Capabilities
}

// newNestedAgentFixture seeds a user with a real GitHub connection, a root run
// owned by them and a nested-agent thread that, like a `thread: {mode: new}`
// workflow node's, has no workflow row of its own.
func newNestedAgentFixture(t *testing.T, noMachine bool) *nestedAgentFixture {
	t.Helper()
	h := NewIdempotencyTestHelper(t)
	t.Cleanup(h.Cleanup)
	ctx := context.Background()

	userID := "user-" + uuid.NewString()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), userID)
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, userID)
	if noMachine {
		markChatNoMachine(t, h, chat.ID)
	}
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	owner := userID
	require.NoError(t, h.Repo().CreateWorkflow(ctx, &db.Workflow{
		ID: chat.ID, ChatID: chat.ID, WorkflowName: "pr-review", Thread: chat.ID,
		Status: db.Active(), CreatedAt: time.Now().UTC(), OwnerUserID: &owner,
	}))
	nested := uuid.NewString()
	rootWorkflow := chat.ID
	_, err := h.Repo().CreateThread(ctx, &db.Thread{
		ID: nested, ChatID: chat.ID, WorkflowID: &rootWorkflow, Origin: db.ThreadOriginNode,
		CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	_, err = h.Repo().CreateContextWindow(ctx, &db.ContextWindow{ID: chat.ID + ":" + nested + ":0", ThreadID: nested, Sequence: 0})
	require.NoError(t, err)
	h.CreateTestUserMessage(ctx, chat.ID, nested)

	key := make([]byte, 32)
	_, err = rand.Read(key)
	require.NoError(t, err)
	ring, err := crypto.ParseKeyring("v1:" + base64.StdEncoding.EncodeToString(key))
	require.NoError(t, err)
	vaultDB, err := orm.NewClientWithDB(h.DB(), "postgres")
	require.NoError(t, err)
	v := vault.New(vault.Deps{DB: vaultDB, Keys: vault.NewEnvKeyWrapper(ring)})
	providers, err := connections.ProvidersFromCatalog(catalog.MustBuiltin().Manifests(),
		connections.OAuthClientsFromEnv(func(string) string { return "" }))
	require.NoError(t, err)
	store := h.Repo().Connections()
	tokens := connections.NewTokenSource(store, v, providers, nil)
	broker := connections.NewBroker(store, v, providers, nil, "https://reliant.example")
	_, err = connections.NewService(store, v, providers, tokens, broker, nil).CreateAPIKey(ctx, connections.CreateAPIKeyParams{
		UserID: userID, IntegrationID: "github", Name: "pat", Kind: connections.APIKeyKindAPIKey,
		Fields: map[string]string{"api_key": "ghp_test_not_a_real_token"},
	})
	require.NoError(t, err)
	source := connauth.New(connauth.Deps{Resolver: connections.NewResolver(h.Repo(), store, tokens)})

	driver := &promptCaptureDriver{}
	resolver := func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
		return driver, nil
	}
	factory := tools.NewToolsFactory(&tools.ToolsOptions{Repo: h.Repo(), IntegrationCredentials: source})
	return &nestedAgentFixture{
		h: h, chat: chat, thread: nested, driver: driver,
		callLLM: NewCallLLMActivity(h.Repo(), nil, factory, &staticConfigProvider{}, resolver, nil),
	}
}

// turn runs one call_llm turn on the nested thread, shaped as autopilot-agent
// declares it: the caller's tools preloaded, nothing loadable, mutating tier,
// unattended, and a response tool. It returns what the model was handed.
func (f *nestedAgentFixture) turn(t *testing.T) []string {
	t.Helper()
	var output CallLLMOutput
	require.NoError(t, f.h.ExecuteActivity(f.callLLM.Execute, ActivityInput{
		Runtime: RuntimeContext{ChatID: f.chat.ID, Thread: f.thread, Unattended: true},
		Node: &reliantv1.Node{Type: "call_llm", Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{
			Model: &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Literal{Literal: &reliantv1.ModelSelector{Id: "mock-model"}}},
			ToolsConfig: &reliantv1.ToolsConfig{
				PreloadedTools: celStringListLiteral(autopilotReviewerTools),
				LoadableTools:  celStringListLiteral([]string{}),
			},
			ResponseTool: &reliantv1.ResponseTool{
				Name:        &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: autopilotResponseTool}},
				Description: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "Submit the review."}},
			},
		}}},
	}, &output))
	f.lastCalls = tools.CapabilitiesFromProto(output.GetCapabilities())
	return append([]string(nil), f.driver.capturedTools...)
}

// An agent node that names integration actions and a response tool is handed
// all of them, whether or not its run has a machine, on the thread layout a
// nested workflow node produces.
func TestAgentNodeIsOfferedItsIntegrationToolsAndResponseTool(t *testing.T) {
	for name, noMachine := range map[string]bool{"no machine": true, "machine attached": false} {
		t.Run(name, func(t *testing.T) {
			f := newNestedAgentFixture(t, noMachine)

			offered := f.turn(t)

			for _, want := range autopilotReviewerTools {
				assert.Contains(t, offered, want, "the node names %s, so the model must be handed it", want)
			}
			assert.Contains(t, offered, autopilotResponseTool, "the response tool is the node's own output channel")
			assert.Empty(t, f.lastCalls.WithheldIntegrations,
				"the owner's GitHub connection must be found from the nested thread")

			// Every offered name must also be executable: either a registry
			// tool with a placement, or the node's response tool, which
			// execute_tools runs inline instead of dispatching.
			for _, name := range offered {
				if name == autopilotResponseTool {
					continue
				}
				_, err := tools.PlacementOf(name)
				assert.NoError(t, err, "%s was offered but has no placement to execute at", name)
			}
		})
	}
}
