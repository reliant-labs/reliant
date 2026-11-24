package model

import (
	"fmt"
	"regexp"
	"sort"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// The observe block's metric kinds.
const (
	ObserveKindCounter   = "counter"
	ObserveKindHistogram = "histogram"
	ObserveKindGauge     = "gauge"
)

// The observe block's limits (dev-docs/design/workflow-observe.md, section
// 8.1). The per-block counts are shape rules: a block over one is wrong YAML.
// The merged limits apply to a node's block after the workflow's defaults are
// merged into it.
const (
	ObserveMaxAttributes       = 32 // attributes per block
	ObserveMaxEvents           = 16 // events per block (before each:)
	ObserveMaxEventAttributes  = 16 // attributes per event
	ObserveMaxMetrics          = 8  // metrics per block
	ObserveMaxMetricAttributes = 4  // author attributes per metric
	// ObserveMaxMetricsPerWorkflow bounds the distinct metric names one graph
	// declares, defaults and nodes together.
	ObserveMaxMetricsPerWorkflow = 32
	// ObserveMaxRequestBytes bounds a node's merged block as JSON: it rides in
	// the node's activity request, and so in Temporal history.
	ObserveMaxRequestBytes = 4096
)

// observeNamePattern is the shape of every attribute key, event name and
// metric name an author writes: lower-case, dotted, one token.
var observeNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,63}$`)

// observeUnitPattern is a UCUM unit ("ms", "By", "{call}", "1"): printable,
// no spaces.
var observeUnitPattern = regexp.MustCompile(`^[!-~]{1,63}$`)

// ObserveProblem is one shape error in an observe block. Path is relative to
// the block ("events", "[0]", "name").
type ObserveProblem struct {
	Path    []string
	Message string
}

// ObserveProblems returns every shape error in o: names and keys outside
// observeNamePattern, a metric kind outside counter|histogram|gauge, a
// histogram or gauge with no value, and counts over the per-block limits.
// Expressions are not checked here: whether they compile is validation's
// question, and a bad one never stops a run.
func ObserveProblems(o *reliantv1.NodeObserve) []ObserveProblem {
	if o == nil {
		return nil
	}
	var out []ObserveProblem
	add := func(msg string, path ...string) {
		out = append(out, ObserveProblem{Path: path, Message: msg})
	}
	checkKeys := func(attrs map[string]string, limit int, path ...string) {
		if len(attrs) > limit {
			add(fmt.Sprintf("%d attributes; at most %d", len(attrs), limit), path...)
		}
		for _, k := range sortedObserveKeys(attrs) {
			if !observeNamePattern.MatchString(k) {
				add(fmt.Sprintf("attribute key %q must match %s", k, observeNamePattern), append(append([]string{}, path...), k)...)
			}
		}
	}

	checkKeys(o.GetAttributes(), ObserveMaxAttributes, "attributes")

	if n := len(o.GetEvents()); n > ObserveMaxEvents {
		add(fmt.Sprintf("%d events; at most %d", n, ObserveMaxEvents), "events")
	}
	for i, e := range o.GetEvents() {
		at := []string{"events", fmt.Sprintf("[%d]", i)}
		if !observeNamePattern.MatchString(e.GetName()) {
			add(fmt.Sprintf("event name %q must match %s", e.GetName(), observeNamePattern), append(at, "name")...)
		}
		checkKeys(e.GetAttributes(), ObserveMaxEventAttributes, append(at, "attributes")...)
	}

	if n := len(o.GetMetrics()); n > ObserveMaxMetrics {
		add(fmt.Sprintf("%d metrics; at most %d", n, ObserveMaxMetrics), "metrics")
	}
	seen := map[string]bool{}
	for i, m := range o.GetMetrics() {
		at := []string{"metrics", fmt.Sprintf("[%d]", i)}
		if !observeNamePattern.MatchString(m.GetName()) {
			add(fmt.Sprintf("metric name %q must match %s", m.GetName(), observeNamePattern), append(at, "name")...)
		} else if seen[m.GetName()] {
			add(fmt.Sprintf("metric %q is declared twice", m.GetName()), append(at, "name")...)
		}
		seen[m.GetName()] = true
		switch m.GetKind() {
		case ObserveKindCounter:
		case ObserveKindHistogram, ObserveKindGauge:
			if m.GetValue() == "" {
				add(fmt.Sprintf("a %s needs a value", m.GetKind()), append(at, "value")...)
			}
		default:
			add(fmt.Sprintf("kind %q must be counter, histogram or gauge", m.GetKind()), append(at, "kind")...)
		}
		if u := m.GetUnit(); u != "" && !observeUnitPattern.MatchString(u) {
			add(fmt.Sprintf("unit %q must be a UCUM unit with no spaces, at most 63 characters", u), append(at, "unit")...)
		}
		checkKeys(m.GetAttributes(), ObserveMaxMetricAttributes, append(at, "attributes")...)
	}
	return out
}

// MergeObserve returns a node's effective observe block: the workflow's
// defaults merged with the node's own. Attributes merge by key and metrics by
// name, the node winning; events concatenate, defaults first. Neither input is
// modified, and the result is nil when both are.
func MergeObserve(defaults, node *reliantv1.NodeObserve) *reliantv1.NodeObserve {
	if defaults == nil && node == nil {
		return nil
	}
	out := &reliantv1.NodeObserve{}
	if n := len(defaults.GetAttributes()) + len(node.GetAttributes()); n > 0 {
		out.Attributes = make(map[string]string, n)
		for k, v := range defaults.GetAttributes() {
			out.Attributes[k] = v
		}
		for k, v := range node.GetAttributes() {
			out.Attributes[k] = v
		}
	}
	out.Events = append(append(out.Events, defaults.GetEvents()...), node.GetEvents()...)
	own := make(map[string]bool, len(node.GetMetrics()))
	for _, m := range node.GetMetrics() {
		own[m.GetName()] = true
	}
	for _, m := range defaults.GetMetrics() {
		if !own[m.GetName()] {
			out.Metrics = append(out.Metrics, m)
		}
	}
	out.Metrics = append(out.Metrics, node.GetMetrics()...)
	return out
}

func sortedObserveKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ObservedNode is the `node` an observe expression reads: the node that
// finished. Read-only, and the same for every node kind.
type ObservedNode struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Path       string `json:"path"`
	Attempt    int64  `json:"attempt"`
	DurationMs int64  `json:"duration_ms"`
}
