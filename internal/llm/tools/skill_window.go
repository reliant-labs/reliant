// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"regexp"
	"strings"
)

// Windowed reading for action=load.
//
// A skill that exceeds MaxSkillBodySize used to be a dead end: the tool took
// only action/path/query, so the dropped tail was unreachable THROUGH THE TOOL
// and the agent's only recovery was to shell out (`skill load db | sed -n
// '80,175p'`). The mirror-image waste is just as common — an agent paging a
// skill that would have fit, because a plain result never said how big it was.
//
// Both are reachability problems, not budget problems, so the fix is here
// rather than in the ceiling: every load reports its true size and whether
// anything remains, and anything that remains is addressable by offset, by
// markdown heading, or by regex.
//
// Parameter vocabulary deliberately mirrors bash_output (offset/limit/regex/
// regex_case_insensitive/regex_context_before/regex_context_after), so an agent
// that knows one knows the other.

// skillHeadingRe matches an ATX markdown heading, capturing its level and title.
var skillHeadingRe = regexp.MustCompile(`^(#{1,6})[ \t]+(.+?)[ \t]*$`)

// maxOutlineEntries bounds the heading list in the delivery footer. The outline
// exists to give an agent a next move, not to reproduce the document.
const maxOutlineEntries = 24

// skillSection is one markdown heading and the byte range it governs.
type skillSection struct {
	Title string
	Level int
	Start int // byte offset of the heading line
	End   int // byte offset one past the section's last byte
}

// skillSections extracts the heading outline of a skill body.
//
// Headings inside fenced code blocks are ignored: skills are full of shell
// samples whose `# comment` lines would otherwise register as sections and
// make `section=` resolve to a fragment of a code fence.
func skillSections(content string) []skillSection {
	var sections []skillSection
	offset := 0
	inFence := false

	for _, raw := range strings.SplitAfter(content, "\n") {
		line := strings.TrimSuffix(raw, "\n")
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, "```"), strings.HasPrefix(trimmed, "~~~"):
			inFence = !inFence
		case !inFence:
			if m := skillHeadingRe.FindStringSubmatch(line); m != nil {
				sections = append(sections, skillSection{
					Title: strings.TrimSpace(m[2]),
					Level: len(m[1]),
					Start: offset,
				})
			}
		}
		offset += len(raw)
	}

	// A section runs until the next heading at the same or a higher level, so
	// fetching "## Seeding" delivers its "### " subsections with it rather
	// than stopping at the first nested heading.
	for i := range sections {
		sections[i].End = len(content)
		for j := i + 1; j < len(sections); j++ {
			if sections[j].Level <= sections[i].Level {
				sections[i].End = sections[j].Start
				break
			}
		}
	}
	return sections
}

// findSkillSection resolves a caller-supplied section name: case-insensitive
// exact title match first, then a unique case-insensitive substring match.
// Ambiguity and misses both name the real headings so the caller can retry.
func findSkillSection(sections []skillSection, name string) (skillSection, error) {
	want := strings.ToLower(strings.TrimSpace(name))
	if len(sections) == 0 {
		return skillSection{}, fmt.Errorf("this skill has no markdown headings, so section=%q cannot be resolved; load it without 'section' (add 'offset' to page through it)", name)
	}

	for _, s := range sections {
		if strings.ToLower(s.Title) == want {
			return s, nil
		}
	}

	var partial []skillSection
	for _, s := range sections {
		if strings.Contains(strings.ToLower(s.Title), want) {
			partial = append(partial, s)
		}
	}
	switch len(partial) {
	case 1:
		return partial[0], nil
	case 0:
		return skillSection{}, fmt.Errorf("no section named %q in this skill.\n\nSections:\n- %s",
			name, strings.Join(sectionTitles(sections, len(sections)), "\n- "))
	default:
		return skillSection{}, fmt.Errorf("section %q is ambiguous.\n\nDid you mean:\n- %s",
			name, strings.Join(sectionTitles(partial, len(partial)), "\n- "))
	}
}

// sectionTitles projects up to limit heading titles.
func sectionTitles(sections []skillSection, limit int) []string {
	out := make([]string, 0, len(sections))
	for _, s := range sections {
		if len(out) == limit {
			out = append(out, fmt.Sprintf("... and %d more", len(sections)-limit))
			break
		}
		out = append(out, s.Title)
	}
	return out
}

// skillWindow is one selected view of a skill, before size capping.
//
// Start/SpaceLen address the SELECTED space — the whole skill, one section, or
// the filtered match set — not always the raw body. This is bash_output's
// semantics: offset applies to the filtered result, and the continuation call
// repeats the selector that produced it.
type skillWindow struct {
	Text     string // the selected text, pre-cap
	Start    int    // offset of Text within the selected space
	SpaceLen int    // total length of the selected space

	Mode     string // "skill" | "section" | "filter"
	Label    string // section title or regex pattern
	Matches  int    // matching lines, filter mode only
	Sections []skillSection

	SkillBytes int // the skill's true total size, always
}

// DeliverSkillContent caps fully rendered skill content to the delivery budget
// and appends the size/has-more report, for a caller that selected no window.
//
// This is the SINGLE renderer behind both skill-delivery paths: the tool's own
// action=load and the body call_llm seeds as a preloaded skill. They must emit
// identical bytes — a skill that arrives as two different documents depending
// on how it was requested is the defect the shared cap exists to prevent.
//
// The report belongs on the preload path too. A preloaded oversize skill has
// exactly the same dropped tail as a hand-loaded one, and naming the call that
// fetches the remainder is the only way the model can act on it. The text is
// deterministic for a given skill, so the seeded prefix stays byte-stable and
// prompt-cacheable across turns.
//
// Returns the delivered text and whether any content was dropped.
func DeliverSkillContent(path, content string) (string, bool) {
	w := skillWindow{
		Text:       content,
		SpaceLen:   len(content),
		Mode:       "skill",
		Sections:   skillSections(content),
		SkillBytes: len(content),
	}
	text, delivered := renderSkillDelivery(path, w)
	return text, delivered < len(content)
}

// validateSkillWindowParams rejects combinations that cannot mean anything,
// mirroring bash_output's validateParams.
func validateSkillWindowParams(p SkillParams) error {
	if p.Section != "" && p.Regex != "" {
		return fmt.Errorf("cannot use both 'section' and 'regex' — pick one selector")
	}
	if p.Regex == "" {
		switch {
		case p.RegexCaseInsensitive:
			return fmt.Errorf("'regex_case_insensitive' requires 'regex'")
		case p.RegexContextBefore > 0:
			return fmt.Errorf("'regex_context_before' requires 'regex'")
		case p.RegexContextAfter > 0:
			return fmt.Errorf("'regex_context_after' requires 'regex'")
		}
	}
	if p.Offset < 0 {
		return fmt.Errorf("'offset' must not be negative")
	}
	if p.Limit < 0 {
		return fmt.Errorf("'limit' must not be negative")
	}
	return nil
}

// selectSkillWindow applies the caller's selector and pagination to the fully
// rendered skill content.
func selectSkillWindow(content string, p SkillParams) (skillWindow, error) {
	sections := skillSections(content)
	w := skillWindow{
		Mode:       "skill",
		Sections:   sections,
		SkillBytes: len(content),
	}

	space := content
	switch {
	case p.Regex != "":
		filtered, matches, err := filterSkillLines(content, p)
		if err != nil {
			return w, err
		}
		space, w.Mode, w.Label, w.Matches = filtered, "filter", p.Regex, matches

	case p.Section != "":
		sec, err := findSkillSection(sections, p.Section)
		if err != nil {
			return w, err
		}
		space, w.Mode, w.Label = content[sec.Start:sec.End], "section", sec.Title
	}

	w.SpaceLen = len(space)

	start := p.Offset
	if start > len(space) {
		return w, fmt.Errorf("offset %d is past the end of this view (%d bytes). Load without 'offset' to start over",
			p.Offset, len(space))
	}
	end := len(space)
	if p.Limit > 0 && start+p.Limit < end {
		end = start + p.Limit
	}

	w.Start = start
	w.Text = space[start:end]
	return w, nil
}

// filterSkillLines renders the lines of content matching the caller's regex,
// with optional surrounding context, in bash_output's rendering: '>' marks a
// match, ' ' marks a context line, and every line carries its number so the
// agent can convert a hit into a targeted offset or section read.
func filterSkillLines(content string, p SkillParams) (string, int, error) {
	pattern := p.Regex
	if p.RegexCaseInsensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", 0, fmt.Errorf("invalid regex pattern: %v", err)
	}

	lines := strings.Split(content, "\n")
	keep := make(map[int]bool, len(lines))
	isMatch := make(map[int]bool, len(lines))
	matches := 0

	for i, line := range lines {
		if !re.MatchString(line) {
			continue
		}
		matches++
		keep[i] = true
		isMatch[i] = true
		for j := 1; j <= p.RegexContextBefore; j++ {
			if i-j >= 0 {
				keep[i-j] = true
			}
		}
		for j := 1; j <= p.RegexContextAfter; j++ {
			if i+j < len(lines) {
				keep[i+j] = true
			}
		}
	}

	if matches == 0 {
		return "", 0, nil
	}

	var b strings.Builder
	for i := range lines {
		if !keep[i] {
			continue
		}
		marker := " "
		if isMatch[i] {
			marker = ">"
		}
		fmt.Fprintf(&b, "%s Line %d: %s\n", marker, i+1, lines[i])
	}
	return b.String(), matches, nil
}

// renderSkillDelivery caps the window to MaxSkillBodySize INCLUDING its footer
// and appends that footer.
//
// The cap is applied here rather than left to the ToolWrapper on purpose: the
// wrapper's generic cut would take the footer off — the one part of the result
// that tells the agent what it is missing and how to fetch it.
// Returns the delivered text and how many bytes of w.Text survived, so the
// caller can report truncation without re-deriving it.
func renderSkillDelivery(path string, w skillWindow) (string, int) {
	keep := len(w.Text)
	footer := skillDeliveryFooter(path, w, keep)

	// The footer's length depends on the numbers it reports, which depend on
	// how much text survives, so converge instead of guessing a reserve.
	for i := 0; i < 8 && keep+len(footer) > MaxSkillBodySize; i++ {
		keep = MaxSkillBodySize - len(footer)
		if keep < 0 {
			keep = 0
		}
		footer = skillDeliveryFooter(path, w, keep)
	}

	text := w.Text[:keep]
	if keep < len(w.Text) {
		// Cut on a line boundary so the surviving text ends as readable
		// markdown — but never give up more than half the budget for it.
		if idx := strings.LastIndexByte(text, '\n'); idx > keep/2 {
			text = text[:idx+1]
		}
		footer = skillDeliveryFooter(path, w, len(text))
		for len(text) > 0 && len(text)+len(footer) > MaxSkillBodySize {
			text = text[:len(text)-1]
			footer = skillDeliveryFooter(path, w, len(text))
		}
	}
	return text + footer, len(text)
}

// skillDeliveryFooter renders the size/has-more report appended to every load.
//
// The complete-and-unwindowed case gets a single terse line. That line is the
// cheap half of this feature: an agent that can see "complete" after ONE call
// has no reason to spend a second one paging defensively.
func skillDeliveryFooter(path string, w skillWindow, delivered int) string {
	end := w.Start + delivered
	remaining := w.SpaceLen - end
	if remaining < 0 {
		remaining = 0
	}

	if w.Mode == "skill" && w.Start == 0 && remaining == 0 {
		return fmt.Sprintf("\n\n[skill %s — %d bytes, complete]", path, w.SkillBytes)
	}

	var b strings.Builder
	b.WriteString("\n\n=== SKILL WINDOW ===\n")
	switch w.Mode {
	case "section":
		fmt.Fprintf(&b, "Skill %s, section %q. Skill total: %d bytes.\n", path, w.Label, w.SkillBytes)
	case "filter":
		fmt.Fprintf(&b, "Skill %s, filter %q — %d matching lines. Skill total: %d bytes.\n",
			path, w.Label, w.Matches, w.SkillBytes)
	default:
		fmt.Fprintf(&b, "Skill %s. Skill total: %d bytes.\n", path, w.SkillBytes)
	}
	fmt.Fprintf(&b, "This view: bytes %d-%d of %d.\n", w.Start, end, w.SpaceLen)

	if remaining > 0 {
		fmt.Fprintf(&b, "%d BYTES REMAIN — this is NOT the whole thing, and it does not end\nwhere it appears to end. Continue with:\n  %s\n",
			remaining, w.continueCall(path, end))
	} else {
		fmt.Fprintf(&b, "Nothing remains after this view.\n")
	}

	if len(w.Sections) > 0 {
		fmt.Fprintf(&b, "Sections (fetch one with section=\"<name>\"):\n- %s\n",
			strings.Join(sectionTitles(w.Sections, maxOutlineEntries), "\n- "))
	}
	b.WriteString("=== END SKILL WINDOW ===")
	return b.String()
}

// continueCall renders the literal next call, repeating whichever selector
// produced this view. A notice that describes the remainder without naming the
// call to fetch it is the defect this feature exists to remove.
func (w skillWindow) continueCall(path string, offset int) string {
	switch w.Mode {
	case "section":
		return fmt.Sprintf("skill(action=%q, path=%q, section=%q, offset=%d)", "load", path, w.Label, offset)
	case "filter":
		return fmt.Sprintf("skill(action=%q, path=%q, regex=%q, offset=%d)", "load", path, w.Label, offset)
	default:
		return fmt.Sprintf("skill(action=%q, path=%q, offset=%d)", "load", path, offset)
	}
}
