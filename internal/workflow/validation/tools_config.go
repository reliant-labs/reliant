// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/tools/toolcatalog"
)

// validateToolsConfigBindings checks every tools_config.tools entry against the
// generated tool catalog.
//
// This is the guard the whole codegen exists for. The runtime already rejects a
// binding for a parameter a tool does not have — but only once that tool is
// actually constructed, on the call that would have used it, which for a node
// deep in a long workflow can be many minutes of real work in. Worse, a
// binding on a tool the filter never admits is never constructed at all, so the
// runtime check never fires and the misconfiguration is invisible forever.
//
// Checking at load time turns both into an immediate, named failure.
func validateToolsConfigBindings(wf *reliantv1.Workflow, result *Result) {
	for _, node := range wf.GetNodes() {
		args := node.GetCallLlm()
		if args == nil {
			continue
		}
		toolBindings := args.GetToolsConfig().GetTools()
		if len(toolBindings) == 0 {
			continue
		}

		// Sorted so a workflow with several bad keys reports them in a
		// stable order rather than Go's randomized map order — an error
		// list that reshuffles between runs is miserable to diff.
		toolNames := make([]string, 0, len(toolBindings))
		for toolName := range toolBindings {
			toolNames = append(toolNames, toolName)
		}
		sort.Strings(toolNames)

		for _, toolName := range toolNames {
			path := []string{"nodes", node.GetId(), "tools_config", "tools"}

			if _, known := toolcatalog.Lookup(toolName); !known {
				result.AddErrorWithSuggestion(
					CategoryToolBinding, path, toolName,
					fmt.Sprintf("no tool named %q exists; tools_config.tools is keyed by tool name", toolName),
					didYouMeanTool(toolName),
				)
				continue
			}

			paramNames := make([]string, 0)
			for paramName := range toolBindings[toolName].GetFields() {
				paramNames = append(paramNames, paramName)
			}
			sort.Strings(paramNames)

			for _, paramName := range paramNames {
				if toolcatalog.IsBindable(toolName, paramName) {
					continue
				}
				result.AddError(
					CategoryToolBinding,
					append(path, toolName),
					paramName,
					unbindableMessage(toolName, paramName),
				)
			}
		}
	}
}

// unbindableMessage explains a rejected parameter. "Exists but may not be
// bound" and "does not exist" are different mistakes: the first needs a reason,
// the second needs a spelling suggestion. Collapsing them into one message
// would send someone hunting for a typo in a parameter they spelled correctly.
func unbindableMessage(toolName, paramName string) string {
	if reason, unbindable := toolcatalog.UnbindableReason(toolName, paramName); unbindable {
		return fmt.Sprintf(
			"%q is a parameter of %q but is deliberately not bindable: %s",
			paramName, toolName, reason)
	}

	bindable := toolcatalog.BindableNames(toolName)
	if len(bindable) == 0 {
		return fmt.Sprintf("tool %q has no bindable parameters, so %q cannot be set",
			toolName, paramName)
	}
	return fmt.Sprintf("tool %q has no parameter %q; it accepts: %s",
		toolName, paramName, strings.Join(bindable, ", "))
}

// didYouMeanTool suggests a real tool name for a misspelled one. A bare
// "no such tool" against a fifty-entry registry sends the author to grep;
// naming the near-miss usually ends the problem on the spot.
func didYouMeanTool(toolName string) string {
	lowered := strings.ToLower(toolName)
	var near []string
	for _, candidate := range toolcatalog.Tools() {
		if strings.Contains(strings.ToLower(candidate), lowered) ||
			strings.Contains(lowered, strings.ToLower(candidate)) {
			near = append(near, candidate)
		}
	}
	if len(near) == 0 {
		return ""
	}
	return "did you mean: " + strings.Join(near, ", ")
}
