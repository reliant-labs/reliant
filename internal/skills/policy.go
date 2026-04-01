package skills

import (
	"fmt"
	"sort"
	"strings"
)

type allowedToolsPolicyEngine struct{}

type toolAllowRule struct {
	raw      string
	toolName string
	wildcard bool
}

func parseAllowedToolsRules(values []string) []toolAllowRule {
	rules := make([]toolAllowRule, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}

		toolName := trimmed
		if idx := strings.Index(toolName, "("); idx >= 0 {
			toolName = toolName[:idx]
		}
		toolName = strings.TrimSpace(toolName)
		if toolName == "" {
			continue
		}

		rule := toolAllowRule{raw: trimmed}
		if strings.HasSuffix(toolName, "*") {
			rule.wildcard = true
			rule.toolName = strings.TrimSuffix(toolName, "*")
		} else {
			rule.toolName = toolName
		}
		rules = append(rules, rule)
	}
	return rules
}

func allowsToolByRule(toolName string, rules []toolAllowRule) bool {
	for _, rule := range rules {
		if rule.wildcard {
			if strings.HasPrefix(toolName, rule.toolName) {
				return true
			}
			continue
		}
		if toolName == rule.toolName {
			return true
		}
	}
	return false
}

func (allowedToolsPolicyEngine) filterAllowedToolNames(activeSkill *Skill, availableToolNames []string) ([]string, []Notice) {
	if len(availableToolNames) == 0 {
		return nil, nil
	}
	if activeSkill == nil || len(activeSkill.AllowedTools) == 0 {
		return append([]string(nil), availableToolNames...), nil
	}

	rules := parseAllowedToolsRules(activeSkill.AllowedTools)
	if len(rules) == 0 {
		return append([]string(nil), availableToolNames...), nil
	}

	filtered := make([]string, 0, len(availableToolNames))
	blocked := make([]string, 0, len(availableToolNames))
	for _, toolName := range availableToolNames {
		if allowsToolByRule(toolName, rules) {
			filtered = append(filtered, toolName)
		} else {
			blocked = append(blocked, toolName)
		}
	}

	if len(blocked) == 0 {
		return filtered, nil
	}

	sort.Strings(blocked)
	sort.Strings(filtered)
	notice := Notice{
		Level: NoticeLevelWarning,
		Message: fmt.Sprintf(
			"Skill '%s' allowed-tools policy blocked %d tool(s): %s",
			activeSkill.Name,
			len(blocked),
			strings.Join(blocked, ", "),
		),
	}
	return filtered, []Notice{notice}
}
