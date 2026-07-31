// Copyright (c) 2025 Reliant Labs
package tools

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/diff"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type EditLinesParams struct {
	FilePath   string `json:"file_path" jsonschema:"required,description=The absolute path to the file to modify"`
	StartLine  int    `json:"start_line" jsonschema:"required,description=The 1-indexed line number to start the operation (inclusive)"`
	EndLine    int    `json:"end_line,omitempty" jsonschema:"description=The 1-indexed line number to end the operation (inclusive). For insert operations leave empty or set to 0"`
	NewContent string `json:"new_content,omitempty" jsonschema:"description=The new content to insert or replace with. For delete operations leave empty"`
	Operation  string `json:"operation" jsonschema:"required,enum=replace|insert_before|insert_after|delete,description=The operation to perform: replace (replace lines start_line to end_line), insert_before (insert before start_line), insert_after (insert after start_line), delete (delete lines start_line to end_line)"`
	Repo       string `json:"repo,omitempty" jsonschema:"description=Multi-repo only. Which repo the path is relative to: 'root' for the project root\\, or a repo name (e.g. 'api'\\, 'web'). Used as the base for relative paths. Omit in single-repo projects or when path is absolute."`
}

type EditLinesResponseMetadata struct {
	Diff      string `json:"diff"`
	Additions int    `json:"additions"`
	Removals  int    `json:"removals"`
}

type editLinesTool struct{}

const (
	EditLinesToolName    = "edit_lines"
	editLinesDescription = `Edit files by line numbers - replace, insert, or delete specific line ranges.

WHEN TO USE:
- When you know the exact line numbers to modify
- Replacing a specific range of lines (e.g., "replace lines 45-60")
- Inserting content at a specific location (e.g., "insert after line 23")
- Deleting a range of lines (e.g., "delete lines 100-110")
- When View tool output shows exact line numbers you want to modify

WHEN NOT TO USE:
- When you don't know exact line numbers - use Edit tool with string matching
- For pattern-based replacements - use FindReplace tool
- For complex multi-file patches - use Patch tool

OPERATIONS:
- replace: Replace lines from start_line to end_line (inclusive) with new_content
- insert_before: Insert new_content before start_line (end_line ignored)
- insert_after: Insert new_content after start_line (end_line ignored)  
- delete: Delete lines from start_line to end_line (inclusive), new_content ignored

EXAMPLES:

## Replace lines 10-15 with new code
{
  "file_path": "/path/to/file.go",
  "start_line": 10,
  "end_line": 15,
  "new_content": "func newImplementation() {\n    return nil\n}",
  "operation": "replace"
}

## Insert after line 23
{
  "file_path": "/path/to/file.go",
  "start_line": 23,
  "new_content": "// New comment\nvar newVar = 42",
  "operation": "insert_after"
}

## Delete lines 100-110
{
  "file_path": "/path/to/file.go",
  "start_line": 100,
  "end_line": 110,
  "operation": "delete"
}

CRITICAL REQUIREMENTS:
1. Line numbers are 1-indexed (first line is 1, not 0)
2. Both start_line and end_line are INCLUSIVE
4. For insert operations, end_line is ignored
5. For delete operations, new_content is ignored

BEST PRACTICES:
- Double-check line numbers haven't shifted from previous edits
- Use replace with same start and end line to replace a single line
- Consider using Edit tool if line numbers might be imprecise`
)

func NewEditLinesTool() Tool {
	tool := &editLinesTool{}
	return NewToolWrapper[EditLinesParams, ToolResponse](tool)
}

func (e *editLinesTool) Name() string {
	return EditLinesToolName
}

func (e *editLinesTool) Description() string {
	return editLinesDescription
}

func (e *editLinesTool) RequiresPermission(params EditLinesParams) (bool, error) {
	return true, nil
}

func (e *editLinesTool) Execute(rctx *rctx.ToolContext, params EditLinesParams) (ToolResponse, error) {
	if rctx.Daemon == nil {
		return NewTextErrorResponse("filesystem access requires a connected daemon"), nil
	}

	// Validate required parameters
	if params.FilePath == "" {
		return NewTextErrorResponse("file_path is required"), nil
	}
	if params.StartLine < 1 {
		return NewTextErrorResponse("start_line must be >= 1 (line numbers are 1-indexed)"), nil
	}
	if params.Operation == "" {
		return NewTextErrorResponse("operation is required (replace, insert_before, insert_after, or delete)"), nil
	}

	// Validate operation
	validOps := map[string]bool{
		"replace":       true,
		"insert_before": true,
		"insert_after":  true,
		"delete":        true,
	}
	if !validOps[params.Operation] {
		return NewTextErrorResponse(fmt.Sprintf("invalid operation '%s'. Must be one of: replace, insert_before, insert_after, delete", params.Operation)), nil
	}

	// For replace and delete, end_line is required
	if (params.Operation == "replace" || params.Operation == "delete") && params.EndLine < params.StartLine {
		if params.EndLine == 0 {
			// Default to single line operation
			params.EndLine = params.StartLine
		} else {
			return NewTextErrorResponse(fmt.Sprintf("end_line (%d) must be >= start_line (%d)", params.EndLine, params.StartLine)), nil
		}
	}

	// For insert and replace, new_content is required (unless delete)
	if params.Operation != "delete" && params.NewContent == "" {
		return NewTextErrorResponse(fmt.Sprintf("new_content is required for %s operation", params.Operation)), nil
	}

	wd, err := ResolveRepoPath(rctx, params.Repo)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	if wd == "" {
		return NewTextErrorResponse("No project working directory available"), nil
	}

	filePath := params.FilePath
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(wd, filePath)
	}

	chatID := rctx.ChatID
	thread := rctx.Thread
	if thread == "" {
		thread = "0"
	}

	// The rest is a read-modify-write on filePath, and tool calls in one
	// assistant message run concurrently — hold the path lock across it so two
	// line edits to the same file cannot both splice the same starting content.
	// Line numbers make this especially unforgiving: a lost write here also means
	// the surviving edit was computed against line numbers that no longer hold.
	// See file_concurrency.go.
	return withPathLock(rctx, filePath, func() (ToolResponse, error) {
		return e.editLinesLocked(rctx, params, wd, filePath, chatID, thread)
	})
}

// editLinesLocked performs the read-modify-write. Callers must hold the path
// lock for filePath.
func (e *editLinesTool) editLinesLocked(rctx *rctx.ToolContext, params EditLinesParams, wd, filePath, chatID, thread string) (ToolResponse, error) {
	// Validate file exists and has been read
	stat, err := rctx.Daemon.StatFile(rctx.Context, filePath)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("failed to access file: %w", err)
	}

	if !stat.Exists {
		return NewTextErrorResponse(fmt.Sprintf("file not found: %s", filePath)), nil
	}

	if stat.IsDir {
		return NewTextErrorResponse(fmt.Sprintf("path is a directory, not a file: %s", filePath)), nil
	}

	// Read file content
	fc, err := rctx.Daemon.ReadFile(rctx.Context, filePath, nil)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("failed to read file: %w", err)
	}

	oldContent := fc.Content
	lines, terminated := splitLines(oldContent)
	totalLines := len(lines)

	// Validate line numbers
	if params.StartLine > totalLines {
		return NewTextErrorResponse(fmt.Sprintf("start_line %d exceeds file length (%d lines)", params.StartLine, totalLines)), nil
	}
	if params.EndLine > totalLines && (params.Operation == "replace" || params.Operation == "delete") {
		return NewTextErrorResponse(fmt.Sprintf("end_line %d exceeds file length (%d lines)", params.EndLine, totalLines)), nil
	}

	// Convert to 0-indexed
	startIdx := params.StartLine - 1
	endIdx := params.EndLine - 1

	var newLines []string
	var newContent string

	switch params.Operation {
	case "replace":
		// Replace lines from startIdx to endIdx (inclusive) with new content
		newContentLines := strings.Split(params.NewContent, "\n")
		newLines = append(newLines, lines[:startIdx]...)
		newLines = append(newLines, newContentLines...)
		if endIdx+1 < len(lines) {
			newLines = append(newLines, lines[endIdx+1:]...)
		}
		newContent = joinLines(newLines, terminated)

	case "insert_before":
		// Insert new content before startIdx
		newContentLines := strings.Split(params.NewContent, "\n")
		newLines = append(newLines, lines[:startIdx]...)
		newLines = append(newLines, newContentLines...)
		newLines = append(newLines, lines[startIdx:]...)
		newContent = joinLines(newLines, terminated)

	case "insert_after":
		// Insert new content after startIdx
		newContentLines := strings.Split(params.NewContent, "\n")
		newLines = append(newLines, lines[:startIdx+1]...)
		newLines = append(newLines, newContentLines...)
		if startIdx+1 < len(lines) {
			newLines = append(newLines, lines[startIdx+1:]...)
		}
		newContent = joinLines(newLines, terminated)

	case "delete":
		// Delete lines from startIdx to endIdx (inclusive)
		newLines = append(newLines, lines[:startIdx]...)
		if endIdx+1 < len(lines) {
			newLines = append(newLines, lines[endIdx+1:]...)
		}
		newContent = joinLines(newLines, terminated)
	}

	if oldContent == newContent {
		return NewTextErrorResponse("no changes would be made"), nil
	}

	// Generate diff
	diffText, additions, removals := diff.GenerateDiff(oldContent, newContent, filePath, wd)

	// Write file
	if err = writeFileGuarded(rctx, filePath, oldContent, newContent); err != nil {
		var conflict *ConcurrentModificationError
		if errors.As(err, &conflict) {
			return NewTextErrorResponse(conflict.Error()), nil
		}
		return ToolResponse{}, fmt.Errorf("failed to write file: %w", err)
	}

	recordFileAwareness(chatID, thread, filePath)

	// Build response
	opDescriptions := map[string]string{
		"replace":       fmt.Sprintf("Replaced lines %d-%d", params.StartLine, params.EndLine),
		"insert_before": fmt.Sprintf("Inserted content before line %d", params.StartLine),
		"insert_after":  fmt.Sprintf("Inserted content after line %d", params.StartLine),
		"delete":        fmt.Sprintf("Deleted lines %d-%d", params.StartLine, params.EndLine),
	}

	result := fmt.Sprintf("<result>\n%s in %s (+%d/-%d lines)\n</result>\n", opDescriptions[params.Operation], filePath, additions, removals)

	return WithResponseMetadata(
		NewTextResponse(result),
		EditLinesResponseMetadata{
			Diff:      diffText,
			Additions: additions,
			Removals:  removals,
		},
	), nil
}
