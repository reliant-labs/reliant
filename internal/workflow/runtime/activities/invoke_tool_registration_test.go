// Copyright (c) 2025 Reliant Labs
package activities

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// TestInvokeToolIsARegisteredNodeType proves the node type reached the runtime
// registry — descriptor-driven, from the NodeMeta annotation, with no
// hardcoded switch anywhere. Without this the proto message exists but nothing
// in the graph can reach it.
func TestInvokeToolIsARegisteredNodeType(t *testing.T) {
	if !model.IsKnownNodeType(model.NodeTypeInvokeTool) {
		t.Fatalf("%q is not a known node type; its NodeMeta annotation was not discovered", model.NodeTypeInvokeTool)
	}
	if !model.IsActivityNode(model.NodeTypeInvokeTool) {
		t.Errorf("%q should be an activity node, not a structural one", model.NodeTypeInvokeTool)
	}

	meta, ok := schema.GetActivityMetadata("InvokeTool")
	if !ok {
		t.Fatal("InvokeTool activity metadata is not registered")
	}
	if meta.DisplayName == "" {
		t.Error("InvokeTool has no display name, so it cannot appear in the node palette")
	}
}

// TestInvokeToolNodeInputAndOutputFields verifies both descriptors reached the
// registry. The input fields are what a builder renders; the output fields are
// what a downstream CEL reference resolves against.
func TestInvokeToolNodeInputAndOutputFields(t *testing.T) {
	meta, ok := schema.GetActivityMetadata("InvokeTool")
	if !ok {
		t.Fatal("InvokeTool activity metadata is not registered")
	}

	inputNames := fieldNameSet(meta.InputFields)
	for _, want := range []string{"tool", "params"} {
		if !inputNames[want] {
			t.Errorf("InvokeTool input fields missing %q; have %v", want, keysOf(inputNames))
		}
	}

	// The builder-UI extractor deliberately drops message-typed fields (they
	// need custom renderers), so `data` is absent HERE and present in the CEL
	// registry below. That split is intentional and worth pinning: a palette
	// that tried to render a Struct would render nothing useful, while a CEL
	// reference to it resolves fine.
	outputNames := fieldNameSet(meta.OutputFields)
	for _, want := range []string{"content", "is_error", "attachment_ids", "tool"} {
		if !outputNames[want] {
			t.Errorf("InvokeTool output fields missing %q; have %v", want, keysOf(outputNames))
		}
	}
}

// TestInvokeToolDataIsCELReachable is the assertion that matters for graph
// edges: `data` must be visible to the CEL type registry, because that is what
// nodes.<id>.data.<field> resolves against.
func TestInvokeToolDataIsCELReachable(t *testing.T) {
	fields := wfcel.NewTypeRegistry().OutputFieldsForNodeType(model.NodeTypeInvokeTool)
	if len(fields) == 0 {
		t.Fatalf("no CEL output fields for %q", model.NodeTypeInvokeTool)
	}

	names := make(map[string]bool, len(fields))
	for _, field := range fields {
		names[field.Name] = true
	}
	for _, want := range []string{"content", "is_error", "attachment_ids", "data", "tool"} {
		if !names[want] {
			t.Errorf("CEL output fields missing %q; have %v", want, keysOf(names))
		}
	}
}

// TestInvokeToolOutputDescriptorResolvesByConvention pins the naming
// convention this depends on. wfcel.TypeRegistry matches InvokeToolOutput to
// the "invoke_tool" node type by PascalCase→snake_case; renaming either half
// silently un-types every downstream reference, so it is worth a test that
// says so out loud.
func TestInvokeToolOutputDescriptorResolvesByConvention(t *testing.T) {
	registry := wfcel.NewTypeRegistry()

	argsDesc, ok := registry.ArgsForNodeType(model.NodeTypeInvokeTool)
	if !ok {
		t.Fatalf("no args descriptor for %q; the Node.args oneof arm is missing", model.NodeTypeInvokeTool)
	}
	if got := string(argsDesc.Name()); got != "InvokeToolArgs" {
		t.Errorf("args descriptor = %q, want InvokeToolArgs", got)
	}

	outputDesc, ok := registry.OutputForNodeType(model.NodeTypeInvokeTool)
	if !ok {
		t.Fatalf("no output descriptor for %q; InvokeToolOutput did not match by naming convention", model.NodeTypeInvokeTool)
	}
	if got := string(outputDesc.Name()); got != "InvokeToolOutput" {
		t.Errorf("output descriptor = %q, want InvokeToolOutput", got)
	}

	// The `data` field must be a Struct: that is what makes it walkable as
	// nodes.<id>.data.<field> for ANY opted-in tool, rather than needing one
	// proto message per tool.
	dataField := outputDesc.Fields().ByName("data")
	if dataField == nil {
		t.Fatal("InvokeToolOutput has no `data` field")
	}
	if got := string(dataField.Message().FullName()); got != "google.protobuf.Struct" {
		t.Errorf("data field type = %q, want google.protobuf.Struct", got)
	}
}

// TestInvokeToolActivityIsRegistered verifies the Temporal activity name lines
// up with what the step executor derives from the node type. nodeTypeToActivityName
// turns "invoke_tool" into "InvokeTool"; if the activity registered under a
// different name, every invoke_tool node would fail at dispatch.
func TestInvokeToolActivityIsRegistered(t *testing.T) {
	def, ok := nodeTypeActivities[model.NodeTypeInvokeTool]
	if !ok {
		t.Fatalf("no activity definition for node type %q", model.NodeTypeInvokeTool)
	}
	if def.activityName != "InvokeTool" {
		t.Errorf("activity name = %q, want InvokeTool (must match snakeToPascal(%q))",
			def.activityName, model.NodeTypeInvokeTool)
	}
}

// TestNodePaletteStaysCurated is the guard on the opt-in promise. The point of
// tool-as-node is a palette a human can read; if exposure ever became
// automatic, this catches it at the layer that builds the palette.
func TestNodePaletteStaysCurated(t *testing.T) {
	exposed := tools.NodeExposedTools()
	if len(exposed) == 0 {
		t.Fatal("expected at least one node-exposed tool")
	}
	// One generic node type serves every opted-in tool. That is the whole
	// reason this does not cost a proto message and a permanent oneof arm per
	// tool, so it is worth asserting rather than assuming.
	if len(schema.ListVisibleActivities()) > 32 {
		t.Errorf("the activity palette has grown past a readable size (%d); "+
			"did tool exposure become per-tool node types?", len(schema.ListVisibleActivities()))
	}
}

func fieldNameSet(fields []schema.InputFieldInfo) map[string]bool {
	names := make(map[string]bool, len(fields))
	for _, field := range fields {
		names[field.Name] = true
	}
	return names
}

func keysOf(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	return keys
}
