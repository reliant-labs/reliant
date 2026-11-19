package cli

// The env's promotion checks — delivery_policy.required_gates and
// delivery_policy.approval_hook — enforced where the env's ledger lives.
//
// WHO ENFORCES. An env with a control plane is judged BY THE CONTROL PLANE, at
// promote, in the promotion's transaction: every client and every token
// reaches that one write, so a check there cannot be skipped by using another
// client or omitting a flag. forge does nothing for such an env here. An env
// with NO control plane keeps its ledger on this machine (or in the offline
// bindings forge writes), so forge's own promote is the one write, and the
// checks run here, before it — the same place, in the only sense that
// matters.
//
// WHAT IS RECORDED. Each check that ran leaves a gate on the promotion, under
// the reserved names release.RequiredGatesGateName and
// release.ApprovalHookGateName: what was required or asked, what it answered,
// and how the check decided. A caller-supplied gate of either name is
// refused while the policy declares the check, because it would forge the
// check's own verdict.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// promotionCheckInput is the promotion the checks judge.
type promotionCheckInput struct {
	Project, Env, Version, FromEnv, Commit, Promoter, Note string
	// Gates are the gates the promotion carries (--gate).
	Gates []release.Gate
	// Ask is false for a run that writes nothing (--plan, --plan-only): the
	// hook is not asked, because asking can open an approval request.
	Ask bool
	// Dir is where an argv hook runs: the project root.
	Dir string
}

// approvalAsker asks one hook and returns its answer. An error is a hook that
// timed out, failed or answered something unreadable.
type approvalAsker func(ctx context.Context, hook release.ShapeApprovalHook, req release.ApprovalRequest, dir string) (release.ApprovalDecision, error)

// askApprovalHook is the production asker: argv or url.
var askApprovalHook approvalAsker = func(ctx context.Context, hook release.ShapeApprovalHook, req release.ApprovalRequest, dir string) (release.ApprovalDecision, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return release.ApprovalDecision{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(hook.TimeoutSeconds)*time.Second)
	defer cancel()
	if len(hook.Argv) > 0 {
		return askArgvHook(ctx, hook, body, dir)
	}
	return askURLHook(ctx, hook, body)
}

// askArgvHook runs the command with no shell, the request on stdin. Exit 0
// with no output approves; exit 0 with output is read as a decision; any
// other exit denies, with its stderr as the reason.
func askArgvHook(ctx context.Context, hook release.ShapeApprovalHook, body []byte, dir string) (release.ApprovalDecision, error) {
	cmd := exec.CommandContext(ctx, hook.Argv[0], hook.Argv[1:]...) //nolint:gosec // the env's own declared command, run with no shell
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(body)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return release.ApprovalDecision{}, fmt.Errorf("%s did not answer within %ds", hook.Argv[0], hook.TimeoutSeconds)
	}
	var exit *exec.ExitError
	switch {
	case errors.As(runErr, &exit):
		return release.ApprovalDecision{Decision: release.ApprovalDeny, Reason: lastLine(stderr.String(), fmt.Sprintf("%s exited %d", hook.Argv[0], exit.ExitCode()))}, nil
	case runErr != nil:
		return release.ApprovalDecision{}, fmt.Errorf("running %s: %w", hook.Argv[0], runErr)
	case strings.TrimSpace(stdout.String()) == "":
		return release.ApprovalDecision{Decision: release.ApprovalApprove}, nil
	}
	return release.ParseApprovalDecision(stdout.Bytes())
}

// askURLHook POSTs the request. A 2xx answer is read as a decision; anything
// else is an error, which an enforcing check reads as deny.
func askURLHook(ctx context.Context, hook release.ShapeApprovalHook, body []byte) (release.ApprovalDecision, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.URL, bytes.NewReader(body))
	if err != nil {
		return release.ApprovalDecision{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "forge-approval-hook/1")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return release.ApprovalDecision{}, fmt.Errorf("the hook did not answer within %ds", hook.TimeoutSeconds)
		}
		return release.ApprovalDecision{}, fmt.Errorf("asking the hook: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return release.ApprovalDecision{}, fmt.Errorf("reading the hook's answer: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return release.ApprovalDecision{}, fmt.Errorf("the hook answered HTTP %d", resp.StatusCode)
	}
	return release.ParseApprovalDecision(answer)
}

func lastLine(s, fallback string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if l := strings.TrimSpace(lines[len(lines)-1]); l != "" {
		return l
	}
	return fallback
}

// checkPromotionPolicy renders the env's delivery policy and, for an env
// whose ledger is not a control plane, judges the promotion against it. It
// returns the gates the checks record on the promotion.
func checkPromotionPolicy(ctx context.Context, out io.Writer, projectDir string, ledger envLedger, in promotionCheckInput) ([]release.Gate, error) {
	if ledger.Hosted {
		// The control plane judges it, in the promotion's transaction.
		return nil, nil
	}
	entities, err := RenderKCL(ctx, projectDir, in.Env)
	if err != nil || entities == nil {
		// The deploy renders the env again and reports its own error; a
		// policy that cannot be read here is one the plan cannot show either.
		return nil, nil //nolint:nilerr // the plan reports a render failure on its own terms
	}
	return judgePromotionPolicy(ctx, out, entities.DeliveryPolicy, in, askApprovalHook)
}

// judgePromotionPolicy is the checks themselves: required gates first (they
// cost nothing and refuse without asking anyone), then the hook.
func judgePromotionPolicy(ctx context.Context, out io.Writer, pol *release.ShapeDeliveryPolicy, in promotionCheckInput, ask approvalAsker) ([]release.Gate, error) {
	if pol == nil {
		return nil, nil
	}
	required, hook := pol.RequiredGates, pol.ApprovalHook
	if required != nil && (required.Mode == release.DeliveryOff || len(required.Names) == 0) {
		required = nil
	}
	if hook != nil && hook.Mode == release.DeliveryOff {
		hook = nil
	}
	if required == nil && hook == nil {
		return nil, nil
	}
	for _, g := range in.Gates {
		if g.Name == release.RequiredGatesGateName || g.Name == release.ApprovalHookGateName {
			return nil, exitCodeError{code: exitWrong, msg: fmt.Sprintf(
				"--gate %s: %q is the name env %s's delivery policy records its own verdict under; name your check something else",
				g.Name, g.Name, in.Env)}
		}
	}

	var recorded []release.Gate
	if required != nil {
		v := release.JudgeRequiredGates(required.Names, in.Gates)
		gate := release.Gate{
			Name: release.RequiredGatesGateName, Status: release.GateStatusPassed, Summary: v.Summary(),
			Details: map[string]any{"mode": string(required.Mode), "required": required.Names, "outcome": "passed"},
		}
		if !v.OK() {
			if required.Mode == release.DeliveryEnforce {
				return nil, &promoteRefusedError{Reason: v.Reason(), Detail: fmt.Sprintf(
					"%s's delivery_policy.required_gates requires %s passed on the promotion; %s",
					in.Env, strings.Join(required.Names, ", "), v.Summary())}
			}
			gate.Status, gate.Summary = release.GateStatusFailed, "AUDIT (would refuse): "+v.Summary()
			gate.Details["outcome"] = "audit"
			fmt.Fprintf(out, "[deploy] Note: %s's required_gates would refuse this promotion (%s); mode audit records it and admits\n", in.Env, v.Summary())
		}
		recorded = append(recorded, gate)
	}

	if hook != nil && in.Ask {
		gate, err := judgeApprovalHook(ctx, out, *hook, in, recorded, ask)
		if err != nil {
			return nil, err
		}
		recorded = append(recorded, gate)
	}
	return recorded, nil
}

// judgeApprovalHook asks the hook and decides. In enforce, anything but
// approve refuses — a hook that failed is a deny (fail closed).
func judgeApprovalHook(ctx context.Context, out io.Writer, hook release.ShapeApprovalHook, in promotionCheckInput, prior []release.Gate, ask approvalAsker) (release.Gate, error) {
	req := release.ApprovalRequest{
		Schema: release.ApprovalRequestSchema, Project: in.Project, Env: in.Env, Release: in.Version,
		FromEnv: in.FromEnv, Commit: in.Commit, Promoter: in.Promoter, Note: in.Note,
		Gates: append(append([]release.Gate{}, in.Gates...), prior...),
	}
	asked := hook.URL
	if len(hook.Argv) > 0 {
		asked = strings.Join(hook.Argv, " ")
	}
	decision, err := ask(ctx, hook, req, in.Dir)
	outcome := "answered"
	if err != nil {
		decision = release.ApprovalDecision{Decision: release.ApprovalDeny, Reason: err.Error()}
		outcome = "failed"
	}
	gate := release.Gate{
		Name: release.ApprovalHookGateName, URL: decision.URL,
		Summary: decision.Decision + summarySuffix(decision.Reason),
		Details: map[string]any{"mode": string(hook.Mode), "hook": asked, "decision": decision.Decision, "outcome": outcome},
	}
	switch decision.Decision {
	case release.ApprovalApprove:
		gate.Status = release.GateStatusPassed
		return gate, nil
	case release.ApprovalPending:
		gate.Status = release.GateStatusError
	default:
		gate.Status = release.GateStatusFailed
	}
	if hook.Mode == release.DeliveryAudit {
		gate.Summary = "AUDIT (would refuse): " + gate.Summary
		gate.Details["outcome"] = "audit"
		fmt.Fprintf(out, "[deploy] Note: %s's approval hook answered %s; mode audit records it and admits\n", in.Env, gate.Summary)
		return gate, nil
	}
	reason, detail := release.RefusalApprovalDenied, fmt.Sprintf("%s's approval hook (%s) denied %s%s", in.Env, asked, in.Version, summarySuffix(decision.Reason))
	if outcome == "failed" {
		detail = fmt.Sprintf("%s's approval hook (%s) could not be asked, which denies (fail closed): %s", in.Env, asked, decision.Reason)
	}
	if decision.Decision == release.ApprovalPending {
		reason = release.RefusalApprovalPending
		detail = fmt.Sprintf("%s's approval hook (%s) has not decided on %s yet%s", in.Env, asked, in.Version, summarySuffix(decision.Reason))
		if decision.URL != "" {
			detail += "; approve it at " + decision.URL
		}
	}
	return gate, &promoteRefusedError{Reason: reason, Detail: detail}
}

func summarySuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return ": " + reason
}
