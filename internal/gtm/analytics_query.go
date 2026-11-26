// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"fmt"
	"regexp"
	"time"
)

// AnalyticsTemplate names one of the server-built product-analytics queries.
// A caller picks a template and its parameters; it never supplies SQL.
type AnalyticsTemplate string

// AnalyticsTemplate values (plan B.5).
const (
	// AnalyticsEventCounts is the top Limit events by count in the window.
	// Events, when given, restricts the count to those events.
	AnalyticsEventCounts AnalyticsTemplate = "event_counts"
	// AnalyticsEventTrend is the daily count of each of 1 to MaxTrendEvents
	// events.
	AnalyticsEventTrend AnalyticsTemplate = "event_trend"
	// AnalyticsPropertyBreakdown is one event's count grouped by the values of
	// one of its properties, the top Limit values.
	AnalyticsPropertyBreakdown AnalyticsTemplate = "property_breakdown"
	// AnalyticsFunnel is a funnel over MinFunnelSteps to MaxFunnelSteps
	// events, in order.
	AnalyticsFunnel AnalyticsTemplate = "funnel"
)

// Analytics limits.
const (
	// DefaultAnalyticsWindow is the window a query with no From covers.
	DefaultAnalyticsWindow = 7 * 24 * time.Hour
	// MaxAnalyticsWindow is the longest window any template reads.
	MaxAnalyticsWindow = 90 * 24 * time.Hour
	// DefaultAnalyticsRows and MaxAnalyticsRows bound a result's rows.
	DefaultAnalyticsRows = 50
	MaxAnalyticsRows     = 200
	// MaxTrendEvents bounds event_trend's and event_counts' event lists.
	MaxTrendEvents = 10
	// MinFunnelSteps and MaxFunnelSteps bound a funnel.
	MinFunnelSteps = 2
	MaxFunnelSteps = 10
)

// analyticsName is what an event or property name may be. Names are still
// emitted as escaped literals, never interpolated; this keeps them boring.
var analyticsName = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

// ValidAnalyticsName reports whether s may name an event or a property.
func ValidAnalyticsName(s string) bool { return analyticsName.MatchString(s) }

// AnalyticsQuery is one template with its parameters.
type AnalyticsQuery struct {
	Template AnalyticsTemplate
	// Events are the event names the template reads: optional for
	// event_counts (at most MaxTrendEvents), 1 to MaxTrendEvents for
	// event_trend, exactly one for property_breakdown, and the steps in
	// order for a funnel.
	Events []string
	// Property is property_breakdown's property; empty for the others.
	Property string
	// The window, half-open: From <= event time < To. A zero To is now, a
	// zero From is To minus DefaultAnalyticsWindow; at most
	// MaxAnalyticsWindow long.
	From time.Time
	To   time.Time
	// Limit caps the rows; 0 is DefaultAnalyticsRows, at most
	// MaxAnalyticsRows.
	Limit int
}

// Resolve fills the zero window and limit from now, as the Service does
// before Analytics sees a query.
func (q AnalyticsQuery) Resolve(now time.Time) AnalyticsQuery {
	if q.To.IsZero() {
		q.To = now
	}
	if q.From.IsZero() {
		q.From = q.To.Add(-DefaultAnalyticsWindow)
	}
	if q.Limit == 0 {
		q.Limit = DefaultAnalyticsRows
	}
	return q
}

// Validate reports the first thing wrong with a resolved query as a
// *ValidationError.
func (q AnalyticsQuery) Validate() error {
	n := len(q.Events)
	switch q.Template {
	case AnalyticsEventCounts:
		if n > MaxTrendEvents {
			return invalid("events", fmt.Sprintf("at most %d", MaxTrendEvents))
		}
	case AnalyticsEventTrend:
		if n < 1 || n > MaxTrendEvents {
			return invalid("events", fmt.Sprintf("1 to %d events", MaxTrendEvents))
		}
	case AnalyticsPropertyBreakdown:
		if n != 1 {
			return invalid("events", "exactly one event")
		}
		if !ValidAnalyticsName(q.Property) {
			return invalid("property", "must match "+analyticsName.String())
		}
	case AnalyticsFunnel:
		if n < MinFunnelSteps || n > MaxFunnelSteps {
			return invalid("events", fmt.Sprintf("a funnel has %d to %d steps", MinFunnelSteps, MaxFunnelSteps))
		}
	default:
		return invalid("template", fmt.Sprintf("unknown template %q", q.Template))
	}
	if q.Template != AnalyticsPropertyBreakdown && q.Property != "" {
		return invalid("property", "only property_breakdown takes a property")
	}
	for _, e := range q.Events {
		if !ValidAnalyticsName(e) {
			return invalid("events", fmt.Sprintf("%q must match %s", e, analyticsName.String()))
		}
	}
	if !q.From.Before(q.To) {
		return invalid("window", "from must be before to")
	}
	if q.To.Sub(q.From) > MaxAnalyticsWindow {
		return invalid("window", "at most 90 days")
	}
	if q.Limit < 1 || q.Limit > MaxAnalyticsRows {
		return invalid("limit", fmt.Sprintf("1 to %d", MaxAnalyticsRows))
	}
	return nil
}

// AnalyticsRow is one aggregate row. Which fields are set depends on the
// template; none is ever a user or any other identifier.
type AnalyticsRow struct {
	// Event is the event (event_counts, event_trend, property_breakdown) or
	// the step's event (funnel).
	Event string
	// Day is event_trend's UTC day.
	Day time.Time
	// Value is property_breakdown's property value ("" when unset).
	Value string
	// Step is the funnel step, from 1.
	Step int
	// Count is the number of events (or, for a funnel, of the accounts that
	// reached the step).
	Count int64
	// Rate is a funnel step's share of step 1, from 0 to 1.
	Rate float64
}

// AnalyticsResult is a template's answer.
type AnalyticsResult struct {
	Template AnalyticsTemplate
	// From and To are the window read.
	From time.Time
	To   time.Time
	Rows []AnalyticsRow
	// Truncated is true when rows beyond the limit were dropped.
	Truncated bool
}

// ReadProductAnalytics gates, resolves and validates q, then hands it to
// Analytics with the actor's org.
func (s *service) ReadProductAnalytics(ctx context.Context, actor Actor, q AnalyticsQuery) (AnalyticsResult, error) {
	if err := s.admit(ctx, actor); err != nil {
		return AnalyticsResult{}, err
	}
	if s.analytics == nil {
		return AnalyticsResult{}, ErrAnalyticsUnavailable
	}
	q = q.Resolve(s.now())
	if err := q.Validate(); err != nil {
		return AnalyticsResult{}, err
	}
	res, err := s.analytics.Query(ctx, actor.OrgID, q)
	if err != nil {
		return AnalyticsResult{}, fmt.Errorf("product analytics: %w", err)
	}
	return res, nil
}
