// Copyright (c) 2025 Reliant Labs
package handlers

import (
	_ "embed"
	"strings"
)

// reliantCapabilities is the "What Reliant can do" routing guide: which Reliant
// feature fits which kind of request (triggered workflows, integrations,
// approvals, forge), and when forge is the default for something that will be
// deployed — frontend-only work included.
//
// It is rendered by the harness into every turn's base system prompt
// (getSystemPrompts), for every project, chat and sub-agent, local or cloud.
// It used to live only in the reliant.md a new project was scaffolded with, and
// that file is written ONCE: a project created before the guide existed, or
// before it changed, never got the current text, so its agents never learned
// that forge serves a landing page as well as a backend. Rendering it live is
// the same choice the forge framework memory made for the same reason
// (daemonruntime/forge_memory.go): a copy on disk drifts, the binary does not.
//
// It is embedded, static bytes and sits in the first system block, so it is
// part of the cached prompt prefix and costs no cache misses turn to turn.
//
//go:embed guidance/reliant_capabilities.md
var reliantCapabilities string

// scaffoldedCapabilitiesHeading opens the copy of the guide that older
// scaffolded reliant.md files still carry.
const scaffoldedCapabilitiesHeading = "## What Reliant can do"

// withoutScaffoldedCapabilities drops a stale "## What Reliant can do" section
// from a memory file, so the live guide in the system prompt is the only copy
// the model reads. Without this, a project scaffolded before the guide moved
// would carry two versions that disagree (the old one calls a static site a
// forge footnote), and the user memory block is the one told "you must
// adhere".
//
// The section runs from its heading to the next level-1 or level-2 heading (its
// own "### forge" subsection goes with it) or the end of the file. The heading
// is matched exactly, so a section the user wrote under another name is left
// alone. A user who edited the scaffolded section in place loses those edits
// from the prompt: the heading is the only marker the scaffold ever wrote, and
// keeping a hand-edited copy would mean keeping every unedited stale one too.
// The file on disk is never touched.
func withoutScaffoldedCapabilities(md string) string {
	if !strings.Contains(md, scaffoldedCapabilitiesHeading) {
		return md
	}
	var b strings.Builder
	b.Grow(len(md))
	skipping := false
	for _, line := range strings.SplitAfter(md, "\n") {
		heading := strings.TrimRight(line, " \t\r\n")
		if heading == scaffoldedCapabilitiesHeading {
			skipping = true
			continue
		}
		if skipping && (strings.HasPrefix(heading, "# ") || strings.HasPrefix(heading, "## ")) {
			skipping = false
		}
		if !skipping {
			b.WriteString(line)
		}
	}
	return b.String()
}
