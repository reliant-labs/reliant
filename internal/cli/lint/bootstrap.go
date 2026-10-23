// Package lint holds the `forge lint` command group — the project linter
// pipeline (golangci / buf / frontend / forge-convention / scaffold /
// migration-safety / wire-coverage …) plus the
// targeted single-rule flags and the --json aggregator.
//
// It is a dir-nested command group (the devspace idiom). The substrate —
// the ~30 run*/collect* linter entry points and the shared linterStep
// table — moved here verbatim from package cli so the command is the
// substrate's home, not a thin shim over package-cli internals. The few
// genuinely cross-cutting helpers it needs (project-store loader, the
// forge.yaml-not-found sentinel, the user-vs-maintainer flag split) come
// from the leaf packages internal/cli/factory and internal/cli/cmdutil, so
// the group never imports internal/cli (which would cycle — internal/cli
// blank-imports the groups).
//
// audit.go (now the internal/cli/audit group) reuses two of this package's
// collectors — CollectOptionalDepsGuardFindings and
// CollectConfigDepsFindings — via the exported aliases in
// lint_optional_deps_guard.go / lint_config_deps.go. That is a clean
// group→group dependency (audit → lint), no cycle.
package lint

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/projectstore"
)

func init() { factory.Register(newCmd) }

// loadProjectStore loads forge.yaml via the loader internal/cli registered
// with the factory. The lint substrate has many free functions that read
// project config without a *Factory in scope, so reaching the registered
// loader package-level (rather than threading a Factory through ~80
// signatures) keeps the relocation behavior-preserving. It is the same
// loader the rest of the CLI uses.
func loadProjectStore() (*projectstore.Store, error) {
	return factory.LoadProjectStore()
}

// ErrProjectConfigNotFound is the canonical "forge.yaml not found" sentinel,
// re-exported from cmdutil so the lint helpers compare against the same
// value internal/cli's config.ErrProjectConfigNotFound aliases.
var ErrProjectConfigNotFound = cmdutil.ErrProjectConfigNotFound

// hideDevFlags forwards to cmdutil.HideDevFlags (the shared
// user-vs-maintainer surface split).
func hideDevFlags(cmd *cobra.Command, names ...string) { cmdutil.HideDevFlags(cmd, names...) }

// dirExists / fileExists are the trivial os.Stat wrappers the lint pipeline
// uses for its directory/file presence gates. Per cmdutil's policy, these
// one-line stdlib checks are duplicated locally rather than shared.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}
