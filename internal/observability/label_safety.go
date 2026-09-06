// Copyright (c) 2025 Reliant Labs
//
// forge:exclude-contract
package observability

import (
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
)

// Prometheus PANICS on a label value that is not valid UTF-8 — it does not
// return an error the caller could log and move past. Metrics are recorded on
// the error paths of NATS, the DB, the LLM drivers and the slog handler, so
// the panic lands at the exact moment something else has already gone wrong,
// and it takes the process with it.
//
// A caller cannot reasonably be trusted to prevent that. Label values are
// derived from provider names, gRPC procedures, subjects and log messages —
// data that mostly looks safe, so the unsafe case is invisible until it fires.
// It fired once already: the slog handler clamped a message with a byte slice
// and cut an em dash in half, which killed the temporal worker every time a
// heartbeat cancelled an LLM stream.
//
// So instrumentation is coerced here, at the one boundary every metric passes
// through, rather than at each of the ~130 call sites. The vecs below wrap the
// client-library types and sanitize before delegating; call sites are
// unchanged and cannot opt out by forgetting.
//
// This guarantees only VALIDITY. Cardinality is still the call site's problem:
// a label value must be bounded (a status, a procedure, an enum), and no guard
// here can make an unbounded one safe to record.

// safeCounterVec is a *prometheus.CounterVec that coerces label values to
// valid UTF-8. The embedded pointer supplies Describe/Collect/Reset, so it
// registers and behaves exactly like the type it wraps.
type safeCounterVec struct{ *prometheus.CounterVec }

func newCounterVec(opts prometheus.CounterOpts, labelNames []string) safeCounterVec {
	return safeCounterVec{prometheus.NewCounterVec(opts, labelNames)}
}

func (v safeCounterVec) WithLabelValues(values ...string) prometheus.Counter {
	return v.CounterVec.WithLabelValues(validUTF8Values(values)...)
}

func (v safeCounterVec) With(labels prometheus.Labels) prometheus.Counter {
	return v.CounterVec.With(validUTF8Labels(labels))
}

// safeHistogramVec is the histogram twin of safeCounterVec.
type safeHistogramVec struct{ *prometheus.HistogramVec }

func newHistogramVec(opts prometheus.HistogramOpts, labelNames []string) safeHistogramVec {
	return safeHistogramVec{prometheus.NewHistogramVec(opts, labelNames)}
}

func (v safeHistogramVec) WithLabelValues(values ...string) prometheus.Observer {
	return v.HistogramVec.WithLabelValues(validUTF8Values(values)...)
}

func (v safeHistogramVec) With(labels prometheus.Labels) prometheus.Observer {
	return v.HistogramVec.With(validUTF8Labels(labels))
}

// validUTF8Values returns values with any malformed bytes stripped. This runs
// on every metric observation — including per-RPC and per-NATS-message paths —
// so the all-valid case, which is all of them today, allocates nothing and
// returns the caller's slice untouched.
func validUTF8Values(values []string) []string {
	var sanitized []string
	for i, value := range values {
		if utf8.ValidString(value) {
			continue
		}
		if sanitized == nil {
			sanitized = slices.Clone(values)
		}
		sanitized[i] = strings.ToValidUTF8(value, "")
	}
	if sanitized == nil {
		return values
	}
	return sanitized
}

// validUTF8Labels is validUTF8Values for the map form. Label NAMES are
// compile-time constants in this package, so only values are coerced.
func validUTF8Labels(labels prometheus.Labels) prometheus.Labels {
	var sanitized prometheus.Labels
	for name, value := range labels {
		if utf8.ValidString(value) {
			continue
		}
		if sanitized == nil {
			sanitized = maps.Clone(labels)
		}
		sanitized[name] = strings.ToValidUTF8(value, "")
	}
	if sanitized == nil {
		return labels
	}
	return sanitized
}
