// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"path"
	"strings"
)

// A command that deletes recursively, aimed at the filesystem root, a system
// directory, or a home directory, is never what anyone meant. On 2026-10-06
// builtin parallel-compete rendered `rsync -av --delete <winner>/ "/"` from an
// unset template variable and ran it as the user for 172s, deleting files
// across the machine before it was killed.
//
// The workflow and its runtime are fixed at the source; this is the backstop
// for whatever renders the next one. It is deliberately narrow — two tools, and
// only the targets isScanRoot already names — because a general shell
// sanitizer would refuse real work and still miss the next spelling. Below
// those roots, both commands run unchanged.

// destructiveRootRefusal returns a non-empty refusal when command would
// recursively delete under a root, system or home directory, and "" otherwise.
func destructiveRootRefusal(command string) string {
	// A backslash-newline continues the command on the next line; join them so
	// a destination on its own line stays part of its command.
	command = strings.NewReplacer("\\\r\n", " ", "\\\n", " ").Replace(command)
	for _, seg := range splitShellSegments(command) {
		argv := stripLeadingAssignments(tokenizeShell(seg))
		if len(argv) == 0 {
			continue
		}
		var targets []string
		switch path.Base(argv[0]) {
		case "rsync":
			if !rsyncDeletes(argv) {
				continue
			}
			// rsync SRC... DEST: the destination is the last operand.
			if operands := shellOperands(argv); len(operands) >= 2 {
				targets = operands[len(operands)-1:]
			}
		case "rm":
			if !rmRecursive(argv) {
				continue
			}
			targets = shellOperands(argv)
		default:
			continue
		}
		for _, target := range targets {
			if root, broad := isScanRoot(target); broad {
				return fmt.Sprintf(destructiveRefusalTemplate, seg, root)
			}
		}
	}
	return ""
}

// shellOperands returns argv's non-flag arguments, skipping line-continuation
// residue.
func shellOperands(argv []string) []string {
	var operands []string
	for _, a := range argv[1:] {
		if a == "" || a == "\\" || strings.HasPrefix(a, "-") {
			continue
		}
		operands = append(operands, a)
	}
	return operands
}

// rsyncDeletes reports whether an rsync invocation removes files at the
// destination: --delete and its variants (--delete-after, --del, ...).
func rsyncDeletes(argv []string) bool {
	for _, a := range argv[1:] {
		if a == "--del" || strings.HasPrefix(a, "--delete") {
			return true
		}
	}
	return false
}

// rmRecursive reports whether an rm invocation removes directory trees.
func rmRecursive(argv []string) bool {
	for _, a := range argv[1:] {
		if a == "--recursive" {
			return true
		}
		if len(a) > 1 && a[0] == '-' && !strings.HasPrefix(a, "--") && strings.ContainsAny(a[1:], "rR") {
			return true
		}
	}
	return false
}

const destructiveRefusalTemplate = `refused: %q would recursively delete under %s.

A recursive delete or a sync-with-delete aimed at the filesystem root, a system directory or a home directory is never intended — it is what an empty or unexpanded path variable looks like by the time it reaches the shell.

If you meant a directory inside the project, name it explicitly with a relative path (e.g. dist/, ./build). If a path came from a variable, check that it is set before using it.`
