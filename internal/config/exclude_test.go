package config

import "testing"

// TestMatchExclude covers the canonical contract for the shared
// contracts.exclude matcher. Three places in forge used to hand-roll
// this rule (config.ContractsConfig.IsExcluded, contract.IsExcluded,
// forgeconv/internal_pkg_contract.go), and they drifted on the
// empty-pattern + slash-normalisation edges. The single test below is
// the lone behavioural source of truth — if you change the matcher,
// update this table.
func TestMatchExclude(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		patterns []string
		pkgPath  string
		want     bool
	}{
		{
			name:     "empty pattern list",
			patterns: nil,
			pkgPath:  "internal/foo",
			want:     false,
		},
		{
			name:     "exact equality match",
			patterns: []string{"internal/foo"},
			pkgPath:  "internal/foo",
			want:     true,
		},
		{
			name:     "/-suffix match — pattern is the package's leaf path",
			patterns: []string{"foo"},
			pkgPath:  "github.com/example/internal/foo",
			want:     true,
		},
		{
			name:     "/-suffix match — pattern is deeper than the leaf",
			patterns: []string{"linter/contract"},
			pkgPath:  "github.com/example/internal/linter/contract",
			want:     true,
		},
		{
			name:     "substring match — pattern embedded mid-path",
			patterns: []string{"linter"},
			pkgPath:  "github.com/example/internal/linter/contract",
			want:     true,
		},
		{
			name:     "no match — pattern is unrelated",
			patterns: []string{"unrelated"},
			pkgPath:  "github.com/example/internal/foo",
			want:     false,
		},
		{
			name:     "empty pattern is skipped (regression: config.go pre-2026-06 would over-match)",
			patterns: []string{""},
			pkgPath:  "github.com/example/internal/anything",
			want:     false,
		},
		{
			name:     "empty pattern alongside a real one still matches the real one",
			patterns: []string{"", "foo"},
			pkgPath:  "github.com/example/internal/foo",
			want:     true,
		},
		{
			name:     "multiple patterns — first matches",
			patterns: []string{"foo", "bar", "baz"},
			pkgPath:  "github.com/example/internal/foo",
			want:     true,
		},
		{
			name:     "multiple patterns — last matches",
			patterns: []string{"foo", "bar", "baz"},
			pkgPath:  "github.com/example/internal/baz",
			want:     true,
		},
		{
			name:     "multiple patterns — none matches",
			patterns: []string{"foo", "bar", "baz"},
			pkgPath:  "github.com/example/internal/qux",
			want:     false,
		},
		// Segment-boundary regression suite (cp-forge authutil incident,
		// 2026-06): the old raw strings.Contains rule made the exclude
		// entry "internal/auth" swallow "internal/authutil" — and there
		// was NO fuller spelling the project owner could use to escape
		// the over-match, because the pattern already WAS the full path
		// of the package they wanted excluded. forge generate therefore
		// silently never re-emitted internal/authutil/mock_gen.go, and
		// the only workaround was forking a byte-identical "generated"
		// file to keep it alive past the stale-artifact sweep. Matching
		// is now segment-aware: a pattern only matches whole path
		// segments (equality, "/"-suffix, "/"-prefix subtree, or
		// "/pattern/" mid-path), never a partial segment.
		{
			name:     "segment boundary — sibling sharing a prefix is NOT excluded",
			patterns: []string{"internal/auth"},
			pkgPath:  "internal/authutil",
			want:     false,
		},
		{
			name:     "segment boundary — leaf shorthand does not match a longer leaf",
			patterns: []string{"auth"},
			pkgPath:  "internal/authutil",
			want:     false,
		},
		{
			name:     "segment boundary — leaf shorthand still matches the exact leaf",
			patterns: []string{"auth"},
			pkgPath:  "internal/auth",
			want:     true,
		},
		{
			name:     "subtree — excluding a directory still excludes its descendants",
			patterns: []string{"internal/auth"},
			pkgPath:  "internal/auth/oidc",
			want:     true,
		},
		{
			name:     "subtree — descendant of a mid-path segment match",
			patterns: []string{"auth"},
			pkgPath:  "internal/auth/oidc",
			want:     true,
		},
		{
			name:     "segment boundary — multi-segment pattern does not match a partial trailing segment",
			patterns: []string{"billing/provider"},
			pkgPath:  "internal/billing/provideradapters",
			want:     false,
		},
		{
			name:     "trailing slash on the pattern is tolerated",
			patterns: []string{"internal/auth/"},
			pkgPath:  "internal/auth/oidc",
			want:     true,
		},
		// Slash normalisation is OS-dependent (filepath.ToSlash is a
		// no-op when the OS separator is already `/`). The matcher
		// always normalises both sides via filepath.ToSlash, but
		// asserting it portably from a POSIX test runner is awkward;
		// the behaviour is covered by the Windows CI surface and the
		// package doc on MatchExclude. We keep the obvious mid-path
		// case below to lock the substring rule down.
		{
			name:     "substring match — slashes left intact on POSIX",
			patterns: []string{"internal/linter"},
			pkgPath:  "github.com/example/internal/linter/contract",
			want:     true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := MatchExclude(tc.patterns, tc.pkgPath); got != tc.want {
				t.Errorf("MatchExclude(%v, %q) = %v; want %v", tc.patterns, tc.pkgPath, got, tc.want)
			}
		})
	}
}

// TestContractsConfig_IsExcluded_DelegatesToMatchExclude verifies the
// ContractsConfig.IsExcluded method is a thin shim over MatchExclude —
// the test exists to fail loudly if anyone forks the implementation
// off MatchExclude again.
func TestContractsConfig_IsExcluded_DelegatesToMatchExclude(t *testing.T) {
	t.Parallel()
	cfg := ContractsConfig{Exclude: []string{"foo", "bar"}}
	if !cfg.IsExcluded("internal/foo") {
		t.Error("ContractsConfig.IsExcluded should match the 'foo' pattern")
	}
	if cfg.IsExcluded("internal/qux") {
		t.Error("ContractsConfig.IsExcluded should not match an unrelated path")
	}
}
