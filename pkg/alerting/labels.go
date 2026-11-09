// Copyright (c) 2025 Reliant Labs

package alerting

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// WhyColumn is the column whose JSON object is an instance's why-fired payload
// (see Why). Like ValueColumn it is not a label, so a payload that changes
// between evaluations (the observed count, the baseline) never changes the
// alert's identity and never re-notifies.
const WhyColumn = "why"

// WhyAnnotation is the annotation the Alertmanager notifier posts the payload
// under, so every receiver (in-app, Autopilot webhook, Slack templates) gets it.
const WhyAnnotation = "why"

// MaxWhyBytes bounds one payload. An oversized payload is a bad result, not a
// truncation: cut JSON is not JSON.
const MaxWhyBytes = 16 << 10

const (
	maxLabelValueBytes = 1024
	maxLabels          = 64
)

var (
	// ErrBadResult marks a result the evaluator could not turn into instances.
	ErrBadResult = errors.New("alerting: unusable result")

	invalidLabelChar = regexp.MustCompile(`[^a-zA-Z0-9_]`)
)

// series is one row, resolved to final labels.
type series struct {
	fp          string
	labels      map[string]string
	annotations map[string]string
	value       float64
	why         json.RawMessage
	// annotationErr names the annotations sent as raw text because their
	// template failed; the evaluator logs it.
	annotationErr error
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

	annotations := compileAnnotations(rule.Annotations)
	out := make([]series, 0, len(rows))
	seen := make(map[string]int, len(rows))
	for i, row := range rows {
		labels := map[string]string{}
		value := 1.0
		var why json.RawMessage
		for _, col := range sortedRowKeys(row) {
			if col == WhyColumn {
				w, err := toWhy(row[col])
				if err != nil {
					return nil, fmt.Errorf("%w: row %d: column %q: %v", ErrBadResult, i, WhyColumn, err)
				}
				why = w
				continue
			}
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
		ann, annErr := annotations.expand(labels, value)
		out = append(out, series{
			fp:            fp,
			labels:        labels,
			annotations:   ann,
			value:         value,
			why:           why,
			annotationErr: annErr,
		})
	}
	return out, nil
}

// toWhy turns a row's why column into a compact JSON object. A string or byte
// slice must already be JSON; anything else (a map, a struct) is marshalled. A
// NULL or empty value is no payload.
func toWhy(v any) (json.RawMessage, error) {
	var raw []byte
	switch x := v.(type) {
	case nil:
		return nil, nil
	case string:
		raw = []byte(x)
	case []byte:
		raw = x
	case json.RawMessage:
		raw = x
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("is not JSON: %v", err)
		}
		raw = b
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil || probe == nil {
		return nil, errors.New("must be a JSON object")
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil, fmt.Errorf("is not JSON: %v", err)
	}
	if buf.Len() > MaxWhyBytes {
		return nil, fmt.Errorf("is %d bytes, the limit is %d", buf.Len(), MaxWhyBytes)
	}
	return json.RawMessage(buf.Bytes()), nil
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
