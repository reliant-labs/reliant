// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestToolContext(t *testing.T, tempDir, chatID, thread string) *rctx.ToolContext {
	t.Helper()
	worktree := &rctx.WorktreeInfo{ID: "test", Path: tempDir}
	ctx := rctx.NewToolContext(context.Background(), chatID, thread, nil, worktree).
		WithMessageID("test-message").
		WithDaemon(daemon.NewLocalClient())
	return ctx
}

func TestEditToolFileModifiedCheck(t *testing.T) {
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.txt")

	// Create initial file content
	initialContent := "line 1\nline 2\nline 3"
	err := os.WriteFile(testFile, []byte(initialContent), 0644)
	require.NoError(t, err)

	// Create the tool
	tool := &editTool{}

	thread := "0"
	chatID := "test-chat"

	// Clean up any existing records for this thread
	defer ClearFileRecordsForThread(chatID, thread)

	t.Run("Edit succeeds even without prior read", func(t *testing.T) {
		// Clear records to simulate fresh state
		ClearFileRecordsForThread(chatID, thread)

		ctx := newTestToolContext(t, tempDir, chatID, thread)

		params := EditParams{
			Edits: []EditOperation{
				{
					FilePath:  testFile,
					OldString: "line 2",
					NewString: "line TWO",
				},
			},
		}

		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)
		assert.False(t, response.IsError, "Edit should succeed without prior read")
	})

	t.Run("Success after reading file", func(t *testing.T) {
		// Simulate reading the file in this thread
		recordFileAwareness(chatID, thread, testFile)

		ctx := newTestToolContext(t, tempDir, chatID, thread)

		params := EditParams{
			Edits: []EditOperation{
				{
					FilePath:  testFile,
					OldString: "line TWO",
					NewString: "line 2 updated",
				},
			},
		}

		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)

		assert.False(t, response.IsError, "Expected success response, got: %s", response.Content)
		assert.Contains(t, response.Content, "Content replaced in file")
	})

	t.Run("Error when file modified externally after read", func(t *testing.T) {
		// Reset the file
		err := os.WriteFile(testFile, []byte(initialContent), 0644)
		require.NoError(t, err)

		// Record initial read in this thread
		recordFileAwareness(chatID, thread, testFile)

		// Wait a bit to ensure time difference
		time.Sleep(10 * time.Millisecond)

		// Modify file externally (simulating user edit)
		err = os.WriteFile(testFile, []byte("externally modified content"), 0644)
		require.NoError(t, err)

		ctx := newTestToolContext(t, tempDir, chatID, thread)

		params := EditParams{
			Edits: []EditOperation{
				{
					FilePath:  testFile,
					OldString: "line 2",
					NewString: "line TWO",
				},
			},
		}

		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)

		assert.True(t, response.IsError, "Expected error response")
		assert.Contains(t, response.Content, "edit operation 1")
		assert.Contains(t, response.Content, "has been modified since it was last read")
		assert.Contains(t, response.Content, "mod time:")
		assert.Contains(t, response.Content, "last aware:")
	})

	t.Run("Success when AI wrote the file last", func(t *testing.T) {
		// Reset the file
		err := os.WriteFile(testFile, []byte(initialContent), 0644)
		require.NoError(t, err)

		// Record read
		recordFileAwareness(chatID, thread, testFile)

		// Wait a bit
		time.Sleep(10 * time.Millisecond)

		// AI writes the file
		newContent := "AI modified content\nline 2\nline 3"
		err = os.WriteFile(testFile, []byte(newContent), 0644)
		require.NoError(t, err)

		// Record the AI write - AI is now aware of the file
		recordFileAwareness(chatID, thread, testFile)

		ctx := newTestToolContext(t, tempDir, chatID, thread)

		params := EditParams{
			Edits: []EditOperation{
				{
					FilePath:  testFile,
					OldString: "line 2",
					NewString: "line TWO",
				},
			},
		}

		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)

		assert.False(t, response.IsError, "Expected success since AI was last to write, got: %s", response.Content)
		assert.Contains(t, response.Content, "Content replaced in file")
	})
}

func TestEditToolThreadIsolation(t *testing.T) {
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "thread_test.txt")

	initialContent := "line 1\nline 2\nline 3"
	err := os.WriteFile(testFile, []byte(initialContent), 0644)
	require.NoError(t, err)

	tool := &editTool{}

	thread1 := "0"
	thread2 := "0.1"
	chatID := "test-chat"

	// Clean up
	defer ClearFileRecordsForThread(chatID, thread1)
	defer ClearFileRecordsForThread(chatID, thread2)

	t.Run("Thread 1 can read and edit", func(t *testing.T) {
		// Thread 1 reads the file
		recordFileAwareness(chatID, thread1, testFile)

		ctx := newTestToolContext(t, tempDir, chatID, thread1)

		params := EditParams{
			Edits: []EditOperation{
				{
					FilePath:  testFile,
					OldString: "line 2",
					NewString: "line TWO (thread 1)",
				},
			},
		}

		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)
		assert.False(t, response.IsError, "Thread 1 should succeed after reading file")
	})

	t.Run("Thread 2 can edit without prior read", func(t *testing.T) {
		// Thread 2 has NOT read the file - should still succeed
		ctx := newTestToolContext(t, tempDir, chatID, thread2)

		params := EditParams{
			Edits: []EditOperation{
				{
					FilePath:  testFile,
					OldString: "line 1",
					NewString: "line ONE (thread 2)",
				},
			},
		}

		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)
		assert.False(t, response.IsError, "Thread 2 should succeed without prior read")
	})

	t.Run("Thread 2 can edit after reading", func(t *testing.T) {
		// Thread 2 now reads the file
		recordFileAwareness(chatID, thread2, testFile)

		ctx := newTestToolContext(t, tempDir, chatID, thread2)

		params := EditParams{
			Edits: []EditOperation{
				{
					FilePath:  testFile,
					OldString: "line ONE (thread 2)",
					NewString: "line ONE UPDATED (thread 2)",
				},
			},
		}

		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)
		assert.False(t, response.IsError, "Thread 2 should succeed after reading file")
	})
}

func TestEditToolDeleteContentModifiedCheck(t *testing.T) {
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "delete_test.txt")

	initialContent := "line 1\nline 2\nline 3"
	err := os.WriteFile(testFile, []byte(initialContent), 0644)
	require.NoError(t, err)

	tool := &editTool{}

	thread := "0"
	chatID := "test-chat"
	defer ClearFileRecordsForThread(chatID, thread)

	t.Run("Delete content succeeds without prior read", func(t *testing.T) {
		ClearFileRecordsForThread(chatID, thread)

		ctx := newTestToolContext(t, tempDir, chatID, thread)

		params := EditParams{
			Edits: []EditOperation{
				{
					FilePath:  testFile,
					OldString: "line 2\n",
					NewString: "", // Empty string means delete
				},
			},
		}

		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)
		assert.False(t, response.IsError, "Delete should succeed without prior read")
	})

	t.Run("Delete content succeeds after reading file", func(t *testing.T) {
		recordFileAwareness(chatID, thread, testFile)

		ctx := newTestToolContext(t, tempDir, chatID, thread)

		// line 2 was already deleted by the previous test, so delete line 3
		params := EditParams{
			Edits: []EditOperation{
				{
					FilePath:  testFile,
					OldString: "line 3",
					NewString: "",
				},
			},
		}

		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)

		assert.False(t, response.IsError, "Expected success response, got: %s", response.Content)
		assert.Contains(t, response.Content, "Content deleted from file")
	})
}

func TestEditToolValidateFileForEdit(t *testing.T) {
	tempDir := t.TempDir()

	tool := &editTool{}

	thread := "0"
	chatID := "test-chat"
	defer ClearFileRecordsForThread(chatID, thread)

	t.Run("Error when file does not exist", func(t *testing.T) {
		ctx := newTestToolContext(t, tempDir, chatID, thread)
		nonExistentFile := filepath.Join(tempDir, "does_not_exist.txt")
		err := tool.validateFileForEdit(ctx, chatID, thread, nonExistentFile)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "file not found")
	})

	t.Run("Error when path is a directory", func(t *testing.T) {
		ctx := newTestToolContext(t, tempDir, chatID, thread)
		dir := filepath.Join(tempDir, "testdir")
		err := os.Mkdir(dir, 0755)
		require.NoError(t, err)

		err = tool.validateFileForEdit(ctx, chatID, thread, dir)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "path is a directory")
	})

	t.Run("No error when file not read yet", func(t *testing.T) {
		ClearFileRecordsForThread(chatID, thread)

		ctx := newTestToolContext(t, tempDir, chatID, thread)
		testFile := filepath.Join(tempDir, "unread_file.txt")
		err := os.WriteFile(testFile, []byte("content"), 0644)
		require.NoError(t, err)

		err = tool.validateFileForEdit(ctx, chatID, thread, testFile)

		assert.NoError(t, err, "Should not error for unread files")
	})

	t.Run("Success when file was read in this thread", func(t *testing.T) {
		ctx := newTestToolContext(t, tempDir, chatID, thread)
		testFile := filepath.Join(tempDir, "read_file.txt")
		err := os.WriteFile(testFile, []byte("content"), 0644)
		require.NoError(t, err)

		recordFileAwareness(chatID, thread, testFile)

		err = tool.validateFileForEdit(ctx, chatID, thread, testFile)

		assert.NoError(t, err)
	})

	t.Run("No error when file was only read in different thread", func(t *testing.T) {
		ctx := newTestToolContext(t, tempDir, chatID, thread)
		testFile := filepath.Join(tempDir, "other_thread_file.txt")
		err := os.WriteFile(testFile, []byte("content"), 0644)
		require.NoError(t, err)

		// Read in different thread
		otherThread := "0.99"
		recordFileAwareness(chatID, otherThread, testFile)
		defer ClearFileRecordsForThread(chatID, otherThread)

		// Validate in current thread — should succeed since we no longer require prior read
		err = tool.validateFileForEdit(ctx, chatID, thread, testFile)

		assert.NoError(t, err, "Should not error when file was not read in current thread")
	})
}
