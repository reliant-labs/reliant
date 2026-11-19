package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
)

// THE PAYOFF IN `forge env up`'S OWN OUTPUT.
//
// Every RPC a forge service serves is a span and a metric sample with no code
// (edge telemetry), and `forge obs endpoints <env>` turns that into p50, p95,
// p99 and an error rate per RPC. An evaluating agent called that table its one
// "this is good" moment, and it only ran the command because someone asked
// (forge eval 2026-11-17, feedback #1). So a stack that comes up with services
// ends with that table.
//
// THE FIRST REQUEST IS THE HEALTH RPC. A fresh stack has served nobody, so
// once its host services are ready `env up` calls grpc.health.v1.Health/Check
// on each (sendUpHealthProbes): the standard health rpc forge's serverkit
// serves through the listener's own chain, unauthenticated by every generated
// project and free of side effects. Unlike /readyz, which is served in front
// of the chain and records nothing, the call is traced like any rpc, so the
// table shows the stack's telemetry working end to end on the first run, with
// the health row labelled. The call carries observe.HeaderHealthProbe, so its
// span is marked forge.probe and `forge obs check`, RED, the issue views and
// the dashboards leave it out, and it records no rpc.server.* metric, so
// metric alerts do too. A monitor's own health checks stay real traffic.
//
// THE WINDOW IS THIS RUN. The table covers the time since this env up
// started, not the last hour, so an earlier session's traffic does not crowd
// out what this one did.
//
// IT NEVER COSTS THE RUN. The probes go out the moment readiness passes; the
// table waits at most upEndpointsSettle from then for the span to be stored
// (span export batches every few seconds), each query bounded by
// upEndpointsBudget, and stops at once when the run is cancelled or the store
// keeps failing. A service on a forge too old to serve the health rpc, no
// telemetry backend, or a slow store prints one line naming the command.

// upEndpointsBudget bounds one endpoints query.
var upEndpointsBudget = 5 * time.Second

// upEndpointsSettle is how long after the health probes the table waits for
// their spans to reach the store, and upEndpointsPoll how often it looks.
var (
	upEndpointsSettle = 12 * time.Second
	upEndpointsPoll   = 2 * time.Second
)

// upEndpointsMaxErrors is how many queries in a row may fail before the table
// stops waiting: the store is not answering, not merely behind.
const upEndpointsMaxErrors = 2

// upTableNow and upTableSleep are the table's clock: seams so tests run on a fixed one.
// upTableSleep waits d or until ctx is done, and reports whether d elapsed.
var (
	upTableNow   = time.Now
	upTableSleep = func(ctx context.Context, d time.Duration) bool {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			return true
		}
	}
)

// upHealthProbeTimeout bounds one health rpc.
var upHealthProbeTimeout = 2 * time.Second

// upEndpointsTop is how many endpoints the table shows, busiest first.
const upEndpointsTop = 5

// upHealthProbe calls the health rpc at base (a host service's URL), as a
// declared probe (observe.HeaderHealthProbe). A seam for tests.
var upHealthProbe = func(ctx context.Context, base string) error {
	ctx, cancel := context.WithTimeout(ctx, upHealthProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(base, "/")+observe.HealthCheckProcedure, strings.NewReader("{}"))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(observe.HeaderHealthProbe, "env-up")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", observe.HealthCheckProcedure, resp.StatusCode)
	}
	return nil
}

// upHealthProbes is what sendUpHealthProbes did: when this env up started
// (the table's window), when the probes went out, and how many answered.
type upHealthProbes struct {
	started  time.Time
	at       time.Time
	answered int
}

// sendUpHealthProbes calls the health rpc once on each host service that
// listens on a port. Errors are ignored: the table falls back to its line.
func sendUpHealthProbes(ctx context.Context, started time.Time, rows []upServiceRow) upHealthProbes {
	p := upHealthProbes{started: started, at: upTableNow()}
	for _, r := range rows {
		if r.Kind != "host" || r.Port <= 0 {
			continue
		}
		base := r.URL
		if base == "" {
			base = "http://127.0.0.1:" + strconv.Itoa(r.Port)
		}
		if upHealthProbe(ctx, base) == nil {
			p.answered++
		}
	}
	return p
}

// upEndpointsQuery reads the env's busiest endpoints since from, the same
// document `forge obs endpoints <env> --from <from> --json` prints. A seam for
// tests.
var upEndpointsQuery = func(ctx context.Context, env string, from time.Time) ([]obsEndpointDoc, error) {
	cmd := newObsEndpointsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	args := []string{env, "--json", "--top", strconv.Itoa(upEndpointsTop)}
	if !from.IsZero() {
		args = append(args, "--from", from.UTC().Truncate(time.Second).Format(time.RFC3339))
	}
	cmd.SetArgs(args)
	if err := cmd.ExecuteContext(ctx); err != nil {
		return nil, err
	}
	var doc obsEndpointsDoc
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		return nil, fmt.Errorf("read obs endpoints: %w", err)
	}
	return doc.Endpoints, nil
}

// readUpEndpoints queries until the store holds a row, or, when a health
// probe was answered, until its span has had upEndpointsSettle to arrive. It
// queries at least once, returns at once when ctx is done, and gives up after
// upEndpointsMaxErrors failed queries in a row.
func readUpEndpoints(ctx context.Context, env string, probes upHealthProbes) []obsEndpointDoc {
	deadline := upTableNow()
	if probes.answered > 0 {
		deadline = probes.at.Add(upEndpointsSettle)
	}
	failed := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		qctx, cancel := context.WithTimeout(ctx, upEndpointsBudget)
		rows, err := upEndpointsQuery(qctx, env, probes.started)
		cancel()
		switch {
		case err == nil && len(rows) > 0:
			return rows
		case err != nil:
			failed++
			if failed >= upEndpointsMaxErrors || probes.answered == 0 {
				return nil
			}
		default:
			failed = 0
		}
		wait := deadline.Sub(upTableNow())
		if wait <= 0 {
			return nil
		}
		if !upTableSleep(ctx, min(wait, upEndpointsPoll)) {
			return nil
		}
	}
}

// printUpEndpoints prints the per-RPC table for env, or the line naming the
// command when there is nothing to show.
func printUpEndpoints(ctx context.Context, w io.Writer, env string, probes upHealthProbes) {
	rows := readUpEndpoints(ctx, env, probes)
	cmd := fmt.Sprintf("%s obs endpoints %s", Name(), env)
	if len(rows) == 0 {
		if ctx.Err() == nil {
			fmt.Fprintf(w, "\nEvery RPC this stack serves is traced and measured, with no code: `%s` shows p50/p95/p99 and the error rate per RPC once it has served requests.\n", cmd)
		}
		return
	}
	window := "over the last hour"
	if !probes.started.IsZero() {
		window = "since env up started at " + probes.started.Local().Format("15:04")
	}
	fmt.Fprintf(w, "\nPer-RPC latency and errors %s, no instrumentation code (`%s` for more):\n", window, cmd)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  ENDPOINT\tREQUESTS\tERRORS\tP50\tP95\tP99")
	onlyHealth := true
	for _, e := range rows {
		name := e.Name
		if e.Health {
			name += "  (health check; env up sent one, and release health leaves env up's out)"
		} else {
			onlyHealth = false
		}
		fmt.Fprintf(tw, "  %s\t%.0f\t%s%%\t%s\t%s\t%s\n", name, e.Requests,
			strconv.FormatFloat(e.ErrorRatio*100, 'f', 1, 64), ms(e.P50Ms), ms(e.P95Ms), ms(e.P99Ms))
	}
	_ = tw.Flush()
	if onlyHealth {
		fmt.Fprintln(w, "  Your own RPCs join the table as they serve requests.")
	}
}
