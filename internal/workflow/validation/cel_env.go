// Copyright (c) 2025 Reliant Labs
package validation

import (
	"reflect"

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
	opts = append(opts, ext.NativeTypes(
		ext.ParseStructTag("json"),
		reflect.TypeOf(&model.WorkflowContext{}),
		reflect.TypeOf(&model.IterContext{}),
	))

	// Add namespace variable declarations
	for _, ns := range namespaces {
		opts = append(opts, getNamespaceDecl(ns, typeCtx))
	}

	// Add typed node variables when we have workflow context.
	if typeCtx != nil {
		for nodeID, nodeType := range typeCtx.NodeTypes {
			if hasDynamicOutputs(typeCtx, nodeID) {
				opts = append(opts, cel.Variable("nodes_"+nodeID, cel.DynType))
				continue
			}
			celType := getNodeOutputCELType(typeCtx.Registry, nodeType, nodeID)
			if celType != nil {
				opts = append(opts, cel.Variable("nodes_"+nodeID, celType))
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
		return cel.Variable(string(ns), cel.ObjectType("model.IterContext"))
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
	if nodeType == model.NodeTypeWorkflow || nodeType == model.NodeTypeLoop {
		// Workflow/loop nodes with inline outputs have known fields.
		// Ref-based workflow/loop nodes have NO output info and should be treated as dyn.
		outputs, hasOutputs := wtc.NodeOutputs[nodeID]
		if hasOutputs && len(outputs) > 0 {
			return true
		}
		// No output info → ref-based node with dynamic outputs.
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
