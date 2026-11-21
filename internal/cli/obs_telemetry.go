package cli

// `forge obs telemetry [env]`: the systems an env declares forge.Telemetry
// for (systems forge did not build: an image-only workload, a raw manifest
// group, a host-run engine, a Helm chart), what each declares, and whether
// its telemetry is arriving: per signal the newest row and how many the range
// holds, and per scraped endpoint its newest `up` and sample count. The
// targets come from the env's render (pkg/telemetry.Target, exactly what the
// collectors consume); arrival is read from the env's local ClickStack with
// pkg/obsquery (TelemetryArrival, TelemetryScrapes).

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsdash"
	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/pkg/obsquery"
	"github.com/reliant-labs/forge/pkg/telemetry"
)

const schemaObsTelemetry = "forge.obs.telemetry/v1"

var obsTelemetrySchema = obsout.Schema{
	Name: schemaObsTelemetry,
	Fields: []string{
		"scope", "scope.env", "scope.backend", "scope.env_id", "scope.from", "scope.to", "scope.services", "scope.instance", "scope.instance.id", "scope.instance.label", "scope.instance.user",
		"arrival", "arrival_note",
		"targets", "targets.service", "targets.source", "targets.attachment", "targets.runtime", "targets.scope", "targets.declared",
		"targets.dashboards", "targets.default_alerts",
		"targets.signals", "targets.signals.signal", "targets.signals.status", "targets.signals.last_seen", "targets.signals.rows", "targets.signals.note",
		"targets.scrapes", "targets.scrapes.instance", "targets.scrapes.up", "targets.scrapes.samples", "targets.scrapes.kept", "targets.scrapes.sample_limit", "targets.scrapes.last_seen",
		"statements", "statements.purpose", "statements.sql", "statements.params.*",
		"suggested",
	},
}

// Arrival states of a document (obsTelemetryDoc.Arrival) and of a signal
// (obsTelemetrySignalDoc.Status).
const (
	telemetryArrivalRead    = "read"
	telemetryArrivalUnknown = "unknown"

	telemetrySignalArriving = "arriving"
	telemetrySignalAbsent   = "absent"
	telemetrySignalUnknown  = "unknown"
)

type obsTelemetryDoc struct {
	obsout.Head
	Scope obsout.Scope `json:"scope"`
	// Arrival is "read" when the env's store answered, "unknown" when it
	// could not be asked (ArrivalNote says why); the targets are listed
	// either way.
	Arrival     string                  `json:"arrival"`
	ArrivalNote string                  `json:"arrival_note,omitempty"`
	Targets     []obsTelemetryTargetDoc `json:"targets"`
	Statements  []obsbackend.Statement  `json:"statements,omitempty"`
	Suggested   []string                `json:"suggested,omitempty"`
}

type obsTelemetryTargetDoc struct {
	Service    string `json:"service"`
	Source     string `json:"source"`
	Attachment string `json:"attachment"`
	Runtime    string `json:"runtime"`
	Scope      string `json:"scope"`
	// Declared is what the Telemetry collects (Target.Signals).
	Declared []string `json:"declared"`
	// Dashboards are the ids of the dashboards forge generates for it: its
	// system dashboard, then its declared ones.
	Dashboards    []string                `json:"dashboards"`
	DefaultAlerts bool                    `json:"default_alerts"`
	Signals       []obsTelemetrySignalDoc `json:"signals"`
	Scrapes       []obsTelemetryScrapeDoc `json:"scrapes,omitempty"`
}

type obsTelemetrySignalDoc struct {
	Signal   string    `json:"signal"`
	Status   string    `json:"status"`
	LastSeen time.Time `json:"last_seen,omitzero"`
	Rows     int64     `json:"rows"`
	Note     string    `json:"note,omitempty"`
}

type obsTelemetryScrapeDoc struct {
	Instance string  `json:"instance"`
	Up       float64 `json:"up"`
	Samples  float64 `json:"samples"`
	Kept     float64 `json:"kept"`
	// SampleLimit is the largest sample_limit the target's scrapes declare.
	SampleLimit int       `json:"sample_limit"`
	LastSeen    time.Time `json:"last_seen"`
}

// telemetryTargetsFunc reads the targets env declares.
type telemetryTargetsFunc func(ctx context.Context, env string) ([]telemetry.Target, error)

// renderedTelemetryTargets renders env's declaration and returns its targets.
func renderedTelemetryTargets(ctx context.Context, env string) ([]telemetry.Target, error) {
	dir, err := requireProjectDir("obs telemetry")
	if err != nil {
		return nil, err
	}
	e, err := RenderKCL(ctx, dir, env)
	if err != nil {
		return nil, obsbackend.Undetermined("cannot read env %q's declaration: %v", env, err).With(obsbackend.Cmd("forge", "env", "render", env))
	}
	return e.Telemetry, nil
}

func newObsTelemetryCmd() *cobra.Command {
	var o obsCommon
	cmd := &cobra.Command{
		Use:   "telemetry [env]",
		Short: "The systems the env declares forge.Telemetry for, and whether their telemetry is arriving",
		Long: `List every forge.Telemetry the env declares (systems forge did not build: an
image-only workload, a raw manifest group, a host-run engine, a Helm chart),
what each collects, the dashboards forge generates for it, and whether its
telemetry is arriving: per signal the newest row and how many the range holds
(--since, default 1h), and per scraped endpoint its newest ` + "`up`" + ` (1 = the
scrape succeeded) and samples against its sample_limit. [env] defaults to dev.

  ` + Name() + ` obs telemetry
  ` + Name() + ` obs telemetry prod --json
  ` + Name() + ` obs telemetry dev --service temporal --since 15m

The targets are the env's render (` + "`forge env render <env> --json`" + ` prints them);
arrival is read from the env's local ClickStack. An env whose telemetry goes
to the hosted store lists its targets with arrival unknown: read them in the
console, on each system's "telemetry-<service>" dashboard.

` + obsTelemetrySchema.Help(),
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runObsTelemetry(cmd.Context(), defaultObsDeps(cmd), renderedTelemetryTargets, obsEnvArg(args, 0), o)
		},
	}
	o.register(cmd, time.Hour, 0)
	return cmd
}

func runObsTelemetry(ctx context.Context, d obsDeps, targetsOf telemetryTargetsFunc, env string, o obsCommon) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fields, err := o.out.Parsed(obsTelemetrySchema)
	if err != nil {
		return obsFail(d, o.out.JSON, schemaObsTelemetry, err)
	}
	fail := func(err error) error { return obsFail(d, o.out.JSON, schemaObsTelemetry, err) }
	if len(o.filters) > 0 {
		return fail(obsbackend.Wrong("--filter does not apply to obs telemetry: a target is named by --service"))
	}
	n, err := o.narrow(d.Now())
	if err != nil {
		return fail(err)
	}
	targets, err := targetsOf(ctx, env)
	if err != nil {
		return fail(err)
	}
	if len(o.services) > 0 {
		targets = slices.DeleteFunc(slices.Clone(targets), func(t telemetry.Target) bool { return !slices.Contains(o.services, t.ServiceName) })
	}

	doc := obsTelemetryDoc{Scope: obsout.Scope{Env: env, From: n.Range.From, To: n.Range.To, Services: n.Services}, Arrival: telemetryArrivalUnknown, Targets: []obsTelemetryTargetDoc{}}
	var res telemetryArrival
	read := false
	if len(targets) == 0 {
		doc.ArrivalNote = fmt.Sprintf("env %s declares no forge.Telemetry", env)
		if len(o.services) > 0 {
			doc.ArrivalNote = fmt.Sprintf("env %s declares no forge.Telemetry for %v", env, o.services)
		}
	} else if res, read, err = readTelemetryArrival(ctx, d, &o, env, n, targets, &doc); err != nil {
		return fail(err)
	}
	if read {
		doc.Arrival = telemetryArrivalRead
	}
	if n.Explain {
		doc.Statements = res.statements
	}
	for _, t := range targets {
		doc.Targets = append(doc.Targets, telemetryTargetDoc(t, res, read))
	}
	doc.Suggested = obsout.Suggest(obsout.Command("forge", "env", "render", env, "--json"))
	doc.Stamp(schemaObsTelemetry, nil)
	if done, err := o.out.Emit(d.Stdout, obsTelemetrySchema, doc, fields); done || err != nil {
		return err
	}
	if n.Explain {
		explainText(d.Stdout, res.statements)
		return nil
	}
	renderObsTelemetry(d.Stdout, doc)
	return nil
}

// telemetryArrival is what the backend answered for every target: the reads
// of each group (project and platform systems are read apart), with each
// service's unread signals.
type telemetryArrival struct {
	signals    []obsbackend.TelemetrySignalArrival
	scrapes    []obsbackend.TelemetryScrapeResult
	unread     map[string]map[string]string
	statements []obsbackend.Statement
}

// readTelemetryArrival asks env's backend whether the targets' telemetry is
// arriving. A backend that cannot be asked (the stack is down, the env's
// telemetry goes to the hosted store) is not a failure: read is false, and
// doc says why.
func readTelemetryArrival(ctx context.Context, d obsDeps, o *obsCommon, env string, n obsbackend.Narrow, targets []telemetry.Target, doc *obsTelemetryDoc) (telemetryArrival, bool, error) {
	var none telemetryArrival
	b, _, err := o.open(ctx, d, env, "telemetry")
	if err != nil {
		doc.ArrivalNote = err.Error()
		return none, false, nil
	}
	info := b.Info()
	doc.Scope.Backend, doc.Scope.EnvID, doc.Scope.Instance = info.Backend, info.EnvID, obsScopeInstance(info.Instances)
	r, ok := b.(obsbackend.TelemetryArrivalReader)
	if !ok {
		doc.ArrivalNote = fmt.Sprintf("the %s backend does not serve per-target arrival: read each system's telemetry-<service> dashboard in the console", info.Backend)
		return none, false, nil
	}
	// A platform system's signals carry no env, so it is read without one.
	var project, platform []string
	for _, t := range targets {
		if t.Scope == telemetry.ScopePlatform {
			platform = append(platform, t.ServiceName)
		} else {
			project = append(project, t.ServiceName)
		}
	}
	out := telemetryArrival{unread: map[string]map[string]string{}}
	for _, group := range []struct {
		services []string
		platform bool
	}{{project, false}, {platform, true}} {
		if len(group.services) == 0 {
			continue
		}
		gn := n
		gn.Services = group.services
		res, err := r.TelemetryArrival(ctx, obsbackend.TelemetryArrivalRequest{Narrow: gn, Platform: group.platform})
		if err != nil {
			return none, false, err
		}
		out.signals = append(out.signals, res.Signals...)
		out.scrapes = append(out.scrapes, res.Scrapes...)
		out.statements = append(out.statements, res.Statements...)
		for _, svc := range group.services {
			out.unread[svc] = res.Unread
		}
	}
	return out, !n.Explain, nil
}

// telemetrySignalsOf are the signals worth asking about for t: what it
// declares, plus logs, which every system writes (its container's or its
// process's output). A check's results are metrics.
func telemetrySignalsOf(t telemetry.Target) []string {
	declared := t.Signals()
	var out []string
	for _, sig := range obsquery.ArrivalSignals {
		want := sig == obsquery.ArrivalLogs || slices.Contains(declared, sig)
		if sig == obsquery.ArrivalMetrics && slices.Contains(declared, telemetry.SignalChecks) {
			want = true
		}
		if want {
			out = append(out, sig)
		}
	}
	return out
}

func telemetryTargetDoc(t telemetry.Target, res telemetryArrival, read bool) obsTelemetryTargetDoc {
	doc := obsTelemetryTargetDoc{Service: t.ServiceName, Source: t.Source, Attachment: string(t.Attachment), Runtime: string(t.Runtime), Scope: string(t.Scope),
		Declared: t.Signals(), Dashboards: []string{}, DefaultAlerts: t.DefaultAlerts, Signals: []obsTelemetrySignalDoc{}}
	if t.Dashboard.Enabled {
		doc.Dashboards = append(doc.Dashboards, obsdash.TelemetryDashboardPrefix+t.ServiceName)
	}
	if systems, err := obsdash.TelemetrySystemsOf([]obsdash.EnvTelemetry{{Targets: []telemetry.Target{t}}}); err == nil && len(systems) == 1 {
		for _, d := range systems[0].Dashboards {
			doc.Dashboards = append(doc.Dashboards, d.ID)
		}
	}
	for _, sig := range telemetrySignalsOf(t) {
		s := obsTelemetrySignalDoc{Signal: sig, Status: telemetrySignalUnknown}
		switch why, unread := res.unread[t.ServiceName][sig]; {
		case !read:
		case unread:
			s.Note = why
		default:
			s.Status = telemetrySignalAbsent
			for _, a := range res.signals {
				if a.Service == t.ServiceName && a.Signal == sig {
					s.Status, s.LastSeen, s.Rows = telemetrySignalArriving, a.LastSeen.UTC(), a.Rows
				}
			}
		}
		doc.Signals = append(doc.Signals, s)
	}
	limit := 0
	for _, m := range t.Metrics {
		limit = max(limit, m.SampleLimit)
	}
	for _, sc := range res.scrapes {
		if sc.Service == t.ServiceName {
			doc.Scrapes = append(doc.Scrapes, obsTelemetryScrapeDoc{Instance: sc.Instance, Up: sc.Up, Samples: sc.Samples, Kept: sc.Kept, SampleLimit: limit, LastSeen: sc.LastSeen.UTC()})
		}
	}
	return doc
}

func renderObsTelemetry(w io.Writer, doc obsTelemetryDoc) {
	if len(doc.Targets) == 0 {
		fmt.Fprintln(w, doc.ArrivalNote)
		return
	}
	rows := make([][]string, 0, len(doc.Targets))
	for _, t := range doc.Targets {
		for i, s := range t.Signals {
			service, source := t.Service, t.Source+" ("+t.Runtime+")"
			if i > 0 {
				service, source = "", ""
			}
			last, count := "-", "-"
			if s.Status == telemetrySignalArriving {
				last, count = s.LastSeen.Format(time.RFC3339), strconv.FormatInt(s.Rows, 10)
			}
			status := s.Status
			if s.Note != "" {
				status += ": " + s.Note
			}
			rows = append(rows, []string{service, source, s.Signal, status, last, count})
		}
	}
	obsout.Table(w, []string{"SERVICE", "SOURCE", "SIGNAL", "STATUS", "LAST SEEN", "ROWS"}, rows, 60)
	var scrapes [][]string
	for _, t := range doc.Targets {
		for _, s := range t.Scrapes {
			scrapes = append(scrapes, []string{t.Service, s.Instance, strconv.FormatFloat(s.Up, 'f', -1, 64),
				strconv.FormatFloat(s.Samples, 'f', -1, 64), strconv.FormatFloat(s.Kept, 'f', -1, 64), strconv.Itoa(s.SampleLimit), s.LastSeen.Format(time.RFC3339)})
		}
	}
	if len(scrapes) > 0 {
		fmt.Fprintln(w)
		obsout.Table(w, []string{"SERVICE", "ENDPOINT", "UP", "SAMPLES", "KEPT", "LIMIT", "LAST SCRAPE"}, scrapes, 60)
	}
	if doc.Arrival == telemetryArrivalUnknown && doc.ArrivalNote != "" {
		fmt.Fprintf(w, "\narrival unknown: %s\n", doc.ArrivalNote)
	}
	for _, t := range doc.Targets {
		if len(t.Dashboards) > 0 {
			fmt.Fprintf(w, "\n%s dashboards: %v", t.Service, t.Dashboards)
		}
	}
	fmt.Fprintln(w)
}
