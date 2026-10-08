// Copyright (c) 2025 Reliant Labs
package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// localIOPattern matches direct local filesystem / process access, plus the
// config helpers that read and write the project's metadata file on disk.
var localIOPattern = regexp.MustCompile(
	`\bos\.(Open|OpenFile|ReadFile|WriteFile|Stat|Lstat|ReadDir|MkdirAll|Mkdir|Create|Remove|RemoveAll|Rename|Getwd|Chdir|MkdirTemp|CreateTemp|UserHomeDir|Symlink)\(` +
		`|filepath\.(Walk|WalkDir|Glob|EvalSymlinks)\(` +
		`|\bexec\.Command` +
		`|config\.(Save|Load)ProjectMeta`)

// filesTouchingLocalIO maps each non-test file in this package that does local
// I/O to the tools it implements. Each such tool must be PlacementDaemon: the
// worker is multi-tenant and has no user checkout, so running this code there
// reads or writes the worker's own filesystem with user-influenced paths.
// A new file that matches localIOPattern fails the test until it is listed here.
var filesTouchingLocalIO = map[string][]string{
	"project_analyzer.go":     {ToolProjectAnalyzer},
	"metadata_writer.go":      {ToolMetadataWriter},
	"code_context.go":         {ToolCodeContext},
	"code_context_engines.go": {ToolCodeContext},
	"code_context_source.go":  {ToolCodeContext},
}

// localIOAllowlist names files that match the pattern but legitimately run in
// the worker. Every entry needs a reason. (Empty on purpose.)
var localIOAllowlist = map[string]string{}

func TestLocalIOToolsAreDaemonPlaced(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)

	placement := map[string]Placement{}
	for _, def := range GetToolRegistry() {
		placement[def.Name] = def.Placement
	}
	// project_analyzer is feature-flagged out of the default registry.
	placement[ToolProjectAnalyzer] = projectAnalyzerDefinition().Placement

	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		if !localIOPattern.Match(b) {
			continue
		}
		if reason, ok := localIOAllowlist[f]; ok {
			assert.NotEmpty(t, reason, "%s allowlisted without a reason", f)
			continue
		}
		toolNames, ok := filesTouchingLocalIO[f]
		if !assert.True(t, ok, "%s does local filesystem/exec work but is not classified in filesTouchingLocalIO", f) {
			continue
		}
		for _, name := range toolNames {
			p, known := placement[name]
			if assert.True(t, known, "%s: tool %q not in registry", f, name) {
				assert.Equal(t, PlacementDaemon, p, "%s does local I/O, so tool %q must be PlacementDaemon, not %q", f, name, p)
			}
		}
	}
}
