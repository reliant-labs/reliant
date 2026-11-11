package benefits

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

// The renders below are prettier-canonical markdown: both files they land in
// (docs/benefits.md and README.md) are formatted by the repo's pinned
// prettier (the pre-commit hook, `task fmt:web`), so a render prettier would
// rewrite could never be current. Concretely: one blank line between blocks,
// `-` bullets, `_` emphasis, and every table padded to its column widths with
// a delimiter row as wide as the column. Prose (claims, intros, use, why) is
// emitted verbatim, so keeping it prettier-stable is the YAML author's job;
// `task docs:benefits` checks the files with the pinned prettier after
// writing them.

// ReadmeStart and ReadmeEnd delimit the block of README.md RenderReadmeTable
// owns. Everything outside them is hand-written.
const (
	ReadmeStart = "<!-- benefits:start -->"
	ReadmeEnd   = "<!-- benefits:end -->"
)

// DocsPath and ReadmePath are the rendered files, relative to the repo root.
const (
	DocsPath   = "docs/benefits.md"
	ReadmePath = "README.md"
)

// statusLabel and availabilityLabel are the words the page's legend defines.
var statusLabel = map[Status]string{
	StatusEnforced:   "Enforced",
	StatusRecorded:   "Recorded",
	StatusInProgress: "In progress",
}

var availabilityLabel = map[Availability]string{
	AvailabilityForge:   "Forge, on any infrastructure",
	AvailabilityHosted:  "Reliant hosting",
	AvailabilityReliant: "Reliant harness",
}

// RenderReadmeTable renders the README's "Everything included" block: one
// row per category that has headline entries, each headline linked to its
// docs/benefits.md anchor, then the line pointing at the whole catalog. It is
// what goes BETWEEN the markers; SpliceReadme adds the blank lines prettier
// keeps around them.
func RenderReadmeTable(r *Registry) []byte {
	rows := [][]string{{"", "What you get"}}
	tagged := map[Availability]bool{}
	for _, c := range r.Categories {
		var cells []string
		for _, b := range r.InCategory(c.ID) {
			if !b.Headline {
				continue
			}
			cell := fmt.Sprintf("[%s](%s#%s)", b.Title, DocsPath, b.ID)
			// The README is read as "what open-source forge does", so a
			// headline that needs Reliant says so beside its title. The Host
			// row is all hosting by name and is left untagged.
			if tag := readmeTag[b.Availability]; tag != "" && c.ID != "host" {
				cell += " " + tag
				tagged[b.Availability] = true
			}
			cells = append(cells, cell)
		}
		if len(cells) > 0 {
			rows = append(rows, []string{"**" + c.Title + "**", strings.Join(cells, " · ")})
		}
	}
	var buf bytes.Buffer
	writeTable(&buf, rows)
	var legend []string
	if tagged[AvailabilityHosted] {
		legend = append(legend, readmeTag[AvailabilityHosted]+" needs Reliant hosting")
	}
	if tagged[AvailabilityReliant] {
		legend = append(legend, readmeTag[AvailabilityReliant]+" needs the Reliant harness")
	}
	if len(legend) > 0 {
		fmt.Fprintf(&buf, "\n%s; everything else is open-source Forge on any infrastructure.\n", strings.Join(legend, ", "))
	}
	// No entry or rule count here: every PR that adds a benefit changed the
	// same line, so any two of them conflicted and a merge group of two went
	// red on TestRenderedFilesAreCurrent (forge's queue, 2026-11-11).
	fmt.Fprintf(&buf, "\nEvery benefit, with how to use it and the code that backs it: [%s](%s)\n",
		DocsPath, DocsPath)
	return buf.Bytes()
}

// readmeTag marks a README headline that open-source forge alone does not
// give you. `_…_` is the emphasis spelling prettier keeps.
var readmeTag = map[Availability]string{
	AvailabilityHosted:  "_(hosted)_",
	AvailabilityReliant: "_(Reliant)_",
}

// SpliceReadme replaces the block between ReadmeStart and ReadmeEnd with the
// rendered table. found is false when the markers are absent (or out of
// order), in which case readme is returned unchanged.
func SpliceReadme(readme []byte, r *Registry) (out []byte, found bool) {
	s := string(readme)
	i := strings.Index(s, ReadmeStart)
	j := strings.Index(s, ReadmeEnd)
	if i < 0 || j < i {
		return readme, false
	}
	var buf bytes.Buffer
	buf.WriteString(s[:i+len(ReadmeStart)])
	buf.WriteString("\n\n")
	buf.Write(RenderReadmeTable(r))
	buf.WriteString("\n")
	buf.WriteString(s[j:])
	return buf.Bytes(), true
}

// RenderMarkdown renders the whole of docs/benefits.md.
func RenderMarkdown(r *Registry) []byte {
	var buf bytes.Buffer
	p := func(format string, args ...any) { fmt.Fprintf(&buf, format, args...) }

	p("<!-- Generated from internal/benefits/benefits.yaml by `task docs:benefits`; edit the YAML, not this file.\n\n" +
		"How this page stays true:\n\n" +
		"- A PR that adds a user-visible benefit adds its entry to benefits.yaml in the\n" +
		"  same PR and runs `task docs:benefits`, which rewrites this page and the\n" +
		"  README's \"Everything included\" table. The PR title is the changelog entry;\n" +
		"  never edit CHANGELOG.md.\n" +
		"- TestRenderedFilesAreCurrent (internal/benefits) fails while this page or the\n" +
		"  README block differs from a fresh render.\n" +
		"- TestBenefitsEvidenceExists (internal/cli) fails an entry whose command, lint\n" +
		"  lane, marker, skill, library or file no longer exists.\n" +
		"- TestEveryGradableRuleIsABenefit (internal/cli) fails when a rule lint.rules\n" +
		"  can grade is in no entry's rules list, or a listed rule is not a real one, so\n" +
		"  an analyzer cannot merge without the bug class it prevents.\n" +
		"- Validate (internal/benefits) refuses an in-progress headline, a title over\n" +
		"  six words and an evidence item that is not exactly one kind.\n" +
		"-->\n\n")
	p("# What Forge gives you\n\n")
	p("A catalog of what Forge does for a project. Every entry says what the\n" +
		"benefit is, how you use it, and names the evidence that backs it: a\n" +
		"command, a lint lane, a marker, a skill, a library or a file. A test\n" +
		"resolves every piece of evidence against the forge binary and this\n" +
		"repository, so an entry whose backing code is removed fails the build\n" +
		"instead of going stale. It is not a tutorial:\n" +
		"[getting-started](getting-started.md) walks the first project and\n" +
		"[concepts](concepts.md) explains why Forge is shaped the way it is.\n\n")
	p("This page is generated from `internal/benefits/benefits.yaml`: to change\n" +
		"it, edit the YAML and run `task docs:benefits`.\n\n")
	p("**Status words.** \"Enforced\" means something refuses the action.\n" +
		"\"Recorded\" means Forge writes the declaration down and nothing refuses\n" +
		"yet. \"In progress\" means designed but not shipped. The page uses these\n" +
		"words deliberately, because a catalog that blurs them is marketing.\n\n")
	p("**Availability.** \"Forge\" means open-source forge on any infrastructure.\n" +
		"\"Reliant hosting\" means the benefit needs a project deployed to Reliant\n" +
		"hosting. \"Reliant harness\" means it needs the Reliant agent harness.\n\n")

	var cats []Category
	for _, c := range r.Categories {
		if len(r.InCategory(c.ID)) > 0 {
			cats = append(cats, c)
		}
	}
	p("## Contents\n\n")
	for i, c := range cats {
		p("%d. [%s](#%s)\n", i+1, c.Title, c.ID)
	}
	p("\n")

	for _, c := range cats {
		p("---\n\n")
		p("<a id=\"%s\"></a>\n\n", c.ID)
		p("## %s\n\n", c.Title)
		p("%s\n\n", oneLine(c.Intro))
		for _, b := range r.InCategory(c.ID) {
			renderEntry(&buf, b)
		}
	}
	// Each entry ends with a blank line; the file ends with one newline.
	return append(bytes.TrimRight(buf.Bytes(), "\n"), '\n')
}

func renderEntry(buf *bytes.Buffer, b Benefit) {
	p := func(format string, args ...any) { fmt.Fprintf(buf, format, args...) }
	p("<a id=\"%s\"></a>\n\n", b.ID)
	p("### %s\n\n", b.Title)
	p("%s\n\n", oneLine(b.Claim))
	p("_%s · %s_\n\n", statusLabel[b.Status], availabilityLabel[b.Availability])
	if b.Use != "" {
		p("**Use:** %s\n\n", oneLine(b.Use))
	}
	if b.Why != "" {
		p("**Why:** %s\n\n", oneLine(b.Why))
	}
	if b.Docs != "" {
		p("**Docs:** <%s>\n\n", b.Docs)
	}
	p("**Evidence:**\n\n")
	for _, e := range b.Evidence {
		p("- %s\n", evidenceLine(e))
	}
	p("\n")
	if len(b.Rules) > 0 {
		rows := [][]string{{"Rule", "Bug class it prevents", "Lane"}}
		for _, r := range b.Rules {
			rows = append(rows, []string{"`" + r.ID + "`", oneLine(r.BugClass), laneCell(r.Lane)})
		}
		writeTable(buf, rows)
		p("\n")
	}
}

// evidenceLine renders one evidence item as a list entry. Files and
// libraries link relative to docs/, where the page lives.
func evidenceLine(e Evidence) string {
	kind, v := e.Kind()
	switch kind {
	case KindCommand:
		return fmt.Sprintf("command `forge %s`", v)
	case KindAnalyzer:
		return fmt.Sprintf("lint lane `forge lint %s`", v)
	case KindMarker:
		return fmt.Sprintf("marker `// %s`", v)
	case KindSkill:
		return "skill `" + v + "` (`forge skill load " + v + "`)"
	case KindLibrary:
		return "library [`" + v + "`](../" + v + ")"
	case KindPath:
		return "source [`" + v + "`](../" + v + ")"
	case KindExternal:
		repo, p, _ := strings.Cut(v, ":")
		return "`" + p + "` in the " + repo + " repo"
	}
	return v
}

// laneCell renders a rule's lane: the flag that runs it alone, or, for a rule
// no flag selects, the bare `forge lint` that runs it with everything else.
func laneCell(lane string) string {
	if lane == "" {
		return "bare `forge lint`"
	}
	return "`" + lane + "`"
}

// oneLine collapses authored prose onto one line. YAML folded scalars already
// are; a literal block or a stray newline would otherwise split a paragraph
// in a way prettier preserves but a table cell cannot hold.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// writeTable writes rows (the first is the header) as a prettier-aligned
// markdown table: every cell padded to its column's width, the delimiter row
// as wide as the column and at least three dashes.
func writeTable(buf *bytes.Buffer, rows [][]string) {
	widths := make([]int, len(rows[0]))
	for i := range widths {
		widths[i] = 3
	}
	for _, row := range rows {
		for i, cell := range row {
			if w := utf8.RuneCountInString(cell); w > widths[i] {
				widths[i] = w
			}
		}
	}
	line := func(cells []string) {
		buf.WriteString("|")
		for i, cell := range cells {
			buf.WriteString(" " + cell + strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell)) + " |")
		}
		buf.WriteString("\n")
	}
	line(rows[0])
	dashes := make([]string, len(widths))
	for i, w := range widths {
		dashes[i] = strings.Repeat("-", w)
	}
	line(dashes)
	for _, row := range rows[1:] {
		line(row)
	}
}
