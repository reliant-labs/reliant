// Copyright (c) 2025 Reliant Labs
//
// Workflow builder preset assembler - generates workflow_builder.yaml.
//
// Produces a thin preset that tells the agent to load the workflow-builder
// skill instead of embedding all reference docs in the system prompt.
//
// GENERATED FILE:
//   - internal/workflow/builtin/presets/workflow_builder.yaml
//
// Usage: go run ./tools/docgen/assembler <output_file>

package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Preset represents the YAML structure
type Preset struct {
	Name        string                 `yaml:"name"`
	Description string                 `yaml:"description"`
	Tag         string                 `yaml:"tag"`
	Params      map[string]interface{} `yaml:"params"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <output_file>\n", os.Args[0])
		os.Exit(1)
	}

	outputFile := os.Args[1]

	prompt := `You build Reliant workflows. Your goal: create working workflows that solve the user's problem.

IMPORTANT: Start by loading the workflow-builder skill for comprehensive reference:
  skill(action="load", path="workflow-builder")

You are given a workflow draft ID in the system message. Use this ID for all workflow operations.

## Quick Start
1. Load the workflow-builder skill (above)
2. Call get_workflow(id="<draft_id>") to see current content
3. Ask clarifying questions about the user's goal
4. Use list_workflows to see examples and patterns
5. Build using edit_workflow (small changes) or write_workflow (rewrites)
6. Test with scenarios (aim for 3+ covering positive, negative, edge cases)`

	preset := Preset{
		Name:        "workflow_builder",
		Description: "Specialized assistant for building and modifying Reliant workflows",
		Tag:         "agent",
		Params: map[string]interface{}{
			"model": map[string]string{
				"id": "claude-4.6-opus",
			},
			"temperature":    1.0,
			"thinking_level": "high",
			"system_prompt":  prompt,
			"tools": []string{
				"tag:workflow",
				"tag:search",
				"tag:web",
				"skill",
				"view",
			},
			"spawn_presets": []string{},
		},
	}

	data, err := yaml.Marshal(&preset)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling YAML: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(outputFile, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing output: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Generated workflow_builder.yaml: %s\n", outputFile)
}
