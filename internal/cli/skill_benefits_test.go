package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/benefits"
)

// TestGlanceCommandsParse runs every glance line's command, and every
// reason's show command, through the real command tree and its flag set: a
// glance or a reason tells an agent what to run to see the payoff, so a renamed verb or flag must fail here, not in front of a
// user.
func TestGlanceCommandsParse(t *testing.T) {
	reg, err := benefits.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, rs := range reg.Reasons {
		if rs.Show == "" {
			continue
		}
		root := newTestRootCmd(t)
		cmd, rest, err := root.Find(strings.Fields(rs.Show))
		if err != nil || cmd == root {
			t.Errorf("reason %s: `forge %s`: no such command (%v)", rs.ID, rs.Show, err)
			continue
		}
		if err := cmd.ParseFlags(rest); err != nil {
			t.Errorf("reason %s: `forge %s`: %v", rs.ID, rs.Show, err)
		}
	}
	for audience, facts := range reg.Glance {
		for _, f := range facts {
			words := strings.Fields(f.Show)
			root := newTestRootCmd(t)
			cmd, rest, err := root.Find(words)
			if err != nil || cmd == root {
				t.Errorf("glance %s: `forge %s`: no such command (%v)", audience, f.Show, err)
				continue
			}
			if err := cmd.ParseFlags(rest); err != nil {
				t.Errorf("glance %s: `forge %s`: %v", audience, f.Show, err)
			}
		}
	}
}

// TestShippedSkillDirectivesRender renders every forge-shipped skill that
// carries an `@benefits` directive and checks none survives: a directive the
// registry cannot render would otherwise fail `skill load` for a user.
func TestShippedSkillDirectivesRender(t *testing.T) {
	skills, err := listForgeShippedSkills()
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, m := range skills {
		raw, err := loadForgeShippedSkill(m.Path)
		if err != nil {
			t.Fatalf("%s: %v", m.Path, err)
		}
		if !strings.Contains(string(raw), benefitsDirectiveOpen) {
			continue
		}
		found++
		out, err := expandBenefitDirectives(raw)
		if err != nil {
			t.Errorf("skill %s: %v", m.Path, err)
			continue
		}
		if strings.Contains(string(out), benefitsDirectiveOpen) {
			t.Errorf("skill %s: an @benefits directive survived rendering", m.Path)
		}
	}
	if found == 0 {
		t.Fatal("no shipped skill carries an @benefits directive; the entry skills should open with a glance")
	}
}

// TestEntrySkillsOpenWithTheGlance pins that the skills an agent loads first
// render the glance near their top, before the procedure, and that the
// comparison they point at ships.
func TestEntrySkillsOpenWithTheGlance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{"forge", "greenfield", "deploy", "deploy/static-site", "deploy/hosting"} {
		out, err := RenderSkillContentAt("", name, SkillAudienceAll, "forge")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		i := strings.Index(string(out), benefits.GlanceHeading)
		if i < 0 {
			t.Errorf("skill %s does not render the glance (%q)", name, benefits.GlanceHeading)
			continue
		}
		// "Near the top": before the skill's second `## ` heading.
		if before := strings.Count(string(out[:i]), "\n## "); before > 1 {
			t.Errorf("skill %s renders the glance after %d sections; it belongs at the top", name, before)
		}
	}
	out, err := RenderSkillContentAt("", "compare", SkillAudienceAll, "forge")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Where it does not fit", "No hosted cron jobs or operators", "per vCPU-hour", "No CPU, memory or storage is included", "reliantlabs.io/pricing"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("compare skill is missing %q", want)
		}
	}
}
