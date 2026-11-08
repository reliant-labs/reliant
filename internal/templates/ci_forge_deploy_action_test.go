package templates

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The vendored action's shell, EXECUTED — not just matched as text. Each
// composite step's `run:` runs under bash with a stub `forge` on PATH that
// records its argv and answers with a scripted document and exit code.
//
// What this pins is the plumbing no verb's own test can see, because it lives
// in the YAML rather than in Go: the args the step assembles from its inputs,
// the promotion id it hands the next stage via $GITHUB_OUTPUT, and — the one
// that has broken twice — that `forge env deploy`'s exit code becomes the
// step's exit code INSTEAD of being swallowed by the `> deploy.json`
// redirect, while still leaving a document for the gate step to read.

type actionStep struct {
	ID  string            `yaml:"id"`
	Env map[string]string `yaml:"env"`
	Run string            `yaml:"run"`
}

func forgeDeploySteps(t *testing.T) map[string]actionStep {
	t.Helper()
	out, err := CITemplates("github").Render("forge-deploy-action.yml.tmpl", releaseFixture())
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Runs struct {
			Steps []actionStep `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	steps := map[string]actionStep{}
	for _, s := range doc.Runs.Steps {
		if s.ID != "" {
			steps[s.ID] = s
		}
	}
	return steps
}

// runActionStep executes one step with the given inputs substituted for its
// ${{ inputs.* }} / ${{ steps.* }} env values, against a stub forge that
// prints stdout and exits code. It returns the step's exit code, the stub's
// recorded argv lines, and what the step wrote to $GITHUB_OUTPUT.
func runActionStep(t *testing.T, step actionStep, env map[string]string, stdout string, code int) (int, []string, string) {
	t.Helper()
	return runActionStepVerbs(t, step, env, map[string]stubReply{"": {stdout, code}})
}

// stubReply is what the stub forge prints and exits with for one verb.
type stubReply struct {
	stdout string
	code   int
}

// runActionStepVerbs is runActionStep with a reply per forge VERB (the second
// argv word: deploy, gate, ci); "" is the fallback.
func runActionStepVerbs(t *testing.T, step actionStep, env map[string]string, replies map[string]stubReply) (int, []string, string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	argvLog := filepath.Join(dir, "argv.log")
	// Paths enter the stub single-quoted and slash-separated: bash reads a
	// backslash as an escape, so a native Windows path (C:\Users\...) named a
	// different, relative file and the stub recorded and replayed nothing.
	shPath := func(p string) string { return "'" + filepath.ToSlash(p) + "'" }
	var stub strings.Builder
	stub.WriteString("#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> " + shPath(argvLog) + "\ncase \"$2\" in\n")
	for verb, r := range replies {
		payload := filepath.Join(dir, "payload-"+verb)
		if err := os.WriteFile(payload, []byte(r.stdout), 0o644); err != nil {
			t.Fatal(err)
		}
		pattern := verb
		if verb == "" {
			pattern = "*"
		}
		stub.WriteString("  " + pattern + ") cat " + shPath(payload) + "; exit " + strconv.Itoa(r.code) + " ;;\n")
	}
	stub.WriteString("esac\nexit 0\n")
	if err := os.WriteFile(filepath.Join(bin, "forge"), []byte(stub.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(dir, "github_output")
	cmd := exec.Command("bash", "-c", step.Run)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "GITHUB_OUTPUT="+outFile)
	// The runner sets EVERY key in the step's env: block — an omitted input
	// is "", never unset — so the harness does too. Under `set -u` the
	// difference is an "unbound variable" abort the real runner never has.
	for k := range step.Env {
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	out, err := cmd.CombinedOutput()
	exit := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run step %s: %v\n%s", step.ID, err, out)
		}
		exit = ee.ExitCode()
	}
	argv, _ := os.ReadFile(argvLog)
	ghOut, _ := os.ReadFile(outFile)
	return exit, strings.Split(strings.TrimSpace(string(argv)), "\n"), string(ghOut)
}

// The FIRST stage: deploy by version, with gates, and hand the recorded
// promotion id to the gate step and to the next stage.
func TestForgeDeployAction_DeployByVersion(t *testing.T) {
	t.Parallel()
	step := forgeDeploySteps(t)["deploy"]
	code, argv, ghOut := runActionStep(t, step, map[string]string{
		"ENV": "staging", "VERSION": "v1.4.0", "TIMEOUT": "15m",
		"GATES": "gates/lint.json gates/test.json",
	}, `{"ok":true,"exit_code":0,"applied":true,"recorded":{"id":"prom_1"}}`, 0)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	want := "env deploy staging v1.4.0 --json --yes --timeout 15m --gate gates/lint.json --gate gates/test.json"
	if len(argv) != 1 || argv[0] != want {
		t.Errorf("forge argv = %q, want %q", argv, want)
	}
	if !strings.Contains(ghOut, "promotion-id=prom_1") {
		t.Errorf("GITHUB_OUTPUT = %q", ghOut)
	}
}

// A LATER stage: no version, --from / --from-promotion / --expect-current.
// A refused deploy (exit 3, the compare-and-set) is the step's exit — and the
// document carries no recorded id, so the gate step below is skipped rather
// than attaching evidence to a promotion that was never written.
func TestForgeDeployAction_DeployFromIsRefusedWithExit3(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the deploy action's bash steps as subprocesses; runs in task test")
	}
	t.Parallel()
	step := forgeDeploySteps(t)["deploy"]
	code, argv, ghOut := runActionStep(t, step, map[string]string{
		"ENV": "prod", "FROM": "staging", "FROM_PROMOTION": "prom_1",
		"EXPECT_CURRENT": "unbound", "TIMEOUT": "15m",
	}, `{"ok":false,"exit_code":3,"error":"promotion_conflict"}`, 3)
	if code != 3 {
		t.Fatalf("a refused deploy must fail the step with ITS code (3), got %d", code)
	}
	want := "env deploy prod --json --yes --timeout 15m --from staging --from-promotion prom_1 --expect-current unbound"
	if len(argv) != 1 || argv[0] != want {
		t.Errorf("forge argv = %q, want %q", argv, want)
	}
	if !strings.Contains(ghOut, "promotion-id=\n") && !strings.HasSuffix(strings.TrimRight(ghOut, "\n"), "exit-code=3") {
		t.Errorf("GITHUB_OUTPUT = %q", ghOut)
	}
	if strings.Contains(ghOut, "promotion-id=prom") {
		t.Errorf("a refused deploy records no promotion id; GITHUB_OUTPUT = %q", ghOut)
	}
}

// THE CASE THIS TEST EXISTS FOR. A deploy whose health gate goes red DID
// record and apply a promotion — `recorded.id` is there — and that is exactly
// when the rollout evidence must be attached to it. So every non-zero wait
// outcome must still (a) fail the step with its own code and (b) publish the
// promotion id, or `gate record`'s `if:` guard skips and the trail is lost for
// the one release that needed it.
//
// The exit codes are the ADR's, and `gate record --from` maps them to statuses:
// 1 failed; 2/3/4/5/8 error; 6 skipped. Nothing here folds 8 or 6 into a failure.
func TestForgeDeployAction_FailedWaitStillPublishesThePromotionID(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the deploy action's bash steps as subprocesses; runs in task test")
	}
	t.Parallel()
	step := forgeDeploySteps(t)["deploy"]
	for _, tc := range []struct {
		name string
		code int
	}{
		{"degraded", 1},
		{"undetermined", 2},
		{"timed out while still progressing", 8},
		{"superseded by a newer promotion", 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := fmt.Sprintf(`{"ok":false,"exit_code":%d,"applied":true,"recorded":{"id":"prom_9"}}`, tc.code)
			code, _, ghOut := runActionStep(t, step, map[string]string{
				"ENV": "prod", "VERSION": "v2", "TIMEOUT": "15m",
			}, doc, tc.code)
			if code != tc.code {
				t.Errorf("step exit = %d, want the deploy's %d", code, tc.code)
			}
			if !strings.Contains(ghOut, "promotion-id=prom_9") {
				t.Errorf("a recorded promotion whose wait failed must still be published "+
					"(the gate step's if: reads it); GITHUB_OUTPUT = %q", ghOut)
			}
			if !strings.Contains(ghOut, fmt.Sprintf("exit-code=%d", tc.code)) {
				t.Errorf("the phase output must carry the deploy's exit code; GITHUB_OUTPUT = %q", ghOut)
			}
		})
	}
}

// The gate step reads the SAME document the deploy wrote, names it `rollout`,
// and pins it to the promotion id rather than to the env's current one.
func TestForgeDeployAction_GateRecordsTheDeployDocument(t *testing.T) {
	t.Parallel()
	var gate actionStep
	for _, s := range forgeDeployAllSteps(t) {
		if strings.Contains(s.Run, "forge gate record") {
			gate = s
		}
	}
	if gate.Run == "" {
		t.Fatal("no gate-record step in the action")
	}
	code, argv, _ := runActionStep(t, gate, map[string]string{
		"ENV": "prod", "PROMOTION_ID": "prom_9",
	}, "", 0)
	if code != 0 {
		t.Fatalf("recording a gate exits 0 even for a failed document, got %d", code)
	}
	want := "gate record prod --promotion prom_9 --from deploy.json --name rollout"
	if len(argv) != 1 || argv[0] != want {
		t.Errorf("forge argv = %q, want %q", argv, want)
	}
}

// forgeDeployAllSteps is every step, including the ones with no id (the gate
// and summary steps are named, not id'd).
func forgeDeployAllSteps(t *testing.T) []actionStep {
	t.Helper()
	out, err := CITemplates("github").Render("forge-deploy-action.yml.tmpl", releaseFixture())
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Runs struct {
			Steps []actionStep `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Runs.Steps
}
