package main

import (
	"os"
	"os/exec"
	"path/filepath"
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
	// The job that writes runs nothing from the PR: only main's apply script.
	if push.Permissions["contents"] != "write" || push.Permissions["actions"] != "write" || len(push.Permissions) != 2 {
		t.Errorf("push permissions %v, want contents: write and actions: write alone", push.Permissions)
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
		moved bool              // the remote branch moved past the head
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
		cmd.Env = append(os.Environ(), "STAGED="+staged, "TARGET="+work, "BRANCH=feat/ui", "HEAD_SHA="+head, "GIT_CONFIG_GLOBAL=/dev/null")
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
	t.Run("a branch that moved refuses the push", func(t *testing.T) {
		t.Parallel()
		line, rc, _ := run(t, setup{files: map[string]string{shot: png}, moved: true})
		if rc != 1 || line != "RECAPTURE=moved" {
			t.Fatalf("rc=%d last=%q, want moved", rc, line)
		}
	})
}
