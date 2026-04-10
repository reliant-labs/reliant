package daemonruntime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/config"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

// filesystemConfigStore implements config.StoredConfigStore by reading config
// files directly from disk. The "projectID" parameter is treated as a
// filesystem path to the project root — the daemon has no DB-backed project
// identity.
type filesystemConfigStore struct{}

// storedWorkflow / storedPreset / storedScenario mirror the JSON shapes
// produced by the server's flattenIndexed* helpers so that StoredConfigProvider
// can deserialize them identically.
type storedWorkflow struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	YAMLContent string `json:"yaml_content"`
	ContentHash string `json:"content_hash"`
}

type storedPreset struct {
	Name        string `json:"name"`
	YAMLContent string `json:"yaml_content"`
	ContentHash string `json:"content_hash"`
}

type storedScenario struct {
	WorkflowSlug string `json:"workflow_slug"`
	Name         string `json:"name"`
	YAMLContent  string `json:"yaml_content"`
	ContentHash  string `json:"content_hash"`
}

func (f *filesystemConfigStore) GetProjectConfigRecord(_ context.Context, projectPath string) (*config.StoredProjectConfigRecord, error) {
	projectPath = strings.TrimSpace(projectPath)
	if projectPath != "" {
		if abs, err := filepath.Abs(projectPath); err == nil {
			projectPath = filepath.Clean(abs)
		}
	}
	if projectPath == "" {
		return nil, nil
	}

	userConfigDir := config.GetUserConfigDir()

	userConfigYAML, _ := readOptionalFile(filepath.Join(userConfigDir, "config.yaml"))
	projectConfigYAML, _ := readOptionalFile(filepath.Join(projectPath, ".reliant", "config.yaml"))
	localConfigYAML, _ := readOptionalFile(filepath.Join(projectPath, ".reliant.local", "config.yaml"))
	globalMemory, _ := readOptionalFile(filepath.Join(userConfigDir, "reliant.md"))
	projectMemory, _ := readOptionalFile(filepath.Join(projectPath, "reliant.md"))

	userMCP, _ := readOptionalFile(filepath.Join(userConfigDir, "mcp.json"))
	projectMCP, _ := readOptionalFile(filepath.Join(projectPath, ".reliant", "mcp.json"))
	localMCP, _ := readOptionalFile(filepath.Join(projectPath, ".reliant.local", "mcp.json"))

	mcpConfigs := flattenMCPConfigBytes(userMCP, projectMCP, localMCP)

	workflows, _ := indexWorkflows(projectPath)
	presets, _ := indexPresets(projectPath)
	scenarios, _ := indexScenarios(projectPath)
	skills, _ := indexSkills(projectPath)

	workflowsJSON := flattenWorkflows(workflows)
	presetsJSON := flattenPresets(presets)
	scenariosJSON := flattenScenarios(scenarios)
	skillsJSON := flattenSkills(skills)

	return &config.StoredProjectConfigRecord{
		ProjectID:            projectPath,
		UserConfigYAML:       bytesToStringPtr(userConfigYAML),
		ProjectConfigYAML:    bytesToStringPtr(projectConfigYAML),
		LocalConfigYAML:      bytesToStringPtr(localConfigYAML),
		GlobalMemoryMD:       bytesToStringPtr(globalMemory),
		ProjectMemoryMD:      bytesToStringPtr(projectMemory),
		MCPConfigs:           mcpConfigs,
		ProjectWorkflowsJSON: workflowsJSON,
		ProjectPresetsJSON:   presetsJSON,
		ProjectScenariosJSON: scenariosJSON,
		ProjectSkillsJSON:    skillsJSON,
	}, nil
}

// bytesToStringPtr returns a *string for non-nil, non-empty byte slices.
func bytesToStringPtr(b []byte) *string {
	if len(b) == 0 {
		return nil
	}
	s := string(b)
	return &s
}

// flattenMCPConfigBytes produces the JSON object {"user":"...","project":"...","local":"..."}
// matching the format used by the server's flattenMCPConfigs.
func flattenMCPConfigBytes(user, project, local []byte) *string {
	flat := make(map[string]string, 3)
	if len(user) > 0 {
		flat["user"] = string(user)
	}
	if len(project) > 0 {
		flat["project"] = string(project)
	}
	if len(local) > 0 {
		flat["local"] = string(local)
	}
	if len(flat) == 0 {
		return nil
	}
	encoded, err := json.Marshal(flat)
	if err != nil {
		return nil
	}
	s := string(encoded)
	return &s
}

func flattenWorkflows(workflows []*reliantv1.IndexedWorkflow) *string {
	if len(workflows) == 0 {
		return nil
	}
	items := make([]storedWorkflow, 0, len(workflows))
	for _, w := range workflows {
		items = append(items, storedWorkflow{
			Slug:        w.Slug,
			Name:        w.Name,
			YAMLContent: string(w.YamlContent),
			ContentHash: w.ContentHash,
		})
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil
	}
	s := string(encoded)
	return &s
}

func flattenPresets(presets []*reliantv1.IndexedPreset) *string {
	if len(presets) == 0 {
		return nil
	}
	items := make([]storedPreset, 0, len(presets))
	for _, p := range presets {
		items = append(items, storedPreset{
			Name:        p.Name,
			YAMLContent: string(p.YamlContent),
			ContentHash: p.ContentHash,
		})
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil
	}
	s := string(encoded)
	return &s
}

func flattenScenarios(scenarios []*reliantv1.IndexedScenario) *string {
	if len(scenarios) == 0 {
		return nil
	}
	items := make([]storedScenario, 0, len(scenarios))
	for _, s := range scenarios {
		items = append(items, storedScenario{
			WorkflowSlug: s.WorkflowSlug,
			Name:         s.Name,
			YAMLContent:  string(s.YamlContent),
			ContentHash:  s.ContentHash,
		})
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil
	}
	v := string(encoded)
	return &v
}

// flattenSkills converts the proto skills snapshot into the JSON blob that
// StoredConfigProvider parses into config.StoredSkill. The shape matches the
// server-side flattenIndexedSkills in tools_daemon.go exactly.
func flattenSkills(skills []*reliantv1.IndexedSkill) *string {
	if len(skills) == 0 {
		return nil
	}
	items := make([]config.StoredSkill, 0, len(skills))
	for _, s := range skills {
		if s == nil {
			continue
		}
		var userInvocable *bool
		switch s.UserInvocable {
		case "true":
			v := true
			userInvocable = &v
		case "false":
			v := false
			userInvocable = &v
		}
		items = append(items, config.StoredSkill{
			SkillPath:              s.SkillPath,
			Name:                   s.Name,
			Description:            s.Description,
			Scope:                  s.Scope,
			Body:                   s.Body,
			AllowedTools:           s.AllowedTools,
			Metadata:               s.Metadata,
			HasChildren:            s.HasChildren,
			DisableModelInvocation: s.DisableModelInvocation,
			UserInvocable:          userInvocable,
			ArgumentHint:           s.ArgumentHint,
			Paths:                  s.Paths,
			ContentHash:            s.ContentHash,
		})
	}
	if len(items) == 0 {
		return nil
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil
	}
	v := string(encoded)
	return &v
}
