// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// A task (ask_user task: true) is a questions row flagged "task": true, on
// the same async/blocking paths as a question; only the wording differs.
// research/ASYNC_ASK_USER.md, "Tasks".

const askUserTaskItemsJSON = `"questions":[{"question":"Run gcloud auth login on your machine"}]`

func createdMetadata(t *testing.T, created map[string]interface{}) map[string]interface{} {
	t.Helper()
	raw, ok := created["metadata"].(string)
	require.True(t, ok, "QuestionCreate gets the metadata as a JSON string")
	var md map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(raw), &md))
	return md
}

func TestAskUserTask_AsyncIsPostedAsATask(t *testing.T) {
	t.Parallel()
	for name, input := range map[string]string{
		"task: true":     `{"task":true,` + askUserTaskItemsJSON + `}`,
		"task: \"true\"": `{"task":"true",` + askUserTaskItemsJSON + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var suite temporaltest.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			var questions questionActivities
			questions.register(env)

			env.ExecuteWorkflow(askUserOnceWorkflow, input)

			require.True(t, env.IsWorkflowCompleted())
			require.NoError(t, env.GetWorkflowError())
			var result map[string]interface{}
			require.NoError(t, env.GetWorkflowResult(&result))
			assert.Equal(t, asyncAskUserTaskResponse("q-async"), result["content"])
			require.Len(t, questions.created, 1)
			assert.Equal(t, true, questions.created[0]["async"], "a task is async by default, like a question")
			md := createdMetadata(t, questions.created[0])
			assert.Equal(t, true, md["task"], "the metadata tells clients it is a task")
			assert.Equal(t, true, md["async"])
			assert.Equal(t, "ask_user", md["type"])
		})
	}
}

// A question's metadata and tool result are unchanged: no "task" key.
func TestAskUserTask_AQuestionCarriesNoTaskFlag(t *testing.T) {
	t.Parallel()
	for name, input := range map[string]string{
		"no flag":     asyncAskUserInput,
		"task: false": `{"task":false,` + askUserQuestionsJSON + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var suite temporaltest.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			var questions questionActivities
			questions.register(env)

			env.ExecuteWorkflow(askUserOnceWorkflow, input)

			require.NoError(t, env.GetWorkflowError())
			var result map[string]interface{}
			require.NoError(t, env.GetWorkflowResult(&result))
			assert.Equal(t, asyncAskUserResponse("q-async"), result["content"])
			require.Len(t, questions.created, 1)
			_, has := createdMetadata(t, questions.created[0])["task"]
			assert.False(t, has)
		})
	}
}

func TestAskUserTask_LegacyEnvelopePrecedence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		outer string
		inner bool
		want  bool
	}{
		{"outer true", `true`, false, true},
		{"outer string true", `"true"`, false, true},
		{"outer false overrides inner", `false`, true, false},
		{"outer string false overrides inner", `"false"`, true, false},
		{"outer null overrides inner", `null`, true, false},
		{"absent outer uses inner", ``, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inner, err := json.Marshal(map[string]interface{}{
				"task":      tc.inner,
				"questions": []map[string]string{{"question": "Sign in"}},
			})
			require.NoError(t, err)
			envelope := map[string]interface{}{
				"__reliant_tool_meta__": map[string]interface{}{},
				"input":                string(inner),
			}
			if tc.outer != "" {
				envelope["task"] = json.RawMessage(tc.outer)
			}
			input, err := json.Marshal(envelope)
			require.NoError(t, err)
			var suite temporaltest.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			var questions questionActivities
			questions.register(env)

			env.ExecuteWorkflow(askUserOnceWorkflow, string(input))

			require.True(t, env.IsWorkflowCompleted())
			require.NoError(t, env.GetWorkflowError())
			var result map[string]interface{}
			require.NoError(t, env.GetWorkflowResult(&result))
			wantContent := asyncAskUserResponse("q-async")
			if tc.want {
				wantContent = asyncAskUserTaskResponse("q-async")
			}
			assert.Equal(t, wantContent, result["content"])
			require.Len(t, questions.created, 1)
			md := createdMetadata(t, questions.created[0])
			assert.Equal(t, tc.want, md["task"] == true)
			assert.Equal(t, []interface{}{map[string]interface{}{"question": "Sign in"}}, md["questions"])
		})
	}
}

// blocking: true with task: true waits for the user to mark it, and the
// model reads what they marked.
func TestAskUserTask_BlockingWaitsForTheMark(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	var questions questionActivities
	questions.register(env)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.question.q-async", map[string]interface{}{
			"action":        "reply",
			"response_data": `{"answers":[{"question":"Run gcloud auth login on your machine","selected":["Not done"],"freetext":"no laptop today"}]}`,
		})
	}, time.Minute)

	env.ExecuteWorkflow(askUserOnceWorkflow, `{"task":true,"blocking":true,`+askUserTaskItemsJSON+`}`)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var result map[string]interface{}
	require.NoError(t, env.GetWorkflowResult(&result))
	assert.Equal(t, "Task: Run gcloud auth login on your machine\nThe user marked it not done: no laptop today", result["content"])
	require.Len(t, questions.created, 1)
	assert.Nil(t, questions.created[0]["async"], "blocking: true still blocks")
	assert.Equal(t, true, createdMetadata(t, questions.created[0])["task"])
}

func TestAskUserTask_BlockingTimeoutSaysTheTaskWasNotMarked(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	var questions questionActivities
	questions.register(env)

	env.ExecuteWorkflow(askUserOnceWorkflow, `{"task":true,"blocking":true,`+askUserTaskItemsJSON+`}`)

	require.NoError(t, env.GetWorkflowError())
	var result map[string]interface{}
	require.NoError(t, env.GetWorkflowResult(&result))
	assert.Equal(t, askUserTaskTimedOut, result["content"])
}

func TestFormatAskUserTaskResponse(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, action, data, want string
	}{
		{"done", "reply", `{"answers":[{"question":"Sign in","selected":["Done"]}]}`, "Task: Sign in\nThe user marked it done."},
		{"done with a note", "reply", `{"answers":[{"question":"Sign in","selected":["Done"],"freetext":"used SSO"}]}`, "Task: Sign in\nThe user marked it done (note: used SSO)."},
		{"not done, no reason", "reply", `{"answers":[{"question":"Sign in","selected":["Not done"]}]}`, "Task: Sign in\nThe user marked it not done (no reason given)."},
		{"several items", "reply", `{"answers":[{"question":"A","selected":["Done"]},{"question":"B","selected":["Not done"],"freetext":"later"}]}`,
			"Task: A\nThe user marked it done.\n\nTask: B\nThe user marked it not done: later"},
		{"an older client's freetext", "reply", `{"answers":[{"question":"Sign in","selected":[],"freetext":"did it"}]}`, "Task: Sign in\nThe user replied: did it"},
		{"nothing marked", "reply", `{"answers":[{"question":"Sign in","selected":[]}]}`, "Task: Sign in\nThe user did not mark it."},
		{"continue", "continue", ``, "The user continued without marking the task done or not done."},
		{"continue with stale task marks", "continue", `{"answers":[{"question":"Sign in","selected":["Done"]}]}`, "The user continued without marking the task done or not done."},
		{"chat reply", "reply", ``, "The user replied via chat message instead of marking the task. Check the conversation for their response."},
		{"plain text", "reply", `all done`, "all done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, formatAskUserTaskResponse(tc.action, tc.data))
		})
	}
}

// A question's result is formatAskUserResponse's, byte for byte.
func TestFormatAskUserResult_QuestionIsUnchanged(t *testing.T) {
	t.Parallel()
	for _, data := range []string{
		`{"answers":[{"question":"Which auth?","selected":["JWT"],"freetext":"x"}]}`,
		``,
		`plain`,
	} {
		for _, action := range []string{"reply", "continue"} {
			assert.Equal(t, formatAskUserResponse(action, data), formatAskUserResult(action, data, false))
		}
	}
}

func TestUnattendedAskUserResponse_Task(t *testing.T) {
	t.Parallel()
	const metadata = `{"type":"ask_user","task":true,"questions":[{"question":"Approve the deploy in the console"}]}`

	out := unattendedAskUserResponse(metadata)

	require.True(t, strings.HasPrefix(out, UnattendedMarker))
	assert.Contains(t, out, "No human is available to do this task.")
	assert.Contains(t, out, "- Approve the deploy in the console")
	assert.Contains(t, out, "Decide how to proceed without it")
	assert.NotContains(t, out, "options offered")
}
