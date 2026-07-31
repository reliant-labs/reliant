// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"fmt"
	"hash/fnv"
	"path/filepath"
	"sort"

	"github.com/reliant-labs/reliant/internal/rctx"
)

// Concurrency control for the file-editing tools.
//
// edit, insert_at, edit_lines and move_code all work the same way: read the
// whole file through the daemon, splice the bytes in this process, write the
// whole file back. That sequence is a read-modify-write, and it runs
// concurrently with itself: ExecuteTools dispatches every tool call in one
// assistant message onto a pool of up to 10 goroutines
// (internal/workflow/runtime/activities/handlers/execute_tools.go), and the
// edit tool's own description tells the model to batch its edits that way. Two
// calls to one file both read the same bytes and the later write erases the
// earlier one — silently, with both calls reporting success.
//
// Two guards, because they cover different writers:
//
//  1. withPathLock serializes the read-modify-write inside this process. Every
//     tool call in a batch runs here, so this is what makes a batch of same-file
//     edits behave like the same edits issued one at a time: each one reads what
//     the previous one wrote. This is the whole fix for the batching case.
//
//  2. writeFileGuarded catches the writers the lock cannot see — another server
//     replica, a `bash sed`, a git checkout, a human in an editor. It asserts at
//     write time that the file still held the bytes the edit was computed from,
//     and turns a clobber into an error instead of a success.
//
// Note what is deliberately NOT used here: the modification-time check in
// write.go and move_code.go. It cannot cover this case at all — within a batch
// every caller stats the file before any of them writes, so they all see an
// unmodified file and all proceed. It is also unsound across the wire, since it
// compares a mod time from the daemon's host clock against a time.Now() taken
// on the server.

// pathLockStripes is the number of locks the path space is hashed onto. Striping
// keeps the table bounded — a per-path map keyed by user-supplied paths grows
// without limit in a long-lived multi-tenant server, and reclaiming entries
// needs refcounting that is easy to get wrong. The cost of striping is that two
// unrelated files can share a lock and serialize briefly; the critical section
// is a single read and a single write, so that is not worth avoiding.
const pathLockStripes = 256

// pathLock is a mutex that can be waited on with a context, so a tool call that
// is out of time fails cleanly instead of blocking past its own deadline.
type pathLock chan struct{}

func (l pathLock) acquire(ctx context.Context) error {
	select {
	case l <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l pathLock) release() { <-l }

var pathLocks = func() [pathLockStripes]pathLock {
	var t [pathLockStripes]pathLock
	for i := range t {
		t[i] = make(pathLock, 1)
	}
	return t
}()

func pathLockFor(path string) pathLock {
	h := fnv.New32a()
	_, _ = h.Write([]byte(filepath.Clean(path)))
	return pathLocks[h.Sum32()%pathLockStripes]
}

// withPathLock runs fn with exclusive access to path against the other file
// tools in this process. The whole read-modify-write must be inside fn.
func withPathLock(rctx *rctx.ToolContext, path string, fn func() (ToolResponse, error)) (ToolResponse, error) {
	return withPathLocks(rctx, []string{path}, fn)
}

// withPathLocks is the multi-file form, for tools that read and write more than
// one file in a single operation. Locks are taken in a fixed global order
// (stripe index) so two calls contending for the same pair of files cannot
// deadlock against each other.
func withPathLocks(rctx *rctx.ToolContext, paths []string, fn func() (ToolResponse, error)) (ToolResponse, error) {
	seen := make(map[uint32]pathLock, len(paths))
	order := make([]uint32, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		h := fnv.New32a()
		_, _ = h.Write([]byte(filepath.Clean(p)))
		idx := h.Sum32() % pathLockStripes
		if _, dup := seen[idx]; dup {
			continue
		}
		seen[idx] = pathLocks[idx]
		order = append(order, idx)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

	ctx := rctx.Context
	if ctx == nil {
		ctx = context.Background()
	}

	held := make([]pathLock, 0, len(order))
	release := func() {
		for i := len(held) - 1; i >= 0; i-- {
			held[i].release()
		}
	}
	for _, idx := range order {
		l := seen[idx]
		if err := l.acquire(ctx); err != nil {
			release()
			return NewTextErrorResponse(fmt.Sprintf(
				"timed out waiting for exclusive access to %s: another edit to this file is still in flight (%v). Nothing was written; retry the edit.",
				joinPaths(paths), err)), nil
		}
		held = append(held, l)
	}
	defer release()

	return fn()
}

// joinPaths renders the locked paths for the timeout message.
func joinPaths(paths []string) string {
	if len(paths) == 1 {
		return paths[0]
	}
	return fmt.Sprint(paths)
}

// ConcurrentModificationError reports that a file changed between the moment a
// tool read it and the moment it wrote its result back, so the write was
// computed from bytes that were no longer on disk.
type ConcurrentModificationError struct {
	Path string
	// Restored is true when the other writer's content was put back, leaving the
	// file exactly as they wrote it. False means the file is in a state the tool
	// could not undo, and the message says so.
	Restored bool
	Detail   string
}

func (e *ConcurrentModificationError) Error() string {
	if e.Restored {
		return fmt.Sprintf(
			"file %s was modified by something else between the read and the write, so this edit was computed from stale content. "+
				"The edit was NOT applied and the other writer's content has been restored. Re-read the file and reissue the edit.",
			e.Path)
	}
	return fmt.Sprintf(
		"file %s was modified by something else between the read and the write, so this edit was computed from stale content. "+
			"%s Re-read the file before doing anything else with it.",
		e.Path, e.Detail)
}

// writeFileGuarded writes newContent to path, but only counts it as applied if
// the file still held expectedOld at the instant of the write.
//
// The check needs no new daemon call and no new protocol: WriteFile already
// returns the content it replaced, read on the far side immediately before the
// write, and both the local and the remote daemon paths populate it. Comparing
// that against the bytes the edit was computed from is an exact test for "did
// somebody write in between" — exact where a mod-time or size comparison is
// only a heuristic.
//
// The write is not conditional at the daemon, so a detected conflict means the
// clobber already happened; the other writer's content is put straight back and
// the caller is told the edit did not apply. That leaves a window of one round
// trip in which the file holds the wrong bytes, which is the price of not
// changing the daemon protocol — a permanent silent loss traded for a
// millisecond-scale blip plus a loud error.
//
// Closing that window properly means a conditional write at the daemon
// (fs.write_file carrying the expected content hash, refused on mismatch). That
// is deliberately not done here: daemons run on user machines and can be older
// than the server, an old one would ignore an unknown field and skip the check —
// reintroducing exactly this class of silent failure — and a new command name
// needs a fallback that is either unsafe or breaks every old daemon. This
// function is the single place that upgrade would slot into.
func writeFileGuarded(rctx *rctx.ToolContext, path, expectedOld, newContent string) error {
	res, err := rctx.Daemon.WriteFile(rctx.Context, path, newContent)
	if err != nil {
		return err
	}
	if res == nil || res.OldContent == expectedOld {
		return nil
	}

	if res.Created {
		// The file was gone when we wrote, so our write recreated it. There is no
		// previous content to put back and deleting it again would be its own
		// data loss, so leave it and report the conflict.
		return &ConcurrentModificationError{
			Path:   path,
			Detail: "The file had been deleted by something else and this write recreated it with the edited content.",
		}
	}

	restore, restoreErr := rctx.Daemon.WriteFile(rctx.Context, path, res.OldContent)
	switch {
	case restoreErr != nil:
		return &ConcurrentModificationError{
			Path:   path,
			Detail: fmt.Sprintf("Restoring their content failed (%v), so the file currently holds this edit applied to stale content.", restoreErr),
		}
	case restore != nil && restore.OldContent != newContent:
		return &ConcurrentModificationError{
			Path:   path,
			Detail: "The file changed again while their content was being restored, so its current contents are not known.",
		}
	default:
		return &ConcurrentModificationError{Path: path, Restored: true}
	}
}
