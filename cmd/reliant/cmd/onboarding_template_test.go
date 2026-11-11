// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The web app's workflows journey imports this YAML as the user's own
// workflow ("Fix my PR") and walks it on the canvas: a complete example, so it
// must validate exactly as seeded — the tour asks the user to build nothing.
var onboardingTemplatePath = filepath.Join("..", "..", "..", "frontends", "web", "src", "components", "Onboarding", "journeys", "fixMyPrTemplate.yaml")

func validateOnboardingTemplate(t *testing.T, body string) validateResult {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fix-my-pr.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return validateWorkflowFile(path, dir)
}

func TestOnboardingFixMyPrTemplate(t *testing.T) {
	data, err := os.ReadFile(onboardingTemplatePath)
	if err != nil {
		t.Fatalf("read the journey's template: %v", err)
	}
	seed := string(data)

	t.Run("as seeded: valid", func(t *testing.T) {
		res := validateOnboardingTemplate(t, seed)
		if !res.Valid {
			t.Fatalf("the Fix my PR example does not validate:\n  %s", strings.Join(res.Errors, "\n  "))
		}
	})

	// fixMyPr.ts replaces the schedule's timezone with the browser's.
	t.Run("in another timezone: valid", func(t *testing.T) {
		if !strings.Contains(seed, "      timezone: UTC\n") {
			t.Fatal("the template no longer declares `timezone: UTC`; update fixMyPr.ts and this test with it")
		}
		res := validateOnboardingTemplate(t, strings.Replace(seed, "timezone: UTC", "timezone: America/New_York", 1))
		if !res.Valid {
			t.Fatalf("does not validate with a local timezone:\n  %s", strings.Join(res.Errors, "\n  "))
		}
	})
}
