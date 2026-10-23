package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/pkg/release"
)

// newObsDashboardsCmd is `forge obs dashboards check`: the validation a
// release cut runs, on its own, so a dashboard is fixed before it blocks one.
func newObsDashboardsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dashboards",
		Short: "Work with the dashboards a release carries",
	}
	var outputDir string
	check := &cobra.Command{
		Use:   "check",
		Short: "Validate the dashboards a release bundle would carry",
		Long: `Validate every dashboard the next release bundle would carry, exactly as a release
cut does, and print what would ship.

Generated dashboards come from the spec in deploy/observability/spec/ (and the
Custom metrics dashboard, when bin/observability/metrics-schema.json exists). A file
you own in deploy/observability/dashboards/ is a renderer-neutral dashboard object
(the shape of the spec's dashboards, with a "panels" array); one named like a
generated dashboard (shop-overview.json for shop-overview_gen.json) replaces it. A
HyperDX-format file (with "tiles") is for the local HyperDX only and is not shipped.

Errors carry the file, line and column of the offending value.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runObsDashboardsCheck(cmd.OutOrStdout(), outputDir)
		},
	}
	check.Flags().StringVarP(&outputDir, "output", "o", "bin", "build output directory holding observability/metrics-schema.json")
	cmd.AddCommand(check)
	return cmdutil.StrictGroup(cmd)
}

func runObsDashboardsCheck(out io.Writer, outputDir string) error {
	set, err := collectObservabilityFiles(projectDirForKCL(), outputDir)
	if err != nil {
		return err
	}
	for _, d := range set.Dashboards {
		fmt.Fprintf(out, "  %-4s %s  (%d panel(s))\n", d.Source, d.Path, len(d.Dashboard.Panels))
	}
	if len(set.MetricsSchema) > 0 {
		fmt.Fprintf(out, "       %s\n", release.BundleMetricsSchemaPath)
	}
	for _, n := range set.Notes {
		fmt.Fprintf(out, "  note: %s\n", n)
	}
	fmt.Fprintf(out, "%d dashboard(s) would ship\n", len(set.Dashboards))
	return nil
}
