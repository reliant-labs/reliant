// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/diff"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type EditOperation struct {
	FilePath   string `json:"file_path" jsonschema:"required,description=The absolute path to the file to modify"`
	OldString  string `json:"old_string,omitempty" jsonschema:"description=The text to replace (leave empty when creating a new file)"`
	NewString  string `json:"new_string,omitempty" jsonschema:"description=The edited text to replace the old_string (leave empty when deleting content)"`
	ReplaceAll bool   `json:"replace_all,omitempty" jsonschema:"description=Replace all occurrences of old_string in the file (default: false)"`
}

type EditParams struct {
	Edits []EditOperation `json:"edits" jsonschema:"required,description=Array of edit operations to perform. All edits must be provided in this array."`
}

type EditPermissionsParams struct {
	FilePath string `json:"file_path"`
	Diff     string `json:"diff"`
}

type EditResponseMetadata struct {
	Diff      string `json:"diff"`
	Additions int    `json:"additions"`
	Removals  int    `json:"removals"`
}

type editTool struct{}

const (
	EditToolName    = "edit"
	editDescription = `Make precise text replacements in files or create new files. All edit operations must be provided in the edits array.

NOTE: You can parallelize multiple edit tool calls in one message, even if they involve the same files.

WHEN TO USE:
- Precise text replacements
- Creating new files (empty old_string)
- Deleting specific content (empty new_string)
- Renaming variables/functions (with replace_all)
- Coordinated changes across multiple files

WHEN NOT TO USE:
- Complete file rewrite: Use Write tool
- Moving/renaming files: Use Bash mv command

COMMON MISTAKES TO AVOID:
- Insufficient context in old_string (needs 3-5 lines)
- Forgetting whitespace/indentation must match exactly
- Not checking if text appears multiple times

USAGE PATTERNS:

## One Edit
edits: [
  {
    file_path: "/path/to/file.go",
    old_string: "Include 3-5 lines before AND after",
    new_string: "Your replacement text",
    replace_all: false
  }
]

## Multiple Edits
edits: [
  {
    file_path: "/path/to/file1.go",
    old_string: "old text 1",
    new_string: "new text 1"
  },
  {
    file_path: "/path/to/file2.go",
    old_string: "old text 2",
    new_string: "new text 2"
  }
]

## Create New File
edits: [
  {
    file_path: "/path/to/new/file.go",
    old_string: "",
    new_string: "file contents"
  }
]

## Delete Content
edits: [
  {
    file_path: "/path/to/file.go",
    old_string: "text to remove",
    new_string: ""
  }
]

## Rename Variable
edits: [
  {
    file_path: "/path/to/file.go",
    old_string: "oldName",
    new_string: "newName",
    replace_all: true
  }
]

# 🎯 CRITICAL REQUIREMENTS

1. UNIQUENESS (when replace_all=false):
   - Include 3-5 lines of context BEFORE
   - Include 3-5 lines of context AFTER
   - Match whitespace/indentation EXACTLY

2. VERIFICATION CHECKLIST:
   □ Check how many times text appears
   □ Include enough context for uniqueness
   □ Verify parent directories exist (new files)

3. FAILURE CONDITIONS:
   - old_string not found → FAILS
   - Multiple matches (without replace_all) → FAILS
   - Whitespace mismatch → FAILS

# 💡 BEST PRACTICES
- Include ample context
- All operations are atomic (all succeed or all fail)
- Use replace_all for systematic renames
- Verify edits don't break code

# 🔄 WORKS WELL WITH
- AFTER: Bash (test changes)
- ALTERNATIVE: Write (complete rewrite)

# 📝 PARAMETERS
- edits: Array of edit operations (required)
  - file_path: Absolute path (required)
  - old_string: Text to find (exact match)
  - new_string: Replacement text
  - replace_all: Replace all occurrences (optional)

Remember: This tool requires EXACT text matching including all whitespace and indentation.`
)

func NewEditTool() Tool {
	tool := &editTool{}
	return NewToolWrapper[EditParams, ToolResponse](tool)
}

func (e *editTool) Name() string {
	return EditToolName
}

func (e *editTool) Description() string {
	return editDescription
}

func (e *editTool) RequiresPermission(params EditParams) (bool, error) {
	// edit tool requires permissions as it's a write operation
	return true, nil
}

func (e *editTool) validateFileForEdit(rctx *rctx.ToolContext, chatID, thread, filePath string) error {
	stat, err := rctx.Daemon.StatFile(rctx.Context, filePath)
	if err != nil {
		return fmt.Errorf("failed to access file: %w", err)
	}

	if !stat.Exists {
		return fmt.Errorf("file not found: %s", filePath)
	}

	if stat.IsDir {
		return fmt.Errorf("path is a directory, not a file: %s", filePath)
	}

	return nil
}

func (e *editTool) Execute(rctx *rctx.ToolContext, params EditParams) (ToolResponse, error) {
	logging.Debug("Edit tool Execute called", "edits_count", len(params.Edits), "chatID", rctx.ChatID, "thread", rctx.Thread)

	if rctx.Daemon == nil {
		return NewTextErrorResponse("filesystem access requires a connected daemon"), nil
	}

	if len(params.Edits) == 0 {
		logging.Warn("Edit tool called with zero edits", "chatID", rctx.ChatID, "thread", rctx.Thread)
		return NewTextErrorResponse("at least one edit operation is required"), nil
	}

	wd, err := GetWorkingDirectory(rctx)
	if err != nil {
		return NewTextErrorResponse("No project working directory available - ensure you're working within a project"), nil
	}

	// Normalize thread once for consistency across all operations
	thread := rctx.Thread
	if thread == "" {
		thread = "0"
	}

	// Phase 1: Validate all operations
	processedEdits := make([]EditOperation, len(params.Edits))
	for i, edit := range params.Edits {
		if edit.FilePath == "" {
			return NewTextErrorResponse(fmt.Sprintf("file_path is required for edit operation %d", i+1)), nil
		}

		// Convert to absolute path
		if !filepath.IsAbs(edit.FilePath) {
			edit.FilePath = filepath.Join(wd, edit.FilePath)
		}
		processedEdits[i] = edit

		// Validate file access for existing files
		if edit.OldString != "" {
			if err := e.validateFileForEdit(rctx, rctx.ChatID, thread, edit.FilePath); err != nil {
				return NewTextErrorResponse(fmt.Sprintf("edit operation %d: %s", i+1, err.Error())), nil
			}
		}
	}

	// Phase 2: Apply all operations atomically
	var allResponses []ToolResponse
	var totalAdditions, totalRemovals int

	for i, edit := range processedEdits {
		var response ToolResponse
		var err error

		// Handle creating new file (empty old_string)
		if edit.OldString == "" {
			response, err = e.createNewFile(rctx, thread, edit.FilePath, edit.NewString)
		} else if edit.NewString == "" {
			// Handle deleting content (empty new_string)
			response, err = e.deleteContent(rctx, thread, edit.FilePath, edit.OldString)
		} else {
			// Handle normal content replacement
			response, err = e.replaceContent(rctx, thread, edit.FilePath, edit.OldString, edit.NewString, edit.ReplaceAll)
		}

		if err != nil {
			return response, err
		}
		if response.IsError {
			return NewTextErrorResponse(fmt.Sprintf("edit operation %d failed: %s", i+1, response.Content)), nil
		}

		allResponses = append(allResponses, response)

		// Extract metadata from JSON string for aggregation
		if response.Metadata != "" {
			var metadata EditResponseMetadata
			if err := json.Unmarshal([]byte(response.Metadata), &metadata); err == nil {
				totalAdditions += metadata.Additions
				totalRemovals += metadata.Removals
			}
		}
	}

	// Phase 3: Build response
	var resultText string
	if len(allResponses) == 1 {
		resultText = allResponses[0].Content
	} else {
		resultText = fmt.Sprintf("Successfully applied %d edit operations:", len(allResponses))
		for i, response := range allResponses {
			resultText += fmt.Sprintf("\n%d. %s", i+1, response.Content)
		}
	}

	finalText := fmt.Sprintf("<result>\n%s\n</result>\n", resultText)

	return WithResponseMetadata(
		NewTextResponse(finalText),
		EditResponseMetadata{
			Diff:      "", // Combined diff could be added later if needed
			Additions: totalAdditions,
			Removals:  totalRemovals,
		},
	), nil
}

func (e *editTool) createNewFile(rctx *rctx.ToolContext, thread, filePath, content string) (ToolResponse, error) {
	stat, err := rctx.Daemon.StatFile(rctx.Context, filePath)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("failed to access file: %w", err)
	}
	if stat.Exists {
		if stat.IsDir {
			return NewTextErrorResponse(fmt.Sprintf("path is a directory, not a file: %s", filePath)), nil
		}
		return NewTextErrorResponse(fmt.Sprintf("file already exists: %s", filePath)), nil
	}

	dir := filepath.Dir(filePath)
	if err = rctx.Daemon.CreateDirectory(rctx.Context, dir); err != nil {
		return ToolResponse{}, fmt.Errorf("failed to create parent directories: %w", err)
	}

	chatID := rctx.ChatID
	if chatID == "" {
		return ToolResponse{}, fmt.Errorf("chat ID is required")
	}

	rootDir, err := GetWorkingDirectory(rctx)
	if err != nil {
		return NewTextErrorResponse("No project working directory available - ensure you're working within a project"), nil
	}

	// Generate diff for response metadata
	diff, additions, removals := diff.GenerateDiff(
		"",
		content,
		filePath,
		rootDir,
	)

	if _, err = rctx.Daemon.WriteFile(rctx.Context, filePath, content); err != nil {
		return ToolResponse{}, fmt.Errorf("failed to write file: %w", err)
	}

	recordFileAwareness(chatID, thread, filePath)

	return WithResponseMetadata(
		NewTextResponse("File created: "+filePath),
		EditResponseMetadata{
			Diff:      diff,
			Additions: additions,
			Removals:  removals,
		},
	), nil
}

func (e *editTool) deleteContent(rctx *rctx.ToolContext, thread, filePath, oldString string) (ToolResponse, error) {
	chatID := rctx.ChatID

	// Validate file can be edited
	if err := e.validateFileForEdit(rctx, chatID, thread, filePath); err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	fc, err := rctx.Daemon.ReadFile(rctx.Context, filePath, nil)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("failed to read file: %w", err)
	}

	oldContent := fc.Content

	index := strings.Index(oldContent, oldString)
	if index == -1 {
		return NewTextErrorResponse("old_string not found in file. Make sure it matches exactly, including whitespace and line breaks. You may want to re-read the file."), nil
	}

	lastIndex := strings.LastIndex(oldContent, oldString)
	if index != lastIndex {
		return NewTextErrorResponse("old_string appears multiple times in the file. Please provide more context to ensure a unique match"), nil
	}

	newContent := oldContent[:index] + oldContent[index+len(oldString):]

	if chatID == "" {
		return ToolResponse{}, fmt.Errorf("chat ID is required")
	}

	rootDir, err := GetWorkingDirectory(rctx)
	if err != nil {
		return NewTextErrorResponse("No project working directory available - ensure you're working within a project"), nil
	}

	// Generate diff for response metadata
	diff, additions, removals := diff.GenerateDiff(
		oldContent,
		newContent,
		filePath,
		rootDir,
	)

	if _, err = rctx.Daemon.WriteFile(rctx.Context, filePath, newContent); err != nil {
		return ToolResponse{}, fmt.Errorf("failed to write file: %w", err)
	}

	recordFileAwareness(chatID, thread, filePath)

	return WithResponseMetadata(
		NewTextResponse("Content deleted from file: "+filePath),
		EditResponseMetadata{
			Diff:      diff,
			Additions: additions,
			Removals:  removals,
		},
	), nil
}

func (e *editTool) replaceContent(rctx *rctx.ToolContext, thread, filePath, oldString, newString string, replaceAll bool) (ToolResponse, error) {
	chatID := rctx.ChatID

	// Validate file can be edited
	if err := e.validateFileForEdit(rctx, chatID, thread, filePath); err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	fc, err := rctx.Daemon.ReadFile(rctx.Context, filePath, nil)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("failed to read file: %w", err)
	}

	oldContent := fc.Content

	var newContent string

	if replaceAll {
		// Replace all occurrences
		if !strings.Contains(oldContent, oldString) {
			// Try to provide helpful error with line number hint
			lines := strings.Split(oldContent, "\n")
			for i, line := range lines {
				if strings.Contains(line, oldString[:min(len(oldString), 20)]) {
					return NewTextErrorResponse(fmt.Sprintf("old_string not found exactly, but similar text found near line %d. Make sure it matches exactly, including whitespace and line breaks", i+1)), nil
				}
			}
			return NewTextErrorResponse("old_string not found in file. Make sure it matches exactly, including whitespace and line breaks"), nil
		}
		newContent = strings.ReplaceAll(oldContent, oldString, newString)
	} else {
		// Single replacement
		index := strings.Index(oldContent, oldString)
		if index == -1 {
			// Try to provide helpful error with line number hint
			lines := strings.Split(oldContent, "\n")
			for i, line := range lines {
				if strings.Contains(line, oldString[:min(len(oldString), 20)]) {
					return NewTextErrorResponse(fmt.Sprintf("old_string not found exactly, but similar text found near line %d. Make sure it matches exactly, including whitespace and line breaks", i+1)), nil
				}
			}
			return NewTextErrorResponse("old_string not found in file. Make sure it matches exactly, including whitespace and line breaks"), nil
		}

		lastIndex := strings.LastIndex(oldContent, oldString)
		if index != lastIndex && !replaceAll {
			return NewTextErrorResponse("old_string appears multiple times in the file. Please provide more context to ensure a unique match, or use replace_all=true to replace all occurrences. For multiple distinct edits, consider using the Patch tool."), nil
		}

		newContent = oldContent[:index] + newString + oldContent[index+len(oldString):]
	}

	if oldContent == newContent {
		return NewTextErrorResponse("new content is the same as old content. No changes made."), nil
	}

	if chatID == "" {
		return ToolResponse{}, fmt.Errorf("chat ID is required")
	}
	rootDir, err := GetWorkingDirectory(rctx)
	if err != nil {
		return NewTextErrorResponse("No project working directory available - ensure you're working within a project"), nil
	}

	// Generate diff for response metadata
	diff, additions, removals := diff.GenerateDiff(
		oldContent,
		newContent,
		filePath,
		rootDir,
	)

	if _, err = rctx.Daemon.WriteFile(rctx.Context, filePath, newContent); err != nil {
		return ToolResponse{}, fmt.Errorf("failed to write file: %w", err)
	}

	recordFileAwareness(chatID, thread, filePath)

	return WithResponseMetadata(
		NewTextResponse("Content replaced in file: "+filePath),
		EditResponseMetadata{
			Diff:      diff,
			Additions: additions,
			Removals:  removals,
		}), nil
}
