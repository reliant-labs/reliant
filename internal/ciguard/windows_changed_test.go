package ciguard

import (
	"os"
	"slices"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// TestWindowsChangedJobGatesTheMergeQueue holds the merge queue's Windows job
// (owner, 2026-11-13: four Windows-only reds reached main that day, each in a
// package its pull request changed) to what makes it a gate: it runs on a
// native Windows runner in the merge queue and nowhere on an unlabelled pull
// request, `CI gate` needs it and refuses a merge-queue run in which it
// skipped, it tests what scripts/ci-changed-packages.sh says the entry reaches
// under -short, it needs no other job (so it starts with the merge tier, not
// after it), and it is bounded at 20 minutes.
func TestWindowsChangedJobGatesTheMergeQueue(t *testing.T) {
	t.Parallel()
	const maxMinutes = 20
	raw, err := os.ReadFile(ciWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs map[string]struct {
			If             string   `yaml:"if"`
			RunsOn         string   `yaml:"runs-on"`
			Needs          []string `yaml:"needs"`
			TimeoutMinutes int      `yaml:"timeout-minutes"`
			Steps          []struct {
				Name string            `yaml:"name"`
				Uses string            `yaml:"uses"`
				Run  string            `yaml:"run"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	j, ok := wf.Jobs["windows-changed"]
	if !ok {
		t.Fatalf("%s has no windows-changed job; update this guard if it was renamed", ciWorkflow)
	}
	if strings.TrimSpace(j.If) != windowsQueueTier {
		t.Errorf("windows-changed runs if %q, want %q", j.If, windowsQueueTier)
	}
	if !strings.HasPrefix(j.RunsOn, "windows-") {
		t.Errorf("windows-changed runs on %q: only a native Windows run finds what it is for", j.RunsOn)
	}
	if len(j.Needs) != 0 {
		t.Errorf("windows-changed needs %v: it would start after them and lengthen every merge", j.Needs)
	}
	if j.TimeoutMinutes <= 0 || j.TimeoutMinutes > maxMinutes {
		t.Errorf("windows-changed's timeout-minutes is %d; it must be set, between 1 and %d (owner, 2026-11-13)", j.TimeoutMinutes, maxMinutes)
	}
	var plans, tests, depth bool
	for _, s := range j.Steps {
		if strings.Contains(s.Run, "scripts/ci-changed-packages.sh HEAD^1") {
			plans = true
		}
		if strings.Contains(s.Run, "go test -short ") {
			tests = true
		}
		if strings.HasPrefix(s.Uses, "actions/checkout@") && s.With["fetch-depth"] == "2" {
			depth = true
		}
		if strings.HasPrefix(s.Uses, "actions/cache/save@") || strings.HasSuffix(s.Uses, "/save-go-caches") {
			t.Errorf("windows-changed saves a cache (%s): a merge group's cache is visible to no other run", s.Uses)
		}
	}
	if !plans {
		t.Error("windows-changed never runs scripts/ci-changed-packages.sh HEAD^1")
	}
	if !tests {
		t.Error("windows-changed never runs go test -short")
	}
	if !depth {
		t.Error("windows-changed does not check out with fetch-depth: 2; HEAD^1 would not resolve")
	}
	gate := wf.Jobs["gate"]
	if !slices.Contains(gate.Needs, "windows-changed") {
		t.Error("CI gate does not need windows-changed: a Windows failure would merge")
	}
	var every string
	for _, s := range gate.Steps {
		if s.Name == "Every job passed" {
			every = s.Run
		}
	}
	if !strings.Contains(every, `.key == "windows-changed"`) {
		t.Error(`CI gate's "Every job passed" does not refuse a merge-queue run whose windows-changed job skipped`)
	}
}
