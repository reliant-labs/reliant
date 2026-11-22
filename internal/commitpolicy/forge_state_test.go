package commitpolicy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// A rule that excludes the .forge directory itself cannot be answered with a
// `!path` line: git never looks inside an excluded directory. The hint leads
// with narrowing that rule; only a rule on the contents keeps `!path`.
func TestForgeStateFix_FollowsTheRule(t *testing.T) {
	t.Parallel()
	rule := func(pattern string) ignoreMatch {
		return ignoreMatch{source: ".gitignore", line: "7", pattern: pattern}
	}
	for _, tc := range []struct {
		name         string
		fix          string
		want, reject []string
	}{
		{"directory", forgeDirFix(rule(".forge/")),
			[]string{"the .forge directory itself is excluded", "replace `.forge/` with `.forge/*`", "`!.forge/scaffolded/`"},
			[]string{"narrow"}},
		{"directory, no slash", forgeDirFix(rule(".forge")), []string{"replace `.forge` with `.forge/*`"}, nil},
		{"directory, anchored", forgeDirFix(rule("/.forge/")), []string{"replace `/.forge/` with `.forge/*`"}, nil},
		{"broad directory rule", forgeDirFix(rule(".*/")),
			[]string{"the .forge directory itself is excluded by `.*/` (.gitignore:7)", "narrow `.*/`", "`!.forge/`, then `.forge/*`"},
			[]string{"replace"}},
		{"contents, negation missing", forgeStateFix(".forge/scaffolded/", rule(".forge/*")),
			[]string{"keep `.forge/*`", "add `!.forge/scaffolded/`", "`git add .forge/scaffolded/`"},
			[]string{"replace", "excluded"}},
		{"contents, anchored", forgeStateFix(".forge/scaffolded/", rule("/.forge/*")),
			[]string{"keep `/.forge/*`", "add `!.forge/scaffolded/`"}, []string{"replace"}},
		{"broader file rule", forgeStateFix(".forge/disowned.json", rule("*.json")),
			[]string{"add `!.forge/disowned.json` on a line below `*.json` (.gitignore:7)"}, []string{"replace", "excluded"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, w := range tc.want {
				if !strings.Contains(tc.fix, w) {
					t.Errorf("fix %q: want it to contain %q", tc.fix, w)
				}
			}
			for _, r := range tc.reject {
				if strings.Contains(tc.fix, r) {
					t.Errorf("fix %q: must not contain %q", tc.fix, r)
				}
			}
		})
	}
}

func TestExcludesForgeDir(t *testing.T) {
	t.Parallel()
	for pattern, want := range map[string]bool{
		".forge/": true, "/.forge/": true, "**/.forge/": true, ".*/": true, ".f*/": true,
		".forge": false, ".forge/*": false, ".forge/**": false, "forge/": false, "scaffolded/": false, "*.json": false,
	} {
		if got := excludesForgeDir(pattern); got != want {
			t.Errorf("excludesForgeDir(%q) = %v, want %v", pattern, got, want)
		}
	}
}

// Real git decides. For each .gitignore: the finding's hint, then the remedy
// that hint gives, applied, leaves nothing ignored, and for a rule that
// excludes .forge itself the `!path` lines alone are proven not to work. Each
// case runs with .forge on disk (git answers the directory probe) and without
// it (the pattern fallback).
func TestIgnoredForgeState_HintIsWhatGitNeeds(t *testing.T) {
	testkit.Slow(t, "execs git")
	const negations = "!.forge/scaffolded/\n!.forge/disowned.json\n!.forge/hashes/\n"
	for _, tc := range []struct {
		name, gitignore string
		dirExcluded     bool
		hint            string // every finding's Fix contains it; "" means no findings
		remedy          string // the .gitignore the hint leads to
	}{
		{"directory", ".forge/\n", true, "replace `.forge/` with `.forge/*`", ".forge/*\n" + negations},
		{"directory, no slash", ".forge\n", true, "replace `.forge` with `.forge/*`", ".forge/*\n" + negations},
		{"broad directory rule", ".*/\n", true, "narrow `.*/`", ".*/\n!.forge/\n.forge/*\n" + negations},
		{"contents, negations missing", ".forge/*\n", false, "keep `.forge/*`", ".forge/*\n" + negations},
		// forge/ names a directory called forge, not .forge: nothing is ignored.
		{"other directory", "forge/\n", false, "", ""},
	} {
		for _, onDisk := range []bool{true, false} {
			name := tc.name
			if !onDisk {
				name += ", no .forge on disk"
			}
			t.Run(name, func(t *testing.T) {
				dir := newRepo(t)
				if onDisk {
					if err := os.MkdirAll(filepath.Join(dir, ".forge", "scaffolded"), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				write(t, dir, ".gitignore", tc.gitignore)
				vs, err := IgnoredForgeState(dir)
				if err != nil {
					t.Fatal(err)
				}
				if tc.hint == "" {
					if len(vs) != 0 {
						t.Fatalf("want no findings, got %+v", vs)
					}
					return
				}
				if len(vs) != len(committedForgeState) {
					t.Fatalf("want a finding per committed path, got %+v", vs)
				}
				for _, v := range vs {
					if !strings.Contains(v.Fix, tc.hint) {
						t.Errorf("%s: fix %q lacks %q", v.Path, v.Fix, tc.hint)
					}
					if excluded := strings.Contains(v.Fix, "directory itself is excluded"); excluded != tc.dirExcluded {
						t.Errorf("%s: fix says the directory is excluded = %v, want %v: %q", v.Path, excluded, tc.dirExcluded, v.Fix)
					}
				}

				// The `!path` lines alone fix it exactly when .forge is not excluded.
				write(t, dir, ".gitignore", tc.gitignore+negations)
				if vs, err = IgnoredForgeState(dir); err != nil {
					t.Fatal(err)
				}
				if tc.dirExcluded && len(vs) == 0 {
					t.Error("a `!path` line re-included a path below an excluded .forge; the hint's premise is wrong")
				}
				if !tc.dirExcluded && len(vs) != 0 {
					t.Errorf("the `!path` lines did not re-include: %+v", vs)
				}

				write(t, dir, ".gitignore", tc.remedy)
				if vs, err = IgnoredForgeState(dir); err != nil {
					t.Fatal(err)
				}
				if len(vs) != 0 {
					t.Errorf("the hinted remedy %q still ignores: %+v", tc.remedy, vs)
				}
			})
		}
	}
}
