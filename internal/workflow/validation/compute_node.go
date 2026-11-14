// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"sort"

	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// validateComputeNode checks a compute node's declaration: at least one output,
// each named like any other output. Their expressions are type-checked by the
// CEL pass (validateValueTemplates) in the scope the node runs in.
func validateComputeNode(node *reliantv1.Node, nodePath []string, result *Result) {
	args := node.GetCompute()
	if args == nil || len(args.GetOutputs()) == 0 {
		result.AddErrorWithSuggestion(CategoryStructure, nodePath, "outputs",
			"compute node declares no outputs",
			"add at least one: outputs: {size: \"{{ inputs.adds + inputs.dels < 60 ? 'S' : 'M' }}\"}")
		return
	}
	names := make([]string, 0, len(args.GetOutputs()))
	for name := range args.GetOutputs() {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if verr := validateOutputName(name); verr != nil {
			result.AddError(CategoryStructure, nodePath, "outputs."+name, verr.message)
		}
	}
}

// rejectActionParamEnclosingRefs reports an action param's {{ }} read of a
// node outside the inline body the action runs in: a read that can only fail
// at run time. Action params are not otherwise compiled here (their templates
// are left to run time, validateActionNode), so this is the one CEL check
// they get, and it can refuse no workflow that could ever have run.
func (s *workflowScope) rejectActionParamEnclosingRefs(v *structpb.Value, path []string, result *Result) {
	switch k := v.GetKind().(type) {
	case *structpb.Value_StringValue:
		if !containsTemplate(k.StringValue) {
			return
		}
		for _, match := range extractTemplateExpressions(k.StringValue) {
			if s.rejectEnclosingNodeRefs(match.Expr, path, result) {
				return
			}
		}
	case *structpb.Value_StructValue:
		for _, key := range sortedValueKeys(k.StructValue.GetFields()) {
			s.rejectActionParamEnclosingRefs(k.StructValue.GetFields()[key], appendPath(path, key), result)
		}
	case *structpb.Value_ListValue:
		for i, item := range k.ListValue.GetValues() {
			s.rejectActionParamEnclosingRefs(item, appendPath(path, fmt.Sprintf("[%d]", i)), result)
		}
	}
}

// validateValueTemplates type-checks every {{ }} template inside a
// google.protobuf.Value tree (a map value may be a string, a list or a
// nested map), reporting each at its path.
func (s *workflowScope) validateValueTemplates(v *structpb.Value, path []string, scope *nodeOrderScope, result *Result) {
	switch k := v.GetKind().(type) {
	case *structpb.Value_StringValue:
		if containsTemplate(k.StringValue) {
			s.validateTemplate(k.StringValue, path, siteNodeResolution, s.loop, scope, nil, result)
		}
	case *structpb.Value_StructValue:
		keys := make([]string, 0, len(k.StructValue.GetFields()))
		for key := range k.StructValue.GetFields() {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			s.validateValueTemplates(k.StructValue.GetFields()[key], appendPath(path, key), scope, result)
		}
	case *structpb.Value_ListValue:
		for i, item := range k.ListValue.GetValues() {
			s.validateValueTemplates(item, appendPath(path, fmt.Sprintf("[%d]", i)), scope, result)
		}
	}
}
