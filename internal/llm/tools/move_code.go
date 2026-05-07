// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/diff"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type MoveCodeParams struct {
	SourceFile  string `json:"source_file" jsonschema:"required,description=The path to the source file containing the code to move"`
	SourceStart int    `json:"source_start" jsonschema:"required,description=The 1-indexed starting line number in the source file (inclusive)"`
	SourceEnd   int    `json:"source_end" jsonschema:"required,description=The 1-indexed ending line number in the source file (inclusive)"`
	TargetFile  string `json:"target_file" jsonschema:"required,description=The path to the target file where code will be placed. Can be same as source_file."`
	TargetLine  int    `json:"target_line" jsonschema:"required,description=The 1-indexed line number in the target file AFTER which the code will be inserted. Use 0 to insert at the beginning."`
	Operation   string `json:"operation,omitempty" jsonschema:"enum=move,enum=copy,description=Whether to move (delete from source) or copy (keep in source). Default is move."`
	Repo        string `json:"repo,omitempty" jsonschema:"description=Multi-repo only. Which repo source_file and target_file are relative to: 'root' for the project root\\, or a repo name (e.g. 'api'\\, 'web'). Applies to both paths uniformly; for cross-repo moves use absolute paths instead. Omit in single-repo projects or when paths are absolute."`
}

type MoveCodeResponseMetadata struct {
	SourceDiff string `json:"source_diff,omitempty"`
	TargetDiff string `json:"target_diff,omitempty"`
	Additions  int    `json:"additions"`
	Removals   int    `json:"removals"`
	LinesMoved int    `json:"lines_moved"`
}

type moveCodeTool struct{}

const (
	MoveCodeToolName    = "move_code"
	moveCodeDescription = `Move or copy a block of code from one location to another, within the same file or across files.

WHEN TO USE:
- Reorganizing code within a file
- Moving a function/method to a different file
- Extracting code into a new location
- Copying code snippets between files
- Reordering functions in a file

WHEN NOT TO USE:
- For simple cut/paste of small text - use edit tool
- For renaming across files - use find_replace tool
- For complex multi-file refactors - consider using the refactor agent

HOW IT WORKS:
1. Extracts lines source_start to source_end from source_file
2. Inserts the extracted code AFTER target_line in target_file
3. If operation is "move" (default), deletes the original lines from source
4. If operation is "copy", keeps the original lines

EXAMPLES:

## Move function to end of file
{
  "source_file": "/path/to/file.go",
  "source_start": 50,
  "source_end": 75,
  "target_file": "/path/to/file.go",
  "target_line": 200,
  "operation": "move"
}

## Copy code block to another file
{
  "source_file": "/path/to/original.go",
  "source_start": 10,
  "source_end": 30,
  "target_file": "/path/to/new_file.go",
  "target_line": 15,
  "operation": "copy"
}

## Insert at beginning of file
{
  "source_file": "/path/to/source.go",
  "source_start": 100,
  "source_end": 120,
  "target_file": "/path/to/target.go",
  "target_line": 0,
  "operation": "move"
}

CRITICAL REQUIREMENTS:
1. Line numbers are 1-indexed
3. source_start and source_end are INCLUSIVE
4. target_line = 0 inserts at the very beginning
5. For same-file moves, the tool handles line number shifts automatically

BEST PRACTICES:
- For same-file moves, be aware that line numbers shift after the operation
- Add blank lines in the extracted code if needed for proper spacing
- Check for any imports/dependencies that might need to be added to target file`
)

func NewMoveCodeTool() Tool {
	tool := &moveCodeTool{}
	return NewToolWrapper[MoveCodeParams, ToolResponse](tool)
}

func (m *moveCodeTool) Name() string {
	return MoveCodeToolName
}

func (m *moveCodeTool) Description() string {
	return moveCodeDescription
}

func (m *moveCodeTool) RequiresPermission(params MoveCodeParams) (bool, error) {
	return true, nil
}

func (m *moveCodeTool) Execute(rctx *rctx.ToolContext, params MoveCodeParams) (ToolResponse, error) {
	if rctx.Daemon == nil {
		return NewTextErrorResponse("filesystem access requires a connected daemon"), nil
	}

	// Validate required parameters
	if params.SourceFile == "" {
		return NewTextErrorResponse("source_file is required"), nil
	}
	if params.TargetFile == "" {
		return NewTextErrorResponse("target_file is required"), nil
	}
	if params.SourceStart < 1 {
		return NewTextErrorResponse("source_start must be >= 1 (line numbers are 1-indexed)"), nil
	}
	if params.SourceEnd < params.SourceStart {
		return NewTextErrorResponse(fmt.Sprintf("source_end (%d) must be >= source_start (%d)", params.SourceEnd, params.SourceStart)), nil
	}
	if params.TargetLine < 0 {
		return NewTextErrorResponse("target_line must be >= 0 (0 = insert at beginning)"), nil
	}

	// Default operation to move
	if params.Operation == "" {
		params.Operation = "move"
	}
	if params.Operation != "move" && params.Operation != "copy" {
		return NewTextErrorResponse(fmt.Sprintf("invalid operation '%s'. Must be 'move' or 'copy'", params.Operation)), nil
	}

	wd, err := ResolveRepoPath(rctx, params.Repo)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	if wd == "" {
		return NewTextErrorResponse("No project working directory available"), nil
	}

	// Resolve file paths
	sourceFile := params.SourceFile
	if !filepath.IsAbs(sourceFile) {
		sourceFile = filepath.Join(wd, sourceFile)
	}
	targetFile := params.TargetFile
	if !filepath.IsAbs(targetFile) {
		targetFile = filepath.Join(wd, targetFile)
	}

	isSameFile := sourceFile == targetFile

	chatID := rctx.ChatID
	thread := rctx.Thread
	if thread == "" {
		thread = "0"
	}

	// Validate source file
	sourceStat, err := rctx.Daemon.StatFile(rctx.Context, sourceFile)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("failed to access source file: %w", err)
	}
	if !sourceStat.Exists {
		return NewTextErrorResponse(fmt.Sprintf("source file not found: %s", sourceFile)), nil
	}
	if sourceStat.IsDir {
		return NewTextErrorResponse(fmt.Sprintf("source path is a directory: %s", sourceFile)), nil
	}

	// Check if source file was modified after last read
	sourceAwareness := getLastAwarenessTime(chatID, thread, sourceFile)
	if !sourceAwareness.IsZero() && sourceStat.ModTime.After(sourceAwareness) {
		return NewTextErrorResponse(fmt.Sprintf("source file %s has been modified since last read. Please re-read it.", sourceFile)), nil
	}

	// Read source file
	sourceFc, err := rctx.Daemon.ReadFile(rctx.Context, sourceFile, nil)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("failed to read source file: %w", err)
	}
	sourceContent := sourceFc.Content
	sourceLines := strings.Split(sourceContent, "\n")

	// Validate source line numbers
	if params.SourceStart > len(sourceLines) {
		return NewTextErrorResponse(fmt.Sprintf("source_start %d exceeds source file length (%d lines)", params.SourceStart, len(sourceLines))), nil
	}
	if params.SourceEnd > len(sourceLines) {
		return NewTextErrorResponse(fmt.Sprintf("source_end %d exceeds source file length (%d lines)", params.SourceEnd, len(sourceLines))), nil
	}

	// Extract the code block (0-indexed)
	startIdx := params.SourceStart - 1
	endIdx := params.SourceEnd - 1
	codeBlock := sourceLines[startIdx : endIdx+1]
	linesMoved := len(codeBlock)

	var targetLines []string
	var targetContent string

	if isSameFile {
		targetLines = sourceLines
	} else {
		// Validate target file
		targetStat, err := rctx.Daemon.StatFile(rctx.Context, targetFile)
		if err != nil {
			return ToolResponse{}, fmt.Errorf("failed to access target file: %w", err)
		}
		if !targetStat.Exists {
			return NewTextErrorResponse(fmt.Sprintf("target file not found: %s", targetFile)), nil
		}
		if targetStat.IsDir {
			return NewTextErrorResponse(fmt.Sprintf("target path is a directory: %s", targetFile)), nil
		}

		// Check if target file was modified after last read
		targetAwareness := getLastAwarenessTime(chatID, thread, targetFile)
		if !targetAwareness.IsZero() && targetStat.ModTime.After(targetAwareness) {
			return NewTextErrorResponse(fmt.Sprintf("target file %s has been modified since last read. Please re-read it.", targetFile)), nil
		}

		// Read target file
		targetFc, err := rctx.Daemon.ReadFile(rctx.Context, targetFile, nil)
		if err != nil {
			return ToolResponse{}, fmt.Errorf("failed to read target file: %w", err)
		}
		targetContent = targetFc.Content
		targetLines = strings.Split(targetContent, "\n")
	}

	// Validate target line
	if params.TargetLine > len(targetLines) {
		return NewTextErrorResponse(fmt.Sprintf("target_line %d exceeds target file length (%d lines)", params.TargetLine, len(targetLines))), nil
	}

	var newSourceContent, newTargetContent string
	var sourceDiff, targetDiff string
	var totalAdditions, totalRemovals int

	if isSameFile {
		// Same file operation - need to handle carefully
		newLines := make([]string, 0, len(sourceLines))

		// Determine insert position accounting for deletion
		targetIdx := params.TargetLine // 0 means beginning, otherwise insert after this line (1-indexed)

		if params.Operation == "move" {
			// For move within same file, we need to handle the case where
			// the insert position is affected by the deletion

			if targetIdx == 0 {
				// Insert at beginning, then delete from source (shifted)
				newLines = append(newLines, codeBlock...)
				for i, line := range sourceLines {
					// Skip the original source lines (now shifted by linesMoved)
					if i >= startIdx && i <= endIdx {
						continue
					}
					newLines = append(newLines, line)
				}
			} else if targetIdx <= startIdx {
				// Insert before the source block
				for i := 0; i < targetIdx; i++ {
					newLines = append(newLines, sourceLines[i])
				}
				newLines = append(newLines, codeBlock...)
				for i := targetIdx; i < len(sourceLines); i++ {
					if i >= startIdx && i <= endIdx {
						continue
					}
					newLines = append(newLines, sourceLines[i])
				}
			} else if targetIdx > endIdx {
				// Insert after the source block
				for i := 0; i < len(sourceLines); i++ {
					if i >= startIdx && i <= endIdx {
						continue
					}
					newLines = append(newLines, sourceLines[i])
				}
				// Simpler approach: build without source, then insert at adjusted position
				newLines = nil
				adjustedTarget := targetIdx - linesMoved
				for i := 0; i < len(sourceLines); i++ {
					if i >= startIdx && i <= endIdx {
						continue
					}
					newLines = append(newLines, sourceLines[i])
				}
				// Now insert at adjusted position
				finalLines := make([]string, 0, len(newLines)+linesMoved)
				for i := 0; i < adjustedTarget; i++ {
					finalLines = append(finalLines, newLines[i])
				}
				finalLines = append(finalLines, codeBlock...)
				finalLines = append(finalLines, newLines[adjustedTarget:]...)
				newLines = finalLines
			} else {
				// Target is within the source block - error
				return NewTextErrorResponse("target_line cannot be within the source range for same-file move"), nil
			}
		} else {
			// Copy operation - just insert, don't delete
			finalLines := make([]string, 0, len(sourceLines)+linesMoved)
			for i := 0; i < targetIdx; i++ {
				finalLines = append(finalLines, sourceLines[i])
			}
			finalLines = append(finalLines, codeBlock...)
			finalLines = append(finalLines, sourceLines[targetIdx:]...)
			newLines = finalLines
		}

		newSourceContent = strings.Join(newLines, "\n")

		if sourceContent == newSourceContent {
			return NewTextErrorResponse("no changes would be made"), nil
		}

		sourceDiff, totalAdditions, totalRemovals = diff.GenerateDiff(sourceContent, newSourceContent, sourceFile, wd)

		// Write the file
		if _, err = rctx.Daemon.WriteFile(rctx.Context, sourceFile, newSourceContent); err != nil {
			return ToolResponse{}, fmt.Errorf("failed to write file: %w", err)
		}

		recordFileAwareness(chatID, thread, sourceFile)

	} else {
		// Different files

		// Insert into target
		targetIdx := params.TargetLine // 0 means beginning
		newTargetLines := make([]string, 0, len(targetLines)+linesMoved)
		for i := 0; i < targetIdx; i++ {
			newTargetLines = append(newTargetLines, targetLines[i])
		}
		newTargetLines = append(newTargetLines, codeBlock...)
		if targetIdx < len(targetLines) {
			newTargetLines = append(newTargetLines, targetLines[targetIdx:]...)
		}
		newTargetContent = strings.Join(newTargetLines, "\n")

		var additionsTarget, removalsTarget int
		targetDiff, additionsTarget, removalsTarget = diff.GenerateDiff(targetContent, newTargetContent, targetFile, wd)
		totalAdditions += additionsTarget
		totalRemovals += removalsTarget

		// Write target file first
		if _, err = rctx.Daemon.WriteFile(rctx.Context, targetFile, newTargetContent); err != nil {
			return ToolResponse{}, fmt.Errorf("failed to write target file: %w", err)
		}
		recordFileAwareness(chatID, thread, targetFile)

		// If move operation, delete from source
		if params.Operation == "move" {
			newSourceLines := make([]string, 0, len(sourceLines)-linesMoved)
			for i := 0; i < len(sourceLines); i++ {
				if i >= startIdx && i <= endIdx {
					continue
				}
				newSourceLines = append(newSourceLines, sourceLines[i])
			}
			newSourceContent = strings.Join(newSourceLines, "\n")

			var additionsSource, removalsSource int
			sourceDiff, additionsSource, removalsSource = diff.GenerateDiff(sourceContent, newSourceContent, sourceFile, wd)
			totalAdditions += additionsSource
			totalRemovals += removalsSource

			if _, err = rctx.Daemon.WriteFile(rctx.Context, sourceFile, newSourceContent); err != nil {
				return ToolResponse{}, fmt.Errorf("failed to write source file: %w", err)
			}
			recordFileAwareness(chatID, thread, sourceFile)
		}
	}

	// Build response
	displaySource := sourceFile
	displayTarget := targetFile
	if relPath, err := filepath.Rel(wd, sourceFile); err == nil {
		displaySource = relPath
	}
	if relPath, err := filepath.Rel(wd, targetFile); err == nil {
		displayTarget = relPath
	}

	var result string
	if params.Operation == "move" {
		if isSameFile {
			result = fmt.Sprintf("<result>\nMoved %d lines (lines %d-%d) to after line %d in %s (+%d/-%d)\n</result>\n",
				linesMoved, params.SourceStart, params.SourceEnd, params.TargetLine, displaySource, totalAdditions, totalRemovals)
		} else {
			result = fmt.Sprintf("<result>\nMoved %d lines from %s (lines %d-%d) to %s (after line %d) (+%d/-%d)\n</result>\n",
				linesMoved, displaySource, params.SourceStart, params.SourceEnd, displayTarget, params.TargetLine, totalAdditions, totalRemovals)
		}
	} else {
		if isSameFile {
			result = fmt.Sprintf("<result>\nCopied %d lines (lines %d-%d) to after line %d in %s (+%d/-%d)\n</result>\n",
				linesMoved, params.SourceStart, params.SourceEnd, params.TargetLine, displaySource, totalAdditions, totalRemovals)
		} else {
			result = fmt.Sprintf("<result>\nCopied %d lines from %s (lines %d-%d) to %s (after line %d) (+%d/-%d)\n</result>\n",
				linesMoved, displaySource, params.SourceStart, params.SourceEnd, displayTarget, params.TargetLine, totalAdditions, totalRemovals)
		}
	}

	return WithResponseMetadata(
		NewTextResponse(result),
		MoveCodeResponseMetadata{
			SourceDiff: sourceDiff,
			TargetDiff: targetDiff,
			Additions:  totalAdditions,
			Removals:   totalRemovals,
			LinesMoved: linesMoved,
		},
	), nil
}
