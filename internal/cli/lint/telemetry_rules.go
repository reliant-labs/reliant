// File: internal/cli/lint/telemetry_rules.go
//
// The four rules of the telemetry lane (telemetry.go), over rendered and
// normalized targets, and the locator that puts each finding on its KCL line.
// Nothing here renders; the tests drive checkTelemetry with targets directly.
package lint

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/lint/secrets"
	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/telemetry"
)

// telemetryDefaultSampleLimit is MetricsScrape.sample_limit's default
// (kcl/telemetry.k). A scrape that raises it holds more series than the
// default ceiling allows.
const telemetryDefaultSampleLimit = 5000

// promMetricName is a Prometheus exposition name. A field that is not one
// (an OpenTelemetry name such as k8s.pod.cpu.time) cannot come from a scrape,
// so the scrape rules do not judge it.
var promMetricName = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

// counterSuffixes are the exposition suffixes of a cumulative value: a
// counter's _total, and a histogram's or summary's _count and _sum.
var counterSuffixes = []string{"_total", "_count", "_sum"}

// telemetryIssue is one finding before it is merged across envs: the same
// declaration rendered into several envs is reported once, naming them all.
type telemetryIssue struct {
	rule     string
	severity finding.Severity
	at       kclLine
	message  string
	fix      string
	env      string
}

// checkTelemetry runs every rule over envs and returns one finding per
// distinct declaration, each naming the envs it was found in.
func checkTelemetry(envs []telemetryEnv, loc *telemetryLocator) []finding.Finding {
	c := telemetryChecker{loc: loc, res: map[string]*regexp.Regexp{}}
	for _, e := range envs {
		for _, t := range e.Targets {
			c.scrapes(e, t)
			c.dashboards(e, t)
		}
	}
	return c.merged()
}

type telemetryChecker struct {
	loc    *telemetryLocator
	res    map[string]*regexp.Regexp
	issues []telemetryIssue
}

// anchored compiles pattern as the collector applies it: anchored, RE2. nil
// for a pattern that does not compile — telemetry.Validate reports it, and
// these rules then assume nothing about it.
func (c *telemetryChecker) anchored(pattern string) *regexp.Regexp {
	if re, ok := c.res[pattern]; ok {
		return re
	}
	re, err := regexp.Compile("^(?:" + pattern + ")$")
	if err != nil {
		re = nil
	}
	c.res[pattern] = re
	return re
}

// keeps reports whether scrape s keeps the exposition name: its allow (none
// keeps every name) matches and its deny does not. A pattern that does not
// compile is taken to keep everything, so it never produces a finding.
func (c *telemetryChecker) keeps(s telemetry.MetricsScrape, name string) bool {
	// The receiver's own series are not subject to the scrape's relabeling.
	if name == "up" || strings.HasPrefix(name, "scrape_") {
		return true
	}
	if s.Allow != "" {
		if re := c.anchored(s.Allow); re != nil && !re.MatchString(name) {
			return false
		}
	}
	if s.Deny != "" {
		if re := c.anchored(s.Deny); re != nil && re.MatchString(name) {
			return false
		}
	}
	return true
}

// expositionNames are the names a scrape must keep for a series drawing
// field as metricType to have data. A Prometheus histogram x lands from
// x_bucket/x_sum/x_count, and a summary from x (its quantiles) and
// x_sum/x_count: keeping any of them collects the metric, so a partial
// allow is not reported (conservative).
func expositionNames(field, metricType string) []string {
	switch metricType {
	case dashspec.MetricTypeHistogram, dashspec.MetricTypeExpHistogram, dashspec.MetricTypeSummary:
		return []string{field, field + "_bucket", field + "_sum", field + "_count"}
	}
	return []string{field}
}

// scrapeCollects reports whether any of scrapes keeps a series of field.
func (c *telemetryChecker) scrapeCollects(scrapes []telemetry.MetricsScrape, field, metricType string) bool {
	for _, s := range scrapes {
		for _, n := range expositionNames(field, metricType) {
			if c.keeps(s, n) {
				return true
			}
		}
	}
	return false
}

// pushesMetrics reports whether t pushes metrics over OTLP: their names are
// the pusher's, so no scrape rule judges a field it could have pushed.
func pushesMetrics(t telemetry.Target) bool {
	return t.OTLP != nil && contains(t.OTLP.Signals, telemetry.SignalMetrics)
}

// scrapeLabel names one scrape of t in a message.
func scrapeLabel(t telemetry.Target, i int) string {
	s := t.Metrics[i]
	port := strconv.Itoa(s.Port)
	if s.PortName != "" {
		port = strconv.Quote(s.PortName)
	}
	if s.Name != "" {
		return fmt.Sprintf("telemetry service %q, metrics scrape %q (port %s)", t.ServiceName, s.Name, port)
	}
	return fmt.Sprintf("telemetry service %q, metrics scrape on port %s", t.ServiceName, port)
}

// scrapes runs the two per-scrape rules over t.
func (c *telemetryChecker) scrapes(e telemetryEnv, t telemetry.Target) {
	for i, s := range t.Metrics {
		if s.Allow != "" && len(s.DropLabels) > 0 && s.SampleLimit <= telemetryDefaultSampleLimit {
			continue
		}
		at := c.loc.scrape(e, t, i)
		label := scrapeLabel(t, i)
		if s.Allow == "" {
			c.add(telemetryIssue{
				rule: ruleTelemetryScrapeUnbounded, severity: finding.SeverityWarning, at: at, env: e.Name,
				message: fmt.Sprintf("%s has no allow, so it keeps every series the endpoint exposes, bounded only by sample_limit %d (a scrape over the limit fails whole: up = 0)", label, s.SampleLimit),
				fix:     fmt.Sprintf("Set allow to an anchored RE2 of the exposition names you draw or alert on (a histogram x needs x_bucket, x_sum and x_count). Waive it with `# forge:lint-disable-next-line %s: <why>` above the scrape.", ruleTelemetryScrapeUnbounded),
			})
		}
		if len(s.DropLabels) > 0 {
			continue
		}
		var why string
		switch {
		case s.SampleLimit > telemetryDefaultSampleLimit:
			why = fmt.Sprintf("raises sample_limit to %d (default %d)", s.SampleLimit, telemetryDefaultSampleLimit)
		case s.Allow == "":
			why = "keeps every name (no allow)"
		default:
			continue
		}
		c.add(telemetryIssue{
			rule: ruleTelemetryHighCardinality, severity: finding.SeverityWarning, at: at, env: e.Name,
			message: fmt.Sprintf("%s %s and drops no labels: a per-user, per-key or per-id label multiplies every name it is on into a series each, and the scrape fails whole once they pass the limit", label, why),
			fix:     fmt.Sprintf("Name the labels that explode in drop_labels, or narrow allow and keep sample_limit at %d. Waive it with `# forge:lint-disable-next-line %s: <why>` above the scrape.", telemetryDefaultSampleLimit, ruleTelemetryHighCardinality),
		})
	}
}

// dashboards runs the two per-series rules over t's declared dashboards.
func (c *telemetryChecker) dashboards(e telemetryEnv, t telemetry.Target) {
	for _, raw := range t.Dashboards {
		// A dashboard that does not parse is telemetry.Validate's (and forge
		// generate's) to report; there is nothing to judge here.
		d, err := dashspec.ParseDashboard(t.ServiceName, raw)
		if err != nil {
			continue
		}
		for _, p := range d.Panels {
			if p.Signal != dashspec.Metrics {
				continue
			}
			for _, s := range p.Series {
				c.uncollected(e, t, d, p, s)
				c.rateOnGauge(e, t, d, p, s)
			}
		}
	}
}

// uncollected reports a metrics series whose metric no scrape keeps.
func (c *telemetryChecker) uncollected(e telemetryEnv, t telemetry.Target, d dashspec.Dashboard, p dashspec.Panel, s dashspec.Series) {
	if !promMetricName.MatchString(s.Field) || pushesMetrics(t) {
		return
	}
	if c.scrapeCollects(t.Metrics, s.Field, s.MetricType) {
		return
	}
	// Another system's scrape in the env keeps it: a dashboard may draw a
	// neighbour's series, and it is collected.
	for _, other := range e.Targets {
		if other.ServiceName != t.ServiceName && c.scrapeCollects(other.Metrics, s.Field, s.MetricType) {
			return
		}
	}
	var why string
	switch {
	case len(t.Metrics) == 0:
		why = fmt.Sprintf("telemetry service %q declares no metrics scrape (and pushes no OTLP metrics)", t.ServiceName)
	default:
		var allows []string
		for _, m := range t.Metrics {
			if m.Allow != "" {
				allows = append(allows, strconv.Quote(m.Allow))
			}
		}
		why = fmt.Sprintf("no scrape of telemetry service %q keeps %s (allow %s, minus deny)",
			t.ServiceName, strings.Join(expositionNames(s.Field, s.MetricType), " or "), strings.Join(allows, ", "))
	}
	c.add(telemetryIssue{
		rule: ruleTelemetryMetricUncollected, severity: finding.SeverityError, at: c.loc.series(e, t, d, p, s), env: e.Name,
		message: fmt.Sprintf("dashboard %q panel %q (%s) draws metric %q, but %s: the series is empty in that env", d.ID, p.ID, p.Title, s.Field, why),
		fix:     fmt.Sprintf("Widen the scrape's allow to keep the metric (a histogram x: x_bucket|x_sum|x_count), or draw a metric it keeps. Waive it with `# forge:lint-disable-next-line %s: <why>`.", ruleTelemetryMetricUncollected),
	})
}

// rateOnGauge reports rate/increase over a metric nothing marks as a counter:
// a gauge, or a scraped name matched by no `counters` and carrying no
// counter suffix. A heuristic over names, so a warning: an exporter that
// types a counter without _total is right, and naming it in counters says so.
func (c *telemetryChecker) rateOnGauge(e telemetryEnv, t telemetry.Target, d dashspec.Dashboard, p dashspec.Panel, s dashspec.Series) {
	if s.Agg != dashspec.AggRate && s.Agg != dashspec.AggIncrease {
		return
	}
	var why string
	switch s.MetricType {
	case dashspec.MetricTypeGauge:
		why = "its metricType is gauge"
	case dashspec.MetricTypeSum:
		// Judge only a scraped exposition name: an OTLP-pushed sum is typed
		// by its pusher.
		if !promMetricName.MatchString(s.Field) || !c.scrapeCollects(t.Metrics, s.Field, s.MetricType) {
			return
		}
		if hasCounterSuffix(s.Field) || c.countersName(t.Metrics, s.Field) {
			return
		}
		why = "no scrape's counters matches it and it carries no _total, _count or _sum suffix, so it may be a gauge (rate over a gauge reads nothing)"
	default:
		return
	}
	c.add(telemetryIssue{
		rule: ruleTelemetryRateOnGauge, severity: finding.SeverityWarning, at: c.loc.series(e, t, d, p, s), env: e.Name,
		message: fmt.Sprintf("dashboard %q panel %q (%s) takes %s of metric %q (telemetry service %q), but %s", d.ID, p.ID, p.Title, s.Agg, s.Field, t.ServiceName, why),
		fix:     fmt.Sprintf("If the exporter types it as a gauge that is really a cumulative counter, add it to the scrape's counters; if it is a gauge, draw it with agg last/avg/max. Waive it with `# forge:lint-disable-next-line %s: <why>`.", ruleTelemetryRateOnGauge),
	})
}

func hasCounterSuffix(name string) bool {
	for _, suf := range counterSuffixes {
		if strings.HasSuffix(name, suf) {
			return true
		}
	}
	return false
}

// countersName reports whether a scrape's counters names field.
func (c *telemetryChecker) countersName(scrapes []telemetry.MetricsScrape, field string) bool {
	for _, s := range scrapes {
		if s.Counters == "" {
			continue
		}
		if re := c.anchored(s.Counters); re == nil || re.MatchString(field) {
			return true
		}
	}
	return false
}

func (c *telemetryChecker) add(i telemetryIssue) { c.issues = append(c.issues, i) }

// merged folds the issues of one declaration rendered into several envs into
// one finding naming them.
func (c *telemetryChecker) merged() []finding.Finding {
	type key struct {
		rule, file, message string
		line                int
	}
	var order []key
	envs := map[key][]string{}
	first := map[key]telemetryIssue{}
	for _, i := range c.issues {
		k := key{i.rule, i.at.file, i.message, i.at.line}
		if _, seen := first[k]; !seen {
			order = append(order, k)
			first[k] = i
		}
		if !contains(envs[k], i.env) {
			envs[k] = append(envs[k], i.env)
		}
	}
	out := make([]finding.Finding, 0, len(order))
	for _, k := range order {
		i := first[k]
		names := envs[k]
		sort.Strings(names)
		noun := "env"
		if len(names) > 1 {
			noun = "envs"
		}
		out = append(out, finding.Finding{
			Rule: i.rule, Severity: i.severity, File: i.at.file, Line: i.at.line,
			Message:     fmt.Sprintf("%s %s: %s", noun, strings.Join(names, ", "), i.message),
			Remediation: i.fix,
		})
	}
	return out
}

// ── Locating a declaration in the KCL source ────────────────────────────────

// kclLine is a project-relative file and 1-based line.
type kclLine struct {
	file string
	line int
}

// telemetryLocator finds a rendered declaration's line in the KCL source by
// the literals it was written with: a service_name, a scrape's name, a
// dashboard's or panel's id, a metric name. It prefers the env's own
// directory, then files no other env owns (a shared lib), and never another
// env's main.k. What it cannot find lands on the env's main.k, line 1.
type telemetryLocator struct {
	files []secrets.SourceFile
	lines map[string][]string
}

func newTelemetryLocator(files []secrets.SourceFile) *telemetryLocator {
	l := &telemetryLocator{lines: map[string][]string{}}
	l.files = append(l.files, files...)
	sort.SliceStable(l.files, func(i, j int) bool { return l.files[i].Path < l.files[j].Path })
	for _, f := range l.files {
		l.lines[f.Path] = strings.Split(f.Text, "\n")
	}
	return l
}

// isKCLCommentLine reports a line that is only a KCL comment.
func isKCLCommentLine(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "#")
}

// literalAssign matches `<key> = "<value>"`, key a whole word.
func literalAssign(key, value string) *regexp.Regexp {
	return regexp.MustCompile(`(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(key) + `\s*=\s*` + regexp.QuoteMeta(strconv.Quote(value)))
}

// quoted matches value as a KCL string literal anywhere on a line.
func quoted(value string) *regexp.Regexp {
	return regexp.MustCompile(regexp.QuoteMeta(strconv.Quote(value)))
}

// scan returns the nth (0-based) line of file at or after line from that
// matches re.
func (l *telemetryLocator) scan(file string, from int, re *regexp.Regexp, nth int) (kclLine, bool) {
	lines := l.lines[file]
	for i := max(from, 1) - 1; i < len(lines); i++ {
		if isKCLCommentLine(lines[i]) || !re.MatchString(lines[i]) {
			continue
		}
		if nth == 0 {
			return kclLine{file, i + 1}, true
		}
		nth--
	}
	return kclLine{}, false
}

// find returns the first line matching re: the env's own directory first,
// then files no other env owns.
func (l *telemetryLocator) find(e telemetryEnv, re *regexp.Regexp) (kclLine, bool) {
	if l == nil {
		return kclLine{}, false
	}
	dir := path.Dir(e.File)
	root := path.Dir(dir)
	other := func(p string) bool {
		d := path.Dir(p)
		return d != dir && path.Dir(d) == root && len(l.lines[d+"/main.k"]) > 0
	}
	for _, own := range []bool{true, false} {
		for _, f := range l.files {
			mine := strings.HasPrefix(f.Path, dir+"/")
			if mine != own || (!own && other(f.Path)) {
				continue
			}
			if at, ok := l.scan(f.Path, 1, re, 0); ok {
				return at, true
			}
		}
	}
	return kclLine{}, false
}

// target is where t is declared: its service_name, else the name of the
// entity it attaches to, else the env's main.k.
func (l *telemetryLocator) target(e telemetryEnv, t telemetry.Target) kclLine {
	if at, ok := l.find(e, literalAssign("service_name", t.ServiceName)); ok {
		return at
	}
	if _, name, ok := strings.Cut(t.Source, ":"); ok && name != "" {
		if at, ok := l.find(e, literalAssign("name", name)); ok {
			return at
		}
	}
	return kclLine{e.File, 1}
}

// scrape is where t's i-th scrape is declared: by its name, else the i-th
// MetricsScrape after the target, else the target.
func (l *telemetryLocator) scrape(e telemetryEnv, t telemetry.Target, i int) kclLine {
	at := l.target(e, t)
	if l == nil {
		return at
	}
	if name := t.Metrics[i].Name; name != "" {
		if got, ok := l.scan(at.file, at.line, literalAssign("name", name), 0); ok {
			return got
		}
	}
	if got, ok := l.scan(at.file, at.line, regexp.MustCompile(`\bMetricsScrape\s*\{`), i); ok {
		return got
	}
	return at
}

// series is where a panel's series is declared: the metric's literal after
// the panel's id, after the dashboard's id; else the panel, the dashboard,
// or the target.
func (l *telemetryLocator) series(e telemetryEnv, t telemetry.Target, d dashspec.Dashboard, p dashspec.Panel, s dashspec.Series) kclLine {
	dash, ok := l.find(e, literalAssign("id", d.ID))
	if !ok {
		return l.target(e, t)
	}
	panel, ok := l.scan(dash.file, dash.line, literalAssign("id", p.ID), 0)
	if !ok {
		return dash
	}
	if got, ok := l.scan(panel.file, panel.line, quoted(s.Field), 0); ok {
		return got
	}
	return panel
}
