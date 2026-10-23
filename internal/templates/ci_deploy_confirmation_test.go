package templates

import (
	"regexp"
	"strings"
	"testing"
)

// EVERY SCAFFOLDED `forge env deploy` THAT WRITES CARRIES A CONFIRMATION FLAG.
//
// This is the structural guard for F-18. O-13 put a confirmation gate in front
// of the promotion write: with no TTY and no --yes, `forge env deploy` prints
// the plan and exits 5 (plan_unconfirmed) having written nothing. A GitHub
// runner has no TTY, so every scaffolded workflow that deploys is exactly that
// case — which is how forge shipped a release whose CI deploys were red by
// construction.
//
// Asserted over the RENDERED workflows rather than over the template source,
// because a `{{if}}` branch can emit a deploy line no `rg` of the .tmpl would
// read as one, and the rendered text is the only thing a user actually gets.
//
// WHY A TEST RATHER THAN A removalguard ENTRY. removalguard matches text that
// must not EXIST anywhere, which is the wrong shape here: `forge env deploy`
// must keep existing, and what is forbidden is one spelling of it in one
// place. A pattern broad enough to catch a flagless deploy in a workflow would
// also fire on every prose mention in the skills and on the deliberately
// flagless interactive examples in the help text. The brief says pick one —
// this is the one, and it is the only one.
func TestCIWorkflows_EveryDeployIsConfirmed(t *testing.T) {
	t.Parallel()
	for name, doc := range renderedWorkflows(t) {
		for _, line := range deployInvocations(string(doc)) {
			if !confirmedDeploy(line) {
				t.Errorf("%s: this `forge env deploy` writes a promotion with no confirmation flag:\n"+
					"    %s\n"+
					"  CI has no TTY, so the gate refuses it: forge prints the plan and exits 5\n"+
					"  (plan_unconfirmed) having written nothing. Add --yes (\"I read the plan\",\n"+
					"  and the plan is in the job log), or make the step a read-only preview with\n"+
					"  --dry-run / --plan-only / --explain.", name, strings.TrimSpace(line))
			}
		}
	}
}

// deployDirective matches a `forge env deploy` that is EXECUTED, as opposed to
// one that is merely mentioned.
//
// The distinction carries the whole test. A rendered workflow names the verb
// constantly in comments and in the composite action's own `name:` and
// `description:`, and treating those as invocations would make the check fire
// on prose nobody can add a flag to. So the match is anchored to a position
// where a shell command can begin: the start of a block-scalar line, just
// after `run:`, or after a shell separator (`&&`, `||`, `;`, `|`).
//
// The action's spelling is matched too — it builds `args=(env deploy "$ENV")`
// and appends flags on later lines — because that is the one invocation every
// release stage goes through, and `forge` never appears on the same line.
var deployDirective = regexp.MustCompile(`(?:^|run:\s*|&&\s*|\|\|\s*|;\s*|\|\s*)forge\s+env\s+deploy\b|^args=\(env\s+deploy\b`)

// deployInvocations returns one entry per EXECUTED `forge env deploy` in doc,
// each carrying the lines a flag for that invocation could legitimately live
// on: the command itself, plus any continuation of it.
//
// For a plain shell command the continuation is a trailing backslash. For the
// composite action's `args=(...)` form it runs until the array is spent — the
// `forge "${args[@]}"` that executes it — because the flags are appended over
// several lines, interleaved with guards and comments. Collecting past that
// execution line would let a flag on a LATER forge command satisfy the check,
// so the scan stops there.
func deployInvocations(doc string) []string {
	lines := strings.Split(doc, "\n")
	var found []string
	for i, line := range lines {
		// A comment cannot be executed, and the surrounding comments
		// are exactly where the verb is discussed in prose.
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !deployDirective.MatchString(trimmed) {
			continue
		}
		buildsArgv := strings.HasPrefix(trimmed, "args=(")
		cmd := trimmed
		for j := i + 1; j < len(lines); j++ {
			next := strings.TrimSpace(lines[j])
			if buildsArgv {
				// Comments are SKIPPED, not collected. The lines
				// explaining why --yes is there mention it by
				// name, and collecting them would let the
				// explanation satisfy the check that the flag
				// is actually passed.
				if !strings.HasPrefix(next, "#") {
					cmd += "\n" + next
				}
				if strings.Contains(next, `"${args[@]}"`) {
					break
				}
				continue
			}
			if !strings.HasSuffix(strings.TrimSpace(cmd), "\\") {
				break
			}
			cmd += "\n" + next
		}
		found = append(found, cmd)
	}
	return found
}

// confirmationFlags are the spellings that make a deploy legitimate in CI.
//
// --yes and --approve let the write happen: the first says "I read the plan"
// and the second names the plan digest it approves (two-stage). The rest write
// NOTHING by construction, so the gate never applies to them — a preview step
// is allowed to be flagless because there is nothing to confirm.
var confirmationFlags = []string{"--yes", "--approve", "--dry-run", "--plan-only", "--explain", "--plan"}

func confirmedDeploy(cmd string) bool {
	for _, flag := range confirmationFlags {
		if strings.Contains(cmd, flag) {
			return true
		}
	}
	return false
}

// THE MUTATION CHECK, as a test rather than as a manual step.
//
// A guard that cannot fail is indistinguishable from a guard that passes
// vacuously, and this one is a text scan over rendered YAML — exactly the kind
// that silently stops matching when a template's shape changes (a deploy moved
// into a block scalar, the action's args assembled differently). So the
// detector is run against a known-bad document here: strip the flag from a
// real rendered workflow and the scan must report it.
//
// This is what makes "remove one --yes and the test goes red" a property of
// the suite instead of something a reviewer has to take on trust.
func TestCIWorkflows_ConfirmationGuardCatchesAFlaglessDeploy(t *testing.T) {
	t.Parallel()
	// One name per template that actually deploys, under the data shape
	// whose branch emits it: only the k3d e2e runtime deploys to a
	// cluster, and only the `ci k3d` shape renders ci.yml's e2e job that
	// way. Naming the wrong shape would assert over a document with no
	// deploy in it, which is the vacuous pass this test exists to catch.
	for _, name := range []string{"deploy", "e2e no frontend", "ci k3d", "forge-deploy action"} {
		doc, ok := renderedWorkflows(t)[name]
		if !ok {
			t.Fatalf("%s is not in the rendered set; the guard would skip it entirely", name)
		}
		mutated := strings.NewReplacer(" --yes", "", "--yes ", "").Replace(string(doc))
		if mutated == string(doc) {
			t.Fatalf("%s carries no --yes to remove, so the guard above is vacuous for it", name)
		}
		var flagless int
		for _, line := range deployInvocations(mutated) {
			if !confirmedDeploy(line) {
				flagless++
			}
		}
		if flagless == 0 {
			t.Errorf("%s: removing every --yes left no invocation the guard rejects — "+
				"the detector no longer sees this workflow's deploy, so "+
				"TestCIWorkflows_EveryDeployIsConfirmed passes vacuously for it", name)
		}
	}
}
