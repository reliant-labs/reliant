// Copyright (c) 2026 Reliant Labs
package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.temporal.io/sdk/activity"

	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	activitytypes "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// A run step's command is one run.exec span under the node's span, naming the
// command by hash, length and program, never by its text or its output.
// These tests swap the global tracer provider, so none runs in parallel.

const (
	spanSecretCommand = "./scripts/rc.sh --token s3cr3t-in-the-command"
	spanSecretOutput  = "output-that-must-not-leak"
	spanOrgID         = "org-1"
)

// runStepSpan runs attempt of a run step of spanSecretCommand over executor,
// inside a parent span standing in for the node's, and returns its one
// run.exec span (checked to be that parent's child and to carry no command
// text or output) with the step's error.
func runStepSpan(t *testing.T, act *ExecuteRunStepActivity, chatID string, attempt int32) (sdktrace.ReadOnlySpan, error) {
	t.Helper()
	rec := recordHandlerSpans(t)
	env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
	var parentID string
	env.RegisterActivityWithOptions(func(ctx context.Context, in ExecuteRunStepInput) (ExecuteRunStepOutput, error) {
		ctx = telemetry.WithRunScope(ctx, telemetry.RunScope{OrgID: spanOrgID, ChatID: chatID, RunID: "run-1", NodeID: "gate", NodeType: "run"})
		ctx, parent := otel.Tracer("test").Start(ctx, "workflow.node gate")
		defer parent.End()
		parentID = parent.SpanContext().SpanID().String()
		return act.execute(ctx, in, attempt, stepRequestID)
	}, activity.RegisterOptions{Name: "RunStepSpan"})
	_, err := env.ExecuteActivity("RunStepSpan", ExecuteRunStepInput{
		WorkflowID: "wf-1", ChatID: chatID, StepID: "gate", Command: spanSecretCommand,
	})

	var found []sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if strings.HasPrefix(s.Name(), telemetry.SpanNameRunExec) {
			found = append(found, s)
		}
	}
	require.Len(t, found, 1, "one run.exec span per command")
	span := found[0]
	assert.Equal(t, "run.exec gate", span.Name())
	assert.Equal(t, parentID, span.Parent().SpanID().String(), "a child of the node's span")
	for _, kv := range span.Attributes() {
		v := kv.Value.Emit()
		for _, secret := range []string{"s3cr3t", "--token", spanSecretOutput} {
			assert.NotContains(t, v, secret, "attribute %s carries the command or its output", kv.Key)
		}
	}
	assert.Empty(t, span.Events(), "no error event: its text can carry the machine's detail")
	return span, err
}

func TestRunExecSpan_Success(t *testing.T) {
	act, chatID := newRunStepOn(t, &mockRunExecutor{responses: []mockRunResponse{{stdout: spanSecretOutput}}})

	span, err := runStepSpan(t, act, chatID, 1)

	require.NoError(t, err)
	attrs := attrsOf(span)
	assert.Equal(t, "ok", attrs[telemetry.AttrRunOutcome])
	assert.Equal(t, "0", attrs[telemetry.AttrProcessExitCode])
	assert.Equal(t, "rc.sh", attrs[telemetry.AttrRunCommandProgram])
	sum := sha256.Sum256([]byte(spanSecretCommand))
	assert.Equal(t, hex.EncodeToString(sum[:16]), attrs[telemetry.AttrRunCommandSHA256],
		"the hash of the command the step sent, its expressions evaluated")
	assert.Equal(t, strconv.Itoa(len(spanSecretCommand)), attrs[telemetry.AttrRunCommandBytes])
	assert.Equal(t, "1", attrs[telemetry.AttrRunAttempt])
	assert.Equal(t, "false", attrs[telemetry.AttrRunReattached])
	assert.Equal(t, "false", attrs[telemetry.AttrRunTruncated])
	assert.Equal(t, "inherit", attrs[telemetry.AttrRunCredentials])
	assert.Equal(t, "false", attrs[telemetry.AttrRunWorktree])
	assert.Equal(t, "run-1", attrs[telemetry.AttrAIRunID], "the run scope's attributes")
	assert.Equal(t, spanOrgID, attrs[telemetry.AttrOrgID], "every span carries its org")
	assert.Equal(t, chatID, attrs[telemetry.AttrGenAIConversationID], "the chat, as the gen_ai conversation")
	assert.Contains(t, attrs, attribute.Key(telemetry.AttrRunDurationMs))
	assert.NotContains(t, attrs, attribute.Key(telemetry.AttrErrorType))
	assert.Equal(t, codes.Unset, span.Status().Code)
}

// A nonzero exit is the command's verdict: error.type says so, the status is
// left alone.
func TestRunExecSpan_NonzeroExit(t *testing.T) {
	act, chatID := newRunStepOn(t, &mockRunExecutor{responses: []mockRunResponse{{stderr: spanSecretOutput, exitCode: 2}}})

	span, err := runStepSpan(t, act, chatID, 1)

	require.NoError(t, err)
	attrs := attrsOf(span)
	assert.Equal(t, "nonzero_exit", attrs[telemetry.AttrRunOutcome])
	assert.Equal(t, "nonzero_exit", attrs[telemetry.AttrErrorType])
	assert.Equal(t, "2", attrs[telemetry.AttrProcessExitCode])
	assert.Equal(t, codes.Unset, span.Status().Code)
}

func TestRunExecSpan_Truncated(t *testing.T) {
	act, chatID := newRunStepOn(t, &mockRunExecutor{responses: []mockRunResponse{{stdout: spanSecretOutput, truncated: true}}})

	span, err := runStepSpan(t, act, chatID, 1)

	require.NoError(t, err)
	assert.Equal(t, "true", attrsOf(span)[telemetry.AttrRunTruncated])
}

// A command that never reached its machine failed to run at all: the error
// status, the machine-unavailable outcome, the transport's code, no exit code.
func TestRunExecSpan_MachineUnavailable(t *testing.T) {
	clock := newFakeClock()
	daemon := &blippingDaemon{clock: clock, removed: true}
	act, chatID := newRunStepOn(t, daemon)
	act = act.WithMachineObserver(daemon)
	wait := defaultMachineWait()
	wait.now, wait.sleep = clock.Now, clock.Sleep
	act.machineWait = wait

	span, err := runStepSpan(t, act, chatID, 1)

	requireNonRetryable(t, err, activitytypes.RunStepMachineUnavailableErrType)
	attrs := attrsOf(span)
	assert.Equal(t, "machine_unavailable", attrs[telemetry.AttrRunOutcome])
	assert.Equal(t, "machine_unavailable", attrs[telemetry.AttrErrorType])
	assert.NotEmpty(t, attrs[telemetry.AttrRunTransportCode])
	assert.NotContains(t, attrs, attribute.Key(telemetry.AttrProcessExitCode), "-1 is not the command's exit status")
	assert.Equal(t, codes.Error, span.Status().Code)
}

// A retry the machine cannot vouch for is refused, and says so.
func TestRunExecSpan_RetryRefused(t *testing.T) {
	m := newFakeMachine()
	m.forgets = true
	act, chatID := newRunStepOn(t, m)

	span, err := runStepSpan(t, act, chatID, 2)

	requireNonRetryable(t, err, runStepRetryRefusedErrType)
	attrs := attrsOf(span)
	assert.Equal(t, "retry_refused", attrs[telemetry.AttrRunOutcome])
	assert.Equal(t, "2", attrs[telemetry.AttrRunAttempt])
	assert.Equal(t, codes.Error, span.Status().Code)
	assert.Empty(t, m.ran(), "the command was not run again")
}

// A retry that reattaches reports the command's own verdict.
func TestRunExecSpan_RetryReattached(t *testing.T) {
	m := newFakeMachine()
	m.commands[stepRequestID] = &fakeCommand{done: make(chan struct{}), result: CommandResult{ExitCode: 0}}
	close(m.commands[stepRequestID].done)
	act, chatID := newRunStepOn(t, m)

	span, err := runStepSpan(t, act, chatID, 2)

	require.NoError(t, err)
	attrs := attrsOf(span)
	assert.Equal(t, "ok", attrs[telemetry.AttrRunOutcome])
	assert.Equal(t, "true", attrs[telemetry.AttrRunReattached])
}

// An attempt whose transport dropped, and that reattached to the command it
// sent, reports that command's verdict and says it reattached.
func TestRunExecSpan_TransportDropReattached(t *testing.T) {
	m := newFakeMachine()
	m.dropOnly = true
	m.finishOnSend = &CommandResult{Stdout: spanSecretOutput, ExitCode: 3}
	act, chatID := newRunStepOn(t, m)

	span, err := runStepSpan(t, act, chatID, 1)

	require.NoError(t, err)
	attrs := attrsOf(span)
	assert.Equal(t, "nonzero_exit", attrs[telemetry.AttrRunOutcome])
	assert.Equal(t, "3", attrs[telemetry.AttrProcessExitCode], "the reattached command's exit code")
	assert.Equal(t, "1", attrs[telemetry.AttrRunAttempt])
	assert.Equal(t, "true", attrs[telemetry.AttrRunReattached])
	assert.NotContains(t, attrs, attribute.Key(telemetry.AttrRunTransportCode))
	assert.Len(t, m.ran(), 1, "the command was sent once")
}
