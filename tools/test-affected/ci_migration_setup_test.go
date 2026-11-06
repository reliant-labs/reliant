package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMigrationOrderColdSetupBudget(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, ".github/workflows/ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs map[string]struct {
			Timeout int      `yaml:"timeout-minutes"`
			Steps   []ciStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	job, ok := wf.Jobs["migration-order"]
	if !ok {
		t.Fatal("missing migration-order job")
	}
	if job.Timeout < 15 {
		t.Fatalf("migration job budget %dm cannot accommodate cold setup", job.Timeout)
	}
	setup, check := false, false
	for _, step := range job.Steps {
		if step.Uses == "./.github/actions/setup" {
			setup = true
			for _, key := range []string{"go-build-cache", "postgres-cache"} {
				if step.With[key] != "false" {
					t.Errorf("migration setup must disable %s, got %v", key, step.With[key])
				}
			}
		}
		if strings.Contains(step.Run, "task ci:migration-order") {
			check = true
		}
	}
	if !setup || !check {
		t.Fatal("migration setup/check must remain present")
	}
}

func TestSetupOptionalCachesPreserveDefaults(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, ".github/actions/setup/action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var action struct {
		Inputs map[string]struct {
			Default string `yaml:"default"`
		} `yaml:"inputs"`
		Runs struct {
			Steps []ciStep `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(raw, &action); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ input, name string }{
		{"go-build-cache", "Restore Go build cache"},
		{"postgres-cache", "Restore embedded Postgres binaries"},
	} {
		if action.Inputs[tc.input].Default != "true" {
			t.Errorf("%s must default true for existing callers", tc.input)
		}
		found := false
		for _, step := range action.Runs.Steps {
			if step.Name != tc.name {
				continue
			}
			found = true
			want := "inputs.go == 'true' && inputs." + tc.input + " == 'true'"
			if step.If != want {
				t.Errorf("%s condition = %q, want %q", tc.name, step.If, want)
			}
		}
		if !found {
			t.Errorf("missing cache step %s", tc.name)
		}
	}
}
