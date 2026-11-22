package dashspec

import (
	"sort"
	"strings"
)

// figureDisplay validates the Display options that say how figures and
// rankings read (Good, Bar, Share, Units, Layout) against the panel's kind and
// style. Each is presentation only; an option a renderer would silently
// ignore is refused, as everywhere else in the spec.
func (v *validator) figureDisplay(dp string, o, d map[string]any, kind PanelKind, style DisplayStyle) {
	if g := v.oneOf(dp, d, "good", false, string(GoodUp), string(GoodDown)); g != "" {
		switch {
		case kind != KindNumber && kind != KindSQL:
			v.add(childPath(dp, "good"), "only a number or sql panel draws figures whose change is good or bad")
		case style == StyleFunnel || style == StyleHeatmap || style == StyleBarList:
			v.add(childPath(dp, "good"), "a %s draws no figure whose change is good or bad", style)
		}
	}
	barlistOnly := func(key string) {
		if style != StyleBarList {
			v.add(childPath(dp, key), "only a panel drawn as %q takes %q", StyleBarList, key)
		}
	}
	if _, has := d["bar"]; has {
		barlistOnly("bar")
		bar := v.str(dp, d, "bar", true)
		if kind == KindBar && bar != "" && !hasSeriesLabel(o, bar) {
			v.add(childPath(dp, "bar"), "%q is not one of the panel's series labels", bar)
		}
	}
	if raw, has := d["share"]; has {
		barlistOnly("share")
		if _, ok := raw.(bool); !ok {
			v.add(childPath(dp, "share"), "must be true or false")
		}
	}
	if raw, has := d["units"]; has {
		v.columnUnits(childPath(dp, "units"), raw, style)
	}
	if l := v.oneOf(dp, d, "layout", false, string(LayoutSteps)); l != "" && style != StyleFunnel {
		v.add(childPath(dp, "layout"), "only a funnel lays out steps: a funnel panel, or a sql panel drawn as %q", StyleFunnel)
	}
}

// columnUnits validates Display.Units: a unit per value column, on a style
// that draws several columns apart.
func (v *validator) columnUnits(up string, raw any, style DisplayStyle) {
	if style != StyleBarList && style != StyleKPI {
		v.add(up, "only a panel drawn as %q or %q gives its columns their own units", StyleBarList, StyleKPI)
		return
	}
	m, ok := raw.(map[string]any)
	if !ok {
		v.add(up, "must be an object of column name to unit")
		return
	}
	if len(m) > MaxColors {
		v.add(up, "names %d columns, over the %d allowed", len(m), MaxColors)
	}
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		switch {
		case strings.TrimSpace(name) == "":
			v.add(up, "a column name must not be empty")
		case len(name) > maxColorKeyBytes:
			v.add(up, "column name %.40q… is longer than %d bytes", name, maxColorKeyBytes)
		}
		v.oneOf(up, m, name, true, string(UnitCount), string(UnitMillis), string(UnitBytes), string(UnitPercent), string(UnitSeconds))
	}
}

// funnelDisplay validates a funnel panel's Display: the funnel kind is drawn
// one way, so its layout is all it takes.
func (v *validator) funnelDisplay(dp string, raw any) {
	d, ok := v.object(dp, raw)
	if !ok {
		return
	}
	for key := range d {
		if key != "layout" {
			v.add(childPath(dp, key), "a funnel panel's display takes only \"layout\"")
		}
	}
	v.oneOf(dp, d, "layout", false, string(LayoutSteps))
}

// hasSeriesLabel reports whether one of o's series is labelled label.
func hasSeriesLabel(o map[string]any, label string) bool {
	series, _ := o["series"].([]any)
	for _, raw := range series {
		if s, ok := raw.(map[string]any); ok && s["label"] == label {
			return true
		}
	}
	return false
}

// drill validates Panel.Drill: a link to another dashboard of the project,
// and optionally a panel on it.
func (v *validator) drill(path string, o map[string]any) {
	dp := childPath(path, "drill")
	d, ok := v.object(dp, o["drill"])
	if !ok {
		return
	}
	v.known(dp, d, "dashboard", "label", "panel")
	if id := v.str(dp, d, "dashboard", true); id != "" && !idPattern.MatchString(id) {
		v.add(childPath(dp, "dashboard"), "%q is not a valid dashboard id", id)
	}
	v.str(dp, d, "label", false)
	if id := v.str(dp, d, "panel", false); id != "" && !idPattern.MatchString(id) {
		v.add(childPath(dp, "panel"), "%q is not a valid panel id", id)
	}
}

// attentionPanel validates a KindAttention panel: it reads the figures it
// watches, so it carries no query, and its own figures are sql panels that
// say which way is good. ids are the dashboard's panel ids, which a figure's
// id must not repeat. Its Watch is checked against the dashboard's panels
// once they are all read (attentionTargets).
func (v *validator) attentionPanel(path string, o map[string]any, ids map[string]bool) {
	v.refuse(path, o, KindAttention, "signal", "series", "ratio", "formula", "limit", "where", "groupBy", "columns", "unit",
		"funnel", "hasError", "sql", "trendSql", "postgres", "minStep", "markdown", "display", "browse", "link")
	raw, has := o["attention"]
	if !has {
		return
	}
	ap := childPath(path, "attention")
	a, ok := v.object(ap, raw)
	if !ok {
		return
	}
	v.known(ap, a, "watch", "figures", "threshold")
	v.stringList(ap, a, "watch", "")
	if t, ok := v.number(ap, a, "threshold"); ok && (t <= 0 || t > 1) {
		v.add(childPath(ap, "threshold"), "is a relative change, a fraction above 0 and at most 1 (0.05 is 5%%), got %v", t)
	}
	figures, ok := v.list(ap, a, "figures")
	if !ok {
		return
	}
	if len(figures) > MaxAttentionFigures {
		v.add(childPath(ap, "figures"), "has %d figures, over the %d an attention panel may read", len(figures), MaxAttentionFigures)
	}
	for i, raw := range figures {
		fp := indexPath(childPath(ap, "figures"), i)
		f, ok := v.object(fp, raw)
		if !ok {
			continue
		}
		if kind, _ := f["kind"].(string); kind != string(KindSQL) {
			v.add(childPath(fp, "kind"), "an attention panel's own figure is a sql panel, not %q", kind)
			continue
		}
		// ids is the dashboard's one set of panel ids: panel records each
		// figure's id in it, so a figure repeats neither a panel's id nor
		// another figure's, of this attention panel or any other.
		v.panel(fp, f, ids)
		if !declaresGood(f) {
			v.add(fp, "an attention panel's figure says which way is good (display.good)")
		}
	}
}

// attentionTargets checks each attention panel's Watch against the
// dashboard's panels: every id names a panel that declares Display.Good, and
// a panel that watches every such panel (no Watch) has one to watch.
func (v *validator) attentionTargets(panels []any) {
	byID := map[string]map[string]any{}
	anyGood := false
	for _, raw := range panels {
		if o, ok := raw.(map[string]any); ok {
			if id, _ := o["id"].(string); id != "" {
				byID[id] = o
			}
			anyGood = anyGood || declaresGood(o)
		}
	}
	for i, raw := range panels {
		o, ok := raw.(map[string]any)
		if !ok || o["kind"] != string(KindAttention) {
			continue
		}
		path := indexPath("panels", i)
		// No attention object (or not an object, which attentionPanel
		// reports) watches every figure with Display.Good.
		var watch, figures []any
		if a, ok := o["attention"].(map[string]any); ok {
			watch, _ = a["watch"].([]any)
			figures, _ = a["figures"].([]any)
		}
		if len(watch) == 0 && len(figures) == 0 && !anyGood {
			v.add(path, "watches nothing: no panel of the dashboard declares display.good, and it names no figures of its own")
		}
		for j, w := range watch {
			id, _ := w.(string)
			if id == "" {
				continue
			}
			wp := indexPath(childPath(childPath(path, "attention"), "watch"), j)
			target, found := byID[id]
			switch {
			case !found:
				v.add(wp, "no panel %q on this dashboard", id)
			case !declaresGood(target):
				v.add(wp, "panel %q does not say which way is good (display.good), so its change cannot be judged", id)
			}
		}
	}
}

// declaresGood reports whether the panel o declares Display.Good.
func declaresGood(o map[string]any) bool {
	d, _ := o["display"].(map[string]any)
	g, _ := d["good"].(string)
	return g != ""
}
