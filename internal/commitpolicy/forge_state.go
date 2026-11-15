package commitpolicy

import (
	"fmt"
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
	paths := make([]string, 0, len(committedForgeState))
	for _, s := range committedForgeState {
		paths = append(paths, s.path)
	}
	ignored, err := checkIgnore(projectDir, paths)
	if err != nil {
		return nil, err
	}
	var out []Violation
	for _, s := range committedForgeState {
		rule, ok := ignored[s.path]
		if !ok {
			continue
		}
		path := strings.TrimSuffix(s.path, forgeStateProbe)
		out = append(out, Violation{
			Rule: RuleForgeStateIgnored,
			Path: path,
			Why:  fmt.Sprintf("%s is %s, but %s excludes it from git", path, s.what, rule),
			Fix: "ignore the children of .forge/, not the directory: replace `.forge/` with `.forge/*` " +
				"(the scaffold's .gitignore then re-includes .forge/scaffolded/, .forge/disowned.json and .forge/hashes/), " +
				"then `git add .forge/`",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
