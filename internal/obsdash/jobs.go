package obsdash

import (
	"fmt"
	"sort"
	"strings"
)

// JobsID is the id of the one dashboard every job and cron job shares, so its
// file is "<project>-jobs_gen.json".
const JobsID = "jobs"

// maxReplaces is dashspec.MaxReplaces.
const maxReplaces = 32

// The Job controller's event reasons (see obsquery.Workloads.Jobs).
const (
	jobEventCreated   = "SuccessfulCreate"
	jobEventCompleted = "Completed"
)

var jobFailureReasons = []string{"BackoffLimitExceeded", "DeadlineExceeded", "PodFailurePolicy"}

// jobRunSuffix is what follows a job's forge name in the name of one run: a
// standalone Job's spec hash (forge renders 10 hex), or a CronJob's
// scheduled-time stamp (decimal minutes since the epoch). Hex only, so a job
// "app" does not claim the runs of a job named "app-worker".
const jobRunSuffix = `-[0-9a-f]{6,12}`

// BuildJobs is the "Jobs" dashboard: every job and cron job of the project in
// one place, instead of one dashboard per job that is empty between runs.
//
// A run is read from the Job controller's Kubernetes events, so it needs the
// cluster collector and a deployed environment; a local forge env up draws it
// empty. A JOB is a forge process (obsdash.Process): the workloads that run one
// command, which an environment may name differently.
//
// EVERY panel is a SQL panel that selects through jobResolverSQL. That is not a
// style choice. The console hands the dashboard's service selector to every
// declarative panel as ServiceName IN (...) and a panel cannot opt out. The
// telemetry a job leaves carries three different names (metrics the workload,
// logs the service.name, events the Job object's), so a declarative panel
// selects a different subset than its neighbours, or nothing. A SQL panel
// receives the selection only through the resolver, which turns it into jobs
// and then filters each source by the identity that source carries.
//
// jobs must be non-empty; Build only calls it when the project has some.
func BuildJobs(project string, jobs []Process) Dashboard {
	d := Dashboard{
		ID:    JobsID,
		Title: project + ": jobs",
		Description: "Every job and cron job of the project: runs, failures, duration and last run per job, from the Kubernetes Job events, " +
			"plus container restarts, warning events and error logs. Present in deployed environments only. " +
			"The service selector picks a job by its name, a workload name, a service.name or a run name, on every panel alike.",
		Group: GroupJobs,
		Tags:  []string{"jobs"},
		// One filter, and it is the service selector: every panel is SQL and
		// receives it only through the resolver. A deployment.environment.name
		// filter would be a second raw selection no SQL panel can see (the
		// environment is the console's scope, bound as {env_ids}).
		Filters: []Filter{{ID: "service", Label: "service.name", Field: FieldService, Signal: Logs}},
	}
	d.Sections = []Section{{ID: "runs", Title: "Runs"}, {ID: "health", Title: "Health"}, {ID: "logs", Title: "Logs"}}
	d.Replaces = jobReplaces(jobs)

	const perRun = " A run is one Kubernetes Job in one environment, however many pods it created."
	sql := func(id, section, title, desc, query string, w, h int) Panel {
		return Panel{ID: id, Section: section, Title: title, Description: desc, Kind: KindSQL, SQL: query, Width: w, Height: h}
	}
	d.Panels = []Panel{
		sql("jobs-summary", "runs", "Runs per job",
			"Per job over the selected range: runs seen, succeeded and failed, the latest run's start, and run durations (seconds, start to completion). One row per forge job."+perRun,
			jobsSummarySQL(jobs), 24, 8),
		sql("runs-totals", "runs", "Runs in range",
			"Runs started in the selected range, and how many of the runs seen in it succeeded and failed, across every job."+perRun,
			jobsTotalsSQL(jobs), 24, 4),
		sql("runs-by-job", "runs", "Runs started by job", "Runs started over time, one line per job."+perRun,
			jobsByJobSQL(jobs, "started", "started > 0"), 12, 7),
		sql("failures-by-job", "runs", "Failed runs by job",
			"Runs that ended in failure over time, one line per job, at the time of the failure."+perRun,
			jobsByJobSQL(jobs, "failed_at", "failed"), 12, 7),
		sql("recent-runs", "runs", "Recent runs",
			"The newest runs, one row each: job, Kubernetes Job, environment, outcome (Completed, Running, or the failure reason such as BackoffLimitExceeded), start and duration in seconds."+perRun,
			jobsRecentRunsSQL(jobs), 24, 8),
		sql("warning-events", "health", "Warning events by job",
			"Warning-type Kubernetes events of the jobs, over time, one line per job: the Job's own (a run that hit its backoff limit or deadline) and its pods' (failed image pulls, evictions, failed scheduling, back-offs).",
			jobsWarningsSQL(jobs), 12, 7),
		sql("restarts", "health", "Container restarts",
			"The highest restart count among each job's containers, one line per job; a step is a restart.",
			jobsRestartsSQL(jobs), 12, 7),
		sql("error-logs", "logs", "Error logs",
			"The newest error-level log records of the jobs, newest first: job, service, severity, time and message.",
			jobsErrorLogsSQL(jobs), 24, 8),
	}
	return d
}

// The shared SQL fragments. Every Jobs panel is built from these and from
// nothing else that names a job, so there is one definition of what a job is
// and of how a selection picks it.
const (
	// jobSelectionParam is the dashboard's service selector. It appears in
	// jobResolverSQL and nowhere else: a panel that bound it itself would be
	// selecting by the raw string, which is the defect this file exists to end.
	jobSelectionParam = "{services:Array(String)}"

	// jobEnvScope is the dashboard's environment selector over one source row.
	jobEnvScope = "(empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, ResourceAttributes['reliant.env.id']))"

	// jobRange is the dashboard's time range over a table with a Timestamp.
	jobRange = "Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)}"
)

// jobResolverSQL is the ONE job-identity resolver: the CTE definitions
//
//	job_identity   one tuple per job, (name, workloads, service_names)
//	selected_jobs  the names of the jobs the dashboard's selection picks
//
// A job is its process: the forge name (Process.Name), every workload that runs
// its command (the name an environment gives it, so "migrate" and
// "migrate-dev"), and every service.name those workloads report (their
// OTEL_SERVICE_NAME, else the workload name). A selection picks a job when it is
//
//	a service.name of the job, or
//	a workload of the job, bare or with a run suffix (a spec hash, or a cron's
//	scheduled time), which also covers the forge name and any run's name.
//
// An empty selection picks every job. A run name needs no list: it is matched,
// not enumerated.
//
// A source row is then mapped to its job by the identity that source carries
// (jobOfName, jobOfService) and kept when its job is selected (jobSource).
// Names are forge workload names (DNS labels) and service names (an env var),
// embedded as quoted literals.
func jobResolverSQL(jobs []Process) string {
	list := func(xs []string) string {
		q := make([]string, len(xs))
		for i, x := range xs {
			q[i] = sqlQuote(x)
		}
		return "[" + strings.Join(q, ", ") + "]"
	}
	rows := make([]string, len(jobs))
	for i, p := range jobs {
		rows[i] = "(" + sqlQuote(p.Name) + ", " + list(p.Workloads) + ", " + list(p.ServiceNames) + ")"
	}
	return fmt.Sprintf(`[%[1]s] AS job_identity,
  arrayMap(j -> j.1, if(empty(%[2]s), job_identity,
    arrayFilter(j -> arrayExists(s -> has(j.3, s) OR arrayExists(w -> match(s, concat('^', w, '(%[3]s)?$')), j.2), %[2]s), job_identity))) AS selected_jobs`,
		strings.Join(rows, ", "), jobSelectionParam, jobRunSuffix)
}

// jobOfName is the job a workload name, or the name of one of its runs, belongs
// to (” for another project's). Metrics carry the workload name and events the
// involved object's name: a pod's is the bare forge name (its
// app.kubernetes.io/name label), a Job object's carries the run suffix.
func jobOfName(column string) string {
	return fmt.Sprintf("tupleElement(arrayFirst(j -> arrayExists(w -> match(%s, concat('^', w, '(%s)?$')), j.2), job_identity), 1)", column, jobRunSuffix)
}

// jobOfService is the job a log record's service.name belongs to (” for
// another service). Application logs carry the service.name.
func jobOfService(column string) string {
	return fmt.Sprintf("tupleElement(arrayFirst(j -> has(j.3, %s), job_identity), 1)", column)
}

// jobSource reads one table's rows as the jobs they belong to and keeps the
// selected jobs' rows. columns are the source columns the panel needs; jobExpr
// maps a row to its job (jobOfName or jobOfService); conditions are the
// source's own (range, environment, kind).
func jobSource(jobs []Process, columns, jobExpr, table, conditions string) string {
	return fmt.Sprintf(`WITH %s
SELECT %s, %s AS job
FROM %s
WHERE %s
  AND job != '' AND has(selected_jobs, job)`, jobResolverSQL(jobs), columns, jobExpr, table, conditions)
}

// jobExecutionsSQL is the subquery every run count is built on: one row per
// EXECUTION of a Job. It groups events into executions instead of counting
// them, because
//
//   - the Job controller emits SuccessfulCreate once per POD (a parallel Job,
//     or a pod replaced after a failure, repeats it for one run);
//   - identical specs deployed to two environments share a hashed Job name, as
//     can a cron's scheduled-time stamp, so the environment is part of the key;
//   - forge names a standalone Job from its pod-spec hash alone, so deleting
//     and recreating an unchanged Job (or re-adding a pruned version) reuses
//     the name, and two executions share (environment, name).
//
// An execution is (environment, Job name, Job UID, generation):
//
//	uid         the involved object's UID (k8s.object.uid), which the k8s_events
//	            receiver emits as a resource attribute. The node agent drops it
//	            today (control-plane otel_agent.k, INFRA_EVENT_DROP_RES_ATTRS),
//	            so it is '' and this key is inert until the agent keeps it for
//	            Job events; when stored, it separates executions exactly, even
//	            one deleted while running that never emitted a terminal event.
//	generation  how many executions of this (environment, name, uid) began before
//	            this event: a SuccessfulCreate that directly follows a terminal
//	            event (Completed, or a failure) begins the next one. The Job
//	            controller never creates a pod after it has finished a Job, so a
//	            create after a terminal event is always a new Job. Events are
//	            ordered by time, creates before terminals on a tie (event times
//	            have one-second resolution).
//
// Only the events that bound a run are read (creates, Completed, the failure
// reasons), so a Job that only shows other events in the range is no run. Rows
// of other projects' Jobs are dropped and the selection is applied by the
// resolver (jobSource).
//
// Columns: env, run (the Job's name), job (the forge job), started, finished,
// failed_at (DateTime64; the epoch when the event was not seen), failure (the
// failure reason, ” if none) and failed (UInt8). The time range and
// environments are the dashboard's own query parameters.
func jobExecutionsSQL(jobs []Process) string {
	fail := strings.Join(jobFailureReasons, "', '")
	terminal := "'" + jobEventCompleted + "', '" + fail + "'"
	source := jobSource(jobs,
		`Timestamp, ResourceAttributes['reliant.env.id'] AS env, ResourceAttributes['k8s.workload.name'] AS run,
       ResourceAttributes['k8s.object.uid'] AS uid, LogAttributes['k8s.event.reason'] AS reason`,
		jobOfName("ResourceAttributes['k8s.workload.name']"), "otel_logs",
		fmt.Sprintf(`%s
  AND LogAttributes['k8s.event.reason'] IN ('%s', %s)
  AND if(mapContains(LogAttributes, 'k8s.object.kind'), LogAttributes['k8s.object.kind'], ResourceAttributes['k8s.object.kind']) = 'Job'
  AND %s`, jobRange, jobEventCreated, terminal, jobEnvScope))
	return fmt.Sprintf(`SELECT env, run, job, started, finished, failed_at, failure, failed
  FROM (
  SELECT env, run, any(job) AS job,
         minIf(Timestamp, reason = '%[1]s') AS started,
         maxIf(Timestamp, reason = '%[2]s') AS finished,
         maxIf(Timestamp, reason IN ('%[3]s')) AS failed_at,
         argMaxIf(reason, Timestamp, reason IN ('%[3]s')) AS failure,
         countIf(reason IN ('%[3]s')) > 0 AS failed
  FROM (
    SELECT Timestamp, env, run, uid, job, reason,
           sum(reason = '%[1]s' AND prev IN (%[4]s)) OVER (PARTITION BY env, run, uid ORDER BY Timestamp, ended ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS generation
    FROM (
      SELECT Timestamp, env, run, uid, job, reason, reason IN (%[4]s) AS ended,
             lagInFrame(reason, 1, '') OVER (PARTITION BY env, run, uid ORDER BY Timestamp, ended ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING) AS prev
      FROM (
%[5]s
      )
    )
  )
  GROUP BY env, run, uid, generation
  )`, jobEventCreated, jobEventCompleted, fail, terminal, source)
}

// jobsSummarySQL is the per-job table: one row per forge job.
//
// last_duration_s is the NEWEST run's duration, NULL while it is running or
// after it failed. argMax skips rows whose argument is NULL and would return the
// previous successful run's duration beside the newest run's start, so the
// nullable value travels in a tuple (a tuple is never NULL).
func jobsSummarySQL(jobs []Process) string {
	return `SELECT job,
       count() AS runs,
       countIf(finished > 0) AS succeeded,
       countIf(failed) AS failed,
       max(started) AS last_started,
       tupleElement(argMax(tuple(if(finished > 0 AND started > 0, dateDiff('second', started, finished), NULL)), started), 1) AS last_duration_s,
       if(countIf(finished > 0 AND started > 0) = 0, NULL, round(avgIf(dateDiff('second', started, finished), finished > 0 AND started > 0), 1)) AS avg_duration_s,
       if(countIf(finished > 0 AND started > 0) = 0, NULL, maxIf(dateDiff('second', started, finished), finished > 0 AND started > 0)) AS max_duration_s
FROM (
  ` + jobExecutionsSQL(jobs) + `
)
GROUP BY job
ORDER BY last_started DESC, job
`
}

// jobsTotalsSQL is one row for the whole project: executions created in the
// range, and executions seen in it that completed and that failed.
func jobsTotalsSQL(jobs []Process) string {
	return `SELECT countIf(started > 0) AS runs_started,
       countIf(finished > 0) AS runs_succeeded,
       countIf(failed) AS runs_failed
FROM (
  ` + jobExecutionsSQL(jobs) + `
)
`
}

// jobsByJobSQL is a time series of executions, one series per job: each
// execution counts once, in the bucket of at (its creation, or its failure),
// for the executions where cond holds.
func jobsByJobSQL(jobs []Process, at, cond string) string {
	return `SELECT toStartOfInterval(` + at + `, toIntervalSecond({step:UInt32})) AS t, job, count() AS runs
FROM (
  ` + jobExecutionsSQL(jobs) + `
)
WHERE ` + cond + `
GROUP BY t, job
ORDER BY t, job
`
}

// jobsRecentRunsSQL lists the newest executions, one row each. Its first
// column is the job (a leading time column would make the console draw a time
// series); the rest are group columns, and duration_s is the one value.
func jobsRecentRunsSQL(jobs []Process) string {
	return `SELECT job, run, env,
       if(failed, failure, if(finished > 0, '` + jobEventCompleted + `', 'Running')) AS outcome,
       if(started > 0, toString(started), '') AS started_at,
       if(finished > 0 AND started > 0, dateDiff('second', started, finished), NULL) AS duration_s
FROM (
  ` + jobExecutionsSQL(jobs) + `
)
ORDER BY greatest(started, finished, failed_at) DESC, job, run
LIMIT 50
`
}

// jobsWarningsSQL is a time series of Warning-type Kubernetes events, one
// series per job. A job's Warning events come from two kinds of object: the
// Job itself (BackoffLimitExceeded and DeadlineExceeded are Warnings on the Job,
// named for the run) and its pods (named for the forge job). Events carry the
// involved object's name, which jobOfName resolves for both.
func jobsWarningsSQL(jobs []Process) string {
	return `SELECT toStartOfInterval(Timestamp, toIntervalSecond({step:UInt32})) AS t, job, count() AS warnings
FROM (
` + jobSource(jobs, "Timestamp", jobOfName("ResourceAttributes['k8s.workload.name']"), "otel_logs",
		jobRange+"\n  AND LogAttributes['k8s.event.reason'] != ''\n  AND LogAttributes['k8s.event.type'] = 'Warning'\n  AND "+jobEnvScope) + `
)
GROUP BY t, job
ORDER BY t, job
`
}

// jobsRestartsSQL is a time series of container restarts, one series per job:
// the highest k8s.container.restarts among the job's containers in each
// bucket. The node agent's metrics carry the pod's workload name.
func jobsRestartsSQL(jobs []Process) string {
	return `SELECT toStartOfInterval(Timestamp, toIntervalSecond({step:UInt32})) AS t, job, max(restarts) AS restarts
FROM (
` + jobSource(jobs, "TimeUnix AS Timestamp, Value AS restarts", jobOfName("ResourceAttributes['k8s.workload.name']"), "otel_metrics_gauge",
		"TimeUnix >= {from:DateTime64(9)} AND TimeUnix < {to:DateTime64(9)}\n  AND MetricName = 'k8s.container.restarts'\n  AND "+jobEnvScope) + `
)
GROUP BY t, job
ORDER BY t, job
`
}

// jobsErrorLogsSQL lists the newest error-level log records of the jobs. A log
// record carries the service.name of the process that wrote it, which
// jobOfService resolves. The first column is the job, not the time: a leading
// time column would make the console draw a time series.
func jobsErrorLogsSQL(jobs []Process) string {
	return `SELECT job, ServiceName AS service, upper(SeverityText) AS severity, toString(Timestamp) AS at, left(Body, 500) AS message
FROM (
` + jobSource(jobs, "Timestamp, ServiceName, SeverityText, Body", jobOfService("ServiceName"), "otel_logs",
		jobRange+"\n  AND upper(SeverityText) IN ('ERROR', 'ERR')\n  AND "+jobEnvScope) + `
)
ORDER BY Timestamp DESC
LIMIT 100
`
}

// jobReplaces are the ids of the per-job dashboards this one replaces: each
// job's own "process-<name>" and the id it was generated under before
// dashboards were named for the workload. A reader moves stars and copies of
// any of them here. At most MaxReplaces (the spec's bound) are named; the
// newest names come first so the cap drops the oldest.
func jobReplaces(jobs []Process) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range jobs {
		for _, id := range append([]string{ProcessDashboardID(p.Name)}, prefixed("process-", p.Replaces)...) {
			if id != JobsID && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	sort.Strings(out)
	if len(out) > maxReplaces {
		out = out[:maxReplaces]
	}
	return out
}

func prefixed(prefix string, names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = prefix + n
	}
	return out
}
