package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseStoredSkills_NormalizesMissingSkillPathFromName(t *testing.T) {
	raw := `[
		{"name":"testing-methodology","description":"Testing methodology","scope":"builtin","body":"body"},
		{"skill_path":"code-review/security-review","name":"security-review","description":"Security review","scope":"builtin"}
	]`

	skills, err := ParseStoredSkills(&raw)
	require.NoError(t, err)
	require.Len(t, skills, 2)
	require.Equal(t, "testing-methodology", skills[0].SkillPath)
	require.Equal(t, "code-review/security-review", skills[1].SkillPath)
}

func TestNormalizeStoredSkills_CanonicalizesExistingPathAndDoesNotMutateInput(t *testing.T) {
	input := []StoredSkill{
		{
			SkillPath:   ` code-review\security-review/ `,
			Name:        "security-review",
			Description: "Security review",
		},
	}

	normalized := NormalizeStoredSkills(input)
	require.Equal(t, ` code-review\security-review/ `, input[0].SkillPath)
	require.Equal(t, "code-review/security-review", normalized[0].SkillPath)
}
