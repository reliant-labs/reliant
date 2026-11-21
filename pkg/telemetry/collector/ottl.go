package collector

import (
	"fmt"
	"strings"
)

// ottlString is s as an OTTL string literal.
func ottlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// lit escapes s for the collector's config resolver, which reads `${...}`
// as a reference and `$$` as one literal `$`: every string Build renders from
// a declaration (a regex, a path, an OTTL statement) goes through it, so a
// `$` in a user's pattern reaches the component as written.
func lit(s string) string { return strings.ReplaceAll(s, "$", "$$") }

func lits(v []string) []string {
	out := make([]string, len(v))
	for i, s := range v {
		out[i] = lit(s)
	}
	return out
}

// fieldPath splits a LogParse field path into its segments: dotted, with a
// segment holding a dot (or another character outside [A-Za-z0-9_$@-])
// written ["quoted"]. "record.error_severity" is [record error_severity];
// `["k8s.pod"].name` is [k8s.pod name].
func fieldPath(p string) ([]string, error) {
	var segs []string
	for i := 0; i < len(p); {
		if strings.HasPrefix(p[i:], `["`) {
			end := strings.Index(p[i+2:], `"]`)
			if end < 0 {
				return nil, fmt.Errorf("field path %q: unterminated [\"...\"]", p)
			}
			segs = append(segs, p[i+2:i+2+end])
			i += 2 + end + 2
		} else {
			end := strings.IndexByte(p[i:], '.')
			if end < 0 {
				end = len(p) - i
			}
			if end == 0 {
				return nil, fmt.Errorf("field path %q: empty segment", p)
			}
			segs = append(segs, p[i:i+end])
			i += end
		}
		if i < len(p) {
			if p[i] != '.' || i == len(p)-1 {
				return nil, fmt.Errorf("field path %q: segments are separated by one dot", p)
			}
			i++
		}
	}
	if len(segs) == 0 {
		return nil, fmt.Errorf("field path %q is empty", p)
	}
	return segs, nil
}

// accessor is base followed by one map index per segment.
func accessor(base string, segs []string) string {
	var b strings.Builder
	b.WriteString(base)
	for _, s := range segs {
		b.WriteString("[" + ottlString(s) + "]")
	}
	return b.String()
}

// severityEnums are the OTTL severity-number enums of each severity, and
// severityText the text a rule writes beside one.
var (
	severityEnums = map[string]string{
		"trace": "SEVERITY_NUMBER_TRACE", "debug": "SEVERITY_NUMBER_DEBUG", "info": "SEVERITY_NUMBER_INFO",
		"warn": "SEVERITY_NUMBER_WARN", "error": "SEVERITY_NUMBER_ERROR", "fatal": "SEVERITY_NUMBER_FATAL",
	}
	// defaultSeverityMapping applies when a LogSeverity maps nothing: the
	// level names every common logger writes.
	defaultSeverityMapping = map[string][]string{
		"trace": {"trace"},
		"debug": {"debug"},
		"info":  {"info", "information"},
		"warn":  {"warn", "warning"},
		"error": {"error", "err"},
		"fatal": {"fatal", "critical", "crit", "panic"},
	}
)

// severityRange is the condition "the record's severity is sev": its number
// is in [SEV, SEV4].
func severityRange(sev string) string {
	e := severityEnums[sev]
	return fmt.Sprintf("log.severity_number >= %s and log.severity_number <= %s4", e, e)
}
