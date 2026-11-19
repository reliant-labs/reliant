package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/templates"
)

// RenderProjectMemory reads forge.yaml at projectRoot, renders the
// project memory template for the project as it is — its name, the invoking
// CLI name, and which of forge's pieces and which of its own commands it
// uses (projectMemoryData) — and returns the bytes. Used by out-of-process consumers (the reliant
// CLI) to inject framework context in-memory rather than reading a
// possibly-stale on-disk reliant.md.
//
// The template body is the same one `forge project new` writes for non-reliant
// harnesses, so in-memory and on-disk renderings stay byte-identical.
//
// Returns an error when forge.yaml is missing, unreadable, has no
// `name:` field, or when the template fails to render.
func RenderProjectMemory(projectRoot string) ([]byte, error) {
	if projectRoot == "" {
		return nil, fmt.Errorf("projectRoot is required")
	}
	cfgPath := filepath.Join(projectRoot, "forge.yaml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", cfgPath, err)
	}
	// Minimal parse — only the project name is needed for rendering.
	// Deliberately avoid LoadProject here so reliant doesn't transitively
	// reject projects with config issues that don't affect memory.
	var head struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal(data, &head); err != nil {
		return nil, fmt.Errorf("parse %s: %w", cfgPath, err)
	}
	if head.Name == "" {
		return nil, fmt.Errorf("%s: missing required field `name`", cfgPath)
	}
	// The guide describes the project as it is: a static site gets the
	// frontend-only memory, an adopted app one composed of the forge pieces
	// it uses, and a forge Go service the Go guide (projectMemoryData).
	tmplData, err := projectMemoryData(projectRoot, head.Name)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", projectRoot, err)
	}
	return templates.ProjectTemplates().Render("reliant.md.tmpl", tmplData)
}
