package ciguard

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// CHANGELOG.md is written only at release time, by scripts/release-forge.sh,
// from the merged PR titles. A pull request that edits it is the merge-queue
// conflict that policy removed, so ci.yml's `checks` job runs
// scripts/check-changelog-untouched.sh on pull requests. These tests pin the
// script's verdicts and that the workflow still runs it, in an existing job.

var changelogGuardScript = filepath.Join("..", "..", "scripts", "check-changelog-untouched.sh")

type stepsWorkflow struct {
	Jobs map[string]struct {
		Steps []struct {
			Name string `yaml:"name"`
			If   string `yaml:"if"`
			Run  string `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func TestChangelogGuardRunsOnPullRequestsInTheChecksJob(t *testing.T) {
	raw, err := os.ReadFile(ciWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	var wf stepsWorkflow
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	checks, ok := wf.Jobs["checks"]
	if !ok {
		t.Fatal("ci.yml has no checks job")
	}
	found := false
	for _, s := range checks.Steps {
		if !strings.Contains(s.Run, "check-changelog-untouched.sh") {
			continue
		}
		found = true
		if strings.TrimSpace(s.If) != "${{ github.event_name == 'pull_request' }}" {
			t.Errorf("the CHANGELOG guard step has if: %q; it must run on pull_request only (a merge-queue entry has no head branch name for the release/* exemption)", s.If)
		}
		if !strings.Contains(s.Run, "HEAD^1") {
			t.Errorf("the CHANGELOG guard step runs %q; it must diff against HEAD^1, the base branch tip of the merge commit", s.Run)
		}
	}
	if !found {
		t.Error("ci.yml's checks job does not run scripts/check-changelog-untouched.sh: a pull request could edit CHANGELOG.md again")
	}
	for name, j := range wf.Jobs {
		for _, s := range j.Steps {
			if strings.Contains(s.Run, "check-changelog-untouched.sh") && name != "checks" {
				t.Errorf("job %q runs the CHANGELOG guard; it belongs in checks, so no job is added to the pull-request tier", name)
			}
		}
	}
}

// guardRepo builds a base commit plus one change commit and returns the repo.
func guardRepo(t *testing.T, subject string, files map[string]string) string {
	t.Helper()
	for _, bin := range []string{"bash", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
	// Read by this process so the test cache sees an edit to the script.
	if _, err := os.ReadFile(changelogGuardScript); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "t@example.com")
	git("config", "user.name", "t")
	write("CHANGELOG.md", "# Changelog\n")
	write("a.go", "package a\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	for rel, content := range files {
		write(rel, content)
	}
	git("add", ".")
	git("commit", "-q", "-m", subject)
	return dir
}

func runChangelogGuard(t *testing.T, repo, branch string) (string, error) {
	t.Helper()
	abs, err := filepath.Abs(changelogGuardScript)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(context.Background(), "bash", abs, "HEAD^1")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GITHUB_HEAD_REF="+branch)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestChangelogGuardVerdicts(t *testing.T) {
	edit := map[string]string{"CHANGELOG.md": "# Changelog\n\n## [Unreleased]\n\n- an entry\n"}
	for _, tc := range []struct {
		name    string
		subject string
		files   map[string]string
		branch  string
		wantErr bool
		wantOut string
	}{
		{"a PR that does not touch it passes", "feat: x (#1)", map[string]string{"a.go": "package a // changed\n"}, "feature/x", false, "untouched"},
		{"a PR that edits it fails", "feat: x (#2)", edit, "feature/x", true, "only the release script writes"},
		{"a PR that edits it alongside code still fails", "fix: y (#3)", map[string]string{"CHANGELOG.md": edit["CHANGELOG.md"], "a.go": "package a // y\n"}, "fix/y", true, "PR TITLE"},
		{"a release branch may edit it", "docs: notes", edit, "release/v0.2.0", false, "release branch"},
		{"a branch merely named like a release is not exempt", "docs: notes", edit, "my-release/v0.2.0", true, "only the release script writes"},
		{"the release commit may edit it", "chore: release v0.2.0", edit, "main", false, "release commit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := guardRepo(t, tc.subject, tc.files)
			out, err := runChangelogGuard(t, repo, tc.branch)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v\n%s", err, tc.wantErr, out)
			}
			if !strings.Contains(out, tc.wantOut) {
				t.Errorf("output does not mention %q:\n%s", tc.wantOut, out)
			}
		})
	}
}

// TestChangelogIsFormattedOnlyByTheRelease pins the other half of the policy:
// CHANGELOG.md has one writer, release-forge.sh, which formats the whole file
// with the pinned prettier, so the pre-commit formatters exclude it. A hook
// that checked it could only report drift no pull request is allowed to fix:
// a doubled blank line merged before the policy turned every PR's Checks red,
// and the only way out was an admin bypass on main.
func TestChangelogIsFormattedOnlyByTheRelease(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".pre-commit-config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Repos []struct {
			Hooks []struct {
				ID      string `yaml:"id"`
				Files   string `yaml:"files"`
				Exclude string `yaml:"exclude"`
			} `yaml:"hooks"`
		} `yaml:"repos"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	formatters := map[string]bool{"trailing-whitespace": true, "end-of-file-fixer": true, "prettier": true}
	for _, repo := range cfg.Repos {
		for _, h := range repo.Hooks {
			if !formatters[h.ID] {
				continue
			}
			delete(formatters, h.ID)
			if h.Exclude == "" {
				t.Errorf("hook %s excludes nothing; it must exclude CHANGELOG.md", h.ID)
				continue
			}
			re, err := regexp.Compile(h.Exclude)
			if err != nil {
				t.Fatalf("hook %s: exclude %q: %v", h.ID, h.Exclude, err)
			}
			if !re.MatchString("CHANGELOG.md") {
				t.Errorf("hook %s formats CHANGELOG.md (exclude %q); only scripts/release-forge.sh may", h.ID, h.Exclude)
			}
			// Narrowly: a nested changelog (a fixture, a scaffolded project's)
			// is still checked.
			if re.MatchString("docs/CHANGELOG.md") {
				t.Errorf("hook %s excludes every CHANGELOG.md (exclude %q); only the root one has a release-time formatter", h.ID, h.Exclude)
			}
		}
	}
	for id := range formatters {
		t.Errorf("hook %s not found in .pre-commit-config.yaml; update this test", id)
	}

	script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "release-forge.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`prettier@\$pin" --write [^\n]*"\$CHANGELOG_FILE"`).Match(script) {
		t.Error("scripts/release-forge.sh no longer formats CHANGELOG.md with the pinned prettier; the pre-commit exclusion is then unsafe")
	}
}
