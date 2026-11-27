// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"errors"
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
	// MaxAnalyticsWindow is the longest window a funnel reads (control
	// plane's QueryPanel funnel cap).
	MaxAnalyticsWindow = 90 * 24 * time.Hour
	// MaxSQLAnalyticsWindow is the longest window the sql templates
	// (event_counts, event_trend, property_breakdown) read: control plane's
	// QueryPanel caps sql panels at 30 days.
	MaxSQLAnalyticsWindow = 30 * 24 * time.Hour
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

// analyticsID is what a project id may be.
var analyticsID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

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
	// MaxSQLAnalyticsWindow long for the sql templates and
	// MaxAnalyticsWindow for a funnel.
	From time.Time
	To   time.Time
	// Limit caps the rows; 0 is DefaultAnalyticsRows, at most
	// MaxAnalyticsRows.
	Limit int
	// Environment names the deploy environment to read ("prod", ...): the
	// Analytics implementation resolves the name to every live environment
	// of the org that carries it (narrowed by ProjectID when set) and reads
	// only their events; a name it cannot resolve is a *ValidationError
	// naming the environments there are. Empty reads every environment of
	// the org, events with no environment included.
	Environment string
	// ProjectID narrows Environment to one project's environments, or,
	// alone, the read to one project. Empty: every project.
	ProjectID string
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
		seen := make(map[string]bool, n)
		for _, e := range q.Events {
			if seen[e] {
				return invalid("events", fmt.Sprintf("a funnel names each step once; %q is twice", e))
			}
			seen[e] = true
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
	if q.Environment != "" && !ValidAnalyticsName(q.Environment) {
		return invalid("environment", "must match "+analyticsName.String())
	}
	if q.ProjectID != "" && !analyticsID.MatchString(q.ProjectID) {
		return invalid("project_id", "must match "+analyticsID.String())
	}
	if !q.From.Before(q.To) {
		return invalid("window", "from must be before to")
	}
	maxWindow, maxWord := MaxSQLAnalyticsWindow, "30 days"
	if q.Template == AnalyticsFunnel {
		maxWindow, maxWord = MaxAnalyticsWindow, "90 days"
	}
	if q.To.Sub(q.From) > maxWindow {
		return invalid("window", "at most "+maxWord+" for "+string(q.Template))
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
	// HiddenGroups is how many groups were dropped because fewer than 3
	// people were in them (k-anonymity, k=3). Analytics fills it in; the
	// Service's re-cap of Rows keeps it as is.
	HiddenGroups int
	// Environment and ProjectID label the result with the scope read, as
	// the query named it (the Service sets them): "" is every environment
	// (or project). EnvironmentIDs are the environments Environment
	// resolved to, as Analytics fills them in; empty when unrestricted.
	Environment    string
	ProjectID      string
	EnvironmentIDs []string
}

// passesThrough reports whether an Analytics error reaches the caller as it
// is: one that wraps a *ValidationError, ErrAnalyticsUnavailable or
// ErrAnalyticsFailed, whose text the Analytics contract holds fixed.
func passesThrough(err error) bool {
	var ve *ValidationError
	return errors.As(err, &ve) || errors.Is(err, ErrAnalyticsUnavailable) || errors.Is(err, ErrAnalyticsFailed)
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
		// The domain's own errors carry fixed, actionable messages (a refused
		// window, name or property is the caller's to fix, not to retry):
		// they pass through, and the Analytics contract holds their text
		// fixed. Anything else, a bare InvalidArgument included, can name
		// hosts and queries: log it, say less.
		if passesThrough(err) {
			return AnalyticsResult{}, err
		}
		s.log.WarnContext(ctx, "gtm: product analytics query failed", "template", string(q.Template), "error", err)
		return AnalyticsResult{}, ErrAnalyticsFailed
	}
	// The domain holds the caps, whatever the client returned.
	res.Template, res.From, res.To = q.Template, q.From, q.To
	res.Environment, res.ProjectID = q.Environment, q.ProjectID
	if len(res.Rows) > q.Limit {
		res.Rows = res.Rows[:q.Limit]
		res.Truncated = true
	}
	return res, nil
}
