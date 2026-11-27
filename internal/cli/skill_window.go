package cli

import (
	"fmt"
	"regexp"
	"strings"
)

type skillLoadWindow struct {
	section, pattern string
	ignoreCase       bool
	contextLines     int
}

// selectFromSkill slices the rendered skill, not its template. Regex line
// numbers stay relative to the full rendering even when a section is selected.
func (w skillLoadWindow) selectFromSkill(body []byte) ([]byte, error) {
	if w.section == "" && w.pattern == "" {
		return body, nil
	}
	lines := strings.SplitAfter(string(body), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	start, end := 0, len(lines)
	if w.section != "" {
		var err error
		start, end, err = findSkillSection(lines, w.section)
		if err != nil {
			return nil, err
		}
	}
	if w.pattern == "" {
		return []byte(strings.Join(lines[start:end], "")), nil
	}
	return w.matchSkillLines(lines, start, end)
}

type skillHeading struct {
	text  string
	level int
	line  int
}

func findSkillSection(lines []string, query string) (int, int, error) {
	headings := scanSkillHeadings(lines)
	var exact, partial []int
	var names []string
	for i, h := range headings {
		names = append(names, h.text)
		if strings.EqualFold(h.text, query) {
			exact = append(exact, i)
		} else if strings.Contains(strings.ToLower(h.text), strings.ToLower(query)) {
			partial = append(partial, i)
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = partial
	}
	switch len(matches) {
	case 0:
		if len(names) == 0 {
			return 0, 0, fmt.Errorf("section %q not found: no Markdown headings", query)
		}
		return 0, 0, fmt.Errorf("section %q not found; available headings: %s", query, strings.Join(names, ", "))
	case 1:
		i := matches[0]
		h := headings[i]
		for _, next := range headings[i+1:] {
			if next.level <= h.level {
				return h.line, next.line, nil
			}
		}
		return h.line, len(lines), nil
	default:
		var choices []string
		for _, i := range matches {
			choices = append(choices, fmt.Sprintf("%s (line %d)", headings[i].text, headings[i].line+1))
		}
		return 0, 0, fmt.Errorf("section %q is ambiguous: %s; choose a unique heading or use --regex", query, strings.Join(choices, ", "))
	}
}

// scanSkillHeadings recognizes the ATX headings shipped skills use. Skip YAML
// front matter, fenced code (backticks or tildes), and indented examples.
func scanSkillHeadings(lines []string) []skillHeading {
	var headings []skillHeading
	frontmatter := len(lines) > 0 && strings.TrimSpace(lines[0]) == "---"
	var fence byte
	var fenceLen int
	for i, raw := range lines {
		if frontmatter {
			if i > 0 && strings.TrimSpace(raw) == "---" {
				frontmatter = false
			}
			continue
		}
		line := strings.TrimLeft(raw, " ")
		if len(raw)-len(line) > 3 || line == "" {
			continue
		}
		mark := line[0]
		if mark == '`' || mark == '~' {
			n := len(line) - len(strings.TrimLeft(line, string(mark)))
			if fence != 0 && mark == fence && n >= fenceLen && strings.TrimSpace(line[n:]) == "" {
				fence, fenceLen = 0, 0
			} else if fence == 0 && n >= 3 {
				fence, fenceLen = mark, n
			}
			continue
		}
		if fence != 0 || mark != '#' {
			continue
		}
		n := len(line) - len(strings.TrimLeft(line, "#"))
		if n > 6 || n == len(line) || (line[n] != ' ' && line[n] != '\t') {
			continue
		}
		text := strings.TrimSpace(line[n:])
		// A whitespace-separated run of closing hashes is not heading text.
		if trimmed := strings.TrimRight(text, "#"); strings.HasSuffix(trimmed, " ") || strings.HasSuffix(trimmed, "\t") {
			text = strings.TrimSpace(trimmed)
		}
		if text != "" {
			headings = append(headings, skillHeading{text, n, i})
		}
	}
	return headings
}

func (w skillLoadWindow) matchSkillLines(lines []string, start, end int) ([]byte, error) {
	pattern := w.pattern
	if w.ignoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid --regex: %w", err)
	}
	if w.contextLines < 0 {
		return nil, fmt.Errorf("--context must be non-negative")
	}
	contextLines := min(w.contextLines, end-start)
	matches := make([]bool, end-start)
	show := make([]bool, end-start)
	until := 0
	for i, line := range lines[start:end] {
		if !re.MatchString(strings.TrimRight(line, "\r\n")) {
			continue
		}
		matches[i] = true
		lo, hi := max(0, i-contextLines), min(len(show), i+contextLines+1)
		for j := max(lo, until); j < hi; j++ {
			show[j] = true
		}
		until = hi
	}
	var out strings.Builder
	previous := -1
	for i, visible := range show {
		if !visible {
			continue
		}
		if contextLines > 0 && previous >= 0 && previous+1 != i {
			out.WriteString("--\n")
		}
		sep := "-"
		if matches[i] {
			sep = ":"
		}
		fmt.Fprintf(&out, "%d%s%s\n", start+i+1, sep, strings.TrimRight(lines[start+i], "\r\n"))
		previous = i
	}
	if previous < 0 {
		return nil, fmt.Errorf("no lines matched --regex %q", w.pattern)
	}
	return []byte(out.String()), nil
}
