package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
)

// StoredWorkflow represents a project workflow stored in the DB via daemon config sync.
type StoredWorkflow struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	// RelativePath is the file it was synced from, relative to the project
	// root (".reliant/workflows/blog.yaml"). It is never an address; it is
	// how a ref that misses can name the file the author probably meant.
	RelativePath string `json:"relative_path,omitempty"`
	YAMLContent  string `json:"yaml_content"`
	ContentHash  string `json:"content_hash"`
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

// StoredSkill represents a skill (SKILL.md) synced from the daemon via project config.
// It carries everything the server-side skill tool needs so no filesystem access
// is required to list/load/search skills on the server.
type StoredSkill struct {
	SkillPath              string            `json:"skill_path"`
	Name                   string            `json:"name"`
	Description            string            `json:"description"`
	Scope                  string            `json:"scope"`
	Body                   string            `json:"body"`
	AllowedTools           []string          `json:"allowed_tools,omitempty"`
	Metadata               map[string]string `json:"metadata,omitempty"`
	HasChildren            bool              `json:"has_children,omitempty"`
	DisableModelInvocation bool              `json:"disable_model_invocation,omitempty"`
	UserInvocable          *bool             `json:"user_invocable,omitempty"`
	ArgumentHint           string            `json:"argument_hint,omitempty"`
	Paths                  string            `json:"paths,omitempty"`
	ContentHash            string            `json:"content_hash,omitempty"`
	// Source is the repo relative path the skill was discovered in. "" means
	// project-root scope; non-empty means a nested repo (e.g. "api"). LLMs
	// disambiguate same-named skills across repos by source.
	Source string `json:"source,omitempty"`
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

// ParseStoredSkills deserializes the project_skills_json column.
func ParseStoredSkills(jsonStr *string) ([]StoredSkill, error) {
	if jsonStr == nil || *jsonStr == "" {
		return nil, nil
	}
	var items []StoredSkill
	if err := json.Unmarshal([]byte(*jsonStr), &items); err != nil {
		return nil, fmt.Errorf("failed to parse stored skills JSON: %w", err)
	}
	return NormalizeStoredSkills(items), nil
}

// ParseRepoMemories deserializes the repo_memories_json column into a
// map[repoRelPath]->content.
func ParseRepoMemories(jsonStr *string) (map[string]string, error) {
	if jsonStr == nil || *jsonStr == "" {
		return nil, nil
	}
	var result map[string]string
	if err := json.Unmarshal([]byte(*jsonStr), &result); err != nil {
		return nil, fmt.Errorf("failed to parse repo memories JSON: %w", err)
	}
	return result, nil
}

// NormalizeStoredSkills ensures stored skill records always carry the canonical
// path used by skill list/load/search, including builtin records from producers
// that only populate the normalized skill name.
func NormalizeStoredSkills(skills []StoredSkill) []StoredSkill {
	if len(skills) == 0 {
		return skills
	}

	normalized := make([]StoredSkill, len(skills))
	copy(normalized, skills)
	for i := range normalized {
		normalized[i].SkillPath = normalizeStoredSkillPath(normalized[i])
	}
	return normalized
}

func normalizeStoredSkillPath(skill StoredSkill) string {
	if path := canonicalStoredSkillPath(skill.SkillPath); path != "" {
		return path
	}
	return canonicalStoredSkillPath(NormalizeSlug(skill.Name))
}

func canonicalStoredSkillPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = strings.ReplaceAll(path, "\\", "/")
	return strings.Trim(path, "/")
}

// FindStoredSkillByPath looks up a skill by its hierarchical path.
func FindStoredSkillByPath(skills []StoredSkill, path string) *StoredSkill {
	for i := range skills {
		if skills[i].SkillPath == path {
			return &skills[i]
		}
	}
	return nil
}

// ProjectWorkflowIndex indexes a project's synced workflows exactly as the
// CLI indexes .reliant/workflows on disk (workflowref.NewIndex over each
// file's path and content), so a ref resolves the same way in both. The slug
// the daemon stored is not consulted: the index derives it from the content.
func ProjectWorkflowIndex(workflows []StoredWorkflow) *workflowref.Index {
	files := make([]workflowref.File, 0, len(workflows))
	for _, w := range workflows {
		files = append(files, workflowref.File{
			Path:    strings.TrimPrefix(w.RelativePath, workflowref.Dir+"/"),
			Content: []byte(w.YAMLContent),
		})
	}
	return workflowref.NewIndex(files)
}

// NormalizeSlug converts a name to a URL-safe slug: workflowref.Slug, the
// one slug workflow names and refs are compared under.
func NormalizeSlug(name string) string {
	return workflowref.Slug(name)
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
