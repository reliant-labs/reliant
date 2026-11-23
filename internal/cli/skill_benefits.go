package cli

// `<!-- @benefits ... -->` directives in forge-shipped skills.
//
// WHY THIS EXISTS. An agent decides whether forge is worth recommending in the
// first screen of the first skill it loads, so the entry skills open with what
// the project gets with no setup, and the `compare` skill quotes the wins,
// the limits and the prices. Written by hand into each skill, those facts
// would drift from the evidence-checked registry (internal/benefits) the
// moment either changed. A directive line is replaced, when the skill is
// resolved, by the registry's own rendering of it:
//
//	<!-- @benefits glance app -->        the glance block for an app with an API
//	<!-- @benefits glance site -->       ... for a frontend-only static site
//	<!-- @benefits claims <id> [<id>…] -->  those benefits' titles and claims
//	<!-- @benefits limits -->            what forge does not do
//
// It runs in ResolveSkillContentAt, so `skill load`, the MCP skill_load tool
// and every harness that injects a skill (cli.RenderSkill, cli.LoadSkill) see
// the same text. A directive the registry cannot render is an error, never a
// silent gap; TestShippedSkillDirectivesRender renders every shipped skill.

import (
	"bytes"
	"strings"

	"github.com/reliant-labs/forge/internal/benefits"
)

const (
	benefitsDirectiveOpen  = "<!-- @benefits "
	benefitsDirectiveClose = "-->"
)

// expandBenefitDirectives replaces every directive line in body with its
// rendering. A body with no directive is returned as is.
func expandBenefitDirectives(body []byte) ([]byte, error) {
	if !bytes.Contains(body, []byte(benefitsDirectiveOpen)) {
		return body, nil
	}
	reg, err := benefits.Load()
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(body), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		args, ok := parseBenefitsDirective(line)
		if !ok {
			out = append(out, line)
			continue
		}
		rendered, err := benefits.RenderDirective(reg, args)
		if err != nil {
			return nil, err
		}
		out = append(out, strings.TrimRight(rendered, "\n"))
	}
	return []byte(strings.Join(out, "\n")), nil
}

// parseBenefitsDirective returns the arguments of a directive line, or false
// when line is not one. A directive is a whole line.
func parseBenefitsDirective(line string) ([]string, bool) {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, benefitsDirectiveOpen) || !strings.HasSuffix(s, benefitsDirectiveClose) {
		return nil, false
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, benefitsDirectiveOpen), benefitsDirectiveClose)
	return strings.Fields(s), true
}
