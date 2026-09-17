// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

func staticAnalysisOf(t *testing.T, source string) *Result {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(source))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return StaticAnalysis(wf, nil)
}

// A tools block supplied by expression is the path a preset's tool parameters
// travel. The CEL sentinel that carries it is not a tool name, and rejecting it
// would make every workflow that offers the preset path fail to load.
func TestSentinelToolsBlockPassesValidation(t *testing.T) {
	result := staticAnalysisOf(t, `
name: image-workflow
entry: [generate]
inputs:
  tool_params: {type: object, default: {}}
nodes:
  - id: generate
    type: call_llm
    args:
      model: {tags: [flagship]}
      tools_config:
        filter: [tag:coding:default]
        tools: "{{inputs.tool_params}}"
`)
	for _, e := range result.Errors() {
		t.Logf("unexpected error: %v %v", e.Path, e.Message)
	}
	if len(result.Errors()) != 0 {
		t.Fatalf("expression-supplied tools block should validate, got %d errors", len(result.Errors()))
	}
}

// The exemption is scoped to the sentinel entry. A literal tool name beside it
// is still checked in full, so opting into the preset path does not switch off
// validation for everything else in the block.
func TestLiteralEntriesStillValidatedBesideSentinel(t *testing.T) {
	result := staticAnalysisOf(t, `
name: image-workflow
entry: [generate]
inputs:
  img: {type: object, default: {}}
nodes:
  - id: generate
    type: call_llm
    args:
      model: {tags: [flagship]}
      tools_config:
        filter: [tag:coding:default]
        tools:
          generate_image: "{{inputs.img}}"
          no_such_tool:
            whatever: 1
`)
	errs := result.Errors()
	if len(errs) == 0 {
		t.Fatal("a bogus tool name beside a sentinel entry must still be rejected")
	}
	var found bool
	for _, e := range errs {
		if strings.Contains(e.Message, "no_such_tool") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an error naming no_such_tool, got %v", errs)
	}
}

// An unbindable parameter is still rejected when written literally.
func TestUnbindableParamStillRejected(t *testing.T) {
	result := staticAnalysisOf(t, `
name: image-workflow
entry: [generate]
nodes:
  - id: generate
    type: call_llm
    args:
      model: {tags: [flagship]}
      tools_config:
        filter: [tag:coding:default]
        tools:
          generate_image:
            not_a_real_param: 1
`)
	if len(result.Errors()) == 0 {
		t.Fatal("an unknown parameter should still be rejected at load time")
	}
}
