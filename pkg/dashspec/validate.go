package dashspec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Limits a dashboard file is held to. They sit below HyperDX's own caps (500
// tiles per dashboard) and the console's query caps, so a dashboard that
// validates here renders and runs everywhere.
const (
	maxPanelsPerDashboard = 200
	maxSeriesPerPanel     = 10
	maxGroupByPerPanel    = 3
	maxPredicates         = 20
	maxPredicateValues    = 50
	maxRegexLen           = 256
	maxTextLen            = 512
	maxGridHeight         = 60
)

// Problem is one thing wrong with a dashboard file, located in it.
type Problem struct {
	// Line and Col are 1-based; zero when the position is unknown.
	Line, Col int
	// Path is where in the document: panels[2].series[0].agg.
	Path    string
	Message string
}

// problemsError is every Problem found in one file. It prints one
// "file:line:col: path: message" line per problem, the shape editors and CI
// annotate.
type problemsError struct {
	file     string
	problems []Problem
}

func (e *problemsError) Error() string {
	var b strings.Builder
	for i, p := range e.problems {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(formatProblem(p, e.file))
	}
	return b.String()
}

// ProblemsOf returns the problems a ParseDashboard error carries, or nil for an
// error that is not one.
func ProblemsOf(err error) []Problem {
	var pe *problemsError
	if errors.As(err, &pe) {
		return pe.problems
	}
	return nil
}

func formatProblem(p Problem, file string) string {
	loc := file
	if p.Line > 0 {
		loc = fmt.Sprintf("%s:%d:%d", file, p.Line, p.Col)
	}
	if p.Path != "" {
		return fmt.Sprintf("%s: %s: %s", loc, p.Path, p.Message)
	}
	return fmt.Sprintf("%s: %s", loc, p.Message)
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

var tagPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

// ParseDashboard decodes and validates one dashboard file: a single Dashboard
// object in the renderer-neutral spec vocabulary (the objects of
// the spec's dashboards). name is only used in errors.
//
// Every problem found is reported, each with the line and column of the
// offending value (or, for an unknown field, of its key). Validation covers
// what the renderers and the console's query engine would otherwise discover
// one request at a time: field names that exist on the panel's signal, the
// operator set, series limits, a quantile's range, a metric series naming its
// metric and type, and unique ids.
func ParseDashboard(name string, raw []byte) (Dashboard, error) {
	ix, err := indexJSON(raw)
	if err != nil {
		return Dashboard{}, &problemsError{file: name, problems: []Problem{ix.syntaxProblem(err)}}
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return Dashboard{}, &problemsError{file: name, problems: []Problem{ix.syntaxProblem(err)}}
	}
	v := &validator{ix: ix}
	root, ok := tree.(map[string]any)
	if !ok {
		v.add("", "a dashboard file is one JSON object ({\"id\", \"title\", \"panels\", ...})")
	} else {
		v.dashboard(root)
	}
	if len(v.problems) > 0 {
		return Dashboard{}, &problemsError{file: name, problems: v.problems}
	}
	var d Dashboard
	if err := json.Unmarshal(raw, &d); err != nil {
		return Dashboard{}, &problemsError{file: name, problems: []Problem{ix.syntaxProblem(err)}}
	}
	return d, nil
}

// LooksLikeSpecDashboard reports whether raw is a spec-form dashboard rather
// than a HyperDX one: it has a "panels" array and no "tiles".
func LooksLikeSpecDashboard(raw []byte) bool {
	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) != nil {
		return false
	}
	_, panels := probe["panels"]
	_, tiles := probe["tiles"]
	return panels && !tiles
}

// LooksLikeHyperDXDashboard reports whether raw is a HyperDX v2 dashboard.
func LooksLikeHyperDXDashboard(raw []byte) bool {
	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) != nil {
		return false
	}
	_, tiles := probe["tiles"]
	return tiles
}

type validator struct {
	ix       *jsonIndex
	problems []Problem
}

func (v *validator) add(path, format string, args ...any) {
	line, col := v.ix.at(path)
	v.problems = append(v.problems, Problem{Line: line, Col: col, Path: path, Message: fmt.Sprintf(format, args...)})
}

// addKey reports a problem at an object key (an unknown field) rather than at
// its value.
func (v *validator) addKey(objPath, key, format string, args ...any) {
	line, col := v.ix.at(childPath(objPath, key) + keySuffix)
	v.problems = append(v.problems, Problem{Line: line, Col: col, Path: childPath(objPath, key), Message: fmt.Sprintf(format, args...)})
}

func childPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

func indexPath(parent string, i int) string { return fmt.Sprintf("%s[%d]", parent, i) }

func (v *validator) known(path string, obj map[string]any, keys ...string) {
	allowed := map[string]bool{}
	for _, k := range keys {
		allowed[k] = true
	}
	for k := range obj {
		if !allowed[k] {
			v.addKey(path, k, "unknown field %q (valid here: %s)", k, strings.Join(keys, ", "))
		}
	}
}

func (v *validator) str(path string, obj map[string]any, key string, required bool) string {
	raw, present := obj[key]
	p := childPath(path, key)
	if !present {
		if required {
			v.add(path, "missing required field %q", key)
		}
		return ""
	}
	s, ok := raw.(string)
	if !ok {
		v.add(p, "must be a string")
		return ""
	}
	if required && strings.TrimSpace(s) == "" {
		v.add(p, "must not be empty")
	}
	if len(s) > maxTextLen {
		v.add(p, "is longer than %d characters", maxTextLen)
	}
	return s
}

func (v *validator) list(path string, obj map[string]any, key string) ([]any, bool) {
	raw, present := obj[key]
	if !present {
		return nil, false
	}
	l, ok := raw.([]any)
	if !ok {
		v.add(childPath(path, key), "must be an array")
		return nil, false
	}
	return l, true
}

func (v *validator) object(path string, raw any) (map[string]any, bool) {
	o, ok := raw.(map[string]any)
	if !ok {
		v.add(path, "must be an object")
	}
	return o, ok
}

func (v *validator) number(path string, obj map[string]any, key string) (float64, bool) {
	raw, present := obj[key]
	if !present {
		return 0, false
	}
	n, ok := raw.(float64)
	if !ok {
		v.add(childPath(path, key), "must be a number")
		return 0, false
	}
	return n, true
}

func (v *validator) oneOf(path string, obj map[string]any, key string, required bool, allowed ...string) string {
	s := v.str(path, obj, key, required)
	if s == "" {
		return ""
	}
	for _, a := range allowed {
		if s == a {
			return s
		}
	}
	v.add(childPath(path, key), "%q is not one of: %s", s, strings.Join(allowed, ", "))
	return ""
}

func (v *validator) dashboard(d map[string]any) {
	v.known("", d, "version", "id", "title", "description", "scope", "group", "tags", "replaces", "filters", "annotations", "panels", "sections", "defaultRange")
	if ver := v.str("", d, "version", false); ver != "" && ver != SpecVersion {
		v.add("version", "%q is not the spec version this forge reads (%s)", ver, SpecVersion)
	}
	if id := v.str("", d, "id", true); id != "" && !idPattern.MatchString(id) {
		v.add("id", "%q is not a valid id (letters, digits, '-', '_' and '.', starting with a letter or digit)", id)
	}
	v.str("", d, "title", true)
	v.str("", d, "description", false)
	if r := v.str("", d, "defaultRange", false); r != "" {
		if _, err := ParseSpan(r); err != nil {
			v.add("defaultRange", "%v", err)
		}
	}
	v.oneOf("", d, "scope", false, string(ScopeProject), string(ScopeOrg), string(ScopePlatform))
	groups := make([]string, len(Groups))
	for i, g := range Groups {
		groups[i] = string(g)
	}
	v.oneOf("", d, "group", false, groups...)
	v.tags(d)
	v.replaces(d)

	if fs, ok := v.list("", d, "filters"); ok {
		v.filters(fs)
	}

	if as, ok := v.list("", d, "annotations"); ok {
		seen := map[string]bool{}
		for i, raw := range as {
			p := indexPath("annotations", i)
			a, ok := v.object(p, raw)
			if !ok {
				continue
			}
			v.known(p, a, "source", "label")
			if src := v.oneOf(p, a, "source", true, string(AnnotationReleases)); src != "" {
				if seen[src] {
					v.add(childPath(p, "source"), "annotation source %q is declared twice", src)
				}
				seen[src] = true
			}
			v.str(p, a, "label", false)
		}
	}

	ps, ok := v.list("", d, "panels")
	if !ok {
		if _, present := d["panels"]; !present {
			v.add("", "missing required field %q", "panels")
		}
		return
	}
	if len(ps) == 0 {
		v.add("panels", "a dashboard needs at least one panel")
	}
	if len(ps) > maxPanelsPerDashboard {
		v.add("panels", "has %d panels, over the %d a dashboard may hold", len(ps), maxPanelsPerDashboard)
	}
	ids := map[string]bool{}
	for i, raw := range ps {
		p := indexPath("panels", i)
		if o, ok := v.object(p, raw); ok {
			v.panel(p, o, ids)
		}
	}
	v.sectionTargets(d, ps, ids)
}

func (v *validator) filters(fs []any) {
	ids := map[string]bool{}
	for i, raw := range fs {
		p := indexPath("filters", i)
		f, ok := v.object(p, raw)
		if !ok {
			continue
		}
		v.known(p, f, "id", "label", "field", "signal", "panelIds")
		if id := v.str(p, f, "id", true); id != "" {
			if ids[id] {
				v.add(childPath(p, "id"), "duplicate filter id %q", id)
			}
			ids[id] = true
		}
		v.str(p, f, "label", true)
		sig := v.oneOf(p, f, "signal", false, string(Traces), string(Metrics), string(Logs))
		if sig == "" {
			sig = string(Traces)
		}
		v.fieldOn(p, f, "field", Signal(sig))
	}
}

// fieldOn checks that the field named by obj[key] exists on sig.
func (v *validator) fieldOn(path string, obj map[string]any, key string, sig Signal) {
	field := v.str(path, obj, key, true)
	if field == "" {
		return
	}
	if err := CheckField(sig, field); err != nil {
		v.add(childPath(path, key), "%v", err)
	}
}

func (v *validator) stringList(path string, obj map[string]any, key string, sig Signal) {
	l, ok := v.list(path, obj, key)
	if !ok {
		return
	}
	for i, raw := range l {
		p := indexPath(childPath(path, key), i)
		s, ok := raw.(string)
		if !ok {
			v.add(p, "must be a string")
			continue
		}
		if sig == "" {
			continue
		}
		if err := CheckField(sig, s); err != nil {
			v.add(p, "%v", err)
		}
	}
}

func (v *validator) panel(path string, o map[string]any, ids map[string]bool) {
	v.known(path, o, "id", "title", "description", "kind", "signal", "series", "ratio", "formula", "limit", "where", "groupBy", "columns", "unit", "width", "height", "funnel", "hasError", "sql", "postgres", "section", "minStep", "markdown", "display")
	if id := v.str(path, o, "id", true); id != "" {
		if !idPattern.MatchString(id) {
			v.add(childPath(path, "id"), "%q is not a valid id (letters, digits, '-', '_' and '.', starting with a letter or digit)", id)
		}
		if ids[id] {
			v.add(childPath(path, "id"), "duplicate panel id %q", id)
		}
		ids[id] = true
	}
	v.str(path, o, "title", true)
	v.str(path, o, "description", false)
	kind := v.oneOf(path, o, "kind", true, string(KindNumber), string(KindLine), string(KindTable), string(KindSearch), string(KindBar), string(KindHeatmap), string(KindSQL), string(KindIssues), string(KindSessions), string(KindFunnel), string(KindPostgres), string(KindText))
	if kind == string(KindText) {
		v.textPanel(path, o)
		return
	}
	if _, has := o["markdown"]; has {
		v.add(childPath(path, "markdown"), "only a text panel takes \"markdown\"")
	}
	sigName := v.oneOf(path, o, "signal", kind != string(KindSQL) && kind != string(KindPostgres), string(Traces), string(Metrics), string(Logs))
	sig := Signal(sigName)
	if raw, has := o["display"]; has && kind != "" {
		v.display(path, o, PanelKind(kind), raw)
	}
	if _, has := o["minStep"]; has && kind != string(KindSQL) {
		v.add(childPath(path, "minStep"), "only a sql panel takes \"minStep\"")
	}
	v.oneOf(path, o, "unit", false, string(UnitCount), string(UnitMillis), string(UnitBytes), string(UnitPercent), string(UnitSeconds))
	if w, ok := v.number(path, o, "width"); ok && (w < 0 || w > GridColumns || w != float64(int(w))) {
		v.add(childPath(path, "width"), "must be a whole number of grid columns from 1 to %d", GridColumns)
	}
	if h, ok := v.number(path, o, "height"); ok && (h < 0 || h > maxGridHeight || h != float64(int(h))) {
		v.add(childPath(path, "height"), "must be a whole number of grid rows up to %d", maxGridHeight)
	}
	if r, present := o["ratio"]; present {
		if _, ok := r.(bool); !ok {
			v.add(childPath(path, "ratio"), "must be true or false")
		}
	}
	ratio, _ := o["ratio"].(bool)

	v.predicates(path, o, "where", sig)

	groupBy, _ := v.list(path, o, "groupBy")
	if len(groupBy) > maxGroupByPerPanel {
		v.add(childPath(path, "groupBy"), "groups by %d fields, over the %d allowed", len(groupBy), maxGroupByPerPanel)
	}
	v.stringList(path, o, "groupBy", sig)

	if kind == string(KindPostgres) {
		v.postgresPanel(path, o)
		return
	}
	if IsConsoleKind(PanelKind(kind)) {
		v.consolePanel(path, o, PanelKind(kind), sig)
		return
	}
	for _, only := range []string{"funnel", "hasError", "postgres"} {
		if _, has := o[only]; has {
			v.add(childPath(path, only), "only a %s panel takes %q", consoleKindFor(only), only)
		}
	}

	if kind == string(KindSQL) {
		v.sqlPanel(path, o)
		return
	}
	if _, has := o["sql"]; has {
		v.add(childPath(path, "sql"), "only a sql panel takes \"sql\"")
	}

	if kind == string(KindSearch) {
		v.refuse(path, o, KindSearch, "ratio", "formula", "limit")
		v.searchPanel(path, o, sig)
		return
	}
	if _, has := o["columns"]; has {
		v.add(childPath(path, "columns"), "only a search panel takes columns")
	}
	if l, ok := v.number(path, o, "limit"); ok {
		switch {
		case kind != string(KindTable) && kind != string(KindBar):
			v.add(childPath(path, "limit"), "only a table or bar panel takes \"limit\"")
		case l < 1 || l > MaxLimit || l != float64(int(l)):
			v.add(childPath(path, "limit"), "must be a whole number of rows from 1 to %d", MaxLimit)
		}
	}
	_, hasFormula := o["formula"]
	formula := v.str(path, o, "formula", hasFormula)
	if hasFormula {
		if kind != string(KindLine) && kind != string(KindNumber) {
			v.add(childPath(path, "formula"), "a formula applies to a line or number panel, not %q", kind)
		}
		if ratio {
			v.add(childPath(path, "formula"), "a panel plots a formula or a ratio, not both (the ratio is the formula \"A / B\")")
		}
	}
	switch PanelKind(kind) {
	case KindBar:
		if len(groupBy) == 0 {
			v.add(path, "a bar panel needs a \"groupBy\": it draws one bar group per value")
		}
	case KindHeatmap:
		v.refuse(path, o, KindHeatmap, "groupBy")
		if sig == Metrics {
			v.add(childPath(path, "signal"), "a heatmap buckets the values of individual events: its signal is %q or %q", Traces, Logs)
		}
	}

	series, ok := v.list(path, o, "series")
	if !ok {
		if _, present := o["series"]; !present && kind != "" {
			v.add(path, "a %s panel needs at least one series", kind)
		}
		return
	}
	if len(series) == 0 || len(series) > maxSeriesPerPanel {
		v.add(childPath(path, "series"), "a panel needs 1 to %d series, has %d", maxSeriesPerPanel, len(series))
	}
	if ratio {
		if len(series) != 2 {
			v.add(childPath(path, "ratio"), "a ratio panel plots series[0]/series[1] and needs exactly 2 series, has %d", len(series))
		}
		if kind != string(KindLine) {
			v.add(childPath(path, "ratio"), "a ratio applies to a line panel, not %q", kind)
		}
	}
	if formula != "" && len(series) > 0 && len(series) <= maxSeriesPerPanel {
		if _, err := ParseFormula(formula, len(series)); err != nil {
			v.add(childPath(path, "formula"), "%v", err)
		}
	}
	if kind == string(KindHeatmap) {
		v.heatmapSeries(path, series)
	}
	labels := map[string]bool{}
	for i, raw := range series {
		p := indexPath(childPath(path, "series"), i)
		s, ok := v.object(p, raw)
		if !ok {
			continue
		}
		v.series(p, s, sig, labels)
	}
}

// refuse reports each of keys that o carries, on a kind that takes none of
// them: a field a renderer would silently ignore is a mistake worth naming.
func (v *validator) refuse(path string, o map[string]any, kind PanelKind, keys ...string) {
	for _, k := range keys {
		if _, has := o[k]; has {
			v.add(childPath(path, k), "a %s panel takes no %q", kind, k)
		}
	}
}

// sqlPanel validates a KindSQL panel: its statement is all it carries.
func (v *validator) sqlPanel(path string, o map[string]any) {
	v.refuse(path, o, KindSQL, "series", "ratio", "formula", "limit", "where", "groupBy", "columns")
	raw, present := o["sql"]
	if !present {
		v.add(path, "a sql panel needs \"sql\": one read-only SELECT")
		return
	}
	s, ok := raw.(string)
	if !ok {
		v.add(childPath(path, "sql"), "must be a string")
		return
	}
	if err := CheckSQL(s); err != nil {
		v.add(childPath(path, "sql"), "%v", err)
	}
	if m := v.str(path, o, "minStep", false); m != "" {
		if _, err := ParseSpan(m); err != nil {
			v.add(childPath(path, "minStep"), "%v", err)
		}
	}
}

// textPanel validates a KindText panel: Markdown is all it carries, besides
// the common presentation fields.
func (v *validator) textPanel(path string, o map[string]any) {
	v.refuse(path, o, KindText, "signal", "series", "ratio", "formula", "limit", "where", "groupBy", "columns", "unit", "funnel", "hasError", "sql", "postgres", "minStep", "display")
	raw, present := o["markdown"]
	if !present {
		v.add(path, "a text panel needs \"markdown\"")
		return
	}
	s, ok := raw.(string)
	switch {
	case !ok:
		v.add(childPath(path, "markdown"), "must be a string")
	case strings.TrimSpace(s) == "":
		v.add(childPath(path, "markdown"), "must not be empty")
	case len(s) > MaxMarkdownBytes:
		v.add(childPath(path, "markdown"), "is %d bytes, over the %d a text panel may hold", len(s), MaxMarkdownBytes)
	}
}

// display validates Panel.Display against the panel's kind: the style is one
// the kind takes, and each option is one the style uses. An option a renderer
// would silently ignore is refused, as everywhere else in the spec.
func (v *validator) display(path string, o map[string]any, kind PanelKind, raw any) {
	dp := childPath(path, "display")
	styles := displayStyles[kind]
	if len(styles) == 0 {
		v.add(dp, "a %s panel is drawn one way and takes no \"display\" (it applies to line, bar, number and sql panels)", kind)
		return
	}
	d, ok := v.object(dp, raw)
	if !ok {
		return
	}
	v.known(dp, d, "style", "stack", "horizontal", "min", "max", "topN")
	names := make([]string, len(styles))
	for i, s := range styles {
		names[i] = string(s)
	}
	style := DisplayStyle(v.oneOf(dp, d, "style", false, names...))
	if _, has := d["style"]; !has {
		style = styles[0]
	}
	flag := func(key string) bool {
		raw, has := d[key]
		if !has {
			return false
		}
		b, ok := raw.(bool)
		if !ok {
			v.add(childPath(dp, key), "must be true or false")
		}
		return ok && b
	}
	if _, has := d["stack"]; has {
		flag("stack")
		switch {
		case style == "":
		case kind == KindBar && style != StyleBar:
			v.add(childPath(dp, "stack"), "a %s has no bars to stack; stack applies to style %q", style, StyleBar)
		case (kind == KindLine || kind == KindSQL) && style != StyleArea && style != StyleBars:
			v.add(childPath(dp, "stack"), "stacked lines hide each series' own value; stack a style %q or %q panel", StyleArea, StyleBars)
		case kind == KindNumber:
			v.add(childPath(dp, "stack"), "a number panel has nothing to stack")
		}
	}
	if _, has := d["horizontal"]; has {
		flag("horizontal")
		if style != "" && (kind != KindBar || style != StyleBar) {
			v.add(childPath(dp, "horizontal"), "only a bar panel drawn as bars takes \"horizontal\"")
		}
	}
	lo, hasMin := v.number(dp, d, "min")
	hi, hasMax := v.number(dp, d, "max")
	if style == StyleGauge {
		unit, _ := o["unit"].(string)
		switch {
		case !hasMax && unit != string(UnitPercent):
			v.add(dp, "a gauge needs \"max\", the top of its scale (a percent panel's scale is 0 to 1 without one)")
		case hasMax && hasMin && lo >= hi:
			v.add(childPath(dp, "min"), "a gauge's min must be below its max, got min %v and max %v", lo, hi)
		case hasMax && !hasMin && hi <= 0:
			v.add(childPath(dp, "max"), "a gauge's max must be above its min (0 when unset), got %v", hi)
		}
	} else if style != "" {
		if hasMin {
			v.add(childPath(dp, "min"), "only a gauge takes \"min\"")
		}
		if hasMax {
			v.add(childPath(dp, "max"), "only a gauge takes \"max\"")
		}
	}
	if n, has := v.number(dp, d, "topN"); has {
		switch {
		case n < 1 || n > MaxTopN || n != float64(int(n)):
			v.add(childPath(dp, "topN"), "must be a whole number of series from 1 to %d", MaxTopN)
		case style == "":
		case style == StyleFunnel || kind == KindNumber || (kind == KindBar && style == StyleBar):
			v.add(childPath(dp, "topN"), "topN folds a line panel's series, or a pie's slices, into \"Other\"; a %s %s has none to fold (a bar panel keeps its top groups with \"limit\")", style, kind)
		}
	}
	if style == StylePie || style == StyleDonut {
		if series, ok := o["series"].([]any); ok && len(series) != 1 {
			v.add(childPath(path, "series"), "a %s draws one value per group: it takes exactly 1 series, has %d", style, len(series))
		}
	}
}

// nonNumericFields are the semantic fields that never hold a number, so a
// heatmap cannot bucket them. An attribute's type is unknown until query time.
var nonNumericFields = map[string]bool{
	FieldService: true, FieldEnv: true, "timestamp": true,
	"span.name": true, "span.kind": true, "span.status": true, "span.status_message": true,
	"log.severity": true, "log.body": true,
}

// heatmapSeries checks a heatmap's one series: a count of events, bucketed by
// the numeric value in its field.
func (v *validator) heatmapSeries(path string, series []any) {
	if len(series) != 1 {
		v.add(childPath(path, "series"), "a heatmap panel takes exactly 1 series, has %d", len(series))
		return
	}
	p := indexPath(childPath(path, "series"), 0)
	s, ok := series[0].(map[string]any)
	if !ok {
		return
	}
	if agg, _ := s["agg"].(string); agg != "" && agg != string(AggCount) {
		v.add(childPath(p, "agg"), "a heatmap counts events: its series' agg is %q, not %q", AggCount, agg)
	}
	field, _ := s["field"].(string)
	switch {
	case field == "":
		v.add(p, "a heatmap series names the numeric value it buckets in \"field\" (span.duration, say)")
	case nonNumericFields[field]:
		v.add(childPath(p, "field"), "%q is not numeric; a heatmap buckets a numeric field such as span.duration", field)
	}
}

func consoleKindFor(field string) PanelKind {
	switch field {
	case "funnel":
		return KindFunnel
	case "postgres":
		return KindPostgres
	}
	return KindSessions
}

// consolePanel validates the kinds the console draws from its own services
// (issues, sessions, funnel). They carry no query: a series, a ratio, columns
// or a group-by would be silently ignored, so each is refused.
func (v *validator) consolePanel(path string, o map[string]any, kind PanelKind, sig Signal) {
	for _, no := range []string{"series", "ratio", "formula", "limit", "columns", "groupBy", "unit", "sql", "postgres"} {
		if _, has := o[no]; has {
			v.add(childPath(path, no), "a %s panel is drawn by the console and takes no %q", kind, no)
		}
	}
	if sig != "" && sig != Traces {
		v.add(childPath(path, "signal"), "a %s panel reads browser spans: its signal is %q", kind, Traces)
	}
	if _, has := o["hasError"]; has {
		if kind != KindSessions {
			v.add(childPath(path, "hasError"), "only a sessions panel takes \"hasError\"")
		} else if _, ok := o["hasError"].(bool); !ok {
			v.add(childPath(path, "hasError"), "must be true or false")
		}
	}
	fn, has := o["funnel"]
	if kind != KindFunnel {
		if has {
			v.add(childPath(path, "funnel"), "only a funnel panel takes \"funnel\"")
		}
		return
	}
	if !has {
		v.add(path, "a funnel panel needs a \"funnel\"")
		return
	}
	f, ok := v.object(childPath(path, "funnel"), fn)
	if !ok {
		return
	}
	v.funnel(childPath(path, "funnel"), f)
}

// postgresPanel validates a KindPostgres panel: its postgres block is all it
// carries, besides the common presentation fields and a unit.
func (v *validator) postgresPanel(path string, o map[string]any) {
	v.refuse(path, o, KindPostgres, "signal", "series", "sql", "ratio", "formula", "limit", "where", "groupBy", "columns", "funnel", "hasError")
	raw, present := o["postgres"]
	if !present {
		v.add(path, "a postgres panel needs a \"postgres\" block: {\"database\", \"sql\"}")
		return
	}
	pp := childPath(path, "postgres")
	q, ok := v.object(pp, raw)
	if !ok {
		return
	}
	v.known(pp, q, "database", "sql", "draw", "time", "values", "group")

	if db := v.str(pp, q, "database", true); db != "" {
		if len(db) > MaxPostgresDatabaseLen || !postgresDatabasePattern.MatchString(db) {
			v.add(childPath(pp, "database"), "%q is not a database name: lowercase letters, digits, '-' and '_', starting and ending with a letter or digit, at most %d characters", db, MaxPostgresDatabaseLen)
		}
	}

	if sraw, has := q["sql"]; !has {
		v.add(pp, "a postgres panel needs \"sql\": one read-only statement")
	} else if s, ok := sraw.(string); !ok {
		v.add(childPath(pp, "sql"), "must be a string")
	} else if err := CheckPostgresSQL(s); err != nil {
		v.add(childPath(pp, "sql"), "%v", err)
	}

	draw := v.str(pp, q, "draw", false)
	if !isPostgresDraw(draw) {
		v.add(childPath(pp, "draw"), "%q is not one of: %s, %s, %s, %s, %s", draw, PostgresDrawAuto, PostgresDrawLine, PostgresDrawNumber, PostgresDrawTable, PostgresDrawBar)
	}
	timeCol := v.postgresColumn(pp, q, "time")
	if draw == PostgresDrawLine && timeCol == "" {
		v.add(pp, "a line draw needs \"time\": the column holding each row's timestamp")
	}
	values := v.postgresColumns(pp, q, "values", maxPostgresValues)
	if draw == PostgresDrawNumber && len(values) > 1 {
		v.add(childPath(pp, "values"), "a number draw shows one value, has %d", len(values))
	}
	v.postgresColumns(pp, q, "group", maxPostgresGroup)
}

// postgresColumn validates an optional single column name.
func (v *validator) postgresColumn(path string, obj map[string]any, key string) string {
	s := v.str(path, obj, key, false)
	if _, has := obj[key]; has {
		v.columnName(childPath(path, key), s)
	}
	return s
}

// postgresColumns validates an optional list of column names.
func (v *validator) postgresColumns(path string, obj map[string]any, key string, max int) []string {
	l, ok := v.list(path, obj, key)
	if !ok {
		return nil
	}
	if len(l) > max {
		v.add(childPath(path, key), "has %d columns, over the %d allowed", len(l), max)
	}
	var out []string
	for i, raw := range l {
		p := indexPath(childPath(path, key), i)
		s, ok := raw.(string)
		if !ok {
			v.add(p, "must be a string")
			continue
		}
		v.columnName(p, s)
		out = append(out, s)
	}
	return out
}

func (v *validator) columnName(path, name string) {
	if len(name) < 1 || len(name) > MaxPostgresColumnLen {
		v.add(path, "a column name is 1 to %d bytes, has %d", MaxPostgresColumnLen, len(name))
	}
}

var funnelWindowPattern = regexp.MustCompile(`^[1-9][0-9]{0,3}[smhd]$`)

func (v *validator) funnel(path string, f map[string]any) {
	v.known(path, f, "steps", "window", "by")
	steps, ok := v.list(path, f, "steps")
	if !ok {
		if _, present := f["steps"]; !present {
			v.add(path, "a funnel needs \"steps\"")
		}
	} else {
		if len(steps) < minFunnelSteps || len(steps) > maxFunnelSteps {
			v.add(childPath(path, "steps"), "a funnel has %d to %d steps, has %d", minFunnelSteps, maxFunnelSteps, len(steps))
		}
		seen := map[string]bool{}
		for i, raw := range steps {
			p := indexPath(childPath(path, "steps"), i)
			name, ok := raw.(string)
			switch {
			case !ok:
				v.add(p, "must be a string")
			case strings.TrimSpace(name) == "":
				v.add(p, "must not be empty")
			case len(name) > 128:
				v.add(p, "is longer than 128 characters")
			case seen[name]:
				v.add(p, "step %q repeats an earlier step", name)
			default:
				seen[name] = true
			}
		}
	}
	if w := v.str(path, f, "window", false); w != "" && !funnelWindowPattern.MatchString(w) {
		v.add(childPath(path, "window"), "%q is not a window: a number and a unit of s, m, h or d, like \"1h\" or \"7d\"", w)
	}
	v.oneOf(path, f, "by", false, string(FunnelBySession), string(FunnelByUser))
}

func (v *validator) searchPanel(path string, o map[string]any, sig Signal) {
	if _, has := o["series"]; has {
		v.add(childPath(path, "series"), "a search panel shows rows and takes no series; use columns")
	}
	if _, present := o["columns"]; !present {
		v.add(path, "a search panel needs columns")
		return
	}
	v.stringList(path, o, "columns", sig)
}

// hasObservations reports whether a metric kind records observations (a count
// and a distribution) rather than a single value.
func hasObservations(metricType string) bool {
	return metricType == "histogram" || metricType == "exp_histogram" || metricType == "summary"
}

func (v *validator) series(path string, s map[string]any, sig Signal, labels map[string]bool) {
	v.known(path, s, "label", "agg", "quantile", "field", "metricType", "where", "thresholds")
	if l := v.str(path, s, "label", true); l != "" {
		if labels[l] {
			v.add(childPath(path, "label"), "duplicate series label %q in this panel", l)
		}
		labels[l] = true
	}
	agg := v.oneOf(path, s, "agg", true, string(AggCount), string(AggCountDistinct), string(AggSum), string(AggAvg), string(AggMax), string(AggQuantile), string(AggLast), string(AggRate), string(AggIncrease), string(AggMin))
	field := v.str(path, s, "field", false)
	metricType := v.oneOf(path, s, "metricType", false, "sum", "gauge", "histogram", "exp_histogram", "summary")
	q, hasQ := v.number(path, s, "quantile")
	if agg == string(AggQuantile) {
		if !hasQ || q <= 0 || q > 1 {
			v.add(childPath(path, "quantile"), "a quantile series needs a level in (0, 1], e.g. 0.95")
		}
	} else if hasQ {
		v.add(childPath(path, "quantile"), "only a quantile series takes a quantile")
	}
	v.predicates(path, s, "where", sig)
	v.thresholds(path, s)

	switch sig {
	case Metrics:
		if field == "" {
			v.add(path, "a metrics series names its metric in \"field\"")
		}
		if metricType == "" {
			v.add(path, "a metrics series needs \"metricType\" (sum, gauge, histogram, exp_histogram or summary)")
		}
		if agg == string(AggQuantile) && !hasObservations(metricType) {
			v.add(childPath(path, "agg"), "a quantile over a metric needs metricType \"histogram\", \"exp_histogram\" or \"summary\"")
		}
		if agg == string(AggCountDistinct) {
			v.add(childPath(path, "agg"), "%s is not defined over a metric; use rate, increase, sum, avg, min, max, last, quantile or (histograms) count", agg)
		}
		if agg == string(AggCount) && !hasObservations(metricType) {
			v.add(childPath(path, "agg"), "count over a metric needs a histogram, exp_histogram or summary metricType (its observations); a sum's growth is rate or increase (agg sum on a monotonic sum is its increase)")
		}
		if (agg == string(AggRate) || agg == string(AggIncrease)) && metricType == "gauge" {
			v.add(childPath(path, "agg"), "%s needs metricType \"sum\" (a monotonic counter) or a histogram or summary, not a gauge", agg)
		}
	case Traces, Logs:
		if metricType != "" {
			v.add(childPath(path, "metricType"), "only a metrics series takes metricType")
		}
		if agg == string(AggRate) || agg == string(AggIncrease) || agg == string(AggMin) {
			v.add(childPath(path, "agg"), "%s is defined for metrics; use count, sum, avg, max or quantile over a %s field", agg, sig)
		}
		if agg != "" && agg != string(AggCount) && field == "" {
			v.add(path, "aggregate %q needs a \"field\"", agg)
		}
		if field != "" {
			if err := CheckField(sig, field); err != nil {
				v.add(childPath(path, "field"), "%v", err)
			}
		}
		if agg == string(AggLast) {
			v.add(childPath(path, "agg"), "last is defined for metrics; use max or avg over a %s field", sig)
		}
	}
}

func (v *validator) thresholds(path string, s map[string]any) {
	raw, present := s["thresholds"]
	if !present {
		return
	}
	tp := childPath(path, "thresholds")
	t, ok := v.object(tp, raw)
	if !ok {
		return
	}
	v.known(tp, t, "good", "poor")
	good, hasGood := v.number(tp, t, "good")
	poor, hasPoor := v.number(tp, t, "poor")
	if !hasGood || !hasPoor {
		v.add(tp, "thresholds need both \"good\" and \"poor\"")
		return
	}
	if good < 0 || poor <= good {
		v.add(tp, "need 0 <= good < poor, got good %v and poor %v", good, poor)
	}
}

func (v *validator) predicates(parent string, obj map[string]any, key string, sig Signal) {
	path := childPath(parent, key)
	ps, ok := v.list(parent, obj, key)
	if !ok {
		return
	}
	if len(ps) > maxPredicates {
		v.add(path, "has %d predicates, over the %d allowed", len(ps), maxPredicates)
	}
	for i, raw := range ps {
		p := indexPath(path, i)
		o, ok := v.object(p, raw)
		if !ok {
			continue
		}
		v.known(p, o, "field", "op", "value", "values")
		field := v.str(p, o, "field", true)
		if field != "" && sig != "" {
			if err := CheckField(sig, field); err != nil {
				v.add(childPath(p, "field"), "%v", err)
			}
		}
		op := v.oneOf(p, o, "op", true, string(OpEq), string(OpNeq), string(OpIn), string(OpNotIn), string(OpPrefix), string(OpMatch), string(OpExists), string(OpGTE))
		value := v.str(p, o, "value", false)
		var values []string
		if vs, ok := v.list(p, o, "values"); ok {
			if len(vs) > maxPredicateValues {
				v.add(childPath(p, "values"), "has %d values, over the %d allowed", len(vs), maxPredicateValues)
			}
			for j, rv := range vs {
				s, ok := rv.(string)
				if !ok {
					v.add(indexPath(childPath(p, "values"), j), "must be a string")
					continue
				}
				values = append(values, s)
			}
		}
		switch Op(op) {
		case OpEq, OpNeq, OpPrefix:
			if _, has := o["value"]; !has {
				v.add(p, "%q needs \"value\"", op)
			}
		case OpIn, OpNotIn:
			if len(values) == 0 {
				v.add(p, "%q needs a non-empty \"values\" list", op)
			}
		case OpMatch:
			switch {
			case value == "":
				v.add(p, "\"match\" needs a \"value\" regular expression")
			case len(value) > maxRegexLen:
				v.add(childPath(p, "value"), "regular expression is longer than %d characters", maxRegexLen)
			default:
				if _, err := regexp.Compile(value); err != nil {
					v.add(childPath(p, "value"), "not an RE2 regular expression: %v", err)
				}
			}
		case OpGTE:
			if !isNumber(value) {
				v.add(childPath(p, "value"), "\"gte\" compares against a number, got %q", value)
			}
		}
	}
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	var f float64
	return json.Unmarshal([]byte(s), &f) == nil
}

// ── JSON position index ──────────────────────────────────────────────────

// keySuffix marks the entry that locates an object KEY (as opposed to its
// value), so an unknown field is reported where it was typed.
const keySuffix = "#key"

// jsonIndex maps a value's path to the byte offset it starts at.
type jsonIndex struct {
	raw []byte
	pos map[string]int
	// lineStarts[i] is the byte offset of line i+1.
	lineStarts []int
}

// indexJSON walks raw token by token, recording where every value and key
// starts. A syntax error returns the index built so far with the error.
func indexJSON(raw []byte) (*jsonIndex, error) {
	ix := &jsonIndex{raw: raw, pos: map[string]int{}, lineStarts: []int{0}}
	for i, b := range raw {
		if b == '\n' {
			ix.lineStarts = append(ix.lineStarts, i+1)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := ix.walk(dec, ""); err != nil {
		return ix, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("unexpected data after the top-level value")
		}
		return ix, err
	}
	return ix, nil
}

func (ix *jsonIndex) start(dec *json.Decoder) int {
	off := int(dec.InputOffset())
	for off < len(ix.raw) {
		switch ix.raw[off] {
		case ' ', '\t', '\r', '\n', ',', ':':
			off++
			continue
		}
		break
	}
	return off
}

func (ix *jsonIndex) walk(dec *json.Decoder, path string) error {
	at := ix.start(dec)
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	ix.pos[path] = at
	delim, isDelim := tok.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		for dec.More() {
			keyAt := ix.start(dec)
			kt, err := dec.Token()
			if err != nil {
				return err
			}
			key, _ := kt.(string)
			ix.pos[childPath(path, key)+keySuffix] = keyAt
			if err := ix.walk(dec, childPath(path, key)); err != nil {
				return err
			}
		}
	case '[':
		for i := 0; dec.More(); i++ {
			if err := ix.walk(dec, indexPath(path, i)); err != nil {
				return err
			}
		}
	}
	_, err = dec.Token() // the closing delimiter
	return err
}

// at returns the 1-based line and column of path, or (0, 0) when the path was
// not indexed. A path with no entry falls back to its nearest indexed parent.
func (ix *jsonIndex) at(path string) (line, col int) {
	for {
		if off, ok := ix.pos[path]; ok {
			return ix.lineCol(off)
		}
		if path == "" {
			return 0, 0
		}
		path = strings.TrimSuffix(path, keySuffix)
		cut := strings.LastIndexAny(path, ".[")
		if cut < 0 {
			path = ""
		} else {
			path = path[:cut]
		}
	}
}

func (ix *jsonIndex) lineCol(off int) (int, int) {
	lo, hi := 0, len(ix.lineStarts)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if ix.lineStarts[mid] <= off {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo + 1, off - ix.lineStarts[lo] + 1
}

// syntaxProblem locates a JSON syntax or decode error.
func (ix *jsonIndex) syntaxProblem(err error) Problem {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	off := -1
	switch {
	case errors.As(err, &syn):
		off = int(syn.Offset)
	case errors.As(err, &typ):
		off = int(typ.Offset)
	}
	if off < 0 && errors.Is(err, io.EOF) {
		off = len(ix.raw)
	}
	if off < 0 {
		return Problem{Message: "invalid JSON: " + err.Error()}
	}
	if off > len(ix.raw) {
		off = len(ix.raw)
	}
	line, col := ix.lineCol(off)
	return Problem{Line: line, Col: col, Message: "invalid JSON: " + err.Error()}
}

// tags checks Dashboard.Tags: at most MaxTags unique lowercase labels.
func (v *validator) tags(d map[string]any) {
	ts, ok := v.list("", d, "tags")
	if !ok {
		return
	}
	if len(ts) > MaxTags {
		v.add("tags", "has %d tags, over the %d allowed", len(ts), MaxTags)
	}
	seen := map[string]bool{}
	for i, raw := range ts {
		p := indexPath("tags", i)
		t, ok := raw.(string)
		switch {
		case !ok:
			v.add(p, "must be a string")
		case !tagPattern.MatchString(t):
			v.add(p, "%q is not a valid tag (lowercase letters, digits, '.', '_' and '-', 1 to 32 characters, starting with a letter or digit)", t)
		case seen[t]:
			v.add(p, "tag %q repeats an earlier tag", t)
		default:
			seen[t] = true
		}
	}
}

// replaces checks Dashboard.Replaces: unique dashboard ids other than its own.
func (v *validator) replaces(d map[string]any) {
	rs, ok := v.list("", d, "replaces")
	if !ok {
		return
	}
	if len(rs) > MaxReplaces {
		v.add("replaces", "has %d ids, over the %d allowed", len(rs), MaxReplaces)
	}
	self, _ := d["id"].(string)
	seen := map[string]bool{}
	for i, raw := range rs {
		p := indexPath("replaces", i)
		id, ok := raw.(string)
		switch {
		case !ok:
			v.add(p, "must be a string")
		case !idPattern.MatchString(id):
			v.add(p, "%q is not a valid dashboard id", id)
		case id == self:
			v.add(p, "a dashboard cannot replace itself")
		case seen[id]:
			v.add(p, "id %q repeats an earlier id", id)
		default:
			seen[id] = true
		}
	}
}
