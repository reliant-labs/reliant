package obsdash

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/telemetry"
)

// The dashboards of declared telemetry (forge.Telemetry, pkg/telemetry):
// systems forge did not build — an image-only workload, a raw manifest
// group, a host-run engine, a Helm chart.
//
// Every system gets a SYSTEM DASHBOARD, "telemetry-<service>", drawn from
// signal-generic facts alone: whether its metrics are being collected (the
// Prometheus receiver's own `up` and `scrape_*` series), the CPU, memory and
// restarts the node agent reports for its pods, its log volume by severity
// and its error lines, its span rate, error ratio and p95 by span name when it
// pushes traces, its check status when it declares checks, and where its
// profiles are when it declares them. It names no vendor metric: what a
// system's own series mean is the project's to draw, in the system's declared
// `dashboards`, which ride beside it.
//
// Both are written like every generated dashboard (a HyperDX file for the
// local stack, a spec part, the release bundle) and replaced by a hand file of
// the same name (OverridePath). Each system's dashboards are one spec part,
// TelemetrySpecFile, so two branches that declare different systems change
// different files.

// TelemetryDashboardPrefix starts the id of a system dashboard:
// "telemetry-<service>", so its file is "<project>-telemetry-<service>_gen.json".
const TelemetryDashboardPrefix = "telemetry-"

// TelemetrySpecFile is the project-relative file of service's TelemetryPart.
func TelemetrySpecFile(service string) string {
	return SpecDir + "/" + TelemetryDashboardPrefix + service + genSuffix
}

// TelemetrySystem is one declared system as its dashboards and default alerts
// see it: the facts of every environment that declares it, merged
// (TelemetrySystemsOf).
type TelemetrySystem struct {
	// Service is the service.name every signal of the system carries.
	Service string
	// Platform is scope "platform": the system's signals carry no env
	// identity, so its dashboards are scope platform and its alerts read no
	// env.
	Platform bool
	// Title replaces the system dashboard's title.
	Title string
	// SystemDashboard is false when every env turned the system dashboard off.
	SystemDashboard bool
	// DefaultAlerts says some env arms the default alerts.
	DefaultAlerts bool
	// Scrapes are the Prometheus endpoints the collector pulls.
	Scrapes []TelemetryScrape
	// Traces says the system pushes spans over OTLP.
	Traces bool
	// TCPChecks and HTTPChecks say it declares a check of that kind.
	TCPChecks, HTTPChecks bool
	// ProfileTypes are the pprof types pulled from it, sorted.
	ProfileTypes []string
	// NodeMetadata says it runs where a node agent reports per-container
	// CPU, memory and restarts (a cluster or hosted runtime).
	NodeMetadata bool
	// Workloads are the forge workload names it is (a Workload.telemetry):
	// its node metrics are keyed by k8s.workload.name, which the node agent
	// stamps for every workload. Empty for a system forge reaches by selector
	// or address, whose node metrics carry its service.name.
	Workloads []string
	// Dashboards are the system's declared dashboards, parsed, an unset
	// scope taken from the system's.
	Dashboards []Dashboard
}

// TelemetryScrape is one Prometheus endpoint of a system.
type TelemetryScrape struct {
	// Name distinguishes several scrapes of one system ("" for the only one).
	Name string
	// SampleLimit is the most samples a scrape may return before it fails
	// (up = 0); the largest any env declares.
	SampleLimit int
}

// EnvTelemetry is one environment's declared telemetry targets, as
// KCLEntities.Telemetry holds them.
type EnvTelemetry struct {
	Env     string
	Targets []telemetry.Target
}

// TelemetrySystemsOf merges the targets every environment declares into one
// system per service.name, sorted by service, with each declared dashboard
// parsed by dashspec.ParseDashboard.
//
// Dashboards are bundle-wide (a release carries one set, promoted to every
// env), so what several envs declare must agree: a service is one scope in
// every env, a system dashboard has one title, and a dashboard id is one
// dashboard. A disagreement is an error naming both envs. Signals union: a
// system scraped in prod alone still has its collection panels.
func TelemetrySystemsOf(envs []EnvTelemetry) ([]TelemetrySystem, error) {
	type origin struct{ env, source string }
	type declared struct {
		origin
		canonical []byte
	}
	var (
		errs      []string
		order     []string
		systems   = map[string]*TelemetrySystem{}
		scopes    = map[string]origin{}
		titles    = map[string]origin{}
		dashboard = map[string]declared{}
	)
	where := func(o origin) string {
		if o.env == "" {
			return o.source
		}
		return fmt.Sprintf("env %s (%s)", o.env, o.source)
	}
	for _, e := range envs {
		for _, t := range e.Targets {
			here := origin{e.Env, t.Source}
			s := systems[t.ServiceName]
			if s == nil {
				s = &TelemetrySystem{Service: t.ServiceName, Platform: t.Scope == telemetry.ScopePlatform}
				systems[t.ServiceName] = s
				order = append(order, t.ServiceName)
				scopes[t.ServiceName] = here
			} else if s.Platform != (t.Scope == telemetry.ScopePlatform) {
				errs = append(errs, fmt.Sprintf("telemetry service %q is scope %s in %s but %s in %s: its dashboards and alerts are one set for every env, so the scope must agree",
					t.ServiceName, scopeName(s.Platform), where(scopes[t.ServiceName]), t.Scope, where(here)))
			}
			if t.Dashboard.Title != "" {
				if s.Title != "" && s.Title != t.Dashboard.Title {
					errs = append(errs, fmt.Sprintf("telemetry service %q titles its system dashboard %q in %s but %q in %s: the dashboard is one for every env",
						t.ServiceName, s.Title, where(titles[t.ServiceName]), t.Dashboard.Title, where(here)))
				} else if s.Title == "" {
					s.Title, titles[t.ServiceName] = t.Dashboard.Title, here
				}
			}
			mergeTarget(s, t)
			for i, raw := range t.Dashboards {
				name := fmt.Sprintf("%s dashboards[%d]", where(here), i)
				d, err := ParseDashboard(name, raw)
				if err != nil {
					errs = append(errs, err.Error())
					continue
				}
				if d.Scope == "" && s.Platform {
					d.Scope = dashspec.ScopePlatform
				}
				canonical, err := json.Marshal(d)
				if err != nil {
					return nil, err
				}
				if prev, seen := dashboard[d.ID]; seen {
					if !bytes.Equal(prev.canonical, canonical) {
						errs = append(errs, fmt.Sprintf("dashboard %q is declared by %s and, differently, by %s: dashboards ship bundle-wide, so one declared in several places must be the same in each",
							d.ID, where(prev.origin), where(here)))
					}
					continue
				}
				dashboard[d.ID] = declared{here, canonical}
				s.Dashboards = append(s.Dashboards, d)
			}
		}
	}
	if len(errs) > 0 {
		return nil, errors.New(strings.Join(errs, "\n"))
	}
	sort.Strings(order)
	out := make([]TelemetrySystem, 0, len(order))
	for _, svc := range order {
		s := systems[svc]
		sort.Slice(s.Scrapes, func(i, j int) bool { return s.Scrapes[i].Name < s.Scrapes[j].Name })
		sort.Strings(s.ProfileTypes)
		s.Workloads = sortedIdentities(s.Workloads)
		out = append(out, *s)
	}
	return out, nil
}

func scopeName(platform bool) string {
	if platform {
		return string(telemetry.ScopePlatform)
	}
	return string(telemetry.ScopeProject)
}

// mergeTarget adds what one env's target says about the system to s.
func mergeTarget(s *TelemetrySystem, t telemetry.Target) {
	s.SystemDashboard = s.SystemDashboard || t.Dashboard.Enabled
	s.DefaultAlerts = s.DefaultAlerts || t.DefaultAlerts
	s.NodeMetadata = s.NodeMetadata || t.Runtime == telemetry.RuntimeCluster || t.Runtime == telemetry.RuntimeHosted
	if t.Attachment == telemetry.AttachWorkload {
		s.Workloads = append(s.Workloads, strings.TrimPrefix(t.Source, string(telemetry.AttachWorkload)+":"))
	}
	for _, m := range t.Metrics {
		merged := false
		for i := range s.Scrapes {
			if s.Scrapes[i].Name == m.Name {
				s.Scrapes[i].SampleLimit = max(s.Scrapes[i].SampleLimit, m.SampleLimit)
				merged = true
			}
		}
		if !merged {
			s.Scrapes = append(s.Scrapes, TelemetryScrape{Name: m.Name, SampleLimit: m.SampleLimit})
		}
	}
	if t.OTLP != nil {
		for _, sig := range t.OTLP.Signals {
			s.Traces = s.Traces || sig == telemetry.SignalTraces
		}
	}
	for _, c := range t.Checks {
		s.TCPChecks = s.TCPChecks || c.Kind == "tcp"
		s.HTTPChecks = s.HTTPChecks || c.Kind == "http"
	}
	for _, p := range t.Profiles {
		for _, ty := range p.Types {
			if !contains(s.ProfileTypes, ty) {
				s.ProfileTypes = append(s.ProfileTypes, ty)
			}
		}
	}
}

// TelemetryPart is one system's dashboards as forge generate writes them
// (TelemetrySpecFile): its system dashboard, then its declared ones.
type TelemetryPart struct {
	Version    string      `json:"version"`
	Service    string      `json:"service"`
	Dashboards []Dashboard `json:"dashboards"`
}

// BuildTelemetryParts is one part per declared system that has a dashboard.
func BuildTelemetryParts(in Input) []TelemetryPart {
	var out []TelemetryPart
	for _, s := range in.Telemetry {
		var ds []Dashboard
		if s.SystemDashboard {
			ds = append(ds, BuildSystemDashboard(in.Project, s))
		}
		ds = append(ds, s.Dashboards...)
		if len(ds) == 0 {
			continue
		}
		out = append(out, TelemetryPart{Version: PartsVersion, Service: s.Service, Dashboards: ds})
	}
	return out
}

// TelemetryDashboards is every dashboard of systems, in service order: what a
// release bundle carries for the env that declares them.
func TelemetryDashboards(project string, systems []TelemetrySystem) []Dashboard {
	var out []Dashboard
	for _, p := range BuildTelemetryParts(Input{Project: project, Telemetry: systems}) {
		out = append(out, p.Dashboards...)
	}
	return out
}

// withTelemetryParts is project with the parts' dashboards after its own.
func withTelemetryParts(project ProjectPart, parts []TelemetryPart) ProjectPart {
	if len(parts) == 0 {
		return project
	}
	project.Dashboards = append([]Dashboard(nil), project.Dashboards...)
	for _, p := range parts {
		project.Dashboards = append(project.Dashboards, p.Dashboards...)
	}
	return project
}

// declaredDashboardIDs are the ids of the systems' declared dashboards.
func declaredDashboardIDs(systems []TelemetrySystem) map[string]bool {
	ids := map[string]bool{}
	for _, s := range systems {
		for _, d := range s.Dashboards {
			ids[d.ID] = true
		}
	}
	return ids
}

// checkUniqueDashboardIDs refuses two dashboards with one id: each is one
// file named for its id, and the console keys a dashboard on it. A declared
// dashboard can take a generated one's id ("overview", "telemetry-<service>").
func checkUniqueDashboardIDs(sp Spec) error {
	seen := map[string]bool{}
	for _, d := range sp.Dashboards {
		if seen[d.ID] {
			return fmt.Errorf("dashboard id %q is used twice: a declared telemetry dashboard takes the id of another dashboard of the project; rename it", d.ID)
		}
		seen[d.ID] = true
	}
	return nil
}

// localDrawable is d with only what the local HyperDX can draw: a declared
// dashboard is the project's, and may hold panels only the console draws (a
// summary quantile, a sql panel) or a filter on a field HyperDX cannot map.
// The local file leaves those out; the bundle ships d whole.
func localDrawable(d Dashboard) Dashboard {
	out := d
	out.Filters = nil
	for _, f := range d.Filters {
		sig := f.Signal
		if sig == "" {
			sig = Traces
		}
		if _, err := fieldSQL(sig, f.Field); err == nil {
			out.Filters = append(out.Filters, f)
		}
	}
	out.Panels = nil
	for _, p := range d.Panels {
		if !hyperDXDraws(p) {
			continue
		}
		if _, err := renderChart(p); err != nil {
			continue
		}
		out.Panels = append(out.Panels, p)
	}
	return out
}

// System dashboard series: the OpenTelemetry Prometheus receiver's own
// per-scrape gauges, and the check receivers' (tcpcheck, httpcheck). They are
// the collector's, so every system has them, whatever it exports.
const (
	metricUp                 = "up"
	metricScrapeSamples      = "scrape_samples_scraped"
	metricScrapeSamplesKept  = "scrape_samples_post_metric_relabeling"
	metricScrapeDuration     = "scrape_duration_seconds"
	metricTCPCheckStatus     = "tcpcheck.status"
	metricTCPCheckDuration   = "tcpcheck.duration"
	metricHTTPCheckStatus    = "httpcheck.status"
	metricHTTPCheckDuration  = "httpcheck.duration"
	metricHTTPCheckError     = "httpcheck.error"
	fieldInstance            = "resource.attr.service.instance.id"
	fieldTCPCheckEndpoint    = "metric.attr.tcpcheck.endpoint"
	fieldHTTPCheckURL        = "metric.attr.http.url"
	fieldHTTPCheckStatusCls  = "metric.attr.http.status_class"
	telemetryProfileCommand  = "forge obs profile"
	telemetrySystemTag       = "telemetry"
	telemetrySectionScrape   = "collection"
	telemetrySectionResource = "resources"
	telemetrySectionLogs     = "logs"
	telemetrySectionTraces   = "traces"
	telemetrySectionChecks   = "checks"
	telemetrySectionProfiles = "profiles"
)

// BuildSystemDashboard is s's system dashboard: one section per signal it
// declares.
func BuildSystemDashboard(project string, s TelemetrySystem) Dashboard {
	d := Dashboard{
		ID:    TelemetryDashboardPrefix + s.Service,
		Title: project + ": " + s.Service,
		Group: GroupPlatform,
		Tags:  []string{telemetrySystemTag},
	}
	if s.Title != "" {
		d.Title = s.Title
	}
	if s.Platform {
		d.Scope = dashspec.ScopePlatform
	} else {
		d.Filters = []Filter{{ID: "env", Label: "deployment.environment.name", Field: FieldEnv}}
	}
	var what []string
	self := []Predicate{{Field: FieldService, Op: OpEq, Value: s.Service}}
	add := func(section, title string, panels ...Panel) {
		d.Sections = append(d.Sections, Section{ID: section, Title: title})
		for _, p := range panels {
			p.Section = section
			d.Panels = append(d.Panels, p)
		}
	}

	if len(s.Scrapes) > 0 {
		what = append(what, "collection health")
		d.Tags = append(d.Tags, telemetry.SignalMetrics)
		var limits []string
		for _, sc := range s.Scrapes {
			if sc.Name == "" {
				limits = append(limits, fmt.Sprint(sc.SampleLimit))
			} else {
				limits = append(limits, fmt.Sprintf("%s: %d", sc.Name, sc.SampleLimit))
			}
		}
		byInstance := []string{fieldInstance}
		gauge := func(label string, agg Agg, metric string) Series {
			return Series{Label: label, Agg: agg, Field: metric, MetricType: dashspec.MetricTypeGauge}
		}
		add(telemetrySectionScrape, "Collection",
			Panel{ID: "scrape-up", Title: "Scrape up", Description: "1 when the endpoint's last scrape succeeded, 0 when it failed: the target is down, the scrape timed out, or it returned more samples than its sample_limit. One line per scraped endpoint (service.instance.id).",
				Kind: KindLine, Signal: Metrics, Where: self, Series: []Series{gauge("up", AggMin, metricUp)}, GroupBy: byInstance, Unit: UnitNone, Width: 12, Height: 7},
			Panel{ID: "scrape-samples", Title: "Samples per scrape", Description: "Samples each scrape returned, and the ones kept after the allow/deny lists. A scrape over its sample_limit (" + strings.Join(limits, ", ") + ") fails and stores nothing.",
				Kind: KindLine, Signal: Metrics, Where: self, Series: []Series{gauge("scraped", AggMax, metricScrapeSamples), gauge("kept", AggMax, metricScrapeSamplesKept)}, Unit: UnitCount, Width: 12, Height: 7},
			Panel{ID: "scrape-duration", Title: "Scrape duration", Description: "How long each scrape took; one near its timeout is about to fail.",
				Kind: KindLine, Signal: Metrics, Where: self, Series: []Series{gauge("duration", AggMax, metricScrapeDuration)}, GroupBy: byInstance, Unit: UnitSeconds, Width: 24, Height: 7},
		)
	}

	if s.NodeMetadata {
		what = append(what, "CPU, memory and restarts")
		scope := self
		if len(s.Workloads) > 0 {
			scope = identityScope(fieldWorkload, s.Workloads)
		}
		var panels []Panel
		for _, p := range BuildWorkloads(project).Panels {
			switch p.ID {
			case "cpu-usage", "memory-working-set", "restarts", "restarts-by-exit-reason":
				p.Where = scoped(scope, p.Where...)
				panels = append(panels, p)
			}
		}
		add(telemetrySectionResource, "Resources", panels...)
	}

	what = append(what, "log volume and errors")
	add(telemetrySectionLogs, "Logs",
		Panel{ID: "log-volume", Title: "Log volume by severity", Description: "Log records per time bucket, one line per severity. A system whose lines are not parsed reports them unspecified; Telemetry.logs maps its levels.",
			Kind: KindLine, Signal: Logs, Where: self, Series: []Series{{Label: "records", Agg: AggCount}}, GroupBy: []string{"log.severity"}, Unit: UnitCount, Width: 24, Height: 7},
		Panel{ID: "error-logs", Title: "Error lines", Kind: KindSearch, Signal: Logs, Where: scoped(self, errorLogs), Columns: logColumns, Width: 24, Height: 8},
		Panel{ID: "logs", Title: "Recent lines", Kind: KindSearch, Signal: Logs, Where: self, Columns: logColumns, Width: 24, Height: 8},
	)

	if s.Traces {
		what = append(what, "span rate, error ratio and latency")
		d.Tags = append(d.Tags, telemetry.SignalTraces)
		bySpan := []string{"span.name"}
		add(telemetrySectionTraces, "Traces",
			Panel{ID: "span-rate", Title: "Spans by name", Description: "Spans the system pushed per time bucket, one line per span name. Spans may be sampled.",
				Kind: KindLine, Signal: Traces, Where: self, Series: []Series{{Label: "spans", Agg: AggCount}}, GroupBy: bySpan, Unit: UnitCount, Width: 12, Height: 7},
			Panel{ID: "span-errors", Title: "Span error ratio", Description: "Spans with status error over all spans.",
				Kind: KindLine, Signal: Traces, Where: self, Series: []Series{{Label: "errors", Agg: AggCount, Where: []Predicate{errorSpans}}, {Label: "spans", Agg: AggCount}}, Ratio: true, Unit: UnitPercent, Width: 12, Height: 7},
			Panel{ID: "span-p95", Title: "p95 duration by span name",
				Kind: KindLine, Signal: Traces, Where: self, Series: []Series{{Label: "p95", Agg: AggQuantile, Quantile: 0.95, Field: "span.duration"}}, GroupBy: bySpan, Unit: UnitMillis, Width: 24, Height: 7},
			Panel{ID: "spans-by-name", Title: "Rate, errors and latency by span name",
				Kind: KindTable, Signal: Traces, Where: self, Series: redTableSeries(), GroupBy: bySpan, Limit: 50, Width: 24, Height: 8},
		)
	}

	if s.TCPChecks || s.HTTPChecks {
		what = append(what, "check status")
		d.Tags = append(d.Tags, telemetry.SignalChecks)
		var panels []Panel
		var latency []Series
		if s.TCPChecks {
			panels = append(panels, Panel{ID: "tcp-checks", Title: "TCP checks", Description: "1 when the collector connected, 0 when it could not. One line per endpoint.",
				Kind: KindLine, Signal: Metrics, Where: self, Series: []Series{{Label: "connected", Agg: AggMin, Field: metricTCPCheckStatus, MetricType: dashspec.MetricTypeGauge}}, GroupBy: []string{fieldTCPCheckEndpoint}, Unit: UnitNone, Width: 12, Height: 7})
			latency = append(latency, Series{Label: "tcp", Agg: AggAvg, Field: metricTCPCheckDuration, MetricType: dashspec.MetricTypeGauge})
		}
		if s.HTTPChecks {
			panels = append(panels, Panel{ID: "http-checks", Title: "HTTP checks", Description: "1 when the endpoint answered 2xx, 0 when it answered anything else. One line per URL; a URL that did not answer at all draws no point.",
				Kind: KindLine, Signal: Metrics, Where: self, Series: []Series{{Label: "2xx", Agg: AggMax, Field: metricHTTPCheckStatus, MetricType: dashspec.MetricTypeSum, Where: []Predicate{{Field: fieldHTTPCheckStatusCls, Op: OpEq, Value: "2xx"}}}}, GroupBy: []string{fieldHTTPCheckURL}, Unit: UnitNone, Width: 12, Height: 7})
			latency = append(latency, Series{Label: "http", Agg: AggAvg, Field: metricHTTPCheckDuration, MetricType: dashspec.MetricTypeGauge})
		}
		panels = append(panels, Panel{ID: "check-latency", Title: "Check latency", Kind: KindLine, Signal: Metrics, Where: self, Series: latency, Unit: UnitMillis, Width: 24, Height: 7})
		add(telemetrySectionChecks, "Checks", panels...)
	}

	if len(s.ProfileTypes) > 0 {
		what = append(what, "profiles")
		d.Tags = append(d.Tags, telemetry.SignalProfiles)
		add(telemetrySectionProfiles, "Profiles", Panel{ID: "profiles", Title: "Profiles", Kind: KindText, Width: 24, Height: 4,
			Markdown: fmt.Sprintf("The collector pulls this system's pprof profiles (%s) into the env's profiles store, beside forge's own services'. Export them as a flamegraph:\n\n```\n%s --service %s --since 1h -o %s.pb.gz\ngo tool pprof -http=: %s.pb.gz\n```",
				strings.Join(s.ProfileTypes, ", "), telemetryProfileCommand, s.Service, s.Service, s.Service)})
	}

	d.Description = fmt.Sprintf("Declared telemetry for %s, a system forge did not build (forge.Telemetry): %s. Signal-generic: what its own series mean is drawn in the dashboards the Telemetry declares.",
		s.Service, strings.Join(what, ", "))
	if !s.Platform {
		d = withReleaseMarkers(d)
	}
	return d
}
