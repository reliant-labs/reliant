// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/gitpublish"
	"github.com/reliant-labs/reliant/internal/llmgatewaystep"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// A run node's `llm_gateway` runs its command with an LLM gateway step key:
//
//  1. The machine is final (resolveRunCheckout), and its daemon must have
//     registered daemon.CapabilityRunStepEnv: an older one would ignore the
//     key and run the command with the machine's own sign-in, or none.
//  2. The worker mints the key at the control plane (StepKeyIssuer), capped to
//     the node's budget and models and to the command's timeout plus slack,
//     idempotent by the step's request id.
//  3. The key travels to the machine only as the request's server-built
//     run_step_env (toolexec.ToolRequest.RunStepEnv), which the shell tool
//     applies to that one command after every other environment rule and
//     redacts from its output.
//  4. The worker redacts it again from everything it keeps or writes, and
//     revokes it when the step ends, on every path; a retry revokes by the
//     request id whatever it finds.
//
// Every refusal is a command verdict (exit -1, the reason on stderr): the
// command never ran, and retrying cannot change the answer. Nothing here ever
// runs the command without its key.

// StepKeyIssuer mints and revokes run steps' LLM gateway step keys:
// controlplane.Service.
type StepKeyIssuer interface {
	MintStepKey(ctx context.Context, r controlplane.StepKeyRequest) (controlplane.StepKey, error)
	RevokeStepKey(ctx context.Context, requestID string) (controlplane.StepKeyRevocation, error)
}

// WithStepKeys lets a run node with llm_gateway run: keys is where its step
// key is minted and revoked. Without it every such node is refused.
func (a *ExecuteRunStepActivity) WithStepKeys(keys StepKeyIssuer) *ExecuteRunStepActivity {
	a.stepKeys = keys
	return a
}

// RunStepLLMGateway is a run node's `llm_gateway`, as the activity input
// carries it (the proto's LLMGatewayStep, by its YAML names).
type RunStepLLMGateway struct {
	CLI       string   `json:"cli"`
	BudgetUSD float64  `json:"budget_usd,omitempty"`
	Models    []string `json:"models,omitempty"`
}

// RunStepLLMGatewayOutput is nodes.<id>.llm_gateway (the proto's
// RunLLMGatewayOutput): what the step's key was allowed and spent. Never the
// key.
type RunStepLLMGatewayOutput struct {
	KeyID        string   `json:"key_id"`
	BillingOrgID string   `json:"billing_org_id"`
	Models       []string `json:"models"`
	BudgetUSD    float64  `json:"budget_usd"`
	SpendUSD     float64  `json:"spend_usd"`
	SpendKnown   bool     `json:"spend_known"`
	ExpiresAt    string   `json:"expires_at"`
}

// stepKeyRevokeTimeout bounds the revoke, which runs detached from the
// step's context: it must happen after a cancel too.
const stepKeyRevokeTimeout = 10 * time.Second

// stepKeyScope is one attempt's step key: what it must revoke, and how to
// redact the key from what the attempt keeps. A nil scope (no llm_gateway)
// does nothing.
type stepKeyScope struct {
	issuer    StepKeyIssuer
	requestID string
	gateway   *RunStepLLMGateway
	input     ExecuteRunStepInput

	// key is the minted key, empty on a retry (which never mints).
	key controlplane.StepKey
	env *daemon.StepEnv
	// pending: a key may be live under requestID and must be revoked.
	pending bool
	// kept: the attempt ended leaving its command running for a retry to
	// reattach to, which revokes; this attempt must not.
	kept bool
	// out is the output block, filled as the scope learns it.
	out *RunStepLLMGatewayOutput
}

// stepKeyScope is the step key scope of one attempt of input's command,
// nil when the node has no llm_gateway. A retry's scope revokes by the request
// id whatever the attempt finds.
func (a *ExecuteRunStepActivity) stepKeyScope(input ExecuteRunStepInput, requestID string, retry bool) *stepKeyScope {
	if input.LLMGateway == nil {
		return nil
	}
	gw := input.LLMGateway
	return &stepKeyScope{
		issuer: a.stepKeys, requestID: requestID, gateway: gw, input: input,
		pending: retry && a.stepKeys != nil,
		out: &RunStepLLMGatewayOutput{
			Models:    append([]string{}, gw.Models...),
			BudgetUSD: llmgatewaystep.Budget(gw.BudgetUSD),
		},
	}
}

// refusal is the command verdict of a step whose key could not be had.
func stepKeyRefusal(reason string) string {
	return "llm_gateway: " + reason + "; the command was not run"
}

// prepare checks the machine and mints the key for the attempt's command,
// returning the environment the command runs with, or the refusal that ends
// the step without running it. execCtx's selector is pinned to the machine
// whose registration was checked, so the command goes nowhere else.
func (s *stepKeyScope) prepare(ctx context.Context, a *ExecuteRunStepActivity, execCtx *RunExecutorContext, timeoutMs int) (*daemon.StepEnv, string) {
	gw := s.gateway
	if err := llmgatewaystep.Validate(gw.CLI, gw.BudgetUSD, gw.Models); err != nil {
		return nil, stepKeyRefusal(err.Error())
	}
	cli, _ := llmgatewaystep.Lookup(gw.CLI)
	if s.issuer == nil {
		return nil, stepKeyRefusal("this deployment cannot issue LLM gateway step keys (no control plane is configured for this worker)")
	}

	// 1. The machine must apply step keys.
	daemonID, refusal := a.stepKeyMachine(ctx, execCtx)
	if refusal != "" {
		return nil, stepKeyRefusal(refusal)
	}

	// 2. Mint.
	key, err := s.issuer.MintStepKey(ctx, controlplane.StepKeyRequest{
		RequestID: s.requestID,
		UserID:    execCtx.UserID, OrgID: execCtx.OrgID,
		RunID: s.input.WorkflowID, StepID: stepKeyStepID(s.input),
		ChatID: s.input.ChatID, ProjectID: execCtx.ProjectID,
		TriggerID: execCtx.TriggerID, WorkflowName: execCtx.WorkflowName,
		CLI: cli.Name, Models: gw.Models,
		MaxBudgetUSD: llmgatewaystep.Budget(gw.BudgetUSD),
		TTL:          llmgatewaystep.TTL(time.Duration(timeoutMs) * time.Millisecond),
	})
	if err != nil {
		// A mint whose answer was lost may have made a key: revoke by the
		// request id on the way out.
		s.pending = !mintRefusedCleanly(err)
		return nil, stepKeyRefusal(mintRefusalReason(err))
	}
	s.key, s.pending = key, true
	s.out.KeyID, s.out.BillingOrgID = key.KeyID, key.BillingOrgID
	s.out.BudgetUSD = key.MaxBudgetUSD
	if len(key.Models) > 0 {
		s.out.Models = append([]string{}, key.Models...)
	}
	if !key.ExpiresAt.IsZero() {
		s.out.ExpiresAt = key.ExpiresAt.UTC().Format(time.RFC3339)
	}
	models := key.Models
	if len(models) == 0 {
		models = gw.Models
	}

	// 3. The command's environment.
	env, err := cli.Env(key.BaseURL, key.Key.Reveal(), models)
	if err != nil {
		return nil, stepKeyRefusal(err.Error())
	}
	s.env = env
	logging.InfoContext(ctx, "[RunStep] minted an LLM gateway step key for the command",
		"chat_id", s.input.ChatID, "step_id", s.input.StepID, "request_id", s.requestID,
		"daemon_id", daemonID, "cli", cli.Name, "key", key)
	return env, ""
}

// stepKeyMachine is the daemon the command will run on, which must have
// registered daemon.CapabilityRunStepEnv, or the refusal saying why not.
func (a *ExecuteRunStepActivity) stepKeyMachine(ctx context.Context, execCtx *RunExecutorContext) (string, string) {
	daemonID := ""
	if execCtx.DaemonSelector != nil {
		daemonID = execCtx.DaemonSelector.ID
	}
	if daemonID == "" {
		if a.router == nil {
			return "", "the machine this step runs on could not be resolved to check that it applies step keys"
		}
		id, err := toolexec.ResolveDaemonIDForSelector(ctx, a.router, execCtx.UserID, execCtx.DaemonSelector)
		if err != nil || id == "" {
			return "", "the machine this step runs on could not be resolved to check that it applies step keys"
		}
		daemonID = id
	}
	d, err := a.repo.GetDaemon(ctx, daemonID)
	if err != nil && !svcerr.IsNotFound(err) {
		return "", "could not read the registration of machine " + shortMachineID(daemonID)
	}
	var caps *string
	if d != nil {
		caps = d.Capabilities
	}
	if !gitpublish.HasCapability(caps, daemon.CapabilityRunStepEnv) {
		return "", fmt.Sprintf("llm_gateway needs a machine whose daemon applies step keys: daemon %s did not register %q "+
			"(its reliant predates it, or it has not reconnected since an upgrade)", shortMachineID(daemonID), daemon.CapabilityRunStepEnv)
	}
	// The command goes to the machine that was checked, and nowhere else.
	execCtx.DaemonSelector = &toolexec.DaemonSelector{ID: daemonID}
	return daemonID, ""
}

// stepKeyStepID is the step the key is attributed to: the node's path in the
// run, else its id.
func stepKeyStepID(input ExecuteRunStepInput) string {
	if input.NodePath != "" {
		return input.NodePath
	}
	return input.StepID
}

// mintRefusedCleanly reports whether a failed mint certainly made no key: the
// control plane answered with a refusal, or has no such service.
func mintRefusedCleanly(err error) bool {
	var refused *controlplane.StepKeyError
	return errors.Is(err, controlplane.ErrStepKeysUnsupported) || errors.As(err, &refused)
}

// mintRefusalReason is a failed mint as the step reports it.
func mintRefusalReason(err error) string {
	if errors.Is(err, controlplane.ErrStepKeysUnsupported) {
		return "this deployment cannot issue LLM gateway step keys"
	}
	var refused *controlplane.StepKeyError
	if errors.As(err, &refused) {
		switch refused.Reason {
		case controlplane.ReasonStepKeyNoBillingOrg:
			return "the run's owner cannot bill the run's organization for LLM usage (" + refused.Error() + ")"
		case controlplane.ReasonStepKeyCreditExhausted:
			return "the organization's Reliant credits are used up; add credits to run this step (" + refused.Error() + ")"
		case controlplane.ReasonStepKeyNoModels:
			return "none of the requested models is served by the gateway for this CLI (" + refused.Error() + ")"
		}
		return "the control plane refused the step key: " + refused.Error()
	}
	return "the step key could not be minted: " + err.Error()
}

// redact is s with the attempt's key replaced by "[redacted: NAME]".
func (s *stepKeyScope) redact(text string) string {
	if s == nil {
		return text
	}
	return s.env.Redact(text)
}

// keep leaves the key live for the retry that reattaches to the still
// running command: this attempt was released, not ended.
func (s *stepKeyScope) keep() {
	if s != nil {
		s.kept = true
	}
}

// finish revokes the key now, if one may be live, and returns the step's
// llm_gateway output block. Safe to call more than once.
func (s *stepKeyScope) finish(ctx context.Context) *RunStepLLMGatewayOutput {
	if s == nil {
		return nil
	}
	s.revoke(ctx)
	return s.out
}

// release is the deferred revoke on every exit: nothing unless a key may be
// live and the attempt did not leave its command to a retry.
func (s *stepKeyScope) release(ctx context.Context) {
	if s == nil || s.kept {
		return
	}
	s.revoke(ctx)
}

func (s *stepKeyScope) revoke(ctx context.Context) {
	if !s.pending || s.issuer == nil {
		return
	}
	s.pending = false
	revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stepKeyRevokeTimeout)
	defer cancel()
	got, err := s.issuer.RevokeStepKey(revokeCtx, s.requestID)
	if err != nil {
		// The key's own lifetime bounds it.
		logging.ErrorContext(revokeCtx, "[RunStep] could not revoke the step's LLM gateway key; it expires on its own",
			"chat_id", s.input.ChatID, "step_id", s.input.StepID, "request_id", s.requestID,
			"key_id", s.key.KeyID, "expires_at", s.key.ExpiresAt, "error", err)
		return
	}
	if got.KeyID != "" {
		s.out.KeyID = got.KeyID
	}
	s.out.SpendUSD, s.out.SpendKnown = got.SpendUSD, got.SpendKnown
	logging.InfoContext(revokeCtx, "[RunStep] revoked the step's LLM gateway key",
		"chat_id", s.input.ChatID, "step_id", s.input.StepID, "request_id", s.requestID,
		"key_id", s.out.KeyID, "revoked", got.Revoked, "spend_usd", got.SpendUSD, "spend_known", got.SpendKnown)
}

// runStepEnvKey carries a run step's own environment on ctx, from
// ExecuteRunStep to RemoteRunExecutor.ExecuteCommand, beside its request id:
// only the step's own command carries it, never the log-file write.
type runStepEnvKey struct{}

func withRunStepEnv(ctx context.Context, env *daemon.StepEnv) context.Context {
	if env.Empty() {
		return ctx
	}
	return context.WithValue(ctx, runStepEnvKey{}, env)
}

func runStepEnvFrom(ctx context.Context) *daemon.StepEnv {
	env, _ := ctx.Value(runStepEnvKey{}).(*daemon.StepEnv)
	return env
}
