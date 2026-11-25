package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cigate"
	"github.com/reliant-labs/forge/internal/cliutil"
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
		Ignore: cfg.CI.Gate.AffectedIgnore, Rules: cfg.CI.Gate.AffectedRules, Files: flags.files,
	})
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
	if len(a.Files) > 0 {
		fmt.Fprintln(out, "changed files:")
		for _, f := range a.Files {
			fmt.Fprintf(out, "  %s → %s\n", f.Path, f.Note)
		}
	}
}
