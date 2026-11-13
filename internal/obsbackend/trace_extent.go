package obsbackend

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

// TraceExtent is when one trace ran, read from its spans: how many were found
// (0 when none is in the range; a control plane's read stops at
// obsquery.MaxSpansPerTrace), the first one's start and the last one's end.
type TraceExtent struct {
	Spans      int
	Start, End time.Time
}

// TraceExtent reads when a trace ran, in one aggregate over its spans.
func (l Local) TraceExtent(ctx context.Context, req TraceRequest) (TraceExtent, error) {
	sc := obsquery.Scope{Database: obsquery.LocalDatabase, Local: true, Start: req.Range.From, End: req.Range.To}
	l.Instances.apply(&sc)
	st, err := obsquery.TraceExtent{Scope: sc, TraceID: req.TraceID}.Compile()
	if err != nil {
		return TraceExtent{}, compileErr(err)
	}
	r, err := l.run(ctx, st, DefaultLimits())
	if err != nil {
		return TraceExtent{}, err
	}
	if len(r.Rows) == 0 {
		return TraceExtent{}, nil
	}
	row := r.Rows[0]
	if len(row) < 3 {
		return TraceExtent{}, Undetermined("the local ClickStack returned %d columns for a trace's extent, want 3", len(row))
	}
	ext := TraceExtent{Spans: int(AsInt(row[0]))}
	if ext.Spans == 0 {
		return TraceExtent{}, nil
	}
	ext.Start, ext.End = AsTime(row[1]), time.Unix(0, AsInt(row[2])).UTC()
	return ext, nil
}

// TraceExtent reads when a trace ran from the control plane's GetTrace, which
// has no aggregate of its own: the extent of the spans it returns. They are
// the trace's first spans by start time, so the start is exact; past
// obsquery.MaxSpansPerTrace the end is the latest the read saw (a request's
// root span, read first, usually ends last).
func (c Cloud) TraceExtent(ctx context.Context, req TraceRequest) (TraceExtent, error) {
	id, err := obsquery.NormalizeTraceID(req.TraceID)
	if err != nil {
		return TraceExtent{}, compileErr(err)
	}
	in := struct {
		Scope   wireScope `json:"scope"`
		TraceID string    `json:"traceId"`
	}{c.scope(req.Narrow), id}
	var out struct {
		Spans []wireSpan `json:"spans"`
	}
	if err := c.call(ctx, procGetTrace, in, &out); err != nil {
		return TraceExtent{}, err
	}
	var ext TraceExtent
	for _, s := range out.Spans {
		start := s.StartTime.UTC()
		end := start.Add(time.Duration(s.DurationNs))
		if ext.Spans == 0 || start.Before(ext.Start) {
			ext.Start = start
		}
		if end.After(ext.End) {
			ext.End = end
		}
		ext.Spans++
	}
	return ext, nil
}
