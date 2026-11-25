package ciguard

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// testWorkflows are the workflows whose jobs run forge's Go tests. nightly.yml
// runs e2e-suite.yml and e2e-scaffold.yml as reusable workflows, so it is
// covered through them.
var testWorkflows = []string{"ci.yml", "e2e-suite.yml", "e2e-scaffold.yml"}

// embeddedPostgresRestore is the action that restores pkg/pgtest's postgres
// archives from the Actions cache.
const embeddedPostgresRestore = "./.github/actions/embedded-postgres-cache"

// runsGoTests matches a shell line that runs Go tests: `go test` (not the
// compile-only `go test -c`), the shard scripts that wrap it, or the prebuilt
// e2e test binary.
var runsGoTests = regexp.MustCompile(`(^|[\s;(&|])go test\b|scripts/ci-(test-shard|test-full-shard|changed-tests|render-tests)\.sh|e2e\.test"?\s+-test\.(v|run)\b`)

// compileOnly matches `go test -c`, which builds a test binary and runs nothing.
var compileOnly = regexp.MustCompile(`\bgo test\b.*\s-c(\s|$)`)

// TestEveryTestJobRestoresEmbeddedPostgres proves every job that runs Go tests
// restores the embedded-postgres archives before its first test step. A test
// that boots pkg/pgtest on a cold cache fetches the archive from Maven
// Central, which has answered both 429s (a burst of parallel jobs) and 404s
// for the published 18.3.0 (its CDN), each turning main red for a reason
// unrelated to the change. The `build` job ran pgtest-backed integration tests
// with no restore until 2026-11-19, and fetched on every push to main until a
// 404 failed pkg/alerting and pkg/alerting/pgstore.
func TestEveryTestJobRestoresEmbeddedPostgres(t *testing.T) {
	type step struct {
		Name string `yaml:"name"`
		Run  string `yaml:"run"`
		Uses string `yaml:"uses"`
	}
	checked := 0
	for _, file := range testWorkflows {
		path := filepath.Join("..", "..", ".github", "workflows", file)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var wf struct {
			Jobs map[string]struct {
				Steps []step `yaml:"steps"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(raw, &wf); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		names := make([]string, 0, len(wf.Jobs))
		for name := range wf.Jobs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			restoredAt, testAt := -1, -1
			for i, s := range wf.Jobs[name].Steps {
				if s.Uses == embeddedPostgresRestore && restoredAt < 0 {
					restoredAt = i
				}
				if testAt < 0 && stepRunsGoTests(s.Run) {
					testAt = i
				}
			}
			if testAt < 0 {
				continue
			}
			checked++
			switch {
			case restoredAt < 0:
				t.Errorf("%s job %s runs Go tests (step %q) but never restores the embedded postgres archives: add `- uses: %s` before it, and embedded-postgres-cache-save after it on main",
					file, name, wf.Jobs[name].Steps[testAt].Name, embeddedPostgresRestore)
			case restoredAt > testAt:
				t.Errorf("%s job %s restores the embedded postgres archives (step %d) only after its first test step %q (step %d)",
					file, name, restoredAt, wf.Jobs[name].Steps[testAt].Name, testAt)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no job in the test workflows runs Go tests — this guard's matcher is stale and inspects nothing")
	}
}

// stepRunsGoTests reports whether a step's shell script runs Go tests,
// ignoring comments and echoed text.
func stepRunsGoTests(script string) bool {
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "echo ") {
			continue
		}
		if runsGoTests.MatchString(line) && !compileOnly.MatchString(line) {
			return true
		}
	}
	return false
}

// TestStepRunsGoTests pins the matcher on the shapes the workflows use.
func TestStepRunsGoTests(t *testing.T) {
	for script, want := range map[string]bool{
		"go test -short ./...":                                        true,
		"if go test ./...; then":                                      true,
		"scripts/ci-test-shard.sh 0 2":                                true,
		"scripts/ci-test-full-shard.sh unit 0 2":                      true,
		"scripts/ci-changed-tests.sh HEAD^1":                          true,
		"scripts/ci-render-tests.sh HEAD^1":                           true,
		`"$RUNNER_TEMP/e2e-dist/e2e.test" -test.v=test2json \`:        true,
		`go test -c -tags e2e -o "$dist/e2e.test" ./internal/cli/`:    false,
		`(cd internal/cli && "$dist/e2e.test" -test.list '^TestE2E')`: false,
		"# package directory as `go test` runs it.":                   false,
		`echo "✅ go test"`:                                            false,
		"scripts/ci-test-packages.sh check":                           false,
		"go build ./...":                                              false,
	} {
		if got := stepRunsGoTests(script); got != want {
			t.Errorf("stepRunsGoTests(%q) = %v, want %v", script, got, want)
		}
	}
}
