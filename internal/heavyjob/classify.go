// Copyright (c) 2025 Reliant Labs
package heavyjob

import (
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/shellparse"
)

// pattern is one heavy-command pattern, split into words.
type pattern []string

func parsePatterns(src []string) []pattern {
	out := make([]pattern, 0, len(src))
	for _, s := range src {
		if f := strings.Fields(s); len(f) > 0 {
			out = append(out, f)
		}
	}
	return out
}

// classify reports whether any simple command in a shell line starts with a
// pattern's words (the rules are on Gate.Heavy).
func classify(patterns []pattern, line string) bool {
	// A heredoc body is data fed to a command, not commands: `cat >> notes
	// <<'EOF'` followed by a line reading "forge lint" is not a lint run.
	for _, words := range simpleCommands(shellparse.StripHeredocs(line)) {
		if matchWords(patterns, words) {
			return true
		}
	}
	return false
}

// matchWords reports whether a simple command's words start with a pattern.
func matchWords(patterns []pattern, words []string) bool {
	words = skipModifiers(words)
	if len(words) == 0 {
		return false
	}
	// A program run by path (/usr/local/go/bin/go, node_modules/.bin/vitest)
	// is the program.
	words = append([]string{filepath.Base(words[0])}, words[1:]...)
	for _, p := range patterns {
		if len(words) < len(p) {
			continue
		}
		ok := true
		for i, w := range p {
			if !matchWord(w, words[i]) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func matchWord(pat, word string) bool {
	if prefix, ok := strings.CutSuffix(pat, "*"); ok {
		return strings.HasPrefix(word, prefix)
	}
	return pat == word
}

// shellKeywords precede a command without being one.
var shellKeywords = map[string]bool{
	"do": true, "then": true, "else": true, "elif": true, "if": true,
	"while": true, "until": true, "!": true, "{": true,
}

// skipModifiers drops the leading words that only say how the real command
// runs: assignments, shell keywords and the common wrappers with their flags.
func skipModifiers(words []string) []string {
	for len(words) > 0 {
		w := words[0]
		switch {
		case isAssignment(w), shellKeywords[w]:
			words = words[1:]
		case w == "env":
			words = words[1:]
			for len(words) > 0 && (strings.HasPrefix(words[0], "-") || isAssignment(words[0])) {
				words = words[1:]
			}
		case w == "time", w == "nohup", w == "command", w == "exec":
			words = skipFlags(words[1:])
		case w == "nice", w == "ionice":
			words = words[1:]
			for len(words) > 0 && strings.HasPrefix(words[0], "-") {
				// -n 10 and -n10 both occur; a bare -n takes the next word.
				if (words[0] == "-n" || words[0] == "-c") && len(words) > 1 {
					words = words[1:]
				}
				words = words[1:]
			}
		case w == "timeout":
			words = skipFlags(words[1:])
			if len(words) > 0 {
				words = words[1:] // the duration
			}
		default:
			return words
		}
	}
	return words
}

func skipFlags(words []string) []string {
	for len(words) > 0 && strings.HasPrefix(words[0], "-") {
		words = words[1:]
	}
	return words
}

// isAssignment reports a VAR=value word.
func isAssignment(w string) bool {
	name, _, ok := strings.Cut(w, "=")
	if !ok || name == "" {
		return false
	}
	for i, r := range name {
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// simpleCommands splits a shell line into the word lists of its simple
// commands, honouring quotes and backslashes, and dropping comments.
func simpleCommands(line string) [][]string {
	var (
		out   [][]string
		words []string
		word  strings.Builder
		inW   bool
		quote rune
	)
	endWord := func() {
		if inW {
			words = append(words, word.String())
			word.Reset()
			inW = false
		}
	}
	endCmd := func() {
		endWord()
		if len(words) > 0 {
			out = append(out, words)
			words = nil
		}
	}
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else if r == '\\' && quote == '"' && i+1 < len(rs) {
				i++
				word.WriteRune(rs[i])
			} else {
				word.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inW = r, true
		case r == '\\' && i+1 < len(rs):
			i++
			if rs[i] != '\n' {
				word.WriteRune(rs[i])
				inW = true
			}
		case r == '#' && !inW:
			// A comment runs to the end of the line.
			for i+1 < len(rs) && rs[i+1] != '\n' {
				i++
			}
		case strings.ContainsRune(";&|()`\n", r):
			endCmd()
		case r == ' ' || r == '\t' || r == '\r':
			endWord()
		default:
			word.WriteRune(r)
			inW = true
		}
	}
	endCmd()
	return out
}
