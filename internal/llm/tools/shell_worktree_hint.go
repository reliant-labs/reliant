// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"path"
	"strings"
)

// Agents asked to work on a separate branch reach for `git worktree add
// ../<repo>-<feature>` out of habit. Such a checkout is invisible to the
// Reliant UI: no worktree row, no chat can be bound to it, and nothing locks it
// against or reclaims it in cleanup. The worktree tool makes the workspace the
// UI makes, so agents are steered towards it — softly, never by refusal:
//
//   - the worktree tool's description and a system-prompt line say so up front;
//   - when a command runs `git worktree add` anyway, the shell result carries
//     a one-line suggestion (WorktreeAddSuggestion) after the command has run;
//   - a user's or project's memory that names where worktrees go wins: a
//     checkout under a root it names gets no suggestion (WorktreeRootsFromMemory),
//     and the suggestion itself defers to such a rule.
//
// Users with their own worktree layout (a dedicated directory every agent is
// told about in reliant.md) must be able to keep it, which is why this is a
// suggestion and not a guard. A checkout in a temporary directory gets none
// either: inspecting an old commit is a legitimate throwaway.

// worktreeAddSuggestion is the one line appended to a shell result whose
// command made a worktree somewhere Reliant does not know about.
const worktreeAddSuggestion = `Suggestion: a checkout made with git worktree add is invisible to the Reliant UI (the user cannot open a chat on it, and nothing locks or cleans it up). The worktree tool makes the workspace the UI shows — {"action": "create", "name": "<name>"}, via load_tool(name="worktree") if it is not loaded — and spawn(worktree="<name>") runs a sub-agent in it. If the user's or project's memory (reliant.md) says where worktrees go, follow that instead.`

// WorktreeAddTargets returns where each `git worktree add` in command puts its
// checkout, made absolute against workingDir and any `cd` or `git -C` before
// it. Home-relative paths stay home-relative (`~/...`): the machine's home is
// not known here. Checkouts in a temporary directory are left out. A `git
// worktree add` whose path cannot be read yields "". A command that makes no
// worktree returns nil.
func WorktreeAddTargets(command, workingDir string) []string {
	command = strings.NewReplacer("\\\r\n", " ", "\\\n", " ").Replace(command)
	base := workingDir
	var targets []string
	for _, seg := range splitShellSegments(command) {
		argv := stripLeadingAssignments(tokenizeShell(seg))
		if len(argv) == 0 {
			continue
		}
		switch path.Base(argv[0]) {
		case "cd":
			if len(argv) > 1 {
				base = joinShellPath(base, argv[1])
			}
			continue
		case "git":
		default:
			continue
		}
		dir, rest := gitInvocation(argv[1:], base)
		if len(rest) < 2 || rest[0] != "worktree" || rest[1] != "add" {
			continue
		}
		target := worktreeAddPath(rest[2:])
		if target == "" {
			targets = append(targets, "")
			continue
		}
		if isTempPath(target) {
			continue
		}
		target = joinShellPath(dir, target)
		if isTempPath(target) {
			continue
		}
		targets = append(targets, target)
	}
	return targets
}

// WorktreeAddSuggestion is the suggestion for a command that made worktrees at
// targets (WorktreeAddTargets), or "" when there is nothing to suggest: no
// worktree was made, or every one is under a root the user's memory names.
func WorktreeAddSuggestion(targets, memoryRoots []string) string {
	for _, target := range targets {
		if !underAnyRoot(target, memoryRoots) {
			return worktreeAddSuggestion
		}
	}
	return ""
}

// WithShellSuggestion adds suggestion to a shell result's JSON envelope, so the
// result stays one valid ShellOutput. Content that is not a foreground shell
// result — a refusal, a transport error, a backgrounded command — is returned
// unchanged: there is no command output to annotate.
func WithShellSuggestion(content, suggestion string) string {
	if suggestion == "" {
		return content
	}
	var out ShellOutput
	dec := json.NewDecoder(strings.NewReader(content))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil || dec.More() {
		return content
	}
	out.Suggestion = suggestion
	encoded, err := json.Marshal(out)
	if err != nil {
		return content
	}
	return string(encoded)
}

// WorktreeRootsFromMemory returns the directories the given memory documents
// (reliant.md, CLAUDE.md and the like) say worktrees go in. A rule is read
// from any paragraph or list item that mentions worktrees: each path in it
// that starts at `~`, `$HOME` or `/` is a root, cut at its first placeholder
// (`~/wt/<name>` is the root `~/wt`). A path in a negated clause ("never
// /tmp", "do not use ~/projects") is not one.
func WorktreeRootsFromMemory(docs ...string) []string {
	seen := map[string]bool{}
	var roots []string
	for _, doc := range docs {
		for _, block := range memoryBlocks(doc) {
			if !mentionsWorktrees(block.text) && !mentionsWorktrees(block.heading) {
				continue
			}
			for _, root := range rootsInBlock(block.text) {
				if !seen[root] {
					seen[root] = true
					roots = append(roots, root)
				}
			}
		}
	}
	return roots
}

func mentionsWorktrees(s string) bool {
	return strings.Contains(strings.ToLower(s), "worktree")
}

// memoryBlock is one paragraph or list item of a memory document, with the
// heading it sits under: a section titled "Worktrees" is about worktrees
// even where its items do not repeat the word.
type memoryBlock struct {
	heading string
	text    string
}

// memoryBlocks splits a markdown document into paragraphs and list items: a
// rule wrapped over several lines is still one block.
func memoryBlocks(doc string) []memoryBlock {
	var blocks []memoryBlock
	var heading string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			blocks = append(blocks, memoryBlock{heading: heading, text: strings.Join(cur, " ")})
			cur = nil
		}
	}
	for _, line := range strings.Split(doc, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			flush()
			continue
		case strings.HasPrefix(trimmed, "#"):
			flush()
			heading = trimmed
			continue
		case strings.HasPrefix(trimmed, "- "), strings.HasPrefix(trimmed, "* "),
			strings.HasPrefix(trimmed, "+ "), startsWithOrdinal(trimmed):
			flush()
		}
		cur = append(cur, trimmed)
	}
	flush()
	return blocks
}

func startsWithOrdinal(s string) bool {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i > 0 && i+1 < len(s) && (s[i] == '.' || s[i] == ')') && s[i+1] == ' '
}

// memoryPathDelims separate a path from the prose and markdown around it.
const memoryPathDelims = " \t`'\"()[],;|*"

// rootsInBlock extracts the root paths of one memory block.
func rootsInBlock(block string) []string {
	var roots []string
	clauseStart := 0
	for i := 0; i < len(block); {
		if strings.IndexByte(memoryPathDelims, block[i]) >= 0 {
			if endsClause(block, i) {
				clauseStart = i + 1
			}
			i++
			continue
		}
		end := i
		for end < len(block) && strings.IndexByte(memoryPathDelims, block[end]) < 0 {
			end++
		}
		if root := memoryRoot(block[i:end]); root != "" && !negated(block[clauseStart:i]) {
			roots = append(roots, root)
		}
		for j := i; j < end; j++ {
			if endsClause(block, j) {
				clauseStart = j + 1
			}
		}
		i = end
	}
	return roots
}

// endsClause reports whether block[i] is punctuation that ends a clause: a
// negation before it does not reach a path after it.
func endsClause(block string, i int) bool {
	return strings.IndexByte(".;!?,", block[i]) >= 0 && (i+1 == len(block) || strings.IndexByte(" `*", block[i+1]) >= 0)
}

// memoryRoot turns a token from a memory file into a root, or "" when it is
// not a usable path.
func memoryRoot(token string) string {
	token = strings.TrimRight(token, ".:!?")
	if token == "" {
		return ""
	}
	var rest string
	switch {
	case strings.HasPrefix(token, "~/"):
		rest = token[1:]
	case strings.HasPrefix(token, "$HOME/"):
		rest = token[len("$HOME"):]
	case strings.HasPrefix(token, "${HOME}/"):
		rest = token[len("${HOME}"):]
	case strings.HasPrefix(token, "/"):
		rest = token
	default:
		return ""
	}
	// Cut at the first placeholder or glob: `<name>`, `{id}`, `*`, `$NAME`.
	if cut := strings.IndexAny(rest, "<{*$"); cut >= 0 {
		rest = rest[:cut]
	}
	rest = path.Clean("/" + rest)
	if rest == "/" {
		return ""
	}
	if token[0] == '/' {
		return rest
	}
	return "~" + rest
}

var negations = []string{"never", "not ", "n't", "avoid", "instead of", "rather than", "except"}

func negated(clause string) bool {
	clause = strings.ToLower(clause)
	for _, n := range negations {
		if strings.Contains(clause, n) {
			return true
		}
	}
	return false
}

// joinShellPath resolves p the way a shell in dir would, keeping a home
// prefix symbolic. A path built from other variables is returned as written.
func joinShellPath(dir, p string) string {
	switch {
	case p == "~" || strings.HasPrefix(p, "~/"):
		return "~" + path.Clean("/"+strings.TrimPrefix(p, "~"))
	case p == "$HOME" || strings.HasPrefix(p, "$HOME/"):
		return "~" + path.Clean("/"+strings.TrimPrefix(p, "$HOME"))
	case p == "${HOME}" || strings.HasPrefix(p, "${HOME}/"):
		return "~" + path.Clean("/"+strings.TrimPrefix(p, "${HOME}"))
	case strings.HasPrefix(p, "/"):
		return path.Clean(p)
	case strings.Contains(p, "$") || dir == "":
		return p
	case dir == "~" || strings.HasPrefix(dir, "~/"):
		return "~" + path.Clean(strings.TrimPrefix(dir, "~")+"/"+p)
	default:
		return path.Clean(dir + "/" + p)
	}
}

// homeRelative returns p relative to the home directory it is under, for the
// spellings a home takes: ~, /Users/<u> (macOS), /home/<u> (Linux), /root.
func homeRelative(p string) (string, bool) {
	switch {
	case p == "~":
		return "", true
	case strings.HasPrefix(p, "~/"):
		return p[2:], true
	case strings.HasPrefix(p, "/root/"):
		return p[len("/root/"):], true
	}
	for _, prefix := range []string{"/Users/", "/home/"} {
		if rest, ok := strings.CutPrefix(p, prefix); ok {
			if _, after, found := strings.Cut(rest, "/"); found {
				return after, true
			}
		}
	}
	return "", false
}

func underAnyRoot(target string, roots []string) bool {
	if target == "" {
		return false
	}
	for _, root := range roots {
		if underRoot(target, root) {
			return true
		}
	}
	return false
}

func underRoot(target, root string) bool {
	t, targetInHome := homeRelative(target)
	r, rootInHome := homeRelative(root)
	if targetInHome != rootInHome {
		return false
	}
	if !targetInHome {
		t, r = target, root
	}
	return r != "" && (t == r || strings.HasPrefix(t, r+"/"))
}

// gitGlobalOptionsTakingValue are git's options before the subcommand that
// consume the next argument when not spelled --opt=value.
var gitGlobalOptionsTakingValue = map[string]bool{
	"-C": true, "-c": true, "--git-dir": true, "--work-tree": true,
	"--namespace": true, "--super-prefix": true, "--config-env": true,
	"--attr-source": true,
}

// gitInvocation drops git's global options, returning the directory git runs
// in (base, moved by each -C) and the subcommand with its arguments.
func gitInvocation(args []string, base string) (string, []string) {
	dir := base
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			return dir, args[i:]
		}
		if gitGlobalOptionsTakingValue[a] {
			if a == "-C" && i+1 < len(args) {
				dir = joinShellPath(dir, args[i+1])
			}
			i++
		}
	}
	return dir, nil
}

// worktreeAddPath returns the <path> operand of `git worktree add [options]
// <path> [<commit-ish>]`, or "" when there is none.
func worktreeAddPath(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		case a == "-b" || a == "-B" || a == "--reason":
			i++ // the option's value, not the path
		case strings.HasPrefix(a, "-"):
		default:
			return a
		}
	}
	return ""
}

// isTempPath reports whether p names a location under a temporary directory.
// Paths built from variables are judged only by the variables that name the
// temp dir; anything else could be the project folder.
func isTempPath(p string) bool {
	for _, prefix := range []string{
		"/tmp/", "/private/tmp/", "/var/tmp/", "/var/folders/", "/private/var/folders/",
		"$TMPDIR", "${TMPDIR", "$TMP/", "${TMP}", "${TMP:", "$TEMP/", "${TEMP}", "${TEMP:",
		"$RUNNER_TEMP", "${RUNNER_TEMP",
		"$env:TEMP", "$env:TMP", "%TEMP%", "%TMP%",
	} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	if strings.Contains(p, "$(mktemp") || strings.Contains(p, "`mktemp") {
		return true
	}
	return strings.Contains(strings.ToLower(strings.ReplaceAll(p, "/", `\`)), `\appdata\local\temp\`)
}
