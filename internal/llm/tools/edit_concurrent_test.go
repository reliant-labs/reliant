// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Concurrent same-file edits
//
// The edit tool's own description tells the model to issue several edit calls
// in one message, and ExecuteTools runs the calls in a batch on a pool of up to
// 10 goroutines (internal/workflow/runtime/activities/handlers/execute_tools.go).
// Every call that lands on the same file therefore races on the same
// read-modify-write sequence. These tests drive that batch shape directly.
// ---------------------------------------------------------------------------

// concurrentEditFixture builds a file of numbered lines and returns a runner
// that fires n edits at it concurrently — one per line — the way a single
// assistant message with n edit tool calls does.
func concurrentEditFixture(t *testing.T, n int) (string, func() []string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "batch.txt")

	lines := make([]string, 0, n)
	for i := 0; i < n; i++ {
		lines = append(lines, fmt.Sprintf("line %02d original", i))
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))

	chatID, thread := "concurrent-"+t.Name(), "0"
	t.Cleanup(func() { ClearFileRecordsForThread(chatID, thread) })

	run := func() []string {
		tool := &editTool{}
		start := make(chan struct{})
		var wg sync.WaitGroup
		results := make([]string, n)

		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				resp, err := tool.Execute(
					newTestToolContext(t, dir, chatID, thread),
					EditParams{
						FilePath:  path,
						OldString: fmt.Sprintf("line %02d original", i),
						NewString: fmt.Sprintf("line %02d EDITED", i),
					},
				)
				switch {
				case err != nil:
					results[i] = "err: " + err.Error()
				case resp.IsError:
					results[i] = "tool-error: " + resp.Content
				default:
					results[i] = "ok"
				}
			}(i)
		}
		close(start)
		wg.Wait()
		return results
	}
	return path, run
}

// lostEdits returns the indices whose edit reported success but is absent from
// the file — the silent-data-loss set. A reported error is NOT a loss: the
// model sees it and can retry. A success that vanished is unrecoverable.
func lostEdits(t *testing.T, path string, results []string) []int {
	t.Helper()
	body := readRaw(t, path)
	var lost []int
	for i, r := range results {
		if r != "ok" {
			continue
		}
		if !strings.Contains(body, fmt.Sprintf("line %02d EDITED", i)) {
			lost = append(lost, i)
		}
	}
	return lost
}

// TestEditConcurrentSameFileNoLostWrites is the defect, reduced to a test.
// N edit calls in one message, all to one file, all to disjoint lines: every
// one of them must be in the file afterwards. Nothing about this batch is
// ambiguous — no two edits touch the same text — so there is no correct
// outcome other than "all N applied".
func TestEditConcurrentSameFileNoLostWrites(t *testing.T) {
	for _, n := range []int{2, 3, 4, 8} {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			path, run := concurrentEditFixture(t, n)
			results := run()

			for i, r := range results {
				assert.Equal(t, "ok", r, "edit %d should have applied cleanly", i)
			}
			lost := lostEdits(t, path, results)
			assert.Empty(t, lost, "edits reported success but are missing from the file: %v\nfile:\n%s",
				lost, readRaw(t, path))
		})
	}
}

// TestEditConcurrentSameFileRepeated hammers the same batch shape over many
// trials. A race that shows up once in ten runs is still a race; this is the
// measurement that answers "at what N does it start losing writes".
func TestEditConcurrentSameFileRepeated(t *testing.T) {
	if testing.Short() {
		t.Skip("race measurement; skipped under -short")
	}
	const trials = 40

	for _, n := range []int{2, 3, 4, 6, 8, 10} {
		n := n
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			trialsWithLoss, totalLost := 0, 0
			for trial := 0; trial < trials; trial++ {
				path, run := concurrentEditFixture(t, n)
				results := run()
				if lost := lostEdits(t, path, results); len(lost) > 0 {
					trialsWithLoss++
					totalLost += len(lost)
				}
			}
			t.Logf("n=%d: %d/%d trials lost a write (%d edits silently lost)",
				n, trialsWithLoss, trials, totalLost)
			assert.Zero(t, trialsWithLoss, "n=%d silently lost writes in %d of %d trials", n, trialsWithLoss, trials)
		})
	}
}

// ---------------------------------------------------------------------------
// Unsafe edits must ERROR, never report success
// ---------------------------------------------------------------------------

// interposingDaemon wraps a real daemon client and runs a hook once, right
// after the tool has read the file and before it writes it back. That is the
// exact window a second writer — another pod, a `bash sed`, a human — slips
// into. An in-process lock cannot close this window, so the tool must detect
// the clobber at write time.
type interposingDaemon struct {
	daemon.Client
	once sync.Once
	hook func()
}

func (d *interposingDaemon) ReadFile(ctx context.Context, path string, opts *daemon.ReadFileOpts) (*daemon.FileContent, error) {
	fc, err := d.Client.ReadFile(ctx, path, opts)
	d.once.Do(func() {
		if d.hook != nil {
			d.hook()
		}
	})
	return fc, err
}

// TestEditExternalWriteBetweenReadAndWriteErrors: an edit whose basis went
// stale under it must fail loudly. Reporting "Content replaced in file" while
// having erased somebody else's write is the failure mode that costs data.
func TestEditExternalWriteBetweenReadAndWriteErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "contended.txt")
	require.NoError(t, os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0o644))

	chatID, thread := "interpose-"+t.Name(), "0"
	t.Cleanup(func() { ClearFileRecordsForThread(chatID, thread) })

	// A writer outside this process appends a line while our edit is in flight.
	const outsiderLine = "written-by-someone-else\n"
	d := &interposingDaemon{
		Client: daemon.NewLocalClient(),
		hook: func() {
			require.NoError(t, os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"+outsiderLine), 0o644))
		},
	}

	worktree := &rctx.WorktreeInfo{ID: "test", Path: dir}
	ctx := rctx.NewToolContext(context.Background(), chatID, thread, nil, worktree).
		WithMessageID("test-message").
		WithDaemon(d)

	tool := &editTool{}
	resp, err := tool.Execute(ctx, EditParams{
		FilePath:  path,
		OldString: "beta",
		NewString: "BETA",
	})
	require.NoError(t, err)

	assert.True(t, resp.IsError,
		"edit that raced an external write must report an error, got success: %s", resp.Content)
	assert.NotContains(t, resp.Content, "Content replaced in file",
		"a clobbering edit must never report success")

	// The outsider's line must survive: either the edit was refused outright or
	// it was applied on top of their content. What must never happen is their
	// write disappearing.
	assert.Contains(t, readRaw(t, path), outsiderLine,
		"the concurrent writer's content was silently erased")
}

// TestEditSingleEditUnchanged is the regression guard: the ordinary,
// uncontended single edit — by far the common case — must behave exactly as
// before, success text and file contents included.
func TestEditSingleEditUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "single.go")
	require.NoError(t, os.WriteFile(path, []byte("package main\n\nfunc main() {}\n"), 0o644))

	chatID, thread := "single-"+t.Name(), "0"
	t.Cleanup(func() { ClearFileRecordsForThread(chatID, thread) })

	tool := &editTool{}
	resp, err := tool.Execute(
		newTestToolContext(t, dir, chatID, thread),
		EditParams{FilePath: path, OldString: "func main() {}", NewString: "func main() { println(1) }"},
	)
	require.NoError(t, err)
	require.False(t, resp.IsError, "single edit failed: %s", resp.Content)
	assert.Contains(t, resp.Content, "Content replaced in file")
	assert.Equal(t, "package main\n\nfunc main() { println(1) }\n", readRaw(t, path))
}

// TestInsertAtConcurrentSameFileNoLostWrites — insert_at shares edit's
// read-modify-write shape through the same daemon client, so it inherits the
// same defect. Two inserts in one batch, both to one file, both must land.
func TestInsertAtConcurrentSameFileNoLostWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "insert.txt")
	require.NoError(t, os.WriteFile(path, []byte("aaa\nbbb\nccc\nddd\n"), 0o644))

	chatID, thread := "insert-"+t.Name(), "0"
	t.Cleanup(func() { ClearFileRecordsForThread(chatID, thread) })

	tool := &insertAtTool{}
	inserts := []InsertAtParams{
		{FilePath: path, AnchorText: "aaa", Position: "after", Content: "INSERT-ONE"},
		{FilePath: path, AnchorText: "bbb", Position: "after", Content: "INSERT-TWO"},
		{FilePath: path, AnchorText: "ccc", Position: "after", Content: "INSERT-THREE"},
		{FilePath: path, AnchorText: "ddd", Position: "after", Content: "INSERT-FOUR"},
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	oks := make([]bool, len(inserts))
	for i, p := range inserts {
		wg.Add(1)
		go func(i int, p InsertAtParams) {
			defer wg.Done()
			<-start
			resp, err := tool.Execute(newTestToolContext(t, dir, chatID, thread), p)
			oks[i] = err == nil && !resp.IsError
		}(i, p)
	}
	close(start)
	wg.Wait()

	body := readRaw(t, path)
	for i, p := range inserts {
		if !oks[i] {
			continue
		}
		assert.Contains(t, body, p.Content,
			"insert_at %d reported success but is missing from the file:\n%s", i, body)
	}
}

// TestEditLinesConcurrentSameFileNoLostWrites — same shape for edit_lines.
func TestEditLinesConcurrentSameFileNoLostWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lines.txt")
	require.NoError(t, os.WriteFile(path, []byte("l1\nl2\nl3\nl4\nl5\nl6\n"), 0o644))

	chatID, thread := "editlines-"+t.Name(), "0"
	t.Cleanup(func() { ClearFileRecordsForThread(chatID, thread) })

	tool := &editLinesTool{}
	edits := []EditLinesParams{
		{FilePath: path, Operation: "replace", StartLine: 1, EndLine: 1, NewContent: "L1-EDITED"},
		{FilePath: path, Operation: "replace", StartLine: 3, EndLine: 3, NewContent: "L3-EDITED"},
		{FilePath: path, Operation: "replace", StartLine: 5, EndLine: 5, NewContent: "L5-EDITED"},
		{FilePath: path, Operation: "replace", StartLine: 6, EndLine: 6, NewContent: "L6-EDITED"},
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	oks := make([]bool, len(edits))
	for i, p := range edits {
		wg.Add(1)
		go func(i int, p EditLinesParams) {
			defer wg.Done()
			<-start
			resp, err := tool.Execute(newTestToolContext(t, dir, chatID, thread), p)
			oks[i] = err == nil && !resp.IsError
		}(i, p)
	}
	close(start)
	wg.Wait()

	body := readRaw(t, path)
	for i, p := range edits {
		if !oks[i] {
			continue
		}
		assert.Contains(t, body, p.NewContent,
			"edit_lines %d reported success but is missing from the file:\n%s", i, body)
	}
}
