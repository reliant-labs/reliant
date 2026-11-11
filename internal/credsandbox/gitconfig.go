// Copyright (c) 2026 Reliant Labs

package credsandbox

import (
	"bytes"
	"strings"
)

// gitConfigEntry is one `key = value` of a git config file: the section it
// sits in (lower-cased, its subsection dropped), its key (lower-cased), and
// its value decoded as git decodes it.
type gitConfigEntry struct {
	section string
	key     string
	value   string
}

// parseGitConfig reads a git config file's entries with git's own syntax
// (config.c's git_parse_source): `#` and `;` comments outside quotes,
// quoted runs, the `\t \b \n \\ \"` escapes, `\`-newline continuations,
// each unquoted whitespace character read as a space with leading and
// trailing ones dropped, CRLF line ends and a UTF-8 BOM. A file git would
// refuse is read up to the error, as git refuses to run past it; what came
// before is kept, so nothing it already named goes unprotected.
func parseGitConfig(b []byte) []gitConfigEntry {
	p := gitConfigParser{b: bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))}
	var out []gitConfigEntry
	section := ""
	comment := false
	for {
		c := p.next()
		if c == '\n' {
			if p.eof {
				return out
			}
			comment = false
			continue
		}
		if comment || isGitSpace(c) {
			continue
		}
		if c == '#' || c == ';' {
			comment = true
			continue
		}
		if c == '[' {
			name, ok := p.sectionHeader()
			if !ok {
				return out
			}
			section = name
			continue
		}
		if !isGitAlpha(c) {
			return out
		}
		key, value, ok := p.keyValue(c)
		if !ok {
			return out
		}
		out = append(out, gitConfigEntry{section: section, key: key, value: value})
	}
}

type gitConfigParser struct {
	b   []byte
	i   int
	eof bool
}

// next is git's get_next_char: CRLF reads as LF, and the end of the file as
// a newline with eof set.
func (p *gitConfigParser) next() byte {
	if p.i >= len(p.b) {
		p.eof = true
		return '\n'
	}
	c := p.b[p.i]
	p.i++
	if c == '\r' && p.i < len(p.b) && p.b[p.i] == '\n' {
		p.i++
		c = '\n'
	}
	return c
}

// sectionHeader reads `[name]`, `[name "subsection"]` or the deprecated
// `[name.subsection]` after its `[`, and returns the name before any dot.
func (p *gitConfigParser) sectionHeader() (string, bool) {
	var name []byte
	for {
		c := p.next()
		if p.eof {
			return "", false
		}
		switch {
		case c == ']':
			return baseSection(name), true
		case isGitSpace(c):
			return baseSection(name), p.subsection()
		case isGitKeyChar(c) || c == '.':
			name = append(name, lowerASCII(c))
		default:
			return "", false
		}
	}
}

// subsection reads ` "subsection"]` after a section name, its escapes
// included; its text is not needed.
func (p *gitConfigParser) subsection() bool {
	c := p.next()
	for isGitSpace(c) && !p.eof {
		c = p.next()
	}
	if c != '"' {
		return false
	}
	for {
		c = p.next()
		if c == '\n' {
			return false
		}
		if c == '"' {
			break
		}
		if c == '\\' {
			if c = p.next(); c == '\n' {
				return false
			}
		}
	}
	return p.next() == ']'
}

func baseSection(name []byte) string {
	s, _, _ := strings.Cut(string(name), ".")
	return s
}

// keyValue reads a key starting with first and, after an `=`, its value. A
// key with no `=` is git's boolean true, with an empty value here.
func (p *gitConfigParser) keyValue(first byte) (string, string, bool) {
	key := []byte{lowerASCII(first)}
	c := p.next()
	for !p.eof && isGitKeyChar(c) {
		key = append(key, lowerASCII(c))
		c = p.next()
	}
	for c == ' ' || c == '\t' {
		c = p.next()
	}
	if c == '\n' {
		return string(key), "", true
	}
	if c != '=' {
		return "", "", false
	}
	value, ok := p.value()
	return string(key), value, ok
}

// value is git's parse_value.
func (p *gitConfigParser) value() (string, bool) {
	var buf []byte
	quote, comment := false, false
	space := 0
	for {
		c := p.next()
		if c == '\n' {
			if quote {
				return "", false
			}
			return string(buf), true
		}
		if comment {
			continue
		}
		if isGitSpace(c) && !quote {
			if len(buf) > 0 {
				space++
			}
			continue
		}
		if !quote && (c == ';' || c == '#') {
			comment = true
			continue
		}
		for ; space > 0; space-- {
			buf = append(buf, ' ')
		}
		switch c {
		case '\\':
			switch c = p.next(); c {
			case '\n':
				continue
			case 't':
				c = '\t'
			case 'b':
				c = '\b'
			case 'n':
				c = '\n'
			case '\\', '"':
			default:
				return "", false
			}
			buf = append(buf, c)
		case '"':
			quote = !quote
		default:
			buf = append(buf, c)
		}
	}
}

func isGitSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r'
}

func isGitAlpha(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func isGitKeyChar(c byte) bool { return isGitAlpha(c) || (c >= '0' && c <= '9') || c == '-' }

func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}
