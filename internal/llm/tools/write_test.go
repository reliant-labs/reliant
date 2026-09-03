// Copyright (c) 2025 Reliant Labs
package tools

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWriteTool_ExistingUnreadFile_SucceedsWithDiff verifies the softened
// read-before-write guard: writing an existing-but-unread file now SUCCEEDS and
// the model-visible content carries a compact diff of what was overwritten.
func TestWriteTool_ExistingUnreadFile_SucceedsWithDiff(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "notes.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("original line 1\noriginal line 2\n"), 0644))

	chatID := "write-test-" + t.Name()
	thread := "0"
	// Ensure the file is treated as unread (no awareness recorded).
	ClearFileRecordsForThread(chatID, thread)
	defer ClearFileRecordsForThread(chatID, thread)

	tool := &writeTool{}
	ctx := newTestToolContext(t, tempDir, chatID, thread)

	resp, err := tool.Execute(ctx, WriteParams{
		FilePath: testFile,
		Content:  "brand new content\n",
	})
	require.NoError(t, err)

	assert.False(t, resp.IsError, "unread existing-file write should now SUCCEED: %s", resp.Content)
	assert.Contains(t, resp.Content, "File successfully written")
	// The compact clobber note + diff must reach the model (Content, not metadata).
	assert.Contains(t, resp.Content, "Review the diff",
		"unread overwrite should surface a clobber note in the visible content")
	assert.Contains(t, resp.Content, "original line 1",
		"diff should show the removed old content")
	assert.Contains(t, resp.Content, "brand new content",
		"diff should show the added new content")

	// The full diff is still available in metadata for downstream consumers.
	assert.Contains(t, resp.Metadata, "original line 1")

	// The file was actually overwritten.
	got, err := os.ReadFile(testFile)
	require.NoError(t, err)
	assert.Equal(t, "brand new content\n", string(got))
}

// TestWriteTool_ConcurrentModification_StillErrors verifies the concurrent-mod
// guard remains a HARD ERROR: a file read, then modified externally, then
// written must be rejected to protect against multi-agent data loss.
func TestWriteTool_ConcurrentModification_StillErrors(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "shared.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("v1\n"), 0644))

	chatID := "write-test-" + t.Name()
	thread := "0"
	defer ClearFileRecordsForThread(chatID, thread)

	// Agent reads the file (awareness recorded)...
	recordFileAwareness(chatID, thread, testFile)
	// ...then someone else modifies it AFTER our awareness timestamp.
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, os.WriteFile(testFile, []byte("v2 external\n"), 0644))

	tool := &writeTool{}
	ctx := newTestToolContext(t, tempDir, chatID, thread)

	resp, err := tool.Execute(ctx, WriteParams{
		FilePath: testFile,
		Content:  "v3 agent\n",
	})
	require.NoError(t, err)

	assert.True(t, resp.IsError, "concurrent modification must remain a hard error")
	assert.Contains(t, resp.Content, "modified since it was last read")

	// The agent's write must NOT have landed.
	got, err := os.ReadFile(testFile)
	require.NoError(t, err)
	assert.Equal(t, "v2 external\n", string(got))
}

// TestWriteTool_ReadThenWrite_NoClobberNote verifies the clobber note is
// specific to unread overwrites: a properly-read file overwrite succeeds with a
// plain success message and no diff note.
func TestWriteTool_ReadThenWrite_NoClobberNote(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "known.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("aaa\n"), 0644))

	chatID := "write-test-" + t.Name()
	thread := "0"
	defer ClearFileRecordsForThread(chatID, thread)

	recordFileAwareness(chatID, thread, testFile)

	tool := &writeTool{}
	ctx := newTestToolContext(t, tempDir, chatID, thread)

	resp, err := tool.Execute(ctx, WriteParams{FilePath: testFile, Content: "bbb\n"})
	require.NoError(t, err)

	assert.False(t, resp.IsError, "read-then-write should succeed: %s", resp.Content)
	assert.Contains(t, resp.Content, "File successfully written")
	assert.NotContains(t, resp.Content, "Review the diff",
		"a properly-read file overwrite should not carry the unread-clobber note")
}
