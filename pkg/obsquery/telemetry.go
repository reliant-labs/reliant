package obsquery

import (
	"strconv"
	"strings"
)

// Declared telemetry (forge.Telemetry): whether a system forge did not build
// is reaching the store. `forge obs telemetry` runs one TelemetryArrival per
// signal and one TelemetryScrapes over the systems an env declares.

// The signals a TelemetryArrival reads.
const (
	ArrivalLogs     = "logs"
	ArrivalTraces   = "traces"
	ArrivalMetrics  = "metrics"
	ArrivalProfiles = "profiles"
)

// ArrivalSignals are every signal a TelemetryArrival reads, in report order.
var ArrivalSignals = []string{ArrivalMetrics, ArrivalLogs, ArrivalTraces, ArrivalProfiles}

// MaxTelemetryScrapeRows bounds TelemetryScrapes: scraped endpoints (pods
// times scrapes) across every named service.
const MaxTelemetryScrapeRows = 1000

// TelemetryArrival is when each of Scope.Services last sent Signal in the
// range, and how many rows the range holds of it.
type TelemetryArrival struct {
	// Scope names the services (required) and the range. A profiles read
	// takes only resource-level filters: the profiles table keeps its own
	// attributes elsewhere.
	Scope  Scope
	Signal string
}

// Compile builds the statement. Columns: service, last_seen (the newest row's
// time), rows. One row per named service that has data in the range, by
// service.
func (q TelemetryArrival) Compile() (Stmt, error) {
	limit := MaxRangeLogs
	switch q.Signal {
	case ArrivalTraces:
		limit = MaxRangeTraces
	case ArrivalMetrics:
		limit = MaxRangeMetrics
	case ArrivalProfiles:
		limit = MaxRangeProfiles
	case ArrivalLogs:
	default:
		return Stmt{}, invalid("signal %q is not one of %s", q.Signal, strings.Join(ArrivalSignals, ", "))
	}
	if err := q.Scope.validate(limit); err != nil {
		return Stmt{}, err
	}
	if len(q.Scope.Services) == 0 {
		return Stmt{}, invalid("name at least one service")
	}
	b := newBuilder(q.Scope)
	part := func(table, timeCol string, sig Signal) (string, error) {
		conds := []string{b.rangeCond(timeCol, q.Scope)}
		if q.Signal == ArrivalProfiles {
			conds = append(conds, b.scopeBase(q.Scope)...)
			for _, f := range q.Scope.Filters {
				c, resource, err := b.filterCond(sig, f)
				if err != nil {
					return "", err
				}
				if !resource {
					return "", invalid("filter %q is not a resource attribute: profiles keep only resource attributes", f.Key)
				}
				conds = append(conds, c)
			}
		} else {
			sc, err := b.scopeConds(sig, q.Scope)
			if err != nil {
				return "", err
			}
			conds = append(conds, sc...)
		}
		return "SELECT ServiceName AS service, max(" + timeCol + ") AS last_seen, count() AS rows\n" +
			"FROM " + b.table(q.Scope, table) + "\n" + where(conds) + "\nGROUP BY service", nil
	}
	var parts []string
	add := func(table, timeCol string, sig Signal) error {
		p, err := part(table, timeCol, sig)
		if err != nil {
			return err
		}
		parts = append(parts, p)
		return nil
	}
	var err error
	switch q.Signal {
	case ArrivalLogs:
		err = add("otel_logs", "Timestamp", SigLogs)
	case ArrivalTraces:
		err = add("otel_traces", "Timestamp", SigTraces)
	case ArrivalProfiles:
		err = add("otel_profiles", "Timestamp", SigMetrics)
	case ArrivalMetrics:
		for _, k := range []CatalogKind{CatalogGauge, CatalogSum, CatalogHistogram, CatalogExpHistogram, CatalogSummary} {
			if err = add(k.table(), "TimeUnix", SigMetrics); err != nil {
				break
			}
		}
	}
	if err != nil {
		return Stmt{}, err
	}
	if len(parts) == 1 {
		return b.stmt(parts[0] + "\nORDER BY service"), nil
	}
	return b.stmt("SELECT service, max(last_seen) AS last_seen, sum(rows) AS rows\nFROM (\n" +
		strings.Join(parts, "\nUNION ALL\n") + "\n)\nGROUP BY service\nORDER BY service"), nil
}

// The series the OpenTelemetry Prometheus receiver writes for every scrape,
// whatever the target exports.
const (
	scrapeUp          = "up"
	scrapeSamples     = "scrape_samples_scraped"
	scrapeSamplesKept = "scrape_samples_post_metric_relabeling"
)

// TelemetryScrapes is the newest result of every Prometheus scrape of
// Scope.Services in the range: one row per scraped endpoint
// (service.instance.id).
type TelemetryScrapes struct {
	Scope Scope
}

// Compile builds the statement. Columns: service, instance, up (the newest
// `up`: 1 the scrape succeeded, 0 it failed), samples (the samples it
// returned), kept (the samples kept after the scrape's allow/deny lists),
// last_seen. At most MaxTelemetryScrapeRows rows, by service and instance.
func (q TelemetryScrapes) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeMetrics); err != nil {
		return Stmt{}, err
	}
	if len(q.Scope.Services) == 0 {
		return Stmt{}, invalid("name at least one service")
	}
	b := newBuilder(q.Scope)
	conds := []string{b.rangeCond("TimeUnix", q.Scope),
		"MetricName IN (" + b.str(scrapeUp) + ", " + b.str(scrapeSamples) + ", " + b.str(scrapeSamplesKept) + ")"}
	sc, err := b.scopeConds(SigMetrics, q.Scope)
	if err != nil {
		return Stmt{}, err
	}
	conds = append(conds, sc...)
	up, samples, kept := b.str(scrapeUp), b.str(scrapeSamples), b.str(scrapeSamplesKept)
	return b.stmt("SELECT ServiceName AS service,\n" +
		"       ResourceAttributes['service.instance.id'] AS instance,\n" +
		"       argMaxIf(Value, TimeUnix, MetricName = " + up + ") AS up,\n" +
		"       argMaxIf(Value, TimeUnix, MetricName = " + samples + ") AS samples,\n" +
		"       argMaxIf(Value, TimeUnix, MetricName = " + kept + ") AS kept,\n" +
		"       max(TimeUnix) AS last_seen\n" +
		"FROM " + b.table(q.Scope, "otel_metrics_gauge") + "\n" + where(conds) +
		"\nGROUP BY service, instance\nORDER BY service, instance\nLIMIT " + strconv.Itoa(MaxTelemetryScrapeRows)), nil
}
