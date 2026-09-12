// Copyright (c) 2025 Reliant Labs
package tools

import (
	"testing"
)

// TestNodeExposureIsOptIn pins the property the whole mechanism exists for:
// exposure is DECLARED, not derived from the tool registry. If someone ever
// "helpfully" exposes every registered tool, this fails.
func TestNodeExposureIsOptIn(t *testing.T) {
	if !IsNodeExposedTool(ToolGenerateImage) {
		t.Fatalf("generate_image should be node-exposed")
	}

	// Conversational affordances. Each answers a question an agent asks
	// itself mid-turn; none is a step a human would draw on a graph.
	conversationalOnly := []string{ToolShellOutput, ToolSpawnStatus, ToolLoadTool}
	for _, name := range conversationalOnly {
		if IsNodeExposedTool(name) {
			t.Errorf("%s must NOT be node-exposed: it is a conversational affordance, and a node palette full of them is worse than none", name)
		}
	}

	// The opt-in set must stay far smaller than the registry. A palette that
	// approaches the registry's size has stopped being curated.
	registrySize := len(GetToolRegistry())
	exposedSize := len(NodeExposedTools())
	if exposedSize >= registrySize {
		t.Fatalf("node-exposed tools (%d) should be a small subset of the registry (%d)", exposedSize, registrySize)
	}
}

// TestNodeOutputSchemaReflectsToolOutput verifies the typing substrate: a
// node-exposed tool's structured output fields are recoverable as a JSON
// schema, which is what makes nodes.<id>.data.<field> checkable without
// declaring a proto message per tool.
func TestNodeOutputSchemaReflectsToolOutput(t *testing.T) {
	schema := NodeOutputSchema(ToolGenerateImage)
	if schema == nil {
		t.Fatal("expected an output schema for generate_image")
	}
	if schema.Properties == nil {
		t.Fatal("expected reflected properties on generate_image's output schema")
	}

	// These are GenerateImageOutput's json tags. They are what a downstream
	// node references.
	for _, field := range []string{"attachment_id", "filename", "mime_type", "size", "model", "saved_to", "revised_prompt"} {
		if _, ok := schema.Properties.Get(field); !ok {
			t.Errorf("output schema missing field %q", field)
		}
	}

	// A field the tool does not produce must NOT be present — a schema that
	// admitted anything would type-check typos as valid.
	if _, ok := schema.Properties.Get("attachment_ids"); ok {
		t.Error("output schema should not contain 'attachment_ids'; the tool returns a single attachment_id")
	}
}

// TestNodeOutputSchemaAbsentForUnexposedTool ensures the schema lookup follows
// the opt-in rather than the registry.
func TestNodeOutputSchemaAbsentForUnexposedTool(t *testing.T) {
	if schema := NodeOutputSchema(ToolShellOutput); schema != nil {
		t.Fatalf("expected no output schema for the unexposed tool shell_output, got %+v", schema)
	}
	if schema := NodeOutputSchema("no_such_tool"); schema != nil {
		t.Fatalf("expected no output schema for an unknown tool, got %+v", schema)
	}
}

// TestNodeExposedToolsAreRegisteredTools guards against an entry that names a
// tool nobody can run — an opt-in for a tool that does not exist would produce
// a palette entry that always fails at execution.
func TestNodeExposedToolsAreRegisteredTools(t *testing.T) {
	registered := make(map[string]bool)
	for _, def := range GetToolRegistry() {
		registered[def.Name] = true
	}
	for _, exposure := range NodeExposedTools() {
		if !registered[exposure.Tool] {
			t.Errorf("node-exposed tool %q is not in the tool registry", exposure.Tool)
		}
		if exposure.OutputType == nil {
			t.Errorf("node-exposed tool %q declares no output type, so its fields cannot be type-checked", exposure.Tool)
		}
	}
}
