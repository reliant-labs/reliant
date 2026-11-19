package benefits

import (
	"strings"
	"testing"
)

const glanceBase = validCategories + `benefits:
  - {id: shipped, category: build, title: T, claim: It ships., availability: forge, evidence: [{path: go.mod}]}
  - {id: later, category: build, title: L, claim: Not yet., status: in-progress, availability: forge, evidence: [{path: go.mod}]}
`

func TestParseRejectsBadGlanceAndLimits(t *testing.T) {
	good := glanceBase + "glance:\n  app:\n    - {fact: Traced, show: obs endpoints dev, benefits: [shipped]}\n" +
		"limits:\n  - {id: no-x, title: No X, claim: It does not do X., evidence: [{path: go.mod}]}\n"
	if _, err := Parse([]byte(good)); err != nil {
		t.Fatalf("the baseline registry must parse: %v", err)
	}
	cases := []struct{ name, yaml, wantErr string }{
		{"unknown audience", glanceBase + "glance:\n  cli:\n    - {fact: F, show: lint, benefits: [shipped]}\n", `glance.cli: audience "cli" is not one of app, site`},
		{"empty fact", glanceBase + "glance:\n  app:\n    - {fact: ' ', show: lint, benefits: [shipped]}\n", "glance.app[0]: fact is empty"},
		{"fact too long", glanceBase + "glance:\n  app:\n    - {fact: " + strings.Repeat("x", 121) + ", show: lint, benefits: [shipped]}\n", "at most 120"},
		{"no show", glanceBase + "glance:\n  app:\n    - {fact: F, benefits: [shipped]}\n", "glance.app[0]: show is empty"},
		{"show spells forge", glanceBase + "glance:\n  app:\n    - {fact: F, show: forge lint, benefits: [shipped]}\n", "without the leading `forge`"},
		{"no benefits", glanceBase + "glance:\n  app:\n    - {fact: F, show: lint}\n", "glance.app[0]: benefits is empty"},
		{"unknown benefit", glanceBase + "glance:\n  app:\n    - {fact: F, show: lint, benefits: [nope]}\n", `benefit "nope" is not a benefits[].id`},
		{"in-progress benefit", glanceBase + "glance:\n  app:\n    - {fact: F, show: lint, benefits: [later]}\n", `benefit "later" is in-progress`},
		{"too many facts", glanceBase + "glance:\n  app:\n" + strings.Repeat("    - {fact: F, show: lint, benefits: [shipped]}\n", 7), "7 facts; a glance holds 1 to 6"},
		{"limit collides with a benefit", glanceBase + "limits:\n  - {id: shipped, title: T, claim: C, evidence: [{path: go.mod}]}\n", "collides with a benefit"},
		{"limit without evidence", glanceBase + "limits:\n  - {id: no-x, title: T, claim: C}\n", `limits[0] "no-x": no evidence`},
		{"limit without claim", glanceBase + "limits:\n  - {id: no-x, title: T, evidence: [{path: go.mod}]}\n", `limits[0] "no-x": claim is empty`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("Parse accepted it; want an error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v\nwant it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// The embedded registry carries both glance blocks the skills render, and
// limits for the comparison: a skill directive naming either must never fail
// at load time.
func TestEmbeddedRegistryHasGlanceAndLimits(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, audience := range []string{GlanceApp, GlanceSite} {
		out, err := RenderGlance(r, audience)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Count(strings.TrimSpace(out), "\n") + 1
		// Heading, blank, at most six facts, blank, three pointer lines.
		if lines > 2+maxGlanceFacts+1+3 {
			t.Errorf("glance %s renders %d lines; keep it short", audience, lines)
		}
		if !strings.HasPrefix(out, GlanceHeading) || !strings.Contains(out, "`forge skill load compare`") {
			t.Errorf("glance %s lost its heading or its pointer to the comparison:\n%s", audience, out)
		}
	}
	if _, err := RenderLimits(r); err != nil {
		t.Fatal(err)
	}
}

func TestRenderDirective(t *testing.T) {
	r, err := Parse([]byte(glanceBase +
		"glance:\n  app:\n    - {fact: Every RPC traced, show: obs endpoints dev, benefits: [shipped]}\n" +
		"limits:\n  - {id: no-x, title: No X, claim: It does not do X, instead: do Y, evidence: [{path: go.mod}]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"glance", "app"}, "## What this project gets with no setup\n\n- Every RPC traced: `forge obs endpoints dev`\n"},
		{[]string{"claims", "shipped"}, "- **T.** It ships.\n"},
		{[]string{"limits"}, "- **No X.** It does not do X. Instead: do Y.\n"},
	} {
		got, err := RenderDirective(r, tc.args)
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if !strings.HasPrefix(got, tc.want) {
			t.Errorf("%v rendered\n%s\nwant it to start with\n%s", tc.args, got, tc.want)
		}
	}
	for _, bad := range [][]string{nil, {"glance"}, {"glance", "site"}, {"claims"}, {"claims", "later"}, {"claims", "nope"}, {"limits", "x"}, {"pitch"}} {
		if _, err := RenderDirective(r, bad); err == nil {
			t.Errorf("RenderDirective(%q) succeeded; want an error", bad)
		}
	}
}
