package generator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/reliant-labs/forge/internal/assets"
	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/templates"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// writeProjectMetadata writes everything under .reliant/, the top-level
// memory file (whose name depends on the project's Harness), and the
// project-level .mcp.json files.
//
// File ownership model:
//
//   - forge-owned (always overwritten on regeneration):
//     .reliant/project.json, .reliant/README.md.
//
//   - User-owned (written only if absent, never touched if present):
//     the agent files (see writeAgentSurfaces), .mcp.json, .mcp.json.example.
//
// Skills and conventions are served via `forge skill list/load` from embedded
// templates — no files are written to disk for them.
func (g *ProjectGenerator) writeProjectMetadata() error {
	reliantDir := filepath.Join(g.Path, ".reliant")
	if err := os.MkdirAll(reliantDir, 0o755); err != nil {
		return fmt.Errorf("failed to create .reliant directory: %w", err)
	}

	if err := g.writeProjectJSON(reliantDir); err != nil {
		return err
	}

	if err := assets.WriteTemplateWithData(".reliant-README.md", filepath.Join(reliantDir, "README.md"), nil); err != nil {
		return fmt.Errorf("failed to write .reliant/README.md: %w", err)
	}

	if err := writeAgentSurfaces(g.Path, g.Name, g.Harness, false); err != nil {
		return err
	}

	templateData := ProjectMemoryData{Name: g.Name, CLI: cliName()}

	// User-owned MCP config — write only if absent.
	if err := writeIfAbsent(filepath.Join(g.Path, ".mcp.json"), "mcp.json.tmpl", templateData); err != nil {
		return fmt.Errorf("failed to write .mcp.json: %w", err)
	}

	// Documentation file for opt-in MCP servers — write only if absent so a
	// user who deleted it intentionally isn't pestered.
	if err := writeIfAbsent(filepath.Join(g.Path, ".mcp.json.example"), "mcp.json.example.tmpl", templateData); err != nil {
		return fmt.Errorf("failed to write .mcp.json.example: %w", err)
	}

	return nil
}

// ProjectMemoryData is what the framework memory template
// (project/reliant.md.tmpl) renders from — on disk for a harness that reads a
// file, and live for reliant (cli.RenderProjectMemory).
type ProjectMemoryData struct {
	// Name is the project's name (forge.yaml `name:`).
	Name string
	// CLI is how the user invokes forge: "forge", or "reliant forge" when
	// embedded.
	CLI string
	// Site reports a static site: a project with no Go module, made of the
	// app at its root and its deploy envs (`forge project new --kind site`).
	// Its memory is the frontend-only quick reference; every other project's
	// is the Go one. Derived from the tree (config.DiscoverRootFrontend),
	// like every other fact about a project's shape, so no flag can disagree
	// with what is on disk.
	Site bool
}

// writeProjectJSON writes the immutable project metadata JSON under .reliant/.
func (g *ProjectGenerator) writeProjectJSON(reliantDir string) error {
	metadata := map[string]interface{}{
		"name":        g.Name,
		"module_path": g.ModulePath,
		"created_at":  projectCreatedAt(filepath.Join(reliantDir, "project.json")),
		"version":     "1.0.0",
		"generator":   "forge",
	}

	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}
	// MarshalIndent emits no final newline; end-of-file-fixer would add one.
	data = append(data, '\n')

	if err := atomicfile.WriteFile(filepath.Join(reliantDir, "project.json"), data, 0o644); err != nil {
		return fmt.Errorf("failed to write .reliant/project.json: %w", err)
	}
	return nil
}

// projectCreatedAt is the creation time an existing project.json records, or
// now for a new one. Rewriting the file must not move it: a project is created
// once, and a timestamp that changed on every rewrite made the file differ
// whenever the rewrite crossed a second boundary.
func projectCreatedAt(path string) string {
	var existing struct {
		CreatedAt string `json:"created_at"`
	}
	if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &existing) == nil && existing.CreatedAt != "" {
		return existing.CreatedAt
	}
	return time.Now().Format(time.RFC3339)
}

// agentFile is one agent-facing file forge scaffolds: where it goes and the
// template that renders it.
type agentFile struct{ path, tmpl string }

// agentFiles lists what a harness needs so its tool finds the project guide.
//
// AGENTS.md is the cross-tool standard and carries the guide for EVERY
// harness; it is the one rendered copy (reliant.md.tmpl), so nothing can
// drift. The harness-specific files are thin pointers to it, and the skills
// pointer says only to run the forge binary: skill bodies never leave the
// binary.
func (h Harness) agentFiles() []agentFile {
	files := []agentFile{{"AGENTS.md", "reliant.md.tmpl"}}
	switch h {
	case HarnessClaude:
		// Claude Code reads AGENTS.md only when no CLAUDE.md exists, so the
		// import keeps the one copy.
		files = append(files,
			agentFile{h.MemoryFilePath(), "agent-claude.md.tmpl"},
			agentFile{".claude/skills/forge/SKILL.md", "agent-skill.md.tmpl"})
	case HarnessCursor:
		files = append(files,
			agentFile{h.MemoryFilePath(), "agent-pointer.md.tmpl"},
			agentFile{".cursor/rules/forge.mdc", "agent-cursor.mdc.tmpl"})
	case HarnessCopilot:
		files = append(files, agentFile{h.MemoryFilePath(), "agent-pointer.md.tmpl"})
	}
	return files
}

// writeAgentSurfaces writes the harness's agent files under root, each only if
// absent: they are user-owned from birth. Reliant needs no file of its own
// (it renders the guide in memory from forge.yaml) but still gets AGENTS.md
// for every other tool that opens the project. site renders AGENTS.md as a
// static site's memory (the frontend-only quick reference) rather than the Go
// one; the harness pointers import it, so they follow.
func writeAgentSurfaces(root, name string, h Harness, site bool) error {
	data := ProjectMemoryData{Name: name, CLI: cliName(), Site: site}
	for _, f := range h.agentFiles() {
		dest := filepath.Join(root, f.path)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("failed to create directory for %s: %w", f.path, err)
		}
		if err := writeIfAbsent(dest, f.tmpl, data); err != nil {
			return fmt.Errorf("failed to write %s: %w", f.path, err)
		}
	}
	return nil
}

// writeIfAbsent renders the given template to destPath only if destPath does
// not already exist. This is used for user-owned files (reliant.md, .mcp.json,
// .mcp.json.example) to avoid clobbering local edits on regeneration.
func writeIfAbsent(destPath, templateName string, data interface{}) error {
	if _, err := os.Stat(destPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to stat %s: %w", destPath, err)
	}
	return assets.WriteTemplateWithData(templateName, destPath, data)
}

func (g *ProjectGenerator) generateGolangciLint() error {
	// Tier-2 (user-owned): scaffold the default config on first run, but
	// never stomp on user customizations. Projects routinely tighten /
	// loosen the default linter set; overwriting on every regen would
	// erase that work silently. The v2 cp-forge migration repro'd the
	// pain — users learned to keep a separate .golangci.user.yml just
	// to survive `forge generate`.
	// Render from the full scaffold payload so the template can branch on
	// the typed-access guardrail (config.enforce_typed_access). forScaffold
	// supplies the greenfield default (TypedAccessGuard="error") and the
	// allowlisted loader package; the only other field the template reads is
	// Module (goimports local-prefixes).
	data := g.forScaffold()
	return writeIfAbsent(filepath.Join(g.Path, ".golangci.yml"), "golangci.yml.tmpl", data)
}

// generateExamplesReadme scaffolds an examples/ directory with a README that
// documents the convention for client-side demos. We don't ship a concrete
// example because what's appropriate depends on the project's shape; the
// README gives agents and contributors a stable home to drop one into.
func (g *ProjectGenerator) generateExamplesReadme() error {
	data := struct {
		Name string
	}{Name: g.Name}
	content, err := templates.ProjectTemplates().Render("examples-README.md.tmpl", data)
	if err != nil {
		return fmt.Errorf("render examples/README.md: %w", err)
	}
	examplesDir := filepath.Join(g.Path, "examples")
	if err := os.MkdirAll(examplesDir, 0o755); err != nil {
		return fmt.Errorf("create examples dir: %w", err)
	}
	return atomicfile.WriteFile(filepath.Join(examplesDir, "README.md"), content, 0o644)
}

// generatePkgMiddleware scaffolds the project's thin auth-policy file
// (pkg/middleware/middleware.go) plus its policy-wiring test.
//
// The middleware MECHANISMS live in the forge libraries
// (pkg/authn, pkg/middleware, pkg/observe) — versioned with forge so
// security fixes flow to every project. Historically ~25 static
// middleware files were photocopied here; field evidence showed they
// stayed byte-identical and never received fixes, so they were folded
// into the libraries. The two files below are user-owned from line one
// (scaffold-once; never overwritten if present).
//
// Access control is HANDLER LOGIC now (GetUser + hand-written checks), not
// generated wiring — so there is no role_resolver.go or access-control
// scaffold.
func (g *ProjectGenerator) generatePkgMiddleware() error {
	// middleware.go is RENDERED: it declares the native sign-in session
	// cookie's project-scoped name (SessionCookieName), which the broker in
	// internal/app sets and the interceptor here reads.
	middlewareFiles := []struct {
		templateName string
		destName     string
		templated    bool
	}{
		{"middleware.go.tmpl", "middleware.go", true},
		{"middleware_test.go", "middleware_test.go", false},
	}

	for _, f := range middlewareFiles {
		destPath := filepath.Join(g.Path, "pkg", "middleware", f.destName)
		if _, err := os.Stat(destPath); err == nil {
			continue // user-owned — never clobber an existing copy
		}
		var content []byte
		var err error
		if f.templated {
			content, err = templates.ProjectTemplates().Render(f.templateName, g.forScaffold())
		} else {
			content, err = templates.ProjectTemplates().Get(f.templateName)
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", f.templateName, err)
		}
		if err := atomicfile.WriteFile(destPath, content, 0644); err != nil {
			return fmt.Errorf("write %s: %w", f.destName, err)
		}
	}

	// The open-procedure set the interceptor reads. It is forge-owned and
	// `forge generate` rewrites it from the protos' auth_required
	// declarations — but the package has to COMPILE before any generate has
	// run, and middleware.go references the symbol this file declares. A
	// project with no services yet gets the health probes and nothing else,
	// which is also the correct answer.
	procedures, err := codegen.RenderOpenProcedures(nil, "")
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(filepath.Join(g.Path, "pkg", "middleware", "procedures_gen.go"), procedures, 0644); err != nil {
		return fmt.Errorf("write procedures_gen.go: %w", err)
	}
	return nil
}

// recordFrozenChecksums re-certifies the frozen files managed by
// `forge project upgrade`. Must run after the frozen files have been written so
// new projects start with valid embedded hashes.
func (g *ProjectGenerator) recordFrozenChecksums() error {
	return RecordFrozenChecksums(g.Path, g.effectiveBinary(), g.effectiveKind())
}

// RecordFrozenChecksums re-stamps the embedded forge:hash marker on
// every marker-bearing managed file at projectDir. Exposed publicly so
// callers outside the scaffold path (e.g. `forge project new` after
// `bootstrapGeneratedCode` runs goimports and reformats files) can
// re-certify the post-formatting bytes — otherwise the hashes stamped
// at scaffold time would not match the on-disk content, and the drift
// guard / `forge project upgrade --dry-run` would flag every formatted file as
// user-modified.
func RecordFrozenChecksums(projectDir, binary, kind string) error {
	for _, f := range managedFilesForKindBinary(kind, binary, resolveBinaryName(projectDir)) {
		fullPath := filepath.Join(projectDir, f.destPath)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read %s for re-stamp: %w", f.destPath, err)
		}
		// Only re-certify files that already carry a marker — Tier-2
		// scaffolds are user-owned from birth and stay unmarked.
		if _, found := checksums.ExtractMarker(content); !found {
			continue
		}
		restamped, ok := checksums.Stamp(f.destPath, content)
		if !ok || bytes.Equal(restamped, content) {
			continue
		}
		if err := atomicfile.WriteFile(fullPath, restamped, 0o644); err != nil {
			return fmt.Errorf("re-stamp %s: %w", f.destPath, err)
		}
	}
	return nil
}
