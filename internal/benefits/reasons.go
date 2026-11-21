package benefits

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

// WHY REASONS EXIST. The registry is an inventory: 260-odd true sentences,
// one layer each. A skeptic does not ask "what does the Ship layer do?" but
// "why would I leave my stack?", and the honest answer is a JOIN no single
// entry states: forge knows the proto AND the migrations AND the release AND
// the telemetry, so it can do what a stitched-together stack cannot. A reason
// writes that join down once, and cites the entries that prove it, so the
// argument inherits the registry's evidence discipline instead of becoming a
// second, unchecked set of claims.
//
// Validate holds each reason to that:
//
//   - every cited id is a benefit, and none is in-progress (a reason states
//     only what ships);
//   - the benefits at or below the reason's availability span at least two
//     categories (a one-layer reason is just a benefit, and a reason tagged
//     `forge` must stand on open-source forge alone; hosted or Reliant
//     entries it also cites render with their own tag);
//   - every headline benefit is cited by some reason, so the story and the
//     inventory cannot drift apart;
//   - the mechanism says "Because …", the shape that makes it a reason.

// maxReasons keeps the argument short enough to read: the README lists every
// reason, and an agent quotes them.
const maxReasons = 12

// Length bounds, in bytes, after collapsing whitespace. A title is a README
// list item and a terminal line; a thesis is a short paragraph; a mechanism
// is one sentence.
const (
	maxReasonTitleLen     = 100
	maxReasonThesisLen    = 800
	maxReasonMechanismLen = 400
)

// mechanismPrefix opens every mechanism: "Because forge knows X, it can Y."
const mechanismPrefix = "Because "

// Reason is one answer to "why forge": a cross-layer outcome and the
// benefits that back it.
type Reason struct {
	// ID is kebab-case and unique: the docs/why-forge.md anchor and the
	// `forge why <id>` argument.
	ID string `yaml:"id" json:"id"`
	// Title is the outcome, worded to be searched for, not to be clever.
	Title string `yaml:"title" json:"title"`
	// Thesis is a short paragraph for a skeptical senior engineer: the
	// problem a stitched stack has, and what forge does about it.
	Thesis string `yaml:"thesis" json:"thesis"`
	// Mechanism is "Because forge knows X, it can Y": why the outcome
	// follows from forge owning the seams.
	Mechanism string `yaml:"mechanism" json:"mechanism"`
	// Availability is what the reason needs to hold, on the benefits'
	// scale: forge < hosted < reliant.
	Availability Availability `yaml:"availability" json:"availability"`
	// Show is the command that demonstrates it, a path under the `forge`
	// root with its arguments (optional; checked like a glance's).
	Show string `yaml:"show,omitempty" json:"show,omitempty"`
	// Benefits are the ids of the entries that prove it, in the order the
	// thesis uses them.
	Benefits []string `yaml:"benefits" json:"benefits"`
}

// availabilityRank orders availability: a reason tagged forge stands on
// forge entries alone; one tagged hosted may also stand on hosted ones.
var availabilityRank = map[Availability]int{
	AvailabilityForge:   0,
	AvailabilityHosted:  1,
	AvailabilityReliant: 2,
}

// reservedWhyAnchors are docs/why-forge.md's own headings.
var reservedWhyAnchors = map[string]bool{"contents": true, "limits": true}

func (r *Registry) validateReasons(fail func(format string, args ...any)) {
	if len(r.Reasons) == 0 {
		return
	}
	if len(r.Reasons) > maxReasons {
		fail("reasons: %d reasons; at most %d (the README lists every one)", len(r.Reasons), maxReasons)
	}
	byID := map[string]Benefit{}
	for _, b := range r.Benefits {
		byID[b.ID] = b
	}
	seen := map[string]bool{}
	cited := map[string]bool{}
	for i, rs := range r.Reasons {
		where := fmt.Sprintf("reasons[%d] %q", i, rs.ID)
		switch {
		case !kebabID.MatchString(rs.ID):
			fail("%s: id must be kebab-case", where)
		case seen[rs.ID]:
			fail("%s: duplicate reason id", where)
		case reservedWhyAnchors[rs.ID]:
			fail("%s: id %q is reserved for a docs/why-forge.md heading", where, rs.ID)
		}
		seen[rs.ID] = true
		validateProse(where, "title", rs.Title, maxReasonTitleLen, fail)
		if strings.ContainsAny(rs.Title, "|\n") {
			fail("%s: title may not contain '|' or a newline (it is a list item and a terminal line)", where)
		}
		validateProse(where, "thesis", rs.Thesis, maxReasonThesisLen, fail)
		validateProse(where, "mechanism", rs.Mechanism, maxReasonMechanismLen, fail)
		if m := oneLine(rs.Mechanism); m != "" && !strings.HasPrefix(m, mechanismPrefix) {
			fail("%s: mechanism must start with %q (\"Because forge knows X, it can Y\")", where, mechanismPrefix)
		}
		rank, ok := availabilityRank[rs.Availability]
		if !ok {
			fail("%s: availability %q is not one of forge, hosted, reliant", where, rs.Availability)
		}
		if rs.Show != "" {
			if msg := validateEvidence(Evidence{Command: rs.Show}); msg != "" {
				fail("%s: show: %s", where, msg)
			}
		}
		if len(rs.Benefits) < 2 {
			fail("%s: %d benefits; a reason cites at least 2", where, len(rs.Benefits))
		}
		inReason := map[string]bool{}
		backing := map[string]bool{} // categories of the benefits at or below the reason's tier
		for _, id := range rs.Benefits {
			b, ok := byID[id]
			switch {
			case !ok:
				fail("%s: benefit %q is not a benefits[].id", where, id)
				continue
			case inReason[id]:
				fail("%s: benefit %q is cited twice", where, id)
			case b.Status == StatusInProgress:
				fail("%s: benefit %q is in-progress; a reason states only what ships", where, id)
			}
			inReason[id] = true
			cited[id] = true
			if availabilityRank[b.Availability] <= rank {
				backing[b.Category] = true
			}
		}
		if ok && len(backing) < 2 {
			fail("%s: the benefits it cites at availability %q or below span %d categories; a reason spans at least 2 (one layer is a benefit, not a reason)",
				where, rs.Availability, len(backing))
		}
	}
	for _, b := range r.Benefits {
		if b.Headline && !cited[b.ID] {
			fail("benefits %q: a headline no reason cites; add it to the reason it proves", b.ID)
		}
	}
}

// validateProse fails an empty or over-long field.
func validateProse(where, field, s string, max int, fail func(format string, args ...any)) {
	switch n := len(oneLine(s)); {
	case n == 0:
		fail("%s: %s is empty", where, field)
	case n > max:
		fail("%s: %s is %d bytes; at most %d", where, field, n, max)
	}
}

// Reason returns the reason with id.
func (r *Registry) Reason(id string) (Reason, bool) {
	for _, rs := range r.Reasons {
		if rs.ID == id {
			return rs, true
		}
	}
	return Reason{}, false
}

// ReasonIDs lists every reason id, in order.
func (r *Registry) ReasonIDs() []string {
	ids := make([]string, len(r.Reasons))
	for i, rs := range r.Reasons {
		ids[i] = rs.ID
	}
	return ids
}

// WhyPath is the rendered reasons page, relative to the repo root.
const WhyPath = "docs/why-forge.md"

// ReasonsStart and ReasonsEnd delimit the README block RenderReadmeReasons
// owns.
const (
	ReasonsStart = "<!-- reasons:start -->"
	ReasonsEnd   = "<!-- reasons:end -->"
)

// RenderReadmeReasons renders the README's "Why Forge" block: one numbered
// line per reason linked to its docs/why-forge.md anchor, then where the
// evidence is. It is what goes between the markers.
func RenderReadmeReasons(r *Registry) []byte {
	var buf bytes.Buffer
	for i, rs := range r.Reasons {
		fmt.Fprintf(&buf, "%d. [%s](%s#%s)", i+1, rs.Title, WhyPath, rs.ID)
		if tag := readmeTag[rs.Availability]; tag != "" {
			buf.WriteString(" " + tag)
		}
		buf.WriteString("\n")
	}
	fmt.Fprintf(&buf, "\nEach reason, how it works and the checked benefits that back it: [%s](%s).\n"+
		"Every benefit: [%s](%s). From a terminal or an agent: `forge why`.\n",
		WhyPath, WhyPath, DocsPath, DocsPath)
	return buf.Bytes()
}

// SpliceReadmeReasons replaces the block between ReasonsStart and ReasonsEnd
// with the rendered reasons; found is false when the markers are absent.
func SpliceReadmeReasons(readme []byte, r *Registry) (out []byte, found bool) {
	return spliceBlock(readme, ReasonsStart, ReasonsEnd, RenderReadmeReasons(r))
}

// RenderWhy renders the whole of docs/why-forge.md.
func RenderWhy(r *Registry) []byte {
	var buf bytes.Buffer
	p := func(format string, args ...any) { fmt.Fprintf(&buf, format, args...) }

	p("<!-- Generated from internal/benefits/benefits.yaml (reasons) by `task docs:benefits`; edit the YAML, not this file.\n\n" +
		"How this page stays true:\n\n" +
		"- Every reason cites the benefits that prove it, and Validate\n" +
		"  (internal/benefits) refuses an unknown or in-progress one, a reason whose\n" +
		"  backing spans one category, and a headline benefit no reason cites.\n" +
		"- TestBenefitsEvidenceExists (internal/cli) resolves every cited benefit's\n" +
		"  evidence against the forge binary and this repository.\n" +
		"- TestRenderedFilesAreCurrent (internal/benefits) fails while this page\n" +
		"  differs from a fresh render.\n" +
		"-->\n\n")
	p("# Why Forge\n\n")
	p("Most stacks are stitched together: a framework from one place, CI, hosting,\n" +
		"logs, alerts and secrets from others. None of them knows what the others\n" +
		"know, so every integration is glue you write, and every agent working in\n" +
		"the repo works blind. Forge reads your services, RPCs, schema,\n" +
		"environments and releases from files you own, and builds each of its\n" +
		"tools on that one model. The reasons below are what that buys you.\n\n")
	p("Each reason names the benefits that back it. Those live in\n" +
		"[benefits.md](benefits.md), where a test resolves every piece of evidence\n" +
		"against the forge binary and this repository, so a reason cannot claim\n" +
		"what forge does not ship. A reason marked \"Reliant hosting\" needs a\n" +
		"project deployed there; every other one is open-source Forge on any\n" +
		"infrastructure, and a cited benefit that needs more says so beside it.\n" +
		"`forge why` prints the same list in a terminal, and what Forge does not do\n" +
		"is [at the end](#limits).\n\n")

	p("## Contents\n\n")
	for i, rs := range r.Reasons {
		p("%d. [%s](#%s)\n", i+1, rs.Title, rs.ID)
	}
	p("%d. [What Forge does not do](#limits)\n\n", len(r.Reasons)+1)

	for _, rs := range r.Reasons {
		p("---\n\n")
		p("<a id=\"%s\"></a>\n\n", rs.ID)
		p("## %s\n\n", rs.Title)
		p("%s\n\n", oneLine(rs.Thesis))
		p("**How:** %s\n\n", oneLine(rs.Mechanism))
		if rs.Show != "" {
			p("**See it:** `forge %s`\n\n", rs.Show)
		}
		if rs.Availability != AvailabilityForge {
			p("**Needs:** %s\n\n", availabilityLabel[rs.Availability])
		}
		p("**Backed by:**\n\n")
		for _, id := range rs.Benefits {
			b, _ := r.Benefit(id)
			line := fmt.Sprintf("- [%s](benefits.md#%s)", b.Title, b.ID)
			if tag := readmeTag[b.Availability]; tag != "" {
				line += " " + tag
			}
			p("%s\n", line)
		}
		p("\n")
	}

	p("---\n\n")
	p("<a id=\"limits\"></a>\n\n")
	p("## What Forge does not do\n\n")
	p("The trade-offs, checked like the benefits. `forge skill load compare` puts\n" +
		"them beside Vercel, Supabase and Render.\n\n")
	if limits, err := RenderLimits(r); err == nil {
		buf.WriteString(limits)
	}
	return append(bytes.TrimRight(buf.Bytes(), "\n"), '\n')
}

// spliceBlock replaces what lies between start and end in doc with body,
// with the blank lines prettier keeps around a block. found is false when
// the markers are absent or out of order; doc is then returned unchanged.
func spliceBlock(doc []byte, start, end string, body []byte) (out []byte, found bool) {
	s := string(doc)
	i := strings.Index(s, start)
	j := strings.Index(s, end)
	if i < 0 || j < i {
		return doc, false
	}
	var buf bytes.Buffer
	buf.WriteString(s[:i+len(start)])
	buf.WriteString("\n\n")
	buf.Write(body)
	buf.WriteString("\n")
	buf.WriteString(s[j:])
	return buf.Bytes(), true
}

// WrapText wraps prose to width columns, each line prefixed with indent. It
// is the terminal rendering of a thesis or a mechanism.
func WrapText(s string, width int, indent string) string {
	var b strings.Builder
	line := indent
	for _, w := range strings.Fields(s) {
		if line != indent && utf8.RuneCountInString(line)+1+utf8.RuneCountInString(w) > width {
			b.WriteString(line + "\n")
			line = indent
		}
		if line != indent {
			line += " "
		}
		line += w
	}
	if line != indent {
		b.WriteString(line + "\n")
	}
	return b.String()
}
