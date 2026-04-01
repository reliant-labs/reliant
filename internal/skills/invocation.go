package skills

import (
	"fmt"
	"sort"
	"strings"

	skillscore "github.com/reliant-labs/reliant/internal/skills/core"
)

func extractExplicitInvocation(text string) string {
	query, _ := parseExplicitInvocation(text)
	return query
}

func resolveExplicitInvocation(result Result, text string) (*Skill, string) {
	query, allowPrefix := parseExplicitInvocation(text)
	if query == "" {
		return nil, ""
	}

	if exact, ok := result.ByName[query]; ok {
		s := exact
		return &s, ""
	}

	if !allowPrefix {
		return nil, fmt.Sprintf("Requested skill '%s' was not found.", query)
	}

	matches := make([]Skill, 0)
	for name, s := range result.ByName {
		if strings.HasPrefix(name, query) {
			matches = append(matches, s)
		}
	}

	if len(matches) == 0 {
		return nil, fmt.Sprintf("Requested skill '%s' was not found.", query)
	}
	if len(matches) == 1 {
		s := matches[0]
		return &s, fmt.Sprintf("Resolved partial skill '%s' to '%s'.", query, s.Name)
	}

	names := make([]string, 0, len(matches))
	for _, s := range matches {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return nil, fmt.Sprintf("Skill '%s' is ambiguous. Matches: %s.", query, strings.Join(names, ", "))
}

func parseExplicitInvocation(text string) (query string, allowPrefix bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || !strings.HasPrefix(trimmed, "/") {
		return "", false
	}

	if trimmed == "/skill" {
		return "", false
	}
	if strings.HasPrefix(trimmed, "/skill ") {
		name := strings.TrimSpace(strings.TrimPrefix(trimmed, "/skill "))
		if name == "" {
			return "", false
		}
		parts := strings.Fields(name)
		if len(parts) == 0 {
			return "", false
		}
		return skillscore.NormalizeSkillName(parts[0]), true
	}

	cmd := strings.TrimPrefix(trimmed, "/")
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return "", false
	}
	if strings.Contains(parts[0], "/") {
		return "", false
	}
	return skillscore.NormalizeSkillName(parts[0]), false
}
