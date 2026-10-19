// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/chatselection"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// startRecorder is a Temporal fake that keeps the inputs of every run the
// service starts, on top of the signal-recording fake.
type startRecorder struct {
	wakeTestTemporalClient
	started []v2.WorkflowInput
}

func (c *startRecorder) ExecuteWorkflow(
	_ context.Context, options client.StartWorkflowOptions, _ interface{}, args ...interface{},
) (client.WorkflowRun, error) {
	if len(args) > 0 {
		if in, ok := args[0].(v2.WorkflowInput); ok {
			c.started = append(c.started, in)
		}
	}
	return &fakeWorkflowRun{id: options.ID, runID: "run-" + options.ID}, nil
}

func (c *startRecorder) lastStart(t *testing.T) v2.WorkflowInput {
	t.Helper()
	require.NotEmpty(t, c.started, "no run was started")
	return c.started[len(c.started)-1]
}

func selectionFixture(t *testing.T, status db.WorkflowStatus) (context.Context, absorbFixture, *db.Repo, *startRecorder, *Service) {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", status)
	connectOnlyAnthropic(t, ctx, repo, "test-user")
	temporal := &startRecorder{wakeTestTemporalClient: wakeTestTemporalClient{
		absorbTestTemporalClient: absorbTestTemporalClient{exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_RUNNING},
	}}
	service := &Service{
		deps: Deps{Repo: repo, Temporal: temporal},
		runs: runs.NewService(repo, temporal, workflow.NewPauseService(temporal, repo)),
	}
	return ctx, fx, repo, temporal, service
}

func setSelection(t *testing.T, ctx context.Context, repo *db.Repo, chatID string, params map[string]any) *chatselection.Selection {
	t.Helper()
	sel, err := chatselection.New(repo).Merge(ctx, chatID, "test-user", "builtin://agent", params, 0)
	require.NoError(t, err)
	return sel
}

func paramsOf(t *testing.T, kv map[string]any) map[string]*structpb.Value {
	t.Helper()
	out := map[string]*structpb.Value{}
	for key, value := range kv {
		v, err := structpb.NewValue(value)
		require.NoError(t, err)
		out[key] = v
	}
	return out
}

func updateParams(chatID string, params map[string]*structpb.Value) *connect.Request[reliantv1.UpdateWorkflowParamsRequest] {
	return connect.NewRequest(&reliantv1.UpdateWorkflowParamsRequest{ChatId: chatID, Params: params})
}

// F1: the pick lived only in one browser profile's localStorage, so a send that
// carried no params (another device, Restart Conversation, the run API) built a
// fresh run from the preset's tier. The run now starts on the chat's selection.
func TestSendMessage_CompletedRunWithNoParamsRunsTheSelectedModel(t *testing.T) {
	ctx, fx, repo, temporal, service := selectionFixture(t, db.Completed())
	setSelection(t, ctx, repo, fx.chatID, map[string]any{
		"model": map[string]any{"id": sonnetOnClaude},
		"mode":  "plan",
	})

	_, err := service.SendMessage(ctx, sendContinue(fx.chatID, nil))
	require.NoError(t, err)

	in := temporal.lastStart(t)
	assert.Equal(t, sonnetOnClaude, modelID(t, in.Inputs["model"]), "the run must use the chat's selected model, not the preset's tier")
	assert.Equal(t, "plan", in.Inputs["mode"])
}

// The control: without a selection the same send runs the workflow's default,
// so the assertion above is the selection's doing.
func TestSendMessage_CompletedRunWithoutASelectionRunsTheDefault(t *testing.T) {
	ctx, fx, _, temporal, service := selectionFixture(t, db.Completed())

	_, err := service.SendMessage(ctx, sendContinue(fx.chatID, nil))
	require.NoError(t, err)

	assert.NotEqual(t, sonnetOnClaude, modelID(t, temporal.lastStart(t).Inputs["model"]))
}

// A send's own params beat the selection key by key, and the pick it carries
// becomes the selection.
func TestSendMessage_ParamsOverrideAndUpdateTheSelection(t *testing.T) {
	ctx, fx, repo, temporal, service := selectionFixture(t, db.Completed())
	setSelection(t, ctx, repo, fx.chatID, map[string]any{
		"model": map[string]any{"id": sonnetOnClaude},
		"mode":  "plan",
	})

	_, err := service.SendMessage(ctx, sendContinue(fx.chatID, paramsOf(t, map[string]any{
		"model": map[string]any{"id": opusOnClaude},
	})))
	require.NoError(t, err)

	in := temporal.lastStart(t)
	assert.Equal(t, opusOnClaude, modelID(t, in.Inputs["model"]))
	assert.Equal(t, "plan", in.Inputs["mode"], "a key the send did not carry keeps the selection's value")

	sel, err := chatselection.New(repo).Get(ctx, fx.chatID)
	require.NoError(t, err)
	assert.Equal(t, opusOnClaude, modelID(t, sel.Params["model"]))
	assert.Equal(t, "plan", sel.Params["mode"])
}

// A selection made for another workflow does not apply to this one.
func TestSendMessage_ASelectionForAnotherWorkflowIsIgnored(t *testing.T) {
	ctx, fx, repo, temporal, service := selectionFixture(t, db.Completed())
	_, err := chatselection.New(repo).Merge(ctx, fx.chatID, "test-user", "builtin://other", map[string]any{
		"model": map[string]any{"id": sonnetOnClaude},
	}, 0)
	require.NoError(t, err)

	_, err = service.SendMessage(ctx, sendContinue(fx.chatID, nil))
	require.NoError(t, err)

	assert.NotEqual(t, sonnetOnClaude, modelID(t, temporal.lastStart(t).Inputs["model"]))
}

// F2(ii): #685 moves an unservable pin and says so. The move is merged into the
// selection, so the composer converges on what runs and the next send — even one
// that still carries the original pin — moves it silently.
func TestSendMessage_ModelMoveIsRecordedAndAnnouncedOnce(t *testing.T) {
	ctx, fx, repo, temporal, service := selectionFixture(t, db.Completed())
	pin := paramsOf(t, map[string]any{"model": map[string]any{"id": codexOnlyModel}})

	_, err := service.SendMessage(ctx, sendContinue(fx.chatID, pin))
	require.NoError(t, err)

	moved := modelID(t, temporal.lastStart(t).Inputs["model"])
	require.True(t, assertAnthropic(moved), "a Claude-only user's codex pin must run on Claude, got %q", moved)
	sel, err := chatselection.New(repo).Get(ctx, fx.chatID)
	require.NoError(t, err)
	assert.Equal(t, moved, modelID(t, sel.Params["model"]), "the selection holds what runs")
	assert.Len(t, noticesNaming(transcriptBodies(t, ctx, repo, fx.chatID), "gpt-5.6-sol"), 1)

	// A stale composer sends the original pin again.
	_, err = service.SendMessage(ctx, sendContinue(fx.chatID, pin))
	require.NoError(t, err)
	assert.Equal(t, moved, modelID(t, temporal.lastStart(t).Inputs["model"]))
	assert.Len(t, noticesNaming(transcriptBodies(t, ctx, repo, fx.chatID), "gpt-5.6-sol"), 1,
		"the same move must not be announced on every message")

	// And a send with no params at all runs the moved model with no notice.
	_, err = service.SendMessage(ctx, sendContinue(fx.chatID, nil))
	require.NoError(t, err)
	assert.Equal(t, moved, modelID(t, temporal.lastStart(t).Inputs["model"]))
	assert.Len(t, noticesNaming(transcriptBodies(t, ctx, repo, fx.chatID), "gpt-5.6-sol"), 1)
}

func assertAnthropic(id string) bool {
	const suffix = "@anthropic"
	return len(id) > len(suffix) && id[len(id)-len(suffix):] == suffix
}

// F3, inverted: a Mode-only edit of a live run signals exactly {mode}. It used
// to carry the preset's model and every schema default, which reset the running
// agent to the preset's tier.
func TestUpdateWorkflowParams_ModeOnlySignalsExactlyMode(t *testing.T) {
	ctx, fx, repo, temporal, service := selectionFixture(t, db.Active())
	setSelection(t, ctx, repo, fx.chatID, map[string]any{"model": map[string]any{"id": sonnetOnClaude}})

	resp, err := service.UpdateWorkflowParams(ctx, updateParams(fx.chatID, paramsOf(t, map[string]any{"mode": "plan"})))
	require.NoError(t, err)

	require.Len(t, temporal.signals, 1)
	assert.Equal(t, workflow.SignalUpdateWorkflowState, temporal.signals[0].Name)
	assert.Equal(t, map[string]interface{}{"mode": "plan"}, temporal.signals[0].Arg,
		"only the key the client sent may reach the run")

	require.NotNil(t, resp.Msg.Selection)
	assert.Equal(t, "plan", resp.Msg.Selection.Params.AsMap()["mode"])
	assert.Equal(t, sonnetOnClaude, modelID(t, resp.Msg.Selection.Params.AsMap()["model"]), "the model survives a mode edit")
}

// F3(b): a sub-agent's edit is that thread's alone — it carries no root preset,
// and it is never the chat's selection.
func TestUpdateWorkflowParams_ThreadScopedCarriesNoPresetAndIsNotStored(t *testing.T) {
	ctx, fx, repo, temporal, service := selectionFixture(t, db.Active())
	chat, err := repo.GetChat(ctx, fx.chatID)
	require.NoError(t, err)
	chat.SelectedPresets = map[string]string{"": "general"}
	require.NoError(t, repo.UpdateChat(ctx, chat))
	before := setSelection(t, ctx, repo, fx.chatID, map[string]any{"model": map[string]any{"id": sonnetOnClaude}})

	thread := fx.rootThreadID
	req := updateParams(fx.chatID, paramsOf(t, map[string]any{"mode": "plan"}))
	req.Msg.ThreadId = &thread
	_, err = service.UpdateWorkflowParams(ctx, req)
	require.NoError(t, err)

	require.Len(t, temporal.signals, 1)
	assert.Equal(t, map[string]interface{}{"mode": "plan", "__thread": thread}, temporal.signals[0].Arg,
		"a thread-scoped update must not pull the root's preset (model, tools, skills) into the thread")

	after, err := chatselection.New(repo).Get(ctx, fx.chatID)
	require.NoError(t, err)
	assert.Equal(t, before.Version, after.Version)
	assert.NotContains(t, after.Params, "mode", "a sub-agent's edit is not the chat's choice")
}

// An edit made when no run is live is still the chat's choice: it is stored for
// the next run, with no signal to send.
func TestUpdateWorkflowParams_WithoutALiveRunStoresTheSelection(t *testing.T) {
	ctx, fx, repo, temporal, service := selectionFixture(t, db.Completed())

	_, err := service.UpdateWorkflowParams(ctx, updateParams(fx.chatID, paramsOf(t, map[string]any{
		"model": map[string]any{"id": sonnetOnClaude},
	})))
	require.NoError(t, err)
	assert.Empty(t, temporal.signals)

	_, err = service.SendMessage(ctx, sendContinue(fx.chatID, nil))
	require.NoError(t, err)
	assert.Equal(t, sonnetOnClaude, modelID(t, temporal.lastStart(t).Inputs["model"]))
	_ = repo
}

// An edit of a live run moves an unservable pin first, as a send does, and
// stores what runs.
func TestUpdateWorkflowParams_LiveRunPinIsMovedAndTheSelectionHoldsTheMove(t *testing.T) {
	ctx, fx, repo, temporal, service := selectionFixture(t, db.Active())

	_, err := service.UpdateWorkflowParams(ctx, updateParams(fx.chatID, paramsOf(t, map[string]any{
		"model": map[string]any{"id": codexOnlyModel},
	})))
	require.NoError(t, err)

	require.Len(t, temporal.signals, 1)
	update := temporal.signals[0].Arg.(map[string]interface{})
	signalled := modelID(t, update["model"])
	assert.True(t, assertAnthropic(signalled), "the live run must not be pinned to a provider that cannot serve it, got %q", signalled)

	sel, err := chatselection.New(repo).Get(ctx, fx.chatID)
	require.NoError(t, err)
	assert.Equal(t, signalled, modelID(t, sel.Params["model"]))
}

// Optimistic concurrency: an edit made against a selection that has since
// changed is refused with ABORTED, and the stored selection is untouched.
func TestUpdateWorkflowParams_StaleVersionIsAborted(t *testing.T) {
	ctx, fx, repo, _, service := selectionFixture(t, db.Completed())
	first := setSelection(t, ctx, repo, fx.chatID, map[string]any{"mode": "auto"})
	setSelection(t, ctx, repo, fx.chatID, map[string]any{"mode": "plan"})

	req := updateParams(fx.chatID, paramsOf(t, map[string]any{"mode": "manual"}))
	stale := first.Version
	req.Msg.ExpectedVersion = &stale
	_, err := service.UpdateWorkflowParams(ctx, req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeAborted, connect.CodeOf(err))

	got, err := chatselection.New(repo).Get(ctx, fx.chatID)
	require.NoError(t, err)
	assert.Equal(t, "plan", got.Params["mode"])

	// With the current version the same edit lands.
	current := got.Version
	req.Msg.ExpectedVersion = &current
	resp, err := service.UpdateWorkflowParams(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, "manual", resp.Msg.Selection.Params.AsMap()["mode"])
	assert.Greater(t, resp.Msg.Selection.Version, current)
}

// The provider policy is stored and returned, not acted on.
func TestUpdateChatProviderPolicy_RoundTripsThroughGetChat(t *testing.T) {
	ctx, fx, repo, _, service := selectionFixture(t, db.Completed())
	setSelection(t, ctx, repo, fx.chatID, map[string]any{"model": map[string]any{"id": sonnetOnClaude}})
	fallback := codexOnlyModel

	resp, err := service.UpdateChatProviderPolicy(ctx, connect.NewRequest(&reliantv1.UpdateChatProviderPolicyRequest{
		ChatId: fx.chatID,
		ProviderPolicy: &reliantv1.ProviderPolicy{
			OnLimit:       reliantv1.ProviderPolicy_ON_LIMIT_FALLBACK,
			FallbackModel: &fallback,
		},
	}))
	require.NoError(t, err)
	assert.Equal(t, reliantv1.ProviderPolicy_ON_LIMIT_FALLBACK, resp.Msg.Selection.ProviderPolicy.OnLimit)

	got, err := service.GetChat(ctx, connect.NewRequest(&reliantv1.GetChatRequest{ChatId: fx.chatID}))
	require.NoError(t, err)
	sel := got.Msg.Chat.Selection
	require.NotNil(t, sel)
	assert.Equal(t, reliantv1.ProviderPolicy_ON_LIMIT_FALLBACK, sel.ProviderPolicy.OnLimit)
	assert.Equal(t, codexOnlyModel, sel.ProviderPolicy.GetFallbackModel())
	assert.Equal(t, sonnetOnClaude, modelID(t, sel.Params.AsMap()["model"]), "setting the policy must not disturb the params")
	assert.Equal(t, "builtin://agent", sel.WorkflowName)
}

func TestUpdateChatProviderPolicy_RefusesNonsenseAndStaleVersions(t *testing.T) {
	ctx, fx, repo, _, service := selectionFixture(t, db.Completed())
	first := setSelection(t, ctx, repo, fx.chatID, map[string]any{"mode": "auto"})
	setSelection(t, ctx, repo, fx.chatID, map[string]any{"mode": "plan"})

	_, err := service.UpdateChatProviderPolicy(ctx, connect.NewRequest(&reliantv1.UpdateChatProviderPolicyRequest{
		ChatId:         fx.chatID,
		ProviderPolicy: &reliantv1.ProviderPolicy{OnLimit: reliantv1.ProviderPolicy_ON_LIMIT_FALLBACK},
	}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "a fallback with no model is not a policy")

	stale := first.Version
	_, err = service.UpdateChatProviderPolicy(ctx, connect.NewRequest(&reliantv1.UpdateChatProviderPolicyRequest{
		ChatId:          fx.chatID,
		ProviderPolicy:  &reliantv1.ProviderPolicy{OnLimit: reliantv1.ProviderPolicy_ON_LIMIT_WAIT},
		ExpectedVersion: &stale,
	}))
	assert.Equal(t, connect.CodeAborted, connect.CodeOf(err))
}

// A second device opening the chat reads the same selection from the server.
func TestGetChat_CarriesTheSelectionToAnyDevice(t *testing.T) {
	ctx, fx, repo, _, service := selectionFixture(t, db.Completed())

	none, err := service.GetChat(ctx, connect.NewRequest(&reliantv1.GetChatRequest{ChatId: fx.chatID}))
	require.NoError(t, err)
	assert.Nil(t, none.Msg.Chat.Selection, "a chat nobody has configured has no selection to seed from")

	setSelection(t, ctx, repo, fx.chatID, map[string]any{"model": map[string]any{"id": sonnetOnClaude}})
	got, err := service.GetChat(ctx, connect.NewRequest(&reliantv1.GetChatRequest{ChatId: fx.chatID}))
	require.NoError(t, err)
	require.NotNil(t, got.Msg.Chat.Selection)
	assert.Equal(t, sonnetOnClaude, modelID(t, got.Msg.Chat.Selection.Params.AsMap()["model"]))
	assert.EqualValues(t, 1, got.Msg.Chat.Selection.Version)
}

func branchTestContext(t *testing.T, repo *db.Repo) (context.Context, string) {
	t.Helper()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()
	projectID := "test-project-selection-branch-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "Selection Branch", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	return ctx, projectID
}

// F4: a branch starts on the chat it was branched from — its selection and its
// presets. Both the in-place and the to-workspace paths are the one BranchChat.
func TestBranchChat_CopiesTheSelectionAndPresets(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, projectID := branchTestContext(t, repo)
	sourceChatID, messageID := setupBranchAtomicitySource(t, repo, ctx, projectID)

	source, err := repo.GetChat(ctx, sourceChatID)
	require.NoError(t, err)
	source.SelectedPresets = map[string]string{"": "general"}
	require.NoError(t, repo.UpdateChat(ctx, source))
	policy := chatselection.ProviderPolicy{OnLimit: chatselection.OnLimitWait}
	svc := chatselection.New(repo)
	_, err = svc.Merge(ctx, sourceChatID, "test-user", "builtin://agent", map[string]any{
		"model": map[string]any{"id": "gpt-5.6-sol@codex", "thinking_level": "high"},
	}, 0)
	require.NoError(t, err)
	_, err = svc.SetPolicy(ctx, sourceChatID, "test-user", "builtin://agent", policy, 0)
	require.NoError(t, err)

	service := &Service{deps: Deps{Repo: repo}, threads: threads.NewService(repo)}
	resp, err := service.BranchChat(ctx, connect.NewRequest(&reliantv1.BranchChatRequest{ChatId: sourceChatID, MessageId: messageID}))
	require.NoError(t, err)

	branch := resp.Msg.Chat
	assert.Equal(t, map[string]string{"": "general"}, branch.SelectedPresets)
	require.NotNil(t, branch.Selection, "the branch response carries the selection the composer seeds from")
	assert.Equal(t, map[string]any{"id": "gpt-5.6-sol@codex", "thinking_level": "high"}, branch.Selection.Params.AsMap()["model"])
	assert.Equal(t, reliantv1.ProviderPolicy_ON_LIMIT_WAIT, branch.Selection.ProviderPolicy.OnLimit)

	// The branch owns its copy.
	_, err = svc.Merge(ctx, branch.Id, "test-user", "builtin://agent", map[string]any{"mode": "plan"}, 0)
	require.NoError(t, err)
	src, err := svc.Get(ctx, sourceChatID)
	require.NoError(t, err)
	assert.NotContains(t, src.Params, "mode")
}
