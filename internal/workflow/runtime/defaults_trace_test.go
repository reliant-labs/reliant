// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// TestDefaultsTraceE2E traces the full flow from parsing to buildWorkflowContext
// to verify defaults are preserved through the entire path
func TestDefaultsTraceE2E(t *testing.T) {
	yamlData := []byte(`
name: test-loop-workflow
apiVersion: "0.0.6"
inputs:
  message:
    type: string
    # no default = required
  temperature:
    type: number
    default: 1.0
  model:
    type: string
    default: ""
  tools:
    type: array
    default: []
entry: [step1]
nodes:
  - id: step1
    type: loop
    while: iter.iteration >= 3
    ref: builtin://agent
    args:
      temp: "{{workflow.inputs.temperature}}"
`)

	// Step 1: Parse workflow to proto
	wf, err := wfyaml.ParseWorkflow(yamlData)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	t.Logf("Parsed workflow: %s", wf.GetName())
	t.Logf("Input schemas: %d", len(wf.GetInputs()))
	for name, input := range wf.GetInputs() {
		isRequired := model.IsInputRequired(input)
		def := model.GetInputDefault(input)
		t.Logf("  %s: required=%v, default=%v (type %T)", name, isRequired, def, def)
	}

	// Step 2: Simulate incoming inputs (like what the user passes)
	userInputs := map[string]interface{}{
		"message": "Hello world",
		"thread":  "0",
	}
	t.Logf("User inputs: %v", userInputs)

	// Step 3: Apply runtime defaults after required inputs are validated
	inputsWithDefaults := ApplyDefaultsForRuntime(userInputs, wf.GetInputs())
	t.Logf("After ApplyDefaults: %v", inputsWithDefaults)

	// Step 4: Verify temperature is in the result
	if _, ok := inputsWithDefaults["temperature"]; !ok {
		t.Errorf("temperature should be in inputsWithDefaults after ApplyDefaults")
	}
	if _, ok := inputsWithDefaults["model"]; !ok {
		t.Errorf("model should be in inputsWithDefaults after ApplyDefaults")
	}
	if _, ok := inputsWithDefaults["tools"]; !ok {
		t.Errorf("tools should be in inputsWithDefaults after ApplyDefaults")
	}

	// Step 5: Build workflow context (like loop_executor.go)
	workflowContext := buildWorkflowContext(
		"test-wf-id",
		"test-loop-workflow",
		"test-chat-id",
		inputsWithDefaults,
	)
	t.Logf("workflowContext keys: ")
	for k := range workflowContext {
		t.Logf("  %s", k)
	}

	// Step 6: Verify inputs in workflowContext
	inputs, ok := workflowContext["inputs"].(map[string]interface{})
	if !ok {
		t.Fatalf("workflowContext['inputs'] is not a map")
	}
	t.Logf("workflowContext['inputs']: %v", inputs)

	if _, ok := inputs["temperature"]; !ok {
		t.Errorf("temperature should be in workflowContext['inputs']")
	}
	if _, ok := inputs["model"]; !ok {
		t.Errorf("model should be in workflowContext['inputs']")
	}
	if _, ok := inputs["tools"]; !ok {
		t.Errorf("tools should be in workflowContext['inputs']")
	}
}

// TestDefaultsJSONRoundTrip tests that defaults survive the YAML->proto round trip
func TestDefaultsJSONRoundTrip(t *testing.T) {
	yamlData := []byte(`
name: test-roundtrip-workflow
apiVersion: "0.0.6"
inputs:
  message:
    type: string
    # no default = required
  temperature:
    type: number
    default: 1.0
  model:
    type: string
    default: ""
  tools:
    type: array
    default: []
entry: [step1]
nodes:
  - id: step1
    type: call_llm
`)

	// Step 1: Parse workflow to proto
	wf, err := wfyaml.ParseWorkflow(yamlData)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	t.Logf("After ParseWorkflow:")
	for name, input := range wf.GetInputs() {
		def := model.GetInputDefault(input)
		t.Logf("  %s: default=%v (type %T)", name, def, def)
	}

	// Step 2: Check temperature default
	tempInput, ok := wf.GetInputs()["temperature"]
	if !ok {
		t.Fatalf("temperature input not found")
	}
	if model.GetInputDefault(tempInput) == nil {
		t.Errorf("temperature default should not be nil")
	}
	t.Logf("temperature default: %v (type %T)", model.GetInputDefault(tempInput), model.GetInputDefault(tempInput))

	// Step 3: Apply runtime defaults after required inputs are validated
	userInputs := map[string]interface{}{
		"message": "Hello world",
	}
	result := ApplyDefaultsForRuntime(userInputs, wf.GetInputs())

	t.Logf("After ApplyDefaults:")
	for k, v := range result {
		t.Logf("  %s: %v (type %T)", k, v, v)
	}

	// Temperature should be 1.0
	if temp, ok := result["temperature"]; !ok {
		t.Errorf("temperature should be in result")
	} else {
		t.Logf("temperature = %v (type %T)", temp, temp)
	}
}
