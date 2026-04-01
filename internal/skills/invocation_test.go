package skills

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractExplicitInvocation(t *testing.T) {
	require.Equal(t, "explain-code", extractExplicitInvocation("/explain-code"))
	require.Equal(t, "explain-code", extractExplicitInvocation("/skill explain-code"))
	require.Equal(t, "", extractExplicitInvocation("hello"))
	require.Equal(t, "", extractExplicitInvocation("/skill   "))
}

func TestResolveExplicitInvocation(t *testing.T) {
	result := Result{ByName: map[string]Skill{
		"explain-code":     {Name: "explain-code", NormalizedKey: "explain-code"},
		"explain-frontend": {Name: "explain-frontend", NormalizedKey: "explain-frontend"},
	}}

	t.Run("exact slash command", func(t *testing.T) {
		s, notice := resolveExplicitInvocation(result, "/explain-code")
		require.NotNil(t, s)
		require.Equal(t, "explain-code", s.Name)
		require.Equal(t, "", notice)
	})

	t.Run("prefix via /skill resolves when unique", func(t *testing.T) {
		s, notice := resolveExplicitInvocation(result, "/skill explain-front")
		require.NotNil(t, s)
		require.Equal(t, "explain-frontend", s.Name)
		require.Contains(t, notice, "Resolved partial skill")
	})

	t.Run("prefix via /skill ambiguous", func(t *testing.T) {
		s, notice := resolveExplicitInvocation(result, "/skill explain")
		require.Nil(t, s)
		require.Contains(t, notice, "ambiguous")
	})

	t.Run("missing skill", func(t *testing.T) {
		s, notice := resolveExplicitInvocation(result, "/missing")
		require.Nil(t, s)
		require.Contains(t, notice, "not found")
	})
}
