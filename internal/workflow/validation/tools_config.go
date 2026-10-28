// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/tools/toolcatalog"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// validateToolsConfigBindings checks every tools_config.tools entry against the
// generated tool catalog, and the tag entries of every tools_config filter
// list against the generated tag vocabulary.
//
// This is the guard the whole codegen exists for. The runtime already rejects a
// binding for a parameter a tool does not have — but only once that tool is
// actually constructed, on the call that would have used it, which for a node
// deep in a long workflow can be many minutes of real work in. Worse, a
// binding on a tool the filter never admits is never constructed at all, so the
// runtime check never fires and the misconfiguration is invisible forever.
//
// Checking at load time turns both into an immediate, named failure.
//
// Nodes inside inline bodies are walked too (see walkWorkflowNodes): a
// call_llm node in a loop body carries the same silent-failure exposure as one
// on the root graph.
func validateToolsConfigBindings(wf *reliantv1.Workflow, result *Result) {
	validateToolInputDefaults(wf.GetInputs(), []string{"inputs"}, result)
	walkWorkflowNodes(wf, nil, func(node *reliantv1.Node, nodePath []string) {
		validateToolInputDefaults(node.GetWorkflow().GetInline().GetInputs(), joinPath(nodePath, "inline", "inputs"), result)
		validateToolInputDefaults(node.GetLoop().GetInline().GetInputs(), joinPath(nodePath, "inline", "inputs"), result)
		args := node.GetCallLlm()
		if args == nil {
			return
		}
		toolsConfig := args.GetToolsConfig()
		if toolsConfig == nil {
			return
		}

		// The filter lists are checked whether or not bindings are present:
		// a tag that matches nothing is its own failure, independent of the
		// per-tool parameter bindings.
		validateToolFilterTags(toolsConfig.GetPreloadedTools(), joinPath(nodePath, "tools_config", "preloaded_tools"), result)
		validateToolFilterTags(toolsConfig.GetLoadableTools(), joinPath(nodePath, "tools_config", "loadable_tools"), result)

		toolBindings := toolsConfig.GetTools()
		if len(toolBindings) == 0 {
			return
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
			path := joinPath(nodePath, "tools_config", "tools")

			// A tools block supplied by expression — `tools: "{{inputs.x}}"`,
			// the path a preset's tool parameters travel — is carried as the
			// CEL sentinel and is not a tool name. Its CONTENT cannot be
			// checked here: it does not exist until the run has inputs, and
			// which preset supplies it is not known at load time.
			//
			// This is a real and deliberate reduction in what this layer
			// catches, limited to the entry that opted into it. The runtime
			// still rejects an unknown parameter when the binding is applied
			// (ToolWrapper.WithBindings), so a bad preset key degrades to a
			// logged, unapplied binding rather than a silent success. Every
			// literal entry beside it is still checked in full.
			if toolName == wfyaml.CELExprSentinelKey {
				continue
			}

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
	})
}

// validateToolFilterTags checks the `tag:X` entries of a tools_config filter
// list (preloaded_tools, loadable_tools) against the generated tag vocabulary.
//
// A tag entry that names no known tag selects NOTHING: ExpandToolFilter looks
// the tag up in the registry's index and, finding no carriers, moves on —
// no error, no empty-set warning. The workflow then runs with a starting
// bundle smaller than its author believed, which for preloaded_tools can mean
// an agent with zero tools. This is the filter-list twin of the binding check
// above: the name is static, so the check is too.
//
// Only tag entries are checked. A plain name may be an MCP tool
// (mcp__server__tool), which exists only at run time against the chat's
// connected servers, and a glob may match anything: neither can be judged
// from the static catalog, and neither is dead the way an unknown tag is.
// `!exclusions` are skipped for the same reason in reverse — an exclusion of
// a tag nothing carries excludes nothing, which is harmless.
//
// A warning, not an error: unlike a binding, the entry is syntactically fine
// and the list around it still applies, so this reports the dead entry
// without failing the workflow that carries it.
func validateToolFilterTags(list *reliantv1.CelStringList, path []string, result *Result) {
	if list == nil || model.CelStringListIsExpr(list) {
		// The whole list is a CEL expression ("{{inputs.tools}}") — its
		// entries do not exist until the run binds it.
		return
	}
	for _, entry := range model.CelStringListValue(list) {
		if err := toolFilterTagError(entry); err != nil {
			result.AddWarning(CategoryToolBinding, path, entry, err.Error())
		}
	}
}

// toolFilterTagError is shared by literal node filters and tools-typed input
// values. Expressions and runtime-only names remain unjudged.
func toolFilterTagError(entry string) error {
	if containsTemplate(entry) {
		return nil
	}
	tag, isTag := strings.CutPrefix(entry, "tag:")
	if !isTag {
		return nil
	}
	if slices.Contains(toolcatalog.Tags(), tag) {
		return nil
	}
	return fmt.Errorf("tool tag %q is not in the tool-tag vocabulary, so this entry matches no tool and contributes nothing to the filter (known tags: %s)", tag, strings.Join(toolcatalog.Tags(), ", "))
}

// Defaults must be checked before binding: an expression forwarding inputs.tools
// hides the literal entries from the terminal call_llm's tools_config.
func validateToolInputDefaults(inputs map[string]*reliantv1.Input, path []string, result *Result) {
	for name, input := range inputs {
		if model.IsGroupInput(input) {
			validateToolInputDefaults(model.GetGroupInputs(input), joinPath(path, name), result)
			continue
		}
		if input.GetType() != "tools" || input.GetToolsInput().GetDefault() == nil {
			continue
		}
		value := input.GetToolsInput().GetDefault().AsInterface()
		if err := validateProtoInputType(name, value, input, nil); err != nil {
			result.AddError(CategoryInput, joinPath(path, name), "default", err.Error())
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
