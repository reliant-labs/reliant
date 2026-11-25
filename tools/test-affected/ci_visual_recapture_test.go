package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	"gopkg.in/yaml.v3"
)

// visual-recapture.yml re-captures a PR's visual baselines on a hosted runner
// and pushes them to its branch (owner, 2026-11-24): pr-fix has no docker, so a
// PR whose pixels change on purpose ended at needs:human. These tests pin its
// trust boundary: the job that runs the PR's code can only read, and the job
// that can write runs main's apply script and nothing from the PR.

type visualRecapture struct {
	On          map[string]any `yaml:"on"`
	Permissions map[string]any `yaml:"permissions"`
	Concurrency struct {
		Group            string `yaml:"group"`
		CancelInProgress *bool  `yaml:"cancel-in-progress"`
	} `yaml:"concurrency"`
	Jobs map[string]*struct {
		Needs       any            `yaml:"needs"`
		If          string         `yaml:"if"`
		Permissions map[string]any `yaml:"permissions"`
		Steps       []mqStep       `yaml:"steps"`
	} `yaml:"jobs"`
}

func TestVisualRecaptureWorkflow(t *testing.T) {
	var wf visualRecapture
	if err := yaml.Unmarshal(readRepo(t, ".github/workflows/visual-recapture.yml"), &wf); err != nil {
		t.Fatalf("parse visual-recapture.yml: %v", err)
	}
	if _, ok := wf.On["workflow_dispatch"]; !ok || len(wf.On) != 1 {
		t.Errorf("visual-recapture.yml runs on %v, want workflow_dispatch alone", wf.On)
	}
	if len(wf.Permissions) != 1 || wf.Permissions["contents"] != "read" {
		t.Errorf("workflow permissions %v, want contents: read alone", wf.Permissions)
	}
	if !strings.Contains(wf.Concurrency.Group, "inputs.pr") || wf.Concurrency.CancelInProgress == nil || *wf.Concurrency.CancelInProgress {
		t.Error("one re-capture per PR at a time, never cancelled mid-push")
	}
	capture, push := wf.Jobs["capture"], wf.Jobs["push"]
	if capture == nil || push == nil || len(wf.Jobs) != 2 {
		t.Fatalf("want the jobs capture and push, have %v", sortedKeys(wf.Jobs))
	}
	// The job that builds and runs the PR's code can write nothing.
	for k, v := range capture.Permissions {
		if v != "read" {
			t.Errorf("capture holds %s: %v; it runs the PR's code, so it may only read", k, v)
		}
	}
	var update, upload bool
	for _, s := range capture.Steps {
		if strings.Contains(s.Run, "task test:web:visual -- --update-snapshots=changed") {
			update = true
		}
		if strings.HasPrefix(s.Uses, "actions/upload-artifact@") {
			upload = true
		}
		if strings.HasPrefix(s.Uses, "actions/checkout@") && s.With["persist-credentials"] != false {
			t.Error("capture's checkout must not persist a credential")
		}
	}
	if !update || !upload {
		t.Errorf("capture must re-capture the changed baselines (%v) and upload them (%v)", update, upload)
	}
	// The job that pushes runs nothing from the PR: only main's apply script. Its own
	// GITHUB_TOKEN only reads: the push is the Reliant App's, with a token minted for this
	// repository and handed to the push step alone, so control-plane's push gate (cp#3624)
	// trusts the CI that follows it. A GITHUB_TOKEN push stays refused there, and none is made.
	if push.Permissions["contents"] != "read" || len(push.Permissions) != 1 {
		t.Errorf("push permissions %v, want contents: read alone (the push is the Reliant App's token, never GITHUB_TOKEN; no actions: a dispatch on the PR branch would run its workflow with secrets)", push.Permissions)
	}
	const appToken = "${{ steps.app.outputs.token }}"
	var mints, tokenUsers int
	for _, s := range push.Steps {
		if strings.HasPrefix(s.Uses, "actions/create-github-app-token@") {
			mints++
			if s.ID != "app" || !regexp.MustCompile(`@[0-9a-f]{40}$`).MatchString(strings.Fields(s.Uses)[0]) {
				t.Errorf("the App token step is id app, pinned to a commit: %q id %q", s.Uses, s.ID)
			}
			if s.With["permission-contents"] != "write" || s.With["repositories"] != "${{ github.event.repository.name }}" ||
				s.With["private-key"] != "${{ secrets.RELIANT_APP_PRIVATE_KEY }}" || s.With["client-id"] != "${{ vars.RELIANT_APP_CLIENT_ID }}" {
				t.Errorf("the App token is minted for this repository alone, contents: write, from the Reliant App's credentials: %v", s.With)
			}
			for k := range s.With {
				if strings.HasPrefix(k, "permission-") && k != "permission-contents" {
					t.Errorf("the App token asks for %s too: contents: write alone", k)
				}
			}
		}
		blob := s.Run
		for _, v := range s.Env {
			blob += "\n" + v
		}
		for _, v := range s.With {
			if str, ok := v.(string); ok {
				blob += "\n" + str
			}
		}
		if strings.Contains(blob, "steps.app.outputs.token") {
			tokenUsers++
			if !strings.Contains(s.Run, "trusted/.github/scripts/visual-recapture-apply.sh") || s.Env["PUSH_TOKEN"] != appToken {
				t.Errorf("step %q holds the App token: only the push step (PUSH_TOKEN, main's apply script) may", s.Name)
			}
		}
		if strings.Contains(blob, "github.token") || strings.Contains(blob, "GITHUB_TOKEN }}") || strings.Contains(blob, "secrets.GITHUB_TOKEN") {
			t.Errorf("push step %q uses the job's GITHUB_TOKEN: the push is the Reliant App's alone", s.Name)
		}
		if strings.HasPrefix(s.Uses, "actions/checkout@") && s.With["persist-credentials"] != false {
			t.Errorf("push's checkout %v persists a credential: the push must use the App's token, not GITHUB_TOKEN", s.With["path"])
		}
	}
	if mints != 1 || tokenUsers != 1 {
		t.Errorf("one App token step (%d) and one step that uses it (%d)", mints, tokenUsers)
	}
	if push.Needs != "capture" || !strings.Contains(push.If, "needs.capture.outputs.changed") {
		t.Errorf("push needs capture (%v) and runs only when a baseline changed (%q)", push.Needs, push.If)
	}
	var trusted, apply bool
	for _, s := range push.Steps {
		switch {
		case strings.HasPrefix(s.Uses, "actions/checkout@") && s.With["path"] == "trusted":
			trusted = s.With["ref"] == "main" && s.With["persist-credentials"] == false
		case strings.HasPrefix(s.Uses, "actions/checkout@"), strings.HasPrefix(s.Uses, "actions/download-artifact@"):
		case strings.HasPrefix(s.Uses, "./"):
			t.Errorf("push uses a local action %q: it would run the PR's or the artifact's code", s.Uses)
		case s.Run != "":
			if strings.Contains(s.Run, "trusted/.github/scripts/visual-recapture-apply.sh") {
				apply = true
			}
			if strings.Contains(s.Run, "workflow run") || strings.Contains(s.Run, "dispatches") {
				t.Errorf("push step %q dispatches a workflow: on the PR branch that runs the PR's definition with secrets", s.Name)
			}
			for _, bad := range []string{"pr/", "staged/", "task ", "npm ", "pnpm ", "npx ", "make "} {
				if strings.Contains(strings.ReplaceAll(s.Run, "$RUNNER_TEMP", ""), bad) && !strings.Contains(s.Run, "visual-recapture-apply.sh") {
					t.Errorf("push step %q runs %q: nothing from the PR may run where the token can write", s.Name, bad)
				}
			}
		}
	}
	if !trusted || !apply {
		t.Errorf("push must check main's scripts out credential-free (%v) and run its apply script (%v)", trusted, apply)
	}
}

func TestVisualRecaptureApplyScript(t *testing.T) {
	testkit.Slow(t, "runs .github/scripts/visual-recapture-apply.sh against scratch git repositories",
		testkit.Inputs(".github/scripts/visual-recapture-apply.sh"))
	requireShellTools(t)
	script := filepath.Join(repoRoot, ".github", "scripts", "visual-recapture-apply.sh")
	const shot = "frontends/web/e2e/visual/__screenshots__/desktop-dark/app.png"
	png := "\x89PNG\r\n\x1a\nbody"

	type setup struct {
		files map[string]string // staged path -> content
		link  string            // a staged symlink, by path
		dest  string            // a symlink committed in the PR's checkout at this path, to /tmp
		moved bool              // the remote branch moved past the head
		noTok bool              // no PUSH_TOKEN: the step was given no App token
	}
	run := func(t *testing.T, s setup) (string, int, string) {
		t.Helper()
		dir := t.TempDir()
		remote := filepath.Join(dir, "remote.git")
		if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", remote).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
		work := filepath.Join(dir, "pr")
		if err := os.MkdirAll(work, 0o755); err != nil {
			t.Fatal(err)
		}
		git := scratchGit(t, work)
		scratchWrite(t, work, shot, "\x89PNG\r\n\x1a\nold")
		if s.dest != "" {
			outside := filepath.Join(dir, "outside")
			if err := os.MkdirAll(outside, 0o755); err != nil {
				t.Fatal(err)
			}
			_ = os.RemoveAll(filepath.Join(work, s.dest))
			if err := os.MkdirAll(filepath.Dir(filepath.Join(work, s.dest)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(work, s.dest)); err != nil {
				t.Fatal(err)
			}
		}
		git("add", ".")
		git("commit", "-q", "-m", "base")
		git("remote", "add", "origin", remote)
		git("push", "-q", "origin", "HEAD:refs/heads/feat/ui")
		head := git("rev-parse", "HEAD")
		if s.moved {
			other := filepath.Join(dir, "other")
			if out, err := exec.Command("git", "clone", "-q", "-b", "feat/ui", remote, other).CombinedOutput(); err != nil {
				t.Fatalf("clone: %v\n%s", err, out)
			}
			og := func(args ...string) {
				cmd := exec.Command("git", args...)
				cmd.Dir = other
				cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			og("commit", "-q", "--allow-empty", "-m", "a later push")
			og("push", "-q", "origin", "HEAD:refs/heads/feat/ui")
		}
		staged := filepath.Join(dir, "staged")
		scratchWrite(t, staged, "manifest.txt", "head="+head+"\nmain="+strings.Repeat("a", 40)+"\n")
		for p, body := range s.files {
			scratchWrite(t, staged, p, body)
		}
		if s.link != "" {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(staged, s.link)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/etc/passwd", filepath.Join(staged, s.link)); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command("bash", script)
		cmd.Env = append(os.Environ(), "STAGED="+staged, "TARGET="+work, "BRANCH=feat/ui", "HEAD_SHA="+head, "GIT_CONFIG_GLOBAL=/dev/null", "PUSH_TOKEN=")
		if !s.noTok {
			cmd.Env = append(cmd.Env, "PUSH_TOKEN=ghs_test")
		}
		out, err := cmd.CombinedOutput()
		rc := 0
		if err != nil {
			ee, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("run: %v", err)
			}
			rc = ee.ExitCode()
		}
		tip, _ := exec.Command("git", "--git-dir", remote, "rev-parse", "refs/heads/feat/ui").Output()
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		return lines[len(lines)-1], rc, strings.TrimSpace(string(tip))
	}

	t.Run("changed baselines are committed and pushed", func(t *testing.T) {
		t.Parallel()
		line, rc, _ := run(t, setup{files: map[string]string{shot: png, "frontends/web/e2e/visual/__screenshots__/mobile/new.png": png}})
		if rc != 0 || !strings.HasPrefix(line, "RECAPTURE=pushed n=2 sha=") {
			t.Fatalf("rc=%d last=%q, want a push of 2", rc, line)
		}
	})
	t.Run("nothing changed is none, with no commit", func(t *testing.T) {
		t.Parallel()
		if line, rc, _ := run(t, setup{}); rc != 0 || line != "RECAPTURE=none" {
			t.Fatalf("rc=%d last=%q, want none", rc, line)
		}
		if line, rc, _ := run(t, setup{files: map[string]string{shot: "\x89PNG\r\n\x1a\nold"}}); rc != 0 || line != "RECAPTURE=none" {
			t.Fatalf("an identical baseline: rc=%d last=%q, want none", rc, line)
		}
	})
	refused := map[string]setup{
		"a file outside the screenshots":  {files: map[string]string{shot: png, "frontends/web/src/app.tsx": "evil"}},
		"a baseline that is not a PNG":    {files: map[string]string{shot: "#!/bin/sh\n"}},
		"a hidden path segment":           {files: map[string]string{"frontends/web/e2e/visual/__screenshots__/.git/x.png": png}},
		"a symlink":                       {link: shot},
		"a baseline over 1 MiB":           {files: map[string]string{shot: png + strings.Repeat("x", 1<<20)}},
		"a workflow smuggled beside them": {files: map[string]string{shot: png, ".github/workflows/x.yml": "on: push"}},
		"a symlinked directory in the PR": {files: map[string]string{shot: png}, dest: "frontends/web/e2e/visual/__screenshots__/desktop-dark"},
		"a symlinked baseline in the PR":  {files: map[string]string{"frontends/web/e2e/visual/__screenshots__/mobile/x.png": png}, dest: "frontends/web/e2e/visual/__screenshots__/mobile/x.png"},
	}
	for name, s := range refused {
		t.Run(name+" refuses the whole set", func(t *testing.T) {
			t.Parallel()
			line, rc, _ := run(t, s)
			if rc != 1 || !strings.HasPrefix(line, "RECAPTURE=refused ") {
				t.Fatalf("rc=%d last=%q, want refused", rc, line)
			}
		})
	}
	t.Run("no App token pushes nothing", func(t *testing.T) {
		t.Parallel()
		line, rc, tip := run(t, setup{files: map[string]string{shot: png}, noTok: true})
		if rc != 1 || !strings.HasPrefix(line, "RECAPTURE=failed no PUSH_TOKEN") {
			t.Fatalf("rc=%d last=%q, want failed: no PUSH_TOKEN (never a GITHUB_TOKEN push)", rc, line)
		}
		if tip == "" {
			t.Fatal("the remote branch is gone")
		}
	})
	t.Run("a branch that moved refuses the push", func(t *testing.T) {
		t.Parallel()
		line, rc, _ := run(t, setup{files: map[string]string{shot: png}, moved: true})
		if rc != 1 || line != "RECAPTURE=moved" {
			t.Fatalf("rc=%d last=%q, want moved", rc, line)
		}
	})
}
