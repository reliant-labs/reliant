// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type ViewParams struct {
	FilePath string `json:"file_path" jsonschema:"required,description=the file to view"`
	Offset   int    `json:"offset,omitempty" jsonschema:"description=The line to start reading from, default is 0"`
	Limit    int    `json:"limit,omitempty" jsonschema:"description=The amount of lines to read, maximum is 256000, and the default (if empty), is 300. Only set the limit for large files."`
}

type viewTool struct{}

type ViewResponseMetadata struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
	HasMore  bool   `json:"has_more"`
}

const (
	ViewToolName     = "view"
	MaxReadSize      = 16 * 1024 // 16KB - matches MaxOutputSize to avoid reading more than we'll output
	DefaultReadLimit = 300
	MaxLineLength    = 500
	viewDescription  = `File viewing tool that reads and displays the contents of files with line numbers, allowing you to examine code, logs, or text data.

WHEN TO USE:
- Reading contents of specific files (source code, configs, logs)
- Examining text-based file formats

HOW TO USE:
- Provide the file path
- Optional: offset (starting line) and limit (number of lines)
- Issue multiple view tools in a single request for improved performance

FEATURES:
- Displays file contents with line numbers for easy reference
- Can read from any position in a file using the offset parameter
- Handles large files by limiting the number of lines read
- Automatically truncates very long lines for better display
- Suggests similar file names when the requested file isn't found

LIMITATIONS:
- Maximum output size is 16KB (~4K tokens) - larger files are truncated with head+tail
- Default reading limit is 300 lines
- Lines longer than 500 characters are truncated
- Cannot display binary files or images
- Images can be identified but not displayed

TIPS:
- Use with Glob tool to first find files you want to view
- For code exploration, first use Grep to find relevant files, then View to examine them
- When viewing large files, use the offset parameter to read specific sections
- If output is truncated, use offset to read the middle section`
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
	workingDir, err := GetWorkingDirectory(rctx)
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

	// Check if it's an image file
	isImage, imageType := isImageFile(filePath)
	if isImage {
		// Convert to relative path for error message
		displayPath := filePath
		if relPath, err := filepath.Rel(workingDir, filePath); err == nil {
			displayPath = relPath
		}
		return NewTextErrorResponse(fmt.Sprintf("Image file detected (%s): %s\nImage viewing not yet implemented.", imageType, displayPath)), nil
	}

	// Check for potentially problematic files (minified files, etc.)
	if isLikelyMinifiedFile(filePath) && stat.Size > 50*1024 { // > 50KB minified
		logging.Debug("Potentially minified file detected", "path", filePath, "size", stat.Size)
		// Still allow reading, but the byte/line limits will protect us
	}

	// Read the file content via daemon
	fc, err := rctx.Daemon.ReadFile(rctx.Context, filePath, &daemon.ReadFileOpts{
		Offset: params.Offset,
		Limit:  params.Limit,
	})
	if err != nil {
		return ToolResponse{}, fmt.Errorf("error reading file: %w", err)
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
	output += addLineNumbers(content, params.Offset+1)

	// Build actionable truncation message when not all content was shown
	hasMore := fc.Truncated || byteLimitReached
	if hasMore {
		linesRead := countLines(content)
		nextOffset := params.Offset + linesRead
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
			params.Offset+1, nextOffset, fc.TotalLines, fileSize, reason,
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

func addLineNumbers(content string, startLine int) string {
	if content == "" {
		return ""
	}

	lines := strings.Split(content, "\n")

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
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if len(line) > MaxLineLength {
			lines[i] = line[:MaxLineLength] + "..."
		}
	}
	return strings.Join(lines, "\n")
}

// countLines returns the number of lines in content.
func countLines(content string) int {
	if content == "" {
		return 0
	}
	return strings.Count(content, "\n") + 1
}

func isImageFile(filePath string) (bool, string) {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".jpg", ".jpeg":
		return true, "JPEG"
	case ".png":
		return true, "PNG"
	case ".gif":
		return true, "GIF"
	case ".bmp":
		return true, "BMP"
	case ".svg":
		return true, "SVG"
	case ".webp":
		return true, "WebP"
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
