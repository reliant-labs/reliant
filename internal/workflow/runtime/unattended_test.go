// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"
)

// The questions table is the only record of a run being blocked on a human, and
// QuestionCreate is the only way a row gets into it. So "creates zero questions
// rows" is asserted as "never schedules the QuestionCreate activity" — which is
// also, exactly, "never waits", since the signal it would then wait on is keyed
// on the id that activity returns.

// registerUnattendedTestActivities wires the activities DynamicWorkflow needs to
// get off the ground, plus a QuestionCreate that records whether it was called.
func registerUnattendedTestActivities(
	t *testing.T,
	env *testsuite.TestWorkflowEnvironment,
	workflowBytes []byte,
	questionID *string,
) {
	t.Helper()

	env.RegisterActivityWithOptions(
		func(_ context.Context, input json.RawMessage) (map[string]interface{}, error) {
			var payload map[string]interface{}
			require.NoError(t, json.Unmarshal(input, &payload))
			*questionID = "question-123"
			return map[string]interface{}{
				"question_id":      *questionID,
				"already_resolved": false,
				"response_data":    "",
			}, nil
		},
		activity.RegisterOptions{Name: "QuestionCreate"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{"resolved": true}, nil
		},
		activity.RegisterOptions{Name: "QuestionResolve"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]string) (LoadedWorkflow, error) {
			return LoadedWorkflow{WorkflowJSON: workflowBytes}, nil
		},
		activity.RegisterOptions{Name: "ActivityLoadWorkflow"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (interface{}, error) { return nil, nil },
		activity.RegisterOptions{Name: "WorkflowStatus"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{"success": true}, nil
		},
		activity.RegisterOptions{Name: "WorkflowCheckpoint"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{}, nil
		},
		activity.RegisterOptions{Name: "Cleanup"},
	)
}

// askQuestionWorkflowBytes builds a one-node workflow whose only node is an
// ask_question checkpoint, surfacing the fields that distinguish an auto-resolved
// checkpoint from a human answer.
func askQuestionWorkflowBytes(t *testing.T) []byte {
	t.Helper()
	const metadata = `{"type":"ask_user","questions":[{"question":"Proceed?","options":[{"label":"Continue"},{"label":"Stop"}]}]}`
	b, err := protojson.Marshal(&reliantv1.Workflow{
		Name:  "unattended-ask-question",
		Entry: []string{"ask"},
		Nodes: []*reliantv1.Node{
			{
				Id:   "ask",
				Type: model.NodeTypeAskQuestion,
				Args: &reliantv1.Node_AskQuestion{AskQuestion: &reliantv1.AskQuestionArgs{
					Metadata: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: metadata}},
				}},
			},
		},
		Outputs: map[string]string{
			"response":      "{{nodes.ask.response}}",
			"has_feedback":  "{{nodes.ask.has_feedback}}",
			"auto_resolved": "{{has(nodes.ask.auto_resolved) ? nodes.ask.auto_resolved : false}}",
			"resolved_by":   "{{has(nodes.ask.resolved_by) ? nodes.ask.resolved_by : ''}}",
			"record":        "{{has(nodes.ask.record) ? nodes.ask.record : ''}}",
		},
	})
	require.NoError(t, err)
	return b
}

func runAskQuestionWorkflow(
	t *testing.T,
	inputs map[string]interface{},
	onStarted func(env *testsuite.TestWorkflowEnvironment, questionID *string),
) ([]string, WorkflowResult) {
	t.Helper()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	workflowBytes := askQuestionWorkflowBytes(t)

	var scheduled []string
	env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, _ converter.EncodedValues) {
		scheduled = append(scheduled, info.ActivityType.Name)
	})

	var questionID string
	registerUnattendedTestActivities(t, env, workflowBytes, &questionID)
	if onStarted != nil {
		onStarted(env, &questionID)
	}

	env.ExecuteWorkflow(DynamicWorkflow, WorkflowInput{
		ChatID:       "chat-unattended",
		WorkflowName: "unattended-ask-question",
		Inputs:       inputs,
		ExecContext: &ExecutionContext{
			WorkflowID:   "wf-unattended",
			ChatID:       "chat-unattended",
			Thread:       "thread-unattended",
			ThreadMode:   model.ThreadModeNew,
			WorkflowName: "unattended-ask-question",
		},
	})

	require.NoError(t, env.GetWorkflowError())
	var result WorkflowResult
	require.NoError(t, env.GetWorkflowResult(&result))
	return scheduled, result
}

// An unattended run must never park on a checkpoint. No questions row is created,
// so there is nothing for a human to answer and nothing to wait on.
func TestUnattendedAskQuestionCreatesNoQuestionRow(t *testing.T) {
	scheduled, result := runAskQuestionWorkflow(t, map[string]interface{}{"unattended": true}, nil)

	require.NotContains(t, scheduled, "QuestionCreate",
		"an unattended run must create ZERO questions rows — QuestionCreate is the only way one is written, "+
			"and the 24h signal wait is keyed on the id it returns")
	require.NotContains(t, scheduled, "QuestionResolve",
		"nothing was created, so nothing should be resolved either")
	require.Equal(t, false, result.Outputs["has_feedback"],
		"no human answered, so there is no feedback")
	require.Equal(t, "", result.Outputs["response"],
		"nothing may be selected on the user's behalf")
}

// The auto-resolution has to be legible as an auto-resolution. A human answer and
// a machine's "nobody was there" must not read the same in the record.
func TestUnattendedAskQuestionIsDistinguishableFromAHumanAnswer(t *testing.T) {
	_, auto := runAskQuestionWorkflow(t, map[string]interface{}{"unattended": true}, nil)

	require.Equal(t, true, auto.Outputs["auto_resolved"],
		"an auto-resolved checkpoint must say so")
	require.Equal(t, UnattendedResolver, auto.Outputs["resolved_by"],
		"and must name what resolved it")
	require.Contains(t, auto.Outputs["record"], UnattendedMarker,
		"the record must carry the greppable marker")
	require.Contains(t, auto.Outputs["record"], "No option was selected",
		"the record must state that nothing was chosen, not imply a choice was made")

	_, human := runAskQuestionWorkflow(t, map[string]interface{}{}, func(env *testsuite.TestWorkflowEnvironment, questionID *string) {
		env.RegisterDelayedCallback(func() {
			require.NotEmpty(t, *questionID)
			env.SignalWorkflow("signal.question."+*questionID, map[string]interface{}{
				"action":        "reply",
				"response_data": `{"answers":[{"question":"Proceed?","selected":[],"freetext":"Looks good"}]}`,
			})
		}, time.Second)
	})

	require.Equal(t, false, human.Outputs["auto_resolved"],
		"a human answer must NEVER be taggable as auto-resolved — that is the whole distinction")
	require.Equal(t, "", human.Outputs["resolved_by"])
	require.Equal(t, "Looks good", human.Outputs["response"])
}

// Regression guard: with the input unset, nothing about the interactive path moves.
func TestInteractiveAskQuestionStillCreatesAQuestionAndWaits(t *testing.T) {
	scheduled, result := runAskQuestionWorkflow(t, map[string]interface{}{}, func(env *testsuite.TestWorkflowEnvironment, questionID *string) {
		env.RegisterDelayedCallback(func() {
			require.NotEmpty(t, *questionID)
			env.SignalWorkflow("signal.question."+*questionID, map[string]interface{}{
				"action":        "reply",
				"response_data": `{"answers":[{"question":"Proceed?","selected":[],"freetext":"Looks good"}]}`,
			})
		}, time.Second)
	})

	require.Contains(t, scheduled, "QuestionCreate",
		"an interactive run must still raise the gate — unattended is opt-in")
	require.Equal(t, "Looks good", result.Outputs["response"])
	require.Equal(t, true, result.Outputs["has_feedback"])
}

// Explicitly false must behave exactly like unset.
func TestExplicitlyAttendedAskQuestionStillCreatesAQuestion(t *testing.T) {
	scheduled, _ := runAskQuestionWorkflow(t, map[string]interface{}{"unattended": false}, func(env *testsuite.TestWorkflowEnvironment, questionID *string) {
		env.RegisterDelayedCallback(func() {
			env.SignalWorkflow("signal.question."+*questionID, map[string]interface{}{
				"action": "continue",
			})
		}, time.Second)
	})

	require.Contains(t, scheduled, "QuestionCreate",
		"unattended: false is the interactive path")
}

// ─── the ask_user TOOL ────────────────────────────────────────────────────────

func askUserToolWorkflowBytes(t *testing.T) []byte {
	t.Helper()
	b, err := protojson.Marshal(&reliantv1.Workflow{
		Name:  "unattended-ask-user",
		Entry: []string{"tools"},
		Nodes: []*reliantv1.Node{
			{
				Id:   "tools",
				Type: model.NodeTypeExecuteTools,
				Args: &reliantv1.Node_ExecuteTools{ExecuteTools: &reliantv1.ExecuteToolsArgs{
					ResolvedToolCalls: []*reliantv1.ToolCallMsg{
						{
							Id:   "call_1",
							Name: "ask_user",
							Input: `{"questions":[{"question":"What visual style?",` +
								`"options":[{"label":"Clinical Minimal"},{"label":"Bold"}]}]}`,
						},
					},
				}},
			},
		},
		Outputs: map[string]string{
			"content": "{{nodes.tools.tool_results[0].content}}",
		},
	})
	require.NoError(t, err)
	return b
}

// The tool-layer backstop. Even if a charter still tells the agent to ask — and
// scope-conversation's charter says "You MUST use ask_user at least once" — the
// call cannot park an unattended run.
func TestUnattendedAskUserToolReturnsUnansweredWithoutAQuestionRow(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	workflowBytes := askUserToolWorkflowBytes(t)

	var scheduled []string
	env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, _ converter.EncodedValues) {
		scheduled = append(scheduled, info.ActivityType.Name)
	})

	var questionID string
	registerUnattendedTestActivities(t, env, workflowBytes, &questionID)

	env.ExecuteWorkflow(DynamicWorkflow, WorkflowInput{
		ChatID:       "chat-unattended",
		WorkflowName: "unattended-ask-user",
		Inputs:       map[string]interface{}{"unattended": true},
		ExecContext: &ExecutionContext{
			WorkflowID:   "wf-unattended",
			ChatID:       "chat-unattended",
			Thread:       "thread-unattended",
			ThreadMode:   model.ThreadModeNew,
			WorkflowName: "unattended-ask-user",
		},
	})

	require.NoError(t, env.GetWorkflowError())
	require.NotContains(t, scheduled, "QuestionCreate",
		"an ask_user call in an unattended run must create ZERO questions rows")

	var result WorkflowResult
	require.NoError(t, env.GetWorkflowResult(&result))
	content, ok := result.Outputs["content"].(string)
	require.True(t, ok, "the ask_user tool must still return a tool result, got %#v", result.Outputs["content"])

	assert.Contains(t, content, UnattendedMarker,
		"the tool result must carry the greppable auto-resolution marker")
	assert.Contains(t, content, "nobody answered it",
		"the model must be told plainly that no answer came back")
	assert.Contains(t, content, "nothing was selected on the user's behalf",
		"the model must not be able to read this as a human decision")
	assert.Contains(t, content, "What visual style?",
		"the questions must be echoed back so the agent can resolve them without re-deriving them")
	assert.Contains(t, content, "Clinical Minimal",
		"the options offered must be echoed back too")
	assert.Contains(t, content, "state the decision you made and the reason",
		"the agent's own statement is the auto-resolution record, so it must be required")
}

// Regression guard for the tool path.
func TestInteractiveAskUserToolStillCreatesAQuestion(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	workflowBytes := askUserToolWorkflowBytes(t)

	var scheduled []string
	env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, _ converter.EncodedValues) {
		scheduled = append(scheduled, info.ActivityType.Name)
	})

	var questionID string
	registerUnattendedTestActivities(t, env, workflowBytes, &questionID)

	env.RegisterDelayedCallback(func() {
		require.NotEmpty(t, questionID)
		env.SignalWorkflow("signal.question."+questionID, map[string]interface{}{
			"action":        "reply",
			"response_data": `{"answers":[{"question":"What visual style?","selected":["Clinical Minimal"]}]}`,
		})
	}, time.Second)

	env.ExecuteWorkflow(DynamicWorkflow, WorkflowInput{
		ChatID:       "chat-interactive",
		WorkflowName: "unattended-ask-user",
		Inputs:       map[string]interface{}{},
		ExecContext: &ExecutionContext{
			WorkflowID:   "wf-interactive",
			ChatID:       "chat-interactive",
			Thread:       "thread-interactive",
			ThreadMode:   model.ThreadModeNew,
			WorkflowName: "unattended-ask-user",
		},
	})

	require.NoError(t, env.GetWorkflowError())
	require.Contains(t, scheduled, "QuestionCreate",
		"an interactive run must still ask the human")

	var result WorkflowResult
	require.NoError(t, env.GetWorkflowResult(&result))
	content, _ := result.Outputs["content"].(string)
	require.Contains(t, content, "Clinical Minimal")
	require.NotContains(t, content, UnattendedMarker,
		"a real answer must never be labelled auto-resolved")
}

// ─── propagation ──────────────────────────────────────────────────────────────

func TestIsUnattended(t *testing.T) {
	assert.False(t, IsUnattended(nil))
	assert.False(t, IsUnattended(map[string]interface{}{}))
	assert.False(t, IsUnattended(map[string]interface{}{"unattended": false}))
	assert.True(t, IsUnattended(map[string]interface{}{"unattended": true}))
	// Toolbar/CLI values arrive as strings.
	assert.True(t, IsUnattended(map[string]interface{}{"unattended": "true"}))
	assert.False(t, IsUnattended(map[string]interface{}{"unattended": "false"}))
	assert.False(t, IsUnattended(map[string]interface{}{"unattended": 1}))
}

// Monotone: a child can never turn unattended back off, because there is no human
// for it to turn back on. This is the property that makes the flag undroppable —
// it is why `unattended` cannot repeat `ask`'s history of being declared,
// threaded, and inert.
func TestPropagateUnattendedIsMonotone(t *testing.T) {
	t.Run("an unattended parent overrides a child arg that says otherwise", func(t *testing.T) {
		child := map[string]interface{}{"unattended": false}
		propagateUnattended(map[string]interface{}{"unattended": true}, child)
		assert.Equal(t, true, child["unattended"])
	})

	t.Run("an unattended parent overrides a child schema default", func(t *testing.T) {
		// get-it-right declares `unattended: false` as its default, so this is the
		// exact case a ref'd sub-workflow hits.
		child := map[string]interface{}{}
		propagateUnattended(map[string]interface{}{"unattended": true}, child)
		assert.Equal(t, true, child["unattended"])
	})

	t.Run("an attended parent never forces the flag on", func(t *testing.T) {
		child := map[string]interface{}{}
		propagateUnattended(map[string]interface{}{}, child)
		_, present := child["unattended"]
		assert.False(t, present, "an interactive run must not gain the flag")
	})

	t.Run("an attended parent leaves a child that opted in alone", func(t *testing.T) {
		child := map[string]interface{}{"unattended": true}
		propagateUnattended(map[string]interface{}{"unattended": false}, child)
		assert.Equal(t, true, child["unattended"],
			"a node may declare one phase unattended even when the run is not")
	})
}

// The forge preset hands every spawned fan-out unit `ask_user`. Without this, one
// unit's question parks the whole unattended run.
func TestBuildSpawnChildInputsPropagatesUnattended(t *testing.T) {
	child := buildSpawnChildInputs(map[string]interface{}{"mode": "auto", "unattended": true})
	assert.Equal(t, true, child["unattended"],
		"a spawned sub-agent runs in the same world as its parent")

	attended := buildSpawnChildInputs(map[string]interface{}{"mode": "auto"})
	_, present := attended["unattended"]
	assert.False(t, present, "an interactive run's children stay interactive")
}

func TestUnattendedAskUserResponseNamesTheQuestionsItCouldNotAnswer(t *testing.T) {
	const metadata = `{"type":"ask_user","questions":[` +
		`{"question":"Which entities?","options":[{"label":"Peptide"},{"label":"Protocol"}]},` +
		`{"question":"Branding?","options":[]}]}`

	out := unattendedAskUserResponse(metadata)

	require.True(t, strings.HasPrefix(out, UnattendedMarker),
		"the marker must lead so the record is greppable from the first character")
	assert.Contains(t, out, "Which entities?")
	assert.Contains(t, out, "Peptide | Protocol")
	assert.Contains(t, out, "Branding?")
	assert.NotContains(t, out, "did not respond in time",
		"this is not a timeout — saying so would blame a human who was never asked")
}
