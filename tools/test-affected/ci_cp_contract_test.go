package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// The vendored control-plane contract before merge (control-plane #2556):
// `CI result`, the merge queue's one required check, runs
// `task contract:controlplane:check` whenever the group changes
// proto-vendor/controlplane/, against a control-plane checkout at the commit
// SOURCE names. It used to run only on the push to main, so a vendored,
// unmerged control-plane commit merged and turned main red. These tests pin
// the wiring and the detection, so removing either fails here.

const cpContractGated = "${{ !cancelled() && steps.cp_contract.outputs.changed == 'true' }}"

type mqStep = struct {
	ciStep `yaml:",inline"`
	Env    map[string]string `yaml:"env"`
}

// cpContractSteps finds the detection step, the control-plane checkout and
// the contract check in `CI result`, by what they do rather than their names.
func cpContractSteps(t *testing.T) (steps []mqStep, detect, checkout, check, golangciWait int) {
	t.Helper()
	wf := loadMergeQueue(t)
	gate := wf.Jobs["ci-result"]
	if gate == nil || gate.Name != "CI result" {
		t.Fatalf("merge-queue.yml needs job ci-result named \"CI result\" (the ruleset's required check), has %v", sortedKeys(wf.Jobs))
	}
	detect, checkout, check, golangciWait = -1, -1, -1, -1
	for i, s := range gate.Steps {
		switch {
		case s.ID == "cp_contract":
			detect = i
		case strings.HasPrefix(s.Uses, "actions/checkout@") && s.With["repository"] == "reliant-labs/control-plane":
			checkout = i
		case strings.Contains(s.Run, "task contract:controlplane:check"):
			check = i
		case s.Name == golangciJobName+" passed":
			golangciWait = i
		}
	}
	if detect < 0 || checkout < 0 || check < 0 {
		t.Fatalf("CI result needs a cp_contract detection step (%d), a control-plane checkout (%d) and `task contract:controlplane:check` (%d)", detect, checkout, check)
	}
	return gate.Steps, detect, checkout, check, golangciWait
}

// TestMergeQueueChecksTheVendoredContract: in `CI result`, detection runs on
// every group, then the control-plane checkout at SOURCE and the contract
// check run under one condition (the detection said the contract changed),
// and the check is fatal: a failing check fails the required check.
func TestMergeQueueChecksTheVendoredContract(t *testing.T) {
	steps, detect, checkout, check, wait := cpContractSteps(t)
	if !(detect < checkout && checkout < check) {
		t.Errorf("order detect=%d checkout=%d check=%d: the check must read the checkout, which needs detection's sha", detect, checkout, check)
	}
	if wait >= 0 && check > wait {
		t.Errorf("the contract check (step %d) runs after the golangci wait (step %d), which is skipped on a failed gate", check, wait)
	}

	d := steps[detect]
	if d.If != "${{ !cancelled() }}" {
		t.Errorf("detection if=%q: it must run on every group that is not cancelled", d.If)
	}
	if !strings.Contains(d.Run, `scripts/ci-lanes.sh --base "${BASE}"`) {
		t.Errorf("detection does not ask scripts/ci-lanes.sh about the group's base:\n%s", d.Run)
	}

	co := steps[checkout]
	if co.If != cpContractGated {
		t.Errorf("checkout if=%q, want %q", co.If, cpContractGated)
	}
	if co.With["ref"] != "${{ steps.cp_contract.outputs.sha }}" {
		t.Errorf("checkout ref=%v: it must be the commit SOURCE names, from detection", co.With["ref"])
	}
	if co.With["persist-credentials"] != false {
		t.Errorf("checkout persist-credentials=%v: the deploy key must not stay in the checkout", co.With["persist-credentials"])
	}
	path, _ := co.With["path"].(string)
	if path == "" {
		t.Fatal("the control-plane checkout names no path, so the check cannot be pointed at it")
	}

	c := steps[check]
	if c.If != cpContractGated {
		t.Errorf("check if=%q, want %q (the same condition as the checkout)", c.If, cpContractGated)
	}
	if c.ContinueOnError != nil {
		t.Errorf("check continue-on-error=%v: a failing contract would pass the required check", c.ContinueOnError)
	}
	if want := "${{ github.workspace }}/" + path; c.Env["CONTROL_PLANE_DIR"] != want {
		t.Errorf("check CONTROL_PLANE_DIR=%q, want the checkout %q", c.Env["CONTROL_PLANE_DIR"], want)
	}
	for _, s := range steps[detect : check+1] {
		if s.ContinueOnError != nil {
			t.Errorf("step %q is continue-on-error: the contract gate must be fatal", s.Name+s.Uses)
		}
	}

	// The task must refuse rather than pass when it checked nothing.
	task := string(readRepo(t, "Taskfile.yml"))
	if !strings.Contains(task, "node .github/scripts/sync-controlplane-proto.mjs --check --require") {
		t.Error("task contract:controlplane:check no longer runs the sync script with --check --require")
	}
}

// TestMergeQueueContractDetection runs the detection step's own script in a
// scratch repository: an unrelated change reports changed=false and leaves the
// check out; a change to the vendored tree reports changed=true with SOURCE's
// commit; and a vendored change whose SOURCE names no commit fails the step,
// so the required check fails.
func TestMergeQueueContractDetection(t *testing.T) {
	testkit.Slow(t, "runs the detection step in bash over scripts/ci-lanes.sh and git subprocesses")
	requireShellTools(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	steps, detect, _, _, _ := cpContractSteps(t)
	script := steps[detect].Run
	lanes := readRepo(t, "scripts/ci-lanes.sh")
	const sha = "84617f5824df3dad673fd1a3aeb5a5a32a4f80c5"

	run := func(t *testing.T, change map[string]string) (bool, map[string]string, string) {
		t.Helper()
		dir := t.TempDir()
		git := func(args ...string) string {
			t.Helper()
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
			return strings.TrimSpace(string(out))
		}
		write := func(rel, body string) {
			t.Helper()
			p := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		git("init", "-q", "-b", "main")
		write("scripts/ci-lanes.sh", string(lanes))
		write("proto-vendor/controlplane/SOURCE", "control-plane "+sha+"\n")
		write("proto-vendor/controlplane/a.proto", "syntax = \"proto3\";\n")
		write("README.md", "x\n")
		git("add", "-A")
		git("commit", "-qm", "base")
		base := git("rev-parse", "HEAD")
		for rel, body := range change {
			write(rel, body)
		}
		git("add", "-A")
		git("commit", "-qm", "change")

		outFile := filepath.Join(dir, "github_output")
		ok, out := ciShellIn(t, dir, script, map[string]string{"BASE": base, "GITHUB_OUTPUT": outFile})
		got := map[string]string{}
		if b, err := os.ReadFile(outFile); err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
				if k, v, found := strings.Cut(line, "="); found {
					got[k] = v
				}
			}
		}
		return ok, got, out
	}

	t.Run("an unrelated change spends nothing", func(t *testing.T) {
		ok, got, out := run(t, map[string]string{"README.md": "y\n"})
		if !ok || got["changed"] != "false" || got["sha"] != "" {
			t.Errorf("ok=%v outputs=%v, want changed=false and no sha:\n%s", ok, got, out)
		}
	})
	t.Run("a vendored change runs the check at SOURCE", func(t *testing.T) {
		ok, got, out := run(t, map[string]string{"proto-vendor/controlplane/a.proto": "syntax = \"proto3\";\n// changed\n"})
		if !ok || got["changed"] != "true" || got["sha"] != sha {
			t.Errorf("ok=%v outputs=%v, want changed=true sha=%s:\n%s", ok, got, sha, out)
		}
	})
	t.Run("a vendored change with no SOURCE commit fails", func(t *testing.T) {
		ok, _, out := run(t, map[string]string{"proto-vendor/controlplane/SOURCE": "control-plane main\n"})
		if ok {
			t.Errorf("the step passed with a SOURCE that names no commit:\n%s", out)
		}
	})
}

// ciShellIn is ciShell run from dir.
func ciShellIn(t *testing.T, dir, script string, env map[string]string) (bool, string) {
	t.Helper()
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, string(out)
	}
	if _, ok := err.(*exec.ExitError); !ok {
		t.Fatalf("run step script: %v", err)
	}
	return false, string(out)
}
