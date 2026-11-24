package ciguard

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var renderTestsScript = filepath.Join("..", "..", "scripts", "ci-render-tests.sh")

// renderTestsStep is the merge tier's run of the full-mode KCL render tests.
const renderTestsStep = "scripts/ci-render-tests.sh HEAD^1"

// bashArray returns the entries of the bash array `name=( ... )` in script,
// one per line, comments and blank lines dropped.
func bashArray(t *testing.T, script, name string) []string {
	t.Helper()
	m := regexp.MustCompile(`(?ms)^` + regexp.QuoteMeta(name) + `=\(\n(.*?)^\)`).FindStringSubmatch(script)
	if m == nil {
		t.Fatalf("%s declares no %s=( ... ) array", renderTestsScript, name)
	}
	var out []string
	for _, line := range strings.Split(m[1], "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, strings.Trim(line, `'"`))
	}
	if len(out) == 0 {
		t.Fatalf("%s's %s array is empty: the step would check nothing", renderTestsScript, name)
	}
	return out
}

// TestRenderTestsRunInTheMergeTier holds the merge tier's full-mode KCL
// render step (forge main went red three times on 2026-11-21/22 from a
// config-roots.k.tmpl change that only full-mode render tests could see):
// test-short, the merge tier's job, runs scripts/ci-render-tests.sh against
// HEAD^1 after its -short tier, and the script runs no -short and no -race.
func TestRenderTestsRunInTheMergeTier(t *testing.T) {
	wf := readWorkflow(t)
	if got := strings.TrimSpace(wf.Jobs["test-short"].If); got != queueTier {
		t.Fatalf("test-short runs if %q, want the merge tier's %q", got, queueTier)
	}
	steps := jobStepRuns(t, "test-short")
	at, shortAt := slices.Index(steps, renderTestsStep), slices.Index(steps, "scripts/ci-test-shard.sh ${{ matrix.args }}")
	switch {
	case at < 0:
		t.Errorf("test-short does not run %q: the merge tier would never run the full-mode KCL render tests", renderTestsStep)
	case shortAt < 0 || at < shortAt:
		t.Errorf("test-short runs %q before its -short tier: the render tests reuse that tier's compile", renderTestsStep)
	}

	script := readText(t, renderTestsScript)
	for _, line := range strings.Split(script, "\n") {
		code := strings.TrimSpace(line)
		if strings.HasPrefix(code, "#") || !strings.Contains(code, "ci-go-test.sh") {
			continue
		}
		for _, banned := range []string{"-short", "-race"} {
			if strings.Contains(code, banned) {
				t.Errorf("%s runs %s (%q): the render tests are full mode, and -race is main's", renderTestsScript, banned, code)
			}
		}
	}
}

// TestRenderTestsNameDeclaredTests proves every test the script names is a
// top-level test its package still declares, and every trigger names a path
// that exists. Without it a rename turns the step into a pass that ran
// nothing (the script also refuses that at run time, but only when a trigger
// fires). Cache-sound: the files are read by this process.
func TestRenderTestsNameDeclaredTests(t *testing.T) {
	script := readText(t, renderTestsScript)
	declared := map[string]string{} // package dir -> its _test.go sources
	for _, entry := range bashArray(t, script, "tests") {
		dot := strings.LastIndex(entry, ".")
		if dot < 0 {
			t.Errorf("tests entry %q is not <package dir>.<TestName>", entry)
			continue
		}
		pkg, name := entry[:dot], entry[dot+1:]
		src, ok := declared[pkg]
		if !ok {
			files, err := filepath.Glob(filepath.Join("..", "..", pkg, "*_test.go"))
			if err != nil || len(files) == 0 {
				t.Errorf("tests entry %q: %s has no test files", entry, pkg)
				continue
			}
			var b strings.Builder
			for _, f := range files {
				b.WriteString(readText(t, f))
			}
			src = b.String()
			declared[pkg] = src
		}
		if !strings.Contains(src, "\nfunc "+name+"(t *testing.T) {") {
			t.Errorf("%s names %s, which %s no longer declares: update its tests list", renderTestsScript, name, pkg)
		}
	}
	for _, spec := range bashArray(t, script, "triggers") {
		path := strings.TrimPrefix(spec, ":(glob)")
		// A glob's literal directory must exist; a plain path must exist.
		if i := strings.IndexAny(path, "*?["); i >= 0 {
			path = filepath.Dir(path[:i] + "x")
		}
		if _, err := os.Stat(filepath.Join("..", "..", path)); err != nil {
			t.Errorf("%s's trigger %q names %s, which does not exist: a trigger that matches nothing never fires", renderTestsScript, spec, path)
		}
	}
}
