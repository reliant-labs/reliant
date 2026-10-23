package cli

// `forge obs check [env]` — did the deploy work? (DESIGN §6)
//
// The verb judges one promotion by comparing the window after it with the
// window before it, per service, and by what the release opened (issues,
// alerts, restarts). The rule is pkg/releasehealth's, shared with the
// control plane; this file is flags, polling, the exit matrix and rendering.
//
// EXIT CODES (exitcodes.go): 0 PASS, and INCONCLUSIVE with
// --allow-inconclusive; 1 FAIL; 2 INCONCLUSIVE, or could not look (control
// plane unreachable, auth refused, telemetry off, the control plane does not
// serve release health yet); 8 --wait ran out before the after window
// finished. Exit 2 deliberately covers "low traffic" and "unreachable": both
// mean "not proven good", and a gate must not read either as a pass.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/pkg/obsquery"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/releasehealth"
)

const schemaObsCheck = "forge.obs.check/v1"

var obsCheckSchema = obsout.Schema{
	Name: schemaObsCheck,
	Fields: []string{
		"scope", "scope.env", "scope.backend", "scope.env_id", "scope.from", "scope.to",
		"query_mode", "verdict", "reason",
		"subject", "subject.promotion_id", "subject.release", "subject.git_commit", "subject.promoted_at", "subject.previous_release",
		"windows", "windows.before", "windows.before.from", "windows.before.to", "windows.after", "windows.after.from", "windows.after.to", "windows.after_complete",
		"services", "services.service", "services.before", "services.after", "services.version_share",
		"findings", "findings.code", "findings.service", "findings.ref", "findings.value", "findings.threshold", "findings.policy", "findings.message",
		"new_issues", "regressed_issues", "firing_alerts", "workload_findings",
		"thresholds", "thresholds.name", "thresholds.value", "thresholds.source",
		"not_evaluated", "waited", "statements", "statements.purpose", "statements.sql", "statements.params.*",
		"suggested",
	},
}

type obsCheckRange struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

type obsCheckWindows struct {
	Before        obsCheckRange `json:"before"`
	After         obsCheckRange `json:"after"`
	AfterComplete bool          `json:"after_complete"`
}

type obsCheckDoc struct {
	obsout.Head
	Scope     obsout.Scope `json:"scope"`
	QueryMode string       `json:"query_mode"`
	Verdict   string       `json:"verdict,omitempty"`
	// Reason says why the verdict is INCONCLUSIVE.
	Reason           string                        `json:"reason,omitempty"`
	Subject          *releasehealth.Subject        `json:"subject,omitempty"`
	Windows          *obsCheckWindows              `json:"windows,omitempty"`
	Services         []releasehealth.ServiceHealth `json:"services,omitempty"`
	Findings         []releasehealth.Finding       `json:"findings"`
	NewIssues        []releasehealth.Issue         `json:"new_issues,omitempty"`
	RegressedIssues  []releasehealth.Issue         `json:"regressed_issues,omitempty"`
	FiringAlerts     []releasehealth.Alert         `json:"firing_alerts,omitempty"`
	WorkloadFindings []releasehealth.Workload      `json:"workload_findings,omitempty"`
	Thresholds       []releasehealth.Threshold     `json:"thresholds,omitempty"`
	NotEvaluated     []string                      `json:"not_evaluated,omitempty"`
	// Waited is how long --wait polled, when it did.
	Waited     string                 `json:"waited,omitempty"`
	Statements []obsbackend.Statement `json:"statements,omitempty"`
	Suggested  []string               `json:"suggested,omitempty"`
}

type obsCheckOpts struct {
	out     obsout.Flags
	backend string

	release, promotion string
	window             time.Duration
	since              string
	wait               bool
	timeout            time.Duration
	failOn             string
	thresholds         []string
	allowInconclusive  bool
	gateJSON           string
	explain            bool
}

// obsCheckPollEvery is how often --wait asks again.
const obsCheckPollEvery = 15 * time.Second

// obsCheckSleep waits between polls. A seam: the test advances its own clock.
var obsCheckSleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// obsLocalSessionStart is when the last `forge env up` of env started, read
// from this machine's ledger. A seam: tests state it.
var obsLocalSessionStart = func(env string) (time.Time, bool, error) {
	store, err := openMachineLedger(projectDirForKCL())
	if err != nil {
		return time.Time{}, false, err
	}
	sessions, err := store.SessionsFor(env)
	if err != nil {
		return time.Time{}, false, err
	}
	var latest time.Time
	for _, s := range sessions {
		if s.StartedAt.After(latest) {
			latest = s.StartedAt
		}
	}
	return latest, !latest.IsZero(), nil
}

func newObsCheckCmd() *cobra.Command {
	var o obsCheckOpts
	cmd := &cobra.Command{
		Use:   "check [env]",
		Short: "Did the deploy work? Judge a release by the window after it against the window before",
		Long: `Judge one promotion of [env] (default dev): compare the [P, P+window) after it
with the [P-window, P) before it, per service, over weighted server spans, and
list what the release opened: new and regressed issues, alerts still firing,
workload restarts. The verdict is PASS, FAIL or INCONCLUSIVE. Each window
stops at the neighbouring promotion; the after window also stops at now.

  ` + Name() + ` obs check prod --wait --json
  ` + Name() + ` obs check prod --release v1.4.2 --window 30m
  ` + Name() + ` obs check prod --fail-on error_rate,new_issue --threshold min_requests=20
  ` + Name() + ` obs check prod --wait --gate-json check.json && ` + Name() + ` gate record prod --from check.json
  ` + Name() + ` obs check dev --since 20m          # local: no promotions, P is --since

Exit status:
  0  PASS (also INCONCLUSIVE with --allow-inconclusive)
  1  FAIL: we looked, and the release made things worse
  2  INCONCLUSIVE, or we could not look (control plane unreachable, auth
     refused, telemetry off). Both mean "not proven good"; a gate must not
     read either as a pass.
  8  --wait ran out before the after window finished

Thresholds come from the env's release_health declaration (KCL); --threshold
name=value overrides one for this call (` + strings.Join(releasehealth.OverrideNames, ", ") + `).
The document says where each effective threshold came from: release_shape,
default or request.

` + obsCheckSchema.Help(),
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runObsCheck(cmd.Context(), defaultObsDeps(cmd), obsEnvArg(args, 0), o)
		},
	}
	f := cmd.Flags()
	o.out.Register(f, 0)
	_ = f.MarkHidden("limit")
	f.StringVar(&o.backend, "backend", "", "local | cloud (default: from the env's declaration)")
	f.StringVar(&o.release, "release", "", "judge the promotion of this release version (default: the env's current one)")
	f.StringVar(&o.promotion, "promotion", "", "judge this promotion id")
	f.DurationVar(&o.window, "window", 0, "width of each window (default: the env's release_health.window, 15m; max 6h)")
	f.StringVar(&o.since, "since", "", "local env only: when the release started, as a duration ago (20m) or an RFC 3339 time (default: the start of the last env up)")
	f.BoolVar(&o.wait, "wait", false, "poll until the after window has finished")
	f.DurationVar(&o.timeout, "timeout", 30*time.Minute, "with --wait: give up (exit 8) after this long")
	f.StringVar(&o.failOn, "fail-on", "", "comma-separated finding codes that fail the release (default: the env's release_health.fail_on): "+strings.Join(release.HealthFindings, ", "))
	f.StringArrayVar(&o.thresholds, "threshold", nil, "override one threshold for this call, name=value (repeatable)")
	f.BoolVar(&o.allowInconclusive, "allow-inconclusive", false, "exit 0 on INCONCLUSIVE instead of 2")
	f.StringVar(&o.gateJSON, "gate-json", "", "also write the verdict to `FILE` as a gate document, for `"+Name()+" gate record --from FILE`")
	f.BoolVar(&o.explain, "explain", false, "local env only: print the compiled statements instead of running them")
	cmd.MarkFlagsMutuallyExclusive("release", "promotion")
	return cmd
}

func runObsCheck(ctx context.Context, d obsDeps, env string, o obsCheckOpts) error {
	if ctx == nil {
		ctx = context.Background()
	}
	started := d.Now()
	fields, err := o.out.Parsed(obsCheckSchema)
	if err != nil {
		return obsFail(d, o.out.JSON, schemaObsCheck, err)
	}
	doc, err := obsCheck(ctx, d, env, o)
	// The gate is evidence about the attempt, whatever its outcome; a check
	// that could not look is recorded as an error gate, never as a pass.
	if o.gateJSON != "" {
		if gerr := writeCheckGate(o.gateJSON, env, doc, err, started, d.Now()); gerr != nil {
			err = errors.Join(err, gerr)
		}
	}
	doc.Stamp(schemaObsCheck, err)
	if o.out.JSON {
		if done, werr := o.out.Emit(d.Stdout, obsCheckSchema, doc, fields); werr != nil {
			return errors.Join(err, werr)
		} else if done {
			return err
		}
	}
	if doc.Verdict != "" || o.explain {
		writeCheckText(d.Stdout, doc)
	}
	return err
}

// obsCheck runs the check and returns the document with the verdict error
// (nil on PASS) the process will exit with. The head is stamped by the caller.
func obsCheck(ctx context.Context, d obsDeps, env string, o obsCheckOpts) (obsCheckDoc, error) {
	doc := obsCheckDoc{QueryMode: QueryModeStructured, Findings: []releasehealth.Finding{}}
	bad := func(err error) (obsCheckDoc, error) {
		doc.Suggested = obsbackend.SuggestedOf(err)
		return doc, err
	}

	opts, err := checkOptions(o)
	if err != nil {
		return bad(err)
	}
	b, _, err := (&obsCommon{backend: o.backend}).open(ctx, d, env, "check")
	if err != nil {
		return bad(err)
	}
	reader, ok := b.(obsbackend.HealthReader)
	if !ok {
		return bad(obsbackend.Undetermined("the %s backend cannot judge release health", b.Info().Backend))
	}
	req := obsbackend.HealthRequest{PromotionID: o.promotion, Release: o.release, Options: opts}
	if b.Info().Backend == obsbackend.BackendLocal {
		if o.explain {
			return explainLocalCheck(ctx, d, b, env, o, &doc)
		}
		if req.PromotedAt, err = localPromotedAt(d.Now(), env, o); err != nil {
			return bad(err)
		}
	}

	deadline := d.Now().Add(o.timeout)
	var res obsbackend.HealthResult
	for {
		req.Now = d.Now()
		if res, err = reader.ReleaseHealth(ctx, req); err != nil {
			return bad(err)
		}
		if res.Report.AfterComplete || !o.wait {
			break
		}
		if !d.Now().Add(obsCheckPollEvery).Before(deadline) {
			fillCheckDoc(&doc, b.Info(), env, res, o)
			doc.Waited = o.timeout.String()
			return doc, exitCodeError{code: exitTimedOut, msg: fmt.Sprintf(
				"the after window of %s had not finished after %s (it ends %s); run again to keep waiting",
				subjectName(res.Report), o.timeout, res.Report.After.To.UTC().Format(time.RFC3339))}
		}
		fmt.Fprintf(d.Stderr, "waiting for the after window of %s (ends %s)\n", subjectName(res.Report), res.Report.After.To.UTC().Format(time.RFC3339))
		if err := obsCheckSleep(ctx, obsCheckPollEvery); err != nil {
			return bad(obsbackend.Undetermined("interrupted while waiting").Cause(err))
		}
	}
	if o.wait {
		doc.Waited = d.Now().Sub(deadline.Add(-o.timeout)).Round(time.Second).String()
	}

	// A control plane judges with the env's recorded policy; --fail-on is this
	// call's, so it is applied to the answer the same way Evaluate would.
	if b.Info().Backend == obsbackend.BackendCloud && opts.FailOn != nil {
		res.Report = releasehealth.Rejudge(res.Report, opts.FailOn)
	}
	fillCheckDoc(&doc, b.Info(), env, res, o)
	return doc, verdictError(res.Report, o.allowInconclusive)
}

// checkOptions parses and validates the threshold flags, so a bad flag is
// exit 1 before anything is read.
func checkOptions(o obsCheckOpts) (releasehealth.Options, error) {
	opts := releasehealth.Options{Window: o.window}
	var err error
	if o.failOn != "" {
		if opts.FailOn, err = releasehealth.ParseFailOn(o.failOn); err != nil {
			return opts, obsbackend.Wrong("%s", unwrapInvalid(err))
		}
	}
	if opts.Overrides, err = releasehealth.ParseOverrides(o.thresholds); err != nil {
		return opts, obsbackend.Wrong("%s", unwrapInvalid(err))
	}
	if _, err := releasehealth.Resolve(opts); err != nil {
		return opts, obsbackend.Wrong("%s", unwrapInvalid(err))
	}
	return opts, nil
}

func unwrapInvalid(err error) string {
	return strings.TrimPrefix(err.Error(), releasehealth.ErrInvalid.Error()+": ")
}

// localPromotedAt is P for a local env: --since, else the start of the last
// `forge env up`.
func localPromotedAt(now time.Time, env string, o obsCheckOpts) (time.Time, error) {
	if o.release != "" || o.promotion != "" {
		return time.Time{}, obsbackend.Wrong("a local env has no promotions to pick by --release or --promotion; use --since")
	}
	if o.since != "" {
		if dur, err := time.ParseDuration(o.since); err == nil {
			if dur <= 0 {
				return time.Time{}, obsbackend.Wrong("--since must be positive")
			}
			return now.Add(-dur).UTC(), nil
		}
		t, err := time.Parse(time.RFC3339, o.since)
		if err != nil {
			return time.Time{}, obsbackend.Wrong("--since %q is neither a duration (20m) nor an RFC 3339 time", o.since)
		}
		return t.UTC(), nil
	}
	t, ok, err := obsLocalSessionStart(env)
	if err != nil {
		return time.Time{}, obsbackend.Undetermined("cannot read this machine's ledger for %s's last start", env).Cause(err).
			With(obsbackend.Cmd("forge", "obs", "check", env, "--since", "20m"))
	}
	if !ok {
		return time.Time{}, obsbackend.Undetermined("no `forge env up %s` is recorded on this machine, so there is no release to judge from", env).
			With(obsbackend.Cmd("forge", "obs", "check", env, "--since", "20m"))
	}
	return t.UTC(), nil
}

func explainLocalCheck(ctx context.Context, d obsDeps, b obsbackend.Backend, env string, o obsCheckOpts, doc *obsCheckDoc) (obsCheckDoc, error) {
	l, ok := b.(obsbackend.Local)
	if !ok {
		return *doc, obsbackend.Wrong("--explain compiles a local env's statements; this env is read through the control plane")
	}
	p, err := localPromotedAt(d.Now(), env, o)
	if err != nil {
		return *doc, err
	}
	opts, _ := checkOptions(o)
	l.Runner = explainRunner{}
	res, err := l.ReleaseHealth(ctx, obsbackend.HealthRequest{PromotedAt: p, Now: d.Now(), Options: opts})
	doc.Statements = res.Statements
	doc.Scope = obsout.Scope{Env: env, Backend: obsbackend.BackendLocal}
	return *doc, err
}

func verdictError(r releasehealth.Report, allowInconclusive bool) error {
	switch r.Verdict {
	case releasehealth.Pass:
		return nil
	case releasehealth.Fail:
		return exitCodeError{code: exitWrong, msg: fmt.Sprintf("%s failed its health check: %s", subjectName(r), failSummary(r))}
	}
	if allowInconclusive {
		return nil
	}
	return exitCodeError{code: exitUndetermined, msg: fmt.Sprintf("%s: INCONCLUSIVE — %s", subjectName(r), releasehealth.InconclusiveWhy(r))}
}

func failSummary(r releasehealth.Report) string {
	var parts []string
	for _, f := range r.Findings {
		if f.Policy == releasehealth.PolicyFail {
			parts = append(parts, f.Message)
		}
	}
	return strings.Join(parts, "; ")
}

func subjectName(r releasehealth.Report) string {
	switch {
	case r.Subject.Release != "":
		return "release " + r.Subject.Release
	case r.Subject.PromotionID != "":
		return "promotion " + r.Subject.PromotionID
	}
	return "the release started " + r.Subject.PromotedAt.UTC().Format(time.RFC3339)
}

func fillCheckDoc(doc *obsCheckDoc, info obsbackend.Info, env string, res obsbackend.HealthResult, o obsCheckOpts) {
	r := res.Report
	doc.Scope = obsout.Scope{Env: env, Backend: info.Backend, EnvID: info.EnvID, From: r.Before.From, To: r.After.To}
	doc.Verdict = string(r.Verdict)
	if r.Verdict == releasehealth.Inconclusive {
		doc.Reason = releasehealth.InconclusiveWhy(r)
	}
	sub := r.Subject
	doc.Subject = &sub
	doc.Windows = &obsCheckWindows{
		Before:        obsCheckRange{From: r.Before.From, To: r.Before.To},
		After:         obsCheckRange{From: r.After.From, To: r.After.To},
		AfterComplete: r.AfterComplete,
	}
	doc.Services = r.Services
	if r.Findings != nil {
		doc.Findings = r.Findings
	}
	doc.NewIssues, doc.RegressedIssues = r.NewIssues, r.RegressedIssues
	doc.FiringAlerts, doc.WorkloadFindings = r.FiringAlerts, r.Workloads
	doc.Thresholds, doc.NotEvaluated = r.Thresholds, r.NotEvaluated
	doc.Statements = res.Statements
	if !o.explain {
		doc.Statements = nil
	}
	if r.Verdict == releasehealth.Fail {
		doc.Suggested = checkSuggestions(env, r)
	}
}

// checkSuggestions are the next commands after a FAIL: each new or regressed
// issue, then the failing endpoints.
func checkSuggestions(env string, r releasehealth.Report) []string {
	var cmds []string
	for _, is := range r.NewIssues {
		cmds = append(cmds, obsout.Command("forge", "obs", "issue", is.ID, env))
	}
	for _, is := range r.RegressedIssues {
		cmds = append(cmds, obsout.Command("forge", "obs", "issue", is.ID, env))
	}
	cmds = append(cmds, obsout.Command("forge", "obs", "endpoints", env, "--sort", "errors"))
	return obsout.Suggest(cmds...)
}

// writeCheckGate writes the gate document `forge gate record --from` reads.
// PASS is passed, FAIL is failed; INCONCLUSIVE, a wait that timed out and a
// check that could not look are all `error` — the gate vocabulary's "could not
// reach a verdict", the same distinction as exit 2.
func writeCheckGate(path, env string, doc obsCheckDoc, verr error, started, finished time.Time) error {
	status := release.GateStatusError
	summary := "could not look: " + checkErrText(verr)
	details := map[string]any{"env": env, "backend": doc.Scope.Backend}
	if doc.Verdict != "" {
		details["verdict"], details["findings"] = doc.Verdict, len(doc.Findings)
		if doc.Subject != nil {
			details["release"], details["promotion_id"] = doc.Subject.Release, doc.Subject.PromotionID
		}
		switch releasehealth.Verdict(doc.Verdict) {
		case releasehealth.Pass:
			status, summary = release.GateStatusPassed, "release health PASS"
		case releasehealth.Fail:
			status, summary = release.GateStatusFailed, "release health FAIL: "+failSummaryOf(doc)
		default:
			summary = "release health INCONCLUSIVE: " + doc.Reason
		}
		if exitCodeForError(verr) == exitTimedOut {
			status, summary = release.GateStatusError, "release health: "+checkErrText(verr)
		}
	}
	return writeGateWithRun(path, gateWindow(release.Gate{
		Name: "release-health", Status: status, Summary: summary, Details: details,
	}, started, finished))
}

func failSummaryOf(doc obsCheckDoc) string {
	var parts []string
	for _, f := range doc.Findings {
		if f.Policy == releasehealth.PolicyFail {
			parts = append(parts, f.Message)
		}
	}
	return strings.Join(parts, "; ")
}

func checkErrText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func writeCheckText(w io.Writer, doc obsCheckDoc) {
	if doc.Subject == nil {
		explainText(w, doc.Statements)
		return
	}
	fmt.Fprintf(w, "%s  %s on %s (%s backend)\n", doc.Verdict, doc.Subject.Release, doc.Scope.Env, doc.Scope.Backend)
	if doc.Windows != nil {
		fmt.Fprintf(w, "before %s .. %s\nafter  %s .. %s%s\n",
			doc.Windows.Before.From.Format(time.RFC3339), doc.Windows.Before.To.Format(time.RFC3339),
			doc.Windows.After.From.Format(time.RFC3339), doc.Windows.After.To.Format(time.RFC3339),
			map[bool]string{true: "", false: "  (still open)"}[doc.Windows.AfterComplete])
	}
	if doc.Reason != "" {
		fmt.Fprintf(w, "inconclusive: %s\n", doc.Reason)
	}
	if len(doc.Services) > 0 {
		rows := make([][]string, len(doc.Services))
		for i, s := range doc.Services {
			share := ""
			if s.VersionShare != nil {
				share = fmt.Sprintf("%.0f%%", *s.VersionShare*100)
			}
			rows[i] = []string{s.Service,
				fmt.Sprintf("%d -> %d", s.Before.Requests, s.After.Requests),
				fmt.Sprintf("%.1f%% -> %.1f%%", s.Before.ErrorRate*100, s.After.ErrorRate*100),
				fmt.Sprintf("%.0fms -> %.0fms", s.Before.P95Ms, s.After.P95Ms), share}
		}
		fmt.Fprintln(w)
		obsout.Table(w, []string{"SERVICE", "REQUESTS", "ERRORS", "P95", "ON RELEASE"}, rows, 40)
	}
	if len(doc.Findings) > 0 {
		fmt.Fprintln(w)
		for _, f := range doc.Findings {
			fmt.Fprintf(w, "%-4s %-17s %s\n", f.Policy, f.Code, f.Message)
		}
	}
	if len(doc.NotEvaluated) > 0 {
		fmt.Fprintf(w, "\nnot evaluated on this backend: %s\n", strings.Join(doc.NotEvaluated, ", "))
	}
	for _, th := range doc.Thresholds {
		if th.Source == releasehealth.SourceRequest {
			fmt.Fprintf(w, "threshold %s=%s (from this call)\n", th.Name, th.Value)
		}
	}
}

// explainRunner answers every statement with no rows, so a local check
// compiles all its statements and runs none.
type explainRunner struct{}

func (explainRunner) Run(context.Context, obsquery.Stmt, obsbackend.Limits) (obsbackend.Result, error) {
	return obsbackend.Result{}, nil
}
