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
}

var (
	leftAloneMu sync.Mutex
	leftAlone   = map[string]string{}
)

// NoteLeftAlone records that forge did not write relPath this run, and why.
// The first reason recorded for a path wins.
func NoteLeftAlone(relPath, why string) {
	relPath = slashKey(relPath)
	leftAloneMu.Lock()
	defer leftAloneMu.Unlock()
	if _, ok := leftAlone[relPath]; !ok {
		leftAlone[relPath] = why
	}
}

// LeftAloneThisRun returns every path recorded by NoteLeftAlone, sorted.
func LeftAloneThisRun() []LeftAlone {
	leftAloneMu.Lock()
	defer leftAloneMu.Unlock()
	out := make([]LeftAlone, 0, len(leftAlone))
	for p, why := range leftAlone {
		out = append(out, LeftAlone{Path: p, Why: why})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func resetLeftAlone() {
	leftAloneMu.Lock()
	defer leftAloneMu.Unlock()
	leftAlone = map[string]string{}
}
