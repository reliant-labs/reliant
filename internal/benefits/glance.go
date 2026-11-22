package benefits

import (
	"fmt"
	"strings"
)

// The renders below are the agent-facing digests of the registry: the
// glance block the entry skills and `forge start app` open with, and the
// lists the `compare` skill quotes. They are rendered when a skill is loaded
// (internal/cli expands `<!-- @benefits ... -->` directives), so a skill can
// never state a benefit the registry does not hold, or keep one it dropped.
//
// Commands are spelled with a bare `forge `; the skill renderer rewrites
// them to the name the binary is mounted under (`reliant forge`).

// GlanceHeading is the heading every glance block opens with.
const GlanceHeading = "## What this project gets with no setup"

// RenderGlance renders the glance block for audience (GlanceApp or
// GlanceSite): the heading, one line per fact with the command that shows
// it, and where the checked list and the honest comparison are.
func RenderGlance(r *Registry, audience string) (string, error) {
	facts, ok := r.Glance[audience]
	if !ok {
		return "", fmt.Errorf("benefits.yaml has no glance for %q (have: %s)", audience, glanceAudiences(r))
	}
	var b strings.Builder
	b.WriteString(GlanceHeading + "\n\n")
	for _, f := range facts {
		fmt.Fprintf(&b, "- %s: `forge %s`\n", strings.TrimSpace(f.Fact), f.Show)
	}
	b.WriteString("\nAll checked against this binary (`forge project benefits --all`). Limits and\n" +
		"prices beside Vercel, Supabase or Render: `forge skill load compare`.\n")
	return b.String(), nil
}

// RenderClaims renders the named benefits as a markdown list, each its title
// and claim, in the order given. An unknown or in-progress id is an error.
func RenderClaims(r *Registry, ids []string) (string, error) {
	if len(ids) == 0 {
		return "", fmt.Errorf("name at least one benefit id")
	}
	var b strings.Builder
	for _, id := range ids {
		e, ok := r.Benefit(id)
		if !ok {
			return "", fmt.Errorf("no benefit %q in benefits.yaml", id)
		}
		if e.Status == StatusInProgress {
			return "", fmt.Errorf("benefit %q is in-progress; only shipped benefits are quoted", id)
		}
		fmt.Fprintf(&b, "- **%s.** %s\n", e.Title, withStop(oneLine(e.Claim)))
	}
	return b.String(), nil
}

// RenderLimits renders every limit as a markdown list: its title, the limit,
// and what to do instead when it has one.
func RenderLimits(r *Registry) (string, error) {
	if len(r.Limits) == 0 {
		return "", fmt.Errorf("benefits.yaml declares no limits")
	}
	var b strings.Builder
	for _, l := range r.Limits {
		fmt.Fprintf(&b, "- **%s.** %s", l.Title, withStop(oneLine(l.Claim)))
		if l.Instead != "" {
			fmt.Fprintf(&b, " Instead: %s", withStop(oneLine(l.Instead)))
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// RenderDirective renders one `@benefits` directive's arguments:
//
//	glance <app|site>     RenderGlance
//	claims <id> [<id>…]   RenderClaims
//	limits                RenderLimits
func RenderDirective(r *Registry, args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("empty @benefits directive; want glance <audience>, claims <id>… or limits")
	}
	switch kind, rest := args[0], args[1:]; kind {
	case "glance":
		if len(rest) != 1 {
			return "", fmt.Errorf("@benefits glance takes one audience (%s)", glanceAudiences(r))
		}
		return RenderGlance(r, rest[0])
	case "claims":
		return RenderClaims(r, rest)
	case "limits":
		if len(rest) != 0 {
			return "", fmt.Errorf("@benefits limits takes no arguments")
		}
		return RenderLimits(r)
	default:
		return "", fmt.Errorf("unknown @benefits directive %q; want glance, claims or limits", kind)
	}
}

func glanceAudiences(r *Registry) string {
	var out []string
	for _, a := range []string{GlanceApp, GlanceSite} {
		if _, ok := r.Glance[a]; ok {
			out = append(out, a)
		}
	}
	return strings.Join(out, ", ")
}
