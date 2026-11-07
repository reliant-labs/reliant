package ciguard

import (
	"path/filepath"
	"strings"
	"testing"
)

// scripts/ci-nightly-issue.sh keeps one "Nightly CI is red" issue: found by
// its label in any state (a closed one is reopened), commented only when the
// set of red jobs changes, and closed by a green nightly. #948 collected a
// comment for every red night, identical for twelve days.
func TestNightlyIssueIsOneRollingThread(t *testing.T) {
	script := filepath.Join("..", "..", "scripts", "ci-nightly-issue.sh")
	red := `{"e2e":{"result":"failure"},"e2e-scaffold":{"result":"success"},"consumer-canary":{"result":"cancelled"}}`
	green := `{"e2e":{"result":"success"},"consumer-canary":{"result":"success"}}`
	marker := "<!-- nightly-red: consumer-canary=cancelled,e2e=failure -->"
	run := func(t *testing.T, results string, files map[string]string) []string {
		t.Helper()
		_, calls := runWithFakeGH(t, script, files, []string{"RESULTS=" + results, "RUN_URL=https://x/runs/9"})
		var verbs []string
		for _, l := range strings.Split(calls, "\n") {
			f := strings.Fields(l)
			// A multi-line --body logs its later lines on lines of their own.
			if len(f) < 3 || f[0] != "issue" || f[1] == "list" || f[1] == "view" {
				continue
			}
			verbs = append(verbs, strings.Join(f[:3], " "))
		}
		return verbs
	}
	for _, tc := range []struct {
		name    string
		results string
		files   map[string]string
		want    string
	}{
		{"red, no issue yet: create", red, map[string]string{}, "issue create --title"},
		{"red after green: reopen, never a second issue", red,
			map[string]string{"list.json": `[{"number":948,"state":"CLOSED"}]`}, "issue reopen 948,issue comment 948"},
		{"red, same jobs as the last report: silent", red, map[string]string{
			"list.json": `[{"number":948,"state":"OPEN"}]`,
			"view.json": `{"body":"first","comments":[{"body":"old\n<!-- nightly-red: e2e=failure -->"},{"body":"` + marker + `"}]}`,
		}, ""},
		{"red, the jobs changed: comment", red, map[string]string{
			"list.json": `[{"number":948,"state":"OPEN"}]`,
			"view.json": `{"body":"first\n` + marker + `","comments":[{"body":"<!-- nightly-red: e2e=failure -->"}]}`,
		}, "issue comment 948"},
		{"red, the pre-label issue open by title: adopted", red, map[string]string{
			"search.json": `[{"number":948,"title":"Nightly CI is red","state":"OPEN"}]`,
			"view.json":   `{"body":"no marker yet","comments":[]}`,
		}, "issue edit 948,issue comment 948"},
		{"green closes the open issue", green, map[string]string{"list.json": `[{"number":948,"state":"OPEN"}]`}, "issue close 948"},
		{"green leaves a closed issue alone", green, map[string]string{"list.json": `[{"number":948,"state":"CLOSED"}]`}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(run(t, tc.results, tc.files), ","); got != tc.want {
				t.Errorf("gh calls = %q, want %q", got, tc.want)
			}
		})
	}
}
