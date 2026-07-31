// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemon"
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
	ctx := rctx.NewToolContext(context.Background(), "test-chat", "0", nil, worktree).WithDaemon(daemon.NewLocalClient()).WithMessageID("test-msg")

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
	ctx := rctx.NewToolContext(context.Background(), "test-chat", "0", nil, worktree).WithDaemon(daemon.NewLocalClient()).WithMessageID("test-msg")

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


// TestTruncationMessagesContainGuidance tests that truncation messages include actionable hints
func TestTruncationMessagesContainGuidance(t *testing.T) {
	t.Run("View truncation message mentions offset", func(t *testing.T) {
		largeOutput := strings.Repeat("line of code\n", 5000)
		result := TruncateOutput("view", largeOutput, true)

		assert.Contains(t, result, "offset",
			"View truncation message should mention offset parameter")
	})

	t.Run("Shell search truncation message indicates truncation", func(t *testing.T) {
		// Need output larger than MaxOutputSize (16KB) to trigger truncation
		largeOutput := strings.Repeat("some/path/to/file.go\n", 1500) // ~30KB
		result := TruncateOutput("bash", largeOutput, true)

		// The result should be truncated (smaller than original)
		assert.Less(t, len(result), len(largeOutput),
			"Output should be truncated")

		// The result should contain truncation indicator
		resultLower := strings.ToLower(result)
		assert.True(t,
			strings.Contains(resultLower, "truncated") || strings.Contains(resultLower, "results"),
			"Shell truncation should indicate output was truncated")
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

// TestOutputLimitsConstants pins the RELATIONSHIPS between the output limits,
// not their values.
//
// It used to assert each constant equalled a literal repeated from the source.
// That kind of test cannot fail for any reason except someone deliberately
// changing the number it copies, and then it fails every time — so it reports
// a tuning decision as a defect and teaches the next person to edit the test
// until it is quiet. The values are a judgement about context cost and are
// meant to move.
//
// What must NOT move is how they relate: a warning has to arrive before the
// cliff, a skill has to be delivered under the same ceiling as any other tool
// result, and head+tail truncation needs enough room to leave two useful ends.
func TestOutputLimitsConstants(t *testing.T) {
	t.Run("the warning threshold arrives before the ceiling", func(t *testing.T) {
		assert.Less(t, TruncationWarningThreshold, MaxOutputSize,
			"a warning at or past the ceiling fires only once output is already cut")
		assert.Greater(t, TruncationWarningThreshold, MaxOutputSize/2,
			"a warning below half the ceiling fires on ordinary output and gets ignored")
	})

	t.Run("a skill is delivered under the same ceiling as any tool result", func(t *testing.T) {
		assert.Equal(t, MaxOutputSize, MaxSkillBodySize,
			"a preloaded skill and a hand-loaded skill must be byte-identical, and both "+
				"ride the general tool-output ceiling")
	})

	t.Run("head+tail truncation has room for two useful ends", func(t *testing.T) {
		// TruncateOutput reserves 500 bytes for its notice and splits the
		// remainder. Below a few KB the halves stop carrying enough to read.
		assert.Greater(t, MaxOutputSize-500, 2_000,
			"the head+tail strategy needs room to leave a readable head AND tail")
	})

	t.Run("a single read cannot exceed what a tool result can carry", func(t *testing.T) {
		assert.LessOrEqual(t, MaxReadSize, MaxOutputSize,
			"a read that outgrows the output ceiling is truncated twice, and the second "+
				"cut is the one nobody accounted for")
	})

	t.Run("per-line and per-read limits stay positive", func(t *testing.T) {
		assert.Greater(t, DefaultReadLimit, 0, "a non-positive read limit returns nothing")
		assert.Greater(t, MaxLineLength, 0, "a non-positive line length truncates every line to nothing")
	})
}