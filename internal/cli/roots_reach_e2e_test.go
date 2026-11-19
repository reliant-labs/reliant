//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EResolutionIsScopedToWhatARootReaches pins reach-scoped resolution
// (composition_graph_reach.go) on a real scaffold, the state a big project is
// in while it declares its roots one at a time:
//
//   - two declared roots (server: the API listener, worker: tick) and a third
//     worker, reaper, that no root holds and whose Deps nothing fills. Generate
//     succeeds, leaves reaper out of graph_gen.go, and the project builds;
//   - `forge lint --graph` fails on it once forge-graph-unreachable gates
//     (forge.yaml's lint.rules sets it to error), and passes with a warning
//     once lint.rules sets the rule to warn;
//   - a Deps field nothing fills on a node a root DOES reach (the api
//     service, under the server root's listener) still fails generate,
//     naming that field and not reaper's.
func TestE2EResolutionIsScopedToWhatARootReaches(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "reach", "--mod", "github.com/test/reach", "--service", "api")
	projectDir := filepath.Join(dir, "reach")
	addCorpusForgePkgReplace(t, projectDir)
	runCmd(t, projectDir, forgeBin, "scaffold", "worker", "tick")
	runCmd(t, projectDir, forgeBin, "scaffold", "worker", "reaper")

	reaperPath := filepath.Join(projectDir, "internal", "workers", "reaper", "worker.go")
	mustReplaceInFile(t, reaperPath, "\tLogger *slog.Logger\n", "\tLogger  *slog.Logger\n\tSweeper Sweeper\n")
	appendFileE2E(t, reaperPath, "\n// Sweeper is what reaper sweeps with; no provider makes one yet.\ntype Sweeper interface{ Sweep(ctx context.Context) error }\n")

	writeFileE2E(t, filepath.Join(projectDir, "internal", "app", "roots.go"), `package app

import "github.com/test/reach/internal/workers/tick"

// Server is what `+"`reach server`"+` runs: the API.
//
//forge:root server
type Server struct {
	API *APIListener
}

// Worker is what `+"`reach worker`"+` runs: the tick worker alone.
//
//forge:root worker
type Worker struct {
	Tick *tick.Worker
}
`)

	// Generate: reaper is reached by no root, so its unfilled Sweeper is no
	// generate error.
	out, err := runCmdAllowFail(t, projectDir, forgeBin, "generate")
	if err != nil {
		t.Fatalf("generate must succeed while the node with an unfilled Dep is reached by no root: %v\n%s", err, out)
	}
	if !strings.Contains(out, "left out of the composition graph") || !strings.Contains(out, "Reaper") {
		t.Errorf("generate does not say which nodes it left out:\n%s", out)
	}
	graphGen := readFileE2E(t, filepath.Join(projectDir, "internal", "app", "graph_gen.go"))
	if strings.Contains(graphGen, "Reaper()") || strings.Contains(graphGen, "workers/reaper") {
		t.Errorf("graph_gen.go holds the left-out reaper:\n%s", graphGen)
	}
	if !strings.Contains(graphGen, "func (g *Graph) Tick()") {
		t.Errorf("graph_gen.go lost the reached tick worker:\n%s", graphGen)
	}
	// The scaffold wrote newReaper before Sweeper existed; it is the
	// author's now, so it stays — and forge writes no key it cannot fill.
	if compose := readFileE2E(t, filepath.Join(projectDir, "internal", "app", "compose.go")); strings.Contains(compose, "Sweeper:") {
		t.Errorf("compose.go fills a Dep no provider makes:\n%s", compose)
	}
	runCmd(t, projectDir, "go", "build", "./...")

	// Lint: an error once the rule gates … forge-graph-unreachable is
	// warn-first (#1272: it gates from the release lint_rollout_table.go
	// names), so a fresh scaffold pinned below that release only warns. The
	// rollout has its own test (TestE2ENewRuleWarnsUntilPinReachesGatesFrom);
	// this one pins what the rule finds, so it gates the rule explicitly
	// rather than depending on which release the tree is.
	forgeYAML := filepath.Join(projectDir, "forge.yaml")
	cfg := readFileE2E(t, forgeYAML)
	if strings.Contains(cfg, "\nlint:") {
		t.Fatalf("the scaffolded forge.yaml has a lint block already; extend the fixture:\n%s", cfg)
	}
	appendFileE2E(t, forgeYAML, "\nlint:\n  rules:\n    forge-graph-unreachable: error\n")
	out, err = runCmdAllowFail(t, projectDir, forgeBin, "lint", "--graph")
	if err == nil {
		t.Fatalf("forge lint --graph passed with a component no root reaches:\n%s", out)
	}
	if !strings.Contains(out, "✗ internal/workers/reaper [forge-graph-unreachable]") || !strings.Contains(out, "no node produces Deps.Sweeper") {
		t.Errorf("lint does not report reaper as unreachable and unconstructible:\n%s", out)
	}

	// … and a warning under lint.rules.
	mustReplaceInFile(t, forgeYAML, "    forge-graph-unreachable: error\n", "    forge-graph-unreachable: warn\n")
	out, err = runCmdAllowFail(t, projectDir, forgeBin, "lint", "--graph")
	if err != nil {
		t.Fatalf("forge lint --graph failed with forge-graph-unreachable set to warn: %v\n%s", err, out)
	}
	if !strings.Contains(out, "⚠ internal/workers/reaper [forge-graph-unreachable]") {
		t.Errorf("the re-graded finding is not reported as a warning:\n%s", out)
	}

	// A reached node with an unfilled Dep still fails generate.
	svcPath := filepath.Join(projectDir, "internal", "handlers", "api", "service.go")
	mustReplaceInFile(t, svcPath, "type Deps struct {\n\tLogger *slog.Logger\n", "type Deps struct {\n\tLogger   *slog.Logger\n\tPayments Payments\n")
	appendFileE2E(t, svcPath, "\n// Payments charges cards; no provider makes one.\ntype Payments interface{ Charge() error }\n")
	out, err = runCmdAllowFail(t, projectDir, forgeBin, "generate")
	if err == nil {
		t.Fatalf("generate succeeded with a reached service's Dep unfilled:\n%s", out)
	}
	if !strings.Contains(out, "Deps.Payments (Payments) has no provider") {
		t.Errorf("generate does not name the reached node's unfilled Dep:\n%s", out)
	}
	if strings.Contains(out, "Reaper.Deps") {
		t.Errorf("generate's error names the unreached reaper's Deps:\n%s", out)
	}
}

// appendFileE2E appends content to path.
func appendFileE2E(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}
