package cli

// `forge obs metrics declared` — print the metrics schema (forge.metrics/v1) of a
// service: every instrument it declares, with unit, attributes and stability.
//
// The schema has one author, the declared metrics registry linked into the
// service binary (pkg/observe/metrics), and three ways to reach it. This
// command is the third: it reads the document from a built binary (running its
// `metrics schema --json`) or from a running service (GET
// /debug/metrics/schema on the pprof listener), and prints it in the same
// shape either way, so a script can use whichever the machine has.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsdash"
	"github.com/reliant-labs/forge/pkg/observe/metrics"
)

type obsMetricsOpts struct {
	binary, url string
	asJSON      bool
	dashboard   bool
	timeout     time.Duration
}

func newObsMetricsDeclaredCmd() *cobra.Command {
	var o obsMetricsOpts
	cmd := &cobra.Command{
		Use:   "declared",
		Short: "Print the metrics a service declares: name, type, unit, attributes, stability",
		Long: `Print the declared metrics schema (forge.metrics/v1) of a service.

Every metric a forge service emits is declared in a typed registry, so the full
list — with each one's type, UCUM unit, attribute keys and stability — can be
read without running a query. The schema comes from one of two places:

  forge obs metrics declared                          # the project's built binary (bin/<name>)
  forge obs metrics declared --binary ./bin/api       # a specific binary: runs 'metrics schema --json'
  forge obs metrics declared --url http://localhost:6060   # a running service's pprof listener

The same document is served by 'GET /debug/metrics/schema', printed by
'<bin> metrics schema --json', and written by 'forge build' to
bin/observability/metrics-schema.json, which a release carries as
observability/metrics-schema.json.

  forge obs metrics declared --dashboard > deploy/observability/dashboards/<name>-custom-metrics.json

prints the HyperDX "Custom metrics" dashboard drawn from the schema (counters as
a rate, histograms as p50/p95/p99, gauges as the last value, grouped by their
enumerated attributes). It writes nothing itself: commit the file to own it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runObsMetrics(cmd.Context(), cmd.OutOrStdout(), o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.binary, "binary", "", "a built service binary to run (default: the project's bin/<name>)")
	f.StringVar(&o.url, "url", "", "a running service's debug listener, e.g. http://localhost:6060")
	f.BoolVar(&o.asJSON, "json", false, "print the forge.metrics/v1 JSON document")
	f.BoolVar(&o.dashboard, "dashboard", false, "print the HyperDX Custom metrics dashboard drawn from the schema instead")
	f.DurationVar(&o.timeout, "timeout", 30*time.Second, "how long to wait for the binary or the service")
	return cmd
}

func runObsMetrics(ctx context.Context, out io.Writer, o obsMetricsOpts) error {
	if o.binary != "" && o.url != "" {
		return fmt.Errorf("--binary and --url are two sources for one schema; pass one")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()

	var raw []byte
	var err error
	switch {
	case o.url != "":
		raw, err = fetchMetricsSchema(ctx, o.url)
	default:
		bin := o.binary
		if bin == "" {
			if bin, err = defaultServiceBinary(); err != nil {
				return err
			}
		}
		raw, err = binaryMetricsSchema(ctx, bin)
	}
	if err != nil {
		return err
	}
	schema, err := metrics.ParseSchema(raw)
	if err != nil {
		return err
	}
	switch {
	case o.dashboard:
		return writeCustomMetricsDashboard(out, schema)
	case o.asJSON:
		return schema.WriteJSON(out)
	}
	return schema.WriteTable(out)
}

// writeCustomMetricsDashboard prints the HyperDX dashboard JSON for the
// metrics a schema declares, named for its service.
func writeCustomMetricsDashboard(out io.Writer, schema metrics.Schema) error {
	d, ok := obsdash.BuildCustomMetrics(schema.Service, schema)
	if !ok {
		return fmt.Errorf("%s declares no metrics of its own: add some to internal/metrics/metrics.go, rebuild, and run this again", schema.Service)
	}
	files, err := obsdash.RenderHyperDX(obsdash.Spec{Version: obsdash.SpecVersion, Project: schema.Service, Dashboards: []obsdash.Dashboard{d}})
	if err != nil {
		return err
	}
	_, err = out.Write(files[0].Content)
	return err
}

// binaryMetricsSchema runs `<bin> metrics schema --json`. The binary needs no
// configuration and starts nothing; the environment is cleared of nothing, but
// the command reads none of it.
func binaryMetricsSchema(ctx context.Context, bin string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, "metrics", "schema", "--json")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("run %s metrics schema --json: %s\n"+
			"     a binary built before the declared-metrics registry has no such command; rebuild it (forge build) after `forge generate` adds cmd/<name>/cmd/metrics.go", bin, msg)
	}
	return b, nil
}

// fetchMetricsSchema reads GET /debug/metrics/schema from base, which may be
// the listener's root URL or already the full schema URL.
func fetchMetricsSchema(ctx context.Context, base string) ([]byte, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("--url %q: want a URL such as http://localhost:6060", base)
	}
	if !strings.HasSuffix(u.Path, metrics.SchemaPath) {
		u.Path = strings.TrimRight(u.Path, "/") + metrics.SchemaPath
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w\n     the schema is served on the pprof listener (PPROF_ADDR, default 127.0.0.1:6060); port-forward it first for a deployed pod", u, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: %s (a service built before the declared-metrics registry does not serve this path)", u, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// defaultServiceBinary is the project's built binary, bin/<project>.
func defaultServiceBinary() (string, error) {
	dir, err := requireProjectDir("obs metrics")
	if err != nil {
		return "", err
	}
	name := filepath.Base(dir)
	if cfg, cerr := loadProjectConfigFrom(filepath.Join(dir, defaultProjectConfigFile)); cerr == nil && cfg.Name != "" {
		name = cfg.Name
	}
	bin := filepath.Join(dir, "bin", name)
	if _, err := os.Stat(bin); err != nil {
		return "", fmt.Errorf("no built binary at %s — run `forge build`, or point at one with --binary, or at a running service with --url", bin)
	}
	return bin, nil
}
