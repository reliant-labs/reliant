// Copyright (c) 2025 Reliant Labs

package toolexec

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/llm/tools/names"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// ── An org's forced `credentials: none` (org-policies §4.6) ───────────
//
// Under machines.force_credentials_none every command of the org's runs runs
// with `credentials: none`, whatever the step's author or the model wrote.
// Every tool request of a run passes RemoteExecutor.ExecuteTool, which applies
// it once, before the request is placed (applyMachinePolicy):
//
//   - The request is stamped (daemonpolicy.OrgStamp in its context map). The
//     tool executor that runs it, on the worker or on a machine that reads
//     the stamp, wraps the daemon client the tool receives
//     (daemon.ForceCredentialsNone), so every command a tool builds is forced.
//   - A tool whose command a machine builds from the tool's input (the shell
//     tool, and with it every workflow run node) gets its input rewritten:
//     `credentials: none`, and the neutral environment over its `env`. A
//     machine that predates the `credentials` field ignores it but applies
//     `env`, so against an old daemon the environment half still holds
//     (tokens, the credential helper, the ssh agent, GH_* and cloud
//     variables are gone; the credential FILES stay readable, which only the
//     sandbox of a current daemon hides).
//   - A tool that builds a command on the machine and has no such input
//     (prove_test) can be forced only by a machine that reads the stamp, which
//     the worker cannot tell from one that does not: it is refused while the
//     rule is enforced, never run with credentials.
//
// commandTools lists every such tool; TestEveryCommandBuildingSiteIsCovered
// fails when a command is built anywhere it does not account for.

// commandReach is how the worker forces `credentials: none` on a tool that
// runs a command on a machine.
type commandReach int

const (
	// reachInput: the machine builds the command from the tool's input,
	// which carries `credentials` and `env`; rewritten here.
	reachInput commandReach = iota + 1
	// reachStampOnly: the machine builds the command with no such input;
	// only the stamp reaches it. Refused while the rule is enforced.
	reachStampOnly
)

// commandTools are the tools that run a command on a machine.
var commandTools = map[string]commandReach{
	names.ToolShell:     reachInput,
	names.ToolProveTest: reachStampOnly,
}

// ErrorCodeOrgPolicy is the ToolResult.ErrorCode of a call an org policy
// refused.
const ErrorCodeOrgPolicy = "ORG_POLICY_REFUSED"

// applyMachinePolicy applies the machines section of the run's org policy to
// req: it stamps req and rewrites its input when the rule is enforced, counts
// what it would change when the rule is audited, and returns a refusal result
// for a call it must not run. nil lets the call proceed.
func (e *RemoteExecutor) applyMachinePolicy(ctx context.Context, req *ToolRequest, startTime time.Time) *ToolResult {
	if req.OrgID == "" {
		return nil
	}
	d := orgpolicy.ForceCredentialsNone(ctx, req.OrgID)
	if !d.Applies {
		return nil
	}
	if d.Enforced {
		// One stamp per request: the MCP rule's, when it has one, gains the
		// machines rule.
		if req.orgStamp == nil {
			req.orgStamp = &daemonpolicy.OrgStamp{OrgID: req.OrgID}
		} else {
			stamp := *req.orgStamp
			req.orgStamp = &stamp
		}
		if d.Version > req.orgStamp.Version {
			req.orgStamp.Version = d.Version
		}
		req.orgStamp.Credentials = daemonpolicy.CredentialsNone
	}
	switch commandTools[req.ToolName] {
	case reachInput:
		// A call that already asks for `credentials: none` changes nothing
		// worth counting, but its env is still neutralised under
		// enforcement: a daemon that predates the field applies env alone,
		// and a caller's credential variables must not reach it either way.
		if !inputCredentialsNone(req.ToolInput) {
			d.Record(ctx, "tool", req.ToolName, "chat_id", req.ChatID)
		}
		if !d.Enforced {
			return nil
		}
		forced, err := withCredentialsNoneInput(req.ToolInput)
		if err != nil {
			// An input that cannot be rewritten must not run with
			// credentials it was meant to lose.
			return orgPolicyRefusal(startTime, "Your organization runs agent commands without credentials, and this command's input could not be rewritten to do so: "+err.Error())
		}
		req.ToolInput = forced
	case reachStampOnly:
		d.Record(ctx, "tool", req.ToolName, "chat_id", req.ChatID)
		if d.Enforced {
			return orgPolicyRefusal(startTime, fmt.Sprintf("Your organization runs agent commands without credentials, which %s cannot guarantee on every machine. Run the command with the shell tool instead.", req.ToolName))
		}
	}
	return nil
}

// inputCredentialsNone reports whether a tool input already asks for
// `credentials: none`.
func inputCredentialsNone(input string) bool {
	var in struct {
		Credentials string `json:"credentials"`
	}
	if json.Unmarshal([]byte(input), &in) != nil {
		return false
	}
	c, err := daemonpolicy.ParseCredentials(in.Credentials)
	return err == nil && c == daemonpolicy.CredentialsNone
}

// withCredentialsNoneInput is a command tool's input with `credentials: none`
// and the neutral environment laid over its own `env`
// (daemonpolicy.NeutralizeEnv). Every other field is kept as it was.
func withCredentialsNoneInput(input string) (string, error) {
	var in map[string]json.RawMessage
	if err := json.Unmarshal([]byte(input), &in); err != nil {
		return "", fmt.Errorf("parse input: %w", err)
	}
	if in == nil {
		in = map[string]json.RawMessage{}
	}
	var own map[string]string
	if raw, ok := in["env"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &own); err != nil {
			return "", fmt.Errorf("env is not an object of strings: %w", err)
		}
	}
	env, err := json.Marshal(daemonpolicy.NeutralizeEnv(own))
	if err != nil {
		return "", err
	}
	creds, err := json.Marshal(daemonpolicy.CredentialsNone)
	if err != nil {
		return "", err
	}
	in["env"], in["credentials"] = env, creds
	out, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func orgPolicyRefusal(startTime time.Time, msg string) *ToolResult {
	return &ToolResult{
		Success:      false,
		IsError:      true,
		Content:      msg,
		ErrorMessage: "refused by the organization's machines policy",
		ErrorCode:    ErrorCodeOrgPolicy,
		StartTime:    startTime,
		EndTime:      time.Now(),
	}
}
