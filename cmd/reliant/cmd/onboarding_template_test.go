// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The web app's workflows journey imports this YAML as the user's own draft
// ("Fix my PR"). It is deliberately incomplete — the user draws review ->
// fix_until_green and adds an Approval before merge during the tour — so it
// must validate with exactly those gaps, and validate cleanly once the tour's
// edits are made.
var onboardingTemplatePath = filepath.Join("..", "..", "..", "frontends", "web", "src", "components", "Onboarding", "journeys", "fixMyPrTemplate.yaml")

// completeOnboardingTemplate applies what the tour has the user do: the edge
// from review into the loop (W2), an Approval spliced into the line into
// merge (W4) and a weekday schedule (W5).
func completeOnboardingTemplate(t *testing.T, seed string) string {
	t.Helper()
	replace := func(s, old, new string) string {
		t.Helper()
		if !strings.Contains(s, old) {
			t.Fatalf("template no longer contains %q; update this test with the template", old)
		}
		return strings.Replace(s, old, new, 1)
	}
	done := replace(seed, "edges:\n  # review -> fix_until_green", "edges:\n  - from: review\n    default: fix_until_green\n  - from: approve_merge\n    default: merge\n  #")
	done = replace(done, "      - to: merge\n", "      - to: approve_merge\n")
	done = replace(done, "  - id: report\n", "  - id: approve_merge\n    type: approval\n    args:\n      title: Merge this pull request?\n  - id: report\n")
	return done + "triggers:\n  - name: schedule\n    schedule:\n      cron: [\"0 9 * * 1-5\"]\n      timezone: Europe/London\n"
}

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

	t.Run("as seeded: only the gaps the tour leaves for the user", func(t *testing.T) {
		res := validateOnboardingTemplate(t, seed)
		if res.Valid {
			t.Fatal("the seed validated, but it should be missing review -> fix_until_green")
		}
		unreachable := map[string]bool{}
		for _, e := range res.Errors {
			if !strings.Contains(e, "is unreachable") {
				t.Errorf("unexpected error (only unreachable nodes are intended): %s", e)
				continue
			}
			for _, id := range []string{"fix_until_green", "merge", "report"} {
				if strings.Contains(e, "'"+id+"'") {
					unreachable[id] = true
				}
			}
		}
		for _, id := range []string{"fix_until_green", "merge", "report"} {
			if !unreachable[id] {
				t.Errorf("expected %s to be reported unreachable; errors: %v", id, res.Errors)
			}
		}
	})

	t.Run("after the tour's edits: valid", func(t *testing.T) {
		res := validateOnboardingTemplate(t, completeOnboardingTemplate(t, seed))
		if !res.Valid {
			t.Fatalf("the completed Fix my PR does not validate:\n  %s", strings.Join(res.Errors, "\n  "))
		}
	})
}
