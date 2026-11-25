package cli

// `forge obs profile` — export an aggregated continuous profile from the
// local ClickStack's ClickHouse as pprof, speedscope JSON or collapsed stacks.
// `forge obs profile <env>` reads the env named, hosted included
// (obs_profile_env.go).
//
// HyperDX cannot draw flamegraphs, so for local development the profile is
// exported and opened in a tool that can: `go tool pprof -http=: profile.pb.gz`
// or speedscope. The query and the writers live in pkg/profilequery so a future
// console view reuses them; this file is only the CLI around them.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/clickstack"
	"github.com/reliant-labs/forge/pkg/profilequery"
)

func newObsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "obs",
		Short: "Ask what happened: logs, traces, endpoints, metrics, SQL, profiles and alerts",
		Long: `Ask what happened: logs, traces, endpoints, metrics, SQL, profiles and alerts.

Every verb reads, except these, which change state:
  obs init                                    registers envs and mints ingest tokens
  obs tokens create|rotate|revoke             an env's telemetry ingest tokens
  obs receivers create|delete|test            where the org's alerts are sent
  obs issue resolve|ignore|unresolve|assign|comment   an issue's triage state
  obs dashboards add-panel                    writes a dashboard file in this checkout

What a control-plane credential needs (a person's ` + "`forge login`" + ` is elevated as
needed; a CI or agent token must carry the scope):
  reads                    obs:read
  issue triage             obs:triage (obs:read does not imply it)
  init, tokens, receivers  a person who is a member of the org holding the
                           obs:write permission; never a machine credential
                           for init and tokens`,
	}
	cmd.AddCommand(newObsProfileCmd())
	cmd.AddCommand(newObsAlertsCmd())
	cmd.AddCommand(newObsLogsCmd(), newObsTracesCmd(), newObsTraceCmd(), newObsEndpointsCmd(), newObsOpsCmd(), newObsBlastRadiusCmd(), newObsQueryCmd())
	cmd.AddCommand(newObsIssuesCmd(), newObsIssueCmd())
	cmd.AddCommand(newObsSessionsCmd(), newObsSessionCmd(), newObsUsersCmd())
	cmd.AddCommand(newObsCheckCmd(), newObsChangesCmd())
	cmd.AddCommand(newObsStatusCmd(), newObsWorkloadsCmd(), newObsTelemetryCmd())
	cmd.AddCommand(newObsMetricsGroupCmd())
	cmd.AddCommand(newObsDashboardsCmd())
	cmd.AddCommand(newObsInitCmd(), newObsTokensCmd(), newObsReceiversCmd())
	cmd.PersistentFlags().StringVar(&obsProjectFlag, "project", "",
		"the forge project name, for a repo with no forge.yaml (default: forge.yaml `name`)")
	cmd.PersistentFlags().StringVar(&obsControlPlaneFlag, "control-plane", "",
		"the control plane of a monitoring-only env (default: forge.yaml monitoring.control_plane, else Reliant's)")
	return cmdutil.StrictGroup(cmd)
}

type obsProfileOpts struct {
	service, sampleType, env string
	since                    time.Duration
	from, to                 string
	trace, span              string
	format, output           string
	printSQL, listServices   bool

	// queryEnv is the [env] the command was given ("" with none): it reads
	// that env, and --sql prints the statement for `forge obs query <env>`.
	queryEnv string
}

func newObsProfileCmd() *cobra.Command {
	var o obsProfileOpts
	cmd := &cobra.Command{
		Use:   "profile [env]",
		Short: "Export an aggregated continuous profile as pprof, speedscope or collapsed stacks",
		Long: `Export the continuous profile an env collected, for a service over a time
range or for one trace.

Apps push a 5s CPU profile every 5 minutes and a live-heap snapshot every 2
hours (pkg/observe, on by default whenever an OTLP endpoint is set). They land
in ClickHouse; HyperDX cannot draw flamegraphs, so this exports them:

  forge obs profile --service api --since 30m -o api.pb.gz
  go tool pprof -http=: api.pb.gz                  # flamegraph, top, source

  forge obs profile --service api --format speedscope -o api.speedscope.json
  #   open api.speedscope.json at https://www.speedscope.app

  forge obs profile --trace 0af7651916cd43dd8448eb211c80319c   # CPU burned inside one trace
  forge obs profile --service api --format folded | flamegraph.pl > api.svg

Which env. With no [env] it reads the local ClickStack, and --env filters its
samples by deployment.environment.name. With an [env] it reads that env where
its telemetry goes, as every other obs verb does: a local env's ClickStack
(only the env's samples), or a hosted env's control plane:

  forge obs profile prod --service api --since 6h -o api.pb.gz
  forge obs profile prod --service api --type inuse_space --format speedscope -o heap.json

A hosted profile is the control plane's flamegraph (the console's): the
heaviest 5000 stacks (a note says when there were more), no --span, and a
credential that is not bound to one environment.

Sample types: cpu (nanoseconds) and inuse_space / inuse_objects (live heap) by
default; alloc_space / alloc_objects and goroutine only from a workload whose
profiling block turns them on (see forge skill load observability/profiles).
CPU and the other accumulated types are summed over the range. The live heap
and the goroutines are gauges: each instance's newest snapshot in the range,
or, when the range holds none, the newest before it, so a range shorter than
the heap interval still shows the heap as last measured. Raw samples (so --trace and the heap) are kept 24h; the
per-minute aggregate behind --since is kept 30 days.

--sql prints the query instead of running it. With no [env], with its
parameters, for clickhouse-client against the ClickStack. With an [env], in
` + "`forge obs query`" + `'s parameters over the unqualified profile tables
(otel_profiles, profile_samples_1m, profile_stacks), so it runs against that
env, hosted or local, and the command to run it is printed on stderr:

  forge obs profile prod --service api --sql > profile.sql
  forge obs query prod --sql-file profile.sql --service api --since 1h

--trace finds samples only when a CPU capture (5s of every 5 minutes) overlapped
the trace, so most traces have none. To find the traces that do:

  forge obs query dev --sql "SELECT TraceId, ServiceName, count() AS n FROM otel_profiles
    WHERE TraceId != '' AND Timestamp > now() - INTERVAL 1 HOUR GROUP BY TraceId, ServiceName ORDER BY n DESC LIMIT 20"

With no [env], needs the local ClickStack running (` + "`forge env up`" + `).`,
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return runObsProfile(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), o, defaultProfileRunner)
			}
			// The profile is written to the command's own writers: a pprof is
			// binary, and the obs deps' writers sanitize text for a terminal.
			return runObsProfileEnv(cmd.Context(), defaultObsDeps(cmd), cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], o, pgoLocalRunner)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.service, "service", "", "service.name to export (default: the only service that sent profiles)")
	f.StringVar(&o.sampleType, "type", "cpu", "sample type: cpu, alloc_space, inuse_space, alloc_objects, inuse_objects, goroutine")
	f.StringVar(&o.env, "env", "", "only samples from this deployment.environment.name of the local ClickStack read with no [env]. A FILTER, not the backend: to read a hosted env, name it as [env]")
	obsDurationVar(f, &o.since, "since", time.Hour, "how far back to read (ignored with --from)")
	f.StringVar(&o.from, "from", "", "range start, RFC 3339")
	f.StringVar(&o.to, "to", "", "range end, RFC 3339 (default now)")
	f.StringVar(&o.trace, "trace", "", "export only samples linked to this trace id (raw tier, last 24h)")
	f.StringVar(&o.span, "span", "", "narrow --trace to one span id (not on a hosted env)")
	f.StringVar(&o.format, "format", "pprof", "pprof | speedscope | folded")
	f.StringVarP(&o.output, "output", "o", "", "write to this file instead of stdout")
	f.BoolVar(&o.printSQL, "sql", false, "print the query and its parameters instead of running it (with [env]: for forge obs query <env> --sql-file)")
	f.BoolVar(&o.listServices, "list", false, "list the services that sent profiles in the range")
	return cmd
}

// profileRunner runs one ClickHouse statement with bound parameters and returns
// its JSONEachRow output. A seam so the command is testable without docker.
type profileRunner func(ctx context.Context, sql string, params map[string]string) ([]byte, error)

// defaultProfileRunner runs the statement inside the clickstack container with
// the credentials it already holds. The SQL and every parameter travel as
// positional arguments of `sh -c`, never interpolated into the script.
func defaultProfileRunner(ctx context.Context, sql string, params map[string]string) ([]byte, error) {
	dir, err := requireProjectDir("obs profile")
	if err != nil {
		return nil, err
	}
	// The local stack is the dev env's unless only another env's is running
	// from this checkout (localClickstackComposeArgs picks that one).
	args := append(localClickstackComposeArgs(ctx, dir, "dev"), "exec", "-T", clickstack.Service, "sh", "-c",
		`q="$1"; shift; exec clickhouse-client -u "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" --query "$q" "$@"`,
		"sh", sql)
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--param_"+k+"="+params[k])
	}
	out, err := doctorCompose(ctx, dir, args...)
	if err != nil {
		if strings.Contains(err.Error(), "is not running") || strings.Contains(err.Error(), "no such service") {
			return nil, fmt.Errorf("the local ClickStack is not running — start it with `forge env up`: %w", err)
		}
		return nil, err
	}
	return out, nil
}

func runObsProfile(ctx context.Context, stdout, stderr io.Writer, o obsProfileOpts, run profileRunner) error {
	from, to, err := profileRange(o, time.Now())
	if err != nil {
		return err
	}
	q := profilequery.Query{Service: o.service, SampleType: o.sampleType, Env: o.env, From: from, To: to, TraceID: o.trace, SpanID: o.span}
	if err := checkProfileOpts(o); err != nil {
		return err
	}

	if o.listServices {
		out, err := run(ctx, profilequery.ServicesSQL, map[string]string{"from": fmt.Sprint(from.Unix()), "to": fmt.Sprint(to.Unix())})
		if err != nil {
			return err
		}
		fmt.Fprint(stdout, string(out))
		return nil
	}

	if q.Service == "" && q.TraceID == "" {
		svc, err := soleProfiledService(ctx, run, from, to)
		if err != nil {
			return err
		}
		q.Service = svc
	}
	if o.printSQL && o.queryEnv != "" {
		return printObsQueryProfileSQL(stdout, stderr, o, q)
	}
	sql, params := q.SQL()
	if o.printSQL {
		fmt.Fprintln(stdout, sql)
		keys := make([]string, 0, len(params))
		for k := range params {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(stdout, "-- {%s} = %q\n", k, params[k])
		}
		return nil
	}

	raw, err := run(ctx, sql, params)
	if err != nil {
		return err
	}
	prof, err := profilequery.Parse(bytes.NewReader(raw), q)
	if err != nil {
		return err
	}
	if len(prof.Stacks) == 0 {
		return noProfileSamples(o, q)
	}
	return writeProfile(stdout, stderr, o, prof)
}

// checkProfileOpts refuses flag combinations that cannot be read.
func checkProfileOpts(o obsProfileOpts) error {
	if o.span != "" && o.trace == "" {
		return fmt.Errorf("--span needs --trace")
	}
	switch o.format {
	case "pprof", "speedscope", "folded":
	default:
		return fmt.Errorf("--format %q: want pprof, speedscope or folded", o.format)
	}
	return nil
}

// noProfileSamples is the error of a read that found no stacks.
func noProfileSamples(o obsProfileOpts, q profilequery.Query) error {
	what := fmt.Sprintf("service %q, type %s, since %s", q.Service, q.SampleType, q.From.Format(time.RFC3339))
	if q.TraceID != "" {
		what = "trace " + q.TraceID
	}
	list := "forge obs profile --list"
	if o.queryEnv != "" {
		list = "forge obs profile " + o.queryEnv + " --list"
	}
	return fmt.Errorf("no profile samples for %s. Apps push a CPU profile about every five minutes by default (the first between ~1 and 5 minutes after start; FORGE_PROFILES_CPU_INTERVAL changes it), and a trace only collects CPU samples when its spans ran for tens of milliseconds; check `%s`", what, list)
}

// writeProfile writes prof in o's format to -o, or to stdout.
func writeProfile(stdout, stderr io.Writer, o obsProfileOpts, prof profilequery.Profile) (retErr error) {
	var w io.Writer = stdout
	if o.output != "" {
		f, err := os.Create(o.output)
		if err != nil {
			return err
		}
		defer func() {
			if cerr := f.Close(); cerr != nil && retErr == nil {
				retErr = cerr
			}
		}()
		w = f
	} else if o.format == "pprof" {
		if f, ok := stdout.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
			return fmt.Errorf("refusing to write a binary pprof profile to a terminal; use -o FILE or pipe it")
		}
	}
	var err error
	switch o.format {
	case "pprof":
		err = prof.Pprof(w)
	case "speedscope":
		err = prof.Speedscope(w)
	default:
		err = prof.Folded(w)
	}
	if err != nil {
		return err
	}
	if o.output != "" {
		fmt.Fprintf(stderr, "wrote %s: %d stacks, total %d %s (%s)\n", o.output, len(prof.Stacks), prof.Total(), prof.Unit, o.format)
		if o.format == "pprof" {
			fmt.Fprintf(stderr, "open it:  go tool pprof -http=: %s\n", o.output)
		}
	}
	return nil
}

func profileRange(o obsProfileOpts, now time.Time) (time.Time, time.Time, error) {
	to := now
	if o.to != "" {
		t, err := time.Parse(time.RFC3339, o.to)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("--to: %w", err)
		}
		to = t
	}
	from := to.Add(-o.since)
	if o.from != "" {
		t, err := time.Parse(time.RFC3339, o.from)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("--from: %w", err)
		}
		from = t
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, fmt.Errorf("the range is empty: from %s is not before to %s", from.Format(time.RFC3339), to.Format(time.RFC3339))
	}
	return from, to, nil
}

func soleProfiledService(ctx context.Context, run profileRunner, from, to time.Time) (string, error) {
	out, err := run(ctx, profilequery.ServicesSQL, map[string]string{"from": fmt.Sprint(from.Unix()), "to": fmt.Sprint(to.Unix())})
	if err != nil {
		return "", err
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		if i := strings.Index(line, `"service":"`); i >= 0 {
			rest := line[i+len(`"service":"`):]
			if j := strings.Index(rest, `"`); j >= 0 {
				names = append(names, rest[:j])
			}
		}
	}
	return soleService(names, from)
}

// soleService is the one service that sent profiles since from, or an error
// that says why there is not one.
func soleService(names []string, from time.Time) (string, error) {
	switch len(names) {
	case 0:
		return "", fmt.Errorf("no service has sent profiles since %s; apps push about every five minutes by default once an OTLP endpoint is set (OTEL_PROFILES_EXPORTER=none turns it off)", from.Format(time.RFC3339))
	case 1:
		return names[0], nil
	}
	return "", fmt.Errorf("several services sent profiles; pick one with --service: %s", strings.Join(names, ", "))
}
