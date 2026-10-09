// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"path"
	"strings"
)

// Agents asked to work on a separate branch reach for `git worktree add
// ../<repo>-<feature>` out of habit. Every such checkout lands beside the repo,
// which is inside the user's project folder, and from there it costs three
// things nothing else does:
//
//   - The Reliant UI never sees it: no worktree row, no chat can be bound to
//     it, and it is never locked against or reclaimed by cleanup.
//   - It lives wherever the agent felt like putting it, while a worktree made
//     from the UI lives under ~/.reliant/worktrees/. The user is left with two
//     layouts and a project folder full of siblings (~30 were measured in one
//     project on 2026-10-08).
//   - A checkout inside the project root looks like one more repository of the
//     project, so its memory files and skills are added to every later chat's
//     system prompt.
//
// The worktree tool creates the same workspace the UI does, so a hand-made
// checkout is refused here with a pointer to it. A checkout in a temporary
// directory is allowed: inspecting an old commit is a legitimate throwaway and
// leaves nothing behind in the project.

// gitWorktreeAddRefusal returns a non-empty refusal when command creates a git
// worktree anywhere but a temporary directory, and "" otherwise. The refusal
// holds either way; only its remedy depends on caps, the turn's capability
// set: the worktree tool is recommended only when load_tool would grant it.
func gitWorktreeAddRefusal(command string, caps *Capabilities) string {
	command = strings.NewReplacer("\\\r\n", " ", "\\\n", " ").Replace(command)
	for _, seg := range splitShellSegments(command) {
		argv := stripLeadingAssignments(tokenizeShell(seg))
		if len(argv) == 0 || path.Base(argv[0]) != "git" {
			continue
		}
		rest := gitSubcommandArgs(argv[1:])
		if len(rest) < 2 || rest[0] != "worktree" || rest[1] != "add" {
			continue
		}
		if target := worktreeAddPath(rest[2:]); isTempPath(target) {
			continue
		}
		if why := caps.LoadRefusal(ToolWorktree); why != "" {
			return fmt.Sprintf(worktreeAddUnavailableTemplate, seg, why)
		}
		return fmt.Sprintf(worktreeAddRefusalTemplate, seg)
	}
	return ""
}

// gitGlobalOptionsTakingValue are git's options before the subcommand that
// consume the next argument when not spelled --opt=value.
var gitGlobalOptionsTakingValue = map[string]bool{
	"-C": true, "-c": true, "--git-dir": true, "--work-tree": true,
	"--namespace": true, "--super-prefix": true, "--config-env": true,
	"--attr-source": true,
}

// gitSubcommandArgs drops git's global options, returning the subcommand and
// its arguments.
func gitSubcommandArgs(args []string) []string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			return args[i:]
		}
		if gitGlobalOptionsTakingValue[a] {
			i++
		}
	}
	return nil
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

const worktreeAddRefusalTemplate = `refused: %q creates a git worktree by hand.

A checkout made with git worktree add is invisible to Reliant: the user cannot see it in the UI or open a chat on it, nothing locks it or cleans it up, and one placed beside the repository sits inside the user's project folder, where it is picked up as another repository of the project.

Create it with the worktree tool instead (load it with load_tool(name="worktree") if it is not loaded yet):
  {"action": "create", "name": "<short-name>"}
It creates the same workspace the UI does — under ~/.reliant/worktrees/, one checkout per repository of the project, each on its own branch — and returns its paths. To work in it, hand it to a sub-agent with spawn(worktree="<short-name>").

A throwaway checkout you remove before you finish (to inspect an old commit, say) may go in a temporary directory:
  git worktree add "${TMPDIR:-/tmp}/<name>" <commit>`

const worktreeAddUnavailableTemplate = `refused: %q creates a git worktree by hand.

A checkout made with git worktree add is invisible to Reliant: the user cannot see it in the UI or open a chat on it, nothing locks it or cleans it up, and one placed beside the repository sits inside the user's project folder, where it is picked up as another repository of the project.

Creating a Reliant worktree is not available in this step: %s
Keep working in the current checkout, or ask the user to create a worktree from the Reliant UI and continue there.

A throwaway checkout you remove before you finish (to inspect an old commit, say) may go in a temporary directory:
  git worktree add "${TMPDIR:-/tmp}/<name>" <commit>`
