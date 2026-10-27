// Copyright (c) 2025 Reliant Labs

// Package shellparse splits a shell command line into the simple commands it
// runs, well enough to decide what a command WOULD do without running it. It is
// what the shell tool's guards and the daemon's heavy-command classifier read a
// command with, so quoted text and heredoc bodies — data, not commands — are
// never mistaken for commands.
//
// It is a classifier's parser, not a shell: it does not expand variables, run
// substitutions or evaluate control flow. Where the shell would run something
// (`$(…)`, backticks) it leaves the text in the word and exposes it through
// SubstitutionAt, so a caller can scan what runs inside.
//
//forge:exclude-contract: stateless text functions over a shell command line; no collaborator to fake and no second implementation
package shellparse

import (
	"strings"
)

// Single-quoted text is data: a `$` or backtick inside it must not look like a
// substitution to a caller scanning words. They are replaced by private-use
// runes while tokenizing; Restore turns them back when the text is shown or
// handed to another shell.
const (
	sentinelDollar   = '\uE000'
	sentinelBacktick = '\uE001'
)

var restorer = strings.NewReplacer(string(sentinelDollar), "$", string(sentinelBacktick), "`")

// Restore undoes the single-quote protection Commands applies, giving back the
// text the user wrote.
func Restore(s string) string { return restorer.Replace(s) }

// Command is one simple command: its words with quoting removed and escapes
// resolved. `$(…)` and backtick text stays in the word verbatim.
type Command struct {
	Words []string
	// PipeIn is true when the command's stdin is the previous command's stdout.
	PipeIn bool
}

// Commands splits line into its simple commands, on ; & && || | newlines and
// parentheses outside quotes. Comments are dropped.
func Commands(line string) []Command {
	line = strings.NewReplacer("\\\r\n", " ", "\\\n", " ").Replace(line)
	var (
		out     []Command
		words   []string
		cur     strings.Builder
		started bool
		pipeIn  bool
	)
	endWord := func() {
		if started {
			words = append(words, cur.String())
			cur.Reset()
			started = false
		}
	}
	// endCmd closes the current command. nextPiped says whether the operator
	// that closed it feeds the next command through a pipe; a bare newline after
	// an empty command (a continued pipeline) leaves the pipe in place.
	endCmd := func(nextPiped, isNewline bool) {
		endWord()
		if len(words) == 0 {
			if !isNewline {
				pipeIn = nextPiped
			}
			return
		}
		out = append(out, Command{Words: words, PipeIn: pipeIn})
		words = nil
		pipeIn = nextPiped
	}
	write := func(s string) {
		cur.WriteString(s)
		started = true
	}

	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch quote {
		case '\'':
			switch c {
			case '\'':
				quote = 0
			case '$':
				cur.WriteRune(sentinelDollar)
			case '`':
				cur.WriteRune(sentinelBacktick)
			default:
				cur.WriteByte(c)
			}
			started = true
			continue
		case '"':
			switch {
			case c == '"':
				quote = 0
			case c == '\\' && i+1 < len(line) && strings.IndexByte("\"\\$`", line[i+1]) >= 0:
				i++
				cur.WriteByte(line[i])
			case c == '$' && i+1 < len(line) && line[i+1] == '(':
				end := substEnd(line, i)
				cur.WriteString(line[i:end])
				i = end - 1
			case c == '`':
				end := backtickEnd(line, i)
				cur.WriteString(line[i:end])
				i = end - 1
			default:
				cur.WriteByte(c)
			}
			started = true
			continue
		}

		switch c {
		case ' ', '\t', '\r':
			endWord()
		case '\n':
			endCmd(false, true)
		case ';':
			endCmd(false, false)
		case '|':
			if i+1 < len(line) && line[i+1] == '|' {
				i++
				endCmd(false, false)
			} else {
				if i+1 < len(line) && line[i+1] == '&' {
					i++
				}
				endCmd(true, false)
			}
		case '&':
			switch {
			case i+1 < len(line) && line[i+1] == '&':
				i++
				endCmd(false, false)
			case i > 0 && (line[i-1] == '>' || line[i-1] == '<') || i+1 < len(line) && line[i+1] == '>':
				write("&") // 2>&1, &>file
			default:
				endCmd(false, false)
			}
		case '(', ')':
			endCmd(false, false)
		case '\\':
			if i+1 < len(line) {
				i++
				cur.WriteByte(line[i])
				started = true
			}
		case '\'', '"':
			quote = c
			started = true
		case '`':
			end := backtickEnd(line, i)
			write(line[i:end])
			i = end - 1
		case '$':
			if i+1 < len(line) && line[i+1] == '(' {
				end := substEnd(line, i)
				write(line[i:end])
				i = end - 1
			} else {
				write("$")
			}
		case '#':
			if started {
				write("#")
				continue
			}
			for i+1 < len(line) && line[i+1] != '\n' {
				i++
			}
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	endCmd(false, false)
	return out
}

// SubstitutionAt reads the command substitution that starts at s[i] — `$(…)` or
// a backtick pair — returning the text inside it and the index just past it.
// Arithmetic `$((…))` is not a substitution.
func SubstitutionAt(s string, i int) (inner string, end int, ok bool) {
	switch {
	case strings.HasPrefix(s[i:], "$(("):
		return "", 0, false
	case strings.HasPrefix(s[i:], "$("):
		end = substEnd(s, i)
		if end-i < 3 || s[end-1] != ')' {
			return s[i+2 : end], end, true
		}
		return s[i+2 : end-1], end, true
	case s[i] == '`':
		end = backtickEnd(s, i)
		if end-i < 2 || s[end-1] != '`' {
			return s[i+1 : end], end, true
		}
		return s[i+1 : end-1], end, true
	}
	return "", 0, false
}

// Substitutions returns the scripts a word runs when the shell expands it.
func Substitutions(word string) []string {
	var out []string
	for i := 0; i < len(word); i++ {
		if word[i] != '$' && word[i] != '`' {
			continue
		}
		if strings.HasPrefix(word[i:], "$((") {
			i = substEnd(word, i) - 1
			continue
		}
		if inner, end, ok := SubstitutionAt(word, i); ok {
			out = append(out, inner)
			i = end - 1
		}
	}
	return out
}

// substEnd returns the index just past the `)` that closes the `$(` at s[i],
// or len(s) when it never closes.
func substEnd(s string, i int) int {
	depth := 0
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '\'':
			for j++; j < len(s) && s[j] != '\''; j++ {
			}
		case '"':
			for j++; j < len(s) && s[j] != '"'; j++ {
				if s[j] == '\\' {
					j++
				}
			}
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return j + 1
			}
		}
	}
	return len(s)
}

// backtickEnd returns the index just past the backtick that closes the one at
// s[i], or len(s) when it never closes.
func backtickEnd(s string, i int) int {
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '`':
			return j + 1
		}
	}
	return len(s)
}
