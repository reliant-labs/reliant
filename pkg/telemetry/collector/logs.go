package collector

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/pkg/telemetry"
)

// Scratch attributes LogsProcessor uses within one record and deletes.
const (
	parsedAttribute = "forge.telemetry.parsed"
	ruleAttribute   = "forge.telemetry.rule"
)

// logs renders what the caller splices into its logs (and node-metrics)
// pipelines: IdentityProcessor, LogsProcessor, the Preserve operator and the
// k8sattributes label extraction.
//
// Why processors and not filelog operators: a filelog operator sees only
// what the log file's PATH names (namespace, pod name, container), never the
// pod's labels, so a selector cannot be matched there. k8sattributes adds
// the labels, IdentityProcessor stamps service.name from them, and
// LogsProcessor parses by service.name. On a host the file name is the
// service, so the same two processors route by it.
func (b *builder) logs(targets []telemetry.Target) {
	cluster := b.opts.Runtime == telemetry.RuntimeCluster
	var identity []any
	labelKeys := map[string]bool{}
	var parsing []telemetry.Target
	for _, t := range targets {
		var cond string
		switch {
		case cluster && len(t.Selector) > 0:
			cond = podCondition(t)
			for k := range t.Selector {
				labelKeys[k] = true
			}
		case !cluster && fileStem(t) != "":
			cond = fmt.Sprintf(`attributes["service.name"] == %s`, ottlString(fileStem(t)))
		default:
			if t.Logs != nil {
				b.warn("telemetry %s (service %s): logs not parsed: %s", t.Source, t.ServiceName, unroutable(cluster))
			}
			continue
		}
		identity = append(identity, map[string]any{
			"context":    "resource",
			"conditions": strs(lit(cond)),
			"statements": strs(lits(b.identityStatements(t))...),
		})
		if t.Logs != nil {
			parsing = append(parsing, t)
		}
	}
	if len(identity) == 0 {
		return
	}
	keys := sortedKeys(labelKeys)
	for _, k := range keys {
		b.f.K8sAttributesLabels = append(b.f.K8sAttributesLabels, map[string]any{"tag_name": LabelAttributePrefix + k, "key": k, "from": "pod"})
	}
	if len(keys) > 0 {
		// The labels were extracted for this match alone: never stored.
		identity = append(identity, map[string]any{"context": "resource", "statements": strs(
			lit(`delete_matching_keys(attributes, ` + ottlString("^"+regexp.QuoteMeta(LabelAttributePrefix)) + `)`))})
	}
	// trace_statements too: a caller whose k8sattributes also enriches
	// pushed traces (forge's node agent) runs this processor there, and
	// only it deletes the extracted selector labels.
	b.f.Processors[IdentityProcessor] = map[string]any{
		"error_mode":         "ignore",
		"trace_statements":   identity,
		"log_statements":     identity,
		"metric_statements":  identity,
		"profile_statements": identity,
	}
	b.f.Logs.Processors = []string{IdentityProcessor}
	if cluster {
		b.f.NodeMetricsProcessors = []string{IdentityProcessor}
	}
	if len(parsing) == 0 {
		return
	}

	var groups []any
	for _, t := range parsing {
		groups = append(groups, map[string]any{
			"conditions": strs(lit(logCondition(t, cluster))),
			"statements": strs(lits(logStatements(*t.Logs))...),
		})
	}
	// Every record: the scratch attributes never leave this processor.
	groups = append(groups, map[string]any{"statements": strs(
		lit(`delete_key(log.attributes, `+ottlString(OriginalAttribute)+`)`),
		lit(`delete_key(log.attributes, `+ottlString(parsedAttribute)+`)`),
		lit(`delete_key(log.attributes, `+ottlString(ruleAttribute)+`)`),
	)})
	// silent: a line that is not in the declared format (a stack trace, a
	// banner) fails its parse, keeps its generic treatment, and is not worth
	// a collector log line each.
	b.f.Processors[LogsProcessor] = map[string]any{"error_mode": "silent", "log_statements": groups}
	b.f.Logs.Processors = append(b.f.Logs.Processors, LogsProcessor)
	b.f.Logs.Preserve = preserveOperator(parsing, cluster)
}

// epochStatement sets the record's time from f, a number (or numeric string)
// of units of unit nanoseconds. The whole and fractional parts scale apart:
// 1763614801.25 s times 1e9 as one float64 is off by 128ns, beyond the 53
// bits a float64 holds.
func epochStatement(f string, unit int) string {
	whole := fmt.Sprintf("Int(Double(%s))", f)
	return fmt.Sprintf(`set(log.time_unix_nano, %s * %d + Int((Double(%s) - Double(%s)) * %d.0)) where %s != nil`, whole, unit, f, whole, unit, f)
}

func unroutable(cluster bool) string {
	if cluster {
		return "a cluster target's lines are matched by its pods' labels, and it names an address, not a selector"
	}
	return "a host target's lines are matched by its log file, and an Observability address has none"
}

// fileStem is the name of t's log file under .forge/logs/<env>/ on a host:
// the declaring workload's or engine's name. Empty for an Observability
// target, which has no file.
func fileStem(t telemetry.Target) string {
	_, name, ok := strings.Cut(t.Source, ":")
	if !ok {
		return ""
	}
	return name
}

// podCondition matches the resource of a record from one of t's pods, in
// the resource context: its namespace (when t names any) and every
// selector label, as K8sAttributesLabels extracts them.
func podCondition(t telemetry.Target) string {
	var parts []string
	if len(t.Namespaces) > 0 {
		var ns []string
		for _, n := range t.Namespaces {
			ns = append(ns, fmt.Sprintf(`attributes["k8s.namespace.name"] == %s`, ottlString(n)))
		}
		parts = append(parts, "("+strings.Join(ns, " or ")+")")
	}
	for _, k := range sortedKeys(t.Selector) {
		parts = append(parts, fmt.Sprintf(`attributes[%s] == %s`, ottlString(LabelAttributePrefix+k), ottlString(t.Selector[k])))
	}
	return strings.Join(parts, " and ")
}

// logCondition matches t's records in the log context, after
// IdentityProcessor stamped them.
func logCondition(t telemetry.Target, cluster bool) string {
	c := fmt.Sprintf(`resource.attributes["service.name"] == %s`, ottlString(t.ServiceName))
	if cluster && t.Container != "" {
		c += fmt.Sprintf(` and resource.attributes["k8s.container.name"] == %s`, ottlString(t.Container))
	}
	return c
}

// preserveOperator copies a candidate line's raw text before the generic
// stage consumes it: on a cluster, the lines of the parsing targets'
// namespaces (every line when one names none); on a host, of their files.
func preserveOperator(parsing []telemetry.Target, cluster bool) map[string]any {
	op := map[string]any{
		"type": "copy", "id": "forge_telemetry_original",
		"from": "body", "to": `attributes["` + OriginalAttribute + `"]`,
	}
	var values []string
	for _, t := range parsing {
		if cluster {
			if len(t.Namespaces) == 0 {
				return op
			}
			values = append(values, t.Namespaces...)
		} else {
			values = append(values, fileStem(t))
		}
	}
	slices.Sort(values)
	values = slices.Compact(values)
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = strconv.Quote(v)
	}
	field := `resource["service.name"]`
	if cluster {
		field = `resource["k8s.namespace.name"]`
	}
	op["if"] = lit(field + " in [" + strings.Join(quoted, ", ") + "]")
	return op
}

// logStatements are the OTTL statements (log context) that parse one
// target's lines as l declares. The line is read from OriginalAttribute,
// filled from the body when the Preserve operator did not run; a parsed
// format's fields are kept whole in parsedAttribute, so the declared fields
// are read as written even where the generic stage already moved them.
func logStatements(l telemetry.LogParse) []string {
	orig := `log.attributes[` + ottlString(OriginalAttribute) + `]`
	parsed := `log.attributes[` + ottlString(parsedAttribute) + `]`
	out := []string{fmt.Sprintf(`set(%s, log.body) where %s == nil`, orig, orig)}

	base := "log.attributes"
	switch l.Format {
	case "json", "auto":
		// The generic stage already put a JSON line's fields in attributes.
		out = append(out, fmt.Sprintf(`set(%s, ParseJSON(%s)) where IsMatch(%s, %s)`, parsed, orig, orig, ottlString(`^\s*\{`)))
		base = parsed
	case "logfmt":
		out = append(out,
			fmt.Sprintf(`set(%s, ParseKeyValue(%s))`, parsed, orig),
			fmt.Sprintf(`merge_maps(log.attributes, %s, "upsert") where %s != nil`, parsed, parsed))
		base = parsed
	case "regex":
		out = append(out,
			fmt.Sprintf(`set(%s, ExtractPatterns(%s, %s))`, parsed, orig, ottlString(l.Regex)),
			fmt.Sprintf(`merge_maps(log.attributes, %s, "upsert") where %s != nil`, parsed, parsed))
		base = parsed
	}
	field := func(p string) string {
		segs, err := fieldPath(p)
		if err != nil {
			// Validated by the KCL schema; a path that is not one reads as
			// a single key.
			segs = []string{p}
		}
		return accessor(base, segs)
	}
	var consumed []string

	if ts := l.Timestamp; ts != nil {
		f := field(ts.Field)
		switch ts.Layout {
		case "rfc3339":
			// OTTL's Time takes strptime only, which has no "Z or an offset"
			// directive: one statement per form. A fractional second after
			// %S is accepted without a directive.
			out = append(out,
				fmt.Sprintf(`set(log.time, Time(%s, "%%Y-%%m-%%dT%%H:%%M:%%SZ", "UTC")) where %s != nil and IsMatch(%s, "Z$")`, f, f, f),
				fmt.Sprintf(`set(log.time, Time(%s, "%%Y-%%m-%%dT%%H:%%M:%%S%%j")) where %s != nil and not IsMatch(%s, "Z$")`, f, f, f))
		case "epoch_s":
			out = append(out, epochStatement(f, 1000000000))
		case "epoch_ms":
			out = append(out, epochStatement(f, 1000000))
		case "epoch_ns":
			out = append(out, fmt.Sprintf(`set(log.time_unix_nano, Int(%s)) where %s != nil`, f, f))
		default:
			out = append(out, fmt.Sprintf(`set(log.time, Time(%s, %s)) where %s != nil`, f, ottlString(ts.Layout), f))
		}
		consumed = append(consumed, ts.Field)
	}

	if s := l.Severity; s != nil {
		f := field(s.Field)
		out = append(out,
			fmt.Sprintf(`set(log.severity_text, String(%s)) where %s != nil`, f, f),
			fmt.Sprintf(`set(log.severity_number, SEVERITY_NUMBER_UNSPECIFIED) where %s != nil`, f))
		mapping := s.Mapping
		if len(mapping) == 0 {
			mapping = defaultSeverityMapping
		}
		for _, sev := range telemetry.Severities {
			values := mapping[sev]
			if len(values) == 0 {
				continue
			}
			quoted := make([]string, len(values))
			for i, v := range values {
				quoted[i] = regexp.QuoteMeta(v)
			}
			re := "(?i)^(?:" + strings.Join(quoted, "|") + ")$"
			// Match the same string conversion we expose as severity_text,
			// including numeric JSON fields such as cert-manager's verbosity.
			out = append(out, fmt.Sprintf(`set(log.severity_number, %s) where %s != nil and IsMatch(String(%s), %s)`, severityEnums[sev], f, f, ottlString(re)))
		}
		consumed = append(consumed, s.Field)
	}

	if l.Body != "" {
		f := field(l.Body)
		out = append(out, fmt.Sprintf(`set(log.body, %s) where %s != nil`, f, f))
		consumed = append(consumed, l.Body)
	}
	if l.TraceID != "" {
		f := field(l.TraceID)
		out = append(out, fmt.Sprintf(`set(log.trace_id.string, %s) where %s != nil`, f, f))
		consumed = append(consumed, l.TraceID)
	}
	if l.SpanID != "" {
		f := field(l.SpanID)
		out = append(out, fmt.Sprintf(`set(log.span_id.string, %s) where %s != nil`, f, f))
		consumed = append(consumed, l.SpanID)
	}

	// Rules: the first that matches sets the severity. The rule's index is
	// marked first, so the severity it then sets cannot make a later rule's
	// from_severity match.
	marker := `log.attributes[` + ottlString(ruleAttribute) + `]`
	for i, r := range l.Rules {
		conds := []string{marker + " == nil"}
		if r.BodyMatch != "" {
			conds = append(conds, fmt.Sprintf(`IsMatch(log.body, %s)`, ottlString(r.BodyMatch)))
		}
		if r.Field != "" && r.ValueMatch != "" {
			f := field(r.Field)
			conds = append(conds, fmt.Sprintf(`%s != nil and IsMatch(%s, %s)`, f, f, ottlString(r.ValueMatch)))
		}
		if r.FromSeverity != "" {
			conds = append(conds, severityRange(r.FromSeverity))
		}
		out = append(out,
			fmt.Sprintf(`set(%s, %d) where %s`, marker, i, strings.Join(conds, " and ")),
			fmt.Sprintf(`set(log.severity_number, %s) where %s == %d`, severityEnums[r.Severity], marker, i),
			fmt.Sprintf(`set(log.severity_text, %s) where %s == %d`, ottlString(strings.ToUpper(r.Severity)), marker, i))
	}

	// The fields promoted onto the record, and the declared drops, leave the
	// attributes.
	seen := map[string]bool{}
	for _, p := range append(consumed, l.Drop...) {
		if seen[p] {
			continue
		}
		seen[p] = true
		segs, err := fieldPath(p)
		if err != nil {
			segs = []string{p}
		}
		out = append(out, fmt.Sprintf(`delete_key(%s, %s)`, accessor("log.attributes", segs[:len(segs)-1]), ottlString(segs[len(segs)-1])))
	}
	return out
}
