// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/toolbindings"
	"github.com/reliant-labs/reliant/internal/workflow/runtime"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// A preset is an untyped bag of values; a workflow reaches into it by declaring
// the path with CEL. These tests run that whole path for tool parameters:
//
//	preset YAML → preset.ApplyToInputs → workflow inputs
//	  → `tools: "{{inputs.tool_params}}"` in the workflow
//	  → CEL resolution → per-tool bindings → the real tool's schema
//
// The preset is parsed with the real preset parser and applied with the real
// ApplyToInputs rather than hand-built, because the claim under test is that a
// preset AUTHOR can do this — not that the runtime can be handed a map.
func presetInputs(t *testing.T, presetYAML string) map[string]interface{} {
	t.Helper()
	p, err := preset.ParsePreset([]byte(presetYAML), "test-preset")
	if err != nil {
		t.Fatalf("parse preset: %v", err)
	}
	return preset.ApplyToInputs(p, map[string]interface{}{}, "")
}

func resolveWithInputs(t *testing.T, source []byte, inputs map[string]interface{}) toolbindings.Scopes {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow(source)
	if err != nil {
		t.Fatalf("parse workflow: %v", err)
	}
	resolved, err := runtime.EvaluateNodeConfig(wf.GetNodes()[0], nil, "wf-1", wf.GetName(), inputs, nil, nil, nil)
	if err != nil {
		t.Fatalf("EvaluateNodeConfig: %v", err)
	}
	activityUnderTest := &CallLLMActivity{}
	return activityUnderTest.resolveToolBindingScopes(
		context.Background(), "", resolved.GetCallLlm().GetToolsConfig())
}

// The whole-map form: the workflow declares the path once and the preset
// decides which tools it configures. This is what makes a preset able to bind a
// parameter the workflow author never named.
const wholeMapWorkflow = `
name: image-workflow
entry: [generate]
inputs:
  tool_params:
    type: object
    default: {}
nodes:
  - id: generate
    type: call_llm
    args:
      model: {tags: [flagship]}
      tools_config:
        filter: [tag:coding:default, generate_image]
        tools: "{{inputs.tool_params}}"
`

func TestPresetSuppliesToolParamsThroughWholeMap(t *testing.T) {
	inputs := presetInputs(t, `
name: landscape-images
tag: agent
params:
  tool_params:
    generate_image:
      size: "1536x1024"
      quality: high
`)

	bindings := resolveWithInputs(t, []byte(wholeMapWorkflow), inputs).For("generate_image")
	if len(bindings) == 0 {
		t.Fatal("preset supplied tool_params but no bindings resolved")
	}
	if got := bindings["size"].Literal; got != "1536x1024" {
		t.Errorf("size: got %v, want %q", got, "1536x1024")
	}
	if got := bindings["quality"].Literal; got != "high" {
		t.Errorf("quality: got %v, want %q", got, "high")
	}
}

// The payoff: a parameter a preset fixed is not offered to the model.
func TestPresetBoundParameterIsHiddenFromTheModel(t *testing.T) {
	inputs := presetInputs(t, `
name: landscape-images
tag: agent
params:
  tool_params:
    generate_image:
      size: "1536x1024"
`)

	scopes := resolveWithInputs(t, []byte(wholeMapWorkflow), inputs)

	tool := tools.NewGenerateImageTool(nil, nil)
	if _, present := tool.ParamSchema().Properties.Get("size"); !present {
		t.Fatal("precondition: generate_image should offer size when unbound")
	}

	bound, problems := toolbindings.Apply([]tools.Tool{tool}, scopes)
	if len(problems) != 0 {
		t.Fatalf("unexpected binding problems: %v", problems)
	}
	if _, present := bound[0].ParamSchema().Properties.Get("size"); present {
		t.Error("the preset fixed size, so it must not be offered to the model")
	}
	if _, present := bound[0].ParamSchema().Properties.Get("prompt"); !present {
		t.Error("prompt is unbound and must still be offered")
	}
}

// A workflow with the path declared but no preset filling it must behave
// exactly like a workflow with no tools block. This is the zero-config
// invariant, and it is the case that would break every existing agent if the
// sentinel leaked through unresolved.
func TestUnfilledToolParamsBindNothing(t *testing.T) {
	scopes := resolveWithInputs(t, []byte(wholeMapWorkflow), map[string]interface{}{
		"tool_params": map[string]interface{}{},
	})
	if names := scopes.ToolNames(); len(names) != 0 {
		t.Errorf("no preset filled tool_params, so nothing should bind; got %v", names)
	}

	// And the sentinel key must never reach a tool name.
	if _, leaked := scopes.Workflow[wfyaml.CELExprSentinelKey]; leaked {
		t.Error("the CEL sentinel leaked through as a tool name")
	}
}

// The per-tool form: the workflow names the tool but lets the preset supply
// that tool's whole parameter object.
func TestPresetSuppliesOneToolsParams(t *testing.T) {
	source := []byte(`
name: image-workflow
entry: [generate]
inputs:
  image_params:
    type: object
    default: {}
nodes:
  - id: generate
    type: call_llm
    args:
      model: {tags: [flagship]}
      tools_config:
        filter: [tag:coding:default, generate_image]
        tools:
          generate_image: "{{inputs.image_params}}"
`)

	inputs := presetInputs(t, `
name: portrait-images
tag: agent
params:
  image_params:
    size: "1024x1536"
`)

	if got := resolveWithInputs(t, source, inputs).For("generate_image")["size"].Literal; got != "1024x1536" {
		t.Errorf("size: got %v, want %q", got, "1024x1536")
	}
}

// Two presets, same workflow, different bindings — the point of presets.
func TestDifferentPresetsProduceDifferentBindings(t *testing.T) {
	for _, tc := range []struct {
		name       string
		presetYAML string
		wantSize   string
	}{
		{"landscape", "name: l\ntag: agent\nparams:\n  tool_params:\n    generate_image:\n      size: \"1536x1024\"\n", "1536x1024"},
		{"portrait", "name: p\ntag: agent\nparams:\n  tool_params:\n    generate_image:\n      size: \"1024x1536\"\n", "1024x1536"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputs := presetInputs(t, tc.presetYAML)
			got := resolveWithInputs(t, []byte(wholeMapWorkflow), inputs).For("generate_image")["size"].Literal
			if got != tc.wantSize {
				t.Errorf("size: got %v, want %q", got, tc.wantSize)
			}
		})
	}
}
