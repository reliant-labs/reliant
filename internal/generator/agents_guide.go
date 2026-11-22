package generator

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/checksums"
)

// AgentsGuidePath is the project guide every harness reads (agentFiles): the
// one rendered copy of reliant.md.tmpl on disk.
const AgentsGuidePath = "AGENTS.md"

// projectNotesHeading divides AGENTS.md into its two owners. Everything
// above it is forge's guide, re-rendered by `forge generate` from the pinned
// forge's template; the heading and everything below it are the project's
// notes, which forge never reads, rewrites or reorders. The template invites
// notes there (`<!-- Add project-specific context ... -->`), so it is the
// boundary the file already declares.
const projectNotesHeading = "## Project Notes"

// guideHeaderRE is the first line of every guide forge renders. A file
// without it is not forge's guide at all: a project's own AGENTS.md, which
// forge leaves alone and says nothing about.
var guideHeaderRE = regexp.MustCompile(`\A<!-- forge:version=\d+ -->\r?\n`)

// guideCLIRE finds the CLI the guide names in its opening paragraph ("driven
// by the `reliant forge` CLI"; older guides said "managed by").
var guideCLIRE = regexp.MustCompile("by the `([^`\n]+)` CLI")

// SplitAgentsGuide splits a guide at its first `## Project Notes` line:
// forge is everything before that line, notes the line and everything after.
// ok is false when the file carries no forge header or no such line, so
// forge cannot tell its guide from the project's notes.
func SplitAgentsGuide(content []byte) (forge, notes []byte, ok bool) {
	if !guideHeaderRE.Match(content) {
		return nil, nil, false
	}
	for off := 0; off < len(content); {
		line := content[off:]
		end := bytes.IndexByte(line, '\n')
		if end >= 0 {
			line = line[:end]
		}
		if string(bytes.TrimRight(line, "\r")) == projectNotesHeading {
			return content[:off], content[off:], true
		}
		if end < 0 {
			break
		}
		off += end + 1
	}
	return nil, nil, false
}

// AgentsGuideRenders renders this forge's guide for the project with the
// given CLI name: current is what `forge project memory` prints (the guide
// of the project as it is), scaffold is what `forge project new` writes
// before the project has a tree to describe. Both are full guides, Project
// Notes placeholder included.
type AgentsGuideRenders func(cli string) (current, scaffold []byte, err error)

// AgentsGuideOutcome is what RefreshAgentsGuide did.
type AgentsGuideOutcome int

const (
	// GuideLeftAlone means nothing for forge to do — no AGENTS.md, one that is
	// not forge's guide (no forge:version header), one disowned or excluded,
	// or one whose guide already is this forge's.
	GuideLeftAlone AgentsGuideOutcome = iota
	// GuideRefreshed means the guide was re-rendered and the notes kept.
	GuideRefreshed
	// GuideDrifted means the guide differs from this forge's and forge has no
	// proof the difference is not the user's, so the file was not touched.
	// AgentsGuideResult.Reason says why.
	GuideDrifted
)

// AgentsGuideResult reports one RefreshAgentsGuide call.
type AgentsGuideResult struct {
	Outcome AgentsGuideOutcome
	// Reason, for GuideDrifted, says why forge would not rewrite the guide.
	Reason string
}

// RefreshAgentsGuide keeps root's AGENTS.md guide in step with this forge:
// the guide above `## Project Notes` is re-rendered, and the notes from that
// heading down are kept byte for byte.
//
// AGENTS.md is a scaffold the user owns, so the rewrite needs proof that
// what it replaces is forge's own text. The guide part is replaced only
// when it is exactly what forge last wrote (its hash in the birth ledger,
// recorded at every write) or exactly what this forge's `forge project new`
// writes (a project scaffolded by this forge, before its first generate).
// Anything else — a guide edited by hand, or one an older forge wrote
// before it kept a record — is reported as drift and left alone: a guide
// that is a release behind costs a stale paragraph, a rewritten one could
// cost an afternoon's edits. A guide that already matches this forge's
// render is adopted: its hash is recorded, so the next forge's guide
// replaces it.
//
// The guide keeps the CLI name it was written with (`forge`, or `reliant
// forge`), so two people generating through different binaries do not flip
// it back and forth; fallbackCLI is used when the file names none.
//
// disowned (`forge project disown AGENTS.md`) and an excluded path leave the
// file alone. Absent, the file stays absent: generate never creates it.
func RefreshAgentsGuide(root, fallbackCLI string, disowned bool, render AgentsGuideRenders) (AgentsGuideResult, error) {
	if disowned || checksums.SkipExcluded(root, AgentsGuidePath) {
		return AgentsGuideResult{}, nil
	}
	full := filepath.Join(root, AgentsGuidePath)
	info, err := os.Stat(full)
	if os.IsNotExist(err) {
		return AgentsGuideResult{}, nil
	} else if err != nil {
		return AgentsGuideResult{}, fmt.Errorf("stat %s: %w", AgentsGuidePath, err)
	}
	content, err := os.ReadFile(full)
	if err != nil {
		return AgentsGuideResult{}, fmt.Errorf("read %s: %w", AgentsGuidePath, err)
	}
	if !guideHeaderRE.Match(content) {
		return AgentsGuideResult{}, nil // the project's own AGENTS.md
	}
	guide, notes, ok := SplitAgentsGuide(content)
	if !ok {
		return AgentsGuideResult{Outcome: GuideDrifted,
			Reason: "it has no `" + projectNotesHeading + "` heading, so forge cannot tell its guide from your notes"}, nil
	}

	cli := fallbackCLI
	if m := guideCLIRE.FindSubmatch(guide); m != nil {
		cli = string(m[1])
	}
	current, scaffold, err := render(cli)
	if err != nil {
		return AgentsGuideResult{}, fmt.Errorf("render the guide for %s: %w", AgentsGuidePath, err)
	}
	want, _, ok := SplitAgentsGuide(current)
	if !ok {
		return AgentsGuideResult{}, fmt.Errorf("the rendered guide has no forge header or no %q heading", projectNotesHeading)
	}
	if bytes.Equal(guide, want) {
		checksums.RecordScaffoldForgePart(root, AgentsGuidePath, guide)
		return AgentsGuideResult{}, nil
	}
	scaffolded, _, _ := SplitAgentsGuide(scaffold)
	if !checksums.ScaffoldForgePartIsPristine(root, AgentsGuidePath, guide) && !bytes.Equal(guide, scaffolded) {
		reason := "forge has no record of writing it (an older forge scaffolded it), so it cannot tell its guide from your edits"
		if checksums.ScaffoldForgePartRecorded(root, AgentsGuidePath) {
			reason = "it was edited above `" + projectNotesHeading + "` since forge last wrote it"
		}
		return AgentsGuideResult{Outcome: GuideDrifted, Reason: reason}, nil
	}

	out := make([]byte, 0, len(want)+len(notes))
	out = append(append(out, want...), notes...)
	checksums.RecordPreWrite(root, AgentsGuidePath) // journaled: a failed run restores it
	if err := atomicfile.WriteFile(full, out, info.Mode().Perm()); err != nil {
		return AgentsGuideResult{}, fmt.Errorf("write %s: %w", AgentsGuidePath, err)
	}
	checksums.RecordScaffoldForgePart(root, AgentsGuidePath, want)
	return AgentsGuideResult{Outcome: GuideRefreshed}, nil
}
