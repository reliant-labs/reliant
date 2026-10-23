// File: internal/cli/lint/lint_pdb.go
//
// pdb-blocks-drain — a PodDisruptionBudget that can never allow a disruption
// (minAvailable >= replicas, maxUnavailable 0, minAvailable 100%) blocks every
// node drain that touches its pods, forever. The check runs over each env's
// real render, so it covers forge-rendered PDBs and hand-written
// forge.Manifests alike. Warnings only: a render that fails here is skipped,
// since lint judges the project and `forge env render` owns render errors.
package lint

import (
	"context"
	"os"
	"path/filepath"
	"sort"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/config"
)

const pdbRuleID = "pdb-blocks-drain"

type pdbLintFinding struct {
	env string
	cluster.PDBFinding
}

func pdbFindings(ctx context.Context, projectDir string, cfg *config.ProjectConfig) []pdbLintFinding {
	kclDir := filepath.Join(projectDir, deployKCLDirFor(cfg))
	entries, err := os.ReadDir(kclDir)
	if err != nil {
		return nil
	}
	var out []pdbLintFinding
	for _, e := range entries {
		mainK := filepath.Join(kclDir, e.Name(), "main.k")
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(mainK); err != nil {
			continue
		}
		manifests, err := cluster.RenderManifests(ctx, mainK, "lint", "", e.Name(), nil, nil)
		if err != nil {
			continue
		}
		for _, f := range cluster.CheckPDBs(manifests) {
			out = append(out, pdbLintFinding{env: e.Name(), PDBFinding: f})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].env < out[j].env })
	return out
}

func collectPDBJSON(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
	var out []lintJSONFinding
	for _, f := range pdbFindings(rc.ctx, rc.cwd, rc.cfg) {
		out = append(out, lintJSONFinding{
			File:     deployKCLDirFor(rc.cfg) + "/" + f.env,
			Severity: lintSevWarning,
			Rule:     pdbRuleID,
			Message:  "env " + f.env + ": PodDisruptionBudget " + f.Name + ": " + f.Reason,
			FixHint:  f.Fix,
		})
	}
	return out, false, nil
}
