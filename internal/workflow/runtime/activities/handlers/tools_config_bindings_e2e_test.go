// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/toolbindings"
	"github.com/reliant-labs/reliant/internal/workflow/runtime"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// These tests run the WHOLE path a human's configuration actually travels:
// real workflow YAML → the real parser → the real CEL resolution step the
// runtime performs before any activity sees a node → the real binding
// conversion → the real tool.
//
// They are deliberately not built from a hand-constructed ToolsConfig. The
// gap these cover shipped with green unit tests on both ends: the parser
// tests proved the YAML reached the proto, the toolbindings tests proved
// Scopes resolved correctly, and the function joining them returned nil. Only
// a test spanning the join could have caught that, so that is what these are.
//
// resolveNodeToolBindings runs the real pipeline: parse, the CEL resolution
// the runtime performs before handing a node to an activity, then the
// activity's OWN scope resolver — not a reimplementation of it. A global
// binding read is not exercised here (nil repo, empty user), which leaves the
// workflow scope as the thing under test.
func resolveNodeToolBindings(t *testing.T, source []byte, inputs map[string]interface{}) toolbindings.Scopes {
	t.Helper()

	wf, err := wfyaml.ParseWorkflow(source)
	if err != nil {
		t.Fatalf("parse workflow: %v", err)
	}

	// The step that makes expression bindings unnecessary: CEL resolves
	// templates inside tools_config.tools before the activity runs.
	resolved, err := runtime.EvaluateNodeConfig(wf.GetNodes()[0], nil, "wf-1", wf.GetName(), inputs, nil, nil, nil)
	if err != nil {
		t.Fatalf("EvaluateNodeConfig: %v", err)
	}

	activityUnderTest := &CallLLMActivity{}
	return activityUnderTest.resolveToolBindingScopes(
		context.Background(), "", resolved.GetCallLlm().GetToolsConfig())
}

// The core regression. Before the fix this resolved to zero bindings: the
// YAML parsed, validation accepted it, and the resolver dropped it silently.
func TestWorkflowToolBindingsReachTheTool(t *testing.T) {
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
            size: "1536x1024"
`)

	scopes := resolveNodeToolBindings(t, source, nil)
	bindings := scopes.For("generate_image")
	if len(bindings) == 0 {
		t.Fatal("workflow bound generate_image.size but it resolved to no bindings; the human's configuration was dropped")
	}
	if got := bindings["size"].Literal; got != "1536x1024" {
		t.Errorf("size: got %v, want %q", got, "1536x1024")
	}
}

// Binding a parameter must REMOVE it from what the model is offered. That is
// the whole access-control claim the binding system makes, so it is asserted
// against the real tool's real schema rather than a stub's.
func TestBoundParameterIsHiddenFromTheModel(t *testing.T) {
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
            size: "1536x1024"
`)

	scopes := resolveNodeToolBindings(t, source, nil)

	tool := tools.NewGenerateImageTool(nil, nil)
	if _, present := tool.ParamSchema().Properties.Get("size"); !present {
		t.Fatal("precondition: generate_image should offer size when unbound")
	}

	bound, problems := toolbindings.Apply([]tools.Tool{tool}, scopes)
	if len(problems) != 0 {
		t.Fatalf("unexpected binding problems: %v", problems)
	}

	if _, present := bound[0].ParamSchema().Properties.Get("size"); present {
		t.Error("size is bound by the workflow but still offered to the model")
	}
	// Unbound parameters must survive: binding is refinement, not a filter.
	if _, present := bound[0].ParamSchema().Properties.Get("prompt"); !present {
		t.Error("prompt is unbound and must still be offered to the model")
	}
}

// A preset's contribution arrives through inputs, which the workflow spends
// via a template. This pins that presetScopeBindings returning nil is correct
// rather than a second dropped channel: the preset's value still lands on the
// tool, through CEL and the workflow scope.
func TestPresetValueReachesToolThroughInputs(t *testing.T) {
	source := []byte(`
name: image-workflow
entry: [generate]
inputs:
  image_size: {type: string, default: "1024x1024"}
nodes:
  - id: generate
    type: call_llm
    args:
      model: {tags: [flagship]}
      tools_config:
        filter: [tag:default, generate_image]
        tools:
          generate_image:
            size: "{{inputs.image_size}}"
`)

	// What preset.ApplyToInputs produces for a preset carrying image_size.
	scopes := resolveNodeToolBindings(t, source, map[string]interface{}{
		"image_size": "1024x1536",
	})

	if got := scopes.For("generate_image")["size"].Literal; got != "1024x1536" {
		t.Errorf("preset value should reach the tool through inputs; got %v, want %q", got, "1024x1536")
	}
}

// A structured value — the shape this feature was designed around — must
// survive as a nested object, not be flattened or stringified.
func TestStructuredBindingSurvives(t *testing.T) {
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
`)

	scopes := resolveNodeToolBindings(t, source, nil)

	literal, ok := scopes.For("generate_image")["model"].Literal.(map[string]interface{})
	if !ok {
		t.Fatalf("model binding should be a nested object, got %T", scopes.For("generate_image")["model"].Literal)
	}
	providers, ok := literal["providers"].([]interface{})
	if !ok || len(providers) != 1 || providers[0] != "codex" {
		t.Errorf("model.providers: got %v, want [codex]", literal["providers"])
	}
}

// Zero configuration is the invariant the whole binding design rests on: a
// workflow with no tools block must leave every tool on its own defaults.
func TestNoToolsBlockLeavesToolsUnbound(t *testing.T) {
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

	scopes := resolveNodeToolBindings(t, source, nil)
	if names := scopes.ToolNames(); len(names) != 0 {
		t.Errorf("no tools block should bind nothing, got %v", names)
	}

	// generate_image's own default binding must survive untouched.
	tool := tools.NewGenerateImageTool(nil, nil)
	bound, problems := toolbindings.Apply([]tools.Tool{tool}, scopes)
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if _, present := bound[0].ParamSchema().Properties.Get("model"); present {
		t.Error("generate_image binds model by default, so it must stay hidden from the model")
	}
}
