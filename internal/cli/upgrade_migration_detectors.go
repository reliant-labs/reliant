package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/codemod"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/shellrun"
)

// forgeDetectCommand is the in-process command a migration's detection
// script calls to ask forge itself whether the project has an old shape:
// `forge-detect <name>` exits 0 when it does, 1 when it does not, and 2
// for a name forge does not know (which reads as "does not apply").
//
// It exists for shapes a grep cannot read the way forge reads them. The
// zod 3 frontend is one: whether a package.json range means zod 3 takes a
// semver range parser and, for an open range, the installed version, and a
// detection that answered differently from `forge generate` would offer the
// v1.8.0 playbook to a frontend generate writes zod 4 pages for, or the
// reverse.
const forgeDetectCommand = "forge-detect"

// migrationDetector answers forge-detect <name> for a project root.
func migrationDetector(name string) (func(projectRoot string) bool, bool) {
	switch name {
	case "pre-zod4-forms":
		return projectHasPreZod4Frontend, true
	case "pgo-refresh-due":
		return projectMayGetPGORefresh, true
	case "trimpath-default":
		// The codemod's own decision, so the playbook is offered exactly
		// where `forge fix trimpath-default` has something to do or say.
		return projectNeedsTrimpathDefault, true
	case "tests-capture":
		// A lane still in an old forge shape, or no test:report: what
		// `forge fix tests-capture` rewrites (projectNeedsTestsCapture).
		return projectNeedsTestsCapture, true
	case "ci-oidc-permissions":
		// A job that runs `forge test report` and lacks id-token: write:
		// what `forge fix ci-oidc-permissions` edits.
		return projectNeedsCIOIDCPermissions, true
	case "entity-type-alias":
		// The codemod's own decision: a page whose entity type import
		// collides with another import of the file.
		return projectNeedsEntityTypeAlias, true
	}
	return nil, false
}

// projectNeedsEntityTypeAlias is the v1.11.0 entity-type-alias playbook's
// detection (`forge-detect entity-type-alias`): whether `forge fix
// entity-type-alias` would rewrite a page or report a use to check. So the
// playbook is offered exactly where a page still collides, and resolved once
// none does.
func projectNeedsEntityTypeAlias(projectRoot string) bool {
	c, ok := codemod.Lookup("entity-type-alias")
	if !ok {
		return false
	}
	rep, err := c.Run(projectRoot, codemod.Options{})
	return err == nil && (rep.Changed() || len(rep.Next) > 0)
}

// migrationDetectionCommands are the in-process commands a detection script
// can call: the portable grep and rg, and forge-detect.
func migrationDetectionCommands() map[string]shellrun.CommandFunc {
	cmds := shellrun.PortableCommands()
	cmds[forgeDetectCommand] = func(_ context.Context, dir string, args []string, _ io.Reader, _, stderr io.Writer) int {
		if len(args) != 1 {
			_, _ = fmt.Fprintf(stderr, "usage: %s <detector>\n", forgeDetectCommand)
			return 2
		}
		detect, ok := migrationDetector(args[0])
		if !ok {
			_, _ = fmt.Fprintf(stderr, "%s: unknown detector %q\n", forgeDetectCommand, args[0])
			return 2
		}
		if detect(dir) {
			return 0
		}
		return 1
	}
	return cmds
}

// projectMayGetPGORefresh reports whether .github/workflows/pgo-refresh.yml
// is, or may still become, the project's — what the v1.11.0 pgo-default-on
// playbook explains. It is not when forge.yaml declines it (ci.workflows,
// ci.provider, features.ci, a non-service kind), when the user deleted it
// (the birth ledger records it) or disowned it, when the project refreshes
// its profiles in a workflow of its own (generator.PGORefreshDeclaredElsewhere),
// or when no cmd/<bin> holds a Go file. Which env builds which binary takes a
// KCL render, which a detection does not pay for: `forge generate` renders,
// and writes the file only where a hosted env builds a Go binary. A forge.yaml
// that does not load leaves the decision to the file checks.
func projectMayGetPGORefresh(projectRoot string) bool {
	if cfg, err := config.LoadProjectDir(projectRoot); err == nil && cfg != nil {
		if !cfg.Features.CIEnabled() || !cfg.IsServiceKind() || cfg.CI.EffectiveProvider() != config.CIProviderGitHub || !cfg.CI.WorkflowEnabled("pgo-refresh") {
			return false
		}
	}
	if _, err := os.Stat(filepath.Join(projectRoot, filepath.FromSlash(generator.PGORefreshPath))); err != nil &&
		!checksums.ScaffoldMayBeBorn(projectRoot, generator.PGORefreshPath) {
		return false
	}
	if generator.PGORefreshDeclaredElsewhere(projectRoot) != "" {
		return false
	}
	gofiles, _ := filepath.Glob(filepath.Join(projectRoot, "cmd", "*", "*.go"))
	return len(gofiles) > 0
}

// projectHasPreZod4Frontend reports whether any frontend of the project
// reads, by frontendFormDialect — the same reading `forge generate` writes
// pages from — as zod 3 (the v1.8.0 playbook's section 1) or as zod 4 with
// an @hookform/resolvers below 5 (its section 2: those resolvers predate
// zod 4 support, so the frontend's forms break either way). The frontends
// are the project's load-time inventory (config.ResolveInventoryAtLoad),
// plus the root package.json (a site) and every frontends/<name>, so a
// project whose forge.yaml does not load is still read. A frontend only a
// deploy/kcl declaration places outside frontends/ takes a KCL render to
// find, which a detection does not pay for; `forge generate` renders, and
// names the playbook when it writes zod 3 pages there.
func projectHasPreZod4Frontend(projectRoot string) bool {
	dirs := []string{projectRoot}
	if matches, err := filepath.Glob(filepath.Join(projectRoot, "frontends", "*")); err == nil {
		dirs = append(dirs, matches...)
	}
	if cfg, err := config.LoadProjectDir(projectRoot); err == nil && cfg != nil {
		for _, fe := range cfg.Frontends {
			if rel, ok := fe.Dir(projectRoot); ok {
				dirs = append(dirs, filepath.Join(projectRoot, rel))
			}
		}
	}
	seen := map[string]bool{}
	for _, dir := range dirs {
		dir = filepath.Clean(dir)
		if seen[dir] {
			continue
		}
		seen[dir] = true
		if d, _ := frontendFormDialect(dir); d != (codegen.FormDialect{}) {
			return true
		}
	}
	return false
}
