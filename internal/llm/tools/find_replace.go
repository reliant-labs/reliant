// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type FindAndReplaceParams struct {
	FindPattern string `json:"find_pattern" jsonschema:"required,description=The pattern to search for (can be literal text or regex if use_regex is true)"`
	ReplaceText string `json:"replace_text" jsonschema:"required,description=The text to replace matches with"`
	FileGlob    string `json:"file_glob,omitempty" jsonschema:"description=Glob pattern to filter files (e.g. '**/*.js', '**/*.{ts,tsx}'). If not specified, searches all files"`
	IgnoreCase  bool   `json:"ignore_case,omitempty" jsonschema:"description=Whether to perform case-insensitive matching (default: false)"`
	UseRegex    bool   `json:"use_regex,omitempty" jsonschema:"description=Whether find_pattern is a regular expression (default: false)"`
	Preview     bool   `json:"preview,omitempty" jsonschema:"description=Preview mode: show what changes would be made without applying them. Use this first to verify the pattern and scope before committing changes."`
	Repo        string `json:"repo,omitempty" jsonschema:"description=Multi-repo only. Which repo to search in: 'root' for the project root\\, or a repo name (e.g. 'api'\\, 'web'). Used as the search base directory. Omit in single-repo projects."`
}

type FindAndReplaceResponseMetadata struct {
	FilesChanged  []string       `json:"files_changed"`
	TotalMatches  int            `json:"total_matches"`
	MatchesByFile map[string]int `json:"matches_by_file"`
}

type findAndReplaceTool struct{}

const (
	FindAndReplaceToolName    = "find_replace"
	findAndReplaceDescription = `Performs find and replace operations across multiple files matching a glob pattern.
WHEN TO USE:
- Renaming variables, functions, or classes across multiple files
- Updating imports or module references project-wide
- Fixing consistent typos or naming conventions
- Batch updating configuration values
- Refactoring patterns across the codebase
WHEN NOT TO USE:
- Single file edits: Use Edit tool instead
- Complex structural changes: Use Patch tool
- Context-dependent replacements: Use Edit or Patch for precision
FEATURES:
- Glob pattern file filtering (e.g., "**/*.js", "src/**/*.{ts,tsx}")
- Regular expression support with capture groups
- Case-insensitive matching option
- Preview mode to see changes before applying
- Automatic file history tracking
USAGE PATTERNS:
## Preview First (Recommended)
Use preview=true to see what would change before committing. Preview mode does not
require permission, making it ideal for scoping changes.
find_pattern: "oldFunction"
replace_text: "newFunction"
file_glob: "**/*.js"
preview: true
## Simple Text Replacement
find_pattern: "oldFunction"
replace_text: "newFunction"
file_glob: "**/*.js"
## Regex with Capture Groups
find_pattern: "import (.*) from 'old-module'"
replace_text: "import $1 from 'new-module'"
use_regex: true
file_glob: "**/*.ts"
## Case-Insensitive Replacement
find_pattern: "TODO"
replace_text: "FIXME"
ignore_case: true
file_glob: "**/*.{js,ts,jsx,tsx}"
# 🎯 CRITICAL REQUIREMENTS
1. PREVIEW FIRST:
   - ALWAYS call with preview=true first to verify the pattern matches and scope
   - Preview shows diffs of what would change without modifying any files
   - After reviewing the preview, call again without preview to apply
2. FILE READING:
   - Checks modification times to prevent conflicts
   - Validates file access permissions
3. PATTERN MATCHING:
   - Literal text matching by default
   - Regex patterns with use_regex=true
   - Case sensitivity controlled by ignore_case
4. SAFETY CHECKS:
   - Atomic operation (all or nothing)
   - Preserves file history
# 💡 BEST PRACTICES
- ALWAYS use preview=true first to see what changes would be made
- Use specific file globs to limit scope
- Review the preview diffs carefully before applying
- Test regex patterns with preview before committing
# 🔄 WORKS WELL WITH
- BEFORE: preview=true (see changes first)
- BEFORE: Grep (find occurrences)
- AFTER: Bash (run tests)
- ALTERNATIVE: Edit (single file)
- ALTERNATIVE: Patch (complex multi-file edits)
# 📝 PARAMETERS
- find_pattern: Text or regex pattern to find (required)
- replace_text: Replacement text (required)
- file_glob: File filter pattern (optional, defaults to all files)
- ignore_case: Case-insensitive matching (optional, default false)
- use_regex: Treat pattern as regex (optional, default false)
- preview: Preview mode - show what would change without applying (optional, default false)
Remember: Use preview=true first, then apply.`
)

func NewFindAndReplaceTool() Tool {
	tool := &findAndReplaceTool{}
	return NewToolWrapper[FindAndReplaceParams, ToolResponse](tool)
}

func (f *findAndReplaceTool) Name() string {
	return FindAndReplaceToolName
}

func (f *findAndReplaceTool) Description() string {
	return findAndReplaceDescription
}

func (f *findAndReplaceTool) RequiresPermission(params FindAndReplaceParams) (bool, error) {
	if params.Preview {
		return false, nil
	}
	return true, nil
}

func (f *findAndReplaceTool) Execute(rctx *rctx.ToolContext, params FindAndReplaceParams) (ToolResponse, error) {
	if rctx.Daemon == nil {
		return NewTextErrorResponse("filesystem access requires a connected daemon"), nil
	}

	if params.FindPattern == "" {
		return NewTextErrorResponse("find_pattern is required"), nil
	}

	wd, err := ResolveRepoPath(rctx, params.Repo)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	if wd == "" {
		return NewTextErrorResponse("No project working directory available - ensure you're working within a project"), nil
	}

	opts := &daemon.FindReplaceOpts{
		BaseDir:    wd,
		FileGlob:   params.FileGlob,
		UseRegex:   params.UseRegex,
		IgnoreCase: params.IgnoreCase,
		Preview:    params.Preview,
	}

	result, err := rctx.Daemon.FindReplace(rctx.Context, params.FindPattern, params.ReplaceText, opts)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("find and replace error: %v", err)), nil
	}

	if result.FilesChanged == 0 {
		return NewTextResponse("No matches found for the given pattern"), nil
	}

	// Build relative paths and totals
	filesChanged := make([]string, 0, len(result.Changes))
	matchesByFile := make(map[string]int, len(result.Changes))
	totalMatches := 0

	for _, change := range result.Changes {
		relPath, _ := filepath.Rel(wd, change.File)
		if relPath == "" {
			relPath = change.File
		}
		filesChanged = append(filesChanged, relPath)
		matchesByFile[relPath] = change.Replacements
		totalMatches += change.Replacements
	}

	// Record file awareness for non-preview writes
	if !params.Preview {
		chatID := rctx.ChatID
		thread := rctx.Thread
		if thread == "" {
			thread = "0"
		}
		for _, change := range result.Changes {
			recordFileAwareness(chatID, thread, change.File)
		}
	}

	// Build response message
	var responseText strings.Builder
	if params.Preview {
		fmt.Fprintf(&responseText, "Preview: %d match(es) across %d file(s) would be replaced (no changes applied)\n\n", totalMatches, len(filesChanged))
	} else {
		fmt.Fprintf(&responseText, "Successfully replaced %d occurrence(s) across %d file(s)\n\n", totalMatches, len(filesChanged))
	}

	for _, file := range filesChanged {
		fmt.Fprintf(&responseText, "• %s: %d replacement(s)\n", file, matchesByFile[file])
	}

	if params.Preview {
		totalDiffLen := 0
		const maxDiffOutput = 4000
		for _, change := range result.Changes {
			if change.Diff == "" {
				continue
			}
			relPath, _ := filepath.Rel(wd, change.File)
			if relPath == "" {
				relPath = change.File
			}
			entry := fmt.Sprintf("\n--- %s ---\n%s\n", relPath, change.Diff)
			if totalDiffLen+len(entry) > maxDiffOutput {
				fmt.Fprintf(&responseText, "\n... diff output truncated (exceeded %d chars) ...\n", maxDiffOutput)
				break
			}
			responseText.WriteString(entry)
			totalDiffLen += len(entry)
		}
	}

	finalResponse := fmt.Sprintf("<result>\n%s\n</result>\n", responseText.String())

	return WithResponseMetadata(
		NewTextResponse(finalResponse),
		FindAndReplaceResponseMetadata{
			FilesChanged:  filesChanged,
			TotalMatches:  totalMatches,
			MatchesByFile: matchesByFile,
		},
	), nil
}
