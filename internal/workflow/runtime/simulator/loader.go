// Copyright (c) 2025 Reliant Labs
package simulator

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"gopkg.in/yaml.v3"
)

// ScenarioFile is a wrapper format for scenario files with an array of scenarios
type ScenarioFile struct {
	APIVersion string      `yaml:"apiVersion"`
	Scenarios  []*Scenario `yaml:"scenarios"`
}

// LoadScenariosFromFile loads scenarios from a YAML file on disk.
// Supports two formats:
// 1. Multi-document YAML (separated by ---)
// 2. Wrapper format: {apiVersion: "1.0", scenarios: [...]}
func LoadScenariosFromFile(path string) ([]*Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read scenario file %s: %w", path, err)
	}
	return ParseScenarioYAML(data)
}

// LoadScenariosFromDir loads all scenario files from a directory.
// Files must have .yaml or .yml extension.
func LoadScenariosFromDir(dir string) ([]*Scenario, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read scenario directory %s: %w", dir, err)
	}

	var allScenarios []*Scenario
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}

		scenarios, err := LoadScenariosFromFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("failed to load scenarios from %s: %w", name, err)
		}
		allScenarios = append(allScenarios, scenarios...)
	}

	return allScenarios, nil
}

// ParseScenarioYAML parses scenario YAML bytes into Scenario structs.
// Supports multi-document YAML or wrapper format.
func ParseScenarioYAML(data []byte) ([]*Scenario, error) {
	var scenarios []*Scenario

	// First try to parse as wrapper format
	var wrapper ScenarioFile
	if err := yaml.Unmarshal(data, &wrapper); err == nil && len(wrapper.Scenarios) > 0 {
		return wrapper.Scenarios, nil
	}

	// Fall back to multi-document format
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var scenario Scenario
		err := decoder.Decode(&scenario)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to parse scenario YAML: %w", err)
		}
		// Skip empty documents
		if scenario.Name == "" {
			continue
		}
		scenarios = append(scenarios, &scenario)
	}

	return scenarios, nil
}

// LoadWorkflowFromFile loads a workflow from a YAML file on disk.
func LoadWorkflowFromFile(path string) (*reliantv1.Workflow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read workflow file %s: %w", path, err)
	}

	wf, err := v2.ParseWorkflowProtoBytes(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse workflow %s: %w", path, err)
	}

	return wf, nil
}

// TestWorkflowScenarios is a convenience function that loads a workflow and its scenarios,
// runs all scenarios, and returns the results.
func TestWorkflowScenarios(workflowPath, scenarioPath string) ([]*ScenarioResult, error) {
	wf, err := LoadWorkflowFromFile(workflowPath)
	if err != nil {
		return nil, err
	}

	var scenarios []*Scenario

	// Check if scenarioPath is a directory or file
	info, err := os.Stat(scenarioPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat scenario path %s: %w", scenarioPath, err)
	}

	if info.IsDir() {
		scenarios, err = LoadScenariosFromDir(scenarioPath)
	} else {
		scenarios, err = LoadScenariosFromFile(scenarioPath)
	}
	if err != nil {
		return nil, err
	}

	if len(scenarios) == 0 {
		return nil, fmt.Errorf("no scenarios found at %s", scenarioPath)
	}

	engine := NewEngine(wf)
	return engine.RunScenarios(scenarios), nil
}
