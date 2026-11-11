package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/templates"
)

// RenderProjectMemory reads forge.yaml at projectRoot, renders the
// project memory template with the project name + invoking CLI name —
// as a static site's memory for a site (no go.mod, the app at the root) —
// and returns
// the bytes. Used by out-of-process consumers (the reliant
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
		Name       string `yaml:"name"`
		ModulePath string `yaml:"module_path"`
	}
	if err := yaml.Unmarshal(data, &head); err != nil {
		return nil, fmt.Errorf("parse %s: %w", cfgPath, err)
	}
	if head.Name == "" {
		return nil, fmt.Errorf("%s: missing required field `name`", cfgPath)
	}
	// A static site — no Go module, a Vite or Next.js app at the root —
	// gets the frontend-only memory. The same shape test the frontend
	// inventory (config.DiscoverRootFrontend) and the scaffolds use, read
	// straight off the tree.
	_, site := config.DiscoverRootFrontend(projectRoot)
	// An adopted app (the `adopt` skill) — workloads declared in
	// deploy/kcl/workloads.k, no Go module, no module_path, no root
	// frontend — gets the any-language memory: the Go one would send an
	// agent after handlers and protos this project does not have.
	container := !site && head.ModulePath == "" &&
		!fileExists(filepath.Join(projectRoot, "go.mod")) &&
		fileExists(filepath.Join(projectRoot, "deploy", "kcl", "workloads.k"))
	tmplData := generator.ProjectMemoryData{Name: head.Name, CLI: Name(), Site: site, Container: container}
	return templates.ProjectTemplates().Render("reliant.md.tmpl", tmplData)
}
