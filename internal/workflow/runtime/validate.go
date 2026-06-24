// Copyright (c) 2025 Reliant Labs
// Package runtime provides workflow runtime types and execution utilities.
package runtime

import (
	"fmt"
	"os"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// WorkflowLoader loads a workflow by reference for cross-workflow validation.
// Returns nil, nil if the workflow is not found (allows validation to continue).
type WorkflowLoader func(ref string) (*reliantv1.Workflow, error)

// ValidateYAML validates workflow YAML bytes directly.
func ValidateYAML(yamlData []byte, loader WorkflowLoader) error {
	result, err := ValidateYAMLResult(yamlData, loader)
	if err != nil {
		return err
	}
	return result.AsError()
}

// ValidateYAMLResult validates workflow YAML and returns the full result with warnings.
func ValidateYAMLResult(yamlData []byte, loader WorkflowLoader) (*validation.Result, error) {
	protoWorkflow, err := wfyaml.ParseWorkflow(yamlData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse workflow: %w", err)
	}

	var workflowLoader validation.WorkflowLoader
	if loader != nil {
		workflowLoader = func(ref string) (*reliantv1.Workflow, error) {
			return loader(ref)
		}
	}

	return validation.StaticAnalysisWithOptions(protoWorkflow, &validation.ValidationOptions{
		WorkflowLoader: workflowLoader,
	}), nil
}

// ParseWorkflowProto parses a workflow YAML file into a proto message.
func ParseWorkflowProto(yamlPath string) (*reliantv1.Workflow, error) {
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read workflow file: %w", err)
	}
	return ParseWorkflowProtoBytes(data)
}

// ParseWorkflowProtoBytes parses workflow YAML bytes into a proto message and validates.
func ParseWorkflowProtoBytes(data []byte) (*reliantv1.Workflow, error) {
	if err := ValidateYAML(data, nil); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}
	return wfyaml.ParseWorkflow(data)
}

// ParseWorkflowProtoBytesWithLoader parses workflow YAML with cross-workflow validation.
func ParseWorkflowProtoBytesWithLoader(data []byte, loader WorkflowLoader) (*reliantv1.Workflow, error) {
	if err := ValidateYAML(data, loader); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}
	return wfyaml.ParseWorkflow(data)
}

// ParseWorkflowProtoBytesNoValidation parses workflow YAML without validation.
func ParseWorkflowProtoBytesNoValidation(data []byte) (*reliantv1.Workflow, error) {
	return wfyaml.ParseWorkflow(data)
}
