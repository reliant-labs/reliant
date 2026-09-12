// Copyright (c) 2025 Reliant Labs
package wfyaml

import "testing"

// The proto field is map<string, google.protobuf.Struct>, which the generic
// YAML unmarshaler reaches through setMapField. These tests pin that a
// tools_config.tools block written the way a human would write it actually
// lands in the proto — the validation layer is worthless if the values never
// arrive.
func TestParseToolsConfigToolBindings(t *testing.T) {
	source := []byte(`
name: image-workflow
entry: [generate]
nodes:
  - id: generate
    type: call_llm
    args:
      model: {tags: [flagship]}
      tools_config:
        filter: [tag:default, generate_image]
        tools:
          generate_image:
            model: {tags: [image-gen], providers: [codex]}
            size: "1536x1024"
`)

	wf, err := ParseWorkflow(source)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	toolsConfig := wf.GetNodes()[0].GetCallLlm().GetToolsConfig()
	bindings := toolsConfig.GetTools()
	if len(bindings) != 1 {
		t.Fatalf("expected 1 tool binding block, got %d", len(bindings))
	}

	imageBindings := bindings["generate_image"]
	if imageBindings == nil {
		t.Fatal("generate_image bindings missing")
	}

	if got := imageBindings.GetFields()["size"].GetStringValue(); got != "1536x1024" {
		t.Errorf("size: got %q want %q", got, "1536x1024")
	}

	// The nested object matters: a model selector is a structured value, not
	// a scalar, and it is the shape this whole feature was designed around.
	model := imageBindings.GetFields()["model"].GetStructValue()
	if model == nil {
		t.Fatal("model binding should be a nested object")
	}
	providers := model.GetFields()["providers"].GetListValue()
	if providers == nil || len(providers.GetValues()) != 1 ||
		providers.GetValues()[0].GetStringValue() != "codex" {
		t.Errorf("model.providers: got %v want [codex]", providers)
	}

	// The availability fields must still work — this is an addition, not a
	// replacement.
	if filter := toolsConfig.GetFilter().GetLiteral().GetValues(); len(filter) != 2 {
		t.Errorf("filter should still parse, got %v", filter)
	}
}

// Zero configuration stays valid: the invariant is that a tool is fully
// functional with no bindings at all.
func TestParseToolsConfigWithoutToolsBlock(t *testing.T) {
	source := []byte(`
name: plain-workflow
entry: [generate]
nodes:
  - id: generate
    type: call_llm
    args:
      model: {tags: [flagship]}
      tools_config:
        filter: [tag:default]
`)

	wf, err := ParseWorkflow(source)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tools := wf.GetNodes()[0].GetCallLlm().GetToolsConfig().GetTools(); len(tools) != 0 {
		t.Errorf("expected no tool bindings, got %v", tools)
	}
}
