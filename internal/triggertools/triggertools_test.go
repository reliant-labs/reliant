// Copyright (c) 2025 Reliant Labs
package triggertools_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/grpc/services"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/triggertools"
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
	activate  tools.Tool
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

	svc := services.NewTriggerService(repo, e.backend, e.backend).WithInbound(services.InboundOptions{
		PublicURL: publicURL, Catalog: fakeCatalog{}, Intake: fakeIntake{},
	})
	activator := triggertools.New(svc)
	e.activate = tools.NewActivateTriggerTool(repo, activator)
	e.list = tools.NewListTriggersTool(repo, activator)
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

func TestActivateTriggerSchedule(t *testing.T) {
	e := setup(t, "https://api.example.com")
	resp := e.call(t, e.activate, tools.ActivateTriggerParams{Workflow: "triage", Trigger: "nightly", Message: "Summarize."})
	require.False(t, resp.IsError, resp.Content)

	var meta tools.ActivateTriggerResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.NotEmpty(t, meta.TriggerID)
	assert.Equal(t, "schedule", meta.Kind)
	assert.Empty(t, meta.WebhookToken)
	assert.Contains(t, resp.Content, `Activated "nightly" of workflow triage`)
	assert.Contains(t, resp.Content, "cron 0 9 * * 1-5 (America/New_York)")
	assert.Contains(t, resp.Content, "Next fire: 2026-01-05T14:00:00Z")

	stored, err := e.repo.GetTrigger(context.Background(), meta.TriggerID)
	require.NoError(t, err)
	assert.Equal(t, e.userID, stored.UserID, "the owner is the calling chat's user")
	assert.Equal(t, e.projectID, stored.ProjectID, "project defaults to the caller's")
	assert.Equal(t, e.daemonID, stored.DaemonID, "daemon defaults to the caller's")
	assert.Equal(t, "triage / nightly", stored.Name)
	require.NotNil(t, stored.WorkflowTrigger)
	assert.Equal(t, "nightly", *stored.WorkflowTrigger)
	assert.Equal(t, []string{meta.TriggerID}, e.backend.synced)
}

// A chat with no machine activates a no-machine trigger, as start_run starts a
// no-machine run. Before, the tool sent the caller's (absent) daemon, so
// CreateTrigger refused it with "daemon_id is required" — safe, but it left a
// no-machine agent unable to set up any automation at all.
func TestActivateTriggerFromANoMachineChatCreatesANoMachineTrigger(t *testing.T) {
	e := setup(t, "https://api.example.com")
	ctx := context.Background()
	now := time.Now().UTC()
	wf := "builtin://agent"
	noMachineChat := uuid.NewString()
	require.NoError(t, e.repo.CreateChat(ctx, &db.Chat{ID: noMachineChat, UserID: e.userID, ProjectID: e.projectID,
		Title: "no machine", WorkflowName: &wf, NoMachine: true, CreatedAt: now, UpdatedAt: now, LastActive: now}))
	e.chatID = noMachineChat

	resp := e.call(t, e.activate, tools.ActivateTriggerParams{Workflow: "triage", Trigger: "nightly", Message: "Summarize."})
	require.False(t, resp.IsError, resp.Content)

	var meta tools.ActivateTriggerResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	stored, err := e.repo.GetTrigger(ctx, meta.TriggerID)
	require.NoError(t, err)
	assert.True(t, stored.NoMachine, "a no-machine caller activates a no-machine trigger")
	assert.Empty(t, stored.DaemonID, "and pins no daemon")
}

// Inheriting no-machine never weakens the check: a workflow that needs a
// machine is still refused at activation, with nothing stored.
func TestActivateTriggerFromANoMachineChatRefusesAMachineWorkflow(t *testing.T) {
	e := setup(t, "https://api.example.com")
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, e.repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{ID: uuid.NewString(), UserID: e.userID,
		Name: "shelly", Slug: "shelly", Definition: `name: shelly
triggers:
  - name: nightly
    schedule: {cron: "0 9 * * 1-5"}
entry: [w]
nodes:
  - id: w
    type: create_worktree
    args: {branch: x}
`, Status: db.WorkflowDraftStatusComplete, CreatedAt: now, UpdatedAt: now}))
	wf := "builtin://agent"
	noMachineChat := uuid.NewString()
	require.NoError(t, e.repo.CreateChat(ctx, &db.Chat{ID: noMachineChat, UserID: e.userID, ProjectID: e.projectID,
		Title: "no machine", WorkflowName: &wf, NoMachine: true, CreatedAt: now, UpdatedAt: now, LastActive: now}))
	e.chatID = noMachineChat

	resp := e.call(t, e.activate, tools.ActivateTriggerParams{Workflow: "shelly", Trigger: "nightly", Message: "Go."})
	require.True(t, resp.IsError, "a machine-only workflow must not become a no-machine automation: %s", resp.Content)
	assert.Contains(t, resp.Content, "this workflow needs a machine", "refused by the no-machine check, not incidentally")
	assert.Empty(t, e.backend.synced, "nothing was created")
}

func TestActivateTriggerWebhookReturnsURLAndTokenOnce(t *testing.T) {
	e := setup(t, "https://api.example.com/")
	resp := e.call(t, e.activate, tools.ActivateTriggerParams{Workflow: "triage", Trigger: "hook", Message: "Deploy.", Name: "ci hook"})
	require.False(t, resp.IsError, resp.Content)

	var meta tools.ActivateTriggerResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	assert.Equal(t, "webhook", meta.Kind)
	require.True(t, strings.HasPrefix(meta.WebhookToken, "whk_"), meta.WebhookToken)
	assert.Equal(t, "https://api.example.com/hooks/"+meta.TriggerID, meta.WebhookURL)
	assert.Contains(t, resp.Content, "shown ONCE")
	assert.Contains(t, resp.Content, meta.WebhookToken)
	assert.Contains(t, resp.Content, "https://api.example.com/hooks/"+meta.TriggerID+"/"+meta.WebhookToken)
	assert.Contains(t, resp.Content, "Filter: trigger.payload.body.ok == true")
}

// Without PUBLIC_URL (a worker that was not given it) the URL is a bare path,
// and the response says so rather than inventing a host.
func TestActivateTriggerWebhookWithoutPublicURL(t *testing.T) {
	e := setup(t, "")
	resp := e.call(t, e.activate, tools.ActivateTriggerParams{Workflow: "triage", Trigger: "hook", Message: "Deploy."})
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "does not know its public URL")
}

// The service's verdicts reach the agent verbatim.
func TestActivateTriggerErrors(t *testing.T) {
	e := setup(t, "https://api.example.com")
	resp := e.call(t, e.activate, tools.ActivateTriggerParams{Workflow: "triage", Trigger: "nope", Message: "x"})
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, `does not declare a trigger named "nope" (it declares: nightly, hook)`)

	resp = e.call(t, e.activate, tools.ActivateTriggerParams{Workflow: "triage", Trigger: "hook", Message: "x",
		Params: map[string]any{"issue_number": 3}})
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "params.issue_number")

	resp = e.call(t, e.activate, tools.ActivateTriggerParams{Workflow: "triage", Trigger: "nightly"})
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "message is required")
}

func TestListTriggersScopedToAWorkflowShowsBrokenHealth(t *testing.T) {
	e := setup(t, "https://api.example.com")
	require.False(t, e.call(t, e.activate, tools.ActivateTriggerParams{Workflow: "triage", Trigger: "nightly", Message: "x"}).IsError)
	require.False(t, e.call(t, e.activate, tools.ActivateTriggerParams{Workflow: "triage", Trigger: "hook", Message: "x"}).IsError)

	resp := e.call(t, e.list, tools.ListTriggersParams{Workflow: "triage"})
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "2 trigger(s)")
	assert.Contains(t, resp.Content, `activates "nightly" declared by triage`)
	assert.Contains(t, resp.Content, "fires on: a POST to its webhook URL")

	resp = e.call(t, e.list, tools.ListTriggersParams{Workflow: "other"})
	assert.Contains(t, resp.Content, "No triggers launch other")

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
