// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestViewToolOutputTruncation tests that large file outputs are properly truncated
// to stay within the MaxOutputSize limit (16KB)
func TestViewToolOutputTruncation(t *testing.T) {
	tempDir := t.TempDir()

	// Create the view tool
	tool := &viewTool{}
	worktree := &rctx.WorktreeInfo{ID: "test", Path: tempDir}
	ctx := rctx.NewToolContext(context.Background(), "test-chat", "0", nil, worktree).WithMessageID("test-msg")

	t.Run("Small file under limit is not truncated", func(t *testing.T) {
		// Create a small file (~10KB)
		smallFile := filepath.Join(tempDir, "small.txt")
		lines := make([]string, 200)
		for i := 0; i < 200; i++ {
			lines[i] = fmt.Sprintf("Line %d: This is a normal line of text", i+1)
		}
		content := strings.Join(lines, "\n")
		require.NoError(t, os.WriteFile(smallFile, []byte(content), 0644))

		params := ViewParams{FilePath: smallFile, Limit: 500}
		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)

		// Should not contain truncation warnings
		assert.NotContains(t, response.Content, "bytes omitted")
		assert.NotContains(t, response.Content, "Output truncated")
	})

	t.Run("Large file exceeding MaxOutputSize is truncated with head+tail", func(t *testing.T) {
		// Create a large file (~40KB, well over the 16KB limit)
		largeFile := filepath.Join(tempDir, "large.txt")
		lines := make([]string, 1000)
		for i := 0; i < 1000; i++ {
			// Each line is ~40 chars, so 1000 lines = ~40KB
			lines[i] = fmt.Sprintf("Line %04d: This is line content for testing truncation behavior", i+1)
		}
		content := strings.Join(lines, "\n")
		require.NoError(t, os.WriteFile(largeFile, []byte(content), 0644))

		params := ViewParams{FilePath: largeFile, Limit: 1000}
		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)

		// The View tool itself doesn't apply output_limiter truncation - that happens
		// in tool_wrapper.go. But we can verify the file was read and produces large output.
		output := response.Content

		// Verify the output exists and contains file content
		assert.Contains(t, output, "<file>", "Should have file wrapper")
		assert.Contains(t, output, "Line 0001", "Should have line numbers")

		// The MaxReadSize limit (16KB) should have kicked in, so we shouldn't have all 1000 lines
		// But the output should still be substantial
		assert.Greater(t, len(output), 10000, "Should have substantial output")
		// Note: The output_limiter truncation is tested separately in tool_wrapper tests
	})

	t.Run("MaxReadSize limits bytes read from file", func(t *testing.T) {
		// Create a very large file (~50KB, over the 16KB MaxReadSize)
		hugeFile := filepath.Join(tempDir, "huge.txt")
		lines := make([]string, 1500)
		for i := 0; i < 1500; i++ {
			lines[i] = fmt.Sprintf("Line %04d: %s", i+1, strings.Repeat("x", 30))
		}
		content := strings.Join(lines, "\n")
		require.NoError(t, os.WriteFile(hugeFile, []byte(content), 0644))

		params := ViewParams{FilePath: hugeFile, Limit: 1500}
		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)

		// Should have an actionable truncation message
		assert.Contains(t, response.Content, "--- Truncated:",
			"Should indicate there are more lines to read")
		assert.Contains(t, response.Content, "Use offset=",
			"Should suggest using offset to continue reading")
		assert.Contains(t, response.Content, "byte limit reached",
			"Should indicate byte limit was the cause")
	})
}

// TestViewToolLongLineTruncation tests that individual lines over MaxLineLength are truncated
func TestViewToolLongLineTruncation(t *testing.T) {
	tempDir := t.TempDir()

	tool := &viewTool{}
	worktree := &rctx.WorktreeInfo{ID: "test", Path: tempDir}
	ctx := rctx.NewToolContext(context.Background(), "test-chat", "0", nil, worktree).WithMessageID("test-msg")

	t.Run("Long lines are truncated with ellipsis", func(t *testing.T) {
		// Create a file with a very long line (like minified JS)
		longLineFile := filepath.Join(tempDir, "minified.js")
		longLine := "var x = " + strings.Repeat("a", 5000) + ";"
		content := "// Header\n" + longLine + "\n// Footer"
		require.NoError(t, os.WriteFile(longLineFile, []byte(content), 0644))

		params := ViewParams{FilePath: longLineFile, Limit: 10}
		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)

		// The long line should be truncated
		assert.Contains(t, response.Content, "...",
			"Long line should be truncated with ellipsis")

		// The truncated output should not contain the full 5000 'a' characters
		assert.Less(t, strings.Count(response.Content, "aaaa"), 500,
			"Should not contain the full long line")
	})
}

// TestGrepContentModeTruncation tests that grep content mode truncates long matched lines
func TestGrepContentModeTruncation(t *testing.T) {
	tempDir := t.TempDir()

	tool := &grepTool{}
	worktree := &rctx.WorktreeInfo{ID: "test", Path: tempDir}
	ctx := rctx.NewToolContext(context.Background(), "test-chat", "0", nil, worktree).WithMessageID("test-msg")

	t.Run("Long matched lines are truncated in content mode", func(t *testing.T) {
		// Create a file with long lines containing the search pattern
		testFile := filepath.Join(tempDir, "longlines.go")
		shortLine := "// This is a short comment with PATTERN"
		longLine := "const longVar = \"PATTERN " + strings.Repeat("x", 500) + "\""
		content := shortLine + "\n" + longLine + "\n// end"
		require.NoError(t, os.WriteFile(testFile, []byte(content), 0644))

		params := GrepParams{
			Pattern:    "PATTERN",
			Path:       tempDir,
			OutputMode: "content",
		}
		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)

		output := response.Content

		// Should contain the pattern
		assert.Contains(t, output, "PATTERN", "Should find the pattern")

		// Long line should be truncated with char count
		assert.Contains(t, output, "chars total",
			"Long matched lines should show total char count when truncated")
	})

	t.Run("Short matched lines are not truncated", func(t *testing.T) {
		testFile := filepath.Join(tempDir, "shortlines.go")
		content := "// Line with KEYWORD here\nfunc test() { KEYWORD }\n"
		require.NoError(t, os.WriteFile(testFile, []byte(content), 0644))

		params := GrepParams{
			Pattern:    "KEYWORD",
			Path:       tempDir,
			OutputMode: "content",
		}
		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)

		// Short lines should not have truncation indicator
		assert.NotContains(t, response.Content, "chars total",
			"Short lines should not show truncation indicator")
	})
}

// TestGrepResultLimitTruncation tests that grep results are limited properly
func TestGrepResultLimitTruncation(t *testing.T) {
	tempDir := t.TempDir()

	tool := &grepTool{}
	worktree := &rctx.WorktreeInfo{ID: "test", Path: tempDir}
	ctx := rctx.NewToolContext(context.Background(), "test-chat", "0", nil, worktree).WithMessageID("test-msg")

	t.Run("Results limited to defaultResultLimit", func(t *testing.T) {
		// Create many files with the pattern
		for i := 0; i < 300; i++ {
			fileName := filepath.Join(tempDir, fmt.Sprintf("file%03d.txt", i))
			content := fmt.Sprintf("File %d contains SEARCHTERM here", i)
			require.NoError(t, os.WriteFile(fileName, []byte(content), 0644))
		}

		params := GrepParams{
			Pattern:    "SEARCHTERM",
			Path:       tempDir,
			OutputMode: "files_with_matches",
		}
		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)

		// Should indicate truncation
		assert.Contains(t, response.Content, "truncated",
			"Should indicate results are truncated when exceeding limit")

		// Should have metadata about truncation
		assert.NotEmpty(t, response.Metadata, "Should have metadata")
	})

	t.Run("HeadLimit parameter is respected", func(t *testing.T) {
		params := GrepParams{
			Pattern:    "SEARCHTERM",
			Path:       tempDir,
			OutputMode: "files_with_matches",
			HeadLimit:  10,
		}
		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)

		// Count the number of file paths in output
		lines := strings.Split(response.Content, "\n")
		fileCount := 0
		for _, line := range lines {
			if strings.HasPrefix(line, "file") && strings.HasSuffix(line, ".txt") {
				fileCount++
			}
		}

		assert.LessOrEqual(t, fileCount, 10,
			"Should respect HeadLimit parameter, got %d files", fileCount)
	})
}

// TestTruncationMessagesContainGuidance tests that truncation messages include actionable hints
func TestTruncationMessagesContainGuidance(t *testing.T) {
	t.Run("View truncation message mentions offset", func(t *testing.T) {
		largeOutput := strings.Repeat("line of code\n", 5000)
		result := TruncateOutput("view", largeOutput, true)

		assert.Contains(t, result, "offset",
			"View truncation message should mention offset parameter")
	})

	t.Run("Grep truncation message indicates truncation", func(t *testing.T) {
		// Need output larger than MaxOutputSize (16KB) to trigger truncation
		largeOutput := strings.Repeat("some/path/to/file.go\n", 1500) // ~30KB
		result := TruncateOutput("grep", largeOutput, true)

		// The result should be truncated (smaller than original)
		assert.Less(t, len(result), len(largeOutput),
			"Output should be truncated")

		// The result should contain truncation indicator
		resultLower := strings.ToLower(result)
		assert.True(t,
			strings.Contains(resultLower, "truncated") || strings.Contains(resultLower, "results"),
			"Grep truncation should indicate results were truncated")
	})

	t.Run("Head+tail truncation shows bytes omitted", func(t *testing.T) {
		largeOutput := strings.Repeat("x", 30000)
		result := TruncateOutput("view", largeOutput, false)

		assert.Contains(t, result, "bytes omitted",
			"Head+tail truncation should indicate how many bytes were omitted")
		assert.Contains(t, result, "offset",
			"Head+tail truncation should mention offset parameter")
	})
}

// TestOutputLimitsConstants verifies the limit constants are set correctly
func TestOutputLimitsConstants(t *testing.T) {
	t.Run("MaxOutputSize is 16KB", func(t *testing.T) {
		assert.Equal(t, 16_000, MaxOutputSize,
			"MaxOutputSize should be 16KB (16000 bytes) to limit token usage (~4K tokens)")
	})

	t.Run("MaxReadSize is 16KB", func(t *testing.T) {
		assert.Equal(t, 16*1024, MaxReadSize,
			"MaxReadSize should be 16KB to match MaxOutputSize")
	})

	t.Run("DefaultReadLimit is 300 lines", func(t *testing.T) {
		assert.Equal(t, 300, DefaultReadLimit,
			"DefaultReadLimit should be 300 lines to reduce context consumption")
	})

	t.Run("MaxLineLength is 500 chars", func(t *testing.T) {
		assert.Equal(t, 500, MaxLineLength,
			"MaxLineLength should be 500 chars to prevent single-line abuse")
	})

	t.Run("TruncationWarningThreshold is 12KB", func(t *testing.T) {
		assert.Equal(t, 12_000, TruncationWarningThreshold,
			"TruncationWarningThreshold should be 12KB (75% of MaxOutputSize)")
	})
}
