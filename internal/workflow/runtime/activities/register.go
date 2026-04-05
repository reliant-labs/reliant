// Copyright (c) 2025 Reliant Labs
package activities

import (
	"reflect"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// nodeTypeActivityDef maps a node type to its Temporal activity name and Go types.
// Only non-structural node types with corresponding Temporal activities are listed.
type nodeTypeActivityDef struct {
	activityName string
	inputType    reflect.Type
	outputType   reflect.Type
}

var nodeTypeActivities = map[string]nodeTypeActivityDef{
	model.NodeTypeCallLLM:        {"CallLLM", reflect.TypeOf(reliantv1.CallLLMArgs{}), reflect.TypeOf((*reliantv1.CallLLMOutput)(nil))},
	model.NodeTypeExecuteTools:   {"ExecuteTools", reflect.TypeOf(reliantv1.ExecuteToolsArgs{}), reflect.TypeOf((*reliantv1.ExecuteToolsOutput)(nil))},
	model.NodeTypeCompact:        {"Compact", reflect.TypeOf(reliantv1.CompactArgs{}), reflect.TypeOf(handlers.CompactOutput{})},
	model.NodeTypeCreateWorktree: {"CreateWorktree", reflect.TypeOf(reliantv1.CreateWorktreeArgs{}), reflect.TypeOf(handlers.CreateWorktreeOutput{})},
	model.NodeTypeApproval:       {"Approval", reflect.TypeOf(reliantv1.ApprovalArgs{}), reflect.TypeOf(handlers.ApprovalOutput{})},
	model.NodeTypeSaveMessage:    {"SaveMessage", reflect.TypeOf(reliantv1.SaveMessageNodeArgs{}), reflect.TypeOf(reliantv1.SaveMessageOutput{})},
}

func init() {
	// Auto-register node-type activities from NodeMeta proto annotations.
	// Proto descriptors and metadata (display_name, description, category) are
	// discovered from annotations, replacing per-activity boilerplate.
	registerNodeTypeActivities()

	// ExecuteRunStep (not visible in builder, no metadata registration)
	schema.RegisterActivityType("ExecuteRunStep",
		reflect.TypeOf(handlers.ExecuteRunStepInput{}),
		reflect.TypeOf(handlers.ExecuteRunStepOutput{}))

	// DeleteWorktree
	schema.RegisterActivityType("DeleteWorktree",
		reflect.TypeOf(handlers.DeleteWorktreeInput{}),
		reflect.TypeOf(handlers.DeleteWorktreeOutput{}))
	schema.RegisterActivityMetadata((*handlers.DeleteWorktreeActivity)(nil))

	// Approval and SaveMessage are registered via nodeTypeActivities map above.

	// CreateWorkflowWithThread (not visible in builder, no metadata registration)
	schema.RegisterActivityType("CreateWorkflowWithThread",
		reflect.TypeOf(handlers.CreateWorkflowWithThreadInput{}),
		reflect.TypeOf(handlers.CreateWorkflowWithThreadOutput{}))

	// YieldCreate (not visible in builder, no metadata registration)
	schema.RegisterActivityType("YieldCreate",
		reflect.TypeOf(handlers.YieldCreateInput{}),
		reflect.TypeOf(handlers.YieldCreateOutput{}))

	// YieldResolve (not visible in builder, no metadata registration)
	schema.RegisterActivityType("YieldResolve",
		reflect.TypeOf(handlers.YieldResolveInput{}),
		reflect.TypeOf(handlers.YieldResolveOutput{}))
}

// registerNodeTypeActivities uses DiscoverNodeMetas to auto-register activity types,
// proto descriptors, and metadata for non-structural node types.
func registerNodeTypeActivities() {
	metas := model.DiscoverNodeMetas()

	// Build a map of node_type -> args message descriptor from the V2Node.args oneof.
	argsDescs := make(map[string]protoreflect.MessageDescriptor)
	nodeDesc := (&reliantv1.Node{}).ProtoReflect().Descriptor()
	argsOneof := nodeDesc.Oneofs().ByName("args")
	if argsOneof != nil {
		fields := argsOneof.Fields()
		for i := 0; i < fields.Len(); i++ {
			fd := fields.Get(i)
			msgDesc := fd.Message()
			if msgDesc == nil {
				continue
			}
			opts := msgDesc.Options()
			if opts == nil {
				continue
			}
			ext := proto.GetExtension(opts, reliantv1.E_NodeMeta)
			if ext == nil {
				continue
			}
			meta, ok := ext.(*reliantv1.NodeMeta)
			if !ok || meta == nil || meta.NodeType == "" {
				continue
			}
			argsDescs[meta.NodeType] = msgDesc
		}
	}

	// Use TypeRegistry to look up output descriptors for each node type.
	typeRegistry := wfcel.NewTypeRegistry()

	// Structural node types that should appear in the workflow builder sidebar.
	// These don't have separate Temporal activities but need catalog metadata.
	visibleStructuralNodes := map[string]bool{
		model.NodeTypeRun:      true,
		model.NodeTypeWorkflow: true,
	}

	for nodeType, meta := range metas {
		def, ok := nodeTypeActivities[nodeType]
		if ok {
			// Non-structural activity node: register types and metadata.
			schema.RegisterActivityType(def.activityName, def.inputType, def.outputType)
			if msgDesc, ok := argsDescs[nodeType]; ok {
				outputDesc, _ := typeRegistry.OutputForNodeType(nodeType)
				schema.RegisterNodeTypeActivity(def.activityName, meta, msgDesc, outputDesc)
			}
		} else if visibleStructuralNodes[nodeType] {
			// Structural node visible in builder: register metadata only (no Temporal activity).
			if msgDesc, ok := argsDescs[nodeType]; ok {
				outputDesc, _ := typeRegistry.OutputForNodeType(nodeType)
				schema.RegisterNodeTypeActivity(meta.NodeType, meta, msgDesc, outputDesc)
			}
		}
	}
}

// RegisterAll registers all runtime workflow activities with the Temporal worker registry.
// Activity types and metadata are registered in init() for static analysis.
// This function registers activity instances with their runtime dependencies.
func RegisterAll(registry *v2.ActivityRegistry, deps *Activities) {
	// ========================================================================
	// MESSAGE PROCESSING ACTIVITIES
	// ========================================================================

	v2.RegisterActivity(registry, handlers.NewSaveMessageActivity(deps.Repo))
	v2.RegisterActivity(registry, handlers.NewCallLLMActivity(deps.Repo, deps.StreamingHub, deps.ToolsFactory, deps.ConfigProvider, deps.DriverResolver, deps.MCPBinder))

	// ========================================================================
	// TOOL EXECUTION ACTIVITIES
	// ========================================================================

	v2.RegisterActivity(registry, handlers.NewExecuteToolsActivity(deps.Repo, deps.ToolExecutor))

	// ========================================================================
	// CONTEXT MANAGEMENT ACTIVITIES
	// ========================================================================

	v2.RegisterActivity(registry, handlers.NewCompactActivity(deps.Repo, deps.DriverResolver))
	v2.RegisterActivity(registry, handlers.NewGenerateTitleActivity(deps.Repo, deps.DriverResolver))

	// ========================================================================
	// APPROVAL ACTIVITIES
	// ========================================================================

	v2.RegisterActivity(registry, handlers.NewApprovalActivity(deps.Repo))

	// ========================================================================
	// RUN STEP ACTIVITIES
	// ========================================================================

	v2.RegisterActivity(registry, handlers.NewExecuteRunStepActivity(deps.Repo, deps.ToolExecutor, deps.RunExecutor))

	// ========================================================================
	// WORKTREE ACTIVITIES
	// ========================================================================

	v2.RegisterActivity(registry, handlers.NewCreateWorktreeActivity(deps.Repo, deps.DaemonRouter))
	v2.RegisterActivity(registry, handlers.NewDeleteWorktreeActivity(deps.Repo, deps.DaemonRouter))

	// ========================================================================
	// WORKFLOW MANAGEMENT ACTIVITIES
	// ========================================================================

	v2.RegisterActivity(registry, handlers.NewLoadWorkflowActivity(deps.Repo))
	v2.RegisterLifecycleActivity(registry, handlers.NewWorkflowStatusActivity(deps.Repo))
	v2.RegisterLifecycleActivity(registry, handlers.NewWorkflowErrorActivity(deps.Repo))
	v2.RegisterLifecycleActivity(registry, handlers.NewCleanupActivity(deps.Repo))
	v2.RegisterActivity(registry, handlers.NewCreateWorkflowWithThreadActivity(deps.Threads))

	// ========================================================================
	// UTILITY ACTIVITIES
	// ========================================================================

	v2.RegisterActivity(registry, handlers.NewUnknownStepTypeActivity())
	v2.RegisterActivity(registry, handlers.NewFailStepActivity())
	v2.RegisterActivity(registry, handlers.NewSkippedStepActivity())
	v2.RegisterActivity(registry, handlers.NewFetchThreadResultActivity(deps.Repo, deps.Threads))
	v2.RegisterActivity(registry, handlers.NewValidateThreadOwnershipActivity(deps.Repo))
	v2.RegisterActivity(registry, handlers.NewLoadPresetParamsActivity(deps.Repo))

	// ========================================================================
	// DEBUG ACTIVITIES (for testing/debugging workflow execution)
	// ========================================================================

	v2.RegisterActivity(registry, handlers.NewDebugTraceActivity(deps.Repo))

	// ========================================================================
	// YIELD ACTIVITIES
	// ========================================================================

	v2.RegisterActivity(registry, handlers.NewYieldCreateActivity(deps.Repo))
	v2.RegisterActivity(registry, handlers.NewYieldResolveActivity(deps.Repo))
}
