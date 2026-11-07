package templates

import (
	"strings"
	"testing"
)

// TestScaffoldGitignoreNegationsAreReachable enforces one git rule across the
// whole scaffolded .gitignore:
//
//	git does not descend into an EXCLUDED DIRECTORY, so a `!dir/file`
//	negation under a `dir/` exclusion never runs. The file is ignored, and
//	silently — nothing warns, `git status` is clean, and the omission only
//	surfaces in a fresh clone.
//
// The template already states this rule in full beside `.forge/*` ("git cannot
// re-include a file whose parent DIRECTORY is excluded … Found the hard way in
// cp-forge"), and then broke it twelve lines earlier with:
//
//	gen/
//	!gen/go.mod
//	!gen/go.sum
//
// so `gen/go.mod` was never committed by any forge project. The root go.mod
// keeps `require <mod>/gen v0.0.0` + `replace <mod>/gen => ./gen`, so a clone
// resolves that replace to a directory with no go.mod and `forge generate` dies
// on `reading gen/go.mod: no such file or directory` before it can rewrite it.
// The correct spelling excludes the CHILDREN (`gen/*`), which leaves the
// negations reachable and still ignores every generated file underneath.
//
// Knowing the rule in a comment is not the same as applying it, which is why
// this is a test and not a third comment.
func TestScaffoldGitignoreNegationsAreReachable(t *testing.T) {
	data, err := templateFS.ReadFile("project/.gitignore")
	if err != nil {
		t.Fatalf("read scaffolded .gitignore: %v", err)
	}

	// Directory exclusions, by the directory prefix they shadow.
	excludedDirs := map[string]int{} // "gen/" -> line number
	type negation struct {
		path string
		line int
	}
	var negations []negation

	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if neg, ok := strings.CutPrefix(line, "!"); ok {
			negations = append(negations, negation{path: neg, line: i + 1})
			continue
		}
		// `dir/` excludes the directory itself; `dir/*` excludes its
		// children and leaves negations reachable.
		if strings.HasSuffix(line, "/") {
			excludedDirs[strings.TrimPrefix(line, "/")] = i + 1
		}
	}

	if len(negations) == 0 {
		t.Fatal("no `!` negations found in the scaffolded .gitignore — this guard would pass vacuously; re-check the parse")
	}

	for _, n := range negations {
		for dir, dirLine := range excludedDirs {
			if !strings.HasPrefix(strings.TrimPrefix(n.path, "/"), dir) {
				continue
			}
			t.Errorf("line %d `!%s` is unreachable: line %d excludes the directory `%s`, "+
				"and git never descends into an excluded directory, so the file is silently "+
				"ignored and never committed. Exclude the CHILDREN instead — `%s*`.",
				n.line, n.path, dirLine, dir, dir)
		}
	}
}
