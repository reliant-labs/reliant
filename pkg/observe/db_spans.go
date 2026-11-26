package observe

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// DBSpans selects which database client spans Setup exports. pkg/observe/dbtrace
// opens one span per statement, plus BEGIN and COMMIT for every transaction, so
// a process that talks to its database a lot exports mostly fast query spans:
// they tell you a query ran, rarely where the time went, and they are most of
// what the trace store keeps. The zero value exports every one, as before.
//
// The filter acts when a span ends, so it can judge duration and outcome. A
// failed statement (span status Error) is always exported, whatever the policy.
// Database spans are leaves (dbtrace opens no child spans), so dropping one never
// orphans another span. Only spans that carry db.system.name and are of kind
// CLIENT are considered; every other span is exported unchanged.
//
// The FORGE_OBSERVE_DB_SPAN_MIN_DURATION and FORGE_OBSERVE_DB_SPAN_OMIT_TX
// variables win over these fields, so a deploy can change the policy without a
// rebuild. An invalid value is ignored with a warning.
type DBSpans struct {
	// OmitTransactions drops the BEGIN and COMMIT spans of transactions that
	// did not fail. ROLLBACK is a statement span like any other.
	OmitTransactions bool

	// MinDuration exports a database span only when it took at least this
	// long, or failed. Zero exports every one.
	MinDuration time.Duration
}

func (p DBSpans) active() bool { return p.OmitTransactions || p.MinDuration > 0 }

// resolveDBSpans applies the environment over cfg.DBSpans.
func resolveDBSpans(cfg Config) (DBSpans, []error) {
	p := cfg.DBSpans
	var warns []error
	if p.MinDuration < 0 {
		warns = append(warns, fmt.Errorf("observe.Config.DBSpans.MinDuration %s is negative; exporting every database span", p.MinDuration))
		p.MinDuration = 0
	}
	minDur, omitTx := dbSpansEnv()
	if minDur != "" {
		if d, err := time.ParseDuration(minDur); err != nil || d < 0 {
			warns = append(warns, fmt.Errorf("%s=%q: want a non-negative Go duration (\"20ms\"); ignoring it", EnvDBSpanMinDuration, minDur))
		} else {
			p.MinDuration = d
		}
	}
	if omitTx != "" {
		if b, err := strconv.ParseBool(omitTx); err != nil {
			warns = append(warns, fmt.Errorf("%s=%q: want true or false; ignoring it", EnvDBSpanOmitTx, omitTx))
		} else {
			p.OmitTransactions = b
		}
	}
	return p, warns
}

// dbSpanFilter is a SpanProcessor that forwards every span to next except the
// database spans policy drops.
type dbSpanFilter struct {
	next   sdktrace.SpanProcessor
	policy DBSpans
}

func newDBSpanFilter(next sdktrace.SpanProcessor, policy DBSpans) sdktrace.SpanProcessor {
	if !policy.active() {
		return next
	}
	return &dbSpanFilter{next: next, policy: policy}
}

func (f *dbSpanFilter) OnStart(ctx context.Context, s sdktrace.ReadWriteSpan) { f.next.OnStart(ctx, s) }

func (f *dbSpanFilter) OnEnd(s sdktrace.ReadOnlySpan) {
	if f.drop(s) {
		return
	}
	f.next.OnEnd(s)
}

func (f *dbSpanFilter) Shutdown(ctx context.Context) error   { return f.next.Shutdown(ctx) }
func (f *dbSpanFilter) ForceFlush(ctx context.Context) error { return f.next.ForceFlush(ctx) }

func (f *dbSpanFilter) drop(s sdktrace.ReadOnlySpan) bool {
	if s.SpanKind() != trace.SpanKindClient || s.Status().Code == codes.Error || !isDBSpan(s.Attributes()) {
		return false
	}
	if f.policy.OmitTransactions {
		switch s.Name() {
		case "BEGIN", "COMMIT":
			return true
		}
	}
	return f.policy.MinDuration > 0 && s.EndTime().Sub(s.StartTime()) < f.policy.MinDuration
}

const dbSystemNameKey = attribute.Key("db.system.name")

func isDBSpan(attrs []attribute.KeyValue) bool {
	for _, kv := range attrs {
		if kv.Key == dbSystemNameKey {
			return true
		}
	}
	return false
}
