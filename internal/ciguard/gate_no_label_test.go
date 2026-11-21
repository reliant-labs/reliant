package ciguard

import (
	"os"
	"regexp"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// TestGateIsNeverPassedByALabel holds `CI gate` to the owner's rule
// (2026-11-20): a pull request's approval is a check result, never a label.
// The gate job reads no pull request label, has no permission to, and its
// "Every job passed" step exits non-zero on any failed or cancelled job with
// no override in between. The `ci:main-red` label override it replaces let
// anyone who could label a pull request turn a red gate green.
func TestGateIsNeverPassedByALabel(t *testing.T) {
	raw, err := os.ReadFile(ciWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
			Steps       []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
				If   string `yaml:"if"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	gate, ok := wf.Jobs["gate"]
	if !ok {
		t.Fatalf("%s has no gate job", ciWorkflow)
	}
	if p := gate.Permissions["pull-requests"]; p != "" {
		t.Errorf("CI gate has pull-requests: %s; it reads no pull request, so a label can never decide it", p)
	}
	labelRead := regexp.MustCompile(`(?i)labels?\b|ci-main-red`)
	var every string
	for _, s := range gate.Steps {
		if labelRead.MatchString(s.Run) || labelRead.MatchString(s.If) {
			t.Errorf("CI gate step %q reads a label (or the retired main-red override): %q", s.Name, strings.TrimSpace(s.Run+s.If))
		}
		if s.Name == "Every job passed" {
			every = s.Run
		}
	}
	if every == "" {
		t.Fatal(`CI gate has no "Every job passed" step`)
	}
	// After the red list is printed, the step must fail: nothing between the
	// listing and the final exit may return 0.
	i := strings.Index(every, `echo "not every job passed:"`)
	if i < 0 {
		t.Fatal(`"Every job passed" no longer prints the red jobs; update this guard with the new shape`)
	}
	tail := every[i:]
	if strings.Contains(tail, "exit 0") || !strings.Contains(tail, "exit 1") {
		t.Errorf("\"Every job passed\" can pass a red run:\n%s", tail)
	}
	if _, err := os.Stat("../../scripts/ci-main-red.sh"); err == nil {
		t.Error("scripts/ci-main-red.sh is back: no label may turn CI gate green")
	}
}
