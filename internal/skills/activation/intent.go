package activation

import (
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Intent describes the user turn mode before skill matching runs.
type Intent string

const (
	IntentExplicitSkill Intent = "explicit_skill"
	IntentMetaCatalog   Intent = "meta_catalog"
	IntentMetaHelp      Intent = "meta_help"
	IntentAdvisory      Intent = "advisory"
	IntentExecution     Intent = "execution"
)

// TurnInput contains the minimum inputs needed for intent classification.
type TurnInput struct {
	LatestUserMessage  string
	RecentUserMessages []string
	ActivationMode     string
}

// Classification captures high-level turn routing signals.
type Classification struct {
	Intent       Intent
	AutoEligible bool
	ExplicitName string
}

var metaCatalogPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b(what|which)\s+skills?\b`),
	regexp.MustCompile(`\b(skills?|tools|capabilities)\s+(do|can)\s+you\s+have\b`),
	regexp.MustCompile(`\bavailable\s+(skills?|tools)\b`),
	regexp.MustCompile(`\b(list|show|tell|display|enumerate)\b.*\b(skills?|tools|capabilities)\b`),
}

var advisoryPrefixes = []string{
	"how do i ",
	"how can i ",
	"what should i ",
	"can you explain ",
	"help me understand ",
}

// ClassifyTurn decides whether a user turn is eligible for skill auto-activation.
func ClassifyTurn(input TurnInput) Classification {
	explicit := extractExplicitInvocation(input.LatestUserMessage)
	if explicit != "" {
		return Classification{Intent: IntentExplicitSkill, AutoEligible: false, ExplicitName: explicit}
	}

	normalized := strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(input.LatestUserMessage))), " ")
	if normalized == "" {
		return Classification{Intent: IntentAdvisory, AutoEligible: true}
	}

	if isMetaCatalogQuery(normalized) {
		return Classification{Intent: IntentMetaCatalog, AutoEligible: false}
	}

	for _, prefix := range advisoryPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return Classification{Intent: IntentAdvisory, AutoEligible: true}
		}
	}

	return Classification{Intent: IntentExecution, AutoEligible: true}
}

func isMetaCatalogQuery(normalized string) bool {
	for _, pattern := range metaCatalogPatterns {
		if pattern.MatchString(normalized) {
			return true
		}
	}
	return false
}

func extractExplicitInvocation(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || !strings.HasPrefix(trimmed, "/") {
		return ""
	}
	if trimmed == "/skill" {
		return ""
	}
	if strings.HasPrefix(trimmed, "/skill ") {
		name := strings.TrimSpace(strings.TrimPrefix(trimmed, "/skill "))
		if name == "" {
			return ""
		}
		parts := strings.Fields(name)
		if len(parts) == 0 {
			return ""
		}
		return normalizeSkillName(parts[0])
	}

	cmd := strings.TrimPrefix(trimmed, "/")
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return ""
	}
	if strings.Contains(parts[0], "/") {
		return ""
	}
	return normalizeSkillName(parts[0])
}

func normalizeSkillName(name string) string {
	normalized := norm.NFKC.String(name)
	return strings.TrimSpace(strings.ToLower(normalized))
}
