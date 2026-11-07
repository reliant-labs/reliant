package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsdash"
	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/obsquery"
	"github.com/reliant-labs/forge/pkg/release"
)

// obsDashboardsCheckOpts are `forge obs dashboards check`'s flags.
type obsDashboardsCheckOpts struct {
	outputDir string
	// against is the env whose telemetry the metrics panels are checked
	// against; "" checks the files alone.
	against string
	backend string
	since   time.Duration
	strict  bool
}

// newObsDashboardsCmd is `forge obs dashboards check`: the validation a
// release cut runs, on its own, so a dashboard is fixed before it blocks one.
func newObsDashboardsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dashboards",
		Short: "Work with the dashboards a release carries",
	}
	var o obsDashboardsCheckOpts
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

Errors carry the file, line and column of the offending value.

--against <env> also checks every metrics panel against that env's telemetry over
--since (default 24h): a series whose metric is stored as a different kind than
its metricType fails the check (the panel reads the wrong table, or refuses its
aggregation), and a series whose metric has no data at all is reported. No data
is often a misspelt name or one the collector drops before storage, and sometimes
a counter that only exists once something fails; --strict fails on it too.

  ` + Name() + ` obs dashboards check --against prod
  ` + Name() + ` obs dashboards check --against dev --since 6h --strict`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runObsDashboardsCheck(cmd.Context(), defaultObsDeps(cmd), obsout.NewSanitizingWriter(cmd.OutOrStdout()), o)
		},
	}
	f := check.Flags()
	f.StringVarP(&o.outputDir, "output", "o", "bin", "build output directory holding observability/metrics-schema.json")
	f.StringVar(&o.against, "against", "", "also check every metrics panel against this env's telemetry")
	f.StringVar(&o.backend, "backend", "", "with --against: local | cloud (default: from the env's declaration)")
	f.DurationVar(&o.since, "since", 24*time.Hour, "with --against: the window a metric must have data in")
	f.BoolVar(&o.strict, "strict", false, "with --against: a metric with no data in the window fails the check too")
	cmd.AddCommand(check, newObsDashboardsAuditCmd(), newObsDashboardsAddPanelCmd())
	return cmdutil.StrictGroup(cmd)
}

func runObsDashboardsCheck(ctx context.Context, d obsDeps, out io.Writer, o obsDashboardsCheckOpts) error {
	set, err := collectObservabilityFiles(projectDirForKCL(), o.outputDir)
	if err != nil {
		return err
	}
	for _, db := range set.Dashboards {
		fmt.Fprintf(out, "  %-4s %s  (%d panel(s))\n", db.Source, db.Path, len(db.Dashboard.Panels))
	}
	if len(set.MetricsSchema) > 0 {
		fmt.Fprintf(out, "       %s\n", release.BundleMetricsSchemaPath)
	}
	for _, n := range set.Notes {
		fmt.Fprintf(out, "  note: %s\n", n)
	}
	fmt.Fprintf(out, "%d dashboard(s) would ship\n", len(set.Dashboards))
	if o.against == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rng, err := resolveRange(d.Now(), o.since, "", "")
	if err != nil {
		return err
	}
	c := obsCommon{backend: o.backend}
	b, _, err := c.open(ctx, d, o.against, "dashboards check")
	if err != nil {
		return err
	}
	lookup := func(name string) ([]obsbackend.MetricInfo, error) {
		res, err := b.MetricsList(ctx, obsbackend.MetricsListRequest{Narrow: obsbackend.Narrow{Range: rng}, NameContains: name, Limit: obsquery.ListMetricsMax})
		if err != nil {
			return nil, err
		}
		var exact []obsbackend.MetricInfo
		for _, m := range res.Metrics {
			if m.Name == name && m.Seen {
				exact = append(exact, m)
			}
		}
		return exact, nil
	}
	problems, absent, err := dashboardMetricFindings(set, lookup)
	if err != nil {
		return err
	}
	window := fmt.Sprintf("in %s over the last %s", o.against, o.since)
	for _, a := range absent {
		fmt.Fprintf(out, "  no data: %s has no data %s\n", a, window)
	}
	for _, p := range problems {
		fmt.Fprintf(out, "  wrong kind: %s\n", p)
	}
	failed := len(problems)
	if o.strict {
		failed += len(absent)
	}
	if failed > 0 {
		return obsbackend.Wrong("%d dashboard metric series do not match %s's telemetry", failed, o.against)
	}
	fmt.Fprintf(out, "every metrics panel matches %s's telemetry", o.against)
	if len(absent) > 0 {
		fmt.Fprintf(out, " (%d series with no data; --strict fails on them)", len(absent))
	}
	fmt.Fprintln(out)
	return nil
}

// dashboardMetricFindings checks every metrics panel series of the set against
// lookup, which returns the stored kinds of one metric with data (none: no
// data). problems are series whose declared metricType is not a stored kind;
// absent are series whose metric has no data. Each names the file and panel.
func dashboardMetricFindings(set obsdash.BundleSet, lookup func(name string) ([]obsbackend.MetricInfo, error)) (problems, absent []string, err error) {
	seen := map[string][]obsbackend.MetricInfo{}
	for _, db := range set.Dashboards {
		for _, p := range db.Dashboard.Panels {
			if p.Signal != dashspec.Metrics {
				continue
			}
			for _, s := range p.Series {
				if s.Field == "" {
					continue
				}
				infos, ok := seen[s.Field]
				if !ok {
					if infos, err = lookup(s.Field); err != nil {
						return nil, nil, err
					}
					seen[s.Field] = infos
				}
				where := fmt.Sprintf("%s panel %q: %s", db.Path, p.ID, s.Field)
				if len(infos) == 0 {
					absent = append(absent, where)
					continue
				}
				var kinds []string
				for _, m := range infos {
					kinds = append(kinds, m.Type)
				}
				sort.Strings(kinds)
				if s.MetricType != "" && !slices.Contains(kinds, s.MetricType) {
					problems = append(problems, fmt.Sprintf("%s is declared %s but stored as %s", where, s.MetricType, strings.Join(kinds, " and ")))
				}
			}
		}
	}
	return problems, absent, nil
}
