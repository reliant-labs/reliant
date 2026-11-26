package commitpolicy

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// RuleForgeStateIgnored names the finding for forge state that must travel
// with the repository but is excluded from git.
const RuleForgeStateIgnored = "forge-state-ignored"

// forgeStateProbe is the file name probed under a committed .forge/
// directory. It never exists; `git check-ignore` answers from the rules.
const forgeStateProbe = "commit-policy-probe"

// committedForgeState is the .forge/ state the scaffold's .gitignore negates
// back in (`.forge/*` then `!.forge/scaffolded/`, ...), probed by path, with
// what an ignored copy costs. releases/ and promotions/ are committed only by
// envs that keep their ledger in the repo, so they are not probed.
var committedForgeState = []struct{ path, what string }{
	{".forge/scaffolded/" + forgeStateProbe, "the scaffold-once birth ledger, which is what makes deleting a scaffolded file stick: " +
		"with it ignored, every fresh clone's first `forge generate` re-creates each scaffolded page, test and seam you deleted"},
	{".forge/disowned.json", "the disown ledger: with it ignored, every fresh clone regenerates the files `forge project disown` handed over"},
	{".forge/hashes/" + forgeStateProbe, "the render-hash certificates of generated JSON files: with them ignored, a fresh clone cannot tell forge's output from an edit"},
}

// IgnoredForgeState reports the committed .forge/ state the project's
// .gitignore excludes. The usual cause is a `.forge/` line, which ignores the
// directory itself, so git cannot re-include anything below it and the
// scaffold's `!.forge/scaffolded/` negations never apply: a repository with
// its own .gitignore that adopted forge with `forge project new --in-place`
// kept exactly that line.
//
// It is separate from Check, which `forge ci verify-generated` fails on: this
// rule reaches projects through `forge lint`'s rollout (a warning first),
// not as a new hard failure in their CI.
//
// nil, nil outside a git work tree.
func IgnoredForgeState(projectDir string) ([]Violation, error) {
	if !inWorkTree(projectDir) {
		return nil, nil
	}
	paths := make([]string, 0, len(committedForgeState)+1)
	for _, s := range committedForgeState {
		paths = append(paths, s.path)
	}
	paths = append(paths, forgeDir)
	ignored, err := checkIgnoreMatches(projectDir, paths)
	if err != nil {
		return nil, err
	}
	dirRule, dirExcluded := forgeDirExclusion(projectDir, ignored)
	var out []Violation
	for _, s := range committedForgeState {
		match, ok := ignored[s.path]
		if !ok {
			continue
		}
		rel := strings.TrimSuffix(s.path, forgeStateProbe)
		fix := forgeStateFix(rel, match)
		if dirExcluded {
			match, fix = dirRule, forgeDirFix(dirRule)
		}
		out = append(out, Violation{
			Rule: RuleForgeStateIgnored,
			Path: rel,
			Why:  fmt.Sprintf("%s is %s, but %s excludes it from git", rel, s.what, match),
			Fix:  fix,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// forgeDir is the .forge directory, probed as itself: a rule that excludes it
// cannot be undone by a negation of anything below it.
const forgeDir = ".forge"

// forgeStateNegations are the lines the scaffold's .gitignore puts below
// `.forge/*` to re-include the committed state.
const forgeStateNegations = "`!.forge/scaffolded/`, `!.forge/disowned.json` and `!.forge/hashes/`"

// forgeDirExclusion reports the rule that excludes the .forge directory
// itself, as opposed to one that excludes its contents (`.forge/*`).
//
// Git answers it exactly when .forge exists: a directory-only rule (`.forge/`,
// `.*/`) applies to a path only when git can see it is a directory. With no
// .forge on disk, a directory-only rule matched against a path below it
// excludes .forge itself when its last segment matches the name ".forge".
func forgeDirExclusion(projectDir string, ignored map[string]ignoreMatch) (ignoreMatch, bool) {
	if m, ok := ignored[forgeDir]; ok {
		return m, true
	}
	if info, err := os.Stat(filepath.Join(projectDir, forgeDir)); err == nil && info.IsDir() {
		return ignoreMatch{}, false
	}
	for _, s := range committedForgeState {
		m, ok := ignored[s.path]
		if ok && excludesForgeDir(m.pattern) {
			return m, true
		}
	}
	return ignoreMatch{}, false
}

// excludesForgeDir reports whether a directory-only pattern names a
// directory called .forge.
func excludesForgeDir(pattern string) bool {
	if !strings.HasSuffix(pattern, "/") {
		return false
	}
	trimmed := strings.TrimSuffix(pattern, "/")
	last := trimmed[strings.LastIndex(trimmed, "/")+1:]
	ok, err := path.Match(last, forgeDir)
	return err == nil && ok
}

// forgeDirFix is the change when the .forge directory itself is excluded. Git
// never looks inside an excluded directory, so a `!` line for a path below it
// does nothing: the rule has to stop matching the directory first.
func forgeDirFix(rule ignoreMatch) string {
	lead := "the .forge directory itself is excluded by `" + rule.pattern + "` (" + rule.source + ":" + rule.line +
		"), and git cannot re-include anything below an excluded directory, so a `!` line for this path alone does nothing: "
	trimmed := strings.TrimPrefix(strings.Trim(rule.pattern, "/"), "**/")
	if trimmed == forgeDir {
		return lead + "replace `" + rule.pattern + "` with `.forge/*` (its contents, not the directory) and put " +
			forgeStateNegations + " below it, then `git add .forge/`"
	}
	return lead + "narrow `" + rule.pattern + "` so it does not match .forge, or re-include the directory right below it " +
		"with `!.forge/`, then `.forge/*` and " + forgeStateNegations + ", then `git add .forge/`"
}

// forgeStateFix is the change that re-includes rel when the .forge directory
// is not itself excluded, chosen by the rule that excludes rel: the rule is
// kept (`.forge/*` with a negation missing, a broader `*.json`), and a
// negation for rel goes below it.
func forgeStateFix(rel string, match ignoreMatch) string {
	switch strings.Trim(match.pattern, "/") {
	case ".forge/*", "**/.forge/*":
		return "keep `" + match.pattern + "` and re-include this path: add `!" + rel + "` on a line below it in " +
			match.source + " (the scaffold's .gitignore re-includes .forge/scaffolded/, .forge/disowned.json and .forge/hashes/), " +
			"then `git add " + rel + "`"
	default:
		return "re-include this path: add `!" + rel + "` on a line below `" + match.pattern + "` (" +
			match.source + ":" + match.line + "), then `git add " + rel + "`"
	}
}
