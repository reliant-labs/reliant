// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"path"
	"strings"
)

// Filesystem-wide searches are the single most expensive thing an agent can do
// by accident. A `find / -path "*forge/pkg/svcerr*"` walks every inode on the
// machine; ten agents doing it concurrently saturate the disk and none of them
// finish. The agent cannot tell in advance that it will be slow, and the shell
// tool cannot tell it afterwards — it just hangs until the timeout.
//
// So refuse before dispatch. The refusal is deliberately a refusal and not a
// silent rewrite of the command: rewriting `find /` into `find .` would report
// results for a command the agent did not run, which is worse than being slow.

// scanRoots are directories whose recursive traversal is unbounded for
// practical purposes. Matched exactly after cleaning — `/usr` is refused,
// `/usr/local/share/myproject` is not.
var scanRoots = map[string]bool{
	"/":             true,
	"/Users":        true,
	"/home":         true,
	"/usr":          true,
	"/var":          true,
	"/opt":          true,
	"/etc":          true,
	"/Library":      true,
	"/System":       true,
	"/Applications": true,
	"/private":      true,
	"/nix":          true,
	"/proc":         true,
	"/sys":          true,
	"/mnt":          true,
	"/media":        true,
}

// recursiveByDefault are search commands that walk a directory tree unless told
// otherwise. grep is handled separately because it needs an explicit -r.
var recursiveByDefault = map[string]bool{
	"find":   true,
	"rg":     true,
	"ag":     true,
	"ack":    true,
	"fd":     true,
	"fdfind": true,
}

// unscopedSearchRefusal returns a non-empty refusal message when command would
// recursively scan a filesystem-wide or home-wide root, and "" otherwise.
func unscopedSearchRefusal(command string) string {
	// cwd tracks `cd` between segments so `cd / && find . -name x` is caught
	// too. "" means "wherever the daemon put us", which is the worktree.
	cwd := ""
	for _, seg := range splitShellSegments(command) {
		argv := tokenizeShell(seg)
		argv = stripLeadingAssignments(argv)
		if len(argv) == 0 {
			continue
		}
		cmd := path.Base(argv[0])

		if cmd == "cd" {
			cwd = absoluteCdTarget(argv)
			continue
		}
		if !isRecursiveScan(cmd, argv) {
			continue
		}
		root, explicit := scanRootOperand(cmd, argv)
		switch {
		case !explicit:
			// No path operand: the scan is rooted at the working directory.
			root = cwd
		case isRelativePath(root) && cwd != "":
			// `cd / && find . -name x` scans the same tree as `find /`.
			root = path.Join(cwd, root)
		}
		if root == "" {
			continue // scoped to the worktree
		}
		if normalized, broad := isScanRoot(root); broad {
			return fmt.Sprintf(refusalTemplate, seg, normalized)
		}
	}
	return ""
}

// In ripgrep, -r is --replace, not --recursive. Every neighbouring tool an agent
// uses reaches the other way (`grep -r`, `cp -r`, `rm -r`, `ls -R`), and
// `grep -rn 'pattern' .` is common enough to be emitted as one token, so `rg -rn`
// gets typed out of habit.
//
// It cannot be left to correct itself, because it does not fail. `rg -rn 'pat' .`
// binds "n" as the replacement and searches for "pat" in the right files: correct
// paths, correct line numbers, and the matched text in every printed line
// rewritten to "n". It exits 0. A measured case printed
// `forgecli.n(projectPath)` for a line whose real content was
// `forgecli.RenderProjectMemory(projectPath)`. With no match it exits 1 — the same
// code as an honest no-match, so the exit status cannot separate them either.
// That makes it the one search mistake with no corrective signal at all: a wrong
// answer that looks exactly like a right one.
//
// So refuse the SHORT spelling only. `--replace` is untouched, which keeps the
// capability whole — capture-group extraction (`rg -o --replace '$1' 'func (\w+)'`)
// still works verbatim. The split holds because the two spellings differ in
// intent, not in power: `-r` is what muscle memory produces, while nobody reaches
// for `--replace` without already knowing what it does. Searched across reliant,
// control-plane and forge, real `--replace` uses: zero.
func ripgrepReplaceRefusal(command string) string {
	for _, seg := range splitShellSegments(command) {
		argv := stripLeadingAssignments(tokenizeShell(seg))
		if len(argv) == 0 || path.Base(argv[0]) != "rg" {
			continue
		}
		for _, a := range argv[1:] {
			if a == "--" {
				break // everything after is an operand
			}
			if len(a) < 2 || a[0] != '-' || strings.HasPrefix(a, "--") {
				continue
			}
			if bundlesShortReplace(a[1:]) {
				return fmt.Sprintf(replaceRefusalTemplate, seg, a)
			}
		}
	}
	return ""
}

// shortFlagsTakingValue are rg's short flags that consume an argument. In a
// bundled run the first of them ends the flags and takes the rest of the token.
const shortFlagsTakingValue = "ABCEefgjMmtT"

// bundlesShortReplace reports whether a run of bundled short flags uses -r as a
// FLAG. Reading left to right is what makes this precise rather than a substring
// test: in `-epattern` the 'r' is part of the pattern -e already claimed, so a
// naive strings.ContainsRune would refuse a legitimate search.
func bundlesShortReplace(flags string) bool {
	for i := 0; i < len(flags); i++ {
		switch c := flags[i]; {
		case c == 'r':
			return true
		case strings.IndexByte(shortFlagsTakingValue, c) >= 0:
			return false
		}
	}
	return false
}

const replaceRefusalTemplate = `refused: %q uses rg's short -r flag, which is --replace, NOT --recursive.

This is refused because it does not fail on its own. %[2]q binds the next characters as REPLACEMENT text and rewrites the matched text in every line it prints, then exits 0 — so the output looks like a normal search result while the content is corrupted. Reading it costs you the answer, not just the command.

If you meant to search recursively, drop the flag entirely — rg already recurses:
  - rg 'pattern' .            — search file contents from the worktree root
  - rg -n 'pattern' internal/ — with line numbers, scoped to a subdirectory

If you did mean substitution, spell it in full and it will run unchanged:
  - rg -o --replace '$1' 'func (\w+)' .   — print capture groups only

Note that --replace only rewrites rg's OUTPUT; it never edits files. To change files on disk, use the edit tool.`

const refusalTemplate = `refused: %q would recursively scan %s. A scan rooted there reads the whole machine, takes minutes, and starves every other agent sharing this disk.

To search the PROJECT, scope the search to a relative path instead of an absolute root:
  - rg 'pattern' .            — search file contents from the worktree root
  - rg --files -g '**/*.go'   — find files by name or path pattern
  - rg honours .gitignore; add --glob '!vendor' for trees it does not cover, and
    bound the output with -l / -m N when a pattern is likely to match widely.

To locate a DEPENDENCY's source outside the project, ask the package manager rather than scanning the disk:
  - Go:   go list -m -f '{{.Dir}}' <module>   (or: go env GOMODCACHE)
  - Node: npm root   (or: node -p "require.resolve('<pkg>')")

If you genuinely need a filesystem search, name the specific directory to search.`

// isScanRoot reports whether p is an unbounded search root, returning the
// cleaned path it matched on for the refusal message.
func isScanRoot(p string) (string, bool) {
	if isHomeToken(p) {
		return p + " (your home directory)", true
	}
	if !strings.HasPrefix(p, "/") {
		return "", false // relative: scoped to the worktree
	}
	clean := path.Clean(p)
	if scanRoots[clean] {
		return clean, true
	}
	// A user's home directory: exactly one component under /Users or /home.
	parts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	if len(parts) == 2 && (parts[0] == "Users" || parts[0] == "home") {
		return clean, true
	}
	return "", false
}

// isRelativePath reports whether p is resolved against the working directory
// rather than naming an absolute location.
func isRelativePath(p string) bool {
	return !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "~") && !strings.HasPrefix(p, "$HOME") && !strings.HasPrefix(p, "${HOME}")
}

// isHomeToken reports whether p is one of the ways a shell spells "my home
// directory". The literal expansion (/Users/x, /home/x) is caught by isScanRoot.
func isHomeToken(p string) bool {
	switch p {
	case "~", "~/", "$HOME", "${HOME}", "$HOME/", "${HOME}/":
		return true
	}
	return false
}

// isRecursiveScan reports whether argv is a directory-walking search.
func isRecursiveScan(cmd string, argv []string) bool {
	if recursiveByDefault[cmd] {
		return true
	}
	switch cmd {
	case "grep", "egrep", "fgrep", "rgrep":
		if cmd == "rgrep" {
			return true
		}
		for _, a := range argv[1:] {
			if a == "--recursive" || a == "--dereference-recursive" {
				return true
			}
			// Bundled short flags: -rn, -Rl, ...
			if len(a) > 1 && a[0] == '-' && !strings.HasPrefix(a, "--") &&
				strings.ContainsAny(a[1:], "rR") {
				return true
			}
		}
	case "ls":
		for _, a := range argv[1:] {
			if a == "--recursive" {
				return true
			}
			if len(a) > 1 && a[0] == '-' && !strings.HasPrefix(a, "--") &&
				strings.ContainsRune(a[1:], 'R') {
				return true
			}
		}
	}
	return false
}

// scanRootOperand extracts the directory a scan is rooted at. explicit is false
// when the command names no path, meaning it searches the working directory.
func scanRootOperand(cmd string, argv []string) (root string, explicit bool) {
	if cmd == "find" {
		// find PATH... [expression]; paths precede the first primary.
		for _, a := range argv[1:] {
			if strings.HasPrefix(a, "-") || a == "(" || a == "!" {
				break
			}
			if _, broad := isScanRoot(a); broad {
				return a, true
			}
			root, explicit = a, true
		}
		return root, explicit
	}

	// grep/rg/ag/ack/fd/ls: operands are the non-flag tokens. For grep-likes the
	// first operand is the pattern, so any path-looking operand after it counts;
	// checking every operand is safe because a broad root is never a pattern
	// anyone means literally.
	for _, a := range argv[1:] {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if _, broad := isScanRoot(a); broad {
			return a, true
		}
		if strings.HasPrefix(a, "/") || strings.HasPrefix(a, "~") || strings.HasPrefix(a, "$HOME") {
			root, explicit = a, true
		}
	}
	return root, explicit
}

// absoluteCdTarget returns the absolute directory a `cd` moves to, or "" when
// the target is relative (which keeps us inside the worktree) or unknown.
func absoluteCdTarget(argv []string) string {
	for _, a := range argv[1:] {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if strings.HasPrefix(a, "/") || strings.HasPrefix(a, "~") || strings.HasPrefix(a, "$HOME") || strings.HasPrefix(a, "${HOME}") {
			return a
		}
		return ""
	}
	return ""
}

// stripLeadingAssignments drops `FOO=bar` prefixes and wrappers that shift the
// real command one token to the right.
func stripLeadingAssignments(argv []string) []string {
	for len(argv) > 0 {
		head := argv[0]
		if i := strings.IndexByte(head, '='); i > 0 && !strings.ContainsAny(head[:i], "/ \t") {
			argv = argv[1:]
			continue
		}
		switch path.Base(head) {
		case "sudo", "env", "nohup", "time", "nice", "xargs", "timeout":
			argv = argv[1:]
			continue
		}
		break
	}
	return argv
}

// splitShellSegments splits on the operators that separate commands, ignoring
// separators inside quotes.
func splitShellSegments(command string) []string {
	var segments []string
	var cur strings.Builder
	var quote byte
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			segments = append(segments, s)
		}
		cur.Reset()
	}
	for i := 0; i < len(command); i++ {
		c := command[i]
		if quote != 0 {
			if c == quote && (i == 0 || command[i-1] != '\\') {
				quote = 0
			}
			cur.WriteByte(c)
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
			cur.WriteByte(c)
		case ';', '|', '&', '\n':
			flush()
			// Consume the second char of && and ||.
			if i+1 < len(command) && command[i+1] == c {
				i++
			}
		case '$':
			// Command substitution: $(...) starts a new command context.
			if i+1 < len(command) && command[i+1] == '(' {
				flush()
				i++
			} else {
				cur.WriteByte(c)
			}
		case '(', ')', '`':
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return segments
}

// tokenizeShell splits a segment into argv, stripping one level of quoting.
func tokenizeShell(seg string) []string {
	var tokens []string
	var cur strings.Builder
	var quote byte
	started := false
	flush := func() {
		if started {
			tokens = append(tokens, cur.String())
		}
		cur.Reset()
		started = false
	}
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		if quote != 0 {
			if c == quote {
				quote = 0
				continue
			}
			cur.WriteByte(c)
			started = true
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
			started = true
		case ' ', '\t':
			flush()
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	flush()
	return tokens
}
