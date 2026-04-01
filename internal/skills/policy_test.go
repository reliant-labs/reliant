package skills

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAllowedToolsPolicyEngine_FilterAllowedToolNames_NoActiveSkillPassthrough(t *testing.T) {
	engine := allowedToolsPolicyEngine{}
	available := []string{"bash", "view", "edit"}

	filtered, notices := engine.filterAllowedToolNames(nil, available)
	require.Equal(t, available, filtered)
	require.Empty(t, notices)
}

func TestAllowedToolsPolicyEngine_FilterAllowedToolNames_ExactAndWildcardRules(t *testing.T) {
	engine := allowedToolsPolicyEngine{}
	active := &Skill{Name: "safe-edit", AllowedTools: []string{"bash(*)", "view"}}
	available := []string{"bash", "view", "edit", "spawn"}

	filtered, notices := engine.filterAllowedToolNames(active, available)
	require.Equal(t, []string{"bash", "view"}, filtered)
	require.Len(t, notices, 1)
	require.Contains(t, notices[0].Message, "blocked")
	require.Contains(t, notices[0].Message, "edit")
	require.Contains(t, notices[0].Message, "spawn")
}

func TestAllowedToolsPolicyEngine_FilterAllowedToolNames_EmptyRulesPassthrough(t *testing.T) {
	engine := allowedToolsPolicyEngine{}
	active := &Skill{Name: "safe-edit", AllowedTools: []string{" ", "(ignored)"}}
	available := []string{"bash", "view"}

	filtered, notices := engine.filterAllowedToolNames(active, available)
	require.Equal(t, available, filtered)
	require.Empty(t, notices)
}
