package obsquery

import (
	"strconv"
	"strings"
	"time"
)

// The infra telemetry the node agent delivers (console v2 contract K2).
// kubeletstats supplies usage and the optional utilization ratios;
// k8s_cluster supplies restarts and the absolute requests and limits. All are
// gauges, with these resource attributes: reliant.env.id, k8s.namespace.name,
// k8s.pod.name, k8s.pod.uid, k8s.container.name, k8s.workload.name,
// container.image.name, container.image.tag; service.name = the workload name.
const (
	MetricCPUUsage       = "container.cpu.usage"                      // {cpu}
	MetricMemWorkingSet  = "container.memory.working_set"             // By
	MetricCPULimitUtil   = "k8s.container.cpu_limit_utilization"      // ratio
	MetricCPUReqUtil     = "k8s.container.cpu_request_utilization"    // ratio
	MetricMemLimitUtil   = "k8s.container.memory_limit_utilization"   // ratio
	MetricMemReqUtil     = "k8s.container.memory_request_utilization" // ratio
	MetricCPULimit       = "k8s.container.cpu_limit"                  // {cpu}
	MetricCPURequest     = "k8s.container.cpu_request"                // {cpu}
	MetricMemLimit       = "k8s.container.memory_limit"               // By
	MetricMemRequest     = "k8s.container.memory_request"             // By
	MetricRestarts       = "k8s.container.restarts"                   // {restart}
	MetricContainerReady = "k8s.container.ready"                      // 1 = passed its readiness probe, 0 = not
	attrLastTerminated   = "k8s.container.last_terminated_reason"
	attrEventReason      = "k8s.event.reason"
	attrEventType        = "k8s.event.type"
	attrEventCount       = "k8s.event.count"
	attrObjectKind       = "k8s.object.kind"
	attrObjectName       = "k8s.object.name"
	recentReplicaWindow  = 5 * time.Minute
	MaxWorkloadRows      = 20000
	MaxWorkloadEvents    = 500
	MaxWorkloadChanges   = 200
	MaxWorkloadSeriesRow = 200 * MaxPointsPerSeries
)

// UsageMetrics are the gauges ListWorkloads summarizes per container.
var UsageMetrics = []string{
	MetricCPUUsage, MetricMemWorkingSet,
	MetricCPULimitUtil, MetricCPUReqUtil, MetricMemLimitUtil, MetricMemReqUtil,
	MetricCPULimit, MetricCPURequest, MetricMemLimit, MetricMemRequest,
}

// SeriesMetrics are the gauges GetWorkload charts (restarts are separate).
var SeriesMetrics = []string{
	MetricCPUUsage, MetricCPULimitUtil, MetricCPUReqUtil,
	MetricMemWorkingSet, MetricMemLimitUtil, MetricMemReqUtil,
}

const (
	wlEnv       = "ResourceAttributes['reliant.env.id']"
	wlWorkload  = "ResourceAttributes['k8s.workload.name']"
	wlContainer = "ResourceAttributes['k8s.container.name']"
	wlPod       = "if(ResourceAttributes['k8s.pod.uid'] != '', ResourceAttributes['k8s.pod.uid'], ResourceAttributes['k8s.pod.name'])"
)

// Workloads is the common shape of the workload statements.
type Workloads struct {
	Scope Scope
	// Workload narrows to one k8s.workload.name (GetWorkload); empty = all.
	Workload string
	// Container narrows to one container; empty = all.
	Container string
}

func (q Workloads) validate() error {
	if err := q.Scope.validate(MaxRangeOverview); err != nil {
		return err
	}
	if len(q.Workload) > 256 || len(q.Container) > 256 {
		return invalid("workload and container names are at most 256 characters")
	}
	return nil
}

// metricConds are the predicates every container-metric statement shares.
// Only RESOURCE-level scope filters apply: a span-level key does not describe
// infra telemetry and would silently empty it.
func (q Workloads) metricConds(b *builder, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, invalid("internal: no metric names")
	}
	ps := make([]string, len(names))
	for i, n := range names {
		ps[i] = b.str(n)
	}
	conds := []string{
		b.rangeCondSec("TimeUnix", q.Scope),
		"MetricName IN (" + strings.Join(ps, ", ") + ")",
		wlWorkload + " != ''",
		wlContainer + " != ''",
	}
	rc, err := b.resourceConds(SigMetrics, q.Scope)
	if err != nil {
		return nil, err
	}
	conds = append(conds, rc...)
	if q.Workload != "" {
		conds = append(conds, wlWorkload+" = "+b.str(q.Workload))
	}
	if q.Container != "" {
		conds = append(conds, wlContainer+" = "+b.str(q.Container))
	}
	return conds, nil
}

// Usage summarizes every container's usage, utilization, requests and limits.
// Columns: env, workload, container, metric, latest (highest per-pod latest),
// p95 (over every sample of every pod), ns, img, tag (latest non-empty).
func (q Workloads) Usage() (Stmt, error) {
	if err := q.validate(); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	conds, err := q.metricConds(b, UsageMetrics)
	if err != nil {
		return Stmt{}, err
	}
	return b.stmt("SELECT env, workload, container, MetricName AS metric,\n" +
		"       max(lv) AS latest, quantileMerge(0.95)(qs) AS p95,\n" +
		"       argMaxIf(ns0, lt, ns0 != '') AS ns, argMaxIf(img0, lt, img0 != '') AS img, argMaxIf(tag0, lt, tag0 != '') AS tag\n" +
		"FROM (\n" +
		"  SELECT " + wlEnv + " AS env, " + wlWorkload + " AS workload, " + wlContainer + " AS container, " + wlPod + " AS pod, MetricName,\n" +
		"         argMax(Value, TimeUnix) AS lv, quantileState(0.95)(Value) AS qs, max(TimeUnix) AS lt,\n" +
		"         argMaxIf(ResourceAttributes['k8s.namespace.name'], TimeUnix, ResourceAttributes['k8s.namespace.name'] != '') AS ns0,\n" +
		"         argMaxIf(ResourceAttributes['container.image.name'], TimeUnix, ResourceAttributes['container.image.name'] != '') AS img0,\n" +
		"         argMaxIf(ResourceAttributes['container.image.tag'], TimeUnix, ResourceAttributes['container.image.tag'] != '') AS tag0\n" +
		"  FROM " + b.table(q.Scope, MetricGauge.table()) + "\n  " + where(conds) + "\n" +
		"  GROUP BY env, workload, container, pod, MetricName\n" +
		") GROUP BY env, workload, container, MetricName\n" +
		"ORDER BY env, workload, container, MetricName LIMIT " + strconv.Itoa(MaxWorkloadRows)), nil
}

// Replicas counts the pods a workload has running: the distinct pods that
// reported within replicaWindow of the workload's own latest sample (so a
// historical range still answers, rather than counting zero). Columns: env,
// workload, replicas, last_seen (DateTime: the workload's latest sample, so a
// reader can tell a live workload from one that stopped reporting).
func (q Workloads) Replicas() (Stmt, error) {
	if err := q.validate(); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	conds, err := q.metricConds(b, []string{MetricCPUUsage, MetricMemWorkingSet})
	if err != nil {
		return Stmt{}, err
	}
	return b.stmt("SELECT env, workload, uniqExactIf(pod, lt >= mx - " + b.u32(uint32(recentReplicaWindow.Seconds())) + ") AS replicas, max(lt) AS last_seen FROM (\n" +
		"  SELECT env, workload, pod, lt, max(lt) OVER (PARTITION BY env, workload) AS mx FROM (\n" +
		"    SELECT " + wlEnv + " AS env, " + wlWorkload + " AS workload, " + wlPod + " AS pod, max(TimeUnix) AS lt\n" +
		"    FROM " + b.table(q.Scope, MetricGauge.table()) + "\n    " + where(conds) + "\n" +
		"    GROUP BY env, workload, pod\n  )\n) GROUP BY env, workload ORDER BY env, workload LIMIT " + strconv.Itoa(MaxWorkloadRows)), nil
}

// Restarts counts restarts per container: for each pod the growth of
// k8s.container.restarts within the range, summed. Columns: env, workload,
// container, restarts.
func (q Workloads) Restarts() (Stmt, error) {
	if err := q.validate(); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	conds, err := q.metricConds(b, []string{MetricRestarts})
	if err != nil {
		return Stmt{}, err
	}
	return b.stmt("SELECT env, workload, container, sum(d) AS restarts FROM (\n" +
		"  SELECT " + wlEnv + " AS env, " + wlWorkload + " AS workload, " + wlContainer + " AS container, " + wlPod + " AS pod,\n" +
		"         max(Value) - min(Value) AS d\n" +
		"  FROM " + b.table(q.Scope, MetricGauge.table()) + "\n  " + where(conds) + "\n" +
		"  GROUP BY env, workload, container, pod\n" +
		") GROUP BY env, workload, container ORDER BY env, workload, container LIMIT " + strconv.Itoa(MaxWorkloadRows)), nil
}

// Terminations finds each container's last terminated reason, read from the
// k8s.container.last_terminated_reason attribute of its restart gauge or of an
// event log (the agent may deliver either, contract K2). Columns: env,
// workload, container, reason, at (DateTime), in_window (UInt8).
//
// From the gauge, `at` is when the reason was first reported, and in_window is
// 0 when it was already there at the container's first sample in the range (so
// the real time is unknown). From an event log `at` is the event time and
// in_window is 1.
func (q Workloads) Terminations() (Stmt, error) {
	if err := q.validate(); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	mconds, err := q.metricConds(b, []string{MetricRestarts})
	if err != nil {
		return Stmt{}, err
	}
	mreason, _, err := b.keyExpr(SigMetrics, attrLastTerminated)
	if err != nil {
		return Stmt{}, err
	}
	lreason, _, err := b.keyExpr(SigLogs, attrLastTerminated)
	if err != nil {
		return Stmt{}, err
	}
	lconds, err := q.logConds(b)
	if err != nil {
		return Stmt{}, err
	}
	lconds = append(lconds, lreason+" != ''")
	return b.stmt("SELECT env, workload, container, argMax(r0, t0) AS reason, max(t0) AS at, argMax(w0, t0) AS in_window FROM (\n" +
		"  SELECT env, workload, container, r0, min(t) AS t0, toUInt8(min(t) > any(pod_first)) AS w0 FROM (\n" +
		"    SELECT env, workload, container, r0, t, min(t) OVER (PARTITION BY env, workload, container, pod) AS pod_first FROM (\n" +
		"      SELECT " + wlEnv + " AS env, " + wlWorkload + " AS workload, " + wlContainer + " AS container, " + wlPod + " AS pod,\n" +
		"             TimeUnix AS t, " + mreason + " AS r0\n" +
		"      FROM " + b.table(q.Scope, MetricGauge.table()) + "\n      " + where(mconds) + "\n" +
		"    )\n" +
		"  ) WHERE r0 != '' GROUP BY env, workload, container, r0\n" +
		"  UNION ALL\n" +
		"  SELECT " + wlEnv + " AS env, " + wlWorkload + " AS workload, " + wlContainer + " AS container, " + lreason + " AS r0,\n" +
		"         toDateTime(Timestamp) AS t0, toUInt8(1) AS w0\n" +
		"  FROM " + b.table(q.Scope, "otel_logs") + "\n  " + where(lconds) + "\n" +
		") GROUP BY env, workload, container ORDER BY env, workload, container LIMIT " + strconv.Itoa(MaxWorkloadRows)), nil
}

// logConds are the predicates shared by the event-log statements: every scope
// filter applies (an event key narrows events), plus the workload and
// container narrowing.
func (q Workloads) logConds(b *builder) ([]string, error) {
	conds := []string{b.rangeCond("Timestamp", q.Scope), wlWorkload + " != ''"}
	sc, err := b.scopeConds(SigLogs, q.Scope)
	if err != nil {
		return nil, err
	}
	conds = append(conds, sc...)
	if q.Workload != "" {
		conds = append(conds, wlWorkload+" = "+b.str(q.Workload))
	}
	if q.Container != "" {
		conds = append(conds, wlContainer+" = "+b.str(q.Container))
	}
	return conds, nil
}

// eventSelect is the shared select list of the event statements. Columns:
// Timestamp, env, workload, type, reason, message, count, kind, name,
// container.
func (b *builder) eventSelect(q Workloads) (sel string, conds []string, err error) {
	exprs := make([]string, 0, 5)
	for _, k := range []string{attrEventType, attrEventReason, attrEventCount, attrObjectKind, attrObjectName} {
		e, _, err := b.keyExpr(SigLogs, k)
		if err != nil {
			return "", nil, err
		}
		exprs = append(exprs, e)
	}
	conds, err = q.logConds(b)
	if err != nil {
		return "", nil, err
	}
	conds = append(conds, exprs[1]+" != ''")
	sel = "SELECT Timestamp, " + wlEnv + " AS env, " + wlWorkload + " AS workload, " + exprs[0] + " AS type, " + exprs[1] + " AS reason,\n" +
		"       Body AS message, " + exprs[2] + " AS cnt, " + exprs[3] + " AS kind, " + exprs[4] + " AS name, " + wlContainer + " AS container\n"
	return sel, conds, nil
}

// LatestWarnings returns each workload's most recent Warning event. Columns as
// eventSelect.
func (q Workloads) LatestWarnings() (Stmt, error) {
	if err := q.validate(); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	sel, conds, err := b.eventSelect(q)
	if err != nil {
		return Stmt{}, err
	}
	typ, _, err := b.keyExpr(SigLogs, attrEventType)
	if err != nil {
		return Stmt{}, err
	}
	conds = append(conds, "lower("+typ+") = 'warning'")
	return b.stmt(sel + "FROM " + b.table(q.Scope, "otel_logs") + "\n" + where(conds) + "\n" +
		"ORDER BY Timestamp DESC LIMIT 1 BY env, workload LIMIT " + strconv.Itoa(MaxWorkloadRows)), nil
}

// Events returns the workload's events, newest first (one more than
// MaxWorkloadEvents, to detect the cut). Columns as eventSelect.
func (q Workloads) Events() (Stmt, error) {
	if err := q.validate(); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	sel, conds, err := b.eventSelect(q)
	if err != nil {
		return Stmt{}, err
	}
	return b.stmt(sel + "FROM " + b.table(q.Scope, "otel_logs") + "\n" + where(conds) + "\n" +
		"ORDER BY Timestamp DESC LIMIT " + strconv.Itoa(MaxWorkloadEvents+1)), nil
}

// ImageChanges lists the distinct images each container ran. Columns: env,
// container, img, tag, first_seen (DateTime), last_seen (DateTime); oldest
// first, one more than MaxWorkloadChanges.
func (q Workloads) ImageChanges() (Stmt, error) {
	if err := q.validate(); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	conds, err := q.metricConds(b, []string{MetricCPUUsage, MetricMemWorkingSet})
	if err != nil {
		return Stmt{}, err
	}
	conds = append(conds, "ResourceAttributes['container.image.name'] != ''")
	return b.stmt("SELECT " + wlEnv + " AS env, " + wlContainer + " AS container,\n" +
		"       ResourceAttributes['container.image.name'] AS img, ResourceAttributes['container.image.tag'] AS tag,\n" +
		"       min(TimeUnix) AS first_seen, max(TimeUnix) AS last_seen\n" +
		"FROM " + b.table(q.Scope, MetricGauge.table()) + "\n" + where(conds) + "\n" +
		"GROUP BY env, container, img, tag ORDER BY first_seen, env, container LIMIT " + strconv.Itoa(MaxWorkloadChanges+1)), nil
}

// Versions lists the service.version values the services' OWN telemetry (spans
// and metrics; service.name = the workload name) reported. Services limits it
// to those names; GetWorkload passes the workload. Columns: env, svc, version,
// first_seen (DateTime), last_seen (DateTime), oldest first.
func (q Workloads) Versions(services []string) (Stmt, error) {
	if err := q.validate(); err != nil {
		return Stmt{}, err
	}
	if len(services) == 0 || len(services) > MaxScopeEnvs {
		return Stmt{}, invalid("a version lookup names 1-%d services", MaxScopeEnvs)
	}
	b := newBuilder(q.Scope)
	svc := "has(" + b.strArray(services) + ", ServiceName)"
	ver := "ResourceAttributes['service.version']"
	rc, err := b.resourceConds(SigTraces, q.Scope)
	if err != nil {
		return Stmt{}, err
	}
	tconds := append([]string{b.rangeCond("Timestamp", q.Scope), svc, ver + " != ''"}, rc...)
	rcm, err := b.resourceConds(SigMetrics, q.Scope)
	if err != nil {
		return Stmt{}, err
	}
	mconds := append([]string{b.rangeCondSec("TimeUnix", q.Scope), svc, ver + " != ''"}, rcm...)
	return b.stmt("SELECT env, svc, version, min(f0) AS first_seen, max(l0) AS last_seen FROM (\n" +
		"  SELECT " + wlEnv + " AS env, ServiceName AS svc, " + ver + " AS version, toDateTime(min(Timestamp)) AS f0, toDateTime(max(Timestamp)) AS l0\n" +
		"  FROM " + b.table(q.Scope, "otel_traces") + " " + where(tconds) + " GROUP BY env, svc, version\n" +
		"  UNION ALL\n" +
		"  SELECT " + wlEnv + " AS env, ServiceName AS svc, " + ver + " AS version, min(TimeUnix) AS f0, max(TimeUnix) AS l0\n" +
		"  FROM " + b.table(q.Scope, MetricGauge.table()) + " " + where(mconds) + " GROUP BY env, svc, version\n" +
		") GROUP BY env, svc, version ORDER BY first_seen, env, svc LIMIT " + strconv.Itoa(MaxWorkloadChanges+1)), nil
}

// Series charts the gauges over time: per pod the last value in each bucket,
// then the worst (highest) replica. Columns: env, container, metric, bucket
// (DateTime), value.
func (q Workloads) Series(stepSec int64) (Stmt, error) {
	if err := q.validate(); err != nil {
		return Stmt{}, err
	}
	if q.Workload == "" {
		return Stmt{}, invalid("a workload series needs a workload")
	}
	if stepSec < 1 || int64(q.Scope.End.Sub(q.Scope.Start).Seconds())/stepSec > MaxPointsPerSeries {
		return Stmt{}, invalid("step %ds would return more than %d points per series", stepSec, MaxPointsPerSeries)
	}
	b := newBuilder(q.Scope)
	conds, err := q.metricConds(b, SeriesMetrics)
	if err != nil {
		return Stmt{}, err
	}
	bucket := "toStartOfInterval(TimeUnix, toIntervalSecond(" + b.u32(uint32(stepSec)) + "))"
	return b.stmt("SELECT env, container, MetricName AS metric, bucket, max(v) AS value FROM (\n" +
		"  SELECT " + wlEnv + " AS env, " + wlContainer + " AS container, " + wlPod + " AS pod, MetricName, " + bucket + " AS bucket,\n" +
		"         argMax(Value, TimeUnix) AS v\n" +
		"  FROM " + b.table(q.Scope, MetricGauge.table()) + "\n  " + where(conds) + "\n" +
		"  GROUP BY env, container, pod, MetricName, bucket\n" +
		") GROUP BY env, container, MetricName, bucket ORDER BY env, container, MetricName, bucket LIMIT " + strconv.Itoa(MaxWorkloadSeriesRow)), nil
}

// RestartSeries charts restarts per bucket: the sum over pods of the growth of
// k8s.container.restarts (a decrease is a reset and counts as zero). Columns:
// env, container, bucket (DateTime), value.
func (q Workloads) RestartSeries(stepSec int64) (Stmt, error) {
	if err := q.validate(); err != nil {
		return Stmt{}, err
	}
	if q.Workload == "" {
		return Stmt{}, invalid("a workload series needs a workload")
	}
	if stepSec < 1 || int64(q.Scope.End.Sub(q.Scope.Start).Seconds())/stepSec > MaxPointsPerSeries {
		return Stmt{}, invalid("step %ds would return more than %d points per series", stepSec, MaxPointsPerSeries)
	}
	b := newBuilder(q.Scope)
	conds, err := q.metricConds(b, []string{MetricRestarts})
	if err != nil {
		return Stmt{}, err
	}
	bucket := "toStartOfInterval(t, toIntervalSecond(" + b.u32(uint32(stepSec)) + "))"
	return b.stmt("SELECT env, container, " + bucket + " AS bucket, sum(inc) AS value FROM (\n" +
		"  SELECT env, container, t, if(r >= prev, r - prev, 0) AS inc FROM (\n" +
		"    SELECT env, container, t, r, lagInFrame(r, 1, r) OVER (PARTITION BY env, container, pod ORDER BY t ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING) AS prev FROM (\n" +
		"      SELECT " + wlEnv + " AS env, " + wlContainer + " AS container, " + wlPod + " AS pod, TimeUnix AS t, Value AS r\n" +
		"      FROM " + b.table(q.Scope, MetricGauge.table()) + "\n      " + where(conds) + "\n" +
		"    )\n  )\n) GROUP BY env, container, bucket ORDER BY env, container, bucket LIMIT " + strconv.Itoa(MaxWorkloadSeriesRow)), nil
}

// MinReadinessStepSec is the finest Readiness bucket: the cluster collector
// delivers k8s.container.ready once a minute, so a finer bucket would be mostly
// empty.
const MinReadinessStepSec = 60

// Readiness is the continuous health signal: per workload and time bucket, how
// many pods reported, how many of them were ready, and how many restarts
// happened, read from the k8s_cluster receiver's k8s.container.ready and
// k8s.container.restarts gauges. It is a pure function of stored telemetry, so a
// verdict derived from it needs no reader to have looked at the time.
//
// A pod counts as ready in a bucket when EVERY sample of EVERY one of its
// containers in the bucket is 1, so a dip inside a coarse bucket is not lost. A bucket in which a workload delivered
// no readiness sample has no row: a gap is missing data, never a healthy bucket,
// and the reader must treat it so. Restarts are the growth of the counter
// between consecutive samples of a container (a decrease is a reset and counts
// as zero), attributed to the bucket of the later sample.
//
// Columns: env, workload, bucket (DateTime), pods (UInt64: distinct pods that
// reported readiness), ready (UInt64: of those, the ones fully ready), restarts
// (Float64), exit (String: the last terminated reason reported in the bucket,
// "" when none). Ordered by env, workload, bucket; at most MaxWorkloadSeriesRow
// rows. A row with pods = 0 carries restarts only.
func (q Workloads) Readiness(stepSec int64) (Stmt, error) {
	if err := q.validate(); err != nil {
		return Stmt{}, err
	}
	if stepSec < MinReadinessStepSec || int64(q.Scope.End.Sub(q.Scope.Start).Seconds())/stepSec > MaxPointsPerSeries {
		return Stmt{}, invalid("step %ds must be at least %ds and return at most %d buckets per workload", stepSec, MinReadinessStepSec, MaxPointsPerSeries)
	}
	b := newBuilder(q.Scope)
	readyConds, err := q.metricConds(b, []string{MetricContainerReady})
	if err != nil {
		return Stmt{}, err
	}
	restartConds, err := q.metricConds(b, []string{MetricRestarts})
	if err != nil {
		return Stmt{}, err
	}
	reason, _, err := b.keyExpr(SigMetrics, attrLastTerminated)
	if err != nil {
		return Stmt{}, err
	}
	readyStep, restartStep := b.u32(uint32(stepSec)), b.u32(uint32(stepSec))
	table := b.table(q.Scope, MetricGauge.table())
	return b.stmt("SELECT env, workload, bucket, sum(pods) AS pods, sum(ready) AS ready, sum(restarts) AS restarts, maxIf(exit, exit != '') AS exit FROM (\n" +
		"  SELECT env, workload, bucket, toUInt64(count()) AS pods, toUInt64(countIf(pr = 1)) AS ready, toFloat64(0) AS restarts, '' AS exit FROM (\n" +
		"    SELECT env, workload, pod, bucket, min(cv) AS pr FROM (\n" +
		"      SELECT " + wlEnv + " AS env, " + wlWorkload + " AS workload, " + wlContainer + " AS container, " + wlPod + " AS pod,\n" +
		"             toStartOfInterval(TimeUnix, toIntervalSecond(" + readyStep + ")) AS bucket, min(Value) AS cv\n" +
		"      FROM " + table + "\n      " + where(readyConds) + "\n" +
		"      GROUP BY env, workload, container, pod, bucket\n" +
		"    ) GROUP BY env, workload, pod, bucket\n" +
		"  ) GROUP BY env, workload, bucket\n" +
		"  UNION ALL\n" +
		"  SELECT env, workload, bucket, toUInt64(0) AS pods, toUInt64(0) AS ready, sum(inc) AS restarts, argMaxIf(r, t, r != '') AS exit FROM (\n" +
		"    SELECT env, workload, toStartOfInterval(t, toIntervalSecond(" + restartStep + ")) AS bucket, t, r, toFloat64(if(v >= prev, v - prev, 0)) AS inc FROM (\n" +
		"      SELECT env, workload, t, v, r, lagInFrame(v, 1, v) OVER (PARTITION BY env, workload, container, pod ORDER BY t ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING) AS prev FROM (\n" +
		"        SELECT " + wlEnv + " AS env, " + wlWorkload + " AS workload, " + wlContainer + " AS container, " + wlPod + " AS pod,\n" +
		"               TimeUnix AS t, Value AS v, " + reason + " AS r\n" +
		"        FROM " + table + "\n        " + where(restartConds) + "\n" +
		"      )\n    )\n  ) GROUP BY env, workload, bucket\n" +
		") GROUP BY env, workload, bucket ORDER BY env, workload, bucket LIMIT " + strconv.Itoa(MaxWorkloadSeriesRow)), nil
}

// Pods summarizes every pod's presence in the range: first and last sample
// and the node. Columns: env, workload, pod_name, pod_uid, first_seen
// (DateTime), last_seen (DateTime), node. Newest first, MaxWorkloadRows.
//
// The node is the k8s.node.name the node agent delivers; it is empty for data
// from an agent that strips it.
func (q Workloads) Pods() (Stmt, error) {
	if err := q.validate(); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	conds, err := q.metricConds(b, []string{MetricCPUUsage, MetricMemWorkingSet})
	if err != nil {
		return Stmt{}, err
	}
	return b.stmt("SELECT " + wlEnv + " AS env, " + wlWorkload + " AS workload,\n" +
		"       ResourceAttributes['k8s.pod.name'] AS pod_name, ResourceAttributes['k8s.pod.uid'] AS pod_uid,\n" +
		"       min(TimeUnix) AS first_seen, max(TimeUnix) AS last_seen,\n" +
		"       argMaxIf(ResourceAttributes['k8s.node.name'], TimeUnix, ResourceAttributes['k8s.node.name'] != '') AS node\n" +
		"FROM " + b.table(q.Scope, MetricGauge.table()) + "\n" + where(conds) + "\n" +
		"GROUP BY env, workload, pod_name, pod_uid ORDER BY last_seen DESC, env, workload, pod_name LIMIT " + strconv.Itoa(MaxWorkloadRows)), nil
}

// PodRestarts gives, per pod and container, the growth of
// k8s.container.restarts in the range and the last terminated reason. Columns:
// env, workload, pod_name, pod_uid, container, restarts, first (DateTime: the
// container's first sample in the range), reason, reason_at (DateTime: when the
// reason was first reported). A reason already present at the first sample has
// reason_at == first: the real exit time is then unknown.
func (q Workloads) PodRestarts() (Stmt, error) {
	if err := q.validate(); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	conds, err := q.metricConds(b, []string{MetricRestarts})
	if err != nil {
		return Stmt{}, err
	}
	reason, _, err := b.keyExpr(SigMetrics, attrLastTerminated)
	if err != nil {
		return Stmt{}, err
	}
	return b.stmt("SELECT env, workload, pod_name, pod_uid, container, max(v) - min(v) AS restarts, min(t) AS first,\n" +
		"       argMaxIf(r, t, r != '') AS reason, minIf(t, r != '') AS reason_at\n" +
		"FROM (\n" +
		"  SELECT " + wlEnv + " AS env, " + wlWorkload + " AS workload, " + wlContainer + " AS container,\n" +
		"         ResourceAttributes['k8s.pod.name'] AS pod_name, ResourceAttributes['k8s.pod.uid'] AS pod_uid,\n" +
		"         TimeUnix AS t, Value AS v, " + reason + " AS r\n" +
		"  FROM " + b.table(q.Scope, MetricGauge.table()) + "\n  " + where(conds) + "\n" +
		") GROUP BY env, workload, pod_name, pod_uid, container ORDER BY env, workload, pod_name, container LIMIT " + strconv.Itoa(MaxWorkloadRows)), nil
}

// The reasons a Job's own events carry (the Job controller's, upstream
// kubernetes): one run starts with SuccessfulCreate and ends with Completed or
// one of the failure reasons.
const (
	JobEventCreated   = "SuccessfulCreate"
	JobEventCompleted = "Completed"
)

// JobFailureReasons are the Job events that end a run in failure.
var JobFailureReasons = []string{"BackoffLimitExceeded", "DeadlineExceeded", "PodFailurePolicy"}

// MaxJobs bounds the Jobs one request returns (one more is read, to detect the cut).
const MaxJobs = 200

// Jobs finds the Job runs in the range from the Jobs' own Kubernetes events
// (k8s.object.kind = Job). The cluster collector delivers them with the Job's
// name as the workload. Columns: env, job, created_n (UInt64), started
// (DateTime), done_n (UInt64), finished (DateTime), failed_n (UInt64),
// failed_at (DateTime), reason, last (DateTime). Newest activity first, one
// more than MaxJobs. A count of 0 means the event was not seen, and the
// matching time is then meaningless.
func (q Workloads) Jobs() (Stmt, error) {
	if err := q.validate(); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	reasonE, _, err := b.keyExpr(SigLogs, attrEventReason)
	if err != nil {
		return Stmt{}, err
	}
	kindE, _, err := b.keyExpr(SigLogs, attrObjectKind)
	if err != nil {
		return Stmt{}, err
	}
	conds, err := q.logConds(b)
	if err != nil {
		return Stmt{}, err
	}
	conds = append(conds, reasonE+" != ''", kindE+" = "+b.str("Job"))
	fail := b.strArray(JobFailureReasons)
	created, completed := b.str(JobEventCreated), b.str(JobEventCompleted)
	return b.stmt("SELECT " + wlEnv + " AS env, " + wlWorkload + " AS job,\n" +
		"       countIf(" + reasonE + " = " + created + ") AS created_n, toDateTime(minIf(Timestamp, " + reasonE + " = " + created + ")) AS started,\n" +
		"       countIf(" + reasonE + " = " + completed + ") AS done_n, toDateTime(maxIf(Timestamp, " + reasonE + " = " + completed + ")) AS finished,\n" +
		"       countIf(has(" + fail + ", " + reasonE + ")) AS failed_n, toDateTime(maxIf(Timestamp, has(" + fail + ", " + reasonE + "))) AS failed_at,\n" +
		"       argMaxIf(" + reasonE + ", Timestamp, has(" + fail + ", " + reasonE + ")) AS reason, toDateTime(max(Timestamp)) AS last\n" +
		"FROM " + b.table(q.Scope, "otel_logs") + "\n" + where(conds) + "\n" +
		"GROUP BY env, job ORDER BY last DESC, env, job LIMIT " + strconv.Itoa(MaxJobs+1)), nil
}
