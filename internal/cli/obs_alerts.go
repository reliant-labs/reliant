package cli

// `forge obs alerts check` — evaluate an env's resolved alert rules against
// the local ClickStack's ClickHouse and print what would fire. It drives
// pkg/alerting's Evaluator (the guard, the row-to-instance mapping, the label
// rules and the limits are the evaluator's, not re-implemented here), with a
// Querier that runs inside the clickstack container, a Notifier that delivers
// nothing and an in-memory store.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/clickstack"
	"github.com/reliant-labs/forge/internal/obsdash"
	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/pkg/alerting"
	"github.com/reliant-labs/forge/pkg/release"
)

type obsAlertsCheckOpts struct {
	env      string
	rule     string
	timeout  time.Duration
	maxRows  int
	asJSON   bool
	printSQL bool
}

func newObsAlertsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "alerts",
		Short: "Alert rules declared for an environment",
	}
	cmd.AddCommand(newObsAlertsCheckCmd(), newObsAlertsRunCmd())
	return cmdutil.StrictGroup(cmd)
}

func newObsAlertsCheckCmd() *cobra.Command {
	o := obsAlertsCheckOpts{}
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Evaluate the env's alert rules against the local ClickStack and print what would fire",
		Long: `Evaluate the env's resolved alert rules (your forge.AlertRule entries plus the
generated defaults you did not disable, exactly what ` + "`forge env shape`" + ` records)
against the local ClickStack's ClickHouse, once, and print every instance that
is returning a row now: its labels and its value.

Nothing is notified and nothing is stored. A rule's ` + "`for`" + ` hold time is NOT applied:
this answers "what does the condition return right now", so a rule listed here
fires in production only once it has kept returning for its ` + "`for`" + `.

Each expr runs read-only with a timeout and a row cap (--timeout, --max-rows);
a rule that errors is reported and does not stop the others.

  ` + Name() + ` obs alerts check               # the dev env
  ` + Name() + ` obs alerts check --env staging
  ` + Name() + ` obs alerts check --rule ServiceErrorRateHigh --sql

Needs the local ClickStack running (` + "`forge env up`" + `).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runObsAlertsCheck(cmd.Context(), obsout.NewSanitizingWriter(cmd.OutOrStdout()), obsout.NewSanitizingWriter(cmd.ErrOrStderr()), o, defaultAlertShapeLoader, alertQueryRunnerFor(o.env))
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.env, "env", "dev", "environment whose rules to evaluate")
	f.StringVar(&o.rule, "rule", "", "evaluate only this alert name")
	f.DurationVar(&o.timeout, "timeout", 20*time.Second, "per-rule query timeout")
	f.IntVar(&o.maxRows, "max-rows", 200, "a rule returning more rows than this is reported as an error")
	f.BoolVar(&o.asJSON, "json", false, "print the result as JSON")
	f.BoolVar(&o.printSQL, "sql", false, "print each rule's expr instead of running it")
	return cmd
}

// alertShapeLoader returns the env's recorded alerts. A seam so the command is
// testable without rendering KCL.
type alertShapeLoader func(ctx context.Context, errOut io.Writer, env string) ([]release.ShapeAlert, error)

// alertQueryRunner runs one statement in the local ClickHouse with the settings
// applied and returns its JSONEachRow output.
type alertQueryRunner func(ctx context.Context, sql string, settings map[string]any) ([]byte, error)

func defaultAlertShapeLoader(ctx context.Context, errOut io.Writer, env string) ([]release.ShapeAlert, error) {
	doc, err := projectEnvShape(ctx, errOut, env)
	if err != nil {
		return nil, err
	}
	return doc.Shape.Alerts, nil
}

// alertQueryRunnerFor runs a statement in env's local ClickStack, the compose
// project `forge env up` started it in.
func alertQueryRunnerFor(env string) alertQueryRunner {
	return func(ctx context.Context, sql string, settings map[string]any) ([]byte, error) {
		return runAlertQuery(ctx, env, sql, settings)
	}
}

func runAlertQuery(ctx context.Context, env, sql string, settings map[string]any) ([]byte, error) {
	dir, err := requireProjectDir("obs alerts")
	if err != nil {
		return nil, err
	}
	args := append(localClickstackComposeArgs(ctx, dir, env), "exec", "-T", clickstack.Service, "sh", "-c",
		`q="$1"; shift; exec clickhouse-client -u "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" --format JSONEachRow --output_format_json_quote_64bit_integers=0 --query "$q" "$@"`,
		"sh", sql)
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, fmt.Sprintf("--%s=%v", k, settings[k]))
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

// clickhouseAlertQuerier is an alerting.Querier over an alertQueryRunner. The
// owner key is ignored: the local ClickStack holds one project's data.
type clickhouseAlertQuerier struct{ run alertQueryRunner }

func (q clickhouseAlertQuerier) Query(ctx context.Context, _ string, query string, opts alerting.QueryOptions) ([]alerting.Row, error) {
	if err := alerting.ValidateExpr(query); err != nil {
		return nil, err
	}
	out, err := q.run(ctx, query, opts.Settings())
	if err != nil {
		return nil, err
	}
	var rows []alerting.Row
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var r map[string]any
		if err := dec.Decode(&r); err != nil {
			return nil, fmt.Errorf("reading ClickHouse output: %w", err)
		}
		rows = append(rows, alerting.Row(r))
	}
	return rows, nil
}

type noopAlertNotifier struct{}

func (noopAlertNotifier) Notify(context.Context, []alerting.Alert) error { return nil }

// checkInstance is one instance that is returning a row now.
type checkInstance struct {
	Labels map[string]string `json:"labels"`
	Value  float64           `json:"value"`
}

// checkRule is one rule's outcome.
type checkRule struct {
	Alert     string            `json:"alert"`
	Source    string            `json:"source"`
	Severity  string            `json:"severity"`
	For       string            `json:"for"`
	Firing    []checkInstance   `json:"firing"`
	Platform  bool              `json:"platform,omitempty"`
	Error     string            `json:"error,omitempty"`
	Expr      string            `json:"expr,omitempty"`
	Summaries map[string]string `json:"-"`
}

type checkResult struct {
	Env   string      `json:"env"`
	Rules []checkRule `json:"rules"`
}

// ruleSpecOf converts a recorded alert into pkg/alerting's RuleSpec through the
// JSON both define, so a field the two disagree about is an error here and not
// a rule that silently loses its `for`.
func ruleSpecOf(a release.ShapeAlert) (alerting.RuleSpec, error) {
	b, err := json.Marshal(a)
	if err != nil {
		return alerting.RuleSpec{}, err
	}
	var spec alerting.RuleSpec
	if err := json.Unmarshal(b, &spec); err != nil {
		return alerting.RuleSpec{}, err
	}
	return spec, nil
}

func runObsAlertsCheck(ctx context.Context, stdout, stderr io.Writer, o obsAlertsCheckOpts, load alertShapeLoader, run alertQueryRunner) error {
	alerts, err := load(ctx, stderr, o.env)
	if err != nil {
		return err
	}
	if len(alerts) == 0 {
		fmt.Fprintf(stdout, "env %q declares no alert rules (no forge.AlertRule, and no generated defaults: run `%s generate`, and check that observability is on)\n", o.env, Name())
		return nil
	}
	if o.rule != "" {
		var kept []release.ShapeAlert
		for _, a := range alerts {
			if a.Alert == o.rule {
				kept = append(kept, a)
			}
		}
		if len(kept) == 0 {
			return fmt.Errorf("env %q has no alert %q (have: %s)", o.env, o.rule, alertNames(alerts))
		}
		alerts = kept
	}
	if o.printSQL {
		for _, a := range alerts {
			fmt.Fprintf(stdout, "-- %s\n%s\n\n", alertSummary(a), a.Expr)
		}
		return nil
	}

	ev, err := alerting.NewEvaluator(alerting.EvaluatorDeps{
		Querier:  clickhouseAlertQuerier{run: run},
		Notifier: noopAlertNotifier{},
		Store:    alerting.NewMemoryStore(),
		Limits:   alerting.Limits{MaxExecutionTime: o.timeout, MaxInstancesPerRule: o.maxRows},
	})
	if err != nil {
		return err
	}

	res := checkResult{Env: o.env}
	for _, a := range alerts {
		cr := checkRule{Alert: a.Alert, Source: a.Source, Severity: a.Labels["severity"], For: orDash(a.For), Firing: []checkInstance{}}
		spec, err := ruleSpecOf(a)
		if err != nil {
			cr.Error = err.Error()
			res.Rules = append(res.Rules, cr)
			continue
		}
		if obsdash.IsPlatformExpr(a.Expr) {
			// Raised by the control plane from its own events; there is no
			// telemetry to query locally.
			cr.Platform = true
			res.Rules = append(res.Rules, cr)
			continue
		}
		spec.For = 0 // the question is what returns now, not what has held for `for`
		spec.Expr = obsdash.BindEnvID(spec.Expr, o.env)
		rule := spec.Rule("local", o.env, nil)
		after, err := ev.EvaluateRule(ctx, rule, nil)
		if err != nil {
			cr.Error = err.Error()
		}
		for _, in := range after {
			if in.State != alerting.StateFiring {
				continue
			}
			cr.Firing = append(cr.Firing, checkInstance{Labels: displayLabels(in.Labels), Value: in.LastValue})
		}
		sort.Slice(cr.Firing, func(i, j int) bool { return labelKey(cr.Firing[i].Labels) < labelKey(cr.Firing[j].Labels) })
		res.Rules = append(res.Rules, cr)
	}

	if o.asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	return printAlertCheck(stdout, res)
}

// displayLabels drops what is the same on every instance of a rule.
func displayLabels(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		if k == "alertname" || k == "severity" {
			continue
		}
		out[k] = v
	}
	return out
}

func labelKey(l map[string]string) string {
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + l[k] + ",")
	}
	return b.String()
}

func alertNames(alerts []release.ShapeAlert) string {
	names := make([]string, len(alerts))
	for i, a := range alerts {
		names[i] = a.Alert
	}
	return strings.Join(names, ", ")
}

func printAlertCheck(w io.Writer, res checkResult) error {
	firing, failed := 0, 0
	fmt.Fprintf(w, "alert rules for env %q, evaluated once against the local ClickStack (no notifications; `for` not applied)\n\n", res.Env)
	for _, r := range res.Rules {
		state := "ok"
		switch {
		case r.Platform:
			state = "n/a (the control plane raises it from its own events)"
		case r.Error != "":
			state = "ERROR"
			failed++
		case len(r.Firing) > 0:
			state = fmt.Sprintf("FIRING x%d", len(r.Firing))
			firing += len(r.Firing)
		}
		fmt.Fprintf(w, "%-34s %-9s %-8s for %-5s  %s\n", r.Alert, r.Severity, r.Source, r.For, state)
		for _, in := range r.Firing {
			fmt.Fprintf(w, "    %s  value=%s\n", formatLabels(in.Labels), formatValue(in.Value))
		}
		if r.Error != "" {
			fmt.Fprintf(w, "    %s\n", firstLine(r.Error))
		}
	}
	fmt.Fprintf(w, "\n%d rule(s), %d instance(s) returning now, %d rule(s) failed\n", len(res.Rules), firing, failed)
	if failed > 0 {
		return errors.New("some rules could not be evaluated")
	}
	return nil
}

func formatLabels(l map[string]string) string {
	if len(l) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%q", k, l[k])
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func formatValue(v float64) string { return fmt.Sprintf("%.6g", v) }
