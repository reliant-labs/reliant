// Copyright (c) 2025 Reliant Labs

package alerting

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ReservedPrefix is the label-name prefix only the RuleSource may set, through
// Rule.Identity. A column or label a rule author writes with this prefix is
// dropped, in any letter case.
const ReservedPrefix = "reliant_"

// ValueColumn is the column whose number is an instance's sample.
const ValueColumn = "value"

const (
	maxLabelValueBytes = 1024
	maxLabels          = 64
)

var (
	// ErrBadResult marks a result the evaluator could not turn into instances.
	ErrBadResult = errors.New("alerting: unusable result")

	invalidLabelChar = regexp.MustCompile(`[^a-zA-Z0-9_]`)
	labelRef         = regexp.MustCompile(`\{\{\s*\$labels\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)
	valueRef         = regexp.MustCompile(`\{\{\s*\$value\s*\}\}`)
)

// series is one row, resolved to final labels.
type series struct {
	fp          string
	labels      map[string]string
	annotations map[string]string
	value       float64
}

// isReserved reports whether a label name is in the identity namespace.
func isReserved(name string) bool {
	return strings.HasPrefix(strings.ToLower(name), ReservedPrefix)
}

// sanitizeLabelName makes a column name a legal label name, or returns "" when
// nothing usable is left. Alertmanager rejects a whole batch over one bad name,
// so a name is repaired here rather than sent as it is.
func sanitizeLabelName(name string) string {
	name = invalidLabelChar.ReplaceAllString(name, "_")
	if name == "" || strings.Trim(name, "_") == "" {
		return ""
	}
	if name[0] >= '0' && name[0] <= '9' {
		name = "_" + name
	}
	return name
}

func truncate(s string) string {
	if len(s) <= maxLabelValueBytes {
		return s
	}
	cut := maxLabelValueBytes
	for cut > 0 && s[cut]&0xC0 == 0x80 { // do not split a UTF-8 sequence
		cut--
	}
	return s[:cut]
}

// buildSeries turns query rows into instances' labels and samples.
//
// Precedence, lowest to highest: the row's non-value columns, the rule's own
// labels, `alertname`, then Rule.Identity. Identity goes on last and no earlier
// layer may contain a reserved name, so nothing a rule author or a column can
// produce overrides it.
func buildSeries(rule Rule, rows []Row) ([]series, error) {
	identity := make(map[string]string, len(rule.Identity))
	for k, v := range rule.Identity {
		if k == "" || sanitizeLabelName(k) != k {
			return nil, fmt.Errorf("%w: identity label %q is not a valid label name", ErrBadResult, k)
		}
		identity[k] = v
	}
	ruleLabels := map[string]string{}
	for _, k := range sortedKeys(rule.Labels) {
		if n := sanitizeLabelName(k); n != "" && !isReserved(n) {
			ruleLabels[n] = truncate(rule.Labels[k])
		}
	}

	out := make([]series, 0, len(rows))
	seen := make(map[string]int, len(rows))
	for i, row := range rows {
		labels := map[string]string{}
		value := 1.0
		for _, col := range sortedRowKeys(row) {
			if col == ValueColumn {
				v, err := toFloat(row[col])
				if err != nil {
					return nil, fmt.Errorf("%w: row %d: column %q: %v", ErrBadResult, i, ValueColumn, err)
				}
				value = v
				continue
			}
			n := sanitizeLabelName(col)
			if n == "" || isReserved(n) {
				continue
			}
			labels[n] = truncate(toLabel(row[col]))
		}
		for k, v := range ruleLabels {
			labels[k] = v
		}
		labels["alertname"] = rule.Name
		for k, v := range identity {
			labels[k] = v
		}
		for k, v := range labels {
			if v == "" {
				delete(labels, k)
			}
		}
		if len(labels) > maxLabels {
			return nil, fmt.Errorf("%w: row %d has %d labels, the limit is %d", ErrBadResult, i, len(labels), maxLabels)
		}
		fp := fingerprint(rule.RuleKey, labels)
		if j, dup := seen[fp]; dup {
			return nil, fmt.Errorf("%w: rows %d and %d have the same labels; group by the columns that tell them apart", ErrBadResult, j, i)
		}
		seen[fp] = i
		out = append(out, series{
			fp:          fp,
			labels:      labels,
			annotations: expandAnnotations(rule.Annotations, labels, value),
			value:       value,
		})
	}
	return out, nil
}

// fingerprint identifies a label set within a rule.
func fingerprint(k RuleKey, labels map[string]string) string {
	h := sha256.New()
	for _, p := range []string{k.Owner, k.Env, k.Name} {
		h.Write([]byte(p))
		h.Write([]byte{0xff})
	}
	for _, name := range sortedKeys(labels) {
		h.Write([]byte(name))
		h.Write([]byte{'='})
		h.Write([]byte(labels[name]))
		h.Write([]byte{0xff})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// expandAnnotations substitutes {{ $labels.name }} and {{ $value }}. That is
// the whole template language: annotations are text for a human, and nothing a
// rule says is evaluated.
func expandAnnotations(in map[string]string, labels map[string]string, value float64) map[string]string {
	if len(in) == 0 {
		return nil
	}
	val := strconv.FormatFloat(value, 'g', 6, 64)
	out := make(map[string]string, len(in))
	for k, v := range in {
		v = labelRef.ReplaceAllStringFunc(v, func(m string) string {
			return labels[labelRef.FindStringSubmatch(m)[1]]
		})
		v = valueRef.ReplaceAllLiteralString(v, val)
		out[k] = truncate(v)
	}
	return out
}

func toLabel(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case time.Time:
		return x.UTC().Format(time.RFC3339)
	case fmt.Stringer:
		return x.String()
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return ""
		}
		return toLabel(rv.Elem().Interface())
	}
	return fmt.Sprint(v)
}

func toFloat(v any) (float64, error) {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return 0, errors.New("is NULL")
	}
	var f float64
	switch rv.Kind() {
	case reflect.Pointer:
		if rv.IsNil() {
			return 0, errors.New("is NULL")
		}
		return toFloat(rv.Elem().Interface())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		f = float64(rv.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		f = float64(rv.Uint())
	case reflect.Float32, reflect.Float64:
		f = rv.Float()
	case reflect.Bool:
		if rv.Bool() {
			f = 1
		}
	default:
		// decimals and other numeric wrappers print as a number.
		p, err := strconv.ParseFloat(strings.TrimSpace(fmt.Sprint(v)), 64)
		if err != nil {
			return 0, fmt.Errorf("%T is not a number", v)
		}
		f = p
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, errors.New("is NaN or infinite")
	}
	return f, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedRowKeys(m Row) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
