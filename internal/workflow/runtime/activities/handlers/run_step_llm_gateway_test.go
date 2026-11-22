// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

const gatewayTestKey = "rlsk_run_step_test_key_0123456789"

// fakeStepKeys is the control plane's step-key service.
type fakeStepKeys struct {
	mu       sync.Mutex
	mints    []controlplane.StepKeyRequest
	revokes  []string
	mintErr  error
	revokeIt controlplane.StepKeyRevocation
}

func (f *fakeStepKeys) MintStepKey(_ context.Context, r controlplane.StepKeyRequest) (controlplane.StepKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mints = append(f.mints, r)
	if f.mintErr != nil {
		return controlplane.StepKey{}, f.mintErr
	}
	return controlplane.StepKey{
		KeyID: "sk_1", Key: controlplane.StepKeySecret(gatewayTestKey), BaseURL: "https://llm.example",
		Models: []string{"claude-sonnet-4-6", "claude-haiku-4-5"}, MaxBudgetUSD: r.MaxBudgetUSD,
		ExpiresAt: time.Date(2026, 11, 22, 7, 0, 0, 0, time.UTC), BillingOrgID: "org_1",
	}, nil
}

func (f *fakeStepKeys) RevokeStepKey(_ context.Context, requestID string) (controlplane.StepKeyRevocation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revokes = append(f.revokes, requestID)
	return f.revokeIt, nil
}

func (f *fakeStepKeys) calls() (int, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.mints), append([]string(nil), f.revokes...)
}

// envMachine is a machine that records the step env each command came with
// and prints back what the result says (by default, the key itself).
type envMachine struct {
	envs    []*daemon.StepEnv
	ran     []string
	results []CommandResult
	err     error
}

func (m *envMachine) ExecuteCommand(ctx context.Context, command, _ string, _ int, _ map[string]string) (CommandResult, error) {
	m.ran = append(m.ran, command)
	m.envs = append(m.envs, runStepEnvFrom(ctx))
	if m.err != nil {
		return CommandResult{ExitCode: -1}, m.err
	}
	if len(m.results) > 0 {
		r := m.results[0]
		m.results = m.results[1:]
		return r, nil
	}
	return CommandResult{Stdout: "using " + gatewayTestKey, Stderr: "token=" + gatewayTestKey}, nil
}

// gatewayStep is a run step on a chat whose machine registered (or not) the
// step-env capability.
func gatewayStep(t *testing.T, capable bool, keys StepKeyIssuer, machine RunExecutor) (*ExecuteRunStepActivity, string) {
	t.Helper()
	repo := setupTestRepo(t)
	chatID := createTestChat(t, repo)
	caps := `["shell"]`
	if capable {
		caps = `["run_step.llm_gateway_env","shell"]`
	}
	require.NoError(t, repo.UpsertDaemon(context.Background(), &db.Daemon{ID: testMachine, UserID: "test-user", Capabilities: &caps}))
	d, err := repo.GetDaemon(context.Background(), testMachine)
	require.NoError(t, err)
	require.Equal(t, caps, *d.Capabilities, "the registration this test relies on")
	act := NewExecuteRunStepActivity(repo, nil, machine)
	if keys != nil {
		act = act.WithStepKeys(keys)
	}
	return act, chatID
}

func gatewayInput(chatID string) ExecuteRunStepInput {
	return ExecuteRunStepInput{
		WorkflowID: "wf-1", ChatID: chatID, StepID: "bench", NodePath: "loop/bench",
		Command: `claude -p "fix it"`, Timeout: int((20 * time.Minute).Milliseconds()), Credentials: "none",
		LLMGateway: &RunStepLLMGateway{CLI: "claude-code", BudgetUSD: 2, Models: []string{"claude-sonnet-4-6"}},
	}
}

func runGatewayStep(t *testing.T, act *ExecuteRunStepActivity, input ExecuteRunStepInput, attempt int32) (ExecuteRunStepOutput, error) {
	t.Helper()
	env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivityWithOptions(func(ctx context.Context, in ExecuteRunStepInput) (ExecuteRunStepOutput, error) {
		ctx, release := context.WithCancel(ctx)
		defer release()
		ctx = context.WithValue(ctx, releaseAttemptKey{}, func() { release() })
		return act.execute(ctx, in, attempt, stepRequestID)
	}, activity.RegisterOptions{Name: "GatewayStepAttempt"})
	val, err := env.ExecuteActivity("GatewayStepAttempt", input)
	var out ExecuteRunStepOutput
	if err == nil {
		require.NoError(t, val.Get(&out))
	}
	return out, err
}

// A machine whose daemon never registered the capability would run the
// command without the key: refused, nothing minted, nothing run.
func TestRunStepLLMGateway_RefusedOnAMachineWithoutTheCapability(t *testing.T) {
	t.Parallel()
	keys, machine := &fakeStepKeys{}, &envMachine{}
	act, chatID := gatewayStep(t, false, keys, machine)
	out, err := runGatewayStep(t, act, gatewayInput(chatID), 1)
	require.NoError(t, err)
	assert.Equal(t, -1, out.ExitCode)
	assert.Contains(t, out.Stderr, `did not register "run_step.llm_gateway_env"`)
	assert.Contains(t, out.Stderr, "the command was not run")
	mints, revokes := keys.calls()
	assert.Zero(t, mints, "no key for a machine that would ignore it")
	assert.Empty(t, revokes)
	assert.Empty(t, machine.ran)
}

// A worker with no control plane cannot issue keys: refused.
func TestRunStepLLMGateway_RefusedWithoutAnIssuer(t *testing.T) {
	t.Parallel()
	machine := &envMachine{}
	act, chatID := gatewayStep(t, true, nil, machine)
	out, err := runGatewayStep(t, act, gatewayInput(chatID), 1)
	require.NoError(t, err)
	assert.Equal(t, -1, out.ExitCode)
	assert.Contains(t, out.Stderr, "cannot issue LLM gateway step keys")
	assert.Empty(t, machine.ran)
}

// The control plane's refusal is the step's verdict, in Reliant's words.
func TestRunStepLLMGateway_MintRefusalFailsTheStepWithItsReason(t *testing.T) {
	t.Parallel()
	for reason, want := range map[string]string{
		controlplane.ReasonStepKeyCreditExhausted: "Reliant credits are used up",
		controlplane.ReasonStepKeyNoBillingOrg:    "cannot bill the run's organization",
		controlplane.ReasonStepKeyNoModels:        "none of the requested models",
	} {
		keys := &fakeStepKeys{mintErr: &controlplane.StepKeyError{Code: "failed_precondition", Reason: reason, Message: "cp says no"}}
		machine := &envMachine{}
		act, chatID := gatewayStep(t, true, keys, machine)
		out, err := runGatewayStep(t, act, gatewayInput(chatID), 1)
		require.NoError(t, err, reason)
		assert.Equal(t, -1, out.ExitCode, reason)
		assert.Contains(t, out.Stderr, want, reason)
		assert.Empty(t, machine.ran, reason)
		_, revokes := keys.calls()
		assert.Empty(t, revokes, "a clean refusal made no key: %s", reason)
		require.NotNil(t, out.LLMGateway, "the block is there for nodes.<id>.llm_gateway")
		assert.False(t, out.LLMGateway.SpendKnown)
	}

	keys := &fakeStepKeys{mintErr: controlplane.ErrStepKeysUnsupported}
	act, chatID := gatewayStep(t, true, keys, &envMachine{})
	out, err := runGatewayStep(t, act, gatewayInput(chatID), 1)
	require.NoError(t, err)
	assert.Contains(t, out.Stderr, "this deployment cannot issue LLM gateway step keys")

	// A mint whose answer was lost may have made a key: revoked by request id.
	keys = &fakeStepKeys{mintErr: errors.New("connection reset")}
	act, chatID = gatewayStep(t, true, keys, &envMachine{})
	out, err = runGatewayStep(t, act, gatewayInput(chatID), 1)
	require.NoError(t, err)
	assert.Equal(t, -1, out.ExitCode)
	_, revokes := keys.calls()
	assert.Equal(t, []string{stepRequestID}, revokes)
}

// The command gets the key as its step env (never in its input), its output
// is redacted, the key is revoked when it ends, and the step reports spend.
func TestRunStepLLMGateway_RunsWithTheKeyThenRevokesIt(t *testing.T) {
	t.Parallel()
	keys := &fakeStepKeys{revokeIt: controlplane.StepKeyRevocation{Revoked: true, KeyID: "sk_1", SpendUSD: 0.37, SpendKnown: true}}
	machine := &envMachine{}
	act, chatID := gatewayStep(t, true, keys, machine)
	out, err := runGatewayStep(t, act, gatewayInput(chatID), 1)
	require.NoError(t, err)

	require.Len(t, keys.mints, 1)
	mint := keys.mints[0]
	assert.Equal(t, controlplane.StepKeyRequest{
		RequestID: stepRequestID, UserID: "test-user", RunID: "wf-1", StepID: "loop/bench", ChatID: chatID,
		ProjectID: mint.ProjectID, CLI: "claude-code", Models: []string{"claude-sonnet-4-6"},
		MaxBudgetUSD: 2, TTL: 22 * time.Minute,
	}, mint)
	assert.NotEmpty(t, mint.ProjectID)

	require.Len(t, machine.envs, 1)
	env := machine.envs[0]
	require.NotNil(t, env, "the step's command carries its step env")
	assert.Equal(t, gatewayTestKey, env.Secret["ANTHROPIC_AUTH_TOKEN"])
	assert.Equal(t, "https://llm.example", env.Set["ANTHROPIC_BASE_URL"])
	assert.Equal(t, "claude-sonnet-4-6", env.Set["ANTHROPIC_MODEL"])
	assert.Equal(t, "claude-haiku-4-5", env.Set["ANTHROPIC_SMALL_FAST_MODEL"])
	assert.Contains(t, env.Unset, "ANTHROPIC_API_KEY")

	for _, s := range []string{out.Stdout, out.Stderr, out.Output} {
		assert.NotContains(t, s, gatewayTestKey)
	}
	assert.Equal(t, "using [redacted: ANTHROPIC_AUTH_TOKEN]", out.Stdout)
	wire, err := json.Marshal(out)
	require.NoError(t, err)
	assert.NotContains(t, string(wire), gatewayTestKey)

	_, revokes := keys.calls()
	assert.Equal(t, []string{stepRequestID}, revokes, "revoked once, when the command ended")
	assert.Equal(t, &RunStepLLMGatewayOutput{
		KeyID: "sk_1", BillingOrgID: "org_1", Models: []string{"claude-sonnet-4-6", "claude-haiku-4-5"},
		BudgetUSD: 2, SpendUSD: 0.37, SpendKnown: true, ExpiresAt: "2026-11-22T07:00:00Z",
	}, out.LLMGateway)
}

// A failed command, and a command whose result never came back, revoke too.
func TestRunStepLLMGateway_RevokesOnFailure(t *testing.T) {
	t.Parallel()
	keys := &fakeStepKeys{}
	act, chatID := gatewayStep(t, true, keys, &envMachine{results: []CommandResult{{ExitCode: 1, Stderr: "boom"}}})
	out, err := runGatewayStep(t, act, gatewayInput(chatID), 1)
	require.NoError(t, err)
	assert.Equal(t, 1, out.ExitCode)
	_, revokes := keys.calls()
	assert.Equal(t, []string{stepRequestID}, revokes)

	keys = &fakeStepKeys{}
	act, chatID = gatewayStep(t, true, keys, &envMachine{err: errors.New("the machine went away")})
	_, err = runGatewayStep(t, act, gatewayInput(chatID), 1)
	require.Error(t, err)
	_, revokes = keys.calls()
	assert.Equal(t, []string{stepRequestID}, revokes)
}

// An attempt released while its command runs leaves the key to the retry,
// which reattaches to the command and revokes by the request id; a retry
// that finds nothing to reattach to revokes too. A retry never mints.
func TestRunStepLLMGateway_TheRetryRevokes(t *testing.T) {
	t.Parallel()
	keys := &fakeStepKeys{revokeIt: controlplane.StepKeyRevocation{Revoked: true, KeyID: "sk_1", SpendUSD: 1.5, SpendKnown: true}}
	machine := newFakeMachine()
	act, chatID := gatewayStep(t, true, keys, machine)

	_, err := runGatewayStep(t, act, gatewayInput(chatID), 1)
	require.Error(t, err, "the first attempt was released")
	mints, revokes := keys.calls()
	assert.Equal(t, 1, mints)
	assert.Empty(t, revokes, "the command still runs with its key")

	time.AfterFunc(50*time.Millisecond, func() {
		machine.finish(stepRequestID, CommandResult{Stdout: "done", ExitCode: 0})
	})
	out, err := runGatewayStep(t, act, gatewayInput(chatID), 2)
	require.NoError(t, err)
	assert.Equal(t, "done", out.Stdout)
	mints, revokes = keys.calls()
	assert.Equal(t, 1, mints, "a retry never mints")
	assert.Equal(t, []string{stepRequestID}, revokes)
	require.NotNil(t, out.LLMGateway)
	assert.Equal(t, 1.5, out.LLMGateway.SpendUSD)

	// Nothing to reattach to: refused, and still revoked.
	keys = &fakeStepKeys{}
	act, chatID = gatewayStep(t, true, keys, newFakeMachine())
	_, err = runGatewayStep(t, act, gatewayInput(chatID), 2)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "refusing to re-execute") || strings.Contains(err.Error(), "retry"), err.Error())
	mints, revokes = keys.calls()
	assert.Zero(t, mints)
	assert.Equal(t, []string{stepRequestID}, revokes)
}

// A node without llm_gateway is untouched: no key, no env, no block.
func TestRunStepLLMGateway_ANodeWithoutItIsUnchanged(t *testing.T) {
	t.Parallel()
	keys := &fakeStepKeys{}
	machine := &envMachine{results: []CommandResult{{Stdout: "ok"}}}
	act, chatID := gatewayStep(t, false, keys, machine)
	in := gatewayInput(chatID)
	in.LLMGateway = nil
	out, err := runGatewayStep(t, act, in, 1)
	require.NoError(t, err)
	assert.Equal(t, "ok", out.Stdout)
	assert.Nil(t, out.LLMGateway)
	assert.Nil(t, machine.envs[0])
	mints, revokes := keys.calls()
	assert.Zero(t, mints)
	assert.Empty(t, revokes)
}

// The node's llm_gateway reaches the activity input as startRun builds it
// (the args map, by proto names).
func TestRunStepLLMGateway_ReachesTheActivityInput(t *testing.T) {
	t.Parallel()
	node := &reliantv1.Node{Id: "bench", Type: "run", Args: &reliantv1.Node_Run{Run: &reliantv1.RunArgs{
		Command:    &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "claude -p x"}},
		LlmGateway: &reliantv1.LLMGatewayStep{Cli: "claude-code", BudgetUsd: 2, Models: []string{"claude-sonnet-4-6"}},
	}}}
	args, err := model.NodeArgsAsMap(node)
	require.NoError(t, err)
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	var in ExecuteRunStepInput
	require.NoError(t, json.Unmarshal(raw, &in))
	assert.Equal(t, &RunStepLLMGateway{CLI: "claude-code", BudgetUSD: 2, Models: []string{"claude-sonnet-4-6"}}, in.LLMGateway)

	node.GetRun().LlmGateway = nil
	args, err = model.NodeArgsAsMap(node)
	require.NoError(t, err)
	raw, err = json.Marshal(args)
	require.NoError(t, err)
	in = ExecuteRunStepInput{}
	require.NoError(t, json.Unmarshal(raw, &in))
	assert.Nil(t, in.LLMGateway)
}

// The remote executor puts the step env on the request it sends (the
// server-built context), only for the step's own command.
func TestRemoteRunExecutor_CarriesTheStepEnvOnTheRequest(t *testing.T) {
	t.Parallel()
	cap := &capturingToolExecutor{}
	exec := NewRemoteRunExecutor(cap)
	exec.SetContext(RunExecutorContext{UserID: "u", ProjectPath: "/p"})
	env := &daemon.StepEnv{Secret: daemon.SecretEnv{"ANTHROPIC_AUTH_TOKEN": gatewayTestKey}}
	_, err := exec.ExecuteCommand(withRunStepEnv(context.Background(), env), "claude -p x", "/p", 1000, nil)
	require.NoError(t, err)
	_, err = exec.ExecuteCommand(context.Background(), "write log", "/p", 1000, nil)
	require.NoError(t, err)
	require.Len(t, cap.reqs, 2)
	assert.Same(t, env, cap.reqs[0].RunStepEnv)
	assert.NotContains(t, cap.reqs[0].ToolInput, gatewayTestKey, "never in the tool input")
	assert.Nil(t, cap.reqs[1].RunStepEnv, "the log-file write carries no key")
}
