package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// runSkillList executes `skill list` with args and returns stdout.
func runSkillList(t *testing.T, args ...string) string {
	t.Helper()
	cmd := newSkillListCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("skill list %v: %v", args, err)
	}
	return out.String()
}

// skillListBody returns the catalog rows only — everything above the footer.
// The footer names paths on purpose (the `db/seeding` load example, the
// `--all` pointer), so assertions about what the CATALOG prints have to stop
// before it or they match forge's own advice.
func skillListBody(t *testing.T, out string) string {
	t.Helper()
	idx := strings.LastIndex(out, " skills in ")
	if idx < 0 {
		t.Fatalf("`skill list` printed no footer:\n%s", out)
	}
	return out[:strings.LastIndex(out[:idx], "\n")]
}

// TestSkillListDefaultFitsOnOneScreen is the whole point of the grouped view.
// The measured failure was an agent running `forge skill list` TWICE (head -60,
// then tail -30) because the flat table is 84 lines. A catalog you cannot read
// in one tool call is a catalog that competes with the skills themselves for
// context.
func TestSkillListDefaultFitsOnOneScreen(t *testing.T) {
	got := runSkillList(t)
	lines := strings.Count(strings.TrimRight(got, "\n"), "\n") + 1
	if lines > 55 {
		t.Errorf("default `skill list` is %d lines, want <= 55 — it must be readable in one call:\n%s", lines, got)
	}
	if lines < 10 {
		t.Errorf("default `skill list` collapsed to %d lines — it stopped being a catalog:\n%s", lines, got)
	}
}

// TestSkillListDefaultCollapsesSubSkills pins the nesting: a parent with
// children shows THAT they exist without spending a row (and a full
// description) on each one.
func TestSkillListDefaultCollapsesSubSkills(t *testing.T) {
	got := runSkillList(t)
	// Scoped to the catalog body: the footer deliberately NAMES a sub-skill
	// path as the worked example of how to load one, and that mention is the
	// opposite of the noise this test guards against.
	body := skillListBody(t, got)
	if !strings.Contains(body, "  db ") {
		t.Errorf("default listing lost the `db` parent row:\n%s", got)
	}
	for _, child := range []string{"db/seeding", "db/crud-overrides", "testing/unit", "auth/api-keys"} {
		if strings.Contains(body, child) {
			t.Errorf("default listing printed sub-skill %q as its own row — sub-skills must collapse into the parent:\n%s", child, got)
		}
	}
	// A GRANDCHILD collapses all the way to the top-level group, not into a
	// row of its own. `frontend/design/verify` belongs to `frontend`; leaving
	// it at top level puts a third-level path in the same column as `db` and
	// reintroduces exactly the row the grouping removes.
	for _, grandchild := range []string{"frontend/design/verify"} {
		if strings.Contains(body, grandchild) {
			t.Errorf("default listing printed grandchild %q as its own row — it must collapse into its top-level group:\n%s", grandchild, got)
		}
	}
	// The collapse has to be VISIBLE, or the reader concludes db has no
	// sub-skills and never looks.
	if !strings.Contains(got, "+3") {
		t.Errorf("default listing shows no sub-skill count marker (expected a `+3` beside db):\n%s", got)
	}
}

// TestSkillListDefaultClustersHyphenSiblings pins that name-siblings collapse
// the same way path-children do. `api-rest` and `api-openapi` are children of
// `api` in everything but their path spelling, and three sibling rows carrying
// three near-identical descriptions is exactly the noise the redesign removes.
func TestSkillListDefaultClustersHyphenSiblings(t *testing.T) {
	got := runSkillList(t)
	body := skillListBody(t, got)
	for _, sibling := range []string{"api-rest", "api-openapi", "frontend-testing", "proto-split"} {
		if strings.Contains(body, sibling) {
			t.Errorf("default listing printed name-sibling %q as its own row — it belongs under its prefix parent:\n%s", sibling, got)
		}
	}
}

// TestSkillListLeadsWithTheEntryPoints is the "which 3 do I load first"
// requirement: the entry skill and the greenfield walkthrough must be
// visually FIRST, not alphabetically buried between `dev` and `interactor`.
func TestSkillListLeadsWithTheEntryPoints(t *testing.T) {
	got := runSkillList(t)
	head, _, ok := strings.Cut(got, "FORGE")
	if !ok {
		t.Fatalf("default listing has no FORGE catalog section:\n%s", got)
	}
	for _, entry := range startHereSkills {
		if !strings.Contains(head, entry) {
			t.Errorf("start-here skill %q is not in the head section (before the catalog):\n%s", entry, got)
		}
	}
	// And the head must precede the alphabetical body.
	if strings.Index(got, entryPointSkillPath) > strings.Index(got, "adapter") {
		t.Errorf("entry point %q appears after `adapter` — it is still alphabetically buried:\n%s", entryPointSkillPath, got)
	}
}

// TestSkillListSeparatesMethodologyFromFramework pins the section split. The
// two are answers to different questions ("how does forge do X" vs "how do I
// review code"), and interleaving them alphabetically forces the reader to
// classify all 63 by hand.
func TestSkillListSeparatesMethodologyFromFramework(t *testing.T) {
	got := runSkillList(t)
	forgeAt := strings.Index(got, "FORGE")
	methodAt := strings.Index(got, "METHODOLOGY")
	if forgeAt < 0 || methodAt < 0 {
		t.Fatalf("default listing is missing the FORGE/METHODOLOGY sections:\n%s", got)
	}
	if forgeAt > methodAt {
		t.Errorf("METHODOLOGY precedes FORGE — framework skills are what a forge project needs first:\n%s", got)
	}
	// A general-emit skill must land under METHODOLOGY, not FORGE.
	if idx := strings.Index(got, "code-review"); idx < methodAt {
		t.Errorf("`code-review` (emit: general) rendered outside METHODOLOGY:\n%s", got)
	}
}

// TestSkillListFooterNamesSearchAndAll makes the two escape hatches
// discoverable at the exact moment the reader needs them: the collapsed view
// is not the whole catalog, and keyword lookup beats scanning.
func TestSkillListFooterNamesSearchAndAll(t *testing.T) {
	got := runSkillList(t)
	for _, want := range []string{"skill search", "--all", "skill load"} {
		if !strings.Contains(got, want) {
			t.Errorf("footer does not name %q:\n%s", want, got)
		}
	}
}

// TestSkillListAllKeepsTheExhaustiveTable proves --all is a real escape
// hatch — every path the JSON knows about, in the flat table shape.
func TestSkillListAllKeepsTheExhaustiveTable(t *testing.T) {
	got := runSkillList(t, "--all")
	if !strings.Contains(got, "PATH") || !strings.Contains(got, "SCOPE") {
		t.Errorf("--all lost the exhaustive table header:\n%s", got)
	}
	for _, child := range []string{"db/seeding", "testing/unit", "api-rest", "code-review/security-review"} {
		if !strings.Contains(got, child) {
			t.Errorf("--all is missing %q — it must print the full catalog:\n%s", child, got)
		}
	}
}

// TestSkillListJSONIsUnchanged is the compatibility guard: sub-agents parse
// this. The grouped view must not have reshaped it.
func TestSkillListJSONIsUnchanged(t *testing.T) {
	raw := runSkillList(t, "--json")
	var got []jsonSkill
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("--json no longer parses as a flat array: %v\n%s", err, raw)
	}
	want, err := listSkills()
	if err != nil {
		t.Fatalf("listSkills: %v", err)
	}
	want = filterDefaultRelevance(want)
	if len(got) != len(want) {
		t.Fatalf("--json emitted %d skills, want %d (the flat, un-collapsed set)", len(got), len(want))
	}
	seen := map[string]bool{}
	for _, s := range got {
		seen[s.Path] = true
	}
	for _, w := range want {
		if !seen[w.Path] {
			t.Errorf("--json dropped %q", w.Path)
		}
	}
}

// TestSkillListGroupExpandsSubSkills pins the discovery path for a collapsed
// sub-skill: the default view shows only `observability +N`, so the group name
// must expand to the sub-skills' full paths, and each must load by that path.
// `skill list <group>` used to ignore its argument and print the whole catalog.
func TestSkillListGroupExpandsSubSkills(t *testing.T) {
	got := runSkillList(t, "observability")
	for _, path := range []string{"observability ", "observability/query", "observability/alerts"} {
		if !strings.Contains(got, path) {
			t.Errorf("`skill list observability` does not list %q:\n%s", path, got)
		}
	}
	if strings.Contains(got, "\ndb ") || strings.Contains(got, "START HERE") {
		t.Errorf("`skill list observability` printed the whole catalog:\n%s", got)
	}

	body, err := RenderSkillContentAt("", "observability/query", SkillAudienceAll, "forge")
	if err != nil || !strings.Contains(string(body), "forge obs check") {
		t.Errorf("observability/query does not load: %v", err)
	}

	cmd := newSkillListCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"no-such-group"})
	if err := cmd.Execute(); err == nil {
		t.Error("an unknown group listed successfully")
	}
}
