//go:build e2e

package cli

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EAddVerbsProduceABuildableTree is the guard the `forge scaffold`
// surface never had: it scaffolds a project, runs EVERY add verb that grows the
// component graph, and then BUILDS the result and re-runs `forge generate`.
//
// Three shipped bugs made this exact sequence fail, and all three survived
// because the tests around them asserted on template STRINGS instead of on a
// project that compiles:
//
//  1. `scaffold worker --kind cron` emitted an import of github.com/robfig/cron/v3
//     that nothing put in the project's go.mod. `forge generate` failed its own
//     validate step and rolled back.
//  2. `scaffold worker` / `scaffold operator` scaffolded cmd/<bin>/cmd/{workers,operators}/
//     <name>.go calling c.Worker<X>() / c.Operator<X>() — accessors that lived in
//     the scaffold-once internal/app/lifecycle.go and were never written.
//     `c.WorkerNightly undefined`. (A new project composes with the graph
//     now: each worker and operator is a node the default root reaches.)
//  3. `scaffold operator` recorded nothing an `scaffold crd --operator <name>` could find,
//     and `project new --service <svc>` recorded nothing an
//     `scaffold webhook --service <svc>` could find, so both dependent verbs were
//     unreachable on a fresh project.
//
// ONE test does the whole path rather than one per verb: the expensive parts
// (scaffold, generate, module resolution, compile) are per-project, not
// per-verb, so a single fixture that exercises every verb costs roughly what
// one would.
func TestE2EAddVerbsProduceABuildableTree(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "addverbs",
		"--mod", "example.com/addverbs",
		"--service", "widget",
	)
	projectDir := filepath.Join(dir, "addverbs")
	addCorpusForgePkgReplace(t, projectDir)

	// ── the add verbs ────────────────────────────────────────────────
	runCmd(t, projectDir, forgeBin, "scaffold", "worker", "nightly",
		"--kind", "cron", "--schedule", "0 3 * * *")
	runCmd(t, projectDir, forgeBin, "scaffold", "worker", "mailer")
	runCmd(t, projectDir, forgeBin, "scaffold", "operator", "fleet")
	// Depends on `scaffold operator` having recorded the operator (bug 3).
	runCmd(t, projectDir, forgeBin, "scaffold", "crd", "Gadget", "--operator", "fleet")
	// Depends on `project new --service` having recorded the service (bug 3).
	runCmd(t, projectDir, forgeBin, "scaffold", "webhook", "stripe", "--service", "widget")

	// ── bug 1: the cron module is a real dependency ──────────────────
	if gomod := readFileE2E(t, filepath.Join(projectDir, "go.mod")); !strings.Contains(gomod, "github.com/robfig/cron/v3") {
		t.Errorf("go.mod does not require robfig/cron after `scaffold worker --kind cron`:\n%s", gomod)
	}

	// ── bug 2: every scaffolded worker and operator joins the graph ──
	// (a new project composes with the composition graph: no per-component
	// subcommand, so `server` must reach each one through the default root).
	graph := readFileE2E(t, filepath.Join(projectDir, "internal", "app", "graph_gen.go"))
	for _, want := range []string{
		"func (g *Graph) Nightly() ",
		"func (g *Graph) Mailer() ",
		"func (g *Graph) Fleet() ",
		"func (g *Graph) Operators() *Operators",
	} {
		if !strings.Contains(graph, want) {
			t.Errorf("internal/app/graph_gen.go missing %q:\n%s", want, graph)
		}
	}
	compose := readFileE2E(t, filepath.Join(projectDir, "internal", "app", "compose.go"))
	for _, want := range []string{"func (g *Graph) newNightly()", "func (g *Graph) newMailer()", "func (g *Graph) newFleet()", "func (g *Graph) newOperators()"} {
		if !strings.Contains(compose, want) {
			t.Errorf("internal/app/compose.go missing %q — graph_gen.go's accessor calls it:\n%s", want, compose)
		}
	}

	// ── bug 3: the dependent verbs' output landed ────────────────────
	assertPathExistsE2E(t, filepath.Join(projectDir, "internal", "operators", "fleet", "gadget_controller.go"))
	assertPathExistsE2E(t, filepath.Join(projectDir, "internal", "handlers", "widget", "webhook_stripe.go"))

	// Every component is now visible to the shared name-uniqueness gate,
	// across kinds — it matched an empty list before and never fired.
	assertAddVerbRejectsDuplicate(t, forgeBin, projectDir, "worker", "nightly")
	assertAddVerbRejectsDuplicate(t, forgeBin, projectDir, "binary", "fleet")

	// ── the whole point: it compiles, and generate stays happy ───────
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./...")
	// Twice: the webhook route emitter writes webhook_routes_gen.go into the
	// directory it scans for webhook_*.go, so run 2 is where a phantom
	// "routes_gen" webhook would surface.
	runCmd(t, projectDir, forgeBin, "generate")
	runCmd(t, projectDir, forgeBin, "generate")
	runCmd(t, projectDir, "go", "build", "./...")
}

// assertAddVerbRejectsDuplicate checks that `forge scaffold <kind> <name>`
// fails when <name> is already taken by ANY component kind.
func assertAddVerbRejectsDuplicate(t *testing.T, forgeBin, projectDir, kind, name string) {
	t.Helper()
	cmd := exec.Command(forgeBin, "scaffold", kind, name)
	cmd.Dir = projectDir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Errorf("`forge scaffold %s %s` succeeded on a name already in use:\n%s", kind, name, out)
		return
	}
	if !strings.Contains(string(out), "already exists in the project") {
		t.Errorf("`forge scaffold %s %s` failed for the wrong reason:\n%s", kind, name, out)
	}
}
