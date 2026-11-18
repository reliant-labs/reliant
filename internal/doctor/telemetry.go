package doctor

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/clickstack"
)

// Local telemetry is read from ClickHouse itself, through `docker compose exec`
// into the clickstack container with the credentials that container already
// holds. ClickHouse publishes no port to the host, so there is nothing to
// discover and no address to get wrong; and asking the store the collector
// writes to answers the real question — did this signal arrive — rather than
// whether an API in front of it is up.

// telemetrySince bounds every check to recent data, so a stale row from a run
// last week cannot pass today's check.
const telemetrySince = "now() - INTERVAL 15 MINUTE"

// metricTables are all three metric families the collector writes. A check
// that read only one (a sum, say) reported a healthy app that exports gauges
// and histograms as having no metrics.
var metricTables = []string{"otel_metrics_gauge", "otel_metrics_sum", "otel_metrics_histogram"}

// clickhouseQuery runs one query inside the clickstack container and returns
// its TSV rows. The SQL travels as a positional parameter of `sh -c`, never
// interpolated into the script.
func clickhouseQuery(ctx context.Context, env *Environment, sql string) ([][]string, error) {
	out, err := env.compose(ctx, "exec", "-T", clickstack.Service, "sh", "-c",
		`clickhouse-client -u "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" --format TSV --query "$1"`,
		"sh", sql)
	if err != nil {
		return nil, err
	}
	var rows [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		rows = append(rows, strings.Split(line, "\t"))
	}
	return rows, nil
}

// notRunning reports whether a compose error means the clickstack container
// simply is not up — observability off, or the stack down — as opposed to a
// failure to ask.
func notRunning(err error) bool {
	m := err.Error()
	return strings.Contains(m, "is not running") || strings.Contains(m, "no such service") ||
		strings.Contains(m, "no service selected")
}

// CheckTraces verifies spans reached ClickStack, naming the services that sent
// them.
func CheckTraces(ctx context.Context, env *Environment) CheckResult {
	return signalCheck(ctx, env, "traces", "spans",
		`SELECT ServiceName, count() FROM default.otel_traces WHERE Timestamp > `+telemetrySince+` GROUP BY ServiceName`)
}

// CheckMetrics verifies metric points of ANY family reached ClickStack.
func CheckMetrics(ctx context.Context, env *Environment) CheckResult {
	parts := make([]string, 0, len(metricTables))
	for _, t := range metricTables {
		parts = append(parts, `SELECT ServiceName, count() AS n FROM default.`+t+` WHERE TimeUnix > `+telemetrySince+` GROUP BY ServiceName`)
	}
	return signalCheck(ctx, env, "metrics", "points",
		`SELECT ServiceName, sum(n) FROM (`+strings.Join(parts, " UNION ALL ")+`) GROUP BY ServiceName`)
}

// CheckLogs verifies log records reached ClickStack. They come from the files
// `forge env up` writes under .forge/logs/<env>/, so a miss here with healthy
// traces points at the collector's file mount, not the app.
func CheckLogs(ctx context.Context, env *Environment) CheckResult {
	return signalCheck(ctx, env, "logs", "records",
		`SELECT ServiceName, count() FROM default.otel_logs WHERE Timestamp > `+telemetrySince+` GROUP BY ServiceName`)
}

func signalCheck(ctx context.Context, env *Environment, signal, unit, sql string) CheckResult {
	rows, err := clickhouseQuery(ctx, env, sql)
	if err != nil {
		if notRunning(err) {
			return CheckResult{
				Status:   StatusSkip,
				Message:  "local ClickStack is not running — `forge env up` starts it unless `_observability = False` in deploy/kcl/<env>/main.k",
				Evidence: err.Error(),
			}
		}
		return CheckResult{Status: StatusUnknown, Message: "could not query ClickStack for " + signal + ": " + err.Error()}
	}
	return signalVerdict(signal, unit, rows)
}

// signalVerdict turns "which services sent how many rows" into the result.
// Split from the query so the verdict is testable alone.
func signalVerdict(signal, unit string, rows [][]string) CheckResult {
	type svcCount struct {
		name string
		n    int
	}
	var got []svcCount
	total := 0
	for _, r := range rows {
		if len(r) < 2 {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(r[1]))
		if err != nil || n <= 0 {
			continue
		}
		got = append(got, svcCount{r[0], n})
		total += n
	}
	if total == 0 {
		return CheckResult{
			Status: StatusWarn,
			Message: fmt.Sprintf("ClickStack is up but holds no %s from the last 15 minutes — nothing is exporting "+
				"(host processes get OTEL_EXPORTER_OTLP_ENDPOINT from `forge env up`; a process started by hand does not)", signal),
		}
	}
	sort.Slice(got, func(i, j int) bool { return got[i].n > got[j].n })
	names := make([]string, 0, len(got))
	for _, g := range got {
		names = append(names, g.name)
	}
	if len(names) > 4 {
		names = append(names[:4], "…")
	}
	return CheckResult{
		Status:  StatusPass,
		Message: fmt.Sprintf("%d %s from %d service(s): %s", total, unit, len(got), strings.Join(names, ", ")),
	}
}
