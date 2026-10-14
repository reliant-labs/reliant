// Copyright (c) 2025 Reliant Labs
package triggertools_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/handlers/trigger"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/triggertools"
	typedconfig "github.com/reliant-labs/reliant/pkg/config"
)

// The tools end to end: the agent's tool call, the adapter, the real
// TriggerService and the real database. Only the Temporal schedule backend is
// faked, and only to record what it was asked to converge.

const toolWorkflowYAML = `name: triage
inputs:
  issue_number: {type: integer, default: 0}
triggers:
  - name: nightly
    description: Every morning
    schedule: {cron: "0 9 * * 1-5", timezone: America/New_York}
  - name: hook
    webhook: {}
    filter: "trigger.payload.body.ok == true"
    inputs:
      issue_number: "{{ trigger.payload.body.n }}"
  - name: templated
    description: Prompt written by the workflow's author
    webhook: {}
    prompt: "Look at delivery {{ trigger.event_id }}."
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

type fakeBackend struct {
	mu     sync.Mutex
	synced []string
}

func (b *fakeBackend) Sync(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.synced = append(b.synced, id)
	return nil
}
func (b *fakeBackend) Delete(context.Context, string) error { return nil }
func (b *fakeBackend) NextFireAt(context.Context, string) (*time.Time, error) {
	next := time.Date(2026, 1, 5, 14, 0, 0, 0, time.UTC)
	return &next, nil
}
func (b *fakeBackend) StartManualFire(context.Context, string) (string, error) { return "fire", nil }

type fakeIntake struct{}

func (fakeIntake) Accept(context.Context, *core.Trigger, triggers.InboundEvent, triggers.AcceptOptions) (*triggers.AcceptResult, error) {
	return &triggers.AcceptResult{}, nil
}

type fakeCatalog struct{}

func (fakeCatalog) HasInboundSource(string) bool { return true }

type env struct {
	repo      *db.Repo
	svc       *trigger.Service
	list      tools.Tool
	backend   *fakeBackend
	userID    string
	projectID string
	daemonID  string
	chatID    string
}

func setup(t *testing.T, publicURL string) *env {
	t.Helper()
	repo := db.NewTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	e := &env{repo: repo, backend: &fakeBackend{}, userID: uuid.NewString(), projectID: uuid.NewString(),
		daemonID: uuid.NewString(), chatID: uuid.NewString()}
	require.NoError(t, repo.CreateProject(ctx, &db.Project{ID: e.projectID, UserID: e.userID, Name: "P", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now}))
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: e.daemonID, UserID: e.userID}))
	wf := "builtin://agent"
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{ID: e.chatID, UserID: e.userID, ProjectID: e.projectID, Title: "caller",
		WorkflowName: &wf, ActiveDaemonID: &e.daemonID, CreatedAt: now, UpdatedAt: now, LastActive: now}))
	require.NoError(t, repo.UpdateChatActiveDaemon(ctx, e.chatID, &e.daemonID))
	require.NoError(t, repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{ID: uuid.NewString(), UserID: e.userID,
		Name: "triage", Slug: "triage", Definition: toolWorkflowYAML, Status: db.WorkflowDraftStatusComplete,
		CreatedAt: now, UpdatedAt: now}))

	svc, err := trigger.New(trigger.Deps{
		Logger: slog.New(slog.DiscardHandler), Config: &typedconfig.Config{}, Repo: repo,
		Syncer: e.backend, Fires: e.backend,
		PublicURL: publicURL, Catalog: fakeCatalog{}, Intake: fakeIntake{},
	})
	require.NoError(t, err)
	e.svc = svc
	e.list = tools.NewListTriggersTool(repo, triggertools.New(svc))
	return e
}

func (e *env) call(t *testing.T, tool tools.Tool, params any) tools.ToolResponse {
	t.Helper()
	raw, err := json.Marshal(params)
	require.NoError(t, err)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, e.userID)
	resp, err := tool.Run(rctx.NewToolContext(ctx, e.chatID, e.chatID, nil, nil),
		tools.ToolCall{ID: "toolu_1", Name: tool.Name(), Input: string(raw)})
	require.NoError(t, err)
	return resp
}

// activate is the user enabling a declared trigger in the app: the same
// CreateTrigger call the UI makes. No agent tool can do this, which is the
// point of TestNoAgentToolActivates; the list tests need activations to list.
func (e *env) activate(t *testing.T, declared string) string {
	t.Helper()
	enabled := true
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, e.userID)
	resp, err := e.svc.CreateTrigger(ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: &reliantv1.TriggerDefinition{
		Name: "triage / " + declared, ProjectId: e.projectID, Workflow: "triage", DaemonId: e.daemonID,
		Message: "go", Enabled: &enabled,
		Source: &reliantv1.TriggerDefinition_WorkflowTrigger{WorkflowTrigger: declared},
	}}))
	require.NoError(t, err)
	return resp.Msg.GetTrigger().GetId()
}

func TestListTriggersScopedToAWorkflowShowsBrokenHealth(t *testing.T) {
	e := setup(t, "https://api.example.com")
	e.activate(t, "nightly")
	e.activate(t, "hook")

	resp := e.call(t, e.list, tools.ListTriggersParams{Workflow: "triage"})
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "2 trigger(s)")
	assert.Contains(t, resp.Content, `activates "nightly" declared by triage`)
	assert.Contains(t, resp.Content, "fires on: a POST to its webhook URL")
	assert.Contains(t, resp.Content, "webhook: https://api.example.com/hooks/")
	assert.NotContains(t, resp.Content, "whk_", "a webhook token is never shown to an agent")

	resp = e.call(t, e.list, tools.ListTriggersParams{Workflow: "other"})
	assert.Contains(t, resp.Content, "Nothing runs other on its own")
	assert.Contains(t, resp.Content, "Workflows -> the workflow -> Enable", "the agent is told where the user enables it")

	// The workflow drops the webhook declaration: its activation is BROKEN.
	draft, err := e.repo.GetWorkflowDraftBySlug(context.Background(), e.userID, "triage")
	require.NoError(t, err)
	trimmed := toolWorkflowYAML[:strings.Index(toolWorkflowYAML, "  - name: hook")] + toolWorkflowYAML[strings.Index(toolWorkflowYAML, "entry:"):]
	require.NoError(t, e.repo.UpdateWorkflowDraftDefinition(context.Background(), draft.ID, "triage", "triage", trimmed, db.WorkflowDraftStatusComplete))

	resp = e.call(t, e.list, tools.ListTriggersParams{Workflow: "triage"})
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "health BROKEN")
	assert.Contains(t, resp.Content, `no longer declares a trigger named "hook" (it declares: nightly)`)

	var listed []tools.TriggerSummary
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &listed))
	require.Len(t, listed, 2)
}

// No registered tool creates, enables, re-points or deletes an activation, and
// no tool offered to an agent is named for one. This is what "the agent can
// create but not enable workflows" means in the registry: the only trigger
// tool is the read-only list.
func TestNoAgentToolActivates(t *testing.T) {
	var triggerTools []string
	for _, def := range tools.GetToolRegistry() {
		lower := strings.ToLower(def.Name)
		if strings.Contains(lower, "trigger") || strings.Contains(lower, "activat") {
			triggerTools = append(triggerTools, def.Name)
			assert.True(t, slices.Contains(def.Tags, tools.TagReadOnly), "%s must be read-only", def.Name)
		}
	}
	assert.Equal(t, []string{tools.ToolListTriggers}, triggerTools)
}

// triggertools is read-only by construction: its API interface has no write
// method, so the adapter cannot create or change a trigger.
func TestTriggerAPIHasNoWriteMethod(t *testing.T) {
	typ := reflect.TypeOf((*triggertools.TriggerAPI)(nil)).Elem()
	require.Equal(t, 1, typ.NumMethod())
	assert.Equal(t, "ListTriggers", typ.Method(0).Name)
}
