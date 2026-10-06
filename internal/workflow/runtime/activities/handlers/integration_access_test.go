// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// ownerConnections stands in for the worker's credential source: it answers
// which integrations the run's owner has connected, and records which run it
// was asked about. It never hands out a credential; the menu is under test,
// not a call.
type ownerConnections struct {
	mu     sync.Mutex
	usable map[string]bool
	err    error
	runs   []string
	asked  []string
}

func (c *ownerConnections) Credential(context.Context, httpaction.CredentialRequest) (httpaction.Credential, error) {
	return nil, errors.New("ownerConnections resolves no credentials")
}

func (c *ownerConnections) UsableIntegrations(_ context.Context, runID string, integrations map[string]*reliantv1.ConnectionSpec) (map[string]bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runs = append(c.runs, runID)
	out := map[string]bool{}
	for id := range integrations {
		c.asked = append(c.asked, id)
		if c.usable[id] {
			out[id] = true
		}
	}
	return out, c.err
}

// promptCaptureDriver records the system prompts as well as the tools.
type promptCaptureDriver struct {
	toolCaptureMockDriver
	prompts []string
}

func (d *promptCaptureDriver) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, available []tools.Tool) <-chan llm.DriverEvent {
	d.prompts = append([]string(nil), prompts...)
	return d.toolCaptureMockDriver.StreamResponse(ctx, prompts, messages, available)
}

type integrationFixture struct {
	h       *IdempotencyTestHelper
	project *db.Project
	chat    *db.Chat
	driver  *promptCaptureDriver
	conns   *ownerConnections
	callLLM *CallLLMActivity
	// caps is the capability set the last offeredTools turn recorded.
	caps *tools.Capabilities
}

func setupIntegrationFixture(t *testing.T, noMachine bool, conns *ownerConnections) *integrationFixture {
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

	driver := &promptCaptureDriver{}
	resolver := func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
		return driver, nil
	}
	var source httpaction.CredentialSource
	if conns != nil {
		source = conns
	}
	factory := tools.NewToolsFactory(&tools.ToolsOptions{Repo: h.Repo(), IntegrationCredentials: source})
	return &integrationFixture{
		h: h, project: project, chat: chat, driver: driver, conns: conns,
		callLLM: NewCallLLMActivity(h.Repo(), nil, factory, &staticConfigProvider{}, resolver, nil),
	}
}

func (f *integrationFixture) offeredTools(t *testing.T, preloaded, loadable []string) []string {
	t.Helper()
	cfg := &reliantv1.ToolsConfig{PreloadedTools: celStringListLiteral(preloaded)}
	if loadable != nil {
		cfg.LoadableTools = celStringListLiteral(loadable)
	}
	var output CallLLMOutput
	require.NoError(t, f.h.ExecuteActivity(f.callLLM.Execute, ActivityInput{
		Runtime: RuntimeContext{ChatID: f.chat.ID, Thread: f.chat.ID},
		Node: &reliantv1.Node{Type: "call_llm", Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{
			Model:       &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Literal{Literal: &reliantv1.ModelSelector{Id: "mock-model"}}},
			ToolsConfig: cfg,
		}}},
	}, &output))
	f.caps = tools.CapabilitiesFromProto(output.GetCapabilities())
	return append([]string(nil), f.driver.capturedTools...)
}

// canLoad reads load_tool's reach from the capability set the last turn
// recorded — what execute_tools hands load_tool on whichever worker it runs.
func (f *integrationFixture) canLoad(name string) bool {
	return f.caps.CanLoad(name)
}

// The withholding itself, without a database: gated tools of an integration
// the owner cannot use are neither offered nor loadable, an explicit list just
// loses them, and "load anything" stays exactly that — the resolver filters
// per name, so nothing has to be expanded into a list.
func TestUnusableIntegrationsAreWithheldByTheResolver(t *testing.T) {
	resolve := func(access tools.ToolAccess, usable map[string]bool) *tools.Capabilities {
		return tools.ResolveCapabilities(tools.CapabilityInputs{Access: access, Permission: tools.PermissionMutating,
			MCPTools: []string{"mcp__srv__probe"}, UsableIntegrations: usable})
	}

	everything := map[string]bool{"github": true, "slack": true, "gmail": true, "twilio": true}
	caps := resolve(tools.ToolAccess{LoadableAll: true, Preloaded: []string{"github__issue_get", tools.ToolFetch}}, everything)
	assert.True(t, caps.LoadableAll, "nothing withheld: still unrestricted")
	assert.Empty(t, caps.WithheldIntegrations)
	assert.True(t, caps.Offers("github__issue_get"))
	assert.True(t, caps.Offers(tools.ToolFetch))

	onlyGitHub := map[string]bool{"github": true}
	caps = resolve(tools.ToolAccess{LoadableAll: true, Preloaded: []string{"slack__message_post", "http__request"}}, onlyGitHub)
	assert.True(t, caps.LoadableAll, "withholding Slack does not turn \"*\" into a list")
	assert.Empty(t, caps.Loadable)
	assert.False(t, caps.Offers("slack__message_post"))
	assert.True(t, caps.Offers("http__request"))
	assert.False(t, caps.CanLoad("slack__message_post"))
	assert.True(t, caps.CanLoad("github__issue_get"))
	assert.True(t, caps.CanLoad(tools.ToolFetch))
	assert.True(t, caps.CanLoad("mcp__srv__probe"), "the connected MCP tools stay loadable")

	caps = resolve(tools.ToolAccess{Loadable: []string{"github__issue_get", "slack__message_post", tools.ToolFetch}}, nil)
	assert.True(t, caps.CanLoad(tools.ToolFetch), "an explicit list just loses the gated names")
	assert.False(t, caps.CanLoad("github__issue_get"))
	assert.False(t, caps.CanLoad("slack__message_post"))
}

// An integration that needs a connection is offered to an owner who has one:
// handed to the model when the workflow preloads it, and reachable through
// load_tool when the workflow lets it load anything. Before, every such tool
// was withheld from every agent because the registry was built owner-blind,
// so GitHub only ever worked as a workflow action node.
func TestCallLLM_OffersAConnectedIntegrationToItsOwner(t *testing.T) {
	conns := &ownerConnections{usable: map[string]bool{"github": true}}
	f := setupIntegrationFixture(t, false, conns)

	offered := f.offeredTools(t, []string{"github__issue_get", tools.ToolFetch}, []string{"*"})

	assert.Contains(t, offered, "github__issue_get", "the owner has GitHub, so the preloaded GitHub tool is offered")
	assert.Contains(t, offered, tools.ToolFetch)
	assert.True(t, f.canLoad("github__pr_get"), "load_tool may reach the rest of GitHub")
	require.NotEmpty(t, conns.runs, "availability is asked of the credential source")
	assert.Equal(t, f.chat.ID, conns.runs[0], "asked for the run the tools would execute in, as the tool resolves it")
}

// Without a connection the same declaration offers no GitHub tool and leaves
// none loadable, while an integration that needs no connection is unaffected.
func TestCallLLM_WithholdsAnIntegrationTheOwnerHasNotConnected(t *testing.T) {
	f := setupIntegrationFixture(t, false, &ownerConnections{usable: map[string]bool{}})

	offered := f.offeredTools(t, []string{"tag:integration"}, []string{"*"})

	for _, name := range offered {
		assert.False(t, strings.HasPrefix(name, "github__"), "offered %s without a GitHub connection", name)
	}
	assert.Contains(t, offered, "http__request", "an integration that needs no connection is still offered")
	assert.False(t, f.canLoad("github__issue_get"), "nor may load_tool reach it")
	assert.True(t, f.canLoad(tools.ToolGenerateImage), "the rest of the registry stays loadable")
}

// tag:integration keeps its meaning: every integration tool the owner can
// use, and nothing else.
func TestCallLLM_TagIntegrationExpandsToTheOwnersUsableIntegrations(t *testing.T) {
	f := setupIntegrationFixture(t, false, &ownerConnections{usable: map[string]bool{"github": true}})

	offered := f.offeredTools(t, []string{"tag:integration"}, nil)

	assert.Contains(t, offered, "github__issue_get")
	assert.Contains(t, offered, "http__request")
	for _, name := range offered {
		assert.False(t, strings.HasPrefix(name, "slack__"), "offered %s without a Slack connection", name)
	}
}

// A run whose owner the credential source cannot establish is offered
// nothing that needs a connection: the source answers with an error and no
// integration, and the menu fails closed.
func TestCallLLM_WithholdsConnectionToolsWhenTheOwnerIsUnknown(t *testing.T) {
	conns := &ownerConnections{usable: map[string]bool{}, err: errors.New("run has no owner")}
	f := setupIntegrationFixture(t, false, conns)

	offered := f.offeredTools(t, []string{"tag:integration"}, []string{"*"})

	for _, name := range offered {
		assert.False(t, strings.HasPrefix(name, "github__"), "offered %s for a run with no known owner", name)
	}
	assert.False(t, f.canLoad("github__issue_get"))
}

// A process with no credential source at all (the daemon runtime, a test)
// offers nothing that needs a connection, exactly as before.
func TestCallLLM_WithholdsConnectionToolsWithoutACredentialSource(t *testing.T) {
	f := setupIntegrationFixture(t, false, nil)

	offered := f.offeredTools(t, []string{"tag:integration"}, nil)

	assert.Contains(t, offered, "http__request")
	for _, name := range offered {
		assert.False(t, strings.HasPrefix(name, "github__"), "offered %s with no credential source", name)
	}
}

// A node that cannot reach any integration tool never asks: availability is
// a database (and, for GitHub, a control-plane) round trip.
func TestCallLLM_DoesNotAskAboutIntegrationsANodeCannotReach(t *testing.T) {
	conns := &ownerConnections{usable: map[string]bool{"github": true}}
	f := setupIntegrationFixture(t, false, conns)

	f.offeredTools(t, []string{tools.ToolFetch}, nil)

	assert.Empty(t, conns.runs)
}

// The GitHub read tools are exactly what a no-machine run may use, so an
// owner with GitHub gets them there too.
func TestCallLLM_NoMachineRunIsOfferedTheOwnersGitHubTools(t *testing.T) {
	f := setupIntegrationFixture(t, true, &ownerConnections{usable: map[string]bool{"github": true}})

	offered := f.offeredTools(t, []string{"github__issue_get"}, []string{"*"})

	assert.Contains(t, offered, "github__issue_get")
	assert.True(t, f.canLoad("github__pr_list_files"))
	assert.False(t, f.canLoad(tools.ShellToolName), "the no-machine narrowing still applies")
}
