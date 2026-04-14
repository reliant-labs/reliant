// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type SkillParams struct {
	Action string `json:"action" jsonschema:"required,description=Action to perform: load (load a skill by path)\\, list (list available skills)\\, search (search skills by keyword),enum=load,enum=list,enum=search"`
	Path   string `json:"path,omitempty" jsonschema:"description=Skill path to load or list children of. Use skill name for top-level (e.g. 'go'\\, 'reliant-config')"`
	Query  string `json:"query,omitempty" jsonschema:"description=Search query for finding skills (used with action=search)"`
}

// skillTool operates entirely on a pre-loaded slice of skills. The skill data
// is synced from the daemon via the config pipeline and injected by the
// ToolsFactory — the tool itself never touches the filesystem.
type skillTool struct {
	skills []config.StoredSkill
}

const skillDescription = `Load skills — specialized knowledge and instructions for specific tasks.
Skills provide detailed guidance on how to perform particular operations.
Use 'list' to see available skills, 'load' to load a skill's instructions,
or 'search' to find skills by keyword.
When you load a skill, its instructions become available in the conversation.
Skills may suggest tools to load — use the load_tool tool if suggested tools are needed.`

func NewSkillTool(skills []config.StoredSkill) Tool {
	tool := &skillTool{skills: skills}
	return NewToolWrapper[SkillParams, ToolResponse](tool)
}

func (t *skillTool) Name() string {
	return ToolSkill
}

func (t *skillTool) Description() string {
	return skillDescription
}

func (t *skillTool) RequiresPermission(params SkillParams) (bool, error) {
	return false, nil
}

func (t *skillTool) Execute(_ *rctx.ToolContext, params SkillParams) (ToolResponse, error) {
	slog.Debug("[SkillTool] Execute", "action", params.Action, "path", params.Path, "query", params.Query, "availableSkills", len(t.skills))
	switch params.Action {
	case "list":
		return t.listSkills(params.Path)
	case "load":
		if params.Path == "" {
			return NewTextErrorResponse("path is required when action is 'load'"), nil
		}
		return t.loadSkill(params.Path)
	case "search":
		if params.Query == "" {
			return NewTextErrorResponse("query is required when action is 'search'"), nil
		}
		return t.searchSkills(params.Query)
	default:
		return NewTextErrorResponse(fmt.Sprintf("unknown action: %s (expected: load, list, search)", params.Action)), nil
	}
}

// topLevelSkills returns skills whose SkillPath has no '/' separator.
func topLevelSkills(skills []config.StoredSkill) []config.StoredSkill {
	var out []config.StoredSkill
	for _, s := range skills {
		if !strings.Contains(s.SkillPath, "/") {
			out = append(out, s)
		}
	}
	return out
}

// immediateChildren returns skills that are direct children of the given parent path.
// Parent "go" matches "go/defer" but not "go/error-handling/wrap-errors".
func immediateChildren(skills []config.StoredSkill, parentPath string) []config.StoredSkill {
	prefix := parentPath + "/"
	var out []config.StoredSkill
	for _, s := range skills {
		if !strings.HasPrefix(s.SkillPath, prefix) {
			continue
		}
		remainder := s.SkillPath[len(prefix):]
		if remainder == "" || strings.Contains(remainder, "/") {
			continue
		}
		out = append(out, s)
	}
	return out
}

// hasChildren returns true if any other skill's path is a descendant of the given path.
func hasChildren(skills []config.StoredSkill, path string) bool {
	prefix := path + "/"
	for _, s := range skills {
		if strings.HasPrefix(s.SkillPath, prefix) {
			return true
		}
	}
	return false
}

// findSkillByPath looks up a skill, falling back to case-insensitive match.
func findSkillByPath(skills []config.StoredSkill, path string) *config.StoredSkill {
	if direct := config.FindStoredSkillByPath(skills, path); direct != nil {
		return direct
	}
	lower := strings.ToLower(path)
	for i := range skills {
		if strings.ToLower(skills[i].SkillPath) == lower {
			return &skills[i]
		}
	}
	return nil
}

func (t *skillTool) listSkills(filterPath string) (ToolResponse, error) {
	normalizedFilter := strings.ToLower(strings.TrimSpace(filterPath))

	var matches []config.StoredSkill

	if normalizedFilter == "" {
		matches = topLevelSkills(t.skills)
	} else {
		// Verify the parent path exists before listing children — callers
		// expect an error if they ask for children of an unknown skill.
		if findSkillByPath(t.skills, normalizedFilter) == nil {
			return NewTextErrorResponse(fmt.Sprintf("no sub-skills found under: %s", filterPath)), nil
		}
		matches = immediateChildren(t.skills, normalizedFilter)
	}

	if len(matches) == 0 {
		if normalizedFilter == "" {
			return NewTextResponse("No skills available.\n\nSkills can be added to:\n- .reliant/skills/ (project)\n- ~/.reliant/skills/ (global)"), nil
		}
		return NewTextErrorResponse(fmt.Sprintf("no sub-skills found under: %s", filterPath)), nil
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].SkillPath < matches[j].SkillPath
	})

	var sb strings.Builder
	if normalizedFilter == "" {
		sb.WriteString("Available skills:\n\n")
	} else {
		sb.WriteString(fmt.Sprintf("Sub-skills of %s:\n\n", normalizedFilter))
	}

	for _, def := range matches {
		desc := def.Description
		if len(desc) > 120 {
			desc = desc[:117] + "..."
		}
		sb.WriteString(fmt.Sprintf("- %s: %s", def.SkillPath, desc))
		if hasChildren(t.skills, def.SkillPath) {
			sb.WriteString(" (has sub-skills)")
		}
		if def.Scope != "" {
			sb.WriteString(fmt.Sprintf(" [%s]", def.Scope))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("\nUse skill tool with action='load' and path='<skill-path>' to load a skill's instructions.")

	return NewTextResponse(sb.String()), nil
}

func (t *skillTool) loadSkill(path string) (ToolResponse, error) {
	normalizedPath := strings.ToLower(strings.TrimSpace(path))

	def := findSkillByPath(t.skills, normalizedPath)
	if def == nil {
		// Try partial match across all skills.
		var candidates []string
		for _, s := range t.skills {
			if strings.Contains(strings.ToLower(s.SkillPath), normalizedPath) {
				candidates = append(candidates, s.SkillPath)
			}
		}
		if len(candidates) > 0 {
			sort.Strings(candidates)
			return NewTextErrorResponse(fmt.Sprintf("skill not found: %s\n\nDid you mean one of these?\n- %s",
				path, strings.Join(candidates, "\n- "))), nil
		}
		return NewTextErrorResponse(fmt.Sprintf("skill not found: %s\n\nUse action='list' to see available skills.", path)), nil
	}

	var sb strings.Builder
	sb.WriteString(def.Body)

	// Show sub-skills if this skill has children.
	if hasChildren(t.skills, def.SkillPath) {
		children := immediateChildren(t.skills, def.SkillPath)
		if len(children) > 0 {
			sort.Slice(children, func(i, j int) bool {
				return children[i].SkillPath < children[j].SkillPath
			})
			sb.WriteString("\n\n---\nSub-skills available (use skill tool with action=list or action=load):\n")
			for _, child := range children {
				sb.WriteString(fmt.Sprintf("- %s: %s", child.SkillPath, truncateDescription(child.Description, 80)))
				if hasChildren(t.skills, child.SkillPath) {
					sb.WriteString(" (has sub-skills)")
				}
				sb.WriteString("\n")
			}
		}
	}

	// Find sibling skills (same parent path).
	siblings := findSiblingSkills(t.skills, *def)
	if len(siblings) > 0 {
		sb.WriteString("\n---\nRelated skills available (use skill tool to load):\n")
		for _, s := range siblings {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", s.SkillPath, truncateDescription(s.Description, 80)))
		}
	}

	// Append allowed tools suggestion.
	if len(def.AllowedTools) > 0 {
		sb.WriteString("\n---\nThis skill suggests loading these tools: ")
		sb.WriteString(strings.Join(def.AllowedTools, ", "))
		sb.WriteString("\n")
	}

	return NewTextResponse(sb.String()), nil
}

func (t *skillTool) searchSkills(query string) (ToolResponse, error) {
	if len(t.skills) == 0 {
		return NewTextResponse("No skills available to search."), nil
	}

	queryLower := strings.ToLower(query)
	queryWords := strings.Fields(queryLower)

	type scoredDef struct {
		def   config.StoredSkill
		score int
	}
	var results []scoredDef

	for _, def := range t.skills {
		score := 0
		pathLower := strings.ToLower(def.SkillPath)
		nameLower := strings.ToLower(def.Name)
		descLower := strings.ToLower(def.Description)

		for _, word := range queryWords {
			if strings.Contains(pathLower, word) {
				score += 3 // Path match (includes name) is worth more
			} else if strings.Contains(nameLower, word) {
				score += 3
			}
			if strings.Contains(descLower, word) {
				score += 1
			}
		}

		if def.Body != "" {
			bodyLower := strings.ToLower(def.Body)
			for _, word := range queryWords {
				if strings.Contains(bodyLower, word) {
					score += 1
				}
			}
		}

		if score > 0 {
			results = append(results, scoredDef{def: def, score: score})
		}
	}

	if len(results) == 0 {
		return NewTextResponse(fmt.Sprintf("No skills found matching query: %s\n\nUse action='list' to see all available skills.", query)), nil
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		return results[i].def.SkillPath < results[j].def.SkillPath
	})

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Skills matching \"%s\":\n\n", query))

	for _, r := range results {
		desc := r.def.Description
		if len(desc) > 120 {
			desc = desc[:117] + "..."
		}
		sb.WriteString(fmt.Sprintf("- %s: %s\n", r.def.SkillPath, desc))
	}

	sb.WriteString("\nUse skill tool with action='load' and path='<skill-path>' to load a skill's instructions.")

	return NewTextResponse(sb.String()), nil
}

// findSiblingSkills returns skills that share the same parent skill path (excluding the skill itself).
func findSiblingSkills(skills []config.StoredSkill, skill config.StoredSkill) []config.StoredSkill {
	if skill.SkillPath == "" {
		return nil
	}
	// Determine the parent path. e.g. "go/error-handling" -> "go", "go" -> ""
	parentPath := ""
	if idx := strings.LastIndex(skill.SkillPath, "/"); idx >= 0 {
		parentPath = skill.SkillPath[:idx]
	}
	if parentPath == "" {
		// Top-level skill — no meaningful siblings to show.
		return nil
	}

	prefix := parentPath + "/"
	var siblings []config.StoredSkill
	for _, def := range skills {
		if def.SkillPath == skill.SkillPath {
			continue
		}
		if !strings.HasPrefix(def.SkillPath, prefix) {
			continue
		}
		// Only immediate siblings (no further nesting).
		remainder := def.SkillPath[len(prefix):]
		if !strings.Contains(remainder, "/") {
			siblings = append(siblings, def)
		}
	}

	sort.Slice(siblings, func(i, j int) bool {
		return siblings[i].SkillPath < siblings[j].SkillPath
	})

	return siblings
}

func truncateDescription(desc string, maxLen int) string {
	if len(desc) <= maxLen {
		return desc
	}
	return desc[:maxLen-3] + "..."
}

// SkillsAnnouncement returns a brief summary of top-level skills for system
// prompt injection. Operates purely on the provided slice — no filesystem.
// Returns empty string when no skills are eligible.
func SkillsAnnouncement(skills []config.StoredSkill) string {
	tops := topLevelSkills(skills)
	if len(tops) == 0 {
		return ""
	}

	sort.Slice(tops, func(i, j int) bool {
		return tops[i].SkillPath < tops[j].SkillPath
	})

	var sb strings.Builder
	sb.WriteString("\n\n<system-reminder>\nAvailable skills (use the skill tool to load):\n")
	for _, def := range tops {
		desc := def.Description
		if len(desc) > 80 {
			desc = desc[:77] + "..."
		}
		sb.WriteString(fmt.Sprintf("- %s: %s", def.SkillPath, desc))
		if hasChildren(skills, def.SkillPath) {
			sb.WriteString(" (has sub-skills)")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("</system-reminder>")

	return sb.String()
}
