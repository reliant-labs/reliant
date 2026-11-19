// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/tools/names"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// validateInvokeToolNode checks an invoke_tool node's tool against the opt-in
// (names.NodeExposedToolNames, the mirror of tools.NodeExposedTools). Only
// tools that opted in may run as a node, and the runtime refuses any other on
// every attempt; caught here, the mistake is a save-time error instead of a
// run that fails, possibly unattended and long after it was written (prod
// 23295ad3: control-plane main-red-fix's report_bug node). A tool written as a
// {{expr}} template is left to the runtime check.
func validateInvokeToolNode(node *reliantv1.Node, nodePath []string, result *Result) {
	args := node.GetInvokeTool()
	if args == nil {
		result.AddError(CategoryStructure, nodePath, "args", "invoke_tool node missing args")
		return
	}
	if !model.CelStringIsSet(args.GetTool()) {
		result.AddError(CategoryStructure, nodePath, "tool", "required")
		return
	}
	if model.CelStringIsExpr(args.GetTool()) {
		return
	}
	tool := strings.TrimSpace(model.CelStringRaw(args.GetTool()))
	if tool == "" {
		result.AddError(CategoryStructure, nodePath, "tool", "required")
		return
	}
	if !names.IsNodeExposedToolName(tool) {
		result.AddError(CategoryStructure, nodePath, "tool", fmt.Sprintf(
			"tool %q cannot run as an invoke_tool node; the tools that can are %s. To use any other tool, give it to an agent (a call_llm or agent node's tools)",
			tool, strings.Join(names.NodeExposedToolNames, ", ")))
	}
}
