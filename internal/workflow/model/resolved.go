package model

import (
	"encoding/json"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"google.golang.org/protobuf/encoding/protojson"
	structpb "google.golang.org/protobuf/types/known/structpb"
)

// NodeThreadConfig returns the thread configuration for a node.
// Workflow and loop nodes can both carry thread config. Returns nil for all other node types.
func NodeThreadConfig(node *reliantv1.Node) *reliantv1.ThreadConfig {
	if node == nil {
		return nil
	}
	if wf := node.GetWorkflow(); wf != nil {
		return wf.GetThread()
	}
	if loop := node.GetLoop(); loop != nil {
		return loop.GetThread()
	}
	if rt := node.GetRouter(); rt != nil {
		return rt.GetThread()
	}
	return nil
}

// NodeThreadMode returns the resolved thread mode string.
// Returns "inherit" if no thread config is set.
func NodeThreadMode(node *reliantv1.Node) string {
	threadConfig := NodeThreadConfig(node)
	if threadConfig == nil || threadConfig.GetMode() == "" {
		return ThreadModeInherit
	}
	return threadConfig.GetMode()
}

// NodeInjectConfig returns the resolved inject configuration, or nil if not set.
func NodeInjectConfig(node *reliantv1.Node) *reliantv1.InjectConfig {
	threadConfig := NodeThreadConfig(node)
	if threadConfig == nil {
		return nil
	}
	return threadConfig.GetInject()
}

// NodeThreadMemo returns the resolved thread memo value.
// Returns false if not set.
func NodeThreadMemo(node *reliantv1.Node) bool {
	threadConfig := NodeThreadConfig(node)
	if threadConfig == nil {
		return false
	}
	return CelBoolValue(threadConfig.GetMemo())
}

// NodeCommand returns the resolved command string for run nodes.
// Returns "" if not a run node or command is not set.
func NodeCommand(node *reliantv1.Node) string {
	if node == nil {
		return ""
	}
	runArgs := node.GetRun()
	if runArgs == nil {
		return ""
	}
	return CelStringValue(runArgs.GetCommand())
}

// NodeRef returns the resolved workflow reference string.
// Works for both workflow and loop nodes.
func NodeRef(node *reliantv1.Node) string {
	if node == nil {
		return ""
	}
	if workflowArgs := node.GetWorkflow(); workflowArgs != nil {
		return CelStringValue(workflowArgs.GetRef())
	}
	if loopArgs := node.GetLoop(); loopArgs != nil {
		return CelStringValue(loopArgs.GetRef())
	}
	return ""
}

// NodePresets returns the presets map for sub-workflow or loop nodes.
func NodePresets(node *reliantv1.Node) map[string]string {
	if node == nil {
		return nil
	}
	if workflowArgs := node.GetWorkflow(); workflowArgs != nil {
		return workflowArgs.GetPresets()
	}
	if loopArgs := node.GetLoop(); loopArgs != nil {
		return loopArgs.GetPresets()
	}
	return nil
}

// NodeProjectPath returns the resolved project path for sub-workflow or loop nodes.
// Returns "" if not set (inherit from parent).
func NodeProjectPath(node *reliantv1.Node) string {
	if node == nil {
		return ""
	}
	if workflowArgs := node.GetWorkflow(); workflowArgs != nil {
		if projectConfig := workflowArgs.GetProject(); projectConfig != nil {
			return CelStringValue(projectConfig.GetPath())
		}
	}
	if loopArgs := node.GetLoop(); loopArgs != nil {
		if projectConfig := loopArgs.GetProject(); projectConfig != nil {
			return CelStringValue(projectConfig.GetPath())
		}
	}
	if routerArgs := node.GetRouter(); routerArgs != nil {
		if projectConfig := routerArgs.GetProject(); projectConfig != nil {
			return CelStringValue(projectConfig.GetPath())
		}
	}
	return ""
}

// NodeInlineWorkflow returns the inline workflow for sub-workflow or loop nodes, or nil.
func NodeInlineWorkflow(node *reliantv1.Node) *reliantv1.Workflow {
	if node == nil {
		return nil
	}
	if workflowArgs := node.GetWorkflow(); workflowArgs != nil {
		return workflowArgs.GetInline()
	}
	if loopArgs := node.GetLoop(); loopArgs != nil {
		return loopArgs.GetInline()
	}
	return nil
}

// NodeWhileExpr returns the loop while expression for loop nodes.
func NodeWhileExpr(node *reliantv1.Node) string {
	if node == nil {
		return ""
	}
	if loopArgs := node.GetLoop(); loopArgs != nil {
		return DirectCelExpr(loopArgs.GetWhile())
	}
	return ""
}

// NodeYieldExpr returns the loop yield expression for loop nodes.
func NodeYieldExpr(node *reliantv1.Node) string {
	if node == nil {
		return ""
	}
	if loopArgs := node.GetLoop(); loopArgs != nil {
		return loopArgs.GetYield()
	}
	return ""
}

// NodeArgsAsMap returns the resolved node args as map[string]interface{} for Temporal serialization.
// Uses proto reflection to find the populated oneof field, then protojson to serialize.
//
// CelX wrapper types (CelString, CelBool, etc.) are unwrapped to their literal values.
// Without this, protojson serializes them as {"literal": "value"} which breaks downstream
// protojson deserialization when the receiver expects flat values (e.g., repeated ToolCallMsg
// expects a JSON array, not {"literal": "[...]"}.
func NodeArgsAsMap(node *reliantv1.Node) (map[string]interface{}, error) {
	if node == nil {
		return nil, nil
	}

	nodeMessage := node.ProtoReflect()
	oneofDescriptor := nodeMessage.Descriptor().Oneofs().ByName("args")
	if oneofDescriptor == nil {
		return nil, nil
	}

	fieldDescriptor := nodeMessage.WhichOneof(oneofDescriptor)
	if fieldDescriptor == nil {
		return nil, nil // no args set
	}

	argsMessage := nodeMessage.Get(fieldDescriptor).Message().Interface()

	marshaler := protojson.MarshalOptions{
		EmitUnpopulated: false,
		UseProtoNames:   true,
	}

	jsonBytes, err := marshaler.Marshal(argsMessage)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &result); err != nil {
		return nil, err
	}

	// Unwrap CelX wrapper types: protojson serializes CelString{literal: "val"} as
	// {"literal": "val"} but downstream code expects the flat resolved value.
	unwrapCelWrappers(result)

	return result, nil
}

// celWrapperKeys are the possible keys inside CelX wrapper messages.
// CelString/CelBool/CelDouble/CelInt: oneof { T literal = 1; string expr = 2 }
// CelStringList: oneof { StringList literal = 1; string expr = 2 }
// CelModelSelector: oneof { ModelSelector literal = 1; string expr = 2 }
// DirectCelBool: string expr = 1
var celWrapperKeys = map[string]bool{
	"literal": true,
	"expr":    true,
}

// unwrapCelWrappers post-processes a protojson-produced map to collapse CelX wrapper
// structures to their resolved values. A wrapper is identified as a map containing
// only "literal" and/or "expr" keys (matching the CelX oneof pattern).
//
// Only scalar literals (string, number, bool) and string-list literals ([]interface{}
// of strings) are unwrapped. Complex object literals (e.g., CelModelSelector whose
// literal is a ModelSelector map with {id, tags, providers}) are left wrapped so that
// downstream protojson.Unmarshal can re-hydrate the CelX oneof correctly.
func unwrapCelWrappers(m map[string]interface{}) {
	for key, val := range m {
		wrapper, ok := val.(map[string]interface{})
		if !ok {
			continue
		}

		if isCelWrapper(wrapper) {
			// Extract the value: prefer "literal" (resolved), fall back to "expr"
			if literal, hasLiteral := wrapper["literal"]; hasLiteral {
				// Don't unwrap complex object literals — they need the
				// wrapper to round-trip through protojson correctly.
				if _, isMap := literal.(map[string]interface{}); isMap {
					continue
				}
				m[key] = literal
			} else if expr, hasExpr := wrapper["expr"]; hasExpr {
				m[key] = expr
			}
		} else {
			// Recurse into nested maps (e.g., thread config, response_tool config)
			unwrapCelWrappers(wrapper)
		}
	}
}

// isCelWrapper returns true if the map looks like a CelX wrapper message,
// i.e., it contains only keys from the CelX oneof pattern ("literal" and/or "expr").
func isCelWrapper(m map[string]interface{}) bool {
	if len(m) == 0 || len(m) > 2 {
		return false
	}
	for key := range m {
		if !celWrapperKeys[key] {
			return false
		}
	}
	return true
}

// NodeMergedSubWorkflowInputs returns the args map for child workflow invocation.
// Converts the proto structpb.Value args to a Go map.
func NodeMergedSubWorkflowInputs(node *reliantv1.Node) map[string]interface{} {
	if node == nil {
		return nil
	}

	var protoArgs map[string]*structpb.Value
	if workflowArgs := node.GetWorkflow(); workflowArgs != nil {
		protoArgs = workflowArgs.GetArgs()
	} else if loopArgs := node.GetLoop(); loopArgs != nil {
		protoArgs = loopArgs.GetArgs()
	}

	if len(protoArgs) == 0 {
		return nil
	}

	result := make(map[string]interface{}, len(protoArgs))
	for key, value := range protoArgs {
		if value != nil {
			result[key] = value.AsInterface()
		}
	}
	return result
}
