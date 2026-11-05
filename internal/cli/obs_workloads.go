package cli

// `forge obs workloads [env]` — is anything crash-looping, restarting or
// failing its jobs? The cluster state a hosted user cannot read with kubectl:
// the control plane's ListWorkloads reads it from the cluster collectors and
// the deploy ledger, and judges each workload once (WorkloadHealth), so this
// verb, the console and the nav badge cannot disagree.

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
)

const schemaObsWorkloads = "forge.obs.workloads/v1"

var obsWorkloadsSchema = obsout.Schema{
	Name: schemaObsWorkloads,
	Fields: []string{
		"scope", "scope.env", "scope.backend", "scope.env_id", "scope.from", "scope.to", "scope.services",
		"query_mode", "summary", "summary.workloads", "summary.unhealthy", "summary.crash_looping", "summary.restarts", "summary.failed_jobs",
		"workloads", "workloads.workload", "workloads.namespace", "workloads.kind", "workloads.health", "workloads.cause",
		"workloads.reason", "workloads.detail", "workloads.since", "workloads.ready", "workloads.desired", "workloads.restarts",
		"workloads.last_exit_reason", "workloads.last_exit_at", "workloads.release", "workloads.containers", "workloads.latest_warning",
		"jobs", "jobs.name", "jobs.workload", "jobs.result", "jobs.reason", "jobs.release", "jobs.started_at", "jobs.finished_at", "jobs.duration_seconds",
		"truncated", "jobs_truncated", "suggested",
	},
}

type obsWorkloadsSummary struct {
	Workloads    int   `json:"workloads"`
	Unhealthy    int   `json:"unhealthy"`
	CrashLooping int   `json:"crash_looping"`
	Restarts     int64 `json:"restarts"`
	FailedJobs   int   `json:"failed_jobs"`
}

type obsWorkloadsDoc struct {
	obsout.Head
	Scope         obsout.Scope          `json:"scope"`
	QueryMode     string                `json:"query_mode"`
	Summary       obsWorkloadsSummary   `json:"summary"`
	Workloads     []obsbackend.Workload `json:"workloads"`
	Jobs          []obsbackend.Job      `json:"jobs"`
	Truncated     bool                  `json:"truncated,omitempty"`
	JobsTruncated bool                  `json:"jobs_truncated,omitempty"`
	Suggested     []string              `json:"suggested,omitempty"`
}

type obsWorkloadsOpts struct {
	obsCommon
	all  bool
	jobs string
}

// workloadReader is what this verb needs of a backend. Only the control plane
// serves it: a local stack's workloads are host processes or a cluster the
// user already has kubectl for.
type workloadReader interface {
	Workloads(ctx context.Context, n obsbackend.Narrow) (obsbackend.WorkloadsResult, error)
}

func newObsWorkloadsCmd() *cobra.Command {
	var o obsWorkloadsOpts
	cmd := &cobra.Command{
		Use:   "workloads [env]",
		Short: "Restarts, crash loops, last termination reasons and failing jobs, per workload",
		Long: `Show each workload of [env] (default dev) over the range: its health verdict
(crash_looping, degraded, pending, unknown, healthy, suspended) and why, ready
and desired replicas, restarts, the last termination reason (OOMKilled,
Error, ...) and the latest Kubernetes warning; then the env's Job runs
(migrations, provisioning, backups), failed ones first. Read through the
control plane from the cluster's own state, so it needs no kubeconfig.

By default only workloads that need a look are listed (not healthy, or
restarted in the range); --all lists every one.

  ` + Name() + ` obs workloads prod
  ` + Name() + ` obs workloads prod --since 72h --all
  ` + Name() + ` obs workloads prod --service api --json
  ` + Name() + ` obs workloads prod --jobs all

The range is at most 7 days. Exit 0 when the read succeeded, whatever it
found; 2 when it could not look.

` + obsWorkloadsSchema.Help(),
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runObsWorkloads(cmd.Context(), defaultObsDeps(cmd), obsEnvArg(args, 0), o)
		},
	}
	o.register(cmd, 24*time.Hour, 0)
	_ = cmd.Flags().MarkHidden("limit")
	_ = cmd.Flags().MarkHidden("explain")
	_ = cmd.Flags().MarkHidden("print-sql")
	_ = cmd.Flags().MarkHidden("filter")
	cmd.Flags().BoolVar(&o.all, "all", false, "list every workload, healthy ones too")
	cmd.Flags().StringVar(&o.jobs, "jobs", "failed", "which Job runs to list: failed | all | none")
	return cmd
}

func runObsWorkloads(ctx context.Context, d obsDeps, env string, o obsWorkloadsOpts) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fail := func(err error) error { return obsFail(d, o.out.JSON, schemaObsWorkloads, err) }
	fields, err := o.out.Parsed(obsWorkloadsSchema)
	if err != nil {
		return fail(err)
	}
	switch o.jobs {
	case "failed", "all", "none":
	default:
		return fail(obsbackend.Wrong("--jobs %q: use failed, all or none", o.jobs))
	}
	n, err := o.narrow(d.Now())
	if err != nil {
		return fail(err)
	}
	if n.Range.To.Sub(n.Range.From) > 7*24*time.Hour {
		return fail(obsbackend.Wrong("the range is at most 7 days"))
	}
	b, _, err := o.open(ctx, d, env, "workloads")
	if err != nil {
		return fail(err)
	}
	wr, ok := b.(workloadReader)
	if !ok {
		return fail(obsbackend.Undetermined("the %s backend has no workload state; for a local stack use `%s env status %s`", b.Info().Backend, Name(), env))
	}
	res, err := wr.Workloads(ctx, n)
	if err != nil {
		return fail(err)
	}
	doc := obsWorkloadsDoc{Scope: obsScope(b.Info(), n), QueryMode: QueryModeStructured,
		Workloads: []obsbackend.Workload{}, Jobs: []obsbackend.Job{}, Truncated: res.Truncated, JobsTruncated: res.JobsTruncated}
	doc.Summary.Workloads = len(res.Workloads)
	for _, w := range res.Workloads {
		doc.Summary.Restarts += w.Restarts
		if w.Health == obsbackend.WorkloadCrashLooping {
			doc.Summary.CrashLooping++
		}
		if w.Unhealthy() {
			doc.Summary.Unhealthy++
		}
		if o.all || w.Unhealthy() {
			doc.Workloads = append(doc.Workloads, w)
		}
	}
	for _, j := range failedJobsFirst(res.Jobs) {
		if j.Result == obsbackend.JobFailed {
			doc.Summary.FailedJobs++
		}
		if o.jobs == "all" || (o.jobs == "failed" && j.Result == obsbackend.JobFailed) {
			doc.Jobs = append(doc.Jobs, j)
		}
	}
	doc.Suggested = workloadSuggestions(env, n, doc)
	doc.Stamp(schemaObsWorkloads, nil)
	if done, err := o.out.Emit(d.Stdout, obsWorkloadsSchema, doc, fields); done || err != nil {
		return err
	}
	writeWorkloadsText(d.Stdout, d.Now(), doc, o.all)
	return nil
}

// failedJobsFirst keeps the newest-first order within failed, then the rest.
func failedJobsFirst(jobs []obsbackend.Job) []obsbackend.Job {
	out := make([]obsbackend.Job, 0, len(jobs))
	for _, j := range jobs {
		if j.Result == obsbackend.JobFailed {
			out = append(out, j)
		}
	}
	for _, j := range jobs {
		if j.Result != obsbackend.JobFailed {
			out = append(out, j)
		}
	}
	return out
}

// workloadSuggestions are the logs behind the worst workload and the newest
// failed job.
func workloadSuggestions(env string, n obsbackend.Narrow, doc obsWorkloadsDoc) []string {
	since := sinceFlag(n.Range.To.Sub(n.Range.From))
	var cmds []string
	for _, w := range doc.Workloads {
		if w.Unhealthy() {
			cmds = append(cmds, workloadLogsCommand(env, since, w.Namespace, w.Workload))
			break
		}
	}
	for _, j := range doc.Jobs {
		if j.Result == obsbackend.JobFailed {
			cmds = append(cmds, workloadLogsCommand(env, since, "", j.Workload))
			break
		}
	}
	return obsout.Suggest(cmds...)
}

// sinceFlag renders a range as a --since value: whole minutes, no zero
// units ("10m", "24h", "1h10m"), never shorter than a minute.
func sinceFlag(d time.Duration) string {
	m := int64(d.Round(time.Minute) / time.Minute)
	if m < 1 {
		m = 1
	}
	h, m := m/60, m%60
	switch {
	case h == 0:
		return strconv.FormatInt(m, 10) + "m"
	case m == 0:
		return strconv.FormatInt(h, 10) + "h"
	}
	return strconv.FormatInt(h, 10) + "h" + strconv.FormatInt(m, 10) + "m"
}

// workloadLogsCommand is `obs logs` narrowed to one workload's records.
func workloadLogsCommand(env, since, namespace, workload string) string {
	args := []string{"forge", "obs", "logs", env, "--since", since}
	if namespace != "" {
		args = append(args, "--filter", "k8s.namespace.name="+namespace)
	}
	if workload != "" {
		args = append(args, "--filter", "k8s.workload.name="+workload)
	}
	return obsout.Command(args...)
}

func writeWorkloadsText(w io.Writer, now time.Time, doc obsWorkloadsDoc, all bool) {
	s := doc.Summary
	fmt.Fprintf(w, "%d workload(s) on %s: %d need a look, %d crash-looping, %d restart(s), %d failed job run(s)\n",
		s.Workloads, doc.Scope.Env, s.Unhealthy, s.CrashLooping, s.Restarts, s.FailedJobs)
	if len(doc.Workloads) > 0 {
		rows := make([][]string, len(doc.Workloads))
		for i, wl := range doc.Workloads {
			ready := ""
			if wl.Ready != nil {
				ready = strconv.Itoa(int(*wl.Ready))
				if wl.Desired != nil {
					ready += "/" + strconv.Itoa(int(*wl.Desired))
				}
			}
			exit := wl.LastExit
			if exit != "" && !wl.LastExitAt.IsZero() {
				exit += " " + ago(now, wl.LastExitAt) + " ago"
			}
			why := wl.Reason
			if wl.LatestWarning != nil && why == "" {
				why = firstNonEmptyString(wl.LatestWarning.Plain, wl.LatestWarning.Reason)
			}
			rows[i] = []string{wl.Workload, strings.ToUpper(wl.Health), ready, strconv.FormatInt(wl.Restarts, 10), exit, why}
		}
		fmt.Fprintln(w)
		obsout.Table(w, []string{"WORKLOAD", "HEALTH", "READY", "RESTARTS", "LAST EXIT", "WHY"}, rows, 60)
	} else if !all {
		fmt.Fprintln(w, "\nevery workload is healthy with no restarts in the range (--all lists them)")
	}
	if doc.Truncated {
		fmt.Fprintln(w, "(more than 500 workloads matched; narrow with --service)")
	}
	if len(doc.Jobs) > 0 {
		rows := make([][]string, len(doc.Jobs))
		for i, j := range doc.Jobs {
			rows[i] = []string{j.Name, strings.ToUpper(j.Result), j.Reason, j.Release, ago(now, j.StartedAt) + " ago"}
		}
		fmt.Fprintln(w)
		obsout.Table(w, []string{"JOB", "RESULT", "REASON", "RELEASE", "STARTED"}, rows, 60)
	}
	if len(doc.Suggested) > 0 {
		fmt.Fprintln(w)
		for _, c := range doc.Suggested {
			fmt.Fprintln(w, "  "+c)
		}
	}
}

func firstNonEmptyString(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
