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
		var checkedOut, downloaded, checked, uploaded bool
		for _, s := range j.Steps {
			switch {
			case strings.TrimSpace(s.Run) == "scripts/test-artifact-check.sh .forge/tests/runs":
				// main's check of the candidate's artifact, after the download and
				// before the upload, which runs only when it passed.
				if !downloaded || uploaded {
					t.Errorf("job %s checks the artifact before its download or after its upload", id)
				}
				if s.ID != "artifact" || s.ContinueOnError != nil {
					t.Errorf("job %s artifact check has id %q, continue-on-error %v; want id artifact and a failure that stops the upload", id, s.ID, s.ContinueOnError)
				}
				checked = true
				continue
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
				if !checked || s.If != "${{ steps.artifact.outcome == 'success' }}" {
					t.Errorf("job %s uploads (if %q) without the artifact check passing first", id, s.If)
				}
				if strings.TrimSpace(s.Run) != `scripts/test-upload.sh "${HEAD_SHA}"` || s.Env["HEAD_SHA"] != "${{ github.event.workflow_run.head_sha }}" {
					t.Errorf("job %s upload step runs %q with %v; want main's scripts/test-upload.sh on the event's head sha", id, s.Run, s.Env)
				}
			}
			// The artifact is data: no other step may execute anything under it.
			if strings.Contains(s.Run, ".forge/tests/runs") {
				t.Errorf("job %s step %q touches the artifact in a shell; it is data for main's check and forge alone", id, s.Name)
			}
		}
		if !checkedOut || !downloaded || !checked || !uploaded {
			t.Errorf("job %s: checkout=%v download=%v check=%v upload=%v, want all four", id, checkedOut, downloaded, checked, uploaded)
		}
	}
}

// TestMainTestUploadHoldsOIDC: ci.yml's upload of main's runs holds id-token
// and uploads with forge's OIDC exchange alone (no token passed to the step),
// and only a push to main of this repository reaches it.
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
	// The upload runs on the OIDC exchange alone: a token passed to the step
	// would win (cloud.ResolveCredentialFor) and the exchange would never run.
	var uploads int
	for _, s := range j.Steps {
		if !strings.Contains(s.Run, "scripts/test-upload.sh") {
			continue
		}
		uploads++
		if _, ok := s.Env["FORGE_CONTROL_PLANE_TOKEN"]; ok {
			t.Errorf("test-upload's upload step sets FORGE_CONTROL_PLANE_TOKEN; it uploads with the job's OIDC identity")
		}
	}
	if uploads != 1 {
		t.Errorf("test-upload has %d steps running scripts/test-upload.sh, want 1", uploads)
	}
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
	env := map[string]string{"FORGE_CONTROL_PLANE_TOKEN": "", "ACTIONS_ID_TOKEN_REQUEST_URL": "", "GITHUB_ACTIONS": "true"}
	const sha = "0123456789abcdef0123456789abcdef01234567"

	// The credential line names the path forge takes first, never a value, and
	// never stops the upload: a job with id-token and no token uploads by OIDC.
	for _, tc := range []struct {
		name, token, oidcURL, want string
	}{
		{"oidc only", "", "https://token.actions.example/req", "credential: this job's GitHub Actions OIDC identity"},
		{"token wins", "SECRET-TOKEN-VALUE", "https://token.actions.example/req", "credential: FORGE_CONTROL_PLANE_TOKEN\n"},
		{"neither", "", "", "credential: no FORGE_CONTROL_PLANE_TOKEN and no GitHub Actions OIDC identity"},
	} {
		dir := setup(t, "0", true)
		ok, out := ciShellIn(t, dir, "scripts/test-upload.sh "+sha, map[string]string{
			"FORGE_CONTROL_PLANE_TOKEN": tc.token, "ACTIONS_ID_TOKEN_REQUEST_URL": tc.oidcURL, "GITHUB_ACTIONS": "true",
		})
		if !ok || !strings.Contains(out, tc.want) || !strings.Contains(out, "forge-args: test report --commit "+sha+" --upload\n") {
			t.Errorf("%s: want exit 0, %q and an upload request; ok=%v:\n%s", tc.name, tc.want, ok, out)
		}
		if strings.Contains(out, "SECRET-TOKEN-VALUE") || strings.Contains(out, "::warning") {
			t.Errorf("%s: the script printed the token or warned on its own:\n%s", tc.name, out)
		}
	}

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

// TestTestArtifactCheckRefusesEscapes runs scripts/test-artifact-check.sh on
// hostile copies of a merge-gate artifact. The candidate writes that artifact
// and the uploading job holds id-token, so a capture.json path outside its lane
// (or a symlink) would have forge read and upload the job's environment. Each
// is refused, the runs dir is deleted, and the sentinel planted outside it is
// never copied in; a normal artifact passes untouched.
func TestTestArtifactCheckRefusesEscapes(t *testing.T) {
	requireShellTools(t)
	script := readRepo(t, "scripts/test-artifact-check.sh")
	const sentinel = "SENTINEL-OIDC-REQUEST-TOKEN"
	const okCapture = `{"schema":"forge.test.lane/v1","lane":"short","results_file":"results.json","coverage":[{"path":"cover.out","format":"go-profile"},{"path":"covdata/","format":"go-covdata"}]}`
	cases := []struct {
		name    string
		capture string
		tweak   func(t *testing.T, lane, outside string)
		refused bool
	}{
		{name: "normal artifact", capture: okCapture},
		{name: "no paths at all", capture: `{"schema":"forge.test.lane/v1","lane":"short","coverage":[]}`},
		{name: "results_file traversal", capture: `{"lane":"short","results_file":"../../../outside/secret"}`, refused: true},
		{name: "coverage traversal", capture: `{"lane":"short","coverage":[{"path":"a/../../../outside/secret","format":"lcov"}]}`, refused: true},
		{name: "absolute results_file", capture: `{"lane":"short","results_file":"/proc/self/environ"}`, refused: true},
		{name: "dot segment", capture: `{"lane":"short","results_file":"./results.json"}`, refused: true},
		{name: "newline in path", capture: `{"lane":"short","results_file":"ok\n../../x"}`, refused: true},
		{name: "non-string path", capture: `{"lane":"short","results_file":{"p":1}}`, refused: true},
		{name: "coverage not a list", capture: `{"lane":"short","coverage":"../x"}`, refused: true},
		{name: "malformed capture.json", capture: `{"lane":`, refused: true},
		{name: "symlink in a lane", capture: okCapture, refused: true, tweak: func(t *testing.T, lane, outside string) {
			if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(lane, "results.json")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "malformed run.json", capture: okCapture, refused: true, tweak: func(t *testing.T, lane, _ string) {
			if err := os.WriteFile(filepath.Join(filepath.Dir(lane), "run.json"), []byte("not json"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			outside := filepath.Join(dir, "outside")
			runs := filepath.Join(dir, "repo", ".forge", "tests", "runs")
			lane := filepath.Join(runs, "01RUN", "short")
			for _, d := range []string{outside, filepath.Join(lane, "covdata"), filepath.Join(dir, "repo", "scripts")} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			writeExec(t, filepath.Join(dir, "repo", "scripts", "test-artifact-check.sh"), string(script))
			files := map[string]string{
				filepath.Join(outside, "secret"):         sentinel,
				filepath.Join(runs, "01RUN", "run.json"): `{"schema":"forge.test.run/v1","id":"01RUN","lanes":["short"]}`,
				filepath.Join(lane, "capture.json"):      tc.capture,
			}
			for p, body := range files {
				if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.tweak != nil {
				tc.tweak(t, lane, outside)
			}
			ok, out := ciShellIn(t, filepath.Join(dir, "repo"), "scripts/test-artifact-check.sh .forge/tests/runs", map[string]string{"GITHUB_ACTIONS": "true"})
			_, statErr := os.Lstat(runs)
			if strings.Contains(out, sentinel) {
				t.Errorf("the check printed the outside file's bytes:\n%s", out)
			}
			if tc.refused {
				if ok || !strings.Contains(out, "::warning title=Test results refused::") || statErr == nil {
					t.Errorf("want refused (exit 1, a warning, the runs dir deleted); ok=%v runs-dir-err=%v:\n%s", ok, statErr, out)
				}
			} else if !ok || statErr != nil || strings.Contains(out, "::warning") {
				t.Errorf("want accepted, untouched and silent; ok=%v runs-dir-err=%v:\n%s", ok, statErr, out)
			}
			if b, err := os.ReadFile(filepath.Join(outside, "secret")); err != nil || string(b) != sentinel {
				t.Errorf("the outside file changed: %q %v", b, err)
			}
		})
	}

	// No artifact at all (a gate that recorded nothing): nothing to check.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeExec(t, filepath.Join(dir, "scripts", "test-artifact-check.sh"), string(script))
	if ok, out := ciShellIn(t, dir, "scripts/test-artifact-check.sh .forge/tests/runs", nil); !ok {
		t.Errorf("no artifact: want exit 0:\n%s", out)
	}
}
