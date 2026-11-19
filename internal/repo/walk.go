// Copyright (c) 2025 Reliant Labs
package repo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"time"
)

// walkReadBatch is the most entries a bounded walk asks a directory for at
// once. The walk checks its context and deadline between batches, and never
// lists further than its entry budget allows, so a directory of a million
// entries costs at most one batch past a bound: not a read and sort of the
// whole listing before any bound is checked, which is what fs.WalkDir and
// filepath.WalkDir do.
const walkReadBatch = 256

// errWalkStopped ends a walk a bound stopped; walkBounds.stopped says which.
var errWalkStopped = errors.New("walk stopped")

// walkBounds are the bounds one directory walk checks (Discover's and
// FindForgeApps'): its context, its deadline and its entry budget.
//
// The budget is charged per entry VISITED, exactly as the walks charged it
// when they were fs.WalkDir callbacks (the root, every entry of every
// directory entered, and a directory again when listing it failed), in the
// same lexical depth-first order. What is new is that a listing is bounded
// too (list): a directory is read only as far as the budget could still
// visit, in batches with the context and deadline checked between them.
//
// So a walk stops where the WalkDir walk stopped, with one exception: when
// the budget runs out inside a directory with more entries than the budget
// had left, the walk now stops on reaching that directory, where the old
// walk first read the whole listing and visited its lexically first entries.
// Finding those would need the whole listing, the read this bounds. What a
// stopped walk found is then a prefix of what the old one did, and it is
// flagged partial as before.
type walkBounds struct {
	ctx        context.Context
	deadline   time.Time
	maxEntries int

	entries int
	stopped string
}

// bounded reports whether the context or deadline has stopped the walk, or
// a bound already did, recording which.
func (b *walkBounds) bounded() bool {
	if b.stopped != "" {
		return true
	}
	if b.ctx.Err() != nil {
		b.stopped = "the request ended"
		return true
	}
	if !b.deadline.IsZero() && !time.Now().Before(b.deadline) {
		b.stopped = "it ran out of time"
		return true
	}
	return false
}

// visit charges one entry to the budget, after checking the context and the
// deadline, and reports whether the walk must stop.
func (b *walkBounds) visit() bool {
	if b.bounded() {
		return true
	}
	if b.entries++; b.entries > b.maxEntries {
		b.budgetSpent()
		return true
	}
	return false
}

func (b *walkBounds) budgetSpent() {
	b.stopped = fmt.Sprintf("it looked at %d entries", b.maxEntries)
}

// list lists the open directory f sorted by name, as os.ReadDir and
// fs.ReadDir do, reading it in batches of at most walkReadBatch entries and
// checking the context and deadline before every batch.
//
// It reads at most one entry more than the budget has left. A directory
// longer than that is one the walk could never finish visiting: the budget
// would run out inside it, so the walk stops here (errWalkStopped), without
// reading the rest or visiting the part it read. That keeps a stopped walk
// deterministic: a partial listing is in whatever order the filesystem
// returned it, and is never walked.
//
// A listing that fails is returned as its error, with nothing listed,
// as the WalkDir walks skipped a directory they could not list.
func (b *walkBounds) list(f fs.File, name string) ([]fs.DirEntry, error) {
	rd, ok := f.(fs.ReadDirFile)
	if !ok {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: errors.New("not implemented")}
	}
	room := b.maxEntries - b.entries
	var list []fs.DirEntry
	for {
		if b.bounded() {
			return nil, errWalkStopped
		}
		batch, err := rd.ReadDir(min(walkReadBatch, room-len(list)+1))
		list = append(list, batch...)
		if len(list) > room {
			b.budgetSpent()
			return nil, errWalkStopped
		}
		if errors.Is(err, io.EOF) || (err == nil && len(batch) == 0) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name() < list[j].Name() })
	return list, nil
}
