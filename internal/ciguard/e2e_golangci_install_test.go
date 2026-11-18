package ciguard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// On 2026-11-09 golangci-lint's install.sh answered a 504 for its release
// download and every e2e-suite shard failed before running a test. The suite
// job now installs golangci-lint through ./.github/actions/install-release-tool
// (cached, retried, sha256-checked); this pins that, so a revert to install.sh
// turns this red rather than the next nightly.

var e2eSuiteWorkflow = filepath.Join("..", "..", ".github", "workflows", "e2e-suite.yml")

// suiteGolangciInstallProblems returns what is wrong with how the suite job
// installs golangci-lint: empty when it does so exactly once, through
// install-release-tool, and never through install.sh.
func suiteGolangciInstallProblems(raw []byte) []string {
	var wf stepsWorkflow
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		return []string{"e2e-suite.yml does not parse: " + err.Error()}
	}
	suite, ok := wf.Jobs["suite"]
	if !ok {
		return []string{"e2e-suite.yml has no suite job"}
	}
	var problems []string
	viaAction := 0
	for _, s := range suite.Steps {
		if strings.HasSuffix(s.Uses, "/install-release-tool") && s.With["tool"] == "golangci-lint" {
			viaAction++
		}
		if strings.Contains(s.Run, "golangci-lint") && strings.Contains(s.Run, "install.sh") {
			problems = append(problems, fmt.Sprintf("step %q installs golangci-lint through install.sh; use ./.github/actions/install-release-tool (tool: golangci-lint)", s.Name))
		}
	}
	if viaAction != 1 {
		problems = append(problems, fmt.Sprintf("the suite job installs golangci-lint through install-release-tool %d times, want exactly 1", viaAction))
	}
	return problems
}

func TestE2ESuiteInstallsGolangciThroughReleaseTool(t *testing.T) {
	raw, err := os.ReadFile(e2eSuiteWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range suiteGolangciInstallProblems(raw) {
		t.Error(p)
	}
}

// The check itself: the workflow as it was before (an install.sh step) is refused.
func TestSuiteGolangciInstallCheckRejectsInstallSh(t *testing.T) {
	old := `jobs:
  suite:
    steps:
      - name: Install golangci-lint
        run: |
          curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/v2.14.0/install.sh \
            | sh -s -- -b "$(go env GOPATH)/bin" v2.14.0
`
	if got := suiteGolangciInstallProblems([]byte(old)); len(got) != 2 {
		t.Fatalf("problems = %q, want the install.sh step and the missing action", got)
	}
	twice := `jobs:
  suite:
    steps:
      - uses: ./.github/actions/install-release-tool
        with: {tool: golangci-lint}
      - uses: ./.github/actions/install-release-tool
        with: {tool: golangci-lint}
`
	if got := suiteGolangciInstallProblems([]byte(twice)); len(got) != 1 {
		t.Fatalf("problems = %q, want the duplicate install", got)
	}
}
