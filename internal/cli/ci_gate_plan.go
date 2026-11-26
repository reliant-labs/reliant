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
	a := cigate.ComputeAffected(cmd.Context(), cigate.AffectedInput{
		Root: root, BaseRef: base, Env: goexec.CheckEnv(cigate.GateEnv(cfg)...), Force: force,
		Ignore: cfg.CI.Gate.AffectedIgnore, Rules: cfg.CI.Gate.AffectedRules, Reach: cfg.CI.Gate.AffectedReach, Files: flags.files,
	})
	// The rows the diff decides beyond the unit lane: paths_go_closure
	// hits and lockfile-<frontend> rows, so --plan reports them too.
	ciData := generator.CIWorkflowDataFor(root, cfg, generator.CIInputs{Frontends: ciFrontends(root, cfg)})
	if checks, perr := cigate.Plan(cigate.PlanInput{Root: root, CI: ciData, Cfg: cfg, Warn: func(string) {}}); perr == nil {
		env := goexec.CheckEnv(cigate.GateEnv(cfg)...)
		a = cigate.ResolveGoClosures(cmd.Context(), root, env, checks, a, nil)
		a = cigate.WithLockfileReport(a, cigate.WithLockfileChecks(checks, a, root, ciData.Frontends))
	}
	if flags.json {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(a.Document())
	}
	writeAffectedPlan(cmd.OutOrStdout(), a)
	return nil
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
