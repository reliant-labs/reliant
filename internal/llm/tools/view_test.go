// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestViewToolLimitLogic(t *testing.T) {
	// Create a temporary test file with known content
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.txt")

	// Create a file with exactly 100 lines
	lines := make([]string, 100)
	for i := 0; i < 100; i++ {
		lines[i] = "Line " + string(rune('A'+i%26)) + " content here"
	}
	content := strings.Join(lines, "\n")

	err := os.WriteFile(testFile, []byte(content), 0644)
	require.NoError(t, err)

	// Create the underlying tool directly
	tool := &viewTool{}
	worktree := &rctx.WorktreeInfo{ID: "test", Path: tempDir}
	ctx := rctx.NewToolContext(context.Background(), "test-chat", "0", nil, worktree).WithDaemon(daemon.NewLocalClient())

	tests := []struct {
		name          string
		offset        int
		limit         int
		expectMoreMsg bool
		description   string
	}{
		{
			name:          "Read all lines with high limit",
			offset:        0,
			limit:         200,
			expectMoreMsg: false,
			description:   "Should not show more lines message when limit > total lines",
		},
		{
			name:          "Read exact number of lines",
			offset:        0,
			limit:         100,
			expectMoreMsg: false,
			description:   "Should not show more lines message when limit == total lines",
		},
		{
			name:          "Read partial with limit < total",
			offset:        0,
			limit:         50,
			expectMoreMsg: true,
			description:   "Should show more lines message when limit < total lines",
		},
		{
			name:          "Read from offset with remaining lines",
			offset:        80,
			limit:         10,
			expectMoreMsg: true,
			description:   "Should show more lines message when offset + limit < total lines",
		},
		{
			name:          "Read from offset without remaining lines",
			offset:        90,
			limit:         10,
			expectMoreMsg: false,
			description:   "Should not show more lines message when offset + limit >= total lines",
		},
		{
			name:          "Read from high offset",
			offset:        95,
			limit:         10,
			expectMoreMsg: false,
			description:   "Should not show more lines message when offset near end of file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := ViewParams{
				FilePath: testFile,
				Offset:   tt.offset,
				Limit:    tt.limit,
			}

			response, err := tool.Execute(ctx, params)
			require.NoError(t, err)

			output := response.Content
			hasMoreMsg := strings.Contains(output, "--- Truncated:")

			if tt.expectMoreMsg {
				assert.True(t, hasMoreMsg, "Expected truncation message but didn't find it. Test: %s", tt.description)
				assert.Contains(t, output, "Use offset=", "Should suggest using offset. Test: %s", tt.description)
				assert.Contains(t, output, "Use grep to find specific lines", "Should suggest grep. Test: %s", tt.description)
			} else {
				assert.False(t, hasMoreMsg, "Found unexpected truncation message. Test: %s. Output: %s", tt.description, output)
			}
		})
	}
}

func TestViewToolLimitEdgeCases(t *testing.T) {
	// Create a temporary test file with exactly 1000 lines
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "large_test.txt")

	lines := make([]string, 1000)
	for i := 0; i < 1000; i++ {
		lines[i] = "This is line number " + string(rune('0'+i%10)) + " with some content"
	}
	content := strings.Join(lines, "\n")

	err := os.WriteFile(testFile, []byte(content), 0644)
	require.NoError(t, err)

	// Create the underlying tool directly
	tool := &viewTool{}
	worktree := &rctx.WorktreeInfo{ID: "test", Path: tempDir}
	ctx := rctx.NewToolContext(context.Background(), "test-chat", "0", nil, worktree).WithDaemon(daemon.NewLocalClient()).WithMessageID("test-message")

	// Test case that matches the reported issue: offset 820, limit 2000, total lines 948
	t.Run("Bug reproduction: offset 820, limit 2000", func(t *testing.T) {
		// Create a file with exactly 948 lines to match the bug report
		bugFile := filepath.Join(tempDir, "bug_test.txt")
		bugLines := make([]string, 948)
		for i := 0; i < 948; i++ {
			bugLines[i] = "Bug test line " + string(rune('0'+i%10))
		}
		bugContent := strings.Join(bugLines, "\n")

		err := os.WriteFile(bugFile, []byte(bugContent), 0644)
		require.NoError(t, err)

		params := ViewParams{
			FilePath: bugFile,
			Offset:   820,
			Limit:    2000,
		}

		response, err := tool.Execute(ctx, params)
		require.NoError(t, err)

		output := response.Content
		hasMoreMsg := strings.Contains(output, "--- Truncated:")

		// With offset 820 and only 948 total lines, we should read lines 820-947 (128 lines)
		// Since this is less than the limit of 2000, there should be NO truncation message
		assert.False(t, hasMoreMsg, "Should not show truncation message when reading to end of file. Output: %s", output)
	})
}

func newViewTestCtx(t *testing.T, dir string) *rctx.ToolContext {
	t.Helper()
	worktree := &rctx.WorktreeInfo{ID: "test", Path: dir}
	return rctx.NewToolContext(context.Background(), "test-chat", "0", nil, worktree).WithDaemon(daemon.NewLocalClient())
}

func TestViewBinaryAndPDFDetection(t *testing.T) {
	tempDir := t.TempDir()
	tool := &viewTool{}
	ctx := newViewTestCtx(t, tempDir)

	writeFile := func(name string, content []byte) string {
		path := filepath.Join(tempDir, name)
		require.NoError(t, os.WriteFile(path, content, 0644))
		return path
	}

	dummyContent := []byte("dummy content")
	binaryContent := append([]byte("prefix"), append([]byte{0x00}, []byte("suffix")...)...)

	t.Run("PDF returns document response", func(t *testing.T) {
		path := writeFile("foo.pdf", dummyContent)
		resp, err := tool.Execute(ctx, ViewParams{FilePath: path})
		require.NoError(t, err)
		assert.False(t, resp.IsError, "expected IsError=false for PDF")
		assert.Equal(t, ToolResponseTypeImage, resp.Type)
		assert.Contains(t, resp.Content, "PDF file")
		require.Len(t, resp.BinaryParts, 1)
		assert.Equal(t, "application/pdf", resp.BinaryParts[0].MIMEType)
		assert.Equal(t, []byte(dummyContent), resp.BinaryParts[0].Data)
	})

	t.Run("ZIP binary extension detected", func(t *testing.T) {
		path := writeFile("archive.zip", dummyContent)
		resp, err := tool.Execute(ctx, ViewParams{FilePath: path})
		require.NoError(t, err)
		assert.True(t, resp.IsError, "expected IsError=true for zip")
		assert.Contains(t, resp.Content, "Binary file detected")
		assert.Contains(t, resp.Content, "cannot be displayed as text")
	})

	t.Run("EXE binary extension detected", func(t *testing.T) {
		path := writeFile("program.exe", dummyContent)
		resp, err := tool.Execute(ctx, ViewParams{FilePath: path})
		require.NoError(t, err)
		assert.True(t, resp.IsError, "expected IsError=true for exe")
		assert.Contains(t, resp.Content, "Binary file detected")
		assert.Contains(t, resp.Content, "cannot be displayed as text")
	})

	t.Run("DB binary extension detected", func(t *testing.T) {
		path := writeFile("data.db", dummyContent)
		resp, err := tool.Execute(ctx, ViewParams{FilePath: path})
		require.NoError(t, err)
		assert.True(t, resp.IsError, "expected IsError=true for .db")
		assert.Contains(t, resp.Content, "Binary file detected")
		assert.Contains(t, resp.Content, "cannot be displayed as text")
	})

	t.Run("XLSX binary extension detected", func(t *testing.T) {
		path := writeFile("spreadsheet.xlsx", dummyContent)
		resp, err := tool.Execute(ctx, ViewParams{FilePath: path})
		require.NoError(t, err)
		assert.True(t, resp.IsError, "expected IsError=true for xlsx")
		assert.Contains(t, resp.Content, "Binary file detected")
		assert.Contains(t, resp.Content, "cannot be displayed as text")
	})

	t.Run("PNG image returns image response", func(t *testing.T) {
		path := writeFile("image.png", dummyContent)
		resp, err := tool.Execute(ctx, ViewParams{FilePath: path})
		require.NoError(t, err)
		assert.False(t, resp.IsError, "expected IsError=false for png image")
		assert.Equal(t, ToolResponseTypeImage, resp.Type)
		assert.Contains(t, resp.Content, "Image file")
		require.Len(t, resp.BinaryParts, 1)
		assert.Equal(t, "image/png", resp.BinaryParts[0].MIMEType)
		assert.Equal(t, []byte(dummyContent), resp.BinaryParts[0].Data)
	})

	t.Run("TXT with binary content detected at runtime", func(t *testing.T) {
		path := writeFile("notreally.txt", binaryContent)
		resp, err := tool.Execute(ctx, ViewParams{FilePath: path})
		require.NoError(t, err)
		assert.True(t, resp.IsError, "expected IsError=true for binary .txt")
		assert.Contains(t, resp.Content, "Binary file detected")
		assert.Contains(t, resp.Content, "cannot be displayed as text")
	})

	t.Run("Normal TXT file reads successfully", func(t *testing.T) {
		path := writeFile("readme.txt", []byte("hello\nworld\n"))
		resp, err := tool.Execute(ctx, ViewParams{FilePath: path})
		require.NoError(t, err)
		assert.False(t, resp.IsError, "expected IsError=false for plain text")
		assert.Contains(t, resp.Content, "hello")
		assert.Contains(t, resp.Content, "world")
	})

	t.Run("Normal Go file reads successfully", func(t *testing.T) {
		path := writeFile("main.go", []byte("package main\n\nfunc main() {}\n"))
		resp, err := tool.Execute(ctx, ViewParams{FilePath: path})
		require.NoError(t, err)
		assert.False(t, resp.IsError, "expected IsError=false for .go file")
		assert.Contains(t, resp.Content, "package main")
	})
}
