// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"encoding/json"

	"go.temporal.io/sdk/workflow"
)

// v1.29.0 shipped two workflow keys that were later removed: a workflow's
// `requires.cli_login` (vendor agent CLI sign-ins checked before the first
// node) and a run node's `llm_gateway` (a step key for a third-party agent
// CLI). A definition that still declares either no longer loads: the load
// activity's parse refuses it. A run recorded while they existed carries them
// in its history, though, and must replay as it was recorded. This file is
// what keeps it replaying.

// cliLoginRemovedChangeID gates the removal of requires.cli_login. A root run
// recorded while it existed, whose definition declared it, counted its
// sign-ins as a requirement on the machine: it scheduled PreflightRequirements
// at startup (and on each placed pool member) even when it required nothing
// else there. Such a history has no marker and keeps that schedule; a run
// that reaches this point live records the marker and checks tools, repos
// and integrations only. Asked only when the recorded definition declared
// cli_login, so no other run records it.
const cliLoginRemovedChangeID = "requires-cli-login-removed"

// legacyRequiresCLILogin returns the requires.cli_login a root run recorded
// before the removal declared, or nil: for a child (its root checked), for a
// definition without it, and for a run that recorded cliLoginRemovedChangeID.
// workflowJSON is the definition ActivityLoadWorkflow returned, read from the
// run's history on replay.
func legacyRequiresCLILogin(ctx workflow.Context, workflowJSON []byte, execCtx *ExecutionContext) []string {
	if execCtx != nil && execCtx.Parent != nil {
		return nil
	}
	names := recordedCLILogin(workflowJSON)
	if len(names) == 0 {
		return nil
	}
	if workflow.GetVersion(ctx, cliLoginRemovedChangeID, workflow.DefaultVersion, 1) != workflow.DefaultVersion {
		return nil
	}
	return names
}

// recordedCLILogin reads requires.cli_login from a definition's protojson
// (field 4 of WorkflowRequirements, now reserved, so the generated type no
// longer decodes it). protojson writes the JSON name; the proto name is
// accepted as well.
func recordedCLILogin(workflowJSON []byte) []string {
	if len(workflowJSON) == 0 {
		return nil
	}
	var def struct {
		Requires struct {
			CliLogin      []string `json:"cliLogin"`
			CliLoginProto []string `json:"cli_login"`
		} `json:"requires"`
	}
	if err := json.Unmarshal(workflowJSON, &def); err != nil {
		return nil
	}
	if len(def.Requires.CliLogin) > 0 {
		return def.Requires.CliLogin
	}
	return def.Requires.CliLoginProto
}

// dropRemovedDefinitionKeys deletes the removed keys from a raw definition
// recorded before their removal, so its replayed template resolution parses
// as it did then: requires.cli_login, and llm_gateway from every run node
// (the graph's, and those nested in loops, inline workflows and handlers),
// whether written in its args or, as the load activity re-marshals a
// definition, on the node itself. A definition loaded since the removal never carries them (the
// load activity refuses it first), so this changes nothing for a new run.
func dropRemovedDefinitionKeys(raw map[string]interface{}) {
	if req, ok := raw["requires"].(map[string]interface{}); ok {
		delete(req, "cli_login")
	}
	dropRunLLMGateway(raw)
}

func dropRunLLMGateway(v interface{}) {
	switch t := v.(type) {
	case map[string]interface{}:
		if t["type"] == "run" {
			delete(t, "llm_gateway")
			if args, ok := t["args"].(map[string]interface{}); ok {
				delete(args, "llm_gateway")
			}
		}
		for _, child := range t {
			dropRunLLMGateway(child)
		}
	case []interface{}:
		for _, child := range t {
			dropRunLLMGateway(child)
		}
	}
}
