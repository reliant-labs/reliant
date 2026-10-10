// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/workflow"
)

// Two #688 gaps in how a send's model reaches a run it resumes. Both run the
// real PauseService against a Temporal fake that records signals, so they pin
// what the run is actually sent.
//
// (a) Prod chat 97654413 paused on gpt-5.6-sol@codex for a user with no Codex.
// #685 moves such a model to a connected provider — but only a model the send
// CARRIES. A "continue" from another device, or after a reload, carries no
// model at all, so the run resumed on the same pin and failed the same way.
//
// (b) A failed run is resumed by reset-and-replay, and the replayed run starts
// from the inputs its history recorded. The send's params were validated and
// then dropped, so a model the user had just picked never reached it.

const (
	codexOnlyModel = "gpt-5.6-sol@codex"
	opusOnClaude   = "claude-5.5-opus@anthropic"
	sonnetOnClaude = "claude-5.5-sonnet@anthropic"
)

// carryTemporalClient answers get_workflow_inputs with runInputs, serves the
// one-event history a reset-and-replay needs (a completed workflow task to
// reset to), and records every signal (wakeTestTemporalClient).
type carryTemporalClient struct {
	wakeTestTemporalClient
	runInputs map[string]interface{}
}

func (c *carryTemporalClient) QueryWorkflow(
	_ context.Context, _, _, queryType string, _ ...interface{},
) (converter.EncodedValue, error) {
	if queryType != "get_workflow_inputs" || c.runInputs == nil {
		return nil, errors.New("query not answered")
	}
	return encodedInputs{c.runInputs}, nil
}

func (c *carryTemporalClient) GetWorkflowHistory(
	context.Context, string, string, bool, enums.HistoryEventFilterType,
) client.HistoryEventIterator {
	return &closeEventIterator{event: &historypb.HistoryEvent{
		EventId:   4,
		EventType: enums.EVENT_TYPE_WORKFLOW_TASK_COMPLETED,
		EventTime: timestamppb.Now(),
	}}
}

type encodedInputs struct{ inputs map[string]interface{} }

func (v encodedInputs) HasValue() bool { return v.inputs != nil }

func (v encodedInputs) Get(valuePtr interface{}) error {
	raw, err := json.Marshal(v.inputs)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, valuePtr)
}

// carryFixture is a chat whose run is in dbStatus (Temporal: temporalStatus)
// and holds model, for a user whose only connected provider is Claude.
func carryFixture(t *testing.T, dbStatus db.WorkflowStatus, temporalStatus enums.WorkflowExecutionStatus, model string) (context.Context, absorbFixture, *db.Repo, *carryTemporalClient, *ChatService) {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", dbStatus)
	connectOnlyAnthropic(t, ctx, repo, "test-user")
	temporal := &carryTemporalClient{
		wakeTestTemporalClient: wakeTestTemporalClient{
			absorbTestTemporalClient: absorbTestTemporalClient{exists: true, status: temporalStatus},
		},
		runInputs: map[string]interface{}{
			"model": map[string]interface{}{"id": model},
			"mode":  "auto",
		},
	}
	service := &ChatService{
		database:   repo,
		tempClient: temporal,
		runs:       runs.NewService(repo, temporal, workflow.NewPauseService(temporal, repo)),
	}
	return ctx, fx, repo, temporal, service
}

func sendContinue(chatID string, params map[string]*structpb.Value) *connect.Request[reliantv1.SendMessageRequest] {
	return connect.NewRequest(&reliantv1.SendMessageRequest{
		ChatId:         chatID,
		Messages:       []*reliantv1.InputMessage{{Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Content: "continue"}},
		WorkflowParams: params,
	})
}

// modelUpdateBeforeResume returns the model id the run was sent in an input
// update ahead of its resume, failing the test if there was none or it came
// after the resume.
func modelUpdateBeforeResume(t *testing.T, signals []recordedSignal) string {
	t.Helper()
	update, resume := -1, -1
	for i, sig := range signals {
		switch sig.name {
		case workflow.SignalUpdateWorkflowState:
			if update == -1 {
				update = i
			}
		case workflow.SignalResume:
			if resume == -1 {
				resume = i
			}
		}
	}
	require.NotEqual(t, -1, resume, "the run must be resumed; signals: %v", signalNames(signals))
	require.NotEqual(t, -1, update, "the run must be sent an input update; signals: %v", signalNames(signals))
	require.Less(t, update, resume,
		"the update must reach the run before its resume: the woken step re-dispatches from its live inputs")
	inputs, ok := signals[update].arg.(map[string]interface{})
	require.True(t, ok, "update payload %T", signals[update].arg)
	return modelID(t, inputs["model"])
}

func modelID(t *testing.T, value interface{}) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	var selector struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(raw, &selector), "model input %s", raw)
	return selector.ID
}

func signalNames(signals []recordedSignal) []string {
	names := make([]string, len(signals))
	for i, sig := range signals {
		names[i] = sig.name
	}
	return names
}

func noticesNaming(bodies []string, model string) []string {
	var out []string
	for _, body := range bodies {
		if strings.Contains(body, model) && strings.Contains(body, "Settings") {
			out = append(out, body)
		}
	}
	return out
}

// (a) A paused run's own unservable pin moves, though the send carries no model.
func TestSendMessage_PausedRunsUnservablePinMovesWithoutAModelParam(t *testing.T) {
	ctx, fx, repo, temporal, service := carryFixture(t, db.Paused(), enums.WORKFLOW_EXECUTION_STATUS_RUNNING, codexOnlyModel)

	_, err := service.SendMessage(ctx, sendContinue(fx.chatID, nil))
	require.NoError(t, err)

	moved := modelUpdateBeforeResume(t, temporal.signals)
	assert.True(t, strings.HasSuffix(moved, "@anthropic"),
		"a run pinned on %s for a Claude-only user must resume on Claude, got %q", codexOnlyModel, moved)
	assert.Len(t, noticesNaming(transcriptBodies(t, ctx, repo, fx.chatID), "gpt-5.6-sol"), 1,
		"the chat says the model moved, as #685's send-carried move does")
}

// A pin the user's providers serve is left alone: nothing is sent and nothing
// is said.
func TestSendMessage_PausedRunsServablePinIsLeftAlone(t *testing.T) {
	ctx, fx, repo, temporal, service := carryFixture(t, db.Paused(), enums.WORKFLOW_EXECUTION_STATUS_RUNNING, sonnetOnClaude)

	_, err := service.SendMessage(ctx, sendContinue(fx.chatID, nil))
	require.NoError(t, err)

	assert.NotContains(t, signalNames(temporal.signals), workflow.SignalUpdateWorkflowState)
	assert.Contains(t, signalNames(temporal.signals), workflow.SignalResume)
	assert.Empty(t, noticesNaming(transcriptBodies(t, ctx, repo, fx.chatID), "claude-5.5-sonnet"))
}

// (b) A failed run's reset-and-replay resume carries the model the send chose.
func TestSendMessage_FailedRunsReplayGetsTheModelTheSendChose(t *testing.T) {
	ctx, fx, _, temporal, service := carryFixture(t, db.Failed(), enums.WORKFLOW_EXECUTION_STATUS_TERMINATED, sonnetOnClaude)

	_, err := service.SendMessage(ctx, sendContinue(fx.chatID, map[string]*structpb.Value{
		"model": mustStructValue(t, map[string]interface{}{"id": opusOnClaude}),
	}))
	require.NoError(t, err)

	assert.Equal(t, opusOnClaude, modelUpdateBeforeResume(t, temporal.signals),
		"the replayed run starts from its recorded inputs; the user's new pick must be sent to it")
}

// (a) on a failed run: its replay moves off the unservable pin too.
func TestSendMessage_FailedRunsUnservablePinMovesWithoutAModelParam(t *testing.T) {
	ctx, fx, repo, temporal, service := carryFixture(t, db.Failed(), enums.WORKFLOW_EXECUTION_STATUS_TERMINATED, codexOnlyModel)

	_, err := service.SendMessage(ctx, sendContinue(fx.chatID, nil))
	require.NoError(t, err)

	moved := modelUpdateBeforeResume(t, temporal.signals)
	assert.True(t, strings.HasSuffix(moved, "@anthropic"), "got %q", moved)
	assert.Len(t, noticesNaming(transcriptBodies(t, ctx, repo, fx.chatID), "gpt-5.6-sol"), 1)
}
