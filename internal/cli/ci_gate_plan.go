package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cigate"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/goexec"
)

// maxWhyLines bounds the per-package why lines a gate run prints; past it the
// gate names --plan instead, which always prints them all.
const maxWhyLines = 30

// runGatePlan is `forge ci gate --plan [--files a,b]`: the affected-mode
// decision — the test packages, the build set, why each package runs, and
// what each changed file maps to — without running a check. --files judges
// the named files instead of the diff (a what-if); --json prints the
// decision as the document's `affected` object.
func runGatePlan(cmd *cobra.Command, flags gateFlags) error {
	if len(flags.files) == 0 && cmd.Flags().Changed("files") {
		return cliutil.UserErr("forge ci gate", "--files names no file", "", "pass a comma-separated list: --files a.go,b/c.md")
	}
	root, err := projectRoot()
	if err != nil {
		return err
	}
	store, err := loadProjectStore()
	if err != nil {
		return err
	}
	cfg := store.Config()
	base := flags.base
	if base == "" {
		base = os.Getenv(cigate.BaseEnv)
	}
	force := ""
	switch {
	case flags.full:
		force = "--full"
	case truthy(os.Getenv(cigate.FullEnv)) && len(flags.files) == 0:
		force = cigate.FullEnv + "=1"
	}
	a := cigate.ComputeAffected(cmd.Context(), planAffectedInput(cfg.CI.Gate, root, base, force, goexec.CheckEnv(cigate.GateEnv(cfg)...), flags.files))
	// The rows the diff decides beyond the unit lane: paths_go_closure
	// hits and lockfile-<frontend> rows, so --plan reports them too.
	ciData := generator.CIWorkflowDataFor(root, cfg, generator.CIInputs{Frontends: ciFrontends(root, cfg)})
	checks, perr := cigate.Plan(cigate.PlanInput{Root: root, CI: ciData, Cfg: cfg, Warn: func(string) {}})
	if perr == nil {
		env := goexec.CheckEnv(cigate.GateEnv(cfg)...)
		a = cigate.ResolveGoClosures(cmd.Context(), root, env, checks, a, nil)
		a = cigate.WithLockfileReport(a, cigate.WithLockfileChecks(checks, a, root, ciData.Frontends))
	}
	doc := a.Document()
	// The path-gated rows (paths, paths_go_closure): which the gate would
	// run for these changes, and why not the others.
	if perr == nil {
		doc.Checks = planChecks(checks, a)
	}
	if flags.json {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(doc)
	}
	writeAffectedPlan(cmd.OutOrStdout(), a)
	return nil
}

// planAffectedInput is the affected-mode input --plan computes from: every
// ci.gate key the gate run reads (ignore, rules, reach, readers, modules),
// so the plan never selects differently from the run it predicts.
func planAffectedInput(g config.CIGateConfig, root, base, force string, env, files []string) cigate.AffectedInput {
	return cigate.AffectedInput{Root: root, BaseRef: base, Env: env, Force: force, Files: files,
		Ignore: g.AffectedIgnore, Rules: g.AffectedRules, Reach: g.AffectedReach, Readers: g.AffectedReaders, Modules: g.AffectedModules}
}

// planChecks is each path-gated row (paths, paths_go_closure) and whether
// the gate would run it for a's changes; a note says why one would not.
func planChecks(checks []cigate.Check, a cigate.Affected) []cigate.PlanCheck {
	full := a.Full && len(a.Changed) == 0
	var out []cigate.PlanCheck
	for _, c := range cigate.GateByPaths(checks, a) {
		if len(c.Paths) == 0 && len(c.GoClosure) == 0 {
			continue
		}
		pc := cigate.PlanCheck{Name: c.Name, Runs: c.NotApplicable == "" || full, Note: c.Note}
		if c.NotApplicable != "" {
			pc.Note = c.NotApplicable
		}
		out = append(out, pc)
	}
	return out
}

// writeAffectedPlan prints the decision for a person.
func writeAffectedPlan(out io.Writer, a cigate.Affected) {
	fmt.Fprintln(out, a.Line())
	for _, s := range a.Selections {
		fmt.Fprintln(out, "  "+s)
	}
	if len(a.Why) > 0 {
		fmt.Fprintln(out, "why each package runs:")
		for _, w := range a.WhyLines() {
			fmt.Fprintln(out, "  "+w)
		}
	}
	if len(a.Build) > 0 {
		fmt.Fprintf(out, "build set: %d package(s) whose build contains a change\n", len(a.Build))
		for _, p := range a.Build {
			fmt.Fprintln(out, "  "+p)
		}
	}
	for _, m := range a.Modules {
		what := strings.Join(m.Packages, ", ")
		if m.Full {
			what = "whole suite (" + m.Reason + ")"
		}
		fmt.Fprintf(out, "nested module %s (%s): %s\n", m.Dir, cigate.ModuleTestName(m.Dir), what)
	}
	for _, p := range a.Deferred {
		fmt.Fprintln(out, "deferred to the full gate: "+p)
	}
	for _, c := range a.LockfileChecks {
		fmt.Fprintln(out, "lockfile row: "+c)
	}
	for _, name := range slices.Sorted(maps.Keys(a.ClosureHits)) {
		fmt.Fprintf(out, "go closure of %s reached by: %s\n", name, strings.Join(a.ClosureHits[name], ", "))
	}
	if len(a.Files) > 0 {
		fmt.Fprintln(out, "changed files:")
		for _, f := range a.Files {
			fmt.Fprintf(out, "  %s → %s\n", f.Path, f.Note)
		}
	}
}
