// Copyright (c) 2025 Reliant Labs
package suggest_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/skills/catalog"
	"github.com/reliant-labs/reliant/internal/skills/suggest"
)

func builtinWorkflowBuilder(t *testing.T, withBody bool) config.StoredSkill {
	t.Helper()
	blob, err := catalog.ReadBuiltinSkillDefinition("workflow-builder/SKILL.md")
	require.NoError(t, err)
	def, err := catalog.ParseSkillMarkdown("workflow-builder/SKILL.md", "builtin", blob)
	require.NoError(t, err)
	s := config.StoredSkill{SkillPath: "workflow-builder", Name: def.Name, Description: def.Description}
	if withBody {
		s.Body = def.Body
	}
	return s
}

func names(got []suggest.Suggested) []string {
	var out []string
	for _, g := range got {
		out = append(out, g.Skill.Name)
	}
	return out
}

// The ask that went unrouted: a scheduled LLM bot in an empty project. Matching
// is on exact tokens, so the description alone (no body) has to carry it.
func TestSuggest_ScheduledBotAskSuggestsWorkflowBuilder(t *testing.T) {
	for _, withBody := range []bool{true, false} {
		skill := builtinWorkflowBuilder(t, withBody)
		for _, msg := range []string{
			"build a trader bot that wakes up 2x per day and places some trades",
			"I want a bot that checks my inbox every morning and posts a digest to Slack",
			"set up a cron job that monitors the site and alerts me on a schedule",
		} {
			got := suggest.Suggest([]config.StoredSkill{skill}, msg, 3)
			require.Contains(t, names(got), "workflow-builder", "body=%v msg=%q", withBody, msg)
		}
	}
}

// The suggestion reminder shows only the first 77 characters of a description,
// so the need must be in them.
func TestWorkflowBuilderDescriptionLeadsWithTheNeed(t *testing.T) {
	desc := builtinWorkflowBuilder(t, false).Description
	require.Greater(t, len(desc), 77)
	for _, want := range []string{"bot", "agent", "schedule", "event"} {
		require.Contains(t, desc[:77], want)
	}
}
