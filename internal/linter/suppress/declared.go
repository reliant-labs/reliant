package suppress

import (
	"fmt"
	"strings"
)

// Declared is one rule a directive turns off, as written in a file: a line of
// the inventory `forge lint --show-suppressed` prints.
//
// It is built from the same primitives ParseFile uses (parseLine,
// nextLineTargets, isDirectiveOnlyLine), so the inventory and the applier
// cannot disagree about what a directive is or what it covers — the test
// beside this file applies a finding on every line an entry claims to cover.
type Declared struct {
	Rule string `json:"rule"`
	// Line is the directive's own 1-indexed line.
	Line int `json:"line"`
	// Directive is the token that declared it (TokenDisableNextLine, …).
	Directive string `json:"directive"`
	// Covers is the span the directive silences, in words: "the file",
	// "line 12", "lines 12-14", "line 12 to the end of the file".
	Covers string `json:"covers"`
	// Reason is the text after the rule list; "" when none was given.
	Reason string `json:"reason"`
}

// Declarations lists every suppression directive in one file's source, one
// entry per rule, in source order. TokenEnable only closes a block, so it is
// not listed itself; it sets the end of the block it closes.
func Declarations(content string) []Declared {
	lines := strings.Split(content, "\n")
	var out []Declared
	// open maps a rule to the index (in out) of its still-open block. As in
	// ParseFile, a re-opened block keeps the first opener.
	open := map[string]int{}
	for i, raw := range lines {
		lineNo := i + 1
		for _, d := range parseLine(raw, lineNo) {
			if d.token == TokenEnable {
				for _, r := range d.rules {
					if idx, ok := open[r]; ok {
						// A block is [start, end): the enable line is not
						// covered.
						out[idx].Covers = lineSpan(out[idx].Line, lineNo-1)
						delete(open, r)
					}
				}
				continue
			}
			covers := declaredSpan(lines, raw, d)
			for _, r := range d.rules {
				if d.token == TokenDisable {
					if _, already := open[r]; already {
						continue
					}
					open[r] = len(out)
				}
				out = append(out, Declared{Rule: r, Line: d.line, Directive: d.token, Covers: covers, Reason: d.reason})
			}
		}
	}
	return out
}

// declaredSpan renders what a directive covers, using the same span rules
// ParseFile applies.
func declaredSpan(lines []string, raw string, d directive) string {
	switch d.token {
	case TokenDisableFile:
		return "the file"
	case TokenDisableNextLine:
		t := nextLineTargets(lines, d.line)
		return lineSpan(t[0], t[len(t)-1])
	case TokenNolint:
		if isDirectiveOnlyLine(raw, TokenNolint) {
			t := nextLineTargets(lines, d.line)
			return lineSpan(t[0], t[len(t)-1])
		}
		return lineSpan(d.line, d.line)
	default: // TokenDisable; narrowed when its TokenEnable is found
		return fmt.Sprintf("line %d to the end of the file", d.line)
	}
}

func lineSpan(from, to int) string {
	if to <= from {
		return fmt.Sprintf("line %d", from)
	}
	return fmt.Sprintf("lines %d-%d", from, to)
}
