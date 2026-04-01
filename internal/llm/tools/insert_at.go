// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/diff"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type InsertAtParams struct {
	FilePath     string `json:"file_path" jsonschema:"required,description=The absolute path to the file to modify"`
	AnchorText   string `json:"anchor_text" jsonschema:"required,description=Unique text to find in the file as the anchor point. Must be unique within the file."`
	Position     string `json:"position" jsonschema:"required,enum=before|after|replace,description=Where to insert relative to anchor: before (insert before the line containing anchor), after (insert after the line containing anchor), replace (replace the line containing anchor)"`
	Content      string `json:"content" jsonschema:"required,description=The content to insert or replace with"`
	AnchorOffset int    `json:"anchor_offset,omitempty" jsonschema:"description=Number of lines to offset from the anchor line. Positive = down, negative = up. Default is 0."`
}

type InsertAtResponseMetadata struct {
	Diff       string `json:"diff"`
	Additions  int    `json:"additions"`
	Removals   int    `json:"removals"`
	AnchorLine int    `json:"anchor_line"`
}

type insertAtTool struct{}

const (
	InsertAtToolName    = "insert_at"
	insertAtDescription = `Insert or replace content relative to a text anchor - a more human-like editing approach.

WHEN TO USE:
- When you want to insert code relative to a known landmark (function name, comment, etc.)
- "Add this import after the existing imports"
- "Insert this method after the constructor"
- "Replace the error handling block"
- When exact line numbers might shift but anchor text remains stable

WHEN NOT TO USE:
- When you know exact line numbers - use edit_lines tool
- For pattern-based mass replacements - use find_replace tool
- For exact string matching - use edit tool

HOW IT WORKS:
1. Finds the FIRST line containing anchor_text
2. Optionally offsets by anchor_offset lines (+ = down, - = up)
3. Performs the operation (before, after, or replace) at that line

OPERATIONS:
- before: Insert content on new lines BEFORE the anchor line
- after: Insert content on new lines AFTER the anchor line
- replace: Replace the entire anchor line with content

EXAMPLES:

## Insert import after existing imports
{
  "file_path": "/path/to/file.go",
  "anchor_text": "import (",
  "position": "after",
  "content": "\t\"new/package\"",
  "anchor_offset": 0
}

## Add method after constructor
{
  "file_path": "/path/to/service.go",
  "anchor_text": "func NewService(",
  "position": "after",
  "anchor_offset": 3,
  "content": "\nfunc (s *Service) NewMethod() error {\n\treturn nil\n}"
}

## Replace a specific line
{
  "file_path": "/path/to/config.go",
  "anchor_text": "const DefaultTimeout",
  "position": "replace",
  "content": "const DefaultTimeout = 60 * time.Second"
}

CRITICAL REQUIREMENTS:
1. anchor_text must be UNIQUE in the file (or use anchor_offset to disambiguate)
2. anchor_text only needs to be a substring of the line, not the full line
3. The tool finds the line CONTAINING anchor_text, not exact match

BEST PRACTICES:
- Use distinctive anchor text (function signatures, unique comments)
- For non-unique text, use anchor_offset to target the right occurrence
- Include enough context in anchor_text to be unique
- Test with View tool to verify anchor location`
)

func NewInsertAtTool() Tool {
	tool := &insertAtTool{}
	return NewToolWrapper[InsertAtParams, ToolResponse](tool)
}

func (i *insertAtTool) Name() string {
	return InsertAtToolName
}

func (i *insertAtTool) Description() string {
	return insertAtDescription
}

func (i *insertAtTool) RequiresPermission(params InsertAtParams) (bool, error) {
	return true, nil
}

func (i *insertAtTool) Execute(rctx *rctx.ToolContext, params InsertAtParams) (ToolResponse, error) {
	if rctx.Daemon == nil {
		return NewTextErrorResponse("filesystem access requires a connected daemon"), nil
	}

	// Validate required parameters
	if params.FilePath == "" {
		return NewTextErrorResponse("file_path is required"), nil
	}
	if params.AnchorText == "" {
		return NewTextErrorResponse("anchor_text is required"), nil
	}
	if params.Position == "" {
		return NewTextErrorResponse("position is required (before, after, or replace)"), nil
	}
	if params.Content == "" {
		return NewTextErrorResponse("content is required"), nil
	}

	// Validate position
	validPositions := map[string]bool{
		"before":  true,
		"after":   true,
		"replace": true,
	}
	if !validPositions[params.Position] {
		return NewTextErrorResponse(fmt.Sprintf("invalid position '%s'. Must be one of: before, after, replace", params.Position)), nil
	}

	wd, err := GetWorkingDirectory(rctx)
	if err != nil {
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

	// Validate file exists
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
	lines := strings.Split(oldContent, "\n")

	// Find anchor line
	anchorIdx := -1
	matchCount := 0
	for idx, line := range lines {
		if strings.Contains(line, params.AnchorText) {
			if anchorIdx == -1 {
				anchorIdx = idx
			}
			matchCount++
		}
	}

	if anchorIdx == -1 {
		return NewTextErrorResponse(fmt.Sprintf("anchor_text '%s' not found in file. Make sure the text exists in the file.", params.AnchorText)), nil
	}

	if matchCount > 1 {
		return NewTextErrorResponse(fmt.Sprintf("anchor_text '%s' found %d times in file. Please use more specific text or use anchor_offset to target a specific occurrence.", params.AnchorText, matchCount)), nil
	}

	// Apply offset
	targetIdx := anchorIdx + params.AnchorOffset
	if targetIdx < 0 {
		return NewTextErrorResponse(fmt.Sprintf("anchor_offset %d would result in negative line number (anchor at line %d)", params.AnchorOffset, anchorIdx+1)), nil
	}
	if targetIdx >= len(lines) {
		return NewTextErrorResponse(fmt.Sprintf("anchor_offset %d would exceed file length (anchor at line %d, file has %d lines)", params.AnchorOffset, anchorIdx+1, len(lines))), nil
	}

	// Perform the operation
	var newLines []string
	contentLines := strings.Split(params.Content, "\n")

	switch params.Position {
	case "before":
		// Insert content before targetIdx
		newLines = append(newLines, lines[:targetIdx]...)
		newLines = append(newLines, contentLines...)
		newLines = append(newLines, lines[targetIdx:]...)

	case "after":
		// Insert content after targetIdx
		newLines = append(newLines, lines[:targetIdx+1]...)
		newLines = append(newLines, contentLines...)
		if targetIdx+1 < len(lines) {
			newLines = append(newLines, lines[targetIdx+1:]...)
		}

	case "replace":
		// Replace the line at targetIdx
		newLines = append(newLines, lines[:targetIdx]...)
		newLines = append(newLines, contentLines...)
		if targetIdx+1 < len(lines) {
			newLines = append(newLines, lines[targetIdx+1:]...)
		}
	}

	newContent := strings.Join(newLines, "\n")

	if oldContent == newContent {
		return NewTextErrorResponse("no changes would be made"), nil
	}

	// Generate diff
	diffText, additions, removals := diff.GenerateDiff(oldContent, newContent, filePath, wd)

	// Write file
	if _, err = rctx.Daemon.WriteFile(rctx.Context, filePath, newContent); err != nil {
		return ToolResponse{}, fmt.Errorf("failed to write file: %w", err)
	}

	recordFileAwareness(chatID, thread, filePath)

	// Build response
	posDescriptions := map[string]string{
		"before":  "Inserted content before",
		"after":   "Inserted content after",
		"replace": "Replaced",
	}

	displayPath := filePath
	if relPath, err := filepath.Rel(wd, filePath); err == nil {
		displayPath = relPath
	}

	result := fmt.Sprintf("<result>\n%s line %d (anchor: '%s') in %s (+%d/-%d lines)\n</result>\n",
		posDescriptions[params.Position], targetIdx+1, params.AnchorText, displayPath, additions, removals)

	return WithResponseMetadata(
		NewTextResponse(result),
		InsertAtResponseMetadata{
			Diff:       diffText,
			Additions:  additions,
			Removals:   removals,
			AnchorLine: targetIdx + 1,
		},
	), nil
}
