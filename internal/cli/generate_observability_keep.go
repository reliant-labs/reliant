package cli

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
)

// observabilityDir is where the observability step writes: dashboards, the
// project and process specs, saved searches and alert defaults.
const observabilityDir = "deploy/observability"

// keepObservabilityOutputs is the observability step's answer to an
// environment that did not render.
//
// The outputs are the union of what every env declares (frontends,
// workloads, analytics), so deriving them without that env drops its part:
// a version-skewed KCL import once shrank control-plane's project_gen.json
// by 512 lines while the run went on to write it (forge bug report
// f9a3c9a1). So nothing under deploy/observability/ is written, and every
// file already there is recorded as this run's target, which keeps the
// stale-file sweep from reading the unwritten outputs as orphans and
// deleting them.
//
// It is a warning, not a failed step: an env that refuses to render on
// purpose is a normal state. `forge scaffold operator` binds the operator to
// a cluster in each hosted env that declares none, and that env refuses to
// render until one is declared. The run goes on and exits 0. The next run
// after the env renders again regenerates the outputs.
func keepObservabilityOutputs(w io.Writer, projectDir string, failed []envRenderFailure) error {
	kept, err := markObservabilityOutputsKept(projectDir)
	if err != nil {
		return fmt.Errorf("observability dashboards: keep the outputs under %s/: %w", observabilityDir, err)
	}
	fmt.Fprint(w, observabilityKeptWarning(failed, kept))
	return nil
}

// markObservabilityOutputsKept records every file under deploy/observability/
// as a Tier-1 target of this run (emitted by forge, deliberately not
// rewritten) and returns how many there are.
func markObservabilityOutputsKept(projectDir string) (int, error) {
	root := filepath.Join(projectDir, filepath.FromSlash(observabilityDir))
	kept := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == root {
				return filepath.SkipDir
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(projectDir, path)
		if err != nil {
			return err
		}
		checksums.MarkTier1Target(filepath.ToSlash(rel))
		kept++
		return nil
	})
	return kept, err
}

// observabilityKeptWarning names each env that did not render, why, and the
// way out.
func observabilityKeptWarning(failed []envRenderFailure, kept int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "⚠️  observability dashboards: kept the %d existing file(s) under %s/ as they are, because %d environment(s) did not render. "+
		"Deriving them without an env would drop the frontends and processes it declares:\n", kept, observabilityDir, len(failed))
	for _, f := range failed {
		fmt.Fprintf(&b, "    deploy/kcl/%s: %s\n", f.Env, renderFailureCause(f.Err))
	}
	fmt.Fprintf(&b, "  Make it render — declare the cluster a workload bound `_on_cluster` needs (an operator, say), or drop that "+
		"workload's line from the env — and the next `%s generate` regenerates them. `%s env render <env>` shows the full error.\n",
		Name(), Name())
	return b.String()
}
