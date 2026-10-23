package contractkit

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/embedded"
	"go.opentelemetry.io/otel/metric/noop"
)

// fakeMeter wraps the noop meter and records the names + descriptions
// of every instrument it creates. The actual instruments delegate to
// noop so our helpers can call .Add / .Record without panicking.
type fakeMeter struct {
	embedded.Meter
	noop  metric.Meter
	names []string
}

func newFakeMeter() *fakeMeter {
	return &fakeMeter{noop: noop.NewMeterProvider().Meter("test")}
}

func (m *fakeMeter) Int64Counter(name string, opts ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	m.names = append(m.names, name)
	return m.noop.Int64Counter(name, opts...)
}
func (m *fakeMeter) Float64Histogram(name string, opts ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	m.names = append(m.names, name)
	return m.noop.Float64Histogram(name, opts...)
}
func (m *fakeMeter) Int64UpDownCounter(name string, opts ...metric.Int64UpDownCounterOption) (metric.Int64UpDownCounter, error) {
	return m.noop.Int64UpDownCounter(name, opts...)
}
func (m *fakeMeter) Int64Histogram(name string, opts ...metric.Int64HistogramOption) (metric.Int64Histogram, error) {
	return m.noop.Int64Histogram(name, opts...)
}
func (m *fakeMeter) Int64Gauge(name string, opts ...metric.Int64GaugeOption) (metric.Int64Gauge, error) {
	return m.noop.Int64Gauge(name, opts...)
}
func (m *fakeMeter) Float64Counter(name string, opts ...metric.Float64CounterOption) (metric.Float64Counter, error) {
	return m.noop.Float64Counter(name, opts...)
}
func (m *fakeMeter) Float64UpDownCounter(name string, opts ...metric.Float64UpDownCounterOption) (metric.Float64UpDownCounter, error) {
	return m.noop.Float64UpDownCounter(name, opts...)
}
func (m *fakeMeter) Float64Gauge(name string, opts ...metric.Float64GaugeOption) (metric.Float64Gauge, error) {
	return m.noop.Float64Gauge(name, opts...)
}
func (m *fakeMeter) Int64ObservableCounter(name string, opts ...metric.Int64ObservableCounterOption) (metric.Int64ObservableCounter, error) {
	return m.noop.Int64ObservableCounter(name, opts...)
}
func (m *fakeMeter) Int64ObservableUpDownCounter(name string, opts ...metric.Int64ObservableUpDownCounterOption) (metric.Int64ObservableUpDownCounter, error) {
	return m.noop.Int64ObservableUpDownCounter(name, opts...)
}
func (m *fakeMeter) Int64ObservableGauge(name string, opts ...metric.Int64ObservableGaugeOption) (metric.Int64ObservableGauge, error) {
	return m.noop.Int64ObservableGauge(name, opts...)
}
func (m *fakeMeter) Float64ObservableCounter(name string, opts ...metric.Float64ObservableCounterOption) (metric.Float64ObservableCounter, error) {
	return m.noop.Float64ObservableCounter(name, opts...)
}
func (m *fakeMeter) Float64ObservableUpDownCounter(name string, opts ...metric.Float64ObservableUpDownCounterOption) (metric.Float64ObservableUpDownCounter, error) {
	return m.noop.Float64ObservableUpDownCounter(name, opts...)
}
func (m *fakeMeter) Float64ObservableGauge(name string, opts ...metric.Float64ObservableGaugeOption) (metric.Float64ObservableGauge, error) {
	return m.noop.Float64ObservableGauge(name, opts...)
}
func (m *fakeMeter) RegisterCallback(metric.Callback, ...metric.Observable) (metric.Registration, error) {
	return nil, nil
}

// TestNewMetrics_FingerprintLocked verifies the canonical instrument
// names: "<package>.calls", "<package>.errors", "<package>.duration".
func TestNewMetrics_FingerprintLocked(t *testing.T) {
	m := newFakeMeter()
	NewMetrics(m, "emailer")
	want := []string{"emailer.calls", "emailer.errors", "emailer.duration"}
	if len(m.names) != len(want) {
		t.Fatalf("instrument count = %d, want %d (%v)", len(m.names), len(want), m.names)
	}
	for i, w := range want {
		if m.names[i] != w {
			t.Errorf("instrument[%d] = %q, want %q", i, m.names[i], w)
		}
	}
}

func TestMetrics_RecordCall_DoesNotPanic(t *testing.T) {
	m := NewMetrics(newFakeMeter(), "p")
	m.RecordCall(context.Background(), "M")
}

func TestMetrics_RecordCall_NilContextSafe(t *testing.T) {
	m := NewMetrics(newFakeMeter(), "p")
	m.RecordCall(nil, "M")
}

func TestMetrics_RecordDuration(t *testing.T) {
	m := NewMetrics(newFakeMeter(), "p")
	m.RecordDuration(context.Background(), "M", time.Now())
	m.RecordDuration(nil, "M", time.Now())
}

func TestMetrics_RecordError_NilNoOp(t *testing.T) {
	m := NewMetrics(newFakeMeter(), "p")
	// Just verify no panic with nil err.
	m.RecordError(context.Background(), "M", nil)
	m.RecordError(context.Background(), "M", errors.New("e"))
	m.RecordError(nil, "M", errors.New("e"))
}

func TestMetrics_NilReceiverSafe(t *testing.T) {
	var m *Metrics
	m.RecordCall(context.Background(), "M")
	m.RecordDuration(context.Background(), "M", time.Now())
	m.RecordError(context.Background(), "M", errors.New("x"))
}
