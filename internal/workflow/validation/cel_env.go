// Copyright (c) 2025 Reliant Labs
package validation

import (
	"reflect"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// =============================================================================
// NODE OUTPUT CEL TYPES
// =============================================================================

// nodeOutputTypeName returns a synthetic CEL type name for a node's output,
// encoding the node ID so the custom type provider can resolve fields.
// Format: "node_output.{nodeID}"
func nodeOutputTypeName(nodeID string) string {
	return "node_output." + nodeID
}

func encodedNodeCELIdentifier(nodeID string) string {
	return strings.NewReplacer("-", "_", ".", "_", ":", "_").Replace(nodeID)
}

func nodeCELVariableName(nodeID string) string {
	return "nodes_" + encodedNodeCELIdentifier(nodeID)
}

// getNodeOutputCELType returns the CEL type for a node's output using the registry.
// Returns nil if the registry doesn't know about this node type's outputs.
func getNodeOutputCELType(registry *wfcel.TypeRegistry, nodeType, nodeID string) *cel.Type {
	if !registryHasOutput(registry, nodeType) {
		return nil
	}
	return cel.ObjectType(nodeOutputTypeName(nodeID))
}

// =============================================================================
// VALIDATION CEL ENVIRONMENT
// =============================================================================

// newValidationCELEnv creates a CEL environment for validation with the specified
// namespaces and optional WorkflowTypeContext for deep type checking.
func newValidationCELEnv(namespaces []wfcel.CELNamespace, typeCtx *WorkflowTypeContext) (*cel.Env, error) {
	var opts []cel.EnvOption

	opts = append(opts, cel.StdLib())
	opts = append(opts, cel.OptionalTypes())
	opts = append(opts, cel.CrossTypeNumericComparisons(true))

	// Register context types with CEL for native field validation.
	// When we have typed iter item fields, skip registering IterContext as a native
	// type so our custom type provider can control field resolution on iter.item.
	if typeCtx != nil && typeCtx.IterItemFields != nil {
		opts = append(opts, ext.NativeTypes(
			ext.ParseStructTag("json"),
			reflect.TypeOf(&model.WorkflowContext{}),
		))
	} else {
		opts = append(opts, ext.NativeTypes(
			ext.ParseStructTag("json"),
			reflect.TypeOf(&model.WorkflowContext{}),
			reflect.TypeOf(&model.IterContext{}),
		))
	}

	// Add namespace variable declarations
	for _, ns := range namespaces {
		opts = append(opts, getNamespaceDecl(ns, typeCtx))
	}

	// Add typed node variables when we have workflow context.
	if typeCtx != nil {
		for nodeID, nodeType := range typeCtx.NodeTypes {
			if hasDynamicOutputs(typeCtx, nodeID) {
				opts = append(opts, cel.Variable(nodeCELVariableName(nodeID), cel.DynType))
				continue
			}
			celType := getNodeOutputCELType(typeCtx.Registry, nodeType, nodeID)
			if celType != nil {
				opts = append(opts, cel.Variable(nodeCELVariableName(nodeID), celType))
			}
		}
	}

	// Enable extended string functions (trimPrefix, trimSuffix, replace, split, join, format, etc.)
	opts = append(opts, ext.Strings())

	// Add custom functions
	opts = append(opts, wfcel.CustomFunctions()...)

	baseEnv, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, err
	}

	// If we have workflow type context, extend with custom type provider
	if typeCtx != nil {
		customProvider := newWorkflowTypeProvider(
			baseEnv.CELTypeProvider(),
			typeCtx,
		)
		return baseEnv.Extend(cel.CustomTypeProvider(customProvider))
	}

	return baseEnv, nil
}

// newSaveMessageCELEnv creates a CEL environment specifically for save_message validation
// with typed output namespace based on the current node's type.
func newSaveMessageCELEnv(nodeType, nodeID string, typeCtx *WorkflowTypeContext) (*cel.Env, error) {
	var nodeTypeCtx *WorkflowTypeContext
	if typeCtx != nil {
		nodeTypeCtx = &WorkflowTypeContext{
			InputFields:      typeCtx.InputFields,
			InputGroups:      typeCtx.InputGroups,
			NodeOutputs:      typeCtx.NodeOutputs,
			OutputFields:     typeCtx.OutputFields,
			NodeTypes:        typeCtx.NodeTypes,
			Registry:         typeCtx.Registry,
			ConditionalNodes: typeCtx.ConditionalNodes,
			LenientInputs:    typeCtx.LenientInputs,
		}
	} else {
		nodeTypeCtx = &WorkflowTypeContext{
			InputFields:  make(map[string]*FieldInfo),
			InputGroups:  make(map[string]map[string]*FieldInfo),
			NodeOutputs:  make(map[string]map[string]*FieldInfo),
			OutputFields: make(map[string]*FieldInfo),
			NodeTypes:    make(map[string]string),
			Registry:     sharedRegistry,
		}
	}

	nodeTypeCtx.CurrentNodeID = nodeID
	if nodeType == model.NodeTypeWorkflow || nodeType == model.NodeTypeLoop {
		nodeTypeCtx.CurrentNodeOutputType = nil
	} else {
		nodeTypeCtx.CurrentNodeOutputType = getNodeOutputCELType(nodeTypeCtx.Registry, nodeType, nodeID)
	}

	namespaces := []wfcel.CELNamespace{
		wfcel.CELInputs,
		wfcel.CELWorkflow,
		wfcel.CELNodes,
		wfcel.CELIter,
		wfcel.CELOutput,
	}

	return newValidationCELEnv(namespaces, nodeTypeCtx)
}

// =============================================================================
// NAMESPACE DECLARATIONS
// =============================================================================

func getNamespaceDecl(ns wfcel.CELNamespace, typeCtx *WorkflowTypeContext) cel.EnvOption {
	switch ns {
	case wfcel.CELWorkflow:
		return cel.Variable(string(ns), cel.ObjectType("model.WorkflowContext"))
	case wfcel.CELIter:
		// When we have inferred type info for iter.item (from loop items expression),
		// use ObjectType so the custom type provider can validate field access.
		// Otherwise fall back to DynType for backward compatibility.
		if typeCtx != nil && typeCtx.IterItemFields != nil {
			return cel.Variable(string(ns), cel.ObjectType("iter"))
		}
		return cel.Variable(string(ns), cel.DynType)
	case wfcel.CELNodes:
		if typeCtx != nil {
			return cel.Variable(string(ns), cel.ObjectType("nodes"))
		}
		return cel.Variable(string(ns), cel.DynType)
	case wfcel.CELInputs:
		if typeCtx != nil {
			return cel.Variable(string(ns), cel.ObjectType("inputs"))
		}
		return cel.Variable(string(ns), cel.DynType)
	case wfcel.CELOutputs:
		if typeCtx != nil {
			return cel.Variable(string(ns), cel.ObjectType("outputs"))
		}
		return cel.Variable(string(ns), cel.DynType)
	case wfcel.CELOutput:
		if typeCtx != nil && typeCtx.CurrentNodeOutputType != nil {
			return cel.Variable(string(ns), typeCtx.CurrentNodeOutputType)
		}
		return cel.Variable(string(ns), cel.DynType)
	default:
		return cel.Variable(string(ns), cel.DynType)
	}
}

// hasDynamicOutputs checks if a node has dynamic outputs (e.g., inline outputs from loop/workflow nodes).
func hasDynamicOutputs(wtc *WorkflowTypeContext, nodeID string) bool {
	if wtc == nil {
		return false
	}
	nodeType, ok := wtc.NodeTypes[nodeID]
	if !ok {
		return false
	}
	// Workflow and loop nodes flatten child workflow outputs to the top level.
	// The proto output type doesn't describe the full runtime shape — child outputs are
	// added dynamically. When we have resolved candidate outputs (inline or via loader),
	// we use typed ObjectType so the provider can validate field name access. When we
	// can't resolve outputs (e.g., unresolved refs, no loader), we fall back to dyn.
	//
	// Router has fixed top-level fields (5 proto fields); child outputs are nested
	// under the `outputs` sub-field, so the proto type exactly describes the top-level shape.
	if nodeType == model.NodeTypeWorkflow || nodeType == model.NodeTypeLoop {
		outputs, hasOutputs := wtc.NodeOutputs[nodeID]
		if hasOutputs && len(outputs) > 0 {
			return false
		}
		// No output info → ref-based node without resolved outputs. Must be dyn.
		if !hasOutputs {
			return true
		}
	}
	return false
}

// =============================================================================
// OUTPUT TYPE INFERENCE
// =============================================================================

// inferOutputTypes compiles each output CEL expression and infers its result type.
func inferOutputTypes(outputs map[string]string, env *cel.Env) (map[string]*FieldInfo, map[string]error) {
	if len(outputs) == 0 || env == nil {
		return nil, nil
	}

	result := make(map[string]*FieldInfo, len(outputs))
	errors := make(map[string]error)

	for name, expr := range outputs {
		compiled, issues := env.Compile(expr)
		if issues.Err() != nil {
			errors[name] = issues.Err()
			continue
		}

		celType := compiled.OutputType()
		result[name] = celTypeToFieldInfo(name, celType)
	}

	if len(errors) == 0 {
		errors = nil
	}
	return result, errors
}
