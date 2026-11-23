// Copyright (c) 2026 Reliant Labs

package credsandbox

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Gemini CLI's, Cursor Agent's and Copilot CLI's sign-ins are the
// deprecated-readable tier this release (owner, 2026-11-23): named in the plan
// for the notice, and neither hidden nor frozen, so a step that calls those
// CLIs under credentials: none keeps working until the next minor.
func TestDefaultPlan_DeprecatedReadableSignInsAreNamedNotHidden(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("FORGE_HOME", "")
	p := DefaultPlan()

	want := []string{
		filepath.Join(home, ".config", "cursor"),
		filepath.Join(home, ".copilot"),
		filepath.Join(home, ".cursor"),
		filepath.Join(home, ".gemini"),
	}
	assert.Equal(t, want, p.DeprecatedReadable)
	for _, w := range want {
		assert.NotContains(t, p.Hide, w, "still readable this release")
		assert.NotContains(t, p.ReadOnly, w)
		assert.NotContains(t, p.Seal, w)
	}
	assert.Contains(t, p.Hide, filepath.Join(home, ".claude"), "Claude's sign-in stays hidden")
}

func TestDeprecatedReadableNotice_OneLineNamingEachPath(t *testing.T) {
	assert.Empty(t, DeprecatedReadableNotice(nil), "nothing there: nothing to say")
	assert.Equal(t,
		"reliant: /h/.gemini, /h/.cursor will be hidden from credentials: none steps in the next minor release; "+
			"use credentials: inherit\n",
		DeprecatedReadableNotice([]string{"/h/.gemini", "/h/.cursor"}))
}
