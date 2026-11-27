package cli

import (
	"path"
	"strings"
)

// Deciding whether a step's `run:` script uploads test results, for `forge
// fix ci-oidc-permissions` (upgrade_ci_oidc.go).
//
// The script is split into simple commands at newlines, `;`, `&`, `&&`,
// `||`, `|` and parentheses, and each is tokenized the way a shell splits
// words: single and double quotes, backslash escapes, `\`-newline
// continuations, and `#` comments at the start of a word. A simple command
// runs the report when, after any leading VAR=value assignments and the
// wrappers env, exec, time and command, its command word is
//
//   - forge (any path), or `reliant forge`, followed by `test report`; or
//   - a variable (`$FORGE`, `"${FORGE}"`), followed by `test report`; or
//   - task (any path), with `test:report` among its task names.
//
// It uploads unless its own arguments turn upload off: `--upload=<false>` or
// `--upload <false>`, where <false> is a value the flag reads as off (false,
// off, 0, no). For task those are the arguments after `--`, which task hands
// to forge. A job uploads if any one command does.
//
// The analysis is conservative: where the script cannot be read this way (an
// unterminated quote, or the report named inside `$(…)` or backticks) and
// its text names the report, it counts as an upload. Granting `id-token:
// write` to a job that does not upload costs a permission it never uses;
// withholding it from one that does makes the upload skip.

// ciOIDCRunUploads reports whether run, a step's `run:` script, uploads.
func ciOIDCRunUploads(run string) bool {
	cmds, ok := ciOIDCShellCommands(run)
	if !ok {
		return ciOIDCNamesReport(run)
	}
	for _, words := range cmds {
		if args, isReport := ciOIDCReportArgs(words); isReport && !ciOIDCUploadOff(args) {
			return true
		}
	}
	return false
}

// ciOIDCNamesReport is a loose textual check: the script mentions the report.
func ciOIDCNamesReport(s string) bool {
	return strings.Contains(s, "test:report") || strings.Contains(strings.Join(strings.Fields(s), " "), "test report")
}

// ciOIDCShellCommands splits a script into simple commands of words. ok is
// false when the script cannot be read reliably and names the report: an
// unterminated quote, or a command substitution.
func ciOIDCShellCommands(s string) (cmds [][]string, ok bool) {
	var (
		words  []string
		word   strings.Builder
		inWord bool
		subst  bool
	)
	endWord := func() {
		if inWord {
			words = append(words, word.String())
			word.Reset()
			inWord = false
		}
	}
	endCmd := func() {
		endWord()
		if len(words) > 0 {
			cmds = append(cmds, words)
			words = nil
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			i++
			if s[i] != '\n' { // `\`-newline joins lines
				word.WriteByte(s[i])
				inWord = true
			}
		case c == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, false
			}
			word.WriteString(s[i+1 : i+1+j])
			inWord = true
			i += j + 1
		case c == '"':
			i++
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`\n", s[i+1]) >= 0 {
					i++
				}
				if s[i] == '`' || (s[i] == '$' && i+1 < len(s) && s[i+1] == '(') {
					subst = true
				}
				word.WriteByte(s[i])
			}
			if i >= len(s) {
				return nil, false
			}
			inWord = true
		case c == '`' || (c == '$' && i+1 < len(s) && s[i+1] == '('):
			subst = true
			word.WriteByte(c)
			inWord = true
		case c == '#' && !inWord:
			for i < len(s) && s[i] != '\n' {
				i++
			}
			endCmd()
		case c == '\n' || c == ';' || c == '&' || c == '|' || c == '(' || c == ')':
			endCmd()
		case c == ' ' || c == '\t' || c == '\r':
			endWord()
		default:
			word.WriteByte(c)
			inWord = true
		}
	}
	endCmd()
	if subst && ciOIDCNamesReport(s) {
		return nil, false
	}
	return cmds, true
}

// ciOIDCReportArgs reports whether a simple command runs the report, and
// the arguments that reach `forge test report`.
func ciOIDCReportArgs(words []string) (args []string, isReport bool) {
	i := 0
	for i < len(words) {
		w := words[i]
		if eq := strings.IndexByte(w, '='); eq > 0 && !strings.HasPrefix(w, "-") && !strings.ContainsAny(w[:eq], "/$") {
			i++ // VAR=value
			continue
		}
		if b := path.Base(w); b == "env" || b == "exec" || b == "time" || b == "command" {
			i++
			continue
		}
		break
	}
	if i >= len(words) {
		return nil, false
	}
	cmd, rest := path.Base(words[i]), words[i+1:]
	if cmd == "reliant" && len(rest) > 0 && rest[0] == "forge" {
		cmd, rest = "forge", rest[1:]
	}
	switch {
	case cmd == "forge" || strings.HasPrefix(words[i], "$"):
		if len(rest) >= 2 && rest[0] == "test" && rest[1] == "report" {
			return rest[2:], true
		}
	case cmd == "task":
		for j, w := range rest {
			if w == "--" {
				return nil, false
			}
			if w == "test:report" {
				for k := j + 1; k < len(rest); k++ {
					if rest[k] == "--" {
						return rest[k+1:], true
					}
				}
				return nil, true
			}
		}
	}
	return nil, false
}

// ciOIDCUploadOff reports whether the report's arguments turn upload off.
// The last --upload wins, as it does for the flag.
func ciOIDCUploadOff(args []string) bool {
	off := false
	for i, a := range args {
		switch {
		case strings.HasPrefix(a, "--upload="):
			off = ciOIDCFalse(strings.TrimPrefix(a, "--upload="))
		case a == "--upload":
			off = i+1 < len(args) && ciOIDCFalse(args[i+1])
		}
	}
	return off
}

// ciOIDCFalse is the --upload flag's reading of off (uploadFlag.Set).
func ciOIDCFalse(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "false", "off", "0", "no":
		return true
	}
	return false
}
