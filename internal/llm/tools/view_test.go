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

func TestViewToolLimitLogic(t *testing.T) {
	t.Parallel()
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
			offset:        81,
			limit:         10,
			expectMoreMsg: true,
			description:   "Should show more lines message when offset + limit < total lines",
		},
		{
			name:          "Read from offset without remaining lines",
			offset:        91,
			limit:         10,
			expectMoreMsg: false,
			description:   "Should not show more lines message when offset + limit >= total lines",
		},
		{
			name:          "Read from high offset",
			offset:        96,
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
	t.Parallel()
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
	t.Parallel()
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

	t.Run("small PDF returns whole document", func(t *testing.T) {
		pdf := buildTestPDF(3)
		path := writeFile("small.pdf", pdf)
		resp, err := tool.Execute(ctx, ViewParams{FilePath: path})
		require.NoError(t, err)
		assert.False(t, resp.IsError, "expected IsError=false for PDF")
		assert.Equal(t, ToolResponseTypeImage, resp.Type)
		assert.Contains(t, resp.Content, "PDF file")
		assert.Contains(t, resp.Content, "3 pages")
		require.Len(t, resp.BinaryParts, 1)
		assert.Equal(t, "application/pdf", resp.BinaryParts[0].MIMEType)
		assert.Equal(t, pdf, resp.BinaryParts[0].Data)
	})

	t.Run("large PDF without pages prompts for a range", func(t *testing.T) {
		pdf := buildTestPDF(PDFAutoInlinePageLimit + 5)
		path := writeFile("large.pdf", pdf)
		resp, err := tool.Execute(ctx, ViewParams{FilePath: path})
		require.NoError(t, err)
		assert.False(t, resp.IsError)
		assert.Equal(t, ToolResponseTypeText, resp.Type)
		assert.Empty(t, resp.BinaryParts, "large PDF should not inject bytes without a page range")
		assert.Contains(t, resp.Content, "pages")
	})

	t.Run("large PDF with pages returns that range", func(t *testing.T) {
		pdf := buildTestPDF(PDFAutoInlinePageLimit + 5)
		path := writeFile("ranged.pdf", pdf)
		resp, err := tool.Execute(ctx, ViewParams{FilePath: path, Pages: "2-4"})
		require.NoError(t, err)
		assert.False(t, resp.IsError)
		assert.Equal(t, ToolResponseTypeImage, resp.Type)
		require.Len(t, resp.BinaryParts, 1)
		assert.Equal(t, "application/pdf", resp.BinaryParts[0].MIMEType)
		assert.Contains(t, resp.Content, "pages 2-4")
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

// buildTestPDF hand-writes a valid PDF with n blank pages for view/pagination tests.
func buildTestPDF(n int) []byte {
	var b strings.Builder
	offsets := []int{}
	write := func(s string) { b.WriteString(s) }
	obj := func(id int, body string) {
		offsets = append(offsets, b.Len())
		write(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", id, body))
	}
	write("%PDF-1.4\n")
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	kids := ""
	for i := 0; i < n; i++ {
		kids += fmt.Sprintf("%d 0 R ", 3+i)
	}
	obj(2, fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>", n, strings.TrimSpace(kids)))
	for i := 0; i < n; i++ {
		obj(3+i, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>")
	}
	xrefStart := b.Len()
	total := 2 + n
	write(fmt.Sprintf("xref\n0 %d\n", total+1))
	write("0000000000 65535 f \n")
	for _, off := range offsets {
		write(fmt.Sprintf("%010d 00000 n \n", off))
	}
	write(fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF", total+1, xrefStart))
	return []byte(b.String())
}

// TestViewReturnsTheFirstLine pins the CONTENT this tool returns, which nothing
// asserted before — view_test.go checked only truncation messages, which is why
// an off-by-one in the offset contract survived unnoticed.
//
// `offset` is 1-based: offset 1 is the first line of the file. It used to be a
// zero-based skip while the output was numbered one-based, so `offset: 1`
// silently dropped line 1 and numbered the rest correctly — nothing in the
// rendered output looked wrong. Measured in one run: 455 of ~520 reads passed
// `offset: 1`, and one cost a scaffolded file its `"use client";` directive when
// the agent composed the file back from what it had read.
func TestViewReturnsTheFirstLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tool := &viewTool{}
	worktree := &rctx.WorktreeInfo{ID: "test", Path: dir}
	ctx := rctx.NewToolContext(context.Background(), "test-chat", "0", nil, worktree).WithDaemon(daemon.NewLocalClient())

	for _, tc := range []struct {
		name    string
		content string
		offset  int
		want    string
	}{
		// The literal production hazard: a Next.js client component.
		{"offset 1 keeps the directive", "\"use client\";\n\nexport const x = 1;\n", 1, "\"use client\";"},
		// A one-line file must not render as empty. Four correctly-emitted
		// .down.sql files read as empty this way and were hand-rewritten.
		{"single line file", "DROP TABLE products;\n", 1, "DROP TABLE products;"},
		// Omitted offset arrives as 0 and must mean the same thing.
		{"offset 0 clamps to the start", "package main\n\nfunc main() {}\n", 0, "package main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, "f.txt")
			require.NoError(t, os.WriteFile(path, []byte(tc.content), 0o644))

			resp, err := tool.Execute(ctx, ViewParams{FilePath: path, Offset: tc.offset})
			require.NoError(t, err)
			assert.Contains(t, resp.Content, tc.want,
				"line 1 is missing — `offset: %d` must start AT line %d, not skip it", tc.offset, tc.offset)
		})
	}
}
