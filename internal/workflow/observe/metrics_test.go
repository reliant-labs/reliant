// Copyright (c) 2026 Reliant Labs
package wfobserve

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

func newTestRecorder(t *testing.T) (*Recorder, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	return NewRecorder(mp.Meter(telemetry.ObserveMeterName)), reader
}

// collect returns every data point's attribute set, as a string, and value,
// per metric name.
func collect(t *testing.T, reader *sdkmetric.ManualReader) map[string]map[string]float64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	out := map[string]map[string]float64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			points := map[string]float64{}
			switch d := m.Data.(type) {
			case metricdata.Sum[float64]:
				for _, p := range d.DataPoints {
					points[p.Attributes.Encoded(attribute.DefaultEncoder())] = p.Value
				}
			case metricdata.Sum[int64]:
				for _, p := range d.DataPoints {
					points[p.Attributes.Encoded(attribute.DefaultEncoder())] = float64(p.Value)
				}
			case metricdata.Gauge[float64]:
				for _, p := range d.DataPoints {
					points[p.Attributes.Encoded(attribute.DefaultEncoder())] = p.Value
				}
			case metricdata.Histogram[float64]:
				for _, p := range d.DataPoints {
					points[p.Attributes.Encoded(attribute.DefaultEncoder())] = p.Sum
				}
			}
			out[m.Name] = points
		}
	}
	return out
}

var planBase = MetricBase{WorkflowName: "rc-train", NodeID: "plan", NodeType: model.NodeTypeCompute}

// A metric is labelled with the workflow and node and at most the author's
// keys, never an id of the org, user, chat or run; a value that is not a
// bool, an integer or a code is "other"; an author key cannot replace the
// platform's.
func TestRecorder_LabelsByConstruction(t *testing.T) {
	t.Parallel()
	r, reader := newTestRecorder(t)
	ctx := context.Background()
	r.Record(ctx, planBase, []MetricPoint{
		{Name: "rc_train.ticks", Kind: model.ObserveKindCounter, Value: 1, Attributes: map[string]interface{}{
			"decision": "no_cut", "detail": "free text here", "forge.ai.node.id": "spoofed",
		}},
		{Name: "node.duration", Kind: model.ObserveKindHistogram, Unit: "ms", Value: 42},
		{Name: "queue.depth", Kind: model.ObserveKindGauge, Value: 7},
	})
	got := collect(t, reader)
	assert.Equal(t, map[string]float64{
		"decision=no_cut,detail=other,forge.ai.node.id=plan,forge.ai.node.type=compute,forge.ai.workflow.name=rc-train": 1,
	}, got["reliant.observed.rc_train.ticks"])
	assert.Equal(t, map[string]float64{
		"forge.ai.node.id=plan,forge.ai.node.type=compute,forge.ai.workflow.name=rc-train": 42,
	}, got["reliant.observed.node.duration"])
	assert.Equal(t, map[string]float64{
		"forge.ai.node.id=plan,forge.ai.node.type=compute,forge.ai.workflow.name=rc-train": 7,
	}, got["reliant.observed.queue.depth"])
}

// Past its series cap a metric records new attribute sets as one overflow
// series, and counts each such point; a set it already has keeps recording.
func TestRecorder_CollapsesNewSeriesPastTheCap(t *testing.T) {
	t.Parallel()
	r, reader := newTestRecorder(t)
	ctx := context.Background()
	point := func(v string) MetricPoint {
		return MetricPoint{Name: "pr.landed", Kind: model.ObserveKindCounter, Value: 1, Attributes: map[string]interface{}{"pr": v}}
	}
	for i := 0; i < MaxSeriesPerMetric+3; i++ {
		r.Record(ctx, planBase, []MetricPoint{point(fmt.Sprintf("pr%d", i))})
	}
	r.Record(ctx, planBase, []MetricPoint{point("pr0")}) // a known set still records as itself

	got := collect(t, reader)
	series := got["reliant.observed.pr.landed"]
	assert.Len(t, series, MaxSeriesPerMetric+1, "the cap's sets plus the overflow set")
	assert.InDelta(t, 3, series["overflow=true"], 0)
	assert.InDelta(t, 2, series["forge.ai.node.id=plan,forge.ai.node.type=compute,forge.ai.workflow.name=rc-train,pr=pr0"], 0)
	assert.Equal(t, map[string]float64{"metric=reliant.observed.pr.landed": 3}, got[telemetry.MetricObserveOverflow])
}

// The process-wide cap holds across metrics.
func TestRecorder_ProcessCap(t *testing.T) {
	t.Parallel()
	r, reader := newTestRecorder(t)
	r.perProcess = 2
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		r.Record(ctx, planBase, []MetricPoint{{Name: fmt.Sprintf("m%d", i), Kind: model.ObserveKindCounter, Value: 1}})
	}
	got := collect(t, reader)
	assert.Equal(t, map[string]float64{"overflow=true": 1}, got["reliant.observed.m2"])
}

// One name is one instrument: a second kind for it is refused and counted.
func TestRecorder_RefusesASecondKindForAName(t *testing.T) {
	t.Parallel()
	r, reader := newTestRecorder(t)
	ctx := context.Background()
	r.Record(ctx, planBase, []MetricPoint{{Name: "x", Kind: model.ObserveKindCounter, Value: 1}})
	r.Record(ctx, planBase, []MetricPoint{{Name: "x", Kind: model.ObserveKindGauge, Value: 5}})
	r.CountDropped(ctx, telemetry.ObserveDropRowCap)

	got := collect(t, reader)
	assert.Len(t, got["reliant.observed.x"], 1)
	assert.Equal(t, map[string]float64{"kind=kind_conflict": 1}, got[telemetry.MetricObserveErrors])
	assert.Equal(t, map[string]float64{"reason=row_cap": 1}, got[telemetry.MetricObserveDropped])
}
