package generator

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/templates"
)

// githubOwnerFromModulePath extracts the GitHub owner segment from a Go
// module path like `github.com/example/demo` (-> "example"). Returns ""
// for non-github hosts or any path that doesn't have an owner segment.
// Used to seed the default `.github/CODEOWNERS` entry; when inference
// fails the generator skips emitting the file rather than shipping a
// review-free stub.
func githubOwnerFromModulePath(modulePath string) string {
	const host = "github.com/"
	if !strings.HasPrefix(modulePath, host) {
		return ""
	}
	rest := modulePath[len(host):]
	slash := strings.Index(rest, "/")
	if slash <= 0 {
		return ""
	}
	return rest[:slash]
}

// generateCIFiles writes the GitHub Actions workflows, dependabot, and the
// .github starters at `forge project new` time.
//
// The workflow set and every template's data come from CIWorkflows — the
// same function `forge generate` calls — fed the forge.yaml this scaffold
// just wrote, read back through the loader `forge generate` uses. So the
// two commands cannot render different workflows for the same project.
func (g *ProjectGenerator) generateCIFiles() error {
	const provider = "github"

	cfg, err := ReadProjectConfig(filepath.Join(g.Path, "forge.yaml"))
	if err != nil {
		return fmt.Errorf("read the scaffolded forge.yaml for CI workflow data: %w", err)
	}
	var frontends []templates.FrontendCIConfig
	for _, fe := range cfg.Frontends {
		if p, ok := fe.Dir(g.Path); ok {
			frontends = append(frontends, templates.FrontendCIConfig{Name: fe.Name, Path: p})
		}
	}

	// Load checksums to record initial CI file hashes
	cs, err := LoadChecksums(g.Path)
	if err != nil {
		return fmt.Errorf("load checksums: %w", err)
	}

	// Record the binary that produced the CI files in forge's ownership
	// state. (The workflows themselves pin nothing: they install the forge
	// go.mod resolves at run time.)
	cs.ForgeVersion = buildinfo.Version()

	// The deployed envs this scaffold wrote are hosted (generateKCLDeploy
	// records them), so their CI is the hosted pipeline: release.yml builds
	// once and promotes one release through them, with no registry login.
	// `forge generate` discovers the same fact from the render (ciInputs); a
	// project being born has no generated config to render with yet, so the
	// scaffold states what it just wrote.
	in := CIInputs{Frontends: frontends, HostedEnvs: g.hostedEnvs}
	for _, f := range CIWorkflowsFor(g.Path, cfg, in) {
		content, err := templates.CITemplates(provider).Render(f.Template, f.Data)
		if err != nil {
			return fmt.Errorf("render CI template %s: %w", f.Template, err)
		}
		// Scaffold-once ("yours"): CI workflows are the canonical
		// hand-edited policy file (add jobs, secrets, custom steps), so
		// they are user-owned from birth — NO forge:hash marker, never
		// re-emitted while present. Certifying them Tier-1 mis-flagged
		// every sanctioned edit as `user_edited_gen_files` drift and
		// pushed users to `forge project disown`. This mirrors the PR template /
		// CODEOWNERS starters written just below.
		if _, err := checksums.WriteScaffoldIfMissing(g.Path, f.Dest, content); err != nil {
			return fmt.Errorf("write %s: %w", f.Dest, err)
		}
	}

	// Static files
	staticFiles := []struct {
		templateName string
		dest         string
	}{
		{"pull_request_template.md", ".github/pull_request_template.md"},
	}

	for _, f := range staticFiles {
		content, err := templates.CITemplates(provider).Get(f.templateName)
		if err != nil {
			return fmt.Errorf("read CI template %s: %w", f.templateName, err)
		}
		// Scaffold-once ("yours"): the PR template is a one-shot starter
		// the user owns after creation — `forge generate` never re-emits
		// it, so a Tier-1/legacy record would put the user's own edits
		// under the stomp guard (FRICTION 2026-06-05, cp-forge: users
		// hand-flipped `forked: true` to escape exactly that
		// misclassification).
		if _, err := checksums.WriteScaffoldIfMissing(g.Path, f.dest, content); err != nil {
			return fmt.Errorf("write %s: %w", f.dest, err)
		}
	}

	// CODEOWNERS is only emitted when we can confidently infer a GitHub
	// owner from the module path. For non-github module paths (e.g.
	// `example.com/team/proj`) we skip the file entirely — shipping a
	// review-free stub that silently bypasses branch protection is worse
	// than having no file at all. Users can add `.github/CODEOWNERS`
	// manually when they're ready.
	if owner := githubOwnerFromModulePath(g.ModulePath); owner != "" {
		content, err := templates.CITemplates(provider).Render("CODEOWNERS.tmpl", templates.CIWorkflowData{GitHubOwner: owner})
		if err != nil {
			return fmt.Errorf("render CODEOWNERS: %w", err)
		}
		// Scaffold-once ("yours"): CODEOWNERS carries the `yours:
		// scaffolded once ... — starter` banner — review policy is the
		// user's to evolve, and edits must not trip the Tier-1 stomp
		// guard.
		if _, err := checksums.WriteScaffoldIfMissing(g.Path, ".github/CODEOWNERS", content); err != nil {
			return fmt.Errorf("write CODEOWNERS: %w", err)
		}
	}

	// Save checksums so forge generate knows what was initially generated
	if err := SaveChecksums(g.Path, cs); err != nil {
		return fmt.Errorf("save checksums: %w", err)
	}

	return nil
}
