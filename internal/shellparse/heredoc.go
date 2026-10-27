// Copyright (c) 2025 Reliant Labs
package shellparse

import "strings"

type pendingHeredoc struct {
	delim string
	dash  bool // <<-: leading tabs are stripped before the terminator is compared
}

// StripHeredocs removes heredoc bodies and their terminator lines from a
// command line, keeping the line that opens each one. The body is data fed to a
// command's stdin, so a classifier that read it as commands would see `forge
// lint` in `cat >> notes <<'EOF'` and treat a note-taking command as a lint run.
//
// A heredoc that is never terminated is left exactly as written: the shell
// would reject it, and guessing where it ends could hide a real command.
func StripHeredocs(s string) string {
	if !strings.Contains(s, "<<") {
		return s
	}
	var (
		out   strings.Builder
		pend  []pendingHeredoc
		quote byte
		arith int
	)
	for i := 0; i < len(s); {
		c := s[i]
		if quote != 0 {
			out.WriteByte(c)
			switch {
			case c == '\\' && quote == '"' && i+1 < len(s):
				out.WriteByte(s[i+1])
				i += 2
				continue
			case c == quote:
				quote = 0
			}
			i++
			continue
		}
		switch {
		case c == '\\' && i+1 < len(s):
			out.WriteString(s[i : i+2])
			i += 2
		case c == '\'' || c == '"':
			quote = c
			out.WriteByte(c)
			i++
		case c == '#' && (i == 0 || strings.IndexByte(" \t\n;&|(", s[i-1]) >= 0):
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				j = len(s) - i
			}
			out.WriteString(s[i : i+j])
			i += j
		case strings.HasPrefix(s[i:], "$(("):
			arith = 2
			out.WriteString("$((")
			i += 3
		case arith > 0 && c == '(':
			arith++
			out.WriteByte(c)
			i++
		case arith > 0 && c == ')':
			arith--
			out.WriteByte(c)
			i++
		case arith == 0 && strings.HasPrefix(s[i:], "<<<"):
			out.WriteString("<<<")
			i += 3
		case arith == 0 && strings.HasPrefix(s[i:], "<<"):
			j := i + 2
			dash := false
			if j < len(s) && s[j] == '-' {
				dash = true
				j++
			}
			for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
				j++
			}
			delim, end := heredocDelimiter(s, j)
			if delim == "" {
				out.WriteString(s[i:j])
				i = j
				continue
			}
			pend = append(pend, pendingHeredoc{delim: delim, dash: dash})
			out.WriteString(s[i:end])
			i = end
		case c == '\n' && len(pend) > 0:
			out.WriteByte('\n')
			i++
			rest, ok := skipHeredocBodies(s, i, pend)
			if !ok {
				out.WriteString(s[i:])
				return out.String()
			}
			pend = nil
			i = rest
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String()
}

// heredocDelimiter reads the delimiter word at s[i:], returning it with quoting
// removed and the index just past it.
func heredocDelimiter(s string, i int) (delim string, end int) {
	var b strings.Builder
	j := i
loop:
	for j < len(s) {
		switch c := s[j]; {
		case c == '\'' || c == '"':
			k := strings.IndexByte(s[j+1:], c)
			if k < 0 {
				return "", i
			}
			b.WriteString(s[j+1 : j+1+k])
			j += k + 2
		case c == '\\' && j+1 < len(s):
			b.WriteByte(s[j+1])
			j += 2
		case strings.IndexByte(" \t\n;|&<>()", c) >= 0:
			break loop
		default:
			b.WriteByte(c)
			j++
		}
	}
	return b.String(), j
}

// skipHeredocBodies consumes, from s[i:], one body per pending heredoc and
// returns the index after the last terminator line.
func skipHeredocBodies(s string, i int, pend []pendingHeredoc) (int, bool) {
	for _, p := range pend {
		found := false
		for i < len(s) {
			nl := strings.IndexByte(s[i:], '\n')
			line, next := s[i:], len(s)
			if nl >= 0 {
				line, next = s[i:i+nl], i+nl+1
			}
			if p.dash {
				line = strings.TrimLeft(line, "\t")
			}
			i = next
			if line == p.delim {
				found = true
				break
			}
		}
		if !found {
			return 0, false
		}
	}
	return i, true
}
