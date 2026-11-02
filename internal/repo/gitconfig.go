// Copyright (c) 2025 Reliant Labs
package repo

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/gitremote"
)

// gitConfigMaxBytes bounds the config read. A repository's config is a few
// KB; the cap only keeps a pathological file from costing discovery anything.
const gitConfigMaxBytes = 1 << 20

// originURL is remote.origin.url of the repository whose git common dir is
// commonDir, credentials stripped, or "" when it has none or it cannot be
// read. It reads <commonDir>/config itself, which is where `git config --get
// remote.origin.url` finds it for a main checkout and every linked worktree
// (identifyCheckout follows the .git file and commondir indirection to the
// common dir). Discovery used to spawn git once per repository for this.
//
// Like git, the last url in the section wins. Not followed: [include] and
// [includeIf] files; a remote defined only through one reads as none, as a
// missing remote does.
func originURL(commonDir string) string {
	if commonDir == "" {
		return ""
	}
	f, err := os.Open(filepath.Join(commonDir, "config"))
	if err != nil {
		return ""
	}
	defer f.Close()
	return gitremote.StripCredentials(remoteURLFromConfig(bufio.NewReader(io.LimitReader(f, gitConfigMaxBytes)), "origin"))
}

// remoteURLFromConfig reads remote.<remote>.url from git config text: the
// `[remote "<remote>"]` section (the subsection is case-sensitive; the legacy
// `[remote.<remote>]` spelling is not), the `url` key in any case, values with
// git's quoting, escapes, trailing comments and backslash line continuations.
func remoteURLFromConfig(r *bufio.Reader, remote string) string {
	var (
		inSection bool
		url       string
		pending   string // a logical line being joined across continuations
	)
	for {
		raw, err := r.ReadString('\n')
		if raw == "" && err != nil {
			break
		}
		line := strings.TrimRight(raw, "\r\n")
		if pending != "" {
			line = pending + line
			pending = ""
		}
		if endsInContinuation(line) {
			pending = line[:len(line)-1]
			if err == nil {
				continue
			}
			line = pending
			pending = ""
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "" || trimmed[0] == '#' || trimmed[0] == ';':
		case trimmed[0] == '[':
			inSection = isRemoteSection(trimmed, remote)
			// A key may follow the header on the same line.
			if end := closingBracket(trimmed); end >= 0 && inSection {
				if v, ok := urlValue(trimmed[end+1:]); ok {
					url = v
				}
			}
		case inSection:
			if v, ok := urlValue(trimmed); ok {
				url = v
			}
		}
		if err != nil {
			break
		}
	}
	return url
}

// endsInContinuation reports whether line ends in an unescaped backslash.
func endsInContinuation(line string) bool {
	n := 0
	for i := len(line) - 1; i >= 0 && line[i] == '\\'; i-- {
		n++
	}
	return n%2 == 1
}

// closingBracket is the index of the `]` ending a section header, outside the
// quoted subsection, or -1.
func closingBracket(s string) int {
	quoted := false
	for i := 1; i < len(s); i++ {
		switch {
		case s[i] == '\\' && quoted:
			i++
		case s[i] == '"':
			quoted = !quoted
		case s[i] == ']' && !quoted:
			return i
		}
	}
	return -1
}

// isRemoteSection reports whether header opens remote.<remote>.
func isRemoteSection(header, remote string) bool {
	end := closingBracket(header)
	if end < 0 {
		return false
	}
	inner := strings.TrimSpace(header[1:end])
	if sp := strings.IndexAny(inner, " \t"); sp >= 0 {
		name, sub := inner[:sp], strings.TrimSpace(inner[sp:])
		if !strings.EqualFold(name, "remote") || len(sub) < 2 || sub[0] != '"' || sub[len(sub)-1] != '"' {
			return false
		}
		return unescapeSubsection(sub[1:len(sub)-1]) == remote
	}
	// Legacy [remote.origin]: the subsection is lower-cased by git.
	name, sub, ok := strings.Cut(inner, ".")
	return ok && strings.EqualFold(name, "remote") && strings.EqualFold(sub, remote)
}

func unescapeSubsection(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// urlValue parses `url = value` (key case-insensitive), or reports false.
func urlValue(line string) (string, bool) {
	key, value, ok := strings.Cut(line, "=")
	if !ok || !strings.EqualFold(strings.TrimSpace(key), "url") {
		return "", false
	}
	return parseConfigValue(value), true
}

// parseConfigValue applies git's value syntax: surrounding whitespace is
// dropped, `#` or `;` outside quotes starts a comment, double quotes group,
// and \\ \" \n \t \b are escapes.
func parseConfigValue(v string) string {
	var b strings.Builder
	quoted := false
	pendingSpace := ""
	started := false
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c == '\\' && i+1 < len(v):
			i++
			b.WriteString(pendingSpace)
			pendingSpace = ""
			switch v[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'b':
				s := b.String()
				if len(s) > 0 {
					b.Reset()
					b.WriteString(s[:len(s)-1])
				}
			default:
				b.WriteByte(v[i])
			}
			started = true
		case c == '"':
			quoted = !quoted
			b.WriteString(pendingSpace)
			pendingSpace = ""
			started = true
		case !quoted && (c == '#' || c == ';'):
			return b.String()
		case !quoted && (c == ' ' || c == '\t'):
			if started {
				pendingSpace += string(c)
			}
		default:
			b.WriteString(pendingSpace)
			pendingSpace = ""
			b.WriteByte(c)
			started = true
		}
	}
	return b.String()
}
