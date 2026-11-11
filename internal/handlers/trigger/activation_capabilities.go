// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	runtime "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// The capability items an activation's approval compares
// (automationpolicy.Capabilities). Each is display-safe: ids and names pass
// through discloseScope, and anything the engine cannot pin down before a run
// is the item's "dynamic" form, so it still compares — a step that moves from
// a fixed connection to a dynamic one widens power.
const dynamicCapability = "dynamic"

// approvalsDecideCapability is held by a closure with a step that calls
// Reliant's ApprovalService to approve or deny. Whether the credential it
// carries can decide is not knowable here (a saved connection or hosted
// secret is opaque to this server), so the step's target is the signal: any
// token that permits approvals:decide, reliant:api included, is treated as
// able to.
const approvalsDecideCapability = automationpolicy.CapabilityApprovalsDecide

// prodDeploy matches a fixed shell command that deploys or promotes to a
// production environment: `reliant forge env deploy prod`, `forge env promote
// staging prod`, `deploy --env production`. The engine can tell only from a
// literal command; one an agent composes at run time is not visible here.
var prodDeploy = regexp.MustCompile(`(?i)\b(deploy|promote)\b[^|;&\n]*\b(prod|production)\b`)

// rowCapabilities are the powers the activation row itself grants: who its
// integration writes act as and the machine it runs on. A no-machine
// activation holds no machine.
func rowCapabilities(row *core.Trigger) []string {
	out := []string{"identity:" + string(row.ActingIdentity.OrOwner())}
	if !row.NoMachine && row.DaemonID != "" {
		out = append(out, "machine:"+discloseScope(row.DaemonID))
	}
	return out
}

// connectionCapabilities are the activation's own connection and its scopes.
func connectionCapabilities(c *core.Connection) []string {
	if c == nil {
		return nil
	}
	integration := discloseScope(c.IntegrationID)
	out := []string{"connection:" + integration + "/" + discloseScope(c.ID)}
	for _, scope := range c.Scopes {
		out = append(out, "scope:"+integration+"/"+discloseScope(scope))
	}
	return out
}

// nodeCapabilities are the powers one node of the executable closure uses:
// the connection an integration action authenticates with (its integration's
// default when it names none), a hosted use grant, the machine credentials a
// shell step inherits, a fixed production deploy, and a git push.
func nodeCapabilities(node *pb.Node, values map[string]any, identity string, tainted map[string]bool) []string {
	eval := runtime.NewCELContextBuilder().WithWorkflow("", identity).WithInputs(values)
	// fixed is raw's value when it is a literal or resolves now, else "".
	fixed := func(raw string) string {
		if !strings.Contains(raw, "{{") {
			return raw
		}
		if referencesTainted(raw, tainted) {
			return ""
		}
		v, err := eval.EvalString(raw)
		if err != nil {
			return ""
		}
		s, _ := v.(string)
		return s
	}
	if a := node.GetAction(); a != nil {
		var out []string
		uses := fixed(model.CelStringRaw(a.GetUses()))
		integration, _, _ := strings.Cut(uses, "/")
		if integration == "" {
			integration = dynamicCapability
		}
		switch raw := model.CelStringRaw(a.GetConnection()); {
		case raw == "" && integration != "http":
			out = append(out, "connection:"+discloseScope(integration)+"/default")
		case raw != "":
			if name := fixed(raw); name != "" {
				out = append(out, "connection:"+discloseScope(name))
			} else {
				out = append(out, "connection:"+dynamicCapability)
			}
		}
		if raw := model.CelStringRaw(a.GetHostedUse()); raw != "" {
			if grant := fixed(raw); grant != "" {
				out = append(out, "secret:hosted_use/"+discloseScope(grant))
			} else {
				out = append(out, "secret:hosted_use/"+dynamicCapability)
			}
		}
		if integration == "http" {
			rawURL := model.ActionURL(a)
			if model.TargetsApprovalDecision(rawURL) || model.TargetsApprovalDecision(fixed(rawURL)) {
				out = append(out, approvalsDecideCapability)
			}
		}
		return out
	}
	if r := node.GetRun(); r != nil {
		var out []string
		if r.GetCredentials() != "none" {
			out = append(out, "secret:machine_credentials")
		}
		raw := model.CelStringRaw(r.GetCommand())
		if command := fixed(raw); command != "" && prodDeploy.MatchString(command) {
			out = append(out, "deploy:prod")
		}
		if model.TargetsApprovalDecision(raw) || model.TargetsApprovalDecision(fixed(raw)) {
			out = append(out, approvalsDecideCapability)
		}
		return out
	}
	if node.GetGitPublish() != nil {
		return []string{"connection:github/git_publish"}
	}
	return nil
}

// approvalOperation maps an activation tool to the policy's operation.
func approvalOperation(tool string) automationpolicy.Operation {
	switch tool {
	case tools.ToolCreateTriggerActivation:
		return automationpolicy.OperationCreate
	case tools.ToolEnableTrigger:
		return automationpolicy.OperationEnable
	case tools.ToolPauseTrigger:
		return automationpolicy.OperationPause
	default:
		return automationpolicy.OperationUpdate
	}
}

// secretDeclarationDisclosure is what an activation shows, and what its
// approval compares, for the secrets a workflow declares: each name with the
// environment it lives in. The value is never available here, and a secret's
// access list is the control plane's to enforce; this only makes "this
// automation will send these secrets" visible and part of what is approved.
//
// An environment written as an expression resolves against the activation's
// inputs; one an event can steer (or that does not resolve) is dynamic, which
// still compares, so a move from a fixed environment to a dynamic one widens
// the approval.
func secretDeclarationDisclosure(wf *pb.Workflow, values map[string]any, identity string, tainted map[string]bool) (summary, capabilities []string) {
	names := make([]string, 0, len(wf.GetSecrets()))
	for name := range wf.GetSecrets() {
		names = append(names, name)
	}
	sort.Strings(names)
	eval := runtime.NewCELContextBuilder().WithWorkflow("", identity).WithInputs(values)
	for _, name := range names {
		env := dynamicCapability
		raw := model.CelStringRaw(wf.GetSecrets()[name].GetEnvironment())
		switch {
		case raw == "":
		case !strings.Contains(raw, "{{"):
			env = raw
		case referencesTainted(raw, tainted):
		default:
			if v, err := eval.EvalString(raw); err == nil {
				if s, ok := v.(string); ok && s != "" {
					env = s
				}
			}
		}
		if env != dynamicCapability && !scopeIdentifier.MatchString(env) {
			env = dynamicCapability
		}
		summary = append(summary, fmt.Sprintf("Secret %s: sent in request headers by this workflow's http/request@1 steps; environment=%s; value never visible to the workflow; the secret's access list must allow this workflow", name, env))
		capabilities = append(capabilities, "secret:access_list/"+env+"/"+name)
	}
	return summary, capabilities
}
