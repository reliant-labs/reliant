// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// A STEP RESUMED AFTER A PAUSE MUST USE WHAT THE USER CHANGED WHILE PAUSED
//
// Chat dfd85515: the main thread's call_llm (model gpt-5.6-sol@codex) was
// cancelled by a pause. The user disconnected Codex, connected Claude, and sent
// a message — the composer signalled update_workflow_state with
// claude-5.5-opus@anthropic, then signal.resume. The resumed step was
// re-dispatched with the inputs its iteration STARTED with (an inline loop
// copies the parent's inputs once per iteration), so it asked for
// gpt-5.6-sol@codex again and failed "none of required providers [codex]".
// Switching models — the user's only way out — could not take effect.
//
// The YAML is the shape of the builtin agent loop: an inline loop whose body is
// a call_llm reading `{{inputs.model}}`.
const resumeSeesUpdateYAML = `
name: resume-sees-update
entry: [agent_loop]
nodes:
  - id: agent_loop
    type: loop
    while: outputs.keep_going == true
    inline:
      outputs:
        keep_going: "{{has(nodes.call_llm) && has(nodes.call_llm.keep_going) ? nodes.call_llm.keep_going : false}}"
      entry: [call_llm]
      nodes:
        - id: call_llm
          type: call_llm
          args:
            model: "{{inputs.model}}"
edges: []
`

const (
	pinnedToDisconnected = "gpt-5.6-sol@codex"
	userSwitchedTo       = "claude-5.5-opus@anthropic"
)

type resumeSeesUpdateEnv struct {
	mu     sync.Mutex
	models []string // the model id each CallLLM dispatch carried
}

func (e *resumeSeesUpdateEnv) dispatched() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.models...)
}

// dispatchedModelID reads the evaluated `model` arg off a CallLLM input: the
// exact value the activity would resolve.
func dispatchedModelID(t *testing.T, in types.ActivityInput) string {
	t.Helper()
	raw, err := json.Marshal(in)
	require.NoError(t, err)
	var decoded struct {
		Node struct {
			CallLLM struct {
				Model struct {
					Literal struct {
						ID string `json:"id"`
					} `json:"literal"`
				} `json:"model"`
			} `json:"call_llm"`
		} `json:"node"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	return decoded.Node.CallLLM.Model.Literal.ID
}

// newResumeSeesUpdateEnv registers the workflow and a CallLLM whose FIRST
// dispatch fails via firstAttempt; later dispatches complete the run.
func newResumeSeesUpdateEnv(t *testing.T, env *testsuite.TestWorkflowEnvironment, firstAttempt func() error) *resumeSeesUpdateEnv {
	t.Helper()
	e := &resumeSeesUpdateEnv{}

	wf, err := wfyaml.ParseWorkflow([]byte(resumeSeesUpdateYAML))
	require.NoError(t, err)
	wfJSON, err := protojson.Marshal(wf)
	require.NoError(t, err)

	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]string) (LoadedWorkflow, error) {
			return LoadedWorkflow{WorkflowJSON: wfJSON}, nil
		},
		activity.RegisterOptions{Name: "ActivityLoadWorkflow"},
	)
	for _, name := range []string{"WorkflowStatus", "WorkflowCheckpoint", "WorkflowError", "EmitToolCallStatus", "Cleanup"} {
		env.RegisterActivityWithOptions(
			func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
				return map[string]interface{}{"success": true}, nil
			},
			activity.RegisterOptions{Name: name},
		)
	}
	env.RegisterActivityWithOptions(
		func(_ context.Context, in types.ActivityInput) (map[string]interface{}, error) {
			e.mu.Lock()
			e.models = append(e.models, dispatchedModelID(t, in))
			attempt := len(e.models)
			e.mu.Unlock()
			if attempt == 1 {
				return nil, firstAttempt()
			}
			return map[string]interface{}{
				"response_text": "done",
				"keep_going":    false,
				"message":       map[string]interface{}{"role": "assistant", "text": "done"},
			}, nil
		},
		activity.RegisterOptions{Name: "CallLLM"},
	)
	return e
}

func resumeSeesUpdateInput(chatID string) WorkflowInput {
	input := nestedPauseInput(chatID)
	input.WorkflowName = "resume-sees-update"
	input.ExecContext.WorkflowName = "resume-sees-update"
	input.Inputs = map[string]interface{}{
		"model": map[string]interface{}{"id": pinnedToDisconnected, "tags": []interface{}{"flagship"}},
	}
	return input
}

// sendMessageWithModel is what SendMessage does to a paused chat: update the
// workflow's inputs from the composer, then resume it.
func sendMessageWithModel(env *testsuite.TestWorkflowEnvironment, modelID string) {
	env.SignalWorkflow("update_workflow_state", map[string]interface{}{
		"model": map[string]interface{}{"id": modelID, "tags": []interface{}{"flagship"}},
	})
	env.SignalWorkflow("signal.resume", nil)
}

// The path chat dfd85515 took: the in-flight step is cancelled by a pause
// (another thread's failure paused the chat), and the user's model change
// arrives while it waits.
func TestResumedStep_CancelledByPause_UsesModelChosenWhilePaused(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	newEnv := newResumeSeesUpdateEnv(t, env, func() error {
		// The pause lands while this step is in flight, cancelling it.
		env.SignalWorkflow("signal.pause", nil)
		return temporal.NewCanceledError("cancelled by pause")
	})
	env.RegisterDelayedCallback(func() { sendMessageWithModel(env, userSwitchedTo) }, time.Second)

	env.ExecuteWorkflow(DynamicWorkflow, resumeSeesUpdateInput("chat-resume-cancelled"))

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	got := newEnv.dispatched()
	require.Len(t, got, 2, "the cancelled step is re-dispatched once after resume")
	assert.Equal(t, pinnedToDisconnected, got[0])
	assert.Equal(t, userSwitchedTo, got[1],
		"the resumed step must carry the model the user picked while paused, not the one its iteration started with")
}

// The self-pause path: the step fails terminally (here: its pinned provider is
// gone), the loop pauses, and the user's next message carries a new model.
func TestResumedStep_AfterRetryExhaustion_UsesModelChosenWhilePaused(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	newEnv := newResumeSeesUpdateEnv(t, env, func() error {
		pinned := &models.ProviderUnavailableError{ModelID: "gpt-5.6-sol", Providers: []string{"codex"}}
		return temporal.NewNonRetryableApplicationError(pinned.Error(), "TerminalError", pinned)
	})
	env.RegisterDelayedCallback(func() { sendMessageWithModel(env, userSwitchedTo) }, time.Second)

	env.ExecuteWorkflow(DynamicWorkflow, resumeSeesUpdateInput("chat-resume-exhausted"))

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	got := newEnv.dispatched()
	require.Len(t, got, 2)
	assert.Equal(t, userSwitchedTo, got[1],
		"the retried step must carry the model the user picked while paused")
}

// The paused-chat banner shows the resolution failure's own explanation, not
// the Temporal wrap chain around it.
func TestExtractLLMErrorSummary_ResolutionFailureKeepsItsExplanation(t *testing.T) {
	t.Parallel()
	const explanation = "gpt-5.6-sol runs only on Codex (ChatGPT), and Codex (ChatGPT) is not connected. " +
		"Reconnect Codex (ChatGPT) in Settings → Providers, or pick a model from a connected provider in the composer's model picker"
	wrapped := "activity error (type: CallLLM, scheduledEventID: 12494, startedEventID: 12499, identity: w): " +
		"failed to stream LLM response: failed to resolve model: " + explanation +
		" (type: TerminalError, retryable: false): failed to stream LLM response: failed to resolve model: " + explanation
	assert.Equal(t, explanation, extractLLMErrorSummary(wrapped))

	// A rejected credential's reason survives too — the reconnect patterns
	// must not flatten "HTTP 401" into a generic "session expired".
	rejected := "failed to stream LLM response: failed to resolve model: claude-5.5-sonnet runs only on GitHub Copilot, " +
		"and GitHub Copilot rejected the saved credential (HTTP 401: 401 Unauthorized). Reconnect GitHub Copilot in Settings → Providers"
	assert.Contains(t, extractLLMErrorSummary(rejected), "GitHub Copilot rejected the saved credential")
}

// A resolution no provider can serve fails ONCE. It used to be "category=unknown
// is_terminal=false" and retried five times ("Retrying (Attempt 2/5)") over an
// error only the user can fix.
func TestClassifyError_NoServableProviderIsTerminal(t *testing.T) {
	t.Parallel()
	_, resolveErr := models.MustGetRegistry().Resolve(models.ModelSelector{ID: pinnedToDisconnected}, []string{"anthropic"})
	require.ErrorIs(t, resolveErr, drivererrors.ErrNoServableProvider)

	// The activity wraps it, as call_llm does.
	err := fmt.Errorf("failed to stream LLM response: %w", resolveErr)
	assert.True(t, isTerminal(classifyError(err)), "no servable provider must not be retried")
}
