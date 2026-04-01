// Copyright (c) 2025 Reliant Labs
package services

import (
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// parseWorkflowYAML parses YAML bytes into a V2Workflow proto.
func parseWorkflowYAML(data []byte) (*reliantv1.Workflow, error) {
	return wfyaml.ParseWorkflow(data)
}

// rpcWorkflowToYAML converts a V2Workflow proto to YAML bytes.
func rpcWorkflowToYAML(protoWf *reliantv1.Workflow) ([]byte, error) {
	if protoWf == nil {
		return nil, nil
	}
	return wfyaml.MarshalWorkflow(protoWf)
}

// rpcWorkflowHasPresetGroups checks preset groups on a V2Workflow.
func rpcWorkflowHasPresetGroups(wf *reliantv1.Workflow) bool {
	if wf == nil {
		return false
	}
	if wf.Presets != nil && wf.Presets.Tag != "" {
		return true
	}
	for _, input := range wf.Inputs {
		if v2InputHasPresetGroups(input) {
			return true
		}
	}
	return false
}

func v2InputHasPresetGroups(input *reliantv1.Input) bool {
	if input == nil {
		return false
	}
	groupInput, ok := input.Config.(*reliantv1.Input_GroupInput)
	if !ok || groupInput.GroupInput == nil {
		return false
	}
	if groupInput.GroupInput.Presets != nil && groupInput.GroupInput.Presets.Tag != "" {
		return true
	}
	for _, nestedInput := range groupInput.GroupInput.Inputs {
		if v2InputHasPresetGroups(nestedInput) {
			return true
		}
	}
	return false
}

// parseDraftDefinitionV2 parses draft YAML into a V2Workflow proto.
func parseDraftDefinitionV2(yamlData []byte) (*reliantv1.Workflow, error) {
	return wfyaml.ParseDraftWorkflow(yamlData)
}
