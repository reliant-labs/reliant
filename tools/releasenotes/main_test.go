package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const repo = "https://github.com/reliant-labs/reliant"

func TestDraftGroupsByConventionalCommitType(t *testing.T) {
	rel, err := Draft("v1.8.12", "2026-10-30", "v1.8.11", repo, []Commit{
		{Subject: "feat(chats): device-code login (#10)"},
		{Subject: "fix: stop the double send (#11)"},
		{Subject: "perf(test): overlap lanes (#12)"},
		{Subject: "feat(api)!: drop the v0 RPCs (#13)"},
		{Subject: "refactor: one store (#14)", Body: "text\n\nBREAKING CHANGE: the store moved\n"},
		{Subject: "chore: release v1.8.11 (#9)"},
		{Subject: "Merge branch 'x'"},
		{Subject: "a title with no type"},
		{Subject: "Fix(ui): capital type (#15)"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	var order []string
	for _, s := range rel.Sections {
		order = append(order, s.Title)
		for _, it := range s.Items {
			got[s.Title] = append(got[s.Title], it.Title+" | "+it.Description)
		}
	}
	if want := "Breaking changes,Added,Changed,Fixed"; strings.Join(order, ",") != want {
		t.Fatalf("sections = %v, want %s", order, want)
	}
	want := map[string][]string{
		"Breaking changes": {"api: drop the v0 RPCs | [#13](" + repo + "/pull/13)", "one store | [#14](" + repo + "/pull/14)"},
		"Added":            {"chats: device-code login | [#10](" + repo + "/pull/10)"},
		"Changed":          {"test: overlap lanes | [#12](" + repo + "/pull/12)", "a title with no type | "},
		"Fixed":            {"stop the double send | [#11](" + repo + "/pull/11)", "ui: capital type | [#15](" + repo + "/pull/15)"},
	}
	for s, items := range want {
		if strings.Join(got[s], "\n") != strings.Join(items, "\n") {
			t.Errorf("%s:\n got %q\nwant %q", s, got[s], items)
		}
	}
	if rel.Title != "2 breaking changes, 1 new feature, 2 fixes and 2 other changes" {
		t.Errorf("title = %q", rel.Title)
	}
	if !strings.Contains(rel.Summary, "since v1.8.11") {
		t.Errorf("summary = %q", rel.Summary)
	}
}

func TestDraftWithOnlyReleaseCommitsHasNothingToSay(t *testing.T) {
	_, err := Draft("v1.8.12", "2026-10-30", "v1.8.11", repo, []Commit{{Subject: "chore: release v1.8.11 (#9)"}})
	if !errors.Is(err, errNoChanges) {
		t.Fatalf("err = %v, want errNoChanges", err)
	}
}

func TestHeadlineSingleGroup(t *testing.T) {
	if got := headline(map[string][]Item{sectionFixed: {{}}}); got != "1 fix" {
		t.Errorf("got %q", got)
	}
}

// The rendered file must parse as the schema tools/docgen/changelog reads, and
// keep any title byte-exact (quotes, colons, '#', backslashes, MDX braces).
func TestRenderRoundTripsThroughTheChangelogSchema(t *testing.T) {
	tricky := `fix: "quoted": a # b \ c {x} <y> & z: done (#7)`
	rel, err := Draft("v1.8.12", "2026-10-30", "v1.8.11", repo, []Commit{{Subject: tricky}})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Version  string `yaml:"version"`
		Date     string `yaml:"date"`
		Title    string `yaml:"title"`
		Summary  string `yaml:"summary"`
		Sections []struct {
			Title string `yaml:"title"`
			Items []struct {
				Title       string `yaml:"title"`
				Description string `yaml:"description"`
			} `yaml:"items"`
		} `yaml:"sections"`
	}
	out := Render(rel)
	if err := yaml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("rendered YAML does not parse: %v\n%s", err, out)
	}
	if parsed.Version != "v1.8.12" || parsed.Date != "2026-10-30" || parsed.Title != "1 fix" {
		t.Errorf("header = %+v", parsed)
	}
	if len(parsed.Sections) != 1 || len(parsed.Sections[0].Items) != 1 {
		t.Fatalf("sections = %+v", parsed.Sections)
	}
	if got, want := parsed.Sections[0].Items[0].Title, `"quoted": a # b \ c {x} <y> & z: done`; got != want {
		t.Errorf("title = %q, want %q", got, want)
	}
}

// End to end against a real repository: first-parent only, the previous tag
// excluded, an existing file refused unless --force. git on a temp dir only, so
// it is hermetic and fast.
func TestRunAgainstAGitHistory(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "feat: before the tag (#1)")
	git("tag", "v1.0.0")
	git("commit", "-q", "--allow-empty", "-m", "fix: after the tag (#2)")
	git("commit", "-q", "--allow-empty", "-m", "chore: release v1.0.1 (#3)")
	t.Chdir(dir)

	out := filepath.Join(dir, "notes.yaml")
	if err := run("v1.0.1", "v1.0.0", "HEAD", "2026-01-02", repo, out, false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	if !strings.Contains(body, `"after the tag"`) || strings.Contains(body, "before the tag") || strings.Contains(body, "release v1.0.1") {
		t.Errorf("notes:\n%s", body)
	}
	if err := run("v1.0.1", "v1.0.0", "HEAD", "2026-01-02", repo, out, false); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second run without --force: err = %v", err)
	}
	if err := run("v1.0.1", "v1.0.0", "HEAD", "2026-01-02", repo, out, true); err != nil {
		t.Errorf("--force: %v", err)
	}
	if err := run("1.0.1", "v1.0.0", "HEAD", "", repo, out, true); err == nil {
		t.Error("a version without the v was accepted")
	}
}
