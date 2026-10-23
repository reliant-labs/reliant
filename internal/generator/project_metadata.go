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
//     <memory-file>, .mcp.json, .mcp.json.example.
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

	templateData := struct {
		Name string
		CLI  string
	}{Name: g.Name, CLI: cliName()}

	// User-owned top-level memory file — path depends on --harness.
	// Skipped for the reliant harness: reliant loads the framework
	// content in-memory via forgecli.RenderProjectMemory whenever it
	// detects forge.yaml, so a stale on-disk copy would just create
	// upgrade drift. Other harnesses (claude/cursor/copilot/codex) have
	// no such auto-discovery path and still need the file written.
	if g.Harness != HarnessReliant && g.Harness != "" {
		memoryFile := g.Harness.MemoryFilePath()
		memoryDest := filepath.Join(g.Path, memoryFile)
		// Ensure parent directory exists (needed for copilot: .github/).
		if dir := filepath.Dir(memoryDest); dir != g.Path {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("failed to create directory for %s: %w", memoryFile, err)
			}
		}
		if err := writeIfAbsent(memoryDest, "reliant.md.tmpl", templateData); err != nil {
			return fmt.Errorf("failed to write %s: %w", memoryFile, err)
		}
	}

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
