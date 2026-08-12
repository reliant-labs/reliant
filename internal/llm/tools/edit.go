// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/diff"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type EditParams struct {
	FilePath   string `json:"file_path" jsonschema:"required,description=The absolute path to the file to modify"`
	OldString  string `json:"old_string,omitempty" jsonschema:"description=The text to replace (leave empty when creating a new file)"`
	NewString  string `json:"new_string,omitempty" jsonschema:"description=The edited text to replace the old_string (leave empty when deleting content)"`
	ReplaceAll bool   `json:"replace_all,omitempty" jsonschema:"description=Replace all occurrences of old_string in the file (default: false)"`
	Repo       string `json:"repo,omitempty" jsonschema:"description=Multi-repo only. Which repo the path is relative to: 'root' for the project root\\, or a repo name (e.g. 'api'\\, 'web'). Used as the base for relative paths. Omit in single-repo projects or when path is absolute."`
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
	editDescription = `Make a precise text replacement in a single file, or create/delete file content. One edit per call.

NOTE: To make several edits at once, issue multiple edit tool calls IN THE SAME MESSAGE — they run in parallel. Do NOT try to pack multiple edits into one call.

Edits to DIFFERENT files in one message run at the same time. Edits to the SAME file are applied one at a time, in an unspecified order, each one seeing the result of the ones before it. So batch same-file edits freely — but only when they are independent: each old_string must still match after the others have applied. Two edits whose old_string regions overlap, or where one creates the text the other matches, must be sent in separate messages, or the second will fail to match.

WHEN TO USE:
- Precise text replacements
- Creating a new file (empty old_string)
- Deleting specific content (empty new_string)
- Renaming a variable/function (with replace_all)

ALWAYS PREFER THIS TOOL OVER write. Turn latency is set by how many tokens you
GENERATE, so rewriting an existing file costs minutes of generation for a change
edit makes in seconds. write is for creating NEW files; for anything that already
exists, use edit. If an old_string fails to match, re-read the exact region and
retry the edit — falling back to a full rewrite is the most expensive move available.

WHEN NOT TO USE:
- Moving/renaming files: Use Bash mv command

COMMON MISTAKES TO AVOID:
- Insufficient context in old_string (needs 3-5 lines)
- Forgetting whitespace/indentation must match exactly
- Not checking if text appears multiple times

USAGE PATTERNS:

## Replace Text
file_path: "/path/to/file.go"
old_string: "Include 3-5 lines before AND after"
new_string: "Your replacement text"
replace_all: false

## Multiple Edits
Issue one edit call per change, all in the same message. For example, to edit two
files at once, send two edit tool calls in parallel:
  edit(file_path="/path/to/file1.go", old_string="old text 1", new_string="new text 1")
  edit(file_path="/path/to/file2.go", old_string="old text 2", new_string="new text 2")

## Create New File
file_path: "/path/to/new/file.go"
old_string: ""
new_string: "file contents"

## Delete Content
file_path: "/path/to/file.go"
old_string: "text to remove"
new_string: ""

## Rename Variable
file_path: "/path/to/file.go"
old_string: "oldName"
new_string: "newName"
replace_all: true

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
- Use replace_all for systematic renames
- Parallelize independent edits as separate calls in one message
- Verify edits don't break code

# 🔄 WORKS WELL WITH
- AFTER: Bash (test changes)
- ALTERNATIVE: Write (complete rewrite)

# 📝 PARAMETERS
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
	logging.Debug("Edit tool Execute called", "file_path", params.FilePath, "chatID", rctx.ChatID, "thread", rctx.Thread)

	if rctx.Daemon == nil {
		return NewTextErrorResponse("filesystem access requires a connected daemon"), nil
	}

	if params.FilePath == "" {
		logging.Warn("Edit tool called with empty file_path", "chatID", rctx.ChatID, "thread", rctx.Thread)
		return NewTextErrorResponse("file_path is required"), nil
	}

	wd, err := ResolveRepoPath(rctx, params.Repo)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	if wd == "" {
		return NewTextErrorResponse("No project working directory available - ensure you're working within a project"), nil
	}

	// Normalize thread once for consistency across all operations
	thread := rctx.Thread
	if thread == "" {
		thread = "0"
	}

	// Convert to absolute path
	filePath := params.FilePath
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(wd, filePath)
	}

	// Everything from here down is a read-modify-write on filePath, and this tool
	// is called concurrently with itself: the tool calls in one assistant message
	// are dispatched onto a worker pool, and the description above tells the model
	// to batch its edits that way. Hold the path lock across the whole sequence so
	// same-file edits in a batch see each other's writes instead of racing.
	// See file_concurrency.go.
	var response ToolResponse
	response, err = withPathLock(rctx, filePath, func() (ToolResponse, error) {
		// Validate file access for existing files
		if params.OldString != "" {
			if err := e.validateFileForEdit(rctx, rctx.ChatID, thread, filePath); err != nil {
				return NewTextErrorResponse(err.Error()), nil
			}
		}

		// Apply the edit
		switch {
		case params.OldString == "":
			// Creating a new file (empty old_string)
			return e.createNewFile(rctx, thread, filePath, params.NewString)
		case params.NewString == "":
			// Deleting content (empty new_string)
			return e.deleteContent(rctx, thread, filePath, params.OldString)
		default:
			// Normal content replacement
			return e.replaceContent(rctx, thread, filePath, params.OldString, params.NewString, params.ReplaceAll)
		}
	})

	if err != nil {
		return response, err
	}
	if response.IsError {
		return response, nil
	}

	finalText := fmt.Sprintf("<result>\n%s\n</result>\n", response.Content)

	// Propagate the underlying operation's metadata (diff, additions, removals).
	var metadata EditResponseMetadata
	if response.Metadata != "" {
		_ = json.Unmarshal([]byte(response.Metadata), &metadata)
	}

	return WithResponseMetadata(NewTextResponse(finalText), metadata), nil
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

	// Expect the file to still be absent: we checked stat.Exists above, and
	// creating over content that appeared in between would destroy it.
	if err = writeFileGuarded(rctx, filePath, "", content); err != nil {
		var conflict *ConcurrentModificationError
		if errors.As(err, &conflict) {
			return NewTextErrorResponse(conflict.Error()), nil
		}
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

	if err = writeFileGuarded(rctx, filePath, oldContent, newContent); err != nil {
		var conflict *ConcurrentModificationError
		if errors.As(err, &conflict) {
			return NewTextErrorResponse(conflict.Error()), nil
		}
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

	if err = writeFileGuarded(rctx, filePath, oldContent, newContent); err != nil {
		var conflict *ConcurrentModificationError
		if errors.As(err, &conflict) {
			return NewTextErrorResponse(conflict.Error()), nil
		}
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
