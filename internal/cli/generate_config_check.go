// Config vs. filesystem cross-check — the "loud-by-default" guard.
//
// Pre-2026-06-07 the generate pipeline silently skipped declared entities
// that had no on-disk backing, and silently ignored on-disk entities that
// weren't declared in forge.yaml. The asymmetry was the #1 source of
// "I added a service but generate did nothing" friction:
//
//   - forge.yaml declares services[].name=foo but proto/services/foo/
//     doesn't exist → generate runs, emits nothing for foo, exits 0.
//   - proto/services/foo/ exists on disk but forge.yaml lacks an entry →
//     generate sees the proto but skips bootstrap wiring for it.
//
// This file walks forge.yaml's declarations and the on-disk trees in
// parallel and collects every mismatch into a single batched report.
// stepLoadConfig calls validateConfigVsFilesystem after a successful
// LoadProject so the user sees the asymmetry the moment they run generate,
// not three steps deeper at a confusing "missing import" error.
//
// Only what forge.yaml still DECLARES can be cross-checked here. Services
// and internal packages are discovered from their real sources, so there is
// no second list for them to disagree with — the declaration is the code.
//
// Opt-out: --skip-config-check. We expose an opt-out (not opt-in) so the
// default path is loud — new adopters get the check unconditionally, and
// scripted callers in pathological setups (e.g. mid-migration where a
// proto dir exists transiently with no forge.yaml entry yet) can pass the
// flag to bypass without changing the steady-state default.
package cli

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/config"
)

// validateConfigVsFilesystem cross-checks forge.yaml declarations against
// on-disk reality. Returns a non-nil error wrapping every mismatch as a
// single batched cliutil.UserErr — callers (stepLoadConfig) surface this
// at load time so the user fixes all asymmetries in one round-trip.
//
// Skip semantics: when cfg is nil (directory-scan fallback) there's
// nothing to validate against, so we no-op. When the project has no
// proto/ tree at all (CLI / library kind), we also no-op — those project
// shapes legitimately have no services/packages/frontends.
func validateConfigVsFilesystem(projectDir string, cfg *config.ProjectConfig) error {
	if cfg == nil {
		return nil
	}
	var findings []string

	findings = append(findings, checkDeclaredFrontends(projectDir, cfg)...)

	if len(findings) == 0 {
		return nil
	}

	sort.Strings(findings)
	var b strings.Builder
	fmt.Fprintf(&b, "%d forge.yaml ↔ filesystem mismatch(es):\n\n", len(findings))
	for _, f := range findings {
		fmt.Fprintf(&b, "  • %s\n", f)
	}
	return cliutil.UserErr("forge generate (config check)",
		b.String(),
		"forge.yaml",
		"fix the mismatches above, or pass --skip-config-check to bypass for parallel-lane / mid-migration scenarios")
}

// checkDeclaredFrontends walks cfg.Frontends and verifies each entry has
// an on-disk directory at the configured path. Frontends do not have a
// proto-side requirement (they consume the gen/ts/ stubs from services
// they don't own), so the check is one-sided: path must exist.
//
// We skip the check when fe.Path is empty AND the default path doesn't
// exist either — that's a freshly-scaffolded entry the user has declared
// but not run `forge scaffold frontend` against yet. Better to let the
// scaffold step handle it than to error out here.
func checkDeclaredFrontends(projectDir string, cfg *config.ProjectConfig) []string {
	var out []string
	for _, fe := range cfg.Frontends {
		// A frontend declaring a cross-repo `source:` has no directory in
		// this tree BY DESIGN — that is the whole point of the pin, and it
		// is what lets the frontend build in a CI checkout of this repo
		// alone. Its code is materialized from the pin at build time, so
		// there is nothing for a filesystem cross-check to look at here.
		if fe.HasGitSource() {
			continue
		}
		path, ok := fe.Dir(projectDir)
		if !ok {
			continue
		}
		fullPath := filepath.Join(projectDir, path)
		if !dirExists(fullPath) {
			out = append(out, fmt.Sprintf(
				"frontends[name=%s] (type=%s) declared in forge.yaml but path %q does not exist (expected at %s) — run 'forge scaffold frontend %s' to scaffold it",
				fe.Name, fe.Type, path, fullPath, fe.Name))
		}
	}
	return out
}
