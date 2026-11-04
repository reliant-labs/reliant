package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@example.com", "-c", "user.name=t"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeWorkflow(t *testing.T, dir, file, yaml string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
}

const breaksNodes = `entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

// A change to the declared triggers that existing activations cannot follow
// is reported against the revision it changes from: the merge steward's
// deletion left an enabled activation BROKEN while every file still
// validated.
func TestActivationBreaksSinceBase(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".reliant", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "init", "-q")
	writeWorkflow(t, dir, "steward.yaml", "name: steward\ntriggers:\n  - name: tick\n    schedule: {cron: \"*/5 * * * *\"}\n    prompt: go\n"+breaksNodes)
	writeWorkflow(t, dir, "plain.yaml", "name: plain\n"+breaksNodes)
	writeWorkflow(t, dir, "review.yaml", `name: review
triggers:
  - name: pr
    schedule: {cron: "0 9 * * *"}
    prompt: go
  - name: hook
    webhook: {}
    prompt: go
  - name: kept
    schedule: {cron: "0 1 * * *"}
    prompt: go
`+breaksNodes)
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-qm", "base")

	// The change: delete steward and plain, rename pr, turn hook into a
	// schedule, and edit kept in a way an activation follows.
	for _, f := range []string{"steward.yaml", "plain.yaml"} {
		if err := os.Remove(filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
	}
	writeWorkflow(t, dir, "review.yaml", `name: review
triggers:
  - name: pull-request
    schedule: {cron: "0 9 * * *"}
    prompt: go
  - name: hook
    schedule: {cron: "0 2 * * *"}
    prompt: go
  - name: kept
    schedule: {cron: "0 3 * * *"}
    prompt: changed
`+breaksNodes)

	layout, err := readWorkflowLayout(dir)
	if err != nil {
		t.Fatal(err)
	}
	byName, deleted, err := activationBreaks(dir, "HEAD", layout)
	if err != nil {
		t.Fatal(err)
	}

	review := strings.Join(byName["review"], "\n")
	for _, want := range []string{`trigger "pr" is gone`, `"pull-request"`, `trigger "hook" changed from a webhook to a schedule trigger`} {
		if !strings.Contains(review, want) {
			t.Errorf("review's breaks do not say %q:\n%s", want, review)
		}
	}
	if strings.Contains(review, `"kept"`+" changed") || len(byName["review"]) != 2 {
		t.Errorf("an edit an activation follows is not a break; got %d:\n%s", len(byName["review"]), review)
	}

	if len(deleted) != 1 {
		t.Fatalf("deleted = %+v, want steward only: a deleted workflow with no triggers breaks nothing", deleted)
	}
	if deleted[0].Name != "steward" || !strings.Contains(strings.Join(deleted[0].ActivationBreaks, ""), `trigger "tick"`) {
		t.Errorf("deleted[0] = %+v", deleted[0])
	}
}

func TestActivationBreaksRejectsAnUnknownBase(t *testing.T) {
	root := t.TempDir()
	gitIn(t, root, "init", "-q")
	gitIn(t, root, "commit", "-q", "--allow-empty", "-m", "base")
	dir := filepath.Join(root, ".reliant", "workflows")
	layout, err := readWorkflowLayout(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := activationBreaks(dir, "no-such-rev", layout); err == nil || !strings.Contains(err.Error(), "not a commit") {
		t.Fatalf("err = %v, want a not-a-commit error", err)
	}
}
