package obsquery

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/reliant-labs/forge/pkg/alerting"
)

// ServiceAnomaly: a service's request rate, error rate and p95 latency judged
// against the same hour of the week over the previous weeks.
//
// The data is the RPC server METRICS (rpc.server.calls / .errors / .duration),
// never spans: metrics are not sampled, while the gateway's tail sampler keeps
// every error trace and a fraction of the rest, so a ratio of span counts is
// wrong. The statements are the metric evaluator's (EvalMetricPanel), so the
// counter, restart and histogram semantics are the ones every other metrics
// reader has.
//
// The baseline is the same hour of the week: for each past week, every
// window-wide slot of the hours the observed window falls in (4 to 8 slots of
// 15 minutes), so a normal Monday peak is its own baseline. Its median is the
// usual value and its MAD the usual spread, and neither moves for one bad
// week.
//
// Cost: the baseline reads one window of one or two hours per past week, never
// the whole history, so a query reads a few hours of the three metrics, not
// four weeks. The one
// statement that spans the history is the first-seen lookup of the history
// floor, a min() over rpc.server.calls; a caller that evaluates often can
// cache a service once it has crossed the floor.
//
// The judge is pure: it takes samples and returns findings, so what fires is
// testable without a database. A finding fires only when ALL of these hold:
//
//   - the service has at least MinHistory of history (it is not new);
//   - the baseline has at least MinBaselineSamples slots (a real baseline);
//   - the observed value is further than K times the baseline's MAD from the
//     baseline median (it is outside the usual spread);
//   - the absolute floor of its signal holds: MinRequests in the window, and
//     MinErrors errors and an error-rate delta of MinErrorDelta, or a p95 ratio of P95Ratio and a
//     delta of MinLatencyDelta seconds, or a throughput ratio of
//     ThroughputRatio.
//
// The floors are what keep a spread of zero (an error rate of exactly 0 for
// weeks) from firing on one failed request: 1 error in 60 requests is a rate
// of 1.7%, over the 1 point delta, and still not an incident.

// Signals a ServiceAnomaly finding names (alerting.Why.Signal).
const (
	AnomalySignalErrorRate  = "error_rate"
	AnomalySignalP95        = "p95"
	AnomalySignalThroughput = "throughput"
)

// AnomalyParams are the detector's limits. The zero value is not usable: start
// from DefaultAnomalyParams.
type AnomalyParams struct {
	// Window is the span the observed value covers, and the width of one
	// baseline slot, so a slot is directly comparable to the window.
	Window time.Duration
	// Delay is how far the window's end lags now: the metric exporters'
	// interval, so a sample that has not landed does not read as a drop.
	Delay time.Duration
	// Weeks is how many previous weeks make the baseline (1 to 4: metrics are
	// kept 30 days).
	Weeks int
	// K is the multiple of the baseline's MAD the observed value must leave.
	K float64

	// MinRequests: a window with fewer requests never fires an error-rate or
	// latency finding, and a baseline whose median slot has fewer never fires
	// a throughput one.
	MinRequests float64
	// MinErrors is the fewest server errors in the window that fire an
	// error-rate finding, and MinErrorDelta the least rise in the error ratio
	// (0..1) that does.
	MinErrors     float64
	MinErrorDelta float64
	// P95Ratio and MinLatencyDelta (seconds): the p95 must be at least this
	// multiple of the baseline's AND this much above it.
	P95Ratio        float64
	MinLatencyDelta float64
	// ThroughputRatio: requests must be at least this multiple of the
	// baseline's, or at most its reciprocal.
	ThroughputRatio float64

	// MinHistory is how much history a service must have to be judged.
	MinHistory time.Duration
	// MinBaselineSamples is the fewest baseline slots a signal needs.
	MinBaselineSamples int
}

// DefaultAnomalyParams are the platform defaults: a 15 minute window against
// the same hour of the previous 4 weeks, 4 MADs, at least 50 requests, a
// 1 point (and 5 errors) error-rate rise or a 1.5x (and 50 ms) p95 rise, after
// 7 days of history.
func DefaultAnomalyParams() AnomalyParams {
	return AnomalyParams{
		Window: 15 * time.Minute, Delay: 2 * time.Minute, Weeks: 4, K: 4,
		MinRequests: 50, MinErrors: 5, MinErrorDelta: 0.01, P95Ratio: 1.5, MinLatencyDelta: 0.05, ThroughputRatio: 2,
		MinHistory: 7 * 24 * time.Hour, MinBaselineSamples: 8,
	}
}

const (
	week = 7 * 24 * time.Hour
	// anomalyMaxWeeks is how many weeks fit the metrics range.
	anomalyMaxWeeks = int(MaxRangeMetrics / week)
)

// Validate refuses limits the detector cannot work with.
func (p AnomalyParams) Validate() error {
	switch {
	case p.Window < time.Minute || p.Window%time.Minute != 0 || time.Hour%p.Window != 0:
		return invalid("anomaly window must be a whole number of minutes that divides an hour, got %s", p.Window)
	case p.Delay < 0:
		return invalid("anomaly delay must not be negative")
	case p.Weeks < 1 || p.Weeks > anomalyMaxWeeks:
		return invalid("anomaly weeks must be 1 to %d", anomalyMaxWeeks)
	case p.K <= 0 || p.MinRequests < 0 || p.MinErrors < 0 || p.MinErrorDelta < 0 || p.MinLatencyDelta < 0:
		return invalid("anomaly k must be positive and the floors not negative")
	case p.P95Ratio < 1 || p.ThroughputRatio < 1:
		return invalid("anomaly ratios must be at least 1")
	case p.MinBaselineSamples < 1:
		return invalid("anomaly needs at least one baseline sample")
	}
	return nil
}

// REDSample is one window of one service: requests, server errors and p95.
type REDSample struct {
	Requests, Errors float64
	// P95 is in seconds; valid when HasP95.
	P95    float64
	HasP95 bool
}

// ErrorRate is Errors / Requests, 0 when there were no requests.
func (s REDSample) ErrorRate() float64 {
	if s.Requests <= 0 {
		return 0
	}
	return s.Errors / s.Requests
}

// ServiceREDInput is what the judge needs of one service.
type ServiceREDInput struct {
	Service string
	// Observed is the window's aggregate.
	Observed REDSample
	// Baseline is one sample per slot of the previous weeks, a slot with no
	// traffic a zero sample, for the weeks the service already existed in.
	Baseline []REDSample
	// FirstSeen is when the service's metrics first appeared in the lookback
	// (zero: never).
	FirstSeen time.Time
	Now       time.Time
}

// HistoryDays is how many whole days of history the service had, at most the
// lookback the first-seen was read over.
func (in ServiceREDInput) HistoryDays() int {
	if in.FirstSeen.IsZero() || in.Now.Before(in.FirstSeen) {
		return 0
	}
	return int(in.Now.Sub(in.FirstSeen) / (24 * time.Hour))
}

// Anomaly is one signal of one service that left its seasonal baseline.
type Anomaly struct {
	Service string
	// Signal is an AnomalySignal* value.
	Signal string
	// Observed, Baseline (the median) and Spread (the MAD) are in Unit:
	// "fraction" for the error rate, "s" for p95, "requests" for throughput.
	Observed, Baseline, Spread float64
	Unit                       string
	// Limit is the nearest value of the signal that would not have fired,
	// on the side the observation is on.
	Limit float64
	// Up is whether the signal rose; throughput can also fall.
	Up bool
	// Requests is the window's request count.
	Requests float64
	// BaselineSamples is how many slots the baseline had.
	BaselineSamples int
	HistoryDays     int
}

// JudgeService returns the signals of one service that left their baseline,
// in the order error_rate, p95, throughput; nil when the service is young, has
// no baseline, or everything is inside it.
func JudgeService(in ServiceREDInput, p AnomalyParams) []Anomaly {
	if in.FirstSeen.IsZero() || in.Now.Sub(in.FirstSeen) < p.MinHistory {
		return nil
	}
	var out []Anomaly
	mk := func(signal, unit string, obs, med, mad, limit float64, up bool, samples int) Anomaly {
		return Anomaly{
			Service: in.Service, Signal: signal, Observed: obs, Baseline: med, Spread: mad, Unit: unit,
			Limit: limit, Up: up, Requests: in.Observed.Requests, BaselineSamples: samples, HistoryDays: in.HistoryDays(),
		}
	}
	// A slot with too few requests has a noisy ratio and a noisy percentile.
	slotMin := math.Max(1, p.MinRequests/5)

	if in.Observed.Requests >= p.MinRequests {
		var rates []float64
		for _, s := range in.Baseline {
			if s.Requests >= slotMin {
				rates = append(rates, s.ErrorRate())
			}
		}
		if len(rates) >= p.MinBaselineSamples {
			med, mad := medianMAD(rates)
			obs := in.Observed.ErrorRate()
			limit := med + math.Max(p.K*mad, p.MinErrorDelta)
			if in.Observed.Errors >= p.MinErrors && obs-med >= p.MinErrorDelta && obs-med > p.K*mad {
				out = append(out, mk(AnomalySignalErrorRate, "fraction", obs, med, mad, limit, true, len(rates)))
			}
		}
		if in.Observed.HasP95 {
			var lat []float64
			for _, s := range in.Baseline {
				if s.HasP95 && s.Requests >= slotMin {
					lat = append(lat, s.P95)
				}
			}
			if len(lat) >= p.MinBaselineSamples {
				med, mad := medianMAD(lat)
				obs := in.Observed.P95
				limit := math.Max(med*p.P95Ratio, med+math.Max(p.K*mad, p.MinLatencyDelta))
				if med > 0 && obs >= med*p.P95Ratio && obs-med >= p.MinLatencyDelta && obs-med > p.K*mad {
					out = append(out, mk(AnomalySignalP95, "s", obs, med, mad, limit, true, len(lat)))
				}
			}
		}
	}

	if len(in.Baseline) >= p.MinBaselineSamples {
		reqs := make([]float64, len(in.Baseline))
		for i, s := range in.Baseline {
			reqs[i] = s.Requests
		}
		med, mad := medianMAD(reqs)
		obs := in.Observed.Requests
		if med >= p.MinRequests && math.Abs(obs-med) > p.K*mad {
			switch {
			case obs >= med*p.ThroughputRatio && obs >= p.MinRequests:
				out = append(out, mk(AnomalySignalThroughput, "requests", obs, med, mad, math.Max(med*p.ThroughputRatio, med+p.K*mad), true, len(reqs)))
			case obs <= med/p.ThroughputRatio:
				out = append(out, mk(AnomalySignalThroughput, "requests", obs, med, mad, math.Min(med/p.ThroughputRatio, med-p.K*mad), false, len(reqs)))
			}
		}
	}
	return out
}

// medianMAD is the median of xs and the median of the absolute deviations from
// it. xs is not modified; it must not be empty.
func medianMAD(xs []float64) (median, mad float64) {
	median = medianOf(xs)
	dev := make([]float64, len(xs))
	for i, x := range xs {
		dev[i] = math.Abs(x - median)
	}
	return median, medianOf(dev)
}

func medianOf(xs []float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// ── plan and evaluation ──────────────────────────────────────────────────────

// AnomalyQuery is one evaluation: which services (the scope's), when, and
// with which limits. Scope.Start and End are ignored: the windows come from
// Now and the params.
type AnomalyQuery struct {
	Scope  Scope
	Params AnomalyParams
	// Now is the evaluation time; zero is time.Now().
	Now time.Time
}

// Series labels of the anomaly panels.
const (
	anomalyCalls  = "calls"
	anomalyErrors = "errors"
	anomalyP95    = "p95"
)

// anomalySeries are the three signals, each read as its own panel: a window
// with no server errors at all has no rpc.server.errors rows, which the
// evaluator reports as a missing metric, and that must not take the request
// counts with it. Errors are the same classes the ServiceErrorRateHigh default
// counts (not `user`, not `canceled`).
var anomalySeries = []PanelSeries{
	{Label: anomalyCalls, Agg: "increase", Field: "rpc.server.calls", MetricType: "sum"},
	{Label: anomalyErrors, Agg: "increase", Field: "rpc.server.errors", MetricType: "sum",
		Where: []PanelPredicate{{Field: "metric.attr.error_class", Op: "not_in", Values: []string{"user", "canceled"}}}},
	{Label: anomalyP95, Agg: "quantile", Quantile: 0.95, Field: "rpc.server.duration", MetricType: "histogram"},
}

// anomalyPanel reads one signal per service: a "table" is the window as one
// aggregate, a "line" one bucket per slot.
func anomalyPanel(kind string, ps PanelSeries) Panel {
	p := Panel{ID: "service-anomaly", Kind: kind, Signal: "metrics", GroupBy: []string{"service.name"}, Series: []PanelSeries{ps}}
	if kind == "table" {
		p.Limit = MaxTableRows
	}
	return p
}

// anomalyWindows are the scopes an evaluation reads.
type anomalyWindows struct {
	observed  Scope
	baselines []Scope // one per past week, oldest last
	history   Scope
	step      time.Duration
}

func alignDown(t time.Time, step time.Duration) time.Time {
	s := int64(step / time.Second)
	return time.Unix(t.Unix()-t.Unix()%s, 0).UTC()
}

func alignUp(t time.Time, step time.Duration) time.Time {
	d := alignDown(t, step)
	if d.Equal(t) {
		return d
	}
	return d.Add(step)
}

// windows resolves the scopes. The baseline range is the observed window
// rounded out to whole hours, shifted back by whole weeks, so its slots sit on
// the same grid as the bucketing (toStartOfInterval) and the same hour of the
// week is compared.
func (q AnomalyQuery) windows(now time.Time) anomalyWindows {
	p := q.Params
	w := anomalyWindows{step: p.Window}
	obsEnd := now.Add(-p.Delay).UTC().Truncate(time.Second)
	obsStart := obsEnd.Add(-p.Window)
	w.observed = q.Scope
	w.observed.Start, w.observed.End = obsStart, obsEnd
	from := alignDown(obsStart, time.Hour)
	to := alignUp(obsEnd, time.Hour)
	for k := 1; k <= p.Weeks; k++ {
		sc := q.Scope
		sc.Start, sc.End = from.Add(-time.Duration(k)*week), to.Add(-time.Duration(k)*week)
		w.baselines = append(w.baselines, sc)
	}
	w.history = q.Scope
	w.history.Start, w.history.End = w.baselines[len(w.baselines)-1].Start, now.UTC().Truncate(time.Second)
	return w
}

// FirstSeenStmt reads each service's first rpc.server.calls sample within the
// weeks the baseline reaches: columns service (String) and first (DateTime). A
// service whose first sample is the lookback's start has at least that much
// history.
func (q AnomalyQuery) FirstSeenStmt() (Stmt, error) {
	if err := q.Params.Validate(); err != nil {
		return Stmt{}, err
	}
	now := q.now()
	w := q.windows(now)
	if err := w.history.validate(MaxRangeMetrics); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(w.history)
	conds := []string{b.rangeCondSec("TimeUnix", w.history), "MetricName = " + b.str("rpc.server.calls")}
	sc, err := b.scopeConds(SigMetrics, w.history)
	if err != nil {
		return Stmt{}, err
	}
	conds = append(conds, sc...)
	return b.stmt("SELECT ServiceName AS service, min(TimeUnix) AS first FROM " + b.table(w.history, MetricSum.table()) + " " + where(conds) +
		" GROUP BY service ORDER BY service LIMIT " + strconv.Itoa(MaxAnomalyServices)), nil
}

// MaxAnomalyServices bounds the services one evaluation judges: the series
// cap of a line panel.
const MaxAnomalyServices = MaxMetricSeries

func (q AnomalyQuery) now() time.Time {
	if q.Now.IsZero() {
		return time.Now().UTC()
	}
	return q.Now.UTC()
}

// AnomalyResult is what EvalAnomalies returns.
type AnomalyResult struct {
	// Inputs are the services judged, with the samples they were judged on.
	Inputs []ServiceREDInput
	// Anomalies are the findings, by service then signal.
	Anomalies []Anomaly
	// Window is the observed window's start and end.
	WindowStart, WindowEnd time.Time
	// Truncated is set when more services existed than MaxAnomalyServices:
	// the busiest were judged.
	Truncated bool
	Warnings  []string
}

// EvalAnomalies runs the observed window, one baseline window per past week
// and the first-seen lookup through run, and judges every service the lookup
// found. A service with no first sample in the lookback has no metrics and is
// not judged.
func EvalAnomalies(ctx context.Context, run Runner, q AnomalyQuery) (AnomalyResult, error) {
	if err := q.Params.Validate(); err != nil {
		return AnomalyResult{}, err
	}
	now := q.now()
	w := q.windows(now)
	res := AnomalyResult{WindowStart: w.observed.Start, WindowEnd: w.observed.End}
	warn := map[string]bool{}
	note := func(ws []string) {
		for _, x := range ws {
			if !warn[x] {
				warn[x] = true
				res.Warnings = append(res.Warnings, x)
			}
		}
	}

	first, err := q.readFirstSeen(ctx, run)
	if err != nil {
		return res, err
	}
	if len(first) == 0 {
		return res, nil
	}
	eo := EvalOptions{Now: now, MaxSeries: MaxAnomalyServices}
	// read runs one signal's panel over sc; a signal with no rows in the window
	// is an absent series, not a failure.
	read := func(sc Scope, kind string, ps PanelSeries, o EvalOptions) (MetricPanelResult, error) {
		r, err := EvalMetricPanel(ctx, run, sc, anomalyPanel(kind, ps), o)
		if err != nil {
			return r, fmt.Errorf("service anomaly: %s over %s: %w", ps.Label, sc.Start.Format(time.RFC3339), err)
		}
		if len(r.Absent) > 0 {
			return MetricPanelResult{}, nil
		}
		note(r.Warnings)
		res.Truncated = res.Truncated || r.Truncated
		return r, nil
	}

	observed := map[string]REDSample{}
	for _, ps := range anomalySeries {
		r, err := read(w.observed, "table", ps, eo)
		if err != nil {
			return res, err
		}
		observedSamples(observed, r, ps.Label)
	}

	eo.StepSeconds = int32(w.step / time.Second)
	slots := map[string]map[int64]*REDSample{} // service -> slot start (unix) -> sample
	for _, sc := range w.baselines {
		for _, ps := range anomalySeries {
			r, err := read(sc, "line", ps, eo)
			if err != nil {
				return res, err
			}
			if r.StepSeconds != 0 && r.StepSeconds != int64(w.step/time.Second) {
				return res, fmt.Errorf("service anomaly: the metrics export every %ds, too long for a %s window", r.StepSeconds, w.step)
			}
			foldSlots(slots, r, ps.Label)
		}
	}

	services := make([]string, 0, len(first))
	for s := range first {
		services = append(services, s)
	}
	sort.Strings(services)
	for _, svc := range services {
		in := ServiceREDInput{Service: svc, Observed: observed[svc], FirstSeen: first[svc], Now: now}
		for _, sc := range w.baselines {
			for t := sc.Start; t.Before(sc.End); t = t.Add(w.step) {
				// A slot before the service existed is not a quiet slot, it is no slot.
				if t.Before(first[svc]) {
					continue
				}
				var s REDSample
				if got := slots[svc][t.Unix()]; got != nil {
					s = *got
				}
				in.Baseline = append(in.Baseline, s)
			}
		}
		res.Inputs = append(res.Inputs, in)
		res.Anomalies = append(res.Anomalies, JudgeService(in, q.Params)...)
	}
	return res, nil
}

func (q AnomalyQuery) readFirstSeen(ctx context.Context, run Runner) (map[string]time.Time, error) {
	st, err := q.FirstSeenStmt()
	if err != nil {
		return nil, err
	}
	rows, err := run(ctx, st)
	if err != nil {
		return nil, fmt.Errorf("service anomaly: first-seen lookup: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]time.Time{}
	for rows.Next() {
		var svc string
		var at time.Time
		if err := rows.Scan(&svc, &at); err != nil {
			return nil, err
		}
		out[svc] = at.UTC()
	}
	return out, rows.Err()
}

// setSample writes the value a series label stands for.
func setSample(s *REDSample, label string, v float64) {
	switch label {
	case anomalyCalls:
		s.Requests = v
	case anomalyErrors:
		s.Errors = v
	case anomalyP95:
		s.P95, s.HasP95 = v, true
	}
}

func observedSamples(into map[string]REDSample, r MetricPanelResult, label string) {
	for _, s := range r.Series {
		svc := s.Labels["service.name"]
		if svc == "" || s.Reduced == nil {
			continue
		}
		cur := into[svc]
		setSample(&cur, label, *s.Reduced)
		into[svc] = cur
	}
}

func foldSlots(into map[string]map[int64]*REDSample, r MetricPanelResult, label string) {
	for _, s := range r.Series {
		svc := s.Labels["service.name"]
		if svc == "" {
			continue
		}
		if into[svc] == nil {
			into[svc] = map[int64]*REDSample{}
		}
		for _, p := range s.Points {
			if p.Null {
				continue
			}
			cur := into[svc][p.Time.Unix()]
			if cur == nil {
				cur = &REDSample{}
				into[svc][p.Time.Unix()] = cur
			}
			setSample(cur, label, p.Value)
		}
	}
}

// ── the why-fired payload ────────────────────────────────────────────────────

// Why is the shared why-fired payload of the finding, filled with what the
// detector knows: observed, baseline, spread, the method, the window, the
// limits and the forge obs commands that lead to the evidence. The releases,
// activity and issues in the window are the caller's (they live in its
// registries): it fills Context before posting. env names the environment in
// the next commands.
func (a Anomaly) Why(env string, q AnomalyQuery) alerting.Why {
	p := q.Params
	now := q.now()
	w := q.windows(now)
	threshold := map[string]float64{"k": p.K, "min_requests": p.MinRequests}
	switch a.Signal {
	case AnomalySignalErrorRate:
		threshold["min_errors"], threshold["min_error_delta"] = p.MinErrors, p.MinErrorDelta
	case AnomalySignalP95:
		threshold["p95_ratio"], threshold["min_latency_delta_seconds"] = p.P95Ratio, p.MinLatencyDelta
	case AnomalySignalThroughput:
		threshold["throughput_ratio"] = p.ThroughputRatio
	}
	dir := "above"
	if !a.Up {
		dir = "below"
	}
	before, after := a.Baseline, a.Observed
	msg := fmt.Sprintf("%s %s is %s its usual for this hour of the week: %s against a baseline of %s (spread %s)",
		a.Service, a.Signal, dir, fmtAnomaly(a.Unit, a.Observed), fmtAnomaly(a.Unit, a.Baseline), fmtAnomaly(a.Unit, a.Spread))
	return alerting.Why{
		Detector:   alerting.DetectorServiceAnomaly,
		Signal:     a.Signal,
		DedupeKey:  alerting.DedupeKey(alerting.DetectorServiceAnomaly, a.Service, ""),
		Observed:   alerting.F(a.Observed),
		Baseline:   alerting.F(a.Baseline),
		Spread:     alerting.F(a.Spread),
		Unit:       a.Unit,
		Method:     fmt.Sprintf("hour-of-week median/MAD, %dw", p.Weeks),
		Thresholds: threshold,
		Window:     p.Window.String(),
		BaselineWindow: fmt.Sprintf("the same hour of the week over the previous %d weeks, %d samples of %s",
			p.Weeks, a.BaselineSamples, p.Window),
		Since:       w.observed.Start,
		HistoryDays: a.HistoryDays,
		Context: alerting.WhyContext{Findings: []alerting.WhyFinding{{
			Code: "service_anomaly_" + a.Signal, Service: a.Service, Value: a.Observed, Limit: a.Limit,
			Before: &before, After: &after, Policy: "warn", Message: msg,
		}}},
		Next: anomalyNext(env, a, p),
	}
}

func fmtAnomaly(unit string, v float64) string {
	switch unit {
	case "fraction":
		return strconv.FormatFloat(v*100, 'f', 2, 64) + "%"
	case "s":
		return strconv.FormatFloat(v*1000, 'f', 0, 64) + " ms"
	}
	return strconv.FormatFloat(v, 'f', 0, 64) + " requests"
}

// anomalyNext are the commands from the alert to the evidence, runnable as
// written.
func anomalyNext(env string, a Anomaly, p AnomalyParams) []string {
	since := "--since " + p.Window.String()
	svc := "--service " + a.Service
	week := "--compare 168h"
	switch a.Signal {
	case AnomalySignalErrorRate:
		return []string{
			"forge obs traces " + env + " " + svc + " --errors " + since,
			"forge obs issues " + env + " " + svc,
			"forge obs metrics query " + env + " rpc.server.errors " + svc + " --agg increase " + week,
			"forge obs check " + env,
		}
	case AnomalySignalP95:
		return []string{
			"forge obs traces " + env + " " + svc + " --slow " + since,
			"forge obs metrics query " + env + " rpc.server.duration " + svc + " --agg p95 " + week,
			"forge obs check " + env,
		}
	}
	return []string{
		"forge obs metrics query " + env + " rpc.server.calls " + svc + " --agg rate " + week,
		"forge obs changes " + env + " " + since,
		"forge obs check " + env,
	}
}
