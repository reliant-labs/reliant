package cli

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// Upgrade-report gap 2: v1.8.0 ships a playbook, the range crossed v1.8.0, and
// `playbooks: []` was all the agent saw — "none exist" and "checked, none
// apply" read the same. Every playbook of a release after the old pin is now
// judged once, and each that does not apply says why.
func TestEvaluatePlaybookMetas(t *testing.T) {
	metas := []migrationMeta{
		{ID: "v1.8.0", Version: "v1.8.0", Detection: "match-v1.8.0"},
		{ID: "v1.4.0", Version: "v1.4.0", Detection: "match-v1.4.0"},
		{ID: "v1.9.0", Version: "v1.9.0", Detection: "match-v1.9.0"},
		{ID: "v1.7.5", Version: "v1.7.5", Detection: "nomatch"},
		{ID: "v1.7.6", Version: "v1.7.6"},
		{ID: "v1.7.9", Version: "v1.7.9", Detection: "match-v1.7.9"},
	}
	// A target that is a pseudo-version of THIS binary's release line reaches
	// the next release's playbooks (targetReaches); pin the line elsewhere so
	// v1.9.0 is past the v1.8.1-0.… target.
	prev := upgradeLastRelease
	upgradeLastRelease = func() string { return "v1.7.0" }
	t.Cleanup(func() { upgradeLastRelease = prev })
	const from = "v1.7.1-0.20261118104051-4e92ea2af8ca"
	const to = "v1.8.1-0.20261119173027-ff5a370de464"
	state := migrationsState{Applied: map[string]string{"v1.7.9": "2026-11-18T10:00:00Z"}}
	var detected []string
	evals := evaluatePlaybookMetas(metas, from, to, state, func(script string) bool {
		detected = append(detected, script)
		return strings.HasPrefix(script, "match-")
	})

	type verdict struct {
		applies bool
		reason  string
	}
	got := map[string]verdict{}
	var order []string
	for _, ev := range evals {
		got[ev.Meta.ID] = verdict{ev.applies, ev.reason}
		order = append(order, ev.Meta.ID)
	}
	for _, tc := range []struct {
		id      string
		applies bool
		reason  string // substring; "" when it applies
	}{
		{"v1.7.5", false, "detection did not match"},
		{"v1.7.6", false, "declares no detection"},
		{"v1.7.9", false, "recorded as applied at 2026-11-18T10:00:00Z"},
		{"v1.8.0", true, ""},
		{"v1.9.0", false, "past the target"},
	} {
		v, ok := got[tc.id]
		if !ok {
			t.Errorf("%s: not evaluated", tc.id)
			continue
		}
		if v.applies != tc.applies || !strings.Contains(v.reason, tc.reason) || (tc.applies && v.reason != "") {
			t.Errorf("%s: applies=%v reason=%q, want applies=%v reason~%q", tc.id, v.applies, v.reason, tc.applies, tc.reason)
		}
	}
	if _, listed := got["v1.4.0"]; listed {
		t.Error("a playbook of a release at or before the old pin was crossed by an earlier upgrade: not listed")
	}
	if want := "v1.7.5,v1.7.6,v1.7.9,v1.8.0,v1.9.0"; strings.Join(order, ",") != want {
		t.Errorf("order = %v, want release order %s", order, want)
	}
	// Detection runs once, and only where it can decide something.
	sort.Strings(detected)
	if got := strings.Join(detected, ","); got != "match-v1.8.0,nomatch" {
		t.Errorf("detections run = %v, want exactly match-v1.8.0 and nomatch, once each", got)
	}
}

// An unknown baseline cannot say what was crossed: every playbook the target
// reaches is judged by detection alone, as `upgrade list` does.
func TestEvaluatePlaybookMetas_UnknownBaseline(t *testing.T) {
	evals := evaluatePlaybookMetas([]migrationMeta{{ID: "v0.1.44", Version: "v0.1.44", Detection: "x"}}, "", "v1.8.0",
		migrationsState{Applied: map[string]string{}}, func(string) bool { return false })
	if len(evals) != 1 || evals[0].applies || !strings.Contains(evals[0].reason, "detection did not match") {
		t.Fatalf("evals = %+v", evals)
	}
}

func TestPrintPlaybookVerdicts(t *testing.T) {
	ev := func(id, v string, applies bool, reason string) playbookEval {
		return playbookEval{pendingMigration: pendingMigration{Meta: migrationMeta{ID: id, Version: v}}, applies: applies, reason: reason}
	}
	var out bytes.Buffer
	printPlaybookVerdicts(&out, []playbookEval{
		ev("v1.7.5", "v1.7.5", true, ""),
		ev("v1.8.0", "v1.8.0", false, "detection did not match: this project does not have the old shape"),
	})
	for _, want := range []string{
		"v1.7.5: 1 playbook, 1 applies",
		"v1.8.0: 1 playbook, 0 apply (v1.8.0: detection did not match",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	printPlaybookVerdicts(&out, nil)
	if out.Len() != 0 {
		t.Errorf("no playbook in range prints nothing, got %q", out.String())
	}
}

// The applying entries keep their v1 shape (id, description, automation,
// resolved) and gain fields; a non-applying one says why.
func TestUpgradePlaybookRunJSONIsAdditive(t *testing.T) {
	doc, err := json.Marshal(upgradePlaybookRun{ID: "v1.8.0", Automation: []string{}, Reason: "detection did not match", Version: "v1.8.0",
		SkillPath: "migrations/v1.8.0", Detection: "forge-detect pre-zod4-forms"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"v1.8.0","automation":[],"resolved":false,"applies":false,"reason":"detection did not match","version":"v1.8.0","skill_path":"migrations/v1.8.0","detection":"forge-detect pre-zod4-forms"}`
	if string(doc) != want {
		t.Errorf("json = %s\nwant   %s", doc, want)
	}
}
