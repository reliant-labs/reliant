package ciguard

import (
	"os"
	"path/filepath"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// TestE2EJobsAreBounded keeps a hung E2E or nightly job from holding main's
// concurrency group for GitHub's six-hour default. Those groups never cancel
// in progress on main, so one stuck run makes every later push run wait
// `pending` and then be cancelled: on 2026-11-19/20 a `forge generate` that
// deadlocked on its own heavy slot hung E2E Scaffold for 6h (runs 44694168186,
// 44798601472) and kept forge main E2E red for a day. Each bound is a few
// times the job's worst observed run.
func TestE2EJobsAreBounded(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		file, job  string
		maxMinutes int
	}{
		{"e2e-scaffold.yml", "scaffold", 60}, // ~4 min, 12 at worst
		{"e2e-suite.yml", "prepare", 30},     // ~2 min
		{"e2e-suite.yml", "suite", 90},       // the shards
		{"e2e-suite.yml", "ledger", 15},      // reads the shards' results
		{"nightly.yml", "windows-arm64-vet", 45},
		{"nightly.yml", "report", 15}, // files or closes one issue
	} {
		path := filepath.Join("..", "..", ".github", "workflows", c.file)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var wf workflow
		if err := yaml.Unmarshal(raw, &wf); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		j, ok := wf.Jobs[c.job]
		if !ok {
			t.Errorf("%s has no %s job; update this guard if it was renamed", c.file, c.job)
			continue
		}
		if j.TimeoutMinutes <= 0 || j.TimeoutMinutes > c.maxMinutes {
			t.Errorf("%s's %s job has timeout-minutes %d; it must be set, between 1 and %d, "+
				"or a hang holds main's concurrency group for GitHub's 6-hour default", c.file, c.job, j.TimeoutMinutes, c.maxMinutes)
		}
	}
}
