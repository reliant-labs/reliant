package config

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// StoredWorkflow represents a project workflow stored in the DB via daemon config sync.
type StoredWorkflow struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	YAMLContent string `json:"yaml_content"`
	ContentHash string `json:"content_hash"`
}

// StoredPreset represents a project preset stored in the DB via daemon config sync.
type StoredPreset struct {
	Name        string `json:"name"`
	YAMLContent string `json:"yaml_content"`
	ContentHash string `json:"content_hash"`
}

// StoredScenario represents a workflow scenario stored in the DB via daemon config sync.
type StoredScenario struct {
	WorkflowSlug string `json:"workflow_slug"`
	Name         string `json:"name"`
	YAMLContent  string `json:"yaml_content"`
	ContentHash  string `json:"content_hash"`
}

// ParseStoredWorkflows deserializes the project_workflows_json column.
func ParseStoredWorkflows(jsonStr *string) ([]StoredWorkflow, error) {
	if jsonStr == nil || *jsonStr == "" {
		return nil, nil
	}
	var items []StoredWorkflow
	if err := json.Unmarshal([]byte(*jsonStr), &items); err != nil {
		return nil, fmt.Errorf("failed to parse stored workflows JSON: %w", err)
	}
	return items, nil
}

// ParseStoredPresets deserializes the project_presets_json column.
func ParseStoredPresets(jsonStr *string) ([]StoredPreset, error) {
	if jsonStr == nil || *jsonStr == "" {
		return nil, nil
	}
	var items []StoredPreset
	if err := json.Unmarshal([]byte(*jsonStr), &items); err != nil {
		return nil, fmt.Errorf("failed to parse stored presets JSON: %w", err)
	}
	return items, nil
}

// ParseStoredScenarios deserializes the project_scenarios_json column.
func ParseStoredScenarios(jsonStr *string) ([]StoredScenario, error) {
	if jsonStr == nil || *jsonStr == "" {
		return nil, nil
	}
	var items []StoredScenario
	if err := json.Unmarshal([]byte(*jsonStr), &items); err != nil {
		return nil, fmt.Errorf("failed to parse stored scenarios JSON: %w", err)
	}
	return items, nil
}

// FindStoredWorkflowBySlug finds a workflow by slug in the stored list.
func FindStoredWorkflowBySlug(workflows []StoredWorkflow, slug string) *StoredWorkflow {
	for i := range workflows {
		if workflows[i].Slug == slug {
			return &workflows[i]
		}
	}
	return nil
}

// NormalizeSlug converts a name to a URL-safe slug.
func NormalizeSlug(name string) string {
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "_", "-")
	re := regexp.MustCompile(`[^a-z0-9-]`)
	s = re.ReplaceAllString(s, "")
	re = regexp.MustCompile(`-+`)
	s = re.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// FindStoredPresetByName finds a preset by name in the stored list.
func FindStoredPresetByName(presets []StoredPreset, name string) *StoredPreset {
	for i := range presets {
		if presets[i].Name == name {
			return &presets[i]
		}
	}
	return nil
}

// FindStoredScenariosByWorkflow returns all scenarios for a given workflow slug.
func FindStoredScenariosByWorkflow(scenarios []StoredScenario, workflowSlug string) []StoredScenario {
	var result []StoredScenario
	for _, s := range scenarios {
		if s.WorkflowSlug == workflowSlug {
			result = append(result, s)
		}
	}
	return result
}
