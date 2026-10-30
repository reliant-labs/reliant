// Copyright (c) 2025 Reliant Labs

// Package requirements checks a workflow's declared `requires:` before its
// first node runs: the executables its commands call are on the machine, the
// integrations its actions authenticate to resolve for the run's owner, and
// the checkouts it reads exist and reach their origin. A run that cannot have
// them fails at once, naming every missing item and its fix, instead of an
// hour in with a half-finished side effect behind it.
//
// The machine's connectivity is not checked here: the run's daemon preflight
// (wake, wait, fail with the machine's own reason) has already run when this
// does, so a requirement check speaks only to what is ON the machine.
//
// Nothing a check does writes: the tool and checkout probe is one read-only
// shell command (command -v, test -e, git ls-remote, git for-each-ref), and a
// credential is resolved, never used.
package requirements

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// Machine runs one shell command on the run's machine, in the project root.
// err is a transport failure (the command may not have run), never a
// non-zero exit.
type Machine interface {
	Run(ctx context.Context, command string) (stdout string, exitCode int, err error)
}

// Credentials says whether an integration's connection resolves, authorized,
// for this run: nil when it does (or the integration needs none), else an
// error whose text the owner is shown.
type Credentials interface {
	Check(ctx context.Context, integration string) error
}

// UnmetErrType is the Temporal error type of a run whose requirements are not
// met: a verdict, never retried. The runtime and the activity both name it.
const UnmetErrType = "PreflightRequirementsUnmet"

// Problem is one requirement the run cannot have.
type Problem struct {
	// Kind is tool, integration or repo.
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Problem says what is wrong, Fix the command or step that fixes it.
	Problem string `json:"problem"`
	Fix     string `json:"fix"`
}

// Report is what a check found.
type Report struct {
	Problems []Problem `json:"problems,omitempty"`
}

// OK reports whether every requirement is met.
func (r Report) OK() bool { return len(r.Problems) == 0 }

// Error is the report as the run's failure: every problem, one per line, each
// with its fix.
func (r Report) Error() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "the run's requirements are not met (requires:), so nothing ran:")
	for _, p := range r.Problems {
		fmt.Fprintf(&sb, "\n- %s %s: %s. Fix: %s", p.Kind, p.Name, p.Problem, p.Fix)
	}
	return sb.String()
}

// Empty reports whether there is nothing to check.
func Empty(req *reliantv1.WorkflowRequirements) bool {
	return req == nil || (len(req.GetTools()) == 0 && len(req.GetIntegrations()) == 0 && len(req.GetRepos()) == 0)
}

// NeedsMachine reports whether checking req needs the run's machine.
func NeedsMachine(req *reliantv1.WorkflowRequirements) bool {
	return req != nil && (len(req.GetTools()) > 0 || len(req.GetRepos()) > 0)
}

// toolName and repoPath are the names a requirement may use: they are
// interpolated into the probe command, so the alphabet is closed.
var (
	toolName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
	repoPath = regexp.MustCompile(`^(\.|[A-Za-z0-9_][A-Za-z0-9._-]*(/[A-Za-z0-9_][A-Za-z0-9._-]*)*)$`)
)

// Validate returns what is wrong with a declaration itself, one message per
// entry; validation reports them when the definition is saved.
func Validate(req *reliantv1.WorkflowRequirements, integrationKnown func(string) bool) []string {
	var out []string
	for _, t := range req.GetTools() {
		if !toolName.MatchString(t) {
			out = append(out, fmt.Sprintf("requires.tools: %q is not an executable name (letters, digits, . _ + -; no paths or spaces)", t))
		}
	}
	for _, r := range req.GetRepos() {
		if !repoPath.MatchString(r) || strings.Contains(r, "..") {
			out = append(out, fmt.Sprintf("requires.repos: %q is not a path inside the project (\".\" for the root, else e.g. control-plane or services/api)", r))
		}
	}
	for _, i := range req.GetIntegrations() {
		if integrationKnown != nil && !integrationKnown(i) {
			out = append(out, fmt.Sprintf("requires.integrations: %q is not an integration", i))
		}
	}
	return out
}

// Check runs every check and returns what is missing. err is a check that
// could not run (the machine did not answer): not a verdict on the
// requirements, so the caller retries it or reports it as such.
func Check(ctx context.Context, req *reliantv1.WorkflowRequirements, machine Machine, creds Credentials) (Report, error) {
	var report Report
	if Empty(req) {
		return report, nil
	}
	if NeedsMachine(req) {
		if machine == nil {
			return report, fmt.Errorf("requires: tools and repos need the run's machine, and this run has none")
		}
		out, code, err := machine.Run(ctx, ProbeCommand(req))
		if err != nil {
			return report, fmt.Errorf("checking requires: on the machine: %w", err)
		}
		problems, perr := parseProbe(out)
		if perr != nil {
			return report, fmt.Errorf("checking requires: on the machine (exit %d): %w", code, perr)
		}
		report.Problems = append(report.Problems, problems...)
	}
	for _, integration := range req.GetIntegrations() {
		if creds == nil {
			report.Problems = append(report.Problems, Problem{Kind: "integration", Name: integration,
				Problem: "this run has no connections to check", Fix: "start the run from an activation whose owner has connected " + integration})
			continue
		}
		if err := creds.Check(ctx, integration); err != nil {
			report.Problems = append(report.Problems, Problem{Kind: "integration", Name: integration,
				Problem: strings.TrimRight(err.Error(), ". "), Fix: "connect (or reconnect) " + integration + " under Settings → Integrations for the activation's owner"})
		}
	}
	sort.SliceStable(report.Problems, func(i, j int) bool {
		if report.Problems[i].Kind != report.Problems[j].Kind {
			return report.Problems[i].Kind < report.Problems[j].Kind
		}
		return report.Problems[i].Name < report.Problems[j].Name
	})
	return report, nil
}

// probeDone is the probe's last line: a probe whose output lacks it did not
// finish, and its silence is not "nothing missing".
const probeDone = "REQUIRES_PROBE_DONE"

// ProbeCommand is the one read-only shell command that checks tools and
// repos. Each line it prints is `<VERDICT>\t<name>[\t<detail>]`.
//
// A repo is checked three ways, each the cause of a failure a run would
// otherwise hit midway: the directory is a git checkout; `git ls-remote`
// reaches origin (credentials, network, a renamed remote); and no local tag
// differs from origin's, which makes every `git fetch --tags` fail with
// "would clobber existing tag".
func ProbeCommand(req *reliantv1.WorkflowRequirements) string {
	var sb strings.Builder
	sb.WriteString("set -u\n")
	// macOS has no timeout(1); there ls-remote runs unbounded under the
	// activity's own deadline.
	sb.WriteString("T=''; command -v timeout >/dev/null 2>&1 && T='timeout 60'\n")
	for _, t := range req.GetTools() {
		fmt.Fprintf(&sb, "command -v '%s' >/dev/null 2>&1 || printf 'TOOL_MISSING\\t%%s\\n' '%s'\n", t, t)
	}
	for _, r := range req.GetRepos() {
		fmt.Fprintf(&sb, `d='%s'
if ! git -C "$d" rev-parse --git-dir >/dev/null 2>&1; then
  printf 'REPO_MISSING\t%%s\n' "$d"
else
  err="${TMPDIR:-/tmp}/requires-err.$$"
  remote="$($T git -C "$d" ls-remote --tags origin 2>"$err")"
  if [ $? -ne 0 ]; then
    printf 'REPO_UNREACHABLE\t%%s\t%%s\n' "$d" "$(tail -n 1 "$err" | tr '\t' ' ')"
  else
    printf '%%s\n' "$remote" | awk '$2 !~ /\^\{\}$/ {sub("refs/tags/", "", $2); print $2 "\t" $1}' | sort > "${TMPDIR:-/tmp}/requires-remote.$$"
    git -C "$d" for-each-ref --format='%%(refname:strip=2)	%%(objectname)' refs/tags | sort > "${TMPDIR:-/tmp}/requires-local.$$"
    join -t "$(printf '\t')" "${TMPDIR:-/tmp}/requires-local.$$" "${TMPDIR:-/tmp}/requires-remote.$$" | awk -F '\t' -v d="$d" '$2 != $3 {print "REPO_TAG_DIVERGED\t" d "\t" $1}' | head -n 20
    rm -f "${TMPDIR:-/tmp}/requires-remote.$$" "${TMPDIR:-/tmp}/requires-local.$$"
  fi
  rm -f "$err"
fi
`, r)
	}
	sb.WriteString("echo " + probeDone + "\n")
	return sb.String()
}

func parseProbe(out string) ([]Problem, error) {
	var problems []Problem
	done := false
	diverged := map[string][]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		switch f[0] {
		case probeDone:
			done = true
		case "TOOL_MISSING":
			if len(f) >= 2 {
				problems = append(problems, Problem{Kind: "tool", Name: f[1], Problem: "not found on the machine's PATH",
					Fix: "install " + f[1] + " on the machine (or in its image), or drop it from requires.tools"})
			}
		case "REPO_MISSING":
			if len(f) >= 2 {
				problems = append(problems, Problem{Kind: "repo", Name: f[1], Problem: "no git checkout at this path under the project",
					Fix: "clone it there (git clone <url> " + f[1] + "), or fix requires.repos"})
			}
		case "REPO_UNREACHABLE":
			if len(f) >= 2 {
				detail := ""
				if len(f) >= 3 {
					detail = ": " + strings.TrimSpace(f[2])
				}
				problems = append(problems, Problem{Kind: "repo", Name: f[1], Problem: "cannot reach origin" + detail,
					Fix: "git -C " + f[1] + " ls-remote origin, then fix what it says (credentials, network, the remote's URL)"})
			}
		case "REPO_TAG_DIVERGED":
			if len(f) >= 3 {
				diverged[f[1]] = append(diverged[f[1]], f[2])
			}
		}
	}
	if !done {
		return nil, fmt.Errorf("the probe did not finish: %s", lastLine(out))
	}
	for _, repo := range sortedKeys(diverged) {
		tags := diverged[repo]
		problems = append(problems, Problem{Kind: "repo", Name: repo,
			Problem: fmt.Sprintf("local tag(s) %s differ from origin's, so every git fetch --tags fails (would clobber existing tag)", strings.Join(tags, ", ")),
			Fix:     fmt.Sprintf("git -C %s fetch origin '+refs/tags/%s:refs/tags/%s' (once per tag)", repo, tags[0], tags[0])})
	}
	return problems, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) == 0 || lines[len(lines)-1] == "" {
		return "(no output)"
	}
	return lines[len(lines)-1]
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
