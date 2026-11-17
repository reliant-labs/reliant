package checksums

import (
	"sort"
	"sync"
)

// left_alone.go — the per-run record of paths forge deliberately did NOT
// write, so the closing summary can name them.
//
// A refusal printed only at the step that made it scrolls away under the
// hundreds of lines a full generate prints, and the closing line then says
// "N files written" as though the run did everything it set out to. That is
// how a hand-written src/hooks/index.ts was overwritten with nothing in the
// summary to say so (reliant P9, C2): the fix is not only to refuse, but to
// make the refusal impossible to miss. Every writer that declines a path for
// a reason the user must act on records it here; the summary prints the
// list after its counts.

// LeftAlone is one path forge declined to write this run, and why.
type LeftAlone struct {
	// Path is project-relative and slash-separated.
	Path string
	// Why says what forge found and what to do about it, in one sentence.
	Why string
	// Detail is the exact edits, one printed line each, when one sentence
	// cannot carry them — the rows of a starter test a hook-kind flip left
	// stale, with the body that replaces each.
	Detail []string
}

var (
	leftAloneMu sync.Mutex
	leftAlone   = map[string]LeftAlone{}
)

// NoteLeftAlone records that forge did not write relPath this run, and why.
// The first reason recorded for a path wins.
func NoteLeftAlone(relPath, why string) {
	NoteLeftAloneDetail(relPath, why, nil)
}

// NoteLeftAloneDetail is NoteLeftAlone with the exact edits the user must
// make, printed under the reason.
func NoteLeftAloneDetail(relPath, why string, detail []string) {
	relPath = slashKey(relPath)
	leftAloneMu.Lock()
	defer leftAloneMu.Unlock()
	if _, ok := leftAlone[relPath]; !ok {
		leftAlone[relPath] = LeftAlone{Path: relPath, Why: why, Detail: detail}
	}
}

// LeftAloneThisRun returns every path recorded by NoteLeftAlone, sorted.
func LeftAloneThisRun() []LeftAlone {
	leftAloneMu.Lock()
	defer leftAloneMu.Unlock()
	out := make([]LeftAlone, 0, len(leftAlone))
	for _, l := range leftAlone {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func resetLeftAlone() {
	leftAloneMu.Lock()
	defer leftAloneMu.Unlock()
	leftAlone = map[string]LeftAlone{}
}
