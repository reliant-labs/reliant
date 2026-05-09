package daemonruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"sort"
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

func (f *filesystemConfigStore) GetProjectConfigRecord(ctx context.Context, projectPath string) (*config.StoredProjectConfigRecord, error) {
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

	// Walk nested repos and pull in their per-repo mcp.json + .reliant.local/mcp.json
	// files. Each contributes to the "project" scope (and "local" for the
	// .reliant.local variant). Two repos that declare a server with the same
	// (command, args, env) tuple collapse into one entry — a monorepo with
	// shared tooling typically does this.
	repoSources := discoverRepoSources(ctx, projectPath)
	repoProjectMCPs := make([][]byte, 0, len(repoSources))
	repoLocalMCPs := make([][]byte, 0, len(repoSources))
	for _, rel := range repoSources {
		if rel == "" {
			continue
		}
		if b, _ := readOptionalFile(filepath.Join(projectPath, rel, ".reliant", "mcp.json")); len(b) > 0 {
			repoProjectMCPs = append(repoProjectMCPs, b)
		}
		if b, _ := readOptionalFile(filepath.Join(projectPath, rel, ".reliant.local", "mcp.json")); len(b) > 0 {
			repoLocalMCPs = append(repoLocalMCPs, b)
		}
	}

	mergedProjectMCP := mergeMCPDocsDedup(append([][]byte{projectMCP}, repoProjectMCPs...)...)
	mergedLocalMCP := mergeMCPDocsDedup(append([][]byte{localMCP}, repoLocalMCPs...)...)

	mcpConfigs := flattenMCPConfigBytes(userMCP, mergedProjectMCP, mergedLocalMCP)

	workflows, _ := indexWorkflows(projectPath)
	presets, _ := indexPresets(projectPath)
	scenarios, _ := indexScenarios(projectPath)
	skills, _ := indexSkills(projectPath)
	repoMemories, _ := collectRepoMemories(projectPath)

	workflowsJSON := flattenWorkflows(workflows)
	presetsJSON := flattenPresets(presets)
	scenariosJSON := flattenScenarios(scenarios)
	skillsJSON := flattenSkills(skills)
	repoMemoriesJSON := flattenRepoMemories(repoMemories)

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
		RepoMemoriesJSON:     repoMemoriesJSON,
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

// mergeMCPDocsDedup merges N raw mcp.json byte blobs into a single
// {"mcpServers": {...}} document. Servers are deduped by the canonical JSON
// hash of (command, args, env). When two scopes/repos define semantically
// identical servers under different display names, the alphabetically-first
// name wins; the second is dropped silently. When multiple servers in a
// single doc share the same name, last-wins (standard map override).
//
// Returns nil when no input contains any servers.
func mergeMCPDocsDedup(docs ...[]byte) []byte {
	type serverEntry struct {
		name string
		raw  map[string]interface{}
	}
	// nameToServer keeps the canonical entry per server name.
	nameToServer := map[string]map[string]interface{}{}
	// hashToName maps the (command,args,env) hash to the first-claimed display
	// name so we can collapse duplicates across docs.
	hashToName := map[string]string{}
	// Order servers alphabetically by name so the "first" winner is stable.
	var allEntries []serverEntry

	for _, doc := range docs {
		if len(doc) == 0 {
			continue
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal(doc, &parsed); err != nil {
			continue
		}
		serversRaw, _ := parsed["mcpServers"].(map[string]interface{})
		if serversRaw == nil {
			continue
		}
		for name, cfg := range serversRaw {
			cfgMap, ok := cfg.(map[string]interface{})
			if !ok {
				continue
			}
			allEntries = append(allEntries, serverEntry{name: name, raw: cfgMap})
		}
	}

	// Sort by name so alphabetical-first wins on hash collision deterministically.
	sort.Slice(allEntries, func(i, j int) bool {
		return allEntries[i].name < allEntries[j].name
	})

	for _, e := range allEntries {
		h := canonicalServerHash(e.raw)
		if existingName, ok := hashToName[h]; ok && existingName != e.name {
			// Duplicate config under a different name — drop.
			continue
		}
		hashToName[h] = e.name
		nameToServer[e.name] = e.raw
	}

	if len(nameToServer) == 0 {
		return nil
	}
	out := map[string]interface{}{"mcpServers": nameToServer}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return encoded
}

// canonicalServerHash hashes (command, args, env) of an mcp server config so
// equal servers under different names collapse to one entry. The hash is
// stable across runs because we marshal a fixed-key, fixed-order struct.
func canonicalServerHash(cfg map[string]interface{}) string {
	type canonical struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
	}
	c := canonical{Env: map[string]string{}}
	if v, ok := cfg["command"].(string); ok {
		c.Command = v
	}
	if argsRaw, ok := cfg["args"].([]interface{}); ok {
		for _, a := range argsRaw {
			if s, ok := a.(string); ok {
				c.Args = append(c.Args, s)
			}
		}
	}
	if envRaw, ok := cfg["env"].(map[string]interface{}); ok {
		for k, v := range envRaw {
			if s, ok := v.(string); ok {
				c.Env[k] = s
			}
		}
	}
	// Sort env keys before marshaling so the JSON encoding is deterministic.
	envKeys := make([]string, 0, len(c.Env))
	for k := range c.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	sortedEnv := make(map[string]string, len(c.Env))
	// json.Marshal of a map sorts keys for us — but only with the std lib's
	// reflect-based encoder, which currently does sort. Use that.
	for _, k := range envKeys {
		sortedEnv[k] = c.Env[k]
	}
	c.Env = sortedEnv

	encoded, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
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
// flattenRepoMemories converts the proto repo memories map into a JSON object
// suitable for the StoredProjectConfigRecord. Maps repo relative path -> content string.
func flattenRepoMemories(memories map[string][]byte) *string {
	if len(memories) == 0 {
		return nil
	}
	strMap := make(map[string]string, len(memories))
	for k, v := range memories {
		strMap[k] = string(v)
	}
	encoded, err := json.Marshal(strMap)
	if err != nil {
		return nil
	}
	s := string(encoded)
	return &s
}

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
			Source:                 s.Source,
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
