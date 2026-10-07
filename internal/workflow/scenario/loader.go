// Copyright (c) 2025 Reliant Labs
package scenario

import (
	"bytes"
	"fmt"
	"io"
	"os"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
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

// ParseScenarioFile parses a PROJECT scenario file: one scenario per file, as
// .reliant/workflows/<slug>/scenarios/<name>.yaml holds them
// (workflowref.ScenarioPath). A file that declares no name: takes
// defaultName, its file stem.
//
// The CLI and the app both read project scenarios with this. The app
// identifies a project scenario by its file, so a file holding several would
// silently lose all but the first there; it is an error here instead, on
// both surfaces. (ParseScenarioYAML, which accepts many, is for the builtin
// corpus under internal/workflow/builtin/testdata.)
func ParseScenarioFile(data []byte, defaultName string) (*Scenario, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var scenarios []*Scenario
	for {
		var doc yaml.Node
		err := decoder.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to parse scenario YAML: %w", err)
		}
		if len(doc.Content) == 0 || doc.Content[0].Kind == 0 {
			continue // an empty document between separators
		}
		var wrapper ScenarioFile
		if err := doc.Decode(&wrapper); err == nil && len(wrapper.Scenarios) > 0 {
			return nil, fmt.Errorf("holds a list of %d scenarios; a project scenario file holds one — split it into one file per scenario", len(wrapper.Scenarios))
		}
		var sc Scenario
		if err := doc.Decode(&sc); err != nil {
			return nil, fmt.Errorf("failed to parse scenario YAML: %w", err)
		}
		scenarios = append(scenarios, &sc)
	}
	switch len(scenarios) {
	case 0:
		return nil, fmt.Errorf("holds no scenario")
	case 1:
	default:
		return nil, fmt.Errorf("holds %d scenarios; a project scenario file holds one — split it into one file per scenario", len(scenarios))
	}
	sc := scenarios[0]
	if sc.Name == "" {
		sc.Name = defaultName
	}
	return sc, nil
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
