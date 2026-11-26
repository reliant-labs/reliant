package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The Tests view's uploads (no gate had ever uploaded a run): main's runs from
// ci.yml's test-upload job, and the merge gate's from the default-branch
// listener test-upload-merge-queue.yml. Both reach the control plane through
// scripts/test-upload.sh, which always asks forge to upload and lets forge find
// the credential (FORGE_CONTROL_PLANE_TOKEN, else the job's GitHub Actions
// OIDC identity). These pin where id-token may be held: by main's code only,
// never by the merge group's candidate (TestMergeQueueHasNoWriteCredential).

type uploadWorkflow struct {
	On          map[string]any `yaml:"on"`
	Permissions any            `yaml:"permissions"`
	Jobs        map[string]*struct {
		Name            string            `yaml:"name"`
		If              string            `yaml:"if"`
		Environment     any               `yaml:"environment"`
		ContinueOnError any               `yaml:"continue-on-error"`
		Permissions     map[string]string `yaml:"permissions"`
		Steps           []mqStep          `yaml:"steps"`
	} `yaml:"jobs"`
}

func loadUploadWorkflow(t *testing.T, rel string) *uploadWorkflow {
	t.Helper()
	var wf uploadWorkflow
	if err := yaml.Unmarshal(readRepo(t, rel), &wf); err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	return &wf
}

// noLocalActions fails a step of an id-token job that runs a local composite
// action: internal/version's pin test reads only the workflow's own steps, and
// ./.github/actions/setup runs actions by tag, so a job holding an OIDC
// identity sets up Go with the pinned setup-go instead.
func noLocalActions(t *testing.T, where string, steps []mqStep) {
	t.Helper()
	for _, s := range steps {
		if strings.HasPrefix(s.Uses, "./") {
			t.Errorf("%s runs the local action %s while holding id-token; its actions are not pinned to reviewed commits", where, s.Uses)
		}
	}
}

// TestMergeQueueTestUploadRunsMainsCode: the merge gate keeps its recorded run
// as an artifact, and a workflow_run listener (the default branch's copy) on
// main's checkout uploads it, holding id-token where the candidate never does.
func TestMergeQueueTestUploadRunsMainsCode(t *testing.T) {
	// The producer: the merge gate keeps .forge/tests/runs, pass or fail, under
	// the name the listener downloads.
	gate := loadMergeQueue(t).Jobs["merge-gate"]
	if gate == nil {
		t.Fatal("merge-queue.yml has no merge-gate job")
	}
	var kept bool
	for _, s := range gate.Steps {
		if !strings.HasPrefix(s.Uses, "actions/upload-artifact@") || s.With["name"] != "merge-gate-test-runs-${{ github.run_attempt }}" {
			continue
		}
		kept = true
		if s.With["path"] != ".forge/tests/runs" || s.With["include-hidden-files"] != true || s.If != "${{ !cancelled() }}" {
			t.Errorf("the merge gate's test-run artifact step = if %q with %v; want .forge/tests/runs, include-hidden-files, if !cancelled()", s.If, s.With)
		}
	}
	if !kept {
		t.Error("the merge gate does not keep its recorded test run as merge-gate-test-runs-<attempt>")
	}

	rel := ".github/workflows/test-upload-merge-queue.yml"
	wf := loadUploadWorkflow(t, rel)
	run, ok := wf.On["workflow_run"].(map[string]any)
	if !ok || len(wf.On) != 1 {
		t.Fatalf("%s must be a workflow_run listener alone (the default branch's copy runs), on %v", rel, wf.On)
	}
	if !reflect.DeepEqual(run["workflows"], []any{"Merge queue"}) {
		t.Errorf("%s listens to %v, want [Merge queue]", rel, run["workflows"])
	}
	if p, ok := wf.Permissions.(map[string]any); !ok || len(p) != 0 {
		t.Errorf("%s workflow permissions %v, want {}", rel, wf.Permissions)
	}
	if len(wf.Jobs) != 1 {
		t.Fatalf("%s has jobs %v, want the one upload job", rel, sortedKeys(wf.Jobs))
	}
	for id, j := range wf.Jobs {
		if j.Name != "Upload the merge gate's test results" {
			t.Errorf("job %s is named %q", id, j.Name)
		}
		if !strings.Contains(j.If, "github.event.workflow_run.event == 'merge_group'") || !strings.Contains(j.If, "github.repository == 'reliant-labs/reliant'") {
			t.Errorf("job %s if=%q: it must run only for this repository's merge groups", id, j.If)
		}
		if j.ContinueOnError != true {
			t.Errorf("job %s must be continue-on-error: an upload never decides anything", id)
		}
		if j.Environment != nil {
			t.Errorf("job %s uses environment %v: OIDC needs none", id, j.Environment)
		}
		want := map[string]string{"actions": "read", "contents": "read", "id-token": "write"}
		if !reflect.DeepEqual(j.Permissions, want) {
			t.Errorf("job %s permissions %v, want exactly %v", id, j.Permissions, want)
		}
		noLocalActions(t, rel+" job "+id, j.Steps)
		var checkedOut, downloaded, uploaded bool
		for _, s := range j.Steps {
			switch {
			case strings.HasPrefix(s.Uses, "actions/checkout@"):
				checkedOut = true
				if ref, ok := s.With["ref"]; ok {
					t.Errorf("job %s checks out ref %v: it must run main's code, never the group's", id, ref)
				}
				if s.With["persist-credentials"] != false {
					t.Errorf("job %s checkout persists its credential", id)
				}
			case strings.HasPrefix(s.Uses, "actions/download-artifact@"):
				downloaded = true
				if s.With["name"] != "merge-gate-test-runs-${{ github.event.workflow_run.run_attempt }}" ||
					s.With["path"] != ".forge/tests/runs" || s.With["run-id"] != "${{ github.event.workflow_run.id }}" {
					t.Errorf("job %s downloads %v; want the producing run's merge-gate-test-runs-<attempt> into .forge/tests/runs", id, s.With)
				}
			case strings.Contains(s.Run, "scripts/test-upload.sh"):
				uploaded = true
				if strings.TrimSpace(s.Run) != `scripts/test-upload.sh "${HEAD_SHA}"` || s.Env["HEAD_SHA"] != "${{ github.event.workflow_run.head_sha }}" {
					t.Errorf("job %s upload step runs %q with %v; want main's scripts/test-upload.sh on the event's head sha", id, s.Run, s.Env)
				}
			}
			// The artifact is data: no step may execute anything under it.
			if strings.Contains(s.Run, ".forge/tests/runs") {
				t.Errorf("job %s step %q touches the artifact in a shell; it is data for forge alone", id, s.Name)
			}
		}
		if !checkedOut || !downloaded || !uploaded {
			t.Errorf("job %s: checkout=%v download=%v upload=%v, want all three", id, checkedOut, downloaded, uploaded)
		}
	}
}

// TestMainTestUploadHoldsOIDC: ci.yml's upload of main's runs holds id-token
// (forge's OIDC exchange when the environment has no token), and only a push
// to main of this repository reaches it.
func TestMainTestUploadHoldsOIDC(t *testing.T) {
	rel := ".github/workflows/ci.yml"
	j := loadUploadWorkflow(t, rel).Jobs["test-upload"]
	if j == nil {
		t.Fatalf("%s has no test-upload job", rel)
	}
	want := map[string]string{"contents": "read", "id-token": "write"}
	if !reflect.DeepEqual(j.Permissions, want) {
		t.Errorf("test-upload permissions %v, want exactly %v", j.Permissions, want)
	}
	for _, clause := range []string{"github.event_name == 'push'", "github.ref == 'refs/heads/main'", "github.repository == 'reliant-labs/reliant'"} {
		if !strings.Contains(j.If, clause) {
			t.Errorf("test-upload if=%q lacks %q: id-token is for main's own code", j.If, clause)
		}
	}
	if j.ContinueOnError != true {
		t.Error("test-upload must be continue-on-error")
	}
	noLocalActions(t, rel+" job test-upload", j.Steps)
}

// TestTestUploadScriptAlwaysAsksToUpload runs scripts/test-upload.sh in a
// fixture repository with a stub forge: with no token it still asks forge to
// upload (forge then tries the job's OIDC identity), and it exits 0 whatever
// forge does, warning when there is no recorded run.
func TestTestUploadScriptAlwaysAsksToUpload(t *testing.T) {
	requireShellTools(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	script := readRepo(t, "scripts/test-upload.sh")
	setup := func(t *testing.T, forgeExit string, withRun bool) string {
		t.Helper()
		dir := t.TempDir()
		if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
		if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeExec(t, filepath.Join(dir, "scripts", "test-upload.sh"), string(script))
		writeExec(t, filepath.Join(dir, "scripts", "forge.sh"), "#!/usr/bin/env bash\necho \"forge-args: $*\"\nexit "+forgeExit+"\n")
		if withRun {
			run := filepath.Join(dir, ".forge", "tests", "runs", "01RUN")
			if err := os.MkdirAll(run, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(run, "run.json"), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	env := map[string]string{"FORGE_CONTROL_PLANE_TOKEN": "", "GITHUB_ACTIONS": "true"}
	const sha = "0123456789abcdef0123456789abcdef01234567"

	for _, forgeExit := range []string{"0", "2"} {
		dir := setup(t, forgeExit, true)
		ok, out := ciShellIn(t, dir, "scripts/test-upload.sh "+sha, env)
		if !ok {
			t.Errorf("forge exit %s: the script failed; it must always exit 0:\n%s", forgeExit, out)
		}
		if !strings.Contains(out, "forge-args: test report --commit "+sha+" --upload\n") {
			t.Errorf("forge exit %s, no token: forge was not asked to upload:\n%s", forgeExit, out)
		}
		if strings.Contains(out, "--upload=false") || strings.Contains(out, "::warning") {
			t.Errorf("forge exit %s: the script downgraded or warned on its own; the credential is forge's to find:\n%s", forgeExit, out)
		}
	}

	dir := setup(t, "0", false)
	ok, out := ciShellIn(t, dir, "scripts/test-upload.sh "+sha, env)
	if !ok || !strings.Contains(out, "::warning title=Test results not uploaded::no recorded test run") || strings.Contains(out, "forge-args:") {
		t.Errorf("no recorded run: want exit 0, a warning and no forge call; ok=%v:\n%s", ok, out)
	}
}
