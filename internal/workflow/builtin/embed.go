// Copyright (c) 2025 Reliant Labs
package builtin

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

//go:embed *.yaml
var BuiltinWorkflowsFS embed.FS

//go:embed presets/*.yaml
var BuiltinPresetsFS embed.FS

// builtinPromptsFS embeds the prompt files a workflow names as
// builtin://<group>/<name>.md (prompts/README.md says how).
//
//go:embed prompts
var builtinPromptsFS embed.FS

// PromptFile returns the text of the builtin prompt named
// "<group>/<name>.md" (a builtin:// prompt ref without its scheme). The name
// must be a clean relative path with a group directory and a .md file name.
func PromptFile(name string) (string, error) {
	clean := path.Clean(name)
	if clean != name || path.IsAbs(name) || strings.HasPrefix(clean, "../") || clean == ".." ||
		!strings.Contains(clean, "/") || path.Ext(clean) != ".md" {
		return "", fmt.Errorf("%q is not a builtin prompt name: want <group>/<name>.md", name)
	}
	data, err := fs.ReadFile(builtinPromptsFS, "prompts/"+clean)
	if err != nil {
		return "", fmt.Errorf("no builtin prompt is named %q", name)
	}
	return string(data), nil
}

// BuiltinScenarioDirsFS embeds every builtin scenario: the per-workflow
// scenario directories (<workflow-name>/scenarios/*.yaml, one scenario per
// file), the layout a project's .reliant/workflows uses too
// (workflowref.ScenarioDir). `reliant workflow scenario run --include-builtins`
// and `go test` both read them from here (workflowref.BuiltinLayout), so the
// CLI runs exactly the scenarios the tests do.
//
//go:embed */scenarios/*.yaml
var BuiltinScenarioDirsFS embed.FS
