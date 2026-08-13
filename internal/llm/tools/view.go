// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/filepreview"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/pdfutil"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type ViewParams struct {
	FilePath string `json:"file_path" jsonschema:"required,description=the file to view"`
	Offset   int    `json:"offset,omitempty" jsonschema:"description=The 1-based line number to start reading from. Line 1 is the first line of the file. Default 1 — omit it to read from the start."`
	Limit    int    `json:"limit,omitempty" jsonschema:"description=The amount of lines to read, maximum is 256000, and the default (if empty), is 1500. Omit it for ordinary source files — the default reads most files whole. Only set it for very large files."`
	Pages    string `json:"pages,omitempty" jsonschema:"description=PDF files only. Page range to read (e.g. '1-5'\\, '3'\\, '10-20'). Maximum 20 pages per request. Required for PDFs larger than 10 pages; ignored for non-PDF files."`
	Repo     string `json:"repo,omitempty" jsonschema:"description=Multi-repo only. Which repo the path is relative to: 'root' for the project root\\, or a repo name (e.g. 'api'\\, 'web'). Used as the base for relative paths. Omit in single-repo projects or when path is absolute."`
}

type viewTool struct{}

type ViewResponseMetadata struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
	HasMore  bool   `json:"has_more"`
}

const (
	ViewToolName = "view"
	// MaxReadSize is the byte ceiling for ONE file read. It is deliberately
	// LARGER than MaxOutputSize: a read is one contiguous artifact the agent
	// asked for by name, where a truncated middle is not a smaller answer but
	// a WRONG one — the symbol it needed is as likely to be in the hole as
	// not. Shell output and skills keep the smaller shared ceiling, which is
	// the right call for a command whose output volume nobody chose and for
	// authored content with a publishing budget.
	//
	// 64KB ≈ 16K tokens. Measured: a 52KB generated proto (1,685 lines) could
	// not arrive whole at 24KB, so eleven fan-out agents fell back to paging
	// it by `grep`, one message definition at a time — 123 locate-calls and
	// ~28 minutes, the single largest recoverable cost in that run. One read
	// of the whole file costs ~13K tokens once; the grep loop cost a turn
	// each time and still never showed any agent the whole contract.
	//
	// Files larger than this still truncate, and that is intended: past ~16K
	// tokens the right move is a targeted offset read, which the truncation
	// notice names along with the file's true line count.
	MaxReadSize       = 64_000
	MaxBinaryFileSize = 5 * 1024 * 1024 // 5MB max for binary files (images, PDFs)
	// DefaultReadLimit is the line ceiling for a read that does not ask for
	// one. Every view costs a full model round-trip while the read itself
	// costs milliseconds, so paging a 900-line file 300 lines at a time buys
	// nothing and spends two extra turns. The byte ceiling above is the real
	// protection against a huge file; this number only has to be large enough
	// that ordinary source files arrive whole.
	DefaultReadLimit = 1500
	MaxLineLength    = 500
	// PDFAutoInlinePageLimit is the largest PDF (in pages) returned whole without
	// requiring an explicit page range. Larger PDFs must be read a range at a time
	// so a single view call doesn't flood the context window.
	PDFAutoInlinePageLimit = 10
	viewDescription        = `File viewing tool that reads and displays the contents of files with line numbers, allowing you to examine code, logs, or text data.

WHEN TO USE:
- Reading contents of specific files (source code, configs, logs)
- Examining text-based file formats

HOW TO USE:
- Provide the file path
- Optional: offset (starting line) and limit (number of lines)
- For PDFs: use the pages parameter to read a page range (e.g. "1-5"). PDFs larger than 10 pages require a page range; max 20 pages per request.
- Issue multiple view tools in a single request for improved performance

FEATURES:
- Displays file contents with line numbers for easy reference
- Can read from any position in a file using the offset parameter
- Handles large files by limiting the number of lines read
- Automatically truncates very long lines for better display
- Suggests similar file names when the requested file isn't found

LIMITATIONS:
- Maximum output size is 64KB (~16K tokens) - larger files are truncated with head+tail
- Default reading limit is 1500 lines, which reads most source files whole
- Lines longer than 500 characters are truncated
- Cannot display binary files (executables, archives, etc.)
- Images up to 5MB are supported (JPEG, PNG, GIF, BMP, SVG, WebP)
- PDFs up to 5MB are supported; large PDFs are read a page range at a time via the pages parameter

TIPS:
- Prefer ONE whole-file read over several paged reads: each call costs a model
  round-trip, while the read itself takes milliseconds. Omit offset/limit unless
  the file is genuinely too big to arrive in one piece.
- A file OVER ~64KB cannot arrive whole whatever limit you pass — the response is
  capped and you get head+tail with the middle removed. Two calls settle it, and
  neither is a search: read the head (default) to get the shape, then ONE offset
  read for the region you need. The truncation notice tells you the total line
  count, so you can aim the second call directly.
- Do NOT fall back to repeated 'grep' on a large file to page through it by
  symbol. Measured: eleven agents greping one 52KB proto one message at a time
  was the single largest recoverable cost in a long run — each grep is a whole
  turn and returns less context than one offset read.
- Issue several view calls in a SINGLE message to read independent files at once
- Use with Glob tool to first find files you want to view
- For code exploration, first use Grep to find relevant files, then View to examine them
- If output is truncated, use offset to read the remaining section`
)

func NewViewTool() Tool {
	tool := &viewTool{}
	return NewToolWrapper(tool)
}

func (v *viewTool) Name() string {
	return ViewToolName
}

func (v *viewTool) Description() string {
	return viewDescription
}

func (v *viewTool) RequiresPermission(params ViewParams) (bool, error) {
	// view tool doesn't require permissions as it's read-only
	return false, nil
}

// Run implements Tool.
func (v *viewTool) Execute(rctx *rctx.ToolContext, params ViewParams) (ToolResponse, error) {
	logging.Debug("view tool params", "params", params)

	if rctx.Daemon == nil {
		return NewTextErrorResponse("filesystem access requires a connected daemon"), nil
	}

	if params.FilePath == "" {
		return NewTextErrorResponse("file_path is required"), nil
	}

	// Handle relative paths
	workingDir, err := ResolveRepoPath(rctx, params.Repo)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("failed to resolve working directory: %v", err)), nil
	}

	filePath := params.FilePath
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(workingDir, filePath)
	}

	// Check if file exists
	stat, err := rctx.Daemon.StatFile(rctx.Context, filePath)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("error accessing file: %w", err)
	}

	if !stat.Exists {
		// Convert to relative path for error message
		displayPath := filePath
		if relPath, err := filepath.Rel(workingDir, filePath); err == nil {
			displayPath = relPath
		}

		// Try to offer suggestions for similarly named files
		dir := filepath.Dir(filePath)
		base := filepath.Base(filePath)

		dirEntries, dirErr := rctx.Daemon.ListDirectory(rctx.Context, dir)
		if dirErr == nil {
			var suggestions []string
			for _, entry := range dirEntries {
				if strings.Contains(strings.ToLower(entry.Name), strings.ToLower(base)) ||
					strings.Contains(strings.ToLower(base), strings.ToLower(entry.Name)) {
					suggestedPath := filepath.Join(dir, entry.Name)
					// Convert suggestion to relative path
					if relPath, err := filepath.Rel(workingDir, suggestedPath); err == nil {
						suggestions = append(suggestions, relPath)
					} else {
						suggestions = append(suggestions, suggestedPath)
					}
					if len(suggestions) >= 3 {
						break
					}
				}
			}

			if len(suggestions) > 0 {
				return NewTextErrorResponse(fmt.Sprintf("File not found: %s\n\nDid you mean one of these?\n%s",
					displayPath, strings.Join(suggestions, "\n"))), nil
			}
		}

		return NewTextErrorResponse(fmt.Sprintf("File not found: %s", displayPath)), nil
	}

	// Check if it's a directory
	if stat.IsDir {
		// Convert to relative path for error message
		displayPath := filePath
		if relPath, err := filepath.Rel(workingDir, filePath); err == nil {
			displayPath = relPath
		}
		return NewTextErrorResponse(fmt.Sprintf("Path is a directory, not a file: %s", displayPath)), nil
	}

	// Set default limit if not provided
	if params.Limit <= 0 {
		params.Limit = DefaultReadLimit
	}

	// Convert to relative path for display (used in error messages below)
	displayPath := filePath
	if relPath, err := filepath.Rel(workingDir, filePath); err == nil {
		displayPath = relPath
	}

	// Check if it's an image file — read binary and return as image content
	isImage, mimeType := isImageFile(filePath)
	if isImage {
		data, err := rctx.Daemon.ReadBinaryFile(rctx.Context, filePath, MaxBinaryFileSize)
		if err != nil {
			return NewTextErrorResponse(fmt.Sprintf("Failed to read image file %s: %v", displayPath, err)), nil
		}
		return NewImageResponse(
			fmt.Sprintf("Image file: %s (%s, %s)", displayPath, mimeType, formatFileSize(int64(len(data)))),
			[]message.BinaryContent{{
				Path:     filePath,
				MIMEType: mimeType,
				Data:     data,
			}},
		), nil
	}

	// Check if it's a PDF — read as a native document block, paginating large
	// documents so a single view call can't flood the context window.
	if strings.ToLower(filepath.Ext(filePath)) == ".pdf" {
		return v.viewPDF(rctx, filePath, displayPath, params.Pages)
	}

	// Check for other known binary extensions
	if filepreview.IsBinaryExtension(filepath.Ext(filePath)) {
		return NewTextErrorResponse(fmt.Sprintf("Binary file detected: %s\nThis file cannot be displayed as text.", displayPath)), nil
	}

	// Check for potentially problematic files (minified files, etc.)
	if isLikelyMinifiedFile(filePath) && stat.Size > 50*1024 { // > 50KB minified
		logging.Debug("Potentially minified file detected", "path", filePath, "size", stat.Size)
		// Still allow reading, but the byte/line limits will protect us
	}

	// `offset` is 1-BASED: offset 1 is the first line. 0 is accepted and clamped,
	// so a caller that omits it still reads from the start.
	//
	// The reader below takes a zero-based skip, so the conversion happens exactly
	// ONCE, here. The previous split — zero-based in, one-based line numbers out —
	// is what made `offset: 1` silently drop line 1 while numbering the rest
	// correctly, so nothing in the output looked wrong. Measured: 455 of ~520
	// reads in one run passed `offset: 1`, and one of them cost a scaffolded file
	// its `"use client";` directive when the agent composed the file back.
	startLine := params.Offset
	if startLine < 1 {
		startLine = 1
	}

	// Read the file content via daemon
	fc, err := rctx.Daemon.ReadFile(rctx.Context, filePath, &daemon.ReadFileOpts{
		Offset: startLine - 1,
		Limit:  params.Limit,
	})
	if err != nil {
		return ToolResponse{}, fmt.Errorf("error reading file: %w", err)
	}

	// Runtime binary content check for files without recognized binary extensions
	if filepreview.HasBinaryContent([]byte(fc.Content)) {
		return NewTextErrorResponse(fmt.Sprintf("Binary file detected: %s\nThis file cannot be displayed as text.", displayPath)), nil
	}

	// Truncate long lines in the returned content
	content := truncateLongLines(fc.Content)

	// Apply byte-level truncation if content exceeds MaxReadSize.
	byteLimitReached := false
	if len(content) > MaxReadSize {
		// Trim to the last complete line before the limit.
		truncContent := content[:MaxReadSize]
		if idx := strings.LastIndex(truncContent, "\n"); idx > 0 {
			content = truncContent[:idx]
		} else {
			content = truncContent
		}
		byteLimitReached = true
	}

	output := "<file>\n"
	// Format the output with line numbers
	output += addLineNumbers(content, startLine)

	// Build actionable truncation message when not all content was shown
	hasMore := fc.Truncated || byteLimitReached
	if hasMore {
		linesRead := countLines(content)
		// 1-based, so this is the LINE NUMBER to resume at — the caller passes it
		// straight back as `offset` and the seam is exact.
		nextOffset := startLine + linesRead
		fileSize := formatFileSize(stat.Size)

		var reason string
		if byteLimitReached {
			reason = "byte limit reached"
		} else {
			reason = fmt.Sprintf("%d line limit reached", params.Limit)
		}

		output += fmt.Sprintf(
			"\n\n--- Truncated: Showing lines %d-%d of %d total (%s, %s) ---\n"+
				"Use offset=%d to continue reading\n"+
				"Use grep to find specific lines, then view with offset",
			startLine, nextOffset, fc.TotalLines, fileSize, reason,
			nextOffset,
		)
	}
	output += "\n</file>\n"

	chatID := rctx.ChatID
	thread := rctx.Thread
	if thread == "" {
		thread = "0" // Default to main thread if not set
	}
	recordFileAwareness(chatID, thread, filePath)
	return WithResponseMetadata(
		NewTextResponse(output),
		ViewResponseMetadata{
			FilePath: filePath,
			Content:  content,
			HasMore:  hasMore,
		},
	), nil
}

// viewPDF returns a PDF as a native document block, paginating large documents.
// If pages is set, only that page range is returned (capped at 20 pages by the
// daemon). If pages is empty and the document has more than PDFAutoInlinePageLimit
// pages, a text prompt asks the model to request a specific range instead of
// dumping the whole file into context. Small PDFs are returned whole.
func (v *viewTool) viewPDF(rctx *rctx.ToolContext, filePath, displayPath, pages string) (ToolResponse, error) {
	if pages != "" {
		data, err := rctx.Daemon.ReadPDFPages(rctx.Context, filePath, pages)
		if err != nil {
			return NewTextErrorResponse(fmt.Sprintf("Failed to read pages %q of PDF %s: %v", pages, displayPath, err)), nil
		}
		return NewImageResponse(
			fmt.Sprintf("PDF file: %s (pages %s, %s)", displayPath, pages, formatFileSize(int64(len(data)))),
			[]message.BinaryContent{{
				Path:     filePath,
				MIMEType: "application/pdf",
				Data:     data,
			}},
		), nil
	}

	pageCount, err := rctx.Daemon.PDFPageCount(rctx.Context, filePath)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to read PDF file %s: %v", displayPath, err)), nil
	}

	if pageCount > PDFAutoInlinePageLimit {
		return NewTextResponse(fmt.Sprintf(
			"PDF file: %s has %d pages. Call view again with the pages parameter to read a range "+
				"(e.g. pages=%q). Maximum %d pages per request.",
			displayPath, pageCount, fmt.Sprintf("1-%d", PDFAutoInlinePageLimit), pdfutil.MaxPagesPerRequest,
		)), nil
	}

	// Small PDF — return the whole document.
	data, err := rctx.Daemon.ReadBinaryFile(rctx.Context, filePath, MaxBinaryFileSize)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to read PDF file %s: %v", displayPath, err)), nil
	}
	return NewImageResponse(
		fmt.Sprintf("PDF file: %s (%d pages, %s)", displayPath, pageCount, formatFileSize(int64(len(data)))),
		[]message.BinaryContent{{
			Path:     filePath,
			MIMEType: "application/pdf",
			Data:     data,
		}},
	), nil
}

func addLineNumbers(content string, startLine int) string {
	if content == "" {
		return ""
	}

	// splitLines, not strings.Split: content is byte-exact, so a file ending in
	// a newline would otherwise gain a phantom numbered blank line at the end.
	lines, _ := splitLines(content)

	var result []string
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")

		lineNum := i + startLine
		numStr := fmt.Sprintf("%d", lineNum)

		if len(numStr) >= 6 {
			result = append(result, fmt.Sprintf("%s|%s", numStr, line))
		} else {
			paddedNum := fmt.Sprintf("%6s", numStr)
			result = append(result, fmt.Sprintf("%s|%s", paddedNum, line))
		}
	}

	return strings.Join(result, "\n")
}

// truncateLongLines truncates lines that exceed MaxLineLength.
func truncateLongLines(content string) string {
	lines, terminated := splitLines(content)
	for i, line := range lines {
		if len(line) > MaxLineLength {
			lines[i] = line[:MaxLineLength] + "..."
		}
	}
	return joinLines(lines, terminated)
}

// countLines returns the number of lines in content. A trailing newline
// terminates the last line rather than starting another, so "a\nb\n" and "a\nb"
// are both two lines — this feeds the "showing lines X-Y of N" hint and the
// next offset a paginating view call should ask for.
func countLines(content string) int {
	lines, _ := splitLines(content)
	return len(lines)
}

func isImageFile(filePath string) (bool, string) {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".jpg", ".jpeg":
		return true, "image/jpeg"
	case ".png":
		return true, "image/png"
	case ".gif":
		return true, "image/gif"
	case ".bmp":
		return true, "image/bmp"
	case ".svg":
		return true, "image/svg+xml"
	case ".webp":
		return true, "image/webp"
	default:
		return false, ""
	}
}

func formatFileSize(bytes int64) string {
	switch {
	case bytes >= 1024*1024:
		return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
	case bytes >= 1024:
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}

func isLikelyMinifiedFile(filePath string) bool {
	base := filepath.Base(filePath)
	// Check for common minified file patterns
	return strings.Contains(base, ".min.") ||
		strings.HasSuffix(base, ".min.js") ||
		strings.HasSuffix(base, ".min.css")
}
