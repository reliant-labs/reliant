package forgetest

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deploy/kcl/fixtures_guard.k, both ways: a fixture-mode frontend renders only
// when every origin in its runtime config is empty or under .invalid, and a
// frontend not in fixture mode is none of its business.
func TestFixturesGuard(t *testing.T) {
	forge := Pinned(t) // Use the same KCL evaluator as env build, in the full lane.
	t.Setenv("FORGE_LEDGER", "machine")
	t.Setenv("FORGE_LEDGER_HOME", t.TempDir())
	root := kclRoot(t)
	eval := func(t *testing.T, file, selector string) (string, error) {
		t.Helper()
		cmd := forge.Command(t.Context(), "kcl", "eval", filepath.Join("deploy", "kcl", "testdata", file), "-S", selector)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	for _, selector := range []string{"inert", "live_without_fixtures"} {
		t.Run("renders/"+selector, func(t *testing.T) {
			out, err := eval(t, "fixtures_guard_render.k", selector)
			require.NoError(t, err, "%s", out)
			assert.Contains(t, out, `"web"`)
		})
	}

	for file, live := range map[string]string{
		"fixtures_guard_refused_api.k":  "[API_URL]",
		"fixtures_guard_refused_key.k":  "[API_URL, SUPABASE_URL]",
		"fixtures_guard_refused_otel.k": "[OTEL_ENDPOINT]",
	} {
		t.Run("refuses/"+file, func(t *testing.T) {
			out, err := eval(t, file, "refused")
			require.Error(t, err, "%s rendered a fixture-mode frontend with a live origin", file)
			assert.Contains(t, out, "serves a fixture pack, but its runtime config points "+live+" at a live origin",
				"%s: refused, but not by fixtures_guard.k for %s", file, live)
		})
	}
}
