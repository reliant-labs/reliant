// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/diff"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type WriteParams struct {
	FilePath string `json:"file_path" jsonschema:"required,description=The path to the file to write"`
	Content  string `json:"content" jsonschema:"required,description=The content to write to the file"`
	Repo     string `json:"repo,omitempty" jsonschema:"description=Multi-repo only. Which repo the path is relative to: 'root' for the project root\\, or a repo name (e.g. 'api'\\, 'web'). Used as the base for relative paths. Omit in single-repo projects or when path is absolute."`
}

type WritePermissionsParams struct {
	FilePath string `json:"file_path"`
	Diff     string `json:"diff"`
}

type writeTool struct{}

type WriteResponseMetadata struct {
	Diff      string `json:"diff"`
	Additions int    `json:"additions"`
	Removals  int    `json:"removals"`
}

const (
	WriteToolName    = "write"
	writeDescription = `File writing tool that creates or updates files in the filesystem.

WHEN TO USE:
- Creating new files or updating existing files
- Saving generated code, configurations, or text data

HOW TO USE:
- Provide the file path and content to write
- Parent directories are created automatically

FEATURES:
- Creates new files or overwrites existing ones
- Auto-creates parent directories
- Checks for external modifications for safety
- Avoids unnecessary writes when content unchanged

LIMITATIONS:
- Cannot append (rewrites entire file)
- Existing files must be read first (View tool) since Write replaces the entire file

TIPS:
- Use the LS tool to verify the correct location when creating new files
- Combine with Glob and Grep tools to find and modify multiple files
- Always include descriptive comments when making changes to existing code`
)

func NewWriteTool() Tool {
	tool := &writeTool{}
	return NewToolWrapper[WriteParams, ToolResponse](tool)
}

func (w *writeTool) Name() string {
	return WriteToolName
}

func (w *writeTool) Description() string {
	return writeDescription
}

func (w *writeTool) RequiresPermission(params WriteParams) (bool, error) {
	if params.FilePath == "" {
		return false, fmt.Errorf("file_path is required")
	}

	return true, nil // Write operations always require permission
}

func (w *writeTool) Execute(rctx *rctx.ToolContext, params WriteParams) (ToolResponse, error) {
	if rctx.Daemon == nil {
		return NewTextErrorResponse("filesystem access requires a connected daemon"), nil
	}

	if params.FilePath == "" {
		return NewTextErrorResponse("file_path is required"), nil
	}

	if params.Content == "" {
		return NewTextErrorResponse("content is required"), nil
	}

	workingDir, err := ResolveRepoPath(rctx, params.Repo)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("couldn't determine working directory: %v", err)), nil
	}

	filePath := params.FilePath
	if !filepath.IsAbs(filePath) {
		// Relative path - join with working directory
		filePath = filepath.Join(workingDir, filePath)
	}

	chatID := rctx.ChatID
	thread := rctx.Thread
	if thread == "" {
		thread = "0"
	}

	// Serialize against the other file tools. Write replaces the file wholesale,
	// so there is no earlier content this write was derived from and nothing to
	// verify at write time — but the stat, the unchanged-content read and the
	// write below must not interleave with an edit to the same path, or the
	// mod-time guard and the unread-overwrite diff are both computed from a file
	// that changed underneath them. See file_concurrency.go.
	return withPathLock(rctx, filePath, func() (ToolResponse, error) {
		return w.writeLocked(rctx, params, filePath, workingDir, chatID, thread)
	})
}

// writeLocked performs the stat/compare/write. Callers must hold the path lock
// for filePath.
func (w *writeTool) writeLocked(rctx *rctx.ToolContext, params WriteParams, filePath, workingDir, chatID, thread string) (ToolResponse, error) {
	stat, err := rctx.Daemon.StatFile(rctx.Context, filePath)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("error checking file: %w", err)
	}

	// unreadOverwrite records that an existing file was overwritten without a
	// prior read in this thread. When true, the result surfaces a compact diff so
	// the model can see (and repair) an unintended clobber.
	unreadOverwrite := false

	if stat.Exists {
		if stat.IsDir {
			return NewTextErrorResponse(fmt.Sprintf("Path is a directory, not a file: %s", filePath)), nil
		}

		modTime := stat.ModTime
		lastAwareness := getLastAwarenessTime(chatID, thread, filePath)

		// Concurrent-modification guard (multi-agent data-loss protection): if the
		// file changed on disk AFTER we last saw it, hard-error. This stays strict.
		// Only meaningful when we hold a prior awareness timestamp — an unread file
		// (zero awareness) is handled by the soft path below.
		if !lastAwareness.IsZero() && modTime.After(lastAwareness) {
			return NewTextErrorResponse(fmt.Sprintf("file %s has been modified since it was last read (mod time: %s, last aware: %s). Multiple agents or humans may be making edits simultaneously — re-read the file before editing to avoid overwriting their changes.",
				filePath, modTime.Format(time.RFC3339), lastAwareness.Format(time.RFC3339))), nil
		}

		// Read-before-write: the file exists but was never read in this thread.
		// Rather than blocking the write, proceed with the overwrite and surface a
		// compact diff in the result (below) so the model sees what it replaced and
		// can repair a mistaken clobber with a follow-up Edit. Record awareness so
		// subsequent writes in this thread are covered by the concurrent-mod guard
		// above.
		if lastAwareness.IsZero() {
			recordFileAwareness(chatID, thread, filePath)
			unreadOverwrite = true
		}

		// Check if content is unchanged by reading current content
		fc, readErr := rctx.Daemon.ReadFile(rctx.Context, filePath, nil)
		if readErr == nil && fc.Content == params.Content {
			return NewTextErrorResponse(fmt.Sprintf("File %s already contains the exact content. No changes made.", filePath)), nil
		}
	}

	// Write the file via daemon (creates parent directories as needed)
	writeResult, err := rctx.Daemon.WriteFile(rctx.Context, filePath, params.Content)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("error writing file: %w", err)
	}

	// Generate diff for response metadata
	rootDir, err := GetWorkingDirectory(rctx)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("couldn't determine working directory: %v", err)
	}

	diffText, additions, removals := diff.GenerateDiff(
		writeResult.OldContent,
		params.Content,
		filePath,
		rootDir,
	)

	recordFileAwareness(chatID, thread, filePath)

	// Return relative path in response to avoid LLM copying absolute paths
	displayPath := filePath
	if workDir, err := GetWorkingDirectory(rctx); err == nil && workDir != "" {
		if relPath, err := filepath.Rel(workDir, filePath); err == nil {
			displayPath = relPath
		}
	}

	result := fmt.Sprintf("File successfully written: %s", displayPath)
	if unreadOverwrite {
		// The file was overwritten without a prior read. Metadata never reaches
		// the model, so include a compact diff directly in the visible content.
		result += fmt.Sprintf(
			"\n\nNOTE: %s existed but had not been read in this thread, so its entire contents were replaced. "+
				"Review the diff below — if this clobbered content you intended to keep, use the Edit tool to restore it.\n\n%s",
			displayPath, compactDiff(diffText))
	}
	result = fmt.Sprintf("<result>\n%s\n</result>", result)
	return WithResponseMetadata(NewTextResponse(result),
		WriteResponseMetadata{
			Diff:      diffText,
			Additions: additions,
			Removals:  removals,
		},
	), nil
}

// compactDiff returns a length-bounded form of a unified diff suitable for
// inclusion in the model-visible tool result. A full clobber can produce a very
// large diff; cap it so the result stays compact while still showing the model
// what was replaced.
func compactDiff(diffText string) string {
	const maxLines = 60

	trimmed := strings.TrimRight(diffText, "\n")
	if trimmed == "" {
		return "(no textual diff available)"
	}

	lines := strings.Split(trimmed, "\n")
	if len(lines) <= maxLines {
		return trimmed
	}

	omitted := len(lines) - maxLines
	return strings.Join(lines[:maxLines], "\n") +
		fmt.Sprintf("\n... (%d more diff line(s) omitted; full diff retained in tool metadata)", omitted)
}
