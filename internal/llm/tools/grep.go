// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/fileutil"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type GrepParams struct {
	Pattern        string `json:"pattern" jsonschema:"required,description=The regular expression pattern to search for in file contents"`
	Path           string `json:"path,omitempty" jsonschema:"description=File or directory to search in (rg PATH). Defaults to current working directory."`
	Glob           string `json:"glob,omitempty" jsonschema:"description=Glob pattern to filter files (e.g. \"*.js\" \"*.{ts tsx}\") - maps to rg --glob"`
	OutputMode     string `json:"output_mode,omitempty" jsonschema:"enum=content,enum=files_with_matches,enum=count,description=Output mode: \"content\" shows matching lines with line numbers (supports -A/-B/-C context head_limit) \"files_with_matches\" shows file paths (supports head_limit) \"count\" shows match counts (supports head_limit). Defaults to \"files_with_matches\"."`
	Before         int    `json:"-B,omitempty" jsonschema:"description=Number of lines to show before each match (rg -B). Requires output_mode: \"content\" ignored otherwise."`
	After          int    `json:"-A,omitempty" jsonschema:"description=Number of lines to show after each match (rg -A). Requires output_mode: \"content\" ignored otherwise."`
	Context        int    `json:"-C,omitempty" jsonschema:"description=Number of lines to show before and after each match (rg -C). Requires output_mode: \"content\" ignored otherwise."`
	IgnoreCase     bool   `json:"-i,omitempty" jsonschema:"description=Case insensitive search (rg -i)"`
	Type           string `json:"type,omitempty" jsonschema:"description=File type to search (rg --type). Common types: js (includes .jsx) ts (includes .tsx) py rust go java html css etc. Use 'rg --type-list' to see all. More efficient than glob for standard file types."`
	HeadLimit      int    `json:"head_limit,omitempty" jsonschema:"description=Limit output to first N lines/entries equivalent to \"| head -N\". Works across all output modes: content (limits output lines) files_with_matches (limits file paths) count (limits count entries). When unspecified shows all results from ripgrep."`
	Multiline      bool   `json:"multiline,omitempty" jsonschema:"description=Enable multiline mode where . matches newlines and patterns can span lines (rg -U --multiline-dotall). Default: false."`
	WordBoundary   bool   `json:"word_boundary,omitempty" jsonschema:"description=Only match whole words (rg -w). Useful for matching variable names without partial matches."`
	FixedStrings   bool   `json:"fixed_strings,omitempty" jsonschema:"description=Treat pattern as literal string not regex (rg -F). Useful when searching for special characters like braces or brackets without escaping."`
	IncludeIgnored bool   `json:"include_ignored,omitempty" jsonschema:"description=Include commonly ignored directories in results. Excluded by default: node_modules vendor dist build target .git .reliant __pycache__ coverage tmp temp logs bin obj out generated bower_components jspm_packages. Default: false."`
	Repo           string `json:"repo,omitempty" jsonschema:"description=Multi-repo only. Which repo to search in: 'root' for the project root\\, or a repo name (e.g. 'api'\\, 'web'). Used as the search base when 'path' is empty. Omit in single-repo projects."`
}

type GrepResponseMetadata struct {
	NumberOfMatches int  `json:"number_of_matches"`
	Truncated       bool `json:"truncated"`
	TotalMatches    int  `json:"total_matches,omitempty"` // Total before truncation/offset
}

type grepTool struct{}

const (
	GrepToolName    = "grep"
	grepDescription = `A powerful search tool built on ripgrep.

ALWAYS use Grep for search tasks. NEVER use grep or rg via Bash.

OUTPUT MODES:
- "files_with_matches" (default): Returns file paths sorted by modification time
- "content": Shows matching lines with line numbers
- "count": Shows match counts per file

KEY PARAMETERS:
- pattern: Regex pattern (use fixed_strings=true for literal matching)
- glob: Filter files (e.g., "*.js", "**/*.tsx")
- type: File type filter (e.g., "js", "ts", "py", "go")
- word_boundary: Match whole words only (e.g., "foo" won't match "foobar")
- fixed_strings: Treat pattern as literal text, no regex escaping needed
- head_limit: Limit number of results
- -C/-A/-B: Context lines (content mode only)
- include_ignored: Search commonly ignored directories

IGNORED DIRECTORIES:
By default, the following noisy directories are excluded from results:
- Dependencies: node_modules, vendor, bower_components, jspm_packages
- Build outputs: dist, build, target, bin, obj, out, generated
- Cache/temp: __pycache__, coverage, tmp, temp, logs
- Internal: .git, .reliant

Hidden files/directories (starting with .) like .github, .vscode are INCLUDED by default.
Use include_ignored=true to also search the noisy directories listed above.

REGEX SYNTAX:
Patterns use ripgrep regex syntax (Rust regex / ERE-style). This is NOT the same as GNU grep (BRE).
- Alternation: | (NOT \|)
- Grouping: () (NOT \( \))
- Quantifiers: +, ?, {n,m} work without escaping
- Character classes: [a-z], \d, \w, \s work as expected

COMMON MISTAKES:
- WRONG: pattern="foo\|bar"    — \| matches a literal backslash followed by pipe, NOT alternation
  RIGHT: pattern="foo|bar"      — | is alternation in ripgrep
- WRONG: pattern="\(group\)"   — \( matches a literal parenthesis
  RIGHT: pattern="(group)"      — () is grouping in ripgrep
- WRONG: pattern="foo\+"       — \+ matches a literal plus
  RIGHT: pattern="foo+"         — + means one-or-more in ripgrep
- To match literal special characters, use fixed_strings=true instead of backslash escaping

EXAMPLES:
- Find function definitions: pattern="func\s+\w+", type="go"
- Find exact text with special chars: pattern="interface{}", fixed_strings=true
- Find whole word: pattern="Error", word_boundary=true
- Multiline patterns: pattern="struct {[\s\S]*?}", multiline=true
- Search in node_modules: pattern="lodash", include_ignored=true
- Alternation (match any of several): pattern="foo|bar|baz"
- Grouped alternation: pattern="(get|set)Value"
`
)

func NewGrepTool() Tool {
	tool := &grepTool{}
	return NewToolWrapper[GrepParams, ToolResponse](tool)
}

func (g *grepTool) Name() string {
	return GrepToolName
}

func (g *grepTool) Description() string {
	return grepDescription
}

func (g *grepTool) RequiresPermission(params GrepParams) (bool, error) {
	return false, nil
}

func (g *grepTool) Execute(rctx *rctx.ToolContext, params GrepParams) (ToolResponse, error) {
	if rctx.Daemon == nil {
		return NewTextErrorResponse("filesystem access requires a connected daemon"), nil
	}

	if params.Pattern == "" {
		return NewTextErrorResponse("pattern is required"), nil
	}

	wd, err := ResolveRepoPath(rctx, params.Repo)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	if wd == "" {
		return NewTextErrorResponse("couldn't determine working directory"), nil
	}

	searchPath := params.Path
	if searchPath == "" {
		searchPath = wd
	} else if !filepath.IsAbs(searchPath) {
		searchPath = filepath.Join(wd, searchPath)
	}

	// Normalize glob pattern if provided
	glob := params.Glob
	if glob != "" {
		normResult := fileutil.NormalizeGlobPattern(glob, searchPath)
		if normResult.ErrorMessage != "" {
			return NewTextErrorResponse(normResult.ErrorMessage), nil
		}
		glob = normResult.Pattern
		if normResult.PathAdjustment != "" {
			searchPath = filepath.Join(searchPath, normResult.PathAdjustment)
		}
	}

	// Set default output mode
	outputMode := params.OutputMode
	if outputMode == "" {
		outputMode = "files_with_matches"
	}

	// Handle context lines
	before := params.Before
	after := params.After
	if params.Context > 0 {
		before = params.Context
		after = params.Context
	}

	// Set default head limit
	limit := params.HeadLimit
	if limit <= 0 {
		limit = defaultResultLimit
	}

	// Build SearchOpts
	opts := &daemon.SearchOpts{
		BaseDir:         searchPath,
		FileGlob:        glob,
		FileType:        params.Type,
		ContextBefore:   before,
		ContextAfter:    after,
		CaseInsensitive: params.IgnoreCase,
		FixedStrings:    params.FixedStrings,
		WordBoundary:    params.WordBoundary,
		Multiline:       params.Multiline,
		IncludeIgnored:  params.IncludeIgnored,
		MaxResults:      limit,
		OutputMode:      outputMode,
	}

	result, err := rctx.Daemon.SearchFiles(rctx.Context, params.Pattern, opts)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("error searching files: %w", err)
	}

	matches := result.Matches

	// Convert absolute paths to relative paths for display
	for i := range matches {
		cleanPath := strings.TrimPrefix(matches[i].File, "./")
		if filepath.IsAbs(cleanPath) {
			if relPath, err := filepath.Rel(wd, cleanPath); err == nil {
				matches[i].File = relPath
			} else {
				matches[i].File = cleanPath
			}
		} else {
			matches[i].File = cleanPath
		}
	}

	var output string
	if len(matches) == 0 {
		output = "No matches found"
	} else {
		switch outputMode {
		case "files_with_matches":
			output = fmt.Sprintf("Found %d files\n", len(matches))
			for _, m := range matches {
				output += m.File + "\n"
			}

		case "count":
			output = fmt.Sprintf("Found matches in %d files\n", len(matches))
			for _, m := range matches {
				output += fmt.Sprintf("%s: %d matches\n", m.File, m.MatchCount)
			}

		case "content":
			output = fmt.Sprintf("Found %d matches\n", len(matches))
			currentFile := ""
			for _, m := range matches {
				if currentFile != m.File {
					if currentFile != "" {
						output += "\n"
					}
					currentFile = m.File
					output += fmt.Sprintf("%s:\n", m.File)
				}
				content := m.Content
				if len(content) > MaxLineLength {
					content = content[:MaxLineLength] + fmt.Sprintf("... (%d chars total)", len(m.Content))
				}
				if m.Line > 0 {
					output += fmt.Sprintf("  Line %d: %s\n", m.Line, content)
				} else {
					output += fmt.Sprintf("  %s\n", content)
				}
			}
		}

		if result.Truncated {
			output += "\n(Results are truncated. Consider using a more specific path or pattern.)"
		}

		// Record file awareness for content mode
		if outputMode == "content" {
			chatID := rctx.ChatID
			thread := rctx.Thread
			if thread == "" {
				thread = "0"
			}
			seen := make(map[string]bool)
			for _, m := range matches {
				if seen[m.File] {
					continue
				}
				seen[m.File] = true
				absPath := m.File
				if !filepath.IsAbs(absPath) {
					absPath = filepath.Join(wd, absPath)
				}
				recordFileAwareness(chatID, thread, absPath)
			}
		}
	}

	return WithResponseMetadata(
		NewTextResponse(output),
		GrepResponseMetadata{
			NumberOfMatches: len(matches),
			Truncated:       result.Truncated,
		},
	), nil
}
