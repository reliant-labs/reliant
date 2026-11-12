package cli

import (
	"bytes"
	"strings"
	"testing"
)

// In a repo with no forge.yaml, the skills that declare `applies: any` lead
// the listing, adopt first, and are not repeated below; a sub-skill of a group
// that moved up collapses into its +N instead of being orphaned into a row.
// In a forge project the listing is unchanged.
func TestSkillListAppliesToYourRepoFirstOutsideAProject(t *testing.T) {
	skills := []skillMeta{
		{Path: "forge", Description: "entry point", Scope: SkillScopeForge, Emit: SkillEmitForge},
		{Path: "adopt", Description: "start anywhere", Scope: SkillScopeForge, Emit: SkillEmitBoth, Applies: SkillAppliesAny},
		{Path: "adopt/linters", Description: "linters", Scope: SkillScopeForge, Emit: SkillEmitBoth, Applies: SkillAppliesAny},
		{Path: "debug", Description: "debug methodology", Scope: SkillScopeForge, Emit: SkillEmitBoth, Applies: SkillAppliesAny},
		{Path: "debug/telemetry", Description: "local ClickStack", Scope: SkillScopeForge, Emit: SkillEmitForge},
		{Path: "observability", Description: "the local stack", Scope: SkillScopeForge, Emit: SkillEmitForge},
		{Path: "observability/tracing-lint", Description: "forge lint --tracing", Scope: SkillScopeForge, Emit: SkillEmitForge, Applies: SkillAppliesAny},
		{Path: "proto", Description: "proto reference", Scope: SkillScopeForge, Emit: SkillEmitForge},
	}

	var out bytes.Buffer
	if err := writeGroupedSkillsFor(&out, skills, false); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.HasPrefix(got, "APPLIES TO YOUR REPO") {
		t.Fatalf("outside a forge project the listing must open with APPLIES TO YOUR REPO:\n%s", got)
	}
	head, body, _ := strings.Cut(got, "START HERE")
	first := strings.Split(strings.TrimSpace(head), "\n")[1]
	if !strings.HasPrefix(strings.TrimSpace(first), "adopt ") || !strings.Contains(first, "+1") {
		t.Errorf("adopt must be the first applies row, with adopt/linters collapsed (+1); got %q:\n%s", first, got)
	}
	for _, want := range []string{"  debug ", "observability/tracing-lint"} {
		if !strings.Contains(head, want) {
			t.Errorf("APPLIES TO YOUR REPO lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(head, "debug ") || strings.Contains(got, "debug/telemetry") {
		t.Errorf("debug/telemetry must collapse into debug's +N, not print as a row:\n%s", got)
	}
	for _, moved := range []string{"  adopt ", "  debug "} {
		if strings.Contains(body, moved) {
			t.Errorf("%q is listed again below the applies group:\n%s", strings.TrimSpace(moved), got)
		}
	}
	if !strings.Contains(body, "  observability ") || !strings.Contains(body, "  proto ") {
		t.Errorf("the forge-project skills must stay in the sections below:\n%s", got)
	}
	if !strings.Contains(got, "8 skills in") {
		t.Errorf("the footer must count every skill:\n%s", got)
	}

	out.Reset()
	if err := writeGroupedSkillsFor(&out, skills, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "APPLIES TO YOUR REPO") {
		t.Errorf("in a forge project the listing is unchanged:\n%s", out.String())
	}
}

// The shipped catalog marks the adopt path and the playbooks that work in any
// repo, so the group is never empty, and adopt is in it.
func TestShippedSkillsDeclareAppliesAny(t *testing.T) {
	skills, err := listForgeShippedSkills()
	if err != nil {
		t.Fatal(err)
	}
	applies := map[string]bool{}
	for _, s := range skills {
		if s.Applies == SkillAppliesAny {
			applies[s.Path] = true
		} else if s.Applies != "" {
			t.Errorf("skill %q: applies %q is not \"any\" (or absent)", s.Path, s.Applies)
		}
	}
	for _, want := range []string{"adopt", "adopt/deploy", "adopt/linters", "adopt/monitoring", "adopt/libraries", "debug"} {
		if !applies[want] {
			t.Errorf("skill %q must declare applies: any", want)
		}
	}
}
