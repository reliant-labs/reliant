package lint

import (
	"path/filepath"
	"testing"
)

// A scoped run that names no path inside any frontend skips the frontend
// typecheck (an install plus a whole-frontend tsc whose findings the scope
// would drop anyway); a scope inside or around a frontend still runs it.
// Forge bug report 77e3df15: `forge lint --scope internal/...` ran
// `pnpm install` and the web typecheck.
func TestLintScopeReachesAnyDir(t *testing.T) {
	scope := func(paths ...string) *lintScope { return &lintScope{root: "/p", paths: paths} }
	dirs := []string{"frontends/web", "./frontends/admin"}
	cases := []struct {
		name  string
		scope *lintScope
		want  bool
	}{
		{"whole project", nil, true},
		{"go packages only", scope("internal/integrations/githubqueue", "internal/llm"), false},
		{"a file in a frontend", scope("frontends/web/src/app.tsx"), true},
		{"the frontend itself", scope("frontends/admin"), true},
		{"a parent of the frontends", scope("frontends"), true},
		{"a sibling with a shared prefix", scope("frontends/webhooks"), false},
		// Spellings of the same path: every one is normalized before the
		// ancestry test, so none of them silently skips the lane.
		{"a ./-prefixed file in a frontend", scope("./frontends/web/src/app.tsx"), true},
		{"a path through ..", scope("frontends/web/../web/src"), true},
		{"a trailing slash", scope("frontends/web/"), true},
		{"a ./-prefixed Go path", scope("./internal/llm"), false},
	}
	for _, c := range cases {
		if got := c.scope.reachesAnyDir(dirs); got != c.want {
			t.Errorf("%s: reachesAnyDir = %v, want %v", c.name, got, c.want)
		}
	}
}

// contains and reachesAnyDir share scopeRel, so a scope's spelling never
// changes which files it holds.
func TestLintScopeContainsNormalizesBothSides(t *testing.T) {
	// A real absolute root: "/p" has no volume, so on Windows it is not
	// absolute and contains would never strip it from an absolute file.
	root := t.TempDir()
	absFile := filepath.Join(root, "frontends", "web", "src", "app.tsx")
	for _, raw := range []string{"frontends/web/src", "./frontends/web/src", "frontends/web/../web/src", "frontends/web/src/"} {
		s := &lintScope{root: root, paths: []string{raw}}
		for _, f := range []string{"frontends/web/src/app.tsx", "./frontends/web/src/app.tsx", absFile} {
			if !s.contains(f) {
				t.Errorf("scope %q: contains(%q) = false, want true", raw, f)
			}
		}
		if s.contains("frontends/web/srcx/app.tsx") {
			t.Errorf("scope %q: contains a sibling with a shared prefix", raw)
		}
	}
}
