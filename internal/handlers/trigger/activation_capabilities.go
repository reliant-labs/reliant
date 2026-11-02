// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"regexp"
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
		return out
	}
	if r := node.GetRun(); r != nil {
		var out []string
		if r.GetCredentials() != "none" {
			out = append(out, "secret:machine_credentials")
		}
		if command := fixed(model.CelStringRaw(r.GetCommand())); command != "" && prodDeploy.MatchString(command) {
			out = append(out, "deploy:prod")
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
