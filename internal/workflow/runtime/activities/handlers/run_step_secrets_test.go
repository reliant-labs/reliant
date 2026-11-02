package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/stepsecrets"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

const stagingKey = "stg_live/secret+value&k=1"

type fakeResolver struct {
	values map[stepsecrets.Ref]string
	err    error
	calls  []fakeResolveCall
}

type fakeResolveCall struct {
	runID, nodeID string
	refs          []stepsecrets.Ref
}

func (f *fakeResolver) ResolveSecrets(_ context.Context, runID, nodeID string, refs []stepsecrets.Ref) (map[stepsecrets.Ref]string, error) {
	f.calls = append(f.calls, fakeResolveCall{runID, nodeID, refs})
	if f.err != nil {
		return nil, f.err
	}
	out := map[stepsecrets.Ref]string{}
	for _, r := range refs {
		v, ok := f.values[r]
		if !ok {
			return nil, errors.New("secret resolution response is incomplete")
		}
		out[r] = v
	}
	return out, nil
}

func traderResolver() *fakeResolver {
	return &fakeResolver{values: map[stepsecrets.Ref]string{
		{Environment: "trader/staging", Name: "TRADER_KEY"}: stagingKey,
	}}
}

func runSecretInput(chatID, command string) ExecuteRunStepInput {
	return ExecuteRunStepInput{
		WorkflowID: "wf-1", ChatID: chatID, StepID: "deploy", Command: command,
		SecretRefs: []StepSecretRef{{Name: "TRADER_KEY", Environment: "trader/staging"}},
	}
}

func executeRun(t *testing.T, a *ExecuteRunStepActivity, in ExecuteRunStepInput) ExecuteRunStepOutput {
	t.Helper()
	env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.Execute)
	val, err := env.ExecuteActivity(a.Execute, in)
	require.NoError(t, err)
	var out ExecuteRunStepOutput
	require.NoError(t, val.Get(&out))
	return out
}

// The command reaches the machine with the value in its ENVIRONMENT and a
// ${NAME} reference in its text, never the value in the command line.
func TestRunStepExportsDeclaredSecretsAsEnvironmentVariables(t *testing.T) {
	repo := setupTestRepo(t)
	chatID := createTestChat(t, repo)
	exec := &mockRunExecutor{responses: []mockRunResponse{{stdout: "ok", exitCode: 0}}}
	resolver := traderResolver()
	a := NewExecuteRunStepActivity(repo, nil, exec).WithMachineCapabilities(maskingMachine).WithSecretResolver(resolver)

	out := executeRun(t, a, runSecretInput(chatID, `curl -H "X-API-Key: `+stepsecrets.Marker("TRADER_KEY")+`" https://x.example/`))

	require.Len(t, exec.calls, 1)
	assert.Equal(t, `curl -H "X-API-Key: ${TRADER_KEY}" https://x.example/`, exec.calls[0].command, "the value is not in the command text")
	assert.Equal(t, map[string]string{"TRADER_KEY": stagingKey}, exec.calls[0].env, "it is exported under the secret's own name")
	assert.NotContains(t, exec.calls[0].command, stagingKey)
	assert.Equal(t, 0, out.ExitCode)

	// The ticket path is asked for exactly this run, node and reference.
	require.Len(t, resolver.calls, 1)
	assert.Equal(t, "wf-1", resolver.calls[0].runID)
	assert.Equal(t, "deploy", resolver.calls[0].nodeID)
	assert.Equal(t, []stepsecrets.Ref{{Environment: "trader/staging", Name: "TRADER_KEY"}}, resolver.calls[0].refs)
}

// A command that reads $TRADER_KEY without any placeholder still gets it.
func TestRunStepExportsEveryDeclaredSecretEvenWithoutAPlaceholder(t *testing.T) {
	repo := setupTestRepo(t)
	chatID := createTestChat(t, repo)
	exec := &mockRunExecutor{responses: []mockRunResponse{{stdout: "ok"}}}
	a := NewExecuteRunStepActivity(repo, nil, exec).WithMachineCapabilities(maskingMachine).WithSecretResolver(traderResolver())
	executeRun(t, a, runSecretInput(chatID, `curl -H "X-API-Key: $TRADER_KEY" https://x.example/`))
	require.Len(t, exec.calls, 1)
	assert.Equal(t, `curl -H "X-API-Key: $TRADER_KEY" https://x.example/`, exec.calls[0].command)
	assert.Equal(t, stagingKey, exec.calls[0].env["TRADER_KEY"])
}

// Every resolved value, and its base64, URL-encoded and JSON forms, is masked in
// stdout, stderr, the combined output, the log file and the chat update.
func TestRunStepMasksEveryOutputIncludingEncodedForms(t *testing.T) {
	repo := setupTestRepo(t)
	chatID := createTestChat(t, repo)
	jsonForm, _ := json.Marshal(stagingKey)
	stdout := "key=" + stagingKey + "\nb64=" + base64.StdEncoding.EncodeToString([]byte(stagingKey)) + "\nurl=" + url.QueryEscape(stagingKey)
	stderr := "json=" + string(jsonForm) + " raw64=" + base64.RawURLEncoding.EncodeToString([]byte(stagingKey))
	exec := &mockRunExecutor{responses: []mockRunResponse{{stdout: stdout, stderr: stderr, exitCode: 3}, {exitCode: 0}}}
	a := NewExecuteRunStepActivity(repo, nil, exec).WithMachineCapabilities(maskingMachine).WithSecretResolver(traderResolver())

	in := runSecretInput(chatID, "env")
	in.LogFile = "/tmp/secret-logs/out.log"
	out := executeRun(t, a, in)

	for name, field := range map[string]string{"stdout": out.Stdout, "stderr": out.Stderr, "output": out.Output} {
		assert.NotContains(t, field, stagingKey, name)
		assert.NotContains(t, field, base64.StdEncoding.EncodeToString([]byte(stagingKey)), name)
		assert.NotContains(t, field, url.QueryEscape(stagingKey), name)
		assert.NotContains(t, field, base64.RawURLEncoding.EncodeToString([]byte(stagingKey)), name)
		assert.Contains(t, field, stepsecrets.Mask, name)
	}
	assert.Equal(t, 3, out.ExitCode, "masking changes text, not the verdict")

	// The log file is written by a second command: its body is masked too.
	require.Len(t, exec.calls, 2)
	assert.NotContains(t, exec.calls[1].command, stagingKey)
	assert.NotContains(t, exec.calls[1].command, base64.StdEncoding.EncodeToString([]byte(stagingKey)))
	assert.Contains(t, exec.calls[1].command, stepsecrets.Mask)
	assert.Empty(t, exec.calls[1].env, "the log write is not handed the secrets")

	raw, _ := json.Marshal(out)
	assert.NotContains(t, string(raw), stagingKey, "nothing in the activity's result carries the value")
}

// What the activity's input and result contain is what Temporal records.
func TestRunStepInputAndResultCarryNoValue(t *testing.T) {
	repo := setupTestRepo(t)
	chatID := createTestChat(t, repo)
	exec := &mockRunExecutor{responses: []mockRunResponse{{stdout: "echoed " + stagingKey}}}
	a := NewExecuteRunStepActivity(repo, nil, exec).WithMachineCapabilities(maskingMachine).WithSecretResolver(traderResolver())

	in := runSecretInput(chatID, `deploy --key `+stepsecrets.Marker("TRADER_KEY"))
	rawIn, err := json.Marshal(in)
	require.NoError(t, err)
	assert.NotContains(t, string(rawIn), stagingKey, "the activity input (history) has no value")
	assert.Contains(t, string(rawIn), "TRADER_KEY", "it has the name")
	assert.Contains(t, string(rawIn), "trader/staging", "and the environment: a reference is data")

	out := executeRun(t, a, in)
	rawOut, _ := json.Marshal(out)
	assert.NotContains(t, string(rawOut), stagingKey, "the activity result (history) has no value")
}

// A transport failure's text and a Go error are masked before they leave.
func TestRunStepErrorsAreMasked(t *testing.T) {
	repo := setupTestRepo(t)
	chatID := createTestChat(t, repo)
	exec := &mockRunExecutor{responses: []mockRunResponse{{err: errors.New("daemon said " + stagingKey)}}}
	a := NewExecuteRunStepActivity(repo, nil, exec).WithMachineCapabilities(maskingMachine).WithSecretResolver(traderResolver())

	env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.Execute)
	_, err := env.ExecuteActivity(a.Execute, runSecretInput(chatID, "true"))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), stagingKey)
	assert.Contains(t, err.Error(), stepsecrets.Mask)
}

// A denial is one printable line naming the fix; it is shown as it is and the
// command never runs.
func TestRunStepShowsTheControllersDenialAndDoesNotRun(t *testing.T) {
	const fix = `workflow "trader-bot" may not use secret TRADER_KEY in trader/staging: an org owner can run 'forge secret allow TRADER_KEY --env staging --workflow trader-bot'`
	repo := setupTestRepo(t)
	chatID := createTestChat(t, repo)
	exec := &mockRunExecutor{responses: []mockRunResponse{{stdout: "must not run"}}}
	a := NewExecuteRunStepActivity(repo, nil, exec).WithMachineCapabilities(maskingMachine).WithSecretResolver(&fakeResolver{err: &stepsecrets.DeniedError{Reason: fix}})

	out := executeRun(t, a, runSecretInput(chatID, "echo hi"))
	assert.Equal(t, -1, out.ExitCode)
	assert.Equal(t, "Error: "+fix, out.Stderr)
	assert.Empty(t, exec.calls, "a denied step never reaches the machine")
}

func TestRunStepWithoutAResolverRefusesRatherThanRunWithoutSecrets(t *testing.T) {
	repo := setupTestRepo(t)
	chatID := createTestChat(t, repo)
	exec := &mockRunExecutor{responses: []mockRunResponse{{stdout: "must not run"}}}
	a := NewExecuteRunStepActivity(repo, nil, exec).WithMachineCapabilities(maskingMachine)

	out := executeRun(t, a, runSecretInput(chatID, "echo hi"))
	assert.Equal(t, -1, out.ExitCode)
	assert.Contains(t, out.Stderr, "no secret resolver configured")
	assert.Empty(t, exec.calls)
}

// A step with no declared secrets is exactly the step it always was: no
// resolver call, no env, the command untouched.
func TestRunStepWithoutSecretsIsUnchanged(t *testing.T) {
	repo := setupTestRepo(t)
	chatID := createTestChat(t, repo)
	exec := &mockRunExecutor{responses: []mockRunResponse{{stdout: "hello"}}}
	resolver := traderResolver()
	a := NewExecuteRunStepActivity(repo, nil, exec).WithMachineCapabilities(maskingMachine).WithSecretResolver(resolver)

	out := executeRun(t, a, ExecuteRunStepInput{WorkflowID: "wf-1", ChatID: chatID, StepID: "s", Command: "echo ${HOME} {{ not a secret }}"})
	assert.Equal(t, "hello", out.Stdout)
	require.Len(t, exec.calls, 1)
	assert.Equal(t, "echo ${HOME} {{ not a secret }}", exec.calls[0].command)
	assert.Nil(t, exec.calls[0].env)
	assert.Empty(t, resolver.calls)
}

// The chat update the UI shows keeps the author's {{ secrets.X }} form of the
// command, not a marker or a value, and masks what the command printed.
func TestRunStepChatUpdateShowsThePlaceholderAndMasksOutput(t *testing.T) {
	repo := setupTestRepo(t)
	chatID := createTestChat(t, repo)
	exec := &mockRunExecutor{responses: []mockRunResponse{{stdout: "token " + stagingKey}}}
	a := NewExecuteRunStepActivity(repo, nil, exec).WithMachineCapabilities(maskingMachine).WithSecretResolver(traderResolver())
	executeRun(t, a, runSecretInput(chatID, "deploy --key "+stepsecrets.Marker("TRADER_KEY")))

	updates := chatUpdatesOfType(t, repo, chatID, "run_output")
	require.NotEmpty(t, updates)
	joined := strings.Join(updates, "\n")
	assert.NotContains(t, joined, stagingKey)
	assert.Contains(t, joined, "{{ secrets.TRADER_KEY }}")
	assert.NotContains(t, joined, "\u27e6secret:", "no marker reaches the UI")
}

func chatUpdatesOfType(t *testing.T, repo *db.Repo, chatID string, _ string) []string {
	t.Helper()
	updates, err := repo.GetUpdatesSince(context.Background(), chatID, 0, 1000)
	require.NoError(t, err)
	var out []string
	for _, u := range updates {
		if u.UpdateType == db.UpdateTypeRunOutput {
			out = append(out, string(u.Data))
		}
	}
	return out
}
