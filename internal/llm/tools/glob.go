// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/rctx"
)

const (
	GlobToolName       = "glob"
	defaultResultLimit = 200 // Shared default limit for glob and grep results
	globDescription    = `- Fast file pattern matching tool that works with any codebase size
- Supports glob patterns like "**/*.js" or "src/**/*.ts"
- Returns matching file paths sorted by modification time
- Use this tool when you need to find files by name patterns
- When you are doing an open ended search that may require multiple rounds of globbing and grepping, use the Agent tool instead
- You have the capability to call multiple tools in a single response. It is always better to speculatively perform multiple searches as a batch that are potentially useful.

IGNORED DIRECTORIES:
By default, the following noisy directories are excluded from results:
- Dependencies: node_modules, vendor, bower_components, jspm_packages
- Build outputs: dist, build, target, bin, obj, out, generated
- Cache/temp: __pycache__, coverage, tmp, temp, logs
- Internal: .git, .reliant

Hidden files/directories (starting with .) like .github, .vscode are INCLUDED by default.
Gitignored files are INCLUDED by default (we don't respect .gitignore).
Use include_ignored=true to also search the noisy directories listed above.`
)

type GlobParams struct {
	Pattern        string `json:"pattern" jsonschema:"required,description=The glob pattern to match files against"`
	Path           string `json:"path,omitempty" jsonschema:"description=The directory to search in. If not specified the current working directory will be used. IMPORTANT: Omit this field to use the default directory. DO NOT enter \"undefined\" or \"null\" - simply omit it for the default behavior. Must be a valid directory path if provided."`
	HeadLimit      int    `json:"head_limit,omitempty" jsonschema:"description=Limit output to first N files. When unspecified defaults to 200 results."`
	IncludeIgnored bool   `json:"include_ignored,omitempty" jsonschema:"description=Include commonly ignored directories in results. Excluded by default: node_modules vendor dist build target .git .reliant __pycache__ coverage tmp temp logs bin obj out generated bower_components jspm_packages. Default: false."`
	Repo           string `json:"repo,omitempty" jsonschema:"description=Multi-repo only. Which repo to glob in: 'root' for the project root\\, or a repo name (e.g. 'api'\\, 'web'). Used as the base when 'path' is empty or relative. Omit in single-repo projects."`
}

type GlobResponseMetadata struct {
	NumberOfFiles int  `json:"number_of_files"`
	Truncated     bool `json:"truncated"`
}

type globTool struct{}

func NewGlobTool() Tool {
	tool := &globTool{}
	return NewToolWrapper[GlobParams, ToolResponse](tool)
}

func (g *globTool) Name() string {
	return GlobToolName
}

func (g *globTool) Description() string {
	return globDescription
}

func (g *globTool) RequiresPermission(params GlobParams) (bool, error) {
	return false, nil
}

func (g *globTool) Execute(rctx *rctx.ToolContext, params GlobParams) (ToolResponse, error) {
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

	limit := params.HeadLimit
	if limit <= 0 {
		limit = defaultResultLimit
	}

	opts := &daemon.GlobOpts{
		BaseDir:        searchPath,
		IncludeIgnored: params.IncludeIgnored,
		MaxResults:     limit,
	}

	result, err := rctx.Daemon.GlobFiles(rctx.Context, params.Pattern, opts)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("error finding files: %w", err)
	}

	// Convert absolute paths to relative paths for display
	displayFiles := make([]string, len(result.Files))
	for i, file := range result.Files {
		if relPath, err := filepath.Rel(searchPath, file); err == nil {
			displayFiles[i] = relPath
		} else {
			displayFiles[i] = file
		}
	}

	var output string
	if len(displayFiles) == 0 {
		output = "No files found"
	} else {
		output = strings.Join(displayFiles, "\n")
		if result.Truncated {
			output += "\n\n(Results are truncated. Consider using a more specific path or pattern.)"
		}
	}

	return WithResponseMetadata(
		NewTextResponse(output),
		GlobResponseMetadata{
			NumberOfFiles: len(displayFiles),
			Truncated:     result.Truncated,
		},
	), nil
}
