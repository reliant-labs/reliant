// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMoveCodeTool(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	chatID := "test-chat-move"
	thread := "0"

	// Clean up any existing records for this thread
	defer ClearFileRecordsForThread(chatID, thread)

	// Create the tool
	tool := &moveCodeTool{}

	t.Run("Move within same file - forward", func(t *testing.T) {
		ClearFileRecordsForThread(chatID, thread)

		testFile := filepath.Join(tempDir, "same_file_forward.txt")
		content := "line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8\nline 9\nline 10"
		err := os.WriteFile(testFile, []byte(content), 0644)
		require.NoError(t, err)

		// Simulate reading the file
		recordFileAwareness(chatID, thread, testFile)

		worktree := &rctx.WorktreeInfo{ID: "test", Path: tempDir}
		ctx := rctx.NewToolContext(context.Background(), chatID, thread, nil, worktree).
			WithMessageID("test-message").
			WithDaemon(daemon.NewLocalClient())

		// Move lines 2-3 to after line 8
		params := MoveCodeParams{
			SourceFile:  testFile,
			SourceStart: 2,
			SourceEnd:   3,
			TargetFile:  testFile,
			TargetLine:  8,
			Operation:   "move",
		}

		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)
		assert.False(t, response.IsError, "Expected success, got: %s", response.Content)

		// Read result
		result, err := os.ReadFile(testFile)
		require.NoError(t, err)

		expected := "line 1\nline 4\nline 5\nline 6\nline 7\nline 8\nline 2\nline 3\nline 9\nline 10"
		assert.Equal(t, expected, string(result))
	})

	t.Run("Move within same file - backward", func(t *testing.T) {
		ClearFileRecordsForThread(chatID, thread)

		testFile := filepath.Join(tempDir, "same_file_backward.txt")
		content := "line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8\nline 9\nline 10"
		err := os.WriteFile(testFile, []byte(content), 0644)
		require.NoError(t, err)

		recordFileAwareness(chatID, thread, testFile)

		worktree := &rctx.WorktreeInfo{ID: "test", Path: tempDir}
		ctx := rctx.NewToolContext(context.Background(), chatID, thread, nil, worktree).
			WithMessageID("test-message").
			WithDaemon(daemon.NewLocalClient())

		// Move lines 7-8 to after line 2
		params := MoveCodeParams{
			SourceFile:  testFile,
			SourceStart: 7,
			SourceEnd:   8,
			TargetFile:  testFile,
			TargetLine:  2,
			Operation:   "move",
		}

		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)
		assert.False(t, response.IsError, "Expected success, got: %s", response.Content)

		result, err := os.ReadFile(testFile)
		require.NoError(t, err)

		expected := "line 1\nline 2\nline 7\nline 8\nline 3\nline 4\nline 5\nline 6\nline 9\nline 10"
		assert.Equal(t, expected, string(result))
	})

	t.Run("Move to beginning of file", func(t *testing.T) {
		ClearFileRecordsForThread(chatID, thread)

		testFile := filepath.Join(tempDir, "move_to_beginning.txt")
		content := "line 1\nline 2\nline 3\nline 4\nline 5"
		err := os.WriteFile(testFile, []byte(content), 0644)
		require.NoError(t, err)

		recordFileAwareness(chatID, thread, testFile)

		worktree := &rctx.WorktreeInfo{ID: "test", Path: tempDir}
		ctx := rctx.NewToolContext(context.Background(), chatID, thread, nil, worktree).
			WithMessageID("test-message").
			WithDaemon(daemon.NewLocalClient())

		// Move lines 4-5 to beginning (target_line = 0)
		params := MoveCodeParams{
			SourceFile:  testFile,
			SourceStart: 4,
			SourceEnd:   5,
			TargetFile:  testFile,
			TargetLine:  0,
			Operation:   "move",
		}

		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)
		assert.False(t, response.IsError, "Expected success, got: %s", response.Content)

		result, err := os.ReadFile(testFile)
		require.NoError(t, err)

		expected := "line 4\nline 5\nline 1\nline 2\nline 3"
		assert.Equal(t, expected, string(result))
	})

	t.Run("Copy within same file", func(t *testing.T) {
		ClearFileRecordsForThread(chatID, thread)

		testFile := filepath.Join(tempDir, "copy_same_file.txt")
		content := "line 1\nline 2\nline 3\nline 4\nline 5"
		err := os.WriteFile(testFile, []byte(content), 0644)
		require.NoError(t, err)

		recordFileAwareness(chatID, thread, testFile)

		worktree := &rctx.WorktreeInfo{ID: "test", Path: tempDir}
		ctx := rctx.NewToolContext(context.Background(), chatID, thread, nil, worktree).
			WithMessageID("test-message").
			WithDaemon(daemon.NewLocalClient())

		// Copy lines 2-3 to after line 4
		params := MoveCodeParams{
			SourceFile:  testFile,
			SourceStart: 2,
			SourceEnd:   3,
			TargetFile:  testFile,
			TargetLine:  4,
			Operation:   "copy",
		}

		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)
		assert.False(t, response.IsError, "Expected success, got: %s", response.Content)

		result, err := os.ReadFile(testFile)
		require.NoError(t, err)

		// Original lines should still be there, with copies inserted
		expected := "line 1\nline 2\nline 3\nline 4\nline 2\nline 3\nline 5"
		assert.Equal(t, expected, string(result))
	})

	t.Run("Move between different files", func(t *testing.T) {
		ClearFileRecordsForThread(chatID, thread)

		sourceFile := filepath.Join(tempDir, "source.txt")
		targetFile := filepath.Join(tempDir, "target.txt")

		sourceContent := "source line 1\nsource line 2\nsource line 3\nsource line 4"
		targetContent := "target line 1\ntarget line 2\ntarget line 3"

		err := os.WriteFile(sourceFile, []byte(sourceContent), 0644)
		require.NoError(t, err)
		err = os.WriteFile(targetFile, []byte(targetContent), 0644)
		require.NoError(t, err)

		recordFileAwareness(chatID, thread, sourceFile)
		recordFileAwareness(chatID, thread, targetFile)

		worktree := &rctx.WorktreeInfo{ID: "test", Path: tempDir}
		ctx := rctx.NewToolContext(context.Background(), chatID, thread, nil, worktree).
			WithMessageID("test-message").
			WithDaemon(daemon.NewLocalClient())

		// Move lines 2-3 from source to after line 1 in target
		params := MoveCodeParams{
			SourceFile:  sourceFile,
			SourceStart: 2,
			SourceEnd:   3,
			TargetFile:  targetFile,
			TargetLine:  1,
			Operation:   "move",
		}

		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)
		assert.False(t, response.IsError, "Expected success, got: %s", response.Content)

		sourceResult, err := os.ReadFile(sourceFile)
		require.NoError(t, err)
		targetResult, err := os.ReadFile(targetFile)
		require.NoError(t, err)

		expectedSource := "source line 1\nsource line 4"
		expectedTarget := "target line 1\nsource line 2\nsource line 3\ntarget line 2\ntarget line 3"

		assert.Equal(t, expectedSource, string(sourceResult), "Source file mismatch")
		assert.Equal(t, expectedTarget, string(targetResult), "Target file mismatch")
	})

	t.Run("Copy between different files", func(t *testing.T) {
		ClearFileRecordsForThread(chatID, thread)

		sourceFile := filepath.Join(tempDir, "source_copy.txt")
		targetFile := filepath.Join(tempDir, "target_copy.txt")

		sourceContent := "source line 1\nsource line 2\nsource line 3"
		targetContent := "target line 1\ntarget line 2"

		err := os.WriteFile(sourceFile, []byte(sourceContent), 0644)
		require.NoError(t, err)
		err = os.WriteFile(targetFile, []byte(targetContent), 0644)
		require.NoError(t, err)

		recordFileAwareness(chatID, thread, sourceFile)
		recordFileAwareness(chatID, thread, targetFile)

		worktree := &rctx.WorktreeInfo{ID: "test", Path: tempDir}
		ctx := rctx.NewToolContext(context.Background(), chatID, thread, nil, worktree).
			WithMessageID("test-message").
			WithDaemon(daemon.NewLocalClient())

		// Copy lines 1-2 from source to end of target
		params := MoveCodeParams{
			SourceFile:  sourceFile,
			SourceStart: 1,
			SourceEnd:   2,
			TargetFile:  targetFile,
			TargetLine:  2,
			Operation:   "copy",
		}

		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)
		assert.False(t, response.IsError, "Expected success, got: %s", response.Content)

		sourceResult, err := os.ReadFile(sourceFile)
		require.NoError(t, err)
		targetResult, err := os.ReadFile(targetFile)
		require.NoError(t, err)

		// Source should be unchanged for copy
		assert.Equal(t, sourceContent, string(sourceResult), "Source file should be unchanged")
		expectedTarget := "target line 1\ntarget line 2\nsource line 1\nsource line 2"
		assert.Equal(t, expectedTarget, string(targetResult), "Target file mismatch")
	})

	t.Run("Succeeds without prior read", func(t *testing.T) {
		ClearFileRecordsForThread(chatID, thread)

		testFile := filepath.Join(tempDir, "unread.txt")
		content := "line 1\nline 2\nline 3"
		err := os.WriteFile(testFile, []byte(content), 0644)
		require.NoError(t, err)

		// Don't record awareness - file not read

		worktree := &rctx.WorktreeInfo{ID: "test", Path: tempDir}
		ctx := rctx.NewToolContext(context.Background(), chatID, thread, nil, worktree).
			WithMessageID("test-message").
			WithDaemon(daemon.NewLocalClient())

		params := MoveCodeParams{
			SourceFile:  testFile,
			SourceStart: 1,
			SourceEnd:   2,
			TargetFile:  testFile,
			TargetLine:  3,
			Operation:   "move",
		}

		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)
		assert.False(t, response.IsError, "Should succeed without prior read")
	})

	t.Run("Error when source_end < source_start", func(t *testing.T) {
		worktree := &rctx.WorktreeInfo{ID: "test", Path: tempDir}
		ctx := rctx.NewToolContext(context.Background(), chatID, thread, nil, worktree).
			WithMessageID("test-message").
			WithDaemon(daemon.NewLocalClient())

		params := MoveCodeParams{
			SourceFile:  "/some/file.txt",
			SourceStart: 5,
			SourceEnd:   2,
			TargetFile:  "/some/file.txt",
			TargetLine:  10,
		}

		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)
		assert.True(t, response.IsError)
		assert.Contains(t, response.Content, "source_end (2) must be >= source_start (5)")
	})

	t.Run("Error when source_start < 1", func(t *testing.T) {
		worktree := &rctx.WorktreeInfo{ID: "test", Path: tempDir}
		ctx := rctx.NewToolContext(context.Background(), chatID, thread, nil, worktree).
			WithMessageID("test-message").
			WithDaemon(daemon.NewLocalClient())

		params := MoveCodeParams{
			SourceFile:  "/some/file.txt",
			SourceStart: 0,
			SourceEnd:   2,
			TargetFile:  "/some/file.txt",
			TargetLine:  10,
		}

		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)
		assert.True(t, response.IsError)
		assert.Contains(t, response.Content, "source_start must be >= 1")
	})

	t.Run("Error when target_line within source range for same file", func(t *testing.T) {
		ClearFileRecordsForThread(chatID, thread)

		testFile := filepath.Join(tempDir, "overlap.txt")
		content := "line 1\nline 2\nline 3\nline 4\nline 5"
		err := os.WriteFile(testFile, []byte(content), 0644)
		require.NoError(t, err)

		recordFileAwareness(chatID, thread, testFile)

		worktree := &rctx.WorktreeInfo{ID: "test", Path: tempDir}
		ctx := rctx.NewToolContext(context.Background(), chatID, thread, nil, worktree).
			WithMessageID("test-message").
			WithDaemon(daemon.NewLocalClient())

		// Try to insert within the source range
		params := MoveCodeParams{
			SourceFile:  testFile,
			SourceStart: 2,
			SourceEnd:   4,
			TargetFile:  testFile,
			TargetLine:  3, // Within range 2-4
			Operation:   "move",
		}

		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)
		assert.True(t, response.IsError)
		assert.Contains(t, response.Content, "cannot be within the source range")
	})

	t.Run("Error when source line exceeds file length", func(t *testing.T) {
		ClearFileRecordsForThread(chatID, thread)

		testFile := filepath.Join(tempDir, "short.txt")
		content := "line 1\nline 2\nline 3"
		err := os.WriteFile(testFile, []byte(content), 0644)
		require.NoError(t, err)

		recordFileAwareness(chatID, thread, testFile)

		worktree := &rctx.WorktreeInfo{ID: "test", Path: tempDir}
		ctx := rctx.NewToolContext(context.Background(), chatID, thread, nil, worktree).
			WithMessageID("test-message").
			WithDaemon(daemon.NewLocalClient())

		params := MoveCodeParams{
			SourceFile:  testFile,
			SourceStart: 2,
			SourceEnd:   10, // File only has 3 lines
			TargetFile:  testFile,
			TargetLine:  1,
			Operation:   "move",
		}

		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)
		assert.True(t, response.IsError)
		assert.Contains(t, response.Content, "exceeds source file length")
	})
}
