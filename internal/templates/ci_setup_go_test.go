package templates

import (
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A scaffolded job that runs the `go` command must set up Go FIRST, from
// go.mod, with actions/setup-go's go-version-file.
//
// THE BUG THIS CATCHES: build-images.yml's trivy-scan job ran the forge
// install script — whose last line is `go install …` — with no setup-go step
// before it, so it used whatever Go the runner happened to preinstall. On
// houndersclub that was older than go.mod's `go 1.27.0`, and the step died
// with go's own toolchain refusal before forge was ever installed. Every
// neighbouring job in ci.yml did have setup-go, which is exactly why one
// missing copy survived review: the job LOOKS like the others.
//
// The rule is per-JOB because a GitHub job is a fresh runner — a setup-go in
// `lint` does nothing for `trivy-scan` — and it is ORDER-sensitive because a
// setup-go after the `go` call configures a toolchain the call already failed
// without.
//
// go-version-file: go.mod rather than a literal version, for the same reason
// installForgeScript reads the forge version from the project: a version
// stamped into a scaffold-once workflow freezes while go.mod moves on.

// goInvocation matches a `go` command in a shell script — `go install`,
// `go build`, `go test`, `GOWORK=off go mod edit`.
//
// Anchored to a command POSITION (start of line, or after a pipe, `&&`, `;`,
// or an inline env assignment) so it does not fire on the word "go" in prose,
// on `go-licenses check` or `gopls`, or on a path segment like `cmd/forge@`.
var goInvocation = regexp.MustCompile(`(?m)(?:^|[|;&]|\bthen\b|\bdo\b|^\s*)\s*(?:[A-Z_][A-Z0-9_]*=\S*\s+)*go\s+(?:install|build|test|run|mod|list|vet|generate|env|tool)\b`)

// setupGoUsesGoModVersion is actions/setup-go configured from go.mod.
var setupGoAction = regexp.MustCompile(`^actions/setup-go@`)

// ciJob is one job's steps, in declaration order, with the fields the audit
// reads.
type ciStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
}

type ciJob struct {
	Steps []ciStep `yaml:"steps"`
}

func parseJobs(t *testing.T, name string, body []byte) map[string]ciJob {
	t.Helper()
	var parsed struct {
		Jobs map[string]ciJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("%s is not valid YAML: %v", name, err)
	}
	return parsed.Jobs
}

// TestScaffoldedJobsThatRunGoSetItUpFirst audits EVERY rendered workflow: in
// every job, the first step that runs `go` must be preceded by an
// actions/setup-go step reading go-version-file.
func TestScaffoldedJobsThatRunGoSetItUpFirst(t *testing.T) {
	for workflow, body := range renderedWorkflows(t) {
		for jobName, job := range parseJobs(t, workflow, body) {
			setupAt := -1
			for i, step := range job.Steps {
				if setupGoAction.MatchString(step.Uses) {
					if setupAt < 0 {
						setupAt = i
					}
					if step.With["go-version-file"] != "go.mod" {
						t.Errorf("%s job %q step %d sets up Go with go-version-file=%q, want go.mod — a version stamped into a scaffold-once workflow freezes while go.mod moves on",
							workflow, jobName, i, step.With["go-version-file"])
					}
					continue
				}
				if step.Run == "" {
					continue
				}
				m := goInvocation.FindString(step.Run)
				if m == "" {
					continue
				}
				if setupAt < 0 {
					t.Errorf("%s job %q step %d (%q) runs the go command (%q) with no actions/setup-go before it in this job.\n"+
						"    A job is a fresh runner, so it gets the runner's preinstalled Go — older than go.mod's `go` directive, which fails the step.\n"+
						"    Add, before this step:\n"+
						"      - uses: actions/setup-go@v5\n"+
						"        with:\n"+
						"          go-version-file: go.mod",
						workflow, jobName, i, step.Name, strings.TrimSpace(m))
				}
			}
		}
	}
}

// TestInstallForgeScriptRunsGo pins the premise the audit above rests on: the
// forge install script IS a `go` invocation, so a job that installs forge
// needs setup-go even though no step in it names `go` directly. If the script
// stopped shelling out to go (a downloaded binary, say), the audit would go
// quiet on install-forge jobs and this test is what would say so.
func TestInstallForgeScriptRunsGo(t *testing.T) {
	if m := goInvocation.FindString(installForgeScript); m == "" {
		t.Fatalf("installForgeScript no longer runs the go command, so TestScaffoldedJobsThatRunGoSetItUpFirst no longer covers install-forge jobs:\n%s", installForgeScript)
	}
}
