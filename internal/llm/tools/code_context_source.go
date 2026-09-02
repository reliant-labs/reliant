// Copyright (c) 2025 Reliant Labs
package tools

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"unicode"
)

// Source preview for code_context.
//
// Knowing that a function has three callers rarely settles the question the
// agent actually has, which is "what does it DO" — so without the body it reads
// the digest, then spends another turn on `view`. That second turn is the thing
// this whole tool exists to remove, and it is the most common follow-up by far.
//
// The preview is capped rather than complete: the first ~30 lines carry the
// signature, the guard clauses and the shape of the logic, which is what
// decides the next action. Pasting a 400-line function would trade the turn we
// saved for tokens we cannot spend twice, and the reader would skim it anyway.

// codeContextSourceLines is the default preview budget. Wide enough for a
// signature plus early returns; narrow enough that several previews in one
// response stay readable.
const codeContextSourceLines = 30

// readSourcePreview returns the declaration's source, capped at maxLines.
//
// When the engine supplied an exact end line (tsserver does) that bound is
// authoritative. Otherwise the extent is inferred, because gopls reports only
// the declaration position and re-parsing the file to find a body is a lot of
// machinery for a preview that is capped anyway.
func readSourcePreview(loc codeLocation, maxLines int) (string, bool) {
	if maxLines <= 0 {
		maxLines = codeContextSourceLines
	}
	lines, err := readFileLines(loc.Path)
	if err != nil || loc.Line <= 0 || loc.Line > len(lines) {
		return "", false
	}

	start := loc.Line - 1 // to 0-based
	end := inferDeclarationEnd(lines, start, loc.EndLine)

	truncated := false
	if end-start > maxLines {
		end = start + maxLines
		truncated = true
	}

	var sb strings.Builder
	for i := start; i < end && i < len(lines); i++ {
		fmt.Fprintf(&sb, "  %5d | %s\n", i+1, lines[i])
	}
	if truncated {
		fmt.Fprintf(&sb, "  %5s | ... truncated at %d lines (read the file for the rest)\n", "", maxLines)
	}
	return sb.String(), true
}

// inferDeclarationEnd finds the last line of a declaration starting at start.
//
// engineEnd (1-based) wins when present. Otherwise: brace-balance for C-family
// syntax, and for brace-less languages the first non-blank line at or below the
// declaration's own indentation. Both are heuristics, which is acceptable
// because the result is clamped by the line budget regardless — a wrong guess
// shows a few extra lines, not a wrong answer.
func inferDeclarationEnd(lines []string, start int, engineEnd int) int {
	if engineEnd > start {
		if engineEnd > len(lines) {
			return len(lines)
		}
		return engineEnd
	}

	first := lines[start]
	if strings.Contains(first, "{") {
		depth := 0
		for i := start; i < len(lines); i++ {
			depth += netBraceDepth(lines[i])
			if depth <= 0 && i > start {
				return i + 1
			}
			if depth <= 0 && i == start && strings.Contains(lines[i], "}") {
				return i + 1 // single-line body
			}
		}
		return len(lines)
	}

	baseIndent := indentWidth(first)
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		if indentWidth(lines[i]) <= baseIndent {
			return i
		}
	}
	return len(lines)
}

// netBraceDepth counts brace nesting on one line, ignoring braces inside string
// literals and line comments. Without that, a line containing "}" in a message
// closes the function early and truncates the preview mid-body.
func netBraceDepth(line string) int {
	depth := 0
	var quote rune
	escaped := false
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		if escaped {
			escaped = false
			continue
		}
		switch {
		case quote != 0:
			if c == '\\' && quote != '`' {
				escaped = true
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == '/' && i+1 < len(runes) && runes[i+1] == '/':
			return depth // rest of the line is a comment
		case c == '{':
			depth++
		case c == '}':
			depth--
		}
	}
	return depth
}

func indentWidth(line string) int {
	width := 0
	for _, c := range line {
		switch {
		case c == '\t':
			width += 4
		case unicode.IsSpace(c):
			width++
		default:
			return width
		}
	}
	return width
}

// codeContextMaxFileBytes bounds how much of a file the preview will read. A
// generated file can be enormous, and a preview never needs more than its head.
const codeContextMaxFileBytes = 4 << 20

func readFileLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if info, err := f.Stat(); err == nil && info.Size() > codeContextMaxFileBytes {
		return nil, fmt.Errorf("file too large for preview")
	}

	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}
